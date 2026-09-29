// Package addresses reconciles configured and acquired addresses on MWAN links.
package addresses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
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

// Config supplies owned family intent, translation settings, and the journal path.
type Config struct {
	Connections []interfaceintent.Connection
	Providers   map[string]Provider
	ClientIDs   map[string][]byte
	StateFile   string
}

// Provider supplies translation addresses for an exclusively owned connection.
type Provider struct {
	IPv4 *config.IPv4Translation
	IPv6 *config.IPv6Translation
}

// ModuleConfigName selects the registered address module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies configured and acquired family intent after links are ready.
type Module struct {
	ifmgr.BaseModule
	connections    []interfaceintent.Connection
	providers      map[string]Provider
	clientIDs      map[string][]byte
	reconciler     *netif.OwnedStaticReconciler
	clock          clock.Clock
	reconcileMu    sync.Mutex
	sessionMu      sync.Mutex
	sessions       map[string]*dhcpSession
	dhcpv6Sessions map[string]*dhcpv6Session
	nextGeneration uint64
}

type dhcpSession struct {
	client     *netif.DHCPClient
	cancel     context.CancelFunc
	ready      netif.OwnedLinkResult
	identity   string
	generation uint64
}

type dhcpv6Session struct {
	client      *netif.DHCPv6PDClient
	cancel      context.CancelFunc
	ready       netif.OwnedLinkResult
	identity    string
	generation  uint64
	addrUpdates chan netlink.AddrUpdate
	addrErrors  chan error
	addrDone    chan struct{}
}

