// Package addresses reconciles MWAN acquisition and journaled WAN translation aliases.
package addresses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const moduleName = "addresses"

// Config supplies family intent, translations, the observation deadline, and the journal path.
type Config struct {
	Connections     []interfaceintent.Connection
	Providers       map[string]Provider
	ClientIDs       map[string][]byte
	StateFile       string
	LeaseDirectory  string
	NetworkdTimeout time.Duration
}

// Provider supplies translation addresses for a WAN connection.
type Provider struct {
	IPv4 *config.IPv4Translation
	IPv6 *config.IPv6Translation
}

// ModuleConfigName selects the registered address module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies configured and acquired family intent after links are ready.
type Module struct {
	ifmgr.BaseModule
	connections     []interfaceintent.Connection
	providers       map[string]Provider
	clientIDs       map[string][]byte
	reconciler      *netif.OwnedStaticReconciler
	leaseStore      *netif.LeaseRecoveryStore
	clock           clock.Clock
	networkdTimeout time.Duration
	reconcileMu     sync.Mutex
	sessionMu       sync.Mutex
	sessions        map[string]*dhcpSession
	dhcpv6Sessions  map[string]*dhcpv6Session
	nextGeneration  uint64
}

// New validates the address journal before daemon startup.
func New(moduleConfig ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{
		BaseModule:      ifmgr.NewBaseModule(moduleName),
		connections:     nil,
		providers:       nil,
		clientIDs:       nil,
		reconciler:      nil,
		leaseStore:      nil,
		clock:           clock.Real{},
		networkdTimeout: 0,
		reconcileMu:     sync.Mutex{},
		sessionMu:       sync.Mutex{},
		sessions:        make(map[string]*dhcpSession),
		dhcpv6Sessions:  make(map[string]*dhcpv6Session),
		nextGeneration:  0,
	}
	if moduleConfig == nil {
		return module, nil
	}

	settings, ok := moduleConfig.(Config)
	if !ok {
		return nil, fmt.Errorf("addresses: invalid config type %T", moduleConfig)
	}

	module.connections = settings.Connections
	module.providers = settings.Providers
	module.clientIDs = settings.ClientIDs
	module.networkdTimeout = settings.NetworkdTimeout
	if settings.LeaseDirectory != "" {
		store, err := netif.NewLeaseRecoveryStore(filepath.Join(settings.LeaseDirectory, "wan"))
		if err != nil {
			return nil, fmt.Errorf("addresses: lease recovery directory: %w", err)
		}

		module.leaseStore = store
	}

	for _, connection := range settings.Connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.IPv4 == nil ||
			connection.IPv4.DHCPv4 == nil ||
			connection.IPv4.DHCPv4.ClientID == "" {
			continue
		}

		if _, ok := settings.ClientIDs[connection.ID.String()]; !ok {
			return nil, fmt.Errorf(
				"addresses: connection %s has no decoded DHCPv4 client ID",
				connection.ID,
			)
		}
	}

	if settings.StateFile == "" {
		for _, connection := range settings.Connections {
			if networkdMappingProvider(connection, settings.Providers) != nil {
				return nil, fmt.Errorf(
					"addresses: state_file is required for networkd mapped addresses",
				)
			}

			if connection.Owner == interfaceintent.OwnerMWAN &&
				(connection.IPv4 != nil || connection.IPv6 != nil) {
				return nil, fmt.Errorf(
					"addresses: state_file is required for owned address families",
				)
			}
		}

		for _, provider := range settings.Providers {
			if provider.IPv6 != nil && provider.IPv6.Mode == config.TranslationNPTv6 {
				return nil, fmt.Errorf("addresses: state_file is required for NPT edges")
			}
		}

		return module, nil
	}

	if !filepath.IsAbs(settings.StateFile) {
		return nil, fmt.Errorf("addresses: state_file must be absolute")
	}

	reconciler, err := netif.NewOwnedStaticReconciler(settings.StateFile)
	if err != nil {
		slog.Warn("addresses: open ownership state failed", "path", settings.StateFile, "err", err)
		return nil, fmt.Errorf("addresses: open ownership state: %w", err)
	}

	module.reconciler = reconciler

	return module, nil
}

