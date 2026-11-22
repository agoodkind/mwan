// Package addresses acquires addresses and applies translation settings.
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

var (
	errStateFileForMappedAddresses = errors.New(
		"addresses: state_file is required for networkd mapped addresses",
	)
	errStateFileForOwnedFamilies = errors.New(
		"addresses: state_file is required for owned address families",
	)
)

// DHCPv4Starter starts a DHCPv4 client with its runtime configuration.
type DHCPv4Starter func(context.Context, *slog.Logger, netif.DHCPConfig) *netif.DHCPClient

// Config supplies connection intent, provider translations, runtime deadlines,
// and the lease journal path.
type Config struct {
	Connections     []interfaceintent.Connection
	Providers       map[string]Provider
	ClientIDs       map[string][]byte
	StateFile       string
	LeaseDirectory  string
	NetworkdTimeout time.Duration
	StartDHCPv4     DHCPv4Starter
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
	startDHCPv4     DHCPv4Starter
	reconcileMu     sync.Mutex
	sessionMu       sync.Mutex
	sessions        map[string]*dhcpSession
	dhcpv6Sessions  map[string]*dhcpv6Session
	nextGeneration  uint64
}

type dhcpSession struct {
	client           *netif.DHCPClient
	cancel           context.CancelFunc
	ready            netif.OwnedLinkResult
	identity         string
	generation       uint64
	recoveryPending  bool
	recoveryUntil    time.Time
	recoveryRejected bool
}

type dhcpv6Session struct {
	client           *netif.DHCPv6PDClient
	cancel           context.CancelFunc
	ready            netif.OwnedLinkResult
	identity         string
	generation       uint64
	recoveryPending  bool
	recoveryUntil    time.Time
	cachedLease      *netif.DHCPv6PDLease
	recoveryRejected bool
	addrUpdates      chan netlink.AddrUpdate
	addrErrors       chan error
	addrDone         chan struct{}
}

// New rejects address module settings that cannot be applied safely.
func New(moduleConfig ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := newModule()
	if moduleConfig == nil {
		return module, nil
	}

	settings, ok := moduleConfig.(Config)
	if !ok {
		return nil, fmt.Errorf("addresses: invalid config type %T", moduleConfig)
	}

	if err := configure(module, settings); err != nil {
		return nil, err
	}

	return module, nil
}

func newModule() *Module {
	return &Module{
		BaseModule:      ifmgr.NewBaseModule(moduleName),
		connections:     nil,
		providers:       nil,
		clientIDs:       nil,
		reconciler:      nil,
		leaseStore:      nil,
		clock:           clock.Real{},
		networkdTimeout: 0,
		startDHCPv4:     netif.StartDHCPClient,
		reconcileMu:     sync.Mutex{},
		sessionMu:       sync.Mutex{},
		sessions:        make(map[string]*dhcpSession),
		dhcpv6Sessions:  make(map[string]*dhcpv6Session),
		nextGeneration:  0,
	}
}

func configure(module *Module, settings Config) error {
	module.connections = settings.Connections
	module.providers = settings.Providers
	module.clientIDs = settings.ClientIDs
	module.networkdTimeout = settings.NetworkdTimeout
	if settings.StartDHCPv4 != nil {
		module.startDHCPv4 = settings.StartDHCPv4
	}

	if settings.LeaseDirectory != "" {
		store, err := netif.NewLeaseRecoveryStore(filepath.Join(settings.LeaseDirectory, "wan"))
		if err != nil {
			return fmt.Errorf("addresses: lease recovery directory: %w", err)
		}

		module.leaseStore = store
	}

	if err := validateDHCPv4ClientIDs(settings); err != nil {
		return err
	}

	if settings.StateFile == "" {
		return validateStateFileOmission(settings)
	}

	if !filepath.IsAbs(settings.StateFile) {
		return fmt.Errorf("addresses: state_file must be absolute")
	}

	reconciler, err := netif.NewOwnedStaticReconciler(settings.StateFile)
	if err != nil {
		slog.Warn("addresses: open ownership state failed", "path", settings.StateFile, "err", err)
		return fmt.Errorf("addresses: open ownership state: %w", err)
	}

	module.reconciler = reconciler

	return nil
}