// New validates the address journal before daemon startup.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName), connections: nil, providers: nil,
		clientIDs: nil, reconciler: nil, clock: clock.Real{}, reconcileMu: sync.Mutex{},
		sessionMu: sync.Mutex{}, sessions: make(map[string]*dhcpSession),
		dhcpv6Sessions: make(map[string]*dhcpv6Session), nextGeneration: 0,
	}
	if config == nil {
		return module, nil
	}
	settings, ok := config.(Config)
	if !ok {
		return nil, fmt.Errorf("addresses: invalid config type %T", config)
	}
	module.connections = settings.Connections
	module.providers = settings.Providers
	module.clientIDs = settings.ClientIDs
	for _, connection := range settings.Connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.IPv4 == nil || connection.IPv4.DHCPv4 == nil || connection.IPv4.DHCPv4.ClientID == "" {
			continue
		}
		if _, ok := settings.ClientIDs[connection.ID.String()]; !ok {
			return nil, fmt.Errorf("addresses: connection %s has no decoded DHCPv4 client ID", connection.ID)
		}
	}
	if settings.StateFile == "" {
		for _, connection := range settings.Connections {
			if connection.Owner == interfaceintent.OwnerMWAN && (connection.IPv4 != nil || connection.IPv6 != nil) {
				return nil, fmt.Errorf("addresses: state_file is required for owned address families")
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

// Init requires the preceding link module's result store.
func (module *Module) Init(_ context.Context, env *ifmgr.Env) error {
	module.InitBase(env, "module", moduleName)
	if module.reconciler == nil {
		return ifmgr.ErrModuleDisabled
	}
	if env.OwnedLinks == nil {
		return fmt.Errorf("addresses: links module is required")
	}
	env.OwnedAddresses = &ifmgr.OwnedAddressResults{}
	env.Delegations = netif.NewDHCPv6PDStore()
	return nil
}

// Reconcile applies owned addresses and routes, then removes deleted families.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.reconcileMu.Lock()
	defer module.reconcileMu.Unlock()
	module.Env.OwnedAddresses.Replace()
	desiredFamilies := make(map[string]map[string]bool)
	for _, connection := range module.connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		desiredFamilies[connection.ID.String()] = module.reconcileConnection(ctx, log, connection)
	}
	err := module.reconciler.PruneRemoved(desiredFamilies)
	var pending []wanstate.PendingRemoval
	if err != nil {
		var failure *netif.OwnedStaticPruneError
		if errors.As(err, &failure) && module.Env.LiveState != nil {
			result := wanstate.ApplyResult{Operation: "prune-static", Dependency: "link", Result: "failed", Reason: failure.Error(), At: time.Time{}}
			if _, configured := desiredFamilies[failure.ConnectionID]; configured {
				module.Env.LiveState.SetFamilyApplyResult(failure.ConnectionID, failure.Family, result)
			} else {
				pending = append(pending, wanstate.PendingRemoval{ConnectionID: failure.ConnectionID, Name: failure.LinkName, Family: failure.Family, Apply: result})
			}
		}
	}
	if module.Env.LiveState != nil {
		module.Env.LiveState.ReplacePendingRemovals(pending)
	}
	if err != nil {
		log.WarnContext(ctx, "addresses: prune removed families failed", "err", err)
		return fmt.Errorf("addresses: prune removed families: %w", err)
	}
	return nil
}

func (module *Module) reconcileConnection(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection) map[string]bool {
	families := make(map[string]bool)
	provider := module.providers[connection.ID.String()]
	session := module.currentDHCPClient(ctx, log, connection)
	module.currentDHCPv6Client(ctx, log, connection)
	for _, candidate := range []struct {
		name   string
		intent *interfaceintent.Family
	}{
		{name: "ipv4", intent: familyV4(connection.IPv4)},
		{name: "ipv6", intent: familyV6(connection.IPv6)},
	} {
		if candidate.intent == nil {
			continue
		}
		families[candidate.name] = true
		module.reconcileConfiguredFamily(ctx, log, connection, provider, candidate.name, *candidate.intent, session)
	}
	return families
}

func (module *Module) reconcileConfiguredFamily(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, provider Provider, family string, settings interfaceintent.Family, session *dhcpSession) {
	assignments := make([]interfaceintent.Assignment, 0, len(settings.Addresses)+1)
	for _, address := range settings.Addresses {
		assignments = append(assignments, addressAssignment(connection, family, interfaceintent.AssignmentStatic, address.Purpose, address.Prefix))
	}
	var preparationErr error
	if family == "ipv4" {
		for _, address := range mappedAddresses(connection, provider.IPv4) {
			settings.Addresses = append(slices.Clone(settings.Addresses), address)
			assignments = append(assignments, addressAssignment(connection, family, interfaceintent.AssignmentMapped, address.Purpose, address.Prefix))
		}
	} else {
		if prefix := nptAddress(provider.IPv6, module.delegation(connection.Name)); prefix.IsValid() {
			settings.Addresses = append(slices.Clone(settings.Addresses), interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeForward})
			assignments = append(assignments, addressAssignment(connection, family, interfaceintent.AssignmentNPTExternal, interfaceintent.PurposeForward, prefix))
		}
		leasedAddresses, leasedAssignments := module.localDHCPv6Assignments(connection)
		preparationErr = module.prepareLocalIPv6(ctx, log, connection.Name, leasedAddresses)
		settings.Addresses = append(slices.Clone(settings.Addresses), leasedAddresses...)
		assignments = append(assignments, leasedAssignments...)
	}
	assignments = append(assignments, module.familyDelegations(connection, family)...)
	module.reconcileFamily(ctx, log, connection, family, settings, assignments, session, preparationErr)
}

func (module *Module) prepareLocalIPv6(ctx context.Context, log *slog.Logger, iface string, addresses []interfaceintent.Address) error {
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

func (module *Module) currentDHCPClient(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection) *dhcpSession {
	id := connection.ID.String()
	wantsDHCP := connection.IPv4 != nil && connection.IPv4.DHCP != nil && *connection.IPv4.DHCP
	ready, ok := module.Env.OwnedLinks.Get(id)
	identity := ""
	if wantsDHCP && ok && ready.Status == netif.OwnedLinkReady && ready.ConnectionID == id &&
		ready.Name == connection.Name && ready.IfIndex != 0 {
		link, err := netlink.LinkByIndex(ready.IfIndex)
		if err == nil && link.Attrs().Name == ready.ActualName {
			attributes := link.Attrs()
			identity = fmt.Sprintf("%s/%s/%s/%d", link.Type(), attributes.HardwareAddr, attributes.Alias, attributes.ParentIndex)
		}
	}
	module.sessionMu.Lock()
	previous := module.sessions[id]
	if identity == "" {
		if previous != nil {
			previous.cancel()
			delete(module.sessions, id)
		}
		module.sessionMu.Unlock()
		return nil
	}
	if previous != nil && previous.ready.IfIndex == ready.IfIndex &&
		previous.ready.ActualName == ready.ActualName && previous.identity == identity {
		module.sessionMu.Unlock()
		return previous
	}
	if previous != nil {
		previous.cancel()
	}
	clientContext, cancel := context.WithCancel(ctx)
	module.nextGeneration++
	client := netif.StartDHCPClient(clientContext, log, netif.DHCPConfig{
		Iface: ready.ActualName, InitialBackoff: 0, MaxBackoff: 0,
		DiscoverTimeout: 0, RequestTimeout: 0, RenewTimeout: 0,
		ClientID: slices.Clone(module.clientIDs[id]),
	})
	session := &dhcpSession{client: client, cancel: cancel, ready: ready, identity: identity, generation: module.nextGeneration}
	module.sessions[id] = session
	module.sessionMu.Unlock()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(clientContext, "addresses: DHCP event watcher panicked", "connection_id", id, "err", recovered)
			}
		}()
		module.watchDHCP(clientContext, id, session)
	}()
	return session
}