// Init permits scoped NPT journals without an MWAN-owned link store.
func (module *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	module.InitBase(env, "module", moduleName)
	if module.leaseStore != nil {
		active := make(map[string]bool)

		for _, connection := range module.connections {
			if connection.Owner == interfaceintent.OwnerMWAN {
				active[connection.ID.String()] = true
			}
		}

		if err := module.leaseStore.PruneUnconfigured(active); err != nil {
			module.Log.WarnContext(ctx, "addresses: prune saved leases failed", "err", err)
		}
	}

	if module.reconciler == nil {
		return ifmgr.ErrModuleDisabled
	}

	for _, connection := range module.connections {
		if connection.Owner == interfaceintent.OwnerMWAN &&
			(connection.IPv4 != nil || connection.IPv6 != nil) &&
			env.OwnedLinks == nil {
			return fmt.Errorf("addresses: links module is required for owned address families")
		}
	}

	env.OwnedAddresses = &ifmgr.OwnedAddressResults{}
	env.Delegations = netif.NewDHCPv6PDStore()
	env.NPTAddresses = module
	env.TunnelEndpointRoutes = module
	if env.OwnedLinkUp != nil {
		env.OwnedLinkUp.Set(module.wakeDHCPv6)
	}

	if env.VerifyDHCPv4 != nil {
		env.VerifyDHCPv4.Set(module.verifyDHCPv4)
	}

	return nil
}

func (module *Module) verifyDHCPv4(iface string) {
	module.sessionMu.Lock()
	defer module.sessionMu.Unlock()

	for _, session := range module.sessions {
		if session.ready.Name == iface {
			session.client.VerifyLease()
			return
		}
	}
}

// Reconcile applies owned addresses and routes, then removes deleted families.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.reconcileMu.Lock()
	defer module.reconcileMu.Unlock()
	ctx, finish, err := module.Env.BeginForwardingChange(ctx)
	if err != nil {
		return fmt.Errorf("prepare owned address change: %w", err)
	}
	defer finish()
	module.Env.OwnedAddresses.Replace()
	desiredFamilies := make(map[string]map[string]bool)
	var mappingErr error

	for _, connection := range module.connections {
		if connection.Owner == interfaceintent.OwnerNetworkd {
			provider := module.providers[connection.ID.String()]
			if provider.IPv4 != nil && len(provider.IPv4.StaticMappings) != 0 {
				desiredFamilies[connection.ID.String()] = map[string]bool{familyIPv4: true}
				mappingErr = errors.Join(
					mappingErr,
					module.reconcileLegacyMappings(ctx, log, connection, provider.IPv4),
				)
			}

			continue
		}

		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}

		desiredFamilies[connection.ID.String()] = module.reconcileConnection(ctx, log, connection)
	}

	err = errors.Join(mappingErr, module.reconciler.PruneRemoved(desiredFamilies))
	var pending []wanstate.PendingRemoval
	if err != nil {
		var failure *netif.OwnedStaticPruneError
		if errors.As(err, &failure) && module.Env.LiveState != nil {
			result := wanstate.ApplyResult{
				Operation:  "prune-static",
				Dependency: "link",
				Result:     "failed",
				Reason:     failure.Error(),
				At:         time.Time{},
			}
			if _, configured := desiredFamilies[failure.ConnectionID]; configured {
				module.Env.LiveState.SetFamilyApplyResult(
					failure.ConnectionID,
					failure.Family,
					result,
				)
			} else {
				pending = append(
					pending,
					wanstate.PendingRemoval{
						ConnectionID: failure.ConnectionID,
						Name:         failure.LinkName,
						Family:       failure.Family,
						Apply:        result,
					},
				)
			}
		}
	}

	if module.Env.LiveState != nil {
		module.Env.LiveState.ReplacePendingRemovals(pending)
	}

	if err != nil {
		log.WarnContext(ctx, "addresses: reconciliation failed", "err", err)
		return fmt.Errorf("addresses: reconciliation: %w", err)
	}

	return nil
}