func validateDHCPv4ClientIDs(settings Config) error {
	for _, connection := range settings.Connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.IPv4 == nil ||
			connection.IPv4.DHCPv4 == nil ||
			connection.IPv4.DHCPv4.ClientID == "" {
			continue
		}

		if _, ok := settings.ClientIDs[connection.ID.String()]; !ok {
			return fmt.Errorf(
				"addresses: connection %s has no decoded DHCPv4 client ID",
				connection.ID,
			)
		}
	}

	return nil
}

func validateStateFileOmission(settings Config) error {
	for _, connection := range settings.Connections {
		if networkdMappingProvider(connection, settings.Providers) != nil {
			return errStateFileForMappedAddresses
		}

		if connection.Owner == interfaceintent.OwnerMWAN &&
			(connection.IPv4 != nil || connection.IPv6 != nil) {
			return errStateFileForOwnedFamilies
		}
	}

	for _, provider := range settings.Providers {
		if provider.IPv6 != nil && provider.IPv6.Mode == config.TranslationNPTv6 {
			return fmt.Errorf("addresses: state_file is required for NPT edges")
		}
	}

	return nil
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
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("prepare owned address change: %w", err)
	}

	module.Env.OwnedAddresses.Replace()
	desiredFamilies := make(map[string]map[string]bool)
	var mappingErr error

	for _, connection := range module.connections {
		if connection.Owner == interfaceintent.OwnerNetworkd {
			provider := module.providers[connection.ID.String()]
			if provider.IPv4 != nil && len(provider.IPv4.StaticMappings) != 0 {
				desiredFamilies[connection.ID.String()] = map[string]bool{ipv4FamilyName: true}
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

	err := errors.Join(mappingErr, module.reconciler.PruneRemoved(desiredFamilies))
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

func (module *Module) reconcileConnection(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
) map[string]bool {
	families := make(map[string]bool)
	provider := module.providers[connection.ID.String()]
	session := module.currentDHCPClient(ctx, log, connection)
	module.currentDHCPv6Client(ctx, log, connection)

	for _, candidate := range []struct {
		name   string
		intent *interfaceintent.Family
	}{
		{name: ipv4FamilyName, intent: familyV4(connection.IPv4)},
		{name: ipv6FamilyName, intent: familyV6(connection.IPv6)},
	} {
		if candidate.intent == nil {
			continue
		}

		families[candidate.name] = true
		module.reconcileConfiguredFamily(
			ctx,
			log,
			connection,
			provider,
			candidate.name,
			*candidate.intent,
			session,
		)
	}

	return families
}

func (module *Module) reconcileConfiguredFamily(
	ctx context.Context,
	log *slog.Logger,
	connection interfaceintent.Connection,
	provider Provider,
	family string,
	settings interfaceintent.Family,
	session *dhcpSession,
) {
	assignments := make([]interfaceintent.Assignment, 0, len(settings.Addresses)+1)
	for _, address := range settings.Addresses {
		assignments = append(
			assignments,
			addressAssignment(
				connection,
				family,
				interfaceintent.AssignmentStatic,
				address.Purpose,
				address.Prefix,
			),
		)
	}

	var preparationErr error
	if family == ipv4FamilyName {
		for _, address := range mappedAddresses(connection, provider.IPv4) {
			settings.Addresses = append(slices.Clone(settings.Addresses), address)
			assignments = append(
				assignments,
				addressAssignment(
					connection,
					family,
					interfaceintent.AssignmentMapped,
					address.Purpose,
					address.Prefix,
				),
			)
		}
	} else {
		leasedAddresses, leasedAssignments := module.localDHCPv6Assignments(connection)
		preparationErr = module.prepareLocalIPv6(ctx, log, connection.Name, leasedAddresses)
		settings.Addresses = append(slices.Clone(settings.Addresses), leasedAddresses...)
		assignments = append(assignments, leasedAssignments...)
	}

	assignments = append(assignments, module.familyDelegations(connection, family)...)
	module.reconcileFamily(
		ctx,
		log,
		connection,
		family,
		settings,
		assignments,
		session,
		preparationErr,
	)
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
	identity := ownedDHCPv4LinkIdentity(connection, wantsDHCP, ready, ok)

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
	clientContext, cancel := context.WithCancel(ctx)
	module.nextGeneration++
	client := module.startDHCPv4(
		clientContext,
		log,
		netif.DHCPConfig{
			Iface:              ready.ActualName,
			InitialBackoff:     0,
			MaxBackoff:         0,
			DiscoverTimeout:    0,
			RequestTimeout:     0,
			RenewTimeout:       0,
			MinimumRenewalWait: 0,
			ClientID:           slices.Clone(module.clientIDs[id]),
			CachedLease:        cached,
			RestartWindow:      0,
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
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					clientContext,
					"addresses: DHCP event watcher panicked",
					"connection_id",
					id,
					"err",
					recovered,
				)
			}
		}()
		module.watchDHCP(clientContext, id, session)
	}()

	return session
}

func ownedDHCPv4LinkIdentity(
	connection interfaceintent.Connection,
	wantsDHCP bool,
	ready netif.OwnedLinkResult,
	readyFound bool,
) string {
	if !wantsDHCP || !readyFound || ready.Status != netif.OwnedLinkReady ||
		ready.ConnectionID != connection.ID.String() || ready.Name != connection.Name ||
		ready.IfIndex == 0 {
		return ""
	}

	link, err := netlink.LinkByIndex(ready.IfIndex)
	if err != nil || link.Attrs().Name != ready.ActualName {
		return ""
	}

	attributes := link.Attrs()

	return fmt.Sprintf(
		"%s/%s/%s/%d",
		link.Type(),
		attributes.HardwareAddr,
		attributes.Alias,
		attributes.ParentIndex,
	)
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
		module.recordLeasePersistence(id, ipv4FamilyName, "rejected", err.Error())
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
				module.recordLeasePersistence(id, ipv4FamilyName, "failed", err.Error())
			} else {
				module.recordLeasePersistence(id, ipv4FamilyName, "saved", "")
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

func (module *Module) resolveDHCPv4Recovery(id string, session *dhcpSession) bool {
	module.sessionMu.Lock()
	defer module.sessionMu.Unlock()
	current := module.sessions[id]
	if current != session || current.generation != session.generation || !session.recoveryPending {
		return false
	}

	session.recoveryPending = false

	return true
}

func (module *Module) deleteRecoveryLease(
	ctx context.Context,
	id string,
	protocol netif.LeaseProtocol,
) bool {
	if module.leaseStore == nil {
		return false
	}

	family := ipv4FamilyName
	if protocol == netif.LeaseProtocolDHCPv6 {
		family = ipv6FamilyName
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

func (module *Module) recordLeasePersistence(id, family, result, reason string) {
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetLeasePersistence(id, family, result, reason)
	}
}

func (module *Module) requestLeaseReconcile(reason string) {
	if module.Env.RequestReconcile != nil {
		module.Env.RequestReconcile(reason)
	}
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

func (module *Module) queueDHCPv6Decline(
	id string,
	expected *dhcpv6Session,
	address netip.Addr,
) bool {
	module.sessionMu.Lock()
	session := module.dhcpv6Sessions[id]
	module.sessionMu.Unlock()
	if session == nil || expected != nil && session != expected {
		return false
	}

	return session.client.DeclineAddress(address)
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

func staticAddressEquals(connection interfaceintent.Connection, external netip.Addr) bool {
	if connection.IPv4 == nil {
		return false
	}

	for _, address := range connection.IPv4.Addresses {
		if address.Prefix.Addr() == external {
			return true
		}
	}

	return false
}

func mappingOnLink(connection interfaceintent.Connection, external netip.Addr) bool {
	if connection.IPv4 == nil {
		return false
	}

	for _, address := range connection.IPv4.Addresses {
		if address.Prefix.Bits() < 32 && address.Prefix.Masked().Contains(external) {
			return true
		}
	}

	return false
}

func addressAssignment(
	connection interfaceintent.Connection,
	family string,
	kind interfaceintent.AssignmentKind,
	purpose interfaceintent.AddressPurpose,
	value netip.Prefix,
) interfaceintent.Assignment {
	return interfaceintent.Assignment{
		ConnectionID:   connection.ID,
		Family:         family,
		Kind:           kind,
		Source:         "configured",
		Purpose:        purpose,
		Value:          value,
		Route:          nil,
		ClientID:       "",
		DNS:            nil,
		DUID:           "",
		IAID:           nil,
		AcquiredAt:     time.Time{},
		RenewAt:        nil,
		RebindAt:       nil,
		PreferredUntil: nil,
		ValidUntil:     nil,
		Valid:          true,
	}
}

func familyV4(value *interfaceintent.IPv4) *interfaceintent.Family {
	if value == nil {
		return nil
	}

	return &value.Family
}

func familyV6(value *interfaceintent.IPv6) *interfaceintent.Family {
	if value == nil {
		return nil
	}

	return &value.Family
}

func init() { ifmgr.Register(moduleName, New) }