func (module *Module) watchDHCP(ctx context.Context, id string, session *dhcpSession) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-session.client.Events:
			if !ok {
				return
			}
			module.sessionMu.Lock()
			current := module.sessions[id]
			valid := current == session && current.generation == session.generation
			module.sessionMu.Unlock()
			if valid && module.Env.RequestReconcile != nil {
				module.Env.RequestReconcile("dhcpv4 assignment changed")
			}
		}
	}
}

func (module *Module) reconcileFamily(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, family string, settings interfaceintent.Family, assignments []interfaceintent.Assignment, session *dhcpSession, preparationErr error) {
	id := connection.ID.String()
	metric := uint32(0)
	if settings.RouteMetric != nil {
		metric = *settings.RouteMetric
	}
	routes := make([]netif.OwnedRoute, 0, 1)
	if settings.Gateway.IsValid() {
		destination := netip.MustParsePrefix("::/0")
		if family == "ipv4" {
			destination = netip.MustParsePrefix("0.0.0.0/0")
		}
		routes = append(routes, netif.OwnedRoute{Destination: destination, Gateway: settings.Gateway, Metric: metric})
		assignments = append(assignments, interfaceintent.Assignment{
			ConnectionID: connection.ID, Family: family, Kind: interfaceintent.AssignmentStaticRoute,
			Source: "configured", Purpose: "", Value: netip.Prefix{}, Route: &interfaceintent.RouteIntent{
				Destination: destination,
				Gateway:     settings.Gateway, TableID: 254, Metric: metric,
			}, ClientID: "", DUID: "", IAID: nil, AcquiredAt: time.Time{}, RenewAt: nil,
			RebindAt: nil, PreferredUntil: nil, ValidUntil: nil, Valid: true,
		})
	}
	acquisition := "static"
	validity := "valid"
	leaseReady := true
	var leaseError error
	if family == "ipv4" && connection.IPv4 != nil && connection.IPv4.DHCP != nil && *connection.IPv4.DHCP {
		lease, state, status, valid := currentLease(session, module.clock.Now())
		acquisition, validity, leaseReady = state, status, valid
		if valid {
			settings, routes, assignments, leaseError = appendDHCPv4(connection, settings, routes, assignments, lease)
			if leaseError != nil {
				leaseReady = false
				validity = "invalid"
			}
		}
	}
	if family == "ipv6" {
		acquisition, validity, leaseReady = module.dhcpv6State(connection)
	}
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetAssignment(id, family, acquisition, validity, assignments)
	}
	lifetimes := make(map[netip.Prefix]netif.OwnedAddressLifetime)
	for _, assignment := range assignments {
		if assignment.Kind != interfaceintent.AssignmentDHCPv6IANA || assignment.PreferredUntil == nil || assignment.ValidUntil == nil {
			continue
		}
		lifetimes[assignment.Value] = netif.OwnedAddressLifetime{
			PreferredUntil: *assignment.PreferredUntil, ValidUntil: *assignment.ValidUntil,
		}
	}
	ready, ok := module.Env.OwnedLinks.Get(id)
	err := errors.Join(leaseError, preparationErr)
	if err == nil && !ok {
		err = fmt.Errorf("current link result is absent")
	} else if err == nil {
		err = module.reconciler.ReconcileFamilyRoutesWithLifetimes(ctx, connection, family, settings, routes, ready, lifetimes)
	}
	if err == nil {
		err = module.publishInstalled(ctx, log, connection, settings, ready, lifetimes)
	}
	if err == nil {
		module.Env.OwnedAddresses.SetFamilyApplied(id, family)
	}
	if err == nil && leaseReady {
		module.Env.OwnedAddresses.SetFamilyReady(id, family)
	}
	result := "ready"
	if !leaseReady {
		result = "waiting"
	}
	reason := ""
	if err != nil {
		result = "failed"
		reason = err.Error()
		log.WarnContext(ctx, "addresses: reconcile family failed", "connection_id", id, "family", family, "err", err)
	}
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetFamilyApplyResult(id, family, wanstate.ApplyResult{Operation: "reconcile-addresses", Dependency: "link", Result: result, Reason: reason, At: time.Time{}})
	}
}