func (module *Module) prepareLocalIPv6(
	ctx context.Context,
	log *slog.Logger,
	iface string,
	addresses []interfaceintent.Address,
) error {
	if len(addresses) == 0 || module.Env.PrepareLocalIPv6 == nil {
		return nil
	}

	local := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		local = append(local, address.Prefix.Addr())
	}

	if err := module.Env.PrepareLocalIPv6(ctx, log, iface, local); err != nil {
		log.WarnContext(ctx, "addresses: local IPv6 preparation failed", "iface", iface, "err", err)
		return fmt.Errorf("prepare local IPv6 on %s: %w", iface, err)
	}

	return nil
}

func (module *Module) currentDHCPClient(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
) *dhcpSession {
	id := connection.ID.String()
	wantsDHCP := connection.IPv4 != nil && connection.IPv4.DHCP != nil && *connection.IPv4.DHCP
	ready, ok := module.Env.OwnedLinks.Get(id)
	identity := dhcpv4LinkIdentity(connection, ready, id, wantsDHCP && ok)

	module.sessionMu.Lock()
	previous := module.sessions[id]
	if identity == "" {
		if previous != nil {
			previous.cancel()
			delete(module.sessions, id)
		}

		module.sessionMu.Unlock()
		if !wantsDHCP {
			module.deleteRecoveryLease(ctx, id, netif.LeaseProtocolDHCPv4)
		}

		return nil
	}

	if previous != nil && previous.ready.IfIndex == ready.IfIndex &&
		previous.ready.ActualName == ready.ActualName && previous.identity == identity {
		module.sessionMu.Unlock()
		return previous
	}

	if previous != nil {
		previous.cancel()
		delete(module.sessions, id)
	}

	module.sessionMu.Unlock()
	cached, recoveryRejected := module.loadDHCPv4Recovery(ctx, log, id, ready)
	module.sessionMu.Lock()
	clientContext, cancel := context.WithCancel(ifmgr.WithoutForwardingChange(ctx))
	module.nextGeneration++
	client := netif.StartDHCPClient(
		clientContext,
		log,
		netif.DHCPConfig{
			Iface:           ready.ActualName,
			InitialBackoff:  0,
			MaxBackoff:      0,
			DiscoverTimeout: 0,
			RequestTimeout:  0,
			RenewTimeout:    0,
			ClientID:        slices.Clone(module.clientIDs[id]),
			CachedLease:     cached,
			RestartWindow:   0,
		},
	)
	recoveryUntil := time.Time{}
	if cached != nil {
		recoveryUntil = cached.ExpiresAt
	}

	session := &dhcpSession{
		client:           client,
		cancel:           cancel,
		ready:            ready,
		identity:         identity,
		generation:       module.nextGeneration,
		recoveryPending:  cached != nil,
		recoveryUntil:    recoveryUntil,
		recoveryRejected: recoveryRejected,
	}
	module.sessions[id] = session
	module.sessionMu.Unlock()
	module.startDHCPv4Watcher(clientContext, log, id, session)

	return session
}

