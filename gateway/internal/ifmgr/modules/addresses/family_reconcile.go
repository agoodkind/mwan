package addresses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	configuredSource           = "configured"
	ipv4FamilyName             = "ipv4"
	ipv6FamilyName             = "ipv6"
	validAssignmentState       = "valid"
	acquiringLeaseState        = "acquiring"
	pendingLeaseState          = "pending"
	recoveryRejectedLeaseState = "recovery-rejected"
	expiredLeaseState          = "expired"
	ipv6AddressBitLength       = 128
	mainRouteTableID           = 254
	maxDHCPv4PrefixLength      = 32
)

var (
	errCurrentLinkAbsent             = errors.New("current link result is absent")
	errInvalidDHCPv4LeaseAddress     = errors.New("invalid DHCPv4 leased address")
	errDHCPv4RouteMissingDestination = errors.New("DHCPv4 lease has a route without a destination")
	errInvalidDHCPv4RouteGateway     = errors.New("DHCPv4 route has an invalid gateway")
)

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
	connectionID := connection.ID.String()
	state := module.prepareFamilyState(connection, family, settings, assignments, session)

	ready, available := module.Env.OwnedLinks.Get(connectionID)
	if family == ipv6FamilyName && !state.leaseReady && available {
		state.leaseReady, state.leaseError = module.hasUsableAutoconfiguredAddress(
			ctx,
			log,
			connection,
			ready.ActualName,
		)
	}

	err := errors.Join(state.leaseError, preparationErr)
	if err == nil && !available {
		err = errCurrentLinkAbsent
	} else if err == nil {
		err = module.reconciler.ReconcileFamilyRoutesWithLifetimesRetaining(
			ctx,
			connection,
			family,
			state.settings,
			state.routes,
			ready,
			state.lifetimes,
			module.recoveryRetention(connection, family, ready),
		)
	}

	if err == nil {
		module.Env.OwnedAddresses.SetFamilyApplied(connectionID, family)
		err = module.publishFamilyReadiness(
			ctx,
			log,
			connection,
			family,
			state.settings,
			ready,
			state.lifetimes,
			state.leaseReady,
		)
	}

	result := "ready"
	if !state.leaseReady {
		result = "waiting"
	}

	reason := ""
	if err != nil {
		result = "failed"
		reason = err.Error()
		log.WarnContext(
			ctx,
			"addresses: reconcile family failed",
			"connection_id",
			connectionID,
			"family",
			family,
			"err",
			err,
		)
	}

	if module.Env.LiveState != nil {
		module.Env.LiveState.SetFamilyApplyResult(
			connectionID,
			family,
			wanstate.ApplyResult{
				Operation:  "reconcile-addresses",
				Dependency: "link",
				Result:     result,
				Reason:     reason,
				At:         time.Time{},
			},
		)
	}
}

type familyReconcileState struct {
	settings    interfaceintent.Family
	routes      []netif.OwnedRoute
	assignments []interfaceintent.Assignment
	lifetimes   map[netip.Prefix]netif.OwnedAddressLifetime
	acquisition string
	validity    string
	leaseReady  bool
	leaseError  error
}

func (module *Module) prepareFamilyState(
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	assignments []interfaceintent.Assignment,
	session *dhcpSession,
) familyReconcileState {
	routes, assignments := configuredFamilyRoutes(connection, family, settings, assignments)

	state := familyReconcileState{
		settings:    settings,
		routes:      routes,
		assignments: assignments,
		lifetimes:   nil,
		acquisition: "",
		validity:    "",
		leaseReady:  true,
		leaseError:  nil,
	}
	state.setAcquisition(module, connection, family, session)

	if module.Env.LiveState != nil {
		module.Env.LiveState.SetAssignment(
			connection.ID.String(),
			family,
			state.acquisition,
			state.validity,
			state.assignments,
		)
	}

	return state
}

func configuredFamilyRoutes(
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	assignments []interfaceintent.Assignment,
) ([]netif.OwnedRoute, []interfaceintent.Assignment) {
	routes := netif.OwnedRoutesFromIntent(settings.Routes)
	for _, route := range settings.Routes {
		assignments = append(assignments, staticRouteAssignment(connection, family, route))
	}

	if !settings.Gateway.IsValid() {
		return routes, assignments
	}

	metric := uint32(0)
	if settings.RouteMetric != nil {
		metric = *settings.RouteMetric
	}

	destination := netip.MustParsePrefix("::/0")
	if family == ipv4FamilyName {
		destination = netip.MustParsePrefix("0.0.0.0/0")
	}

	routes = append(
		routes,
		netif.OwnedRoute{Destination: destination, Gateway: settings.Gateway, Metric: metric},
	)
	assignment := staticRouteAssignment(
		connection,
		family,
		interfaceintent.RouteIntent{
			Destination: destination,
			Gateway:     settings.Gateway,
			TableID:     mainRouteTableID,
			Metric:      metric,
		},
	)

	return routes, append(assignments, assignment)
}