func currentLease(session *dhcpSession, now time.Time) (netif.LeaseInfo, string, string, bool) {
	if session == nil {
		var empty netif.LeaseInfo
		return empty, "acquiring", "pending", false
	}
	lease := session.client.LastLease()
	matches, err := lease.MatchesLink(session.ready.ActualName)
	if err != nil || !matches || lease.LinkIndex != session.ready.IfIndex {
		return lease, "acquiring", "pending", false
	}
	if lease.State == netif.LeaseExpired || !lease.ExpiresAt.IsZero() && !now.Before(lease.ExpiresAt) {
		return lease, "expired", "expired", false
	}
	if lease.State != netif.LeaseBound && lease.State != netif.LeaseRenewing && lease.State != netif.LeaseRebinding {
		return lease, "acquiring", "pending", false
	}
	return lease, strings.ToLower(lease.State.String()), "valid", true
}

func appendDHCPv4(connection interfaceintent.Connection, settings interfaceintent.Family, routes []netif.OwnedRoute, assignments []interfaceintent.Assignment, lease netif.LeaseInfo) (interfaceintent.Family, []netif.OwnedRoute, []interfaceintent.Assignment, error) {
	address, ok := netip.AddrFromSlice(lease.IP.To4())
	if !ok || lease.PrefixLen < 0 || lease.PrefixLen > 32 {
		slog.Warn("addresses: invalid DHCPv4 leased address", "connection_id", connection.ID)
		return settings, routes, assignments, fmt.Errorf("invalid DHCPv4 leased address")
	}
	prefix := netip.PrefixFrom(address, lease.PrefixLen)
	settings.Addresses = append(slices.Clone(settings.Addresses), interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeLocal})
	clientID := ""
	useRoutes := true
	if connection.IPv4.DHCPv4 != nil {
		clientID = connection.IPv4.DHCPv4.ClientID
		if connection.IPv4.DHCPv4.UseRoutes != nil {
			useRoutes = *connection.IPv4.DHCPv4.UseRoutes
		}
	}
	assignment := interfaceintent.Assignment{
		ConnectionID: connection.ID, Family: "ipv4", Kind: interfaceintent.AssignmentDHCPv4,
		Source: "dhcpv4", Purpose: interfaceintent.PurposeLocal, Value: prefix, Route: nil,
		ClientID: clientID, DUID: "", IAID: nil, AcquiredAt: lease.AcquiredAt,
		RenewAt: &lease.RenewAt, RebindAt: &lease.RebindAt, PreferredUntil: nil,
		ValidUntil: &lease.ExpiresAt, Valid: true,
	}
	assignments = append(assignments, assignment)
	if !useRoutes {
		return settings, routes, assignments, nil
	}
	for _, leasedRoute := range lease.Routes {
		if leasedRoute.Destination == nil {
			slog.Warn("addresses: DHCPv4 lease route has no destination", "connection_id", connection.ID)
			return settings, routes, assignments, fmt.Errorf("DHCPv4 lease has a route without a destination")
		}
		destination, err := netip.ParsePrefix(leasedRoute.Destination.String())
		if err != nil {
			slog.Warn("addresses: invalid DHCPv4 route destination", "connection_id", connection.ID, "err", err)
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
		route := netif.OwnedRoute{Destination: destination.Masked(), Gateway: gateway, Metric: metricForFamily(settings)}
		routes = append(routes, route)
		assignment.Value = netip.Prefix{}
		assignment.Purpose = ""
		assignment.Route = &interfaceintent.RouteIntent{Destination: route.Destination, Gateway: route.Gateway, TableID: 254, Metric: route.Metric}
		assignments = append(assignments, assignment)
	}
	return settings, routes, assignments, nil
}