func (module *Module) loadDHCPv4Recovery(
	ctx context.Context,
	log *slog.Logger,
	id string,
	ready netif.OwnedLinkResult,
) (*netif.LeaseInfo, bool) {
	if module.leaseStore == nil {
		return nil, false
	}

	link, err := net.InterfaceByIndex(ready.IfIndex)
	if err != nil || link.Name != ready.ActualName {
		return nil, false
	}

	cached, err := module.leaseStore.LoadDHCPv4(id, link, module.clientIDs[id])
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return cached, false
	}

	log.WarnContext(ctx, "addresses: DHCPv4 saved lease rejected", "connection_id", id, "err", err)
	if module.deleteRecoveryLease(ctx, id, netif.LeaseProtocolDHCPv4) {
		module.recordLeasePersistence(id, familyIPv4, "rejected", err.Error())
	}

	return nil, true
}

func (module *Module) watchDHCP(ctx context.Context, id string, session *dhcpSession) {
	var expiry <-chan time.Time
	if session.recoveryPending {
		expiry = time.After(max(session.recoveryUntil.Sub(module.clock.Now()), 0))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry:
			expiry = nil
			if module.resolveDHCPv4Recovery(id, session) {
				module.deleteRecoveryLease(ctx, id, netif.LeaseProtocolDHCPv4)
				module.requestLeaseReconcile("dhcpv4 saved lease expired")
			}
		case _, ok := <-session.client.Events:
			if !ok {
				return
			}

			if module.handleDHCPv4Event(ctx, id, session) {
				expiry = nil
			}
		}
	}
}

func (module *Module) handleDHCPv4Event(ctx context.Context, id string, session *dhcpSession) bool {
	lease := session.client.LastLease()
	module.sessionMu.Lock()
	current := module.sessions[id]
	valid := current == session && current.generation == session.generation
	resolved := valid && (lease.State == netif.LeaseBound || lease.State == netif.LeaseExpired)
	if resolved {
		session.recoveryPending = false
	}

	module.sessionMu.Unlock()
	if !valid {
		return false
	}

	if module.leaseStore != nil {
		switch lease.State {
		case netif.LeaseBound:
			if err := module.leaseStore.SaveDHCPv4(
				id,
				session.ready.ActualName,
				module.clientIDs[id],
				lease,
			); err != nil {
				module.Log.WarnContext(
					ctx,
					"addresses: save DHCPv4 lease failed",
					"connection_id",
					id,
					"err",
					err,
				)
				module.recordLeasePersistence(id, familyIPv4, "failed", err.Error())
			} else {
				module.recordLeasePersistence(id, familyIPv4, "saved", "")
			}
		case netif.LeaseExpired:
			module.deleteRecoveryLease(ctx, id, netif.LeaseProtocolDHCPv4)
		case netif.LeaseInit,
			netif.LeaseSelecting,
			netif.LeaseRequesting,
			netif.LeaseRenewing,
			netif.LeaseRebinding:
		}
	}

	module.requestLeaseReconcile("dhcpv4 assignment changed")

	return resolved
}

func (module *Module) deleteRecoveryLease(
	ctx context.Context,
	id string,
	protocol netif.LeaseProtocol,
) bool {
	if module.leaseStore == nil {
		return false
	}

	family := familyIPv4
	if protocol == netif.LeaseProtocolDHCPv6 {
		family = familyIPv6
	}

	if err := module.leaseStore.Delete(id, protocol); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		module.Log.WarnContext(
			ctx,
			"addresses: delete saved lease failed",
			"connection_id",
			id,
			"protocol",
			protocol,
			"err",
			err,
		)
		module.recordLeasePersistence(id, family, "failed", err.Error())

		return false
	}

	module.recordLeasePersistence(id, family, "retired", "")

	return true
}