func staticRouteAssignment(
	connection interfaceintent.Connection,
	family string,
	route interfaceintent.RouteIntent,
) interfaceintent.Assignment {
	return interfaceintent.Assignment{
		ConnectionID:   connection.ID,
		Family:         family,
		Kind:           interfaceintent.AssignmentStaticRoute,
		Source:         configuredSource,
		Purpose:        "",
		Value:          netip.Prefix{},
		Route:          &route,
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

func (state *familyReconcileState) setAcquisition(
	module *Module,
	connection interfaceintent.Connection,
	family string,
	session *dhcpSession,
) {
	state.acquisition = "static"
	state.validity = validAssignmentState
	if family == ipv4FamilyName && connection.IPv4 != nil && connection.IPv4.DHCP != nil &&
		*connection.IPv4.DHCP {
		lease, acquisition, validity, ready := currentLease(session, module.clock.Now())
		state.acquisition, state.validity, state.leaseReady = acquisition, validity, ready
		if ready {
			state.settings, state.routes, state.assignments, state.leaseError = appendDHCPv4(
				connection,
				state.settings,
				state.routes,
				state.assignments,
				lease,
			)
			if state.leaseError != nil {
				state.leaseReady = false
				state.validity = "invalid"
			}
		}
	}

	if family == ipv6FamilyName {
		state.acquisition, state.validity, state.leaseReady = module.dhcpv6State(connection)
	}

	state.lifetimes = assignmentLifetimes(state.assignments)
}

func assignmentLifetimes(
	assignments []interfaceintent.Assignment,
) map[netip.Prefix]netif.OwnedAddressLifetime {
	lifetimes := make(map[netip.Prefix]netif.OwnedAddressLifetime)

	for _, assignment := range assignments {
		if assignment.Kind != interfaceintent.AssignmentDHCPv6IANA ||
			assignment.PreferredUntil == nil || assignment.ValidUntil == nil {
			continue
		}

		lifetimes[assignment.Value] = netif.OwnedAddressLifetime{
			PreferredUntil: *assignment.PreferredUntil,
			ValidUntil:     *assignment.ValidUntil,
		}
	}

	return lifetimes
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
	connectionID := connection.ID.String()
	now := module.clock.Now()
	module.sessionMu.Lock()
	defer module.sessionMu.Unlock()
	if family == ipv4FamilyName {
		session := module.sessions[connectionID]

		return netif.RecordedRetention{
			All: session != nil && session.recoveryPending && now.Before(
				session.recoveryUntil,
			) &&
				session.ready.IfIndex == ready.IfIndex &&
				session.ready.ActualName == ready.ActualName,
			Prefixes: nil,
		}
	}

	session := module.dhcpv6Sessions[connectionID]
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
			retention.Prefixes[netip.PrefixFrom(address.Address, ipv6AddressBitLength)] = true
		}
	}

	return retention
}

func currentLease(session *dhcpSession, now time.Time) (netif.LeaseInfo, string, string, bool) {
	if session == nil {
		var empty netif.LeaseInfo

		return empty, acquiringLeaseState, pendingLeaseState, false
	}

	lease := session.client.LastLease()
	matches, err := lease.MatchesLink(session.ready.ActualName)
	if err != nil || !matches || lease.LinkIndex != session.ready.IfIndex {
		if session.recoveryRejected {
			return lease, recoveryRejectedLeaseState, pendingLeaseState, false
		}

		return lease, acquiringLeaseState, pendingLeaseState, false
	}

	if lease.State == netif.LeaseExpired ||
		!lease.ExpiresAt.IsZero() && !now.Before(lease.ExpiresAt) {
		return lease, expiredLeaseState, expiredLeaseState, false
	}

	if lease.State != netif.LeaseBound && lease.State != netif.LeaseRenewing &&
		lease.State != netif.LeaseRebinding {
		if session.recoveryRejected {
			return lease, recoveryRejectedLeaseState, pendingLeaseState, false
		}

		return lease, acquiringLeaseState, pendingLeaseState, false
	}

	return lease, strings.ToLower(lease.State.String()), validAssignmentState, true
}

func appendDHCPv4(
	connection interfaceintent.Connection,
	settings interfaceintent.Family,
	routes []netif.OwnedRoute,
	assignments []interfaceintent.Assignment,
	lease netif.LeaseInfo,
) (interfaceintent.Family, []netif.OwnedRoute, []interfaceintent.Assignment, error) {
	address, ok := netip.AddrFromSlice(lease.IP.To4())
	if !ok || lease.PrefixLen < 0 || lease.PrefixLen > maxDHCPv4PrefixLength {
		slog.Warn("addresses: invalid DHCPv4 leased address", "connection_id", connection.ID)

		return settings, routes, assignments, errInvalidDHCPv4LeaseAddress
	}

	prefix := netip.PrefixFrom(address, lease.PrefixLen)
	settings.Addresses = append(
		slices.Clone(settings.Addresses),
		interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeLocal},
	)
	clientID := ""
	useRoutes := true
	if connection.IPv4.DHCPv4 != nil {
		clientID = connection.IPv4.DHCPv4.ClientID
		if connection.IPv4.DHCPv4.UseRoutes != nil {
			useRoutes = *connection.IPv4.DHCPv4.UseRoutes
		}
	}

	assignment := interfaceintent.Assignment{
		ConnectionID:   connection.ID,
		Family:         ipv4FamilyName,
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

	return appendDHCPv4Routes(connection, settings, routes, assignments, lease, prefix, assignment)
}

func appendDHCPv4Routes(
	connection interfaceintent.Connection,
	settings interfaceintent.Family,
	routes []netif.OwnedRoute,
	assignments []interfaceintent.Assignment,
	lease netif.LeaseInfo,
	prefix netip.Prefix,
	assignment interfaceintent.Assignment,
) (interfaceintent.Family, []netif.OwnedRoute, []interfaceintent.Assignment, error) {
	for _, leasedRoute := range lease.Routes {
		if leasedRoute.Destination == nil {
			slog.Warn(
				"addresses: DHCPv4 lease route has no destination",
				"connection_id",
				connection.ID,
			)

			return settings, routes, assignments, errDHCPv4RouteMissingDestination
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

			return settings, routes, assignments, errInvalidDHCPv4RouteGateway
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
			TableID:     mainRouteTableID,
			Metric:      route.Metric,
		}
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