func metricForFamily(family interfaceintent.Family) uint32 {
	if family.RouteMetric == nil {
		return 0
	}
	return *family.RouteMetric
}

func (module *Module) publishInstalled(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, settings interfaceintent.Family, ready netif.OwnedLinkResult, lifetimes map[netip.Prefix]netif.OwnedAddressLifetime) error {
	link, err := netlink.LinkByName(ready.ActualName)
	if err != nil || link.Attrs().Index != ready.IfIndex {
		return fmt.Errorf("connection %s link identity changed during address verification", connection.ID)
	}
	addresses, err := netif.ListAddrs(ctx, log, ready.ActualName)
	if err != nil {
		log.WarnContext(ctx, "addresses: installed address read failed", "connection_id", connection.ID, "err", err)
		return fmt.Errorf("verify installed addresses: %w", err)
	}
	verified := make([]string, 0, len(settings.Addresses))
	for _, desired := range settings.Addresses {
		found := false
		for _, current := range addresses {
			if current.CIDR != desired.Prefix.String() {
				continue
			}
			if _, leased := lifetimes[desired.Prefix]; leased && current.Flags&unix.IFA_F_DADFAILED != 0 {
				module.queueDHCPv6Decline(connection.ID.String(), nil, desired.Prefix.Addr())
				return fmt.Errorf("DHCPv6 address %s failed duplicate address detection", desired.Prefix)
			}
			if current.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("address %s is not installed and ready on %s", desired.Prefix, connection.Name)
		}
		verified = append(verified, desired.Prefix.String())
	}
	for _, prefix := range verified {
		module.Env.OwnedAddresses.Set(connection.ID.String(), prefix)
	}
	return nil
}

func (module *Module) queueDHCPv6Decline(id string, expected *dhcpv6Session, address netip.Addr) bool {
	module.sessionMu.Lock()
	session := module.dhcpv6Sessions[id]
	module.sessionMu.Unlock()
	if session == nil || expected != nil && session != expected {
		return false
	}
	return session.client.DeclineAddress(address)
}

func mappedAddresses(connection interfaceintent.Connection, translation *config.IPv4Translation) []interfaceintent.Address {
	if translation == nil {
		return nil
	}
	var addresses []interfaceintent.Address
	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery == interfaceintent.DeliveryRouted || staticAddressEquals(connection, mapping.External) {
			continue
		}
		if mapping.Delivery != interfaceintent.DeliveryLocal && !mappingOnLink(connection, mapping.External) {
			continue
		}
		addresses = append(addresses, interfaceintent.Address{Prefix: netip.PrefixFrom(mapping.External, 32), Purpose: interfaceintent.PurposeForward})
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

func nptAddress(translation *config.IPv6Translation, delegated netip.Prefix) netip.Prefix {
	if translation == nil || translation.NPT == nil || translation.Mode != config.TranslationNPTv6 {
		return netip.Prefix{}
	}
	prefix := translation.NPT.ExternalPrefix.Masked()
	if translation.NPT.ExternalSource == config.PrefixDelegated {
		if !delegated.IsValid() {
			return netip.Prefix{}
		}
		bits := translation.NPT.InternalPrefix.Bits()
		if translation.NPT.ExpectedPrefix.IsValid() {
			bits = translation.NPT.ExpectedPrefix.Bits()
		}
		if bits < delegated.Bits() {
			return netip.Prefix{}
		}
		prefix = netip.PrefixFrom(delegated.Addr(), bits).Masked()
	}
	if !prefix.IsValid() {
		return netip.Prefix{}
	}
	address := prefix.Addr().As16()
	address[15] = 1
	return netip.PrefixFrom(netip.AddrFrom16(address), 128)
}

func addressAssignment(connection interfaceintent.Connection, family string, kind interfaceintent.AssignmentKind, purpose interfaceintent.AddressPurpose, value netip.Prefix) interfaceintent.Assignment {
	return interfaceintent.Assignment{
		ConnectionID: connection.ID, Family: family, Kind: kind,
		Source: "configured", Purpose: purpose, Value: value, Route: nil, ClientID: "", DUID: "", IAID: nil,
		AcquiredAt: time.Time{}, RenewAt: nil, RebindAt: nil, PreferredUntil: nil, ValidUntil: nil, Valid: true,
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