func (module *Module) reconcileFamily(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	assignments []interfaceintent.Assignment,
	session *dhcpSession,
	preparationErr error,
) {
	id := connection.ID.String()
	metric := uint32(0)
	if settings.RouteMetric != nil {
		metric = *settings.RouteMetric
	}

	routes := configuredFamilyRoutes(connection, family, settings, &assignments)

	appendGatewayRoute(connection, family, settings, metric, &routes, &assignments)

	acquisition, validity, leaseReady, leaseError := module.acquireFamily(
		connection,
		family,
		session,
		&settings,
		&routes,
		&assignments,
	)

	if module.Env.LiveState != nil {
		module.Env.LiveState.SetAssignment(id, family, acquisition, validity, assignments)
	}

	lifetimes := assignmentLifetimes(assignments)

	ready, linkFound := module.Env.OwnedLinks.Get(id)
	if family == familyIPv6 && !leaseReady && linkFound {
		leaseReady, leaseError = module.hasUsableAutoconfiguredAddress(
			ctx,
			log,
			connection,
			ready.ActualName,
		)
	}

	err := errors.Join(leaseError, preparationErr)
	if err == nil && !linkFound {
		err = fmt.Errorf("current link result is absent")
	} else if err == nil {
		err = module.reconciler.ReconcileFamilyRoutesWithLifetimesRetaining(
			ctx,
			connection,
			family,
			settings,
			routes,
			ready,
			lifetimes,
			module.recoveryRetention(connection, family, ready),
		)
	}

	if err == nil {
		module.Env.OwnedAddresses.SetFamilyApplied(id, family)
		err = module.publishFamilyReadiness(
			ctx,
			log,
			connection,
			family,
			settings,
			ready,
			lifetimes,
			leaseReady,
		)
	}

	module.recordFamilyApply(ctx, log, id, family, leaseReady, err)
}

func (module *Module) hasUsableAutoconfiguredAddress(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
	name string,
) (bool, error) {
	if connection.IPv6 == nil || connection.IPv6.AutoConf == nil || !*connection.IPv6.AutoConf {
		return false, nil
	}

	provider := module.providers[connection.ID.String()]
	if provider.IPv6 == nil || provider.IPv6.Mode != config.TranslationMasquerade {
		return false, nil
	}

	addresses, err := netif.ListAddrs(ctx, log, name)
	if err != nil {
		log.WarnContext(ctx, "addresses: IPv6 address lookup failed", "interface", name, "err", err)
		return false, fmt.Errorf("list IPv6 addresses on %s: %w", name, err)
	}

	for _, address := range addresses {
		prefix, parseErr := netip.ParsePrefix(address.CIDR)
		if parseErr == nil && prefix.Addr().Is6() && prefix.Addr().IsGlobalUnicast() &&
			address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED|unix.IFA_F_DEPRECATED) == 0 {
			return true, nil
		}
	}

	return false, nil
}

func (module *Module) recoveryRetention(
	connection interfaceintent.Connection,
	family string,
	ready netif.OwnedLinkResult,
) netif.RecordedRetention {
	id := connection.ID.String()
	now := module.clock.Now()
	module.sessionMu.Lock()
	defer module.sessionMu.Unlock()
	if family == familyIPv4 {
		session := module.sessions[id]

		return netif.RecordedRetention{
			All: session != nil && session.recoveryPending && now.Before(
				session.recoveryUntil,
			) &&
				session.ready.IfIndex == ready.IfIndex &&
				session.ready.ActualName == ready.ActualName,
			Prefixes: nil,
		}
	}

	session := module.dhcpv6Sessions[id]
	retention := netif.RecordedRetention{All: false, Prefixes: nil}
	if session == nil || !session.recoveryPending || !now.Before(session.recoveryUntil) ||
		session.ready.IfIndex != ready.IfIndex ||
		session.ready.ActualName != ready.ActualName ||
		session.cachedLease == nil {
		return retention
	}

	retention.Prefixes = make(map[netip.Prefix]bool)

	for _, address := range session.cachedLease.Addresses {
		if now.Before(address.ValidUntil) {
			retention.Prefixes[netip.PrefixFrom(address.Address, 128)] = true
		}
	}

	return retention
}

func currentLease(session *dhcpSession, now time.Time) (netif.LeaseInfo, string, string, bool) {
	if session == nil {
		var empty netif.LeaseInfo
		return empty, "acquiring", "pending", false
	}

	lease := session.client.LastLease()
	matches, err := lease.MatchesLink(session.ready.ActualName)
	if err != nil || !matches || lease.LinkIndex != session.ready.IfIndex {
		if session.recoveryRejected {
			return lease, "recovery-rejected", "pending", false
		}

		return lease, "acquiring", "pending", false
	}

	if lease.State == netif.LeaseExpired ||
		!lease.ExpiresAt.IsZero() && !now.Before(lease.ExpiresAt) {
		return lease, "expired", "expired", false
	}

	if lease.State != netif.LeaseBound && lease.State != netif.LeaseRenewing &&
		lease.State != netif.LeaseRebinding {
		if session.recoveryRejected {
			return lease, "recovery-rejected", "pending", false
		}

		return lease, "acquiring", "pending", false
	}

	return lease, strings.ToLower(lease.State.String()), assignmentValidityValid, true
}

func appendDHCPv4(
	connection interfaceintent.Connection,
	settings interfaceintent.Family,
	routes []netif.OwnedRoute,
	assignments []interfaceintent.Assignment,
	lease netif.LeaseInfo,
) (interfaceintent.Family, []netif.OwnedRoute, []interfaceintent.Assignment, error) {
	address, ok := netip.AddrFromSlice(lease.IP.To4())
	if !ok || lease.PrefixLen < 0 || lease.PrefixLen > 32 {
		slog.Warn("addresses: invalid DHCPv4 leased address", "connection_id", connection.ID)
		return settings, routes, assignments, fmt.Errorf("invalid DHCPv4 leased address")
	}

	prefix := netip.PrefixFrom(address, lease.PrefixLen)
	settings.Addresses = append(
		slices.Clone(settings.Addresses),
		interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeLocal},
	)
	clientID, useRoutes := dhcpv4LeaseOptions(connection)

	assignment := interfaceintent.Assignment{
		ConnectionID:   connection.ID,
		Family:         familyIPv4,
		Kind:           interfaceintent.AssignmentDHCPv4,
		Source:         "dhcpv4",
		Purpose:        interfaceintent.PurposeLocal,
		Value:          prefix,
		Route:          nil,
		ClientID:       clientID,
		DNS:            slices.Clone(lease.DNS),
		DUID:           "",
		IAID:           nil,
		AcquiredAt:     lease.AcquiredAt,
		RenewAt:        &lease.RenewAt,
		RebindAt:       &lease.RebindAt,
		PreferredUntil: nil,
		ValidUntil:     &lease.ExpiresAt,
		Valid:          true,
	}
	assignments = append(assignments, assignment)
	if !useRoutes {
		return settings, routes, assignments, nil
	}

	for _, leasedRoute := range lease.Routes {
		if leasedRoute.Destination == nil {
			slog.Warn(
				"addresses: DHCPv4 lease route has no destination",
				"connection_id",
				connection.ID,
			)

			return settings, routes, assignments, fmt.Errorf(
				"DHCPv4 lease has a route without a destination",
			)
		}

		destination, err := netip.ParsePrefix(leasedRoute.Destination.String())
		if err != nil {
			slog.Warn(
				"addresses: invalid DHCPv4 route destination",
				"connection_id",
				connection.ID,
				"err",
				err,
			)

			return settings, routes, assignments, fmt.Errorf("DHCPv4 route destination: %w", err)
		}

		if destination.Masked() == prefix.Masked() {
			continue
		}

		gateway, ok := netip.AddrFromSlice(leasedRoute.Gateway.To4())
		if !ok {
			slog.Warn("addresses: DHCPv4 route has invalid gateway", "connection_id", connection.ID)
			return settings, routes, assignments, fmt.Errorf("DHCPv4 route has an invalid gateway")
		}

		route := netif.OwnedRoute{
			Destination: destination.Masked(),
			Gateway:     gateway,
			Metric:      metricForFamily(settings),
		}
		routes = append(routes, route)
		assignment.Value = netip.Prefix{}
		assignment.Purpose = ""
		assignment.DNS = nil
		assignment.Route = &interfaceintent.RouteIntent{
			Destination: route.Destination,
			Gateway:     route.Gateway,
			TableID:     254,
			Metric:      route.Metric,
		}
		assignments = append(assignments, assignment)
	}

	return settings, routes, assignments, nil
}

func (module *Module) publishFamilyReadiness(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	ready netif.OwnedLinkResult,
	lifetimes map[netip.Prefix]netif.OwnedAddressLifetime,
	leaseReady bool,
) error {
	id := connection.ID.String()
	readyAddresses, err := module.publishInstalled(ctx, log, connection, settings, ready, lifetimes)
	if err == nil && leaseReady {
		module.Env.OwnedAddresses.SetFamilyReady(id, family)
	}

	if leaseReady && (err == nil || readyAddresses > 0) {
		module.Env.OwnedAddresses.SetFamilyRoutable(id, family)
	}

	return err
}

func (module *Module) publishInstalled(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
	settings interfaceintent.Family,
	ready netif.OwnedLinkResult,
	lifetimes map[netip.Prefix]netif.OwnedAddressLifetime,
) (int, error) {
	link, err := netlink.LinkByName(ready.ActualName)
	if err != nil || link.Attrs().Index != ready.IfIndex {
		return 0, fmt.Errorf(
			"connection %s link identity changed during address verification",
			connection.ID,
		)
	}

	addresses, err := netif.ListAddrs(ctx, log, ready.ActualName)
	if err != nil {
		log.WarnContext(
			ctx,
			"addresses: installed address read failed",
			"connection_id",
			connection.ID,
			"err",
			err,
		)

		return 0, fmt.Errorf("verify installed addresses: %w", err)
	}

	var unready error

	verified := make([]string, 0, len(settings.Addresses))
	for _, desired := range settings.Addresses {
		found := false
		dadFailed := false

		for _, current := range addresses {
			if current.CIDR != desired.Prefix.String() {
				continue
			}

			if _, leased := lifetimes[desired.Prefix]; leased &&
				current.Flags&unix.IFA_F_DADFAILED != 0 {
				module.queueDHCPv6Decline(connection.ID.String(), nil, desired.Prefix.Addr())
				dadFailed = true
				break
			}

			if current.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
				found = true
				break
			}
		}

		if dadFailed {
			unready = errors.Join(
				unready,
				fmt.Errorf("DHCPv6 address %s failed duplicate address detection", desired.Prefix),
			)
			continue
		}

		if !found {
			unready = errors.Join(
				unready,
				fmt.Errorf(
					"address %s is not installed and ready on %s",
					desired.Prefix,
					connection.Name,
				),
			)
			continue
		}

		verified = append(verified, desired.Prefix.String())
	}

	for _, prefix := range verified {
		module.Env.OwnedAddresses.Set(connection.ID.String(), prefix)
	}

	return len(verified), unready
}

func mappedAddresses(
	connection interfaceintent.Connection,
	translation *config.IPv4Translation,
) []interfaceintent.Address {
	if translation == nil {
		return nil
	}

	var addresses []interfaceintent.Address

	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery == interfaceintent.DeliveryRouted ||
			staticAddressEquals(connection, mapping.External) {
			continue
		}

		if mapping.Delivery != interfaceintent.DeliveryLocal &&
			!mappingOnLink(connection, mapping.External) {
			continue
		}

		addresses = append(
			addresses,
			interfaceintent.Address{
				Prefix:  netip.PrefixFrom(mapping.External, 32),
				Purpose: interfaceintent.PurposeForward,
			},
		)
	}

	return addresses
}

func init() { ifmgr.Register(moduleName, New) }
