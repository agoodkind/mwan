package addresses

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	familyIPv4                 = "ipv4"
	familyIPv6                 = "ipv6"
	assignmentSourceConfigured = "configured"
	assignmentValidityValid    = "valid"
)

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
		{name: familyIPv4, intent: familyV4(connection.IPv4)},
		{name: familyIPv6, intent: familyV6(connection.IPv6)},
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
	if family == familyIPv4 {
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

func metricForFamily(family interfaceintent.Family) uint32 {
	if family.RouteMetric == nil {
		return 0
	}

	return *family.RouteMetric
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
		Source:         assignmentSourceConfigured,
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

func configuredFamilyRoutes(
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	assignments *[]interfaceintent.Assignment,
) []netif.OwnedRoute {
	routes := netif.OwnedRoutesFromIntent(settings.Routes)
	for _, route := range settings.Routes {
		*assignments = append(
			*assignments,
			interfaceintent.Assignment{
				ConnectionID: connection.ID,
				Family:       family,
				Kind:         interfaceintent.AssignmentStaticRoute,
				Source:       assignmentSourceConfigured,
				Purpose:      "",
				Value:        netip.Prefix{},
				Route: &interfaceintent.RouteIntent{
					Destination: route.Destination,
					Gateway:     route.Gateway,
					TableID:     route.TableID,
					Metric:      route.Metric,
				},
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
			},
		)
	}

	return routes
}

func (module *Module) acquireFamily(
	connection interfaceintent.Connection,
	family string,
	session *dhcpSession,
	settings *interfaceintent.Family,
	routes *[]netif.OwnedRoute,
	assignments *[]interfaceintent.Assignment,
) (string, string, bool, error) {
	acquisition := "static"
	validity := assignmentValidityValid
	leaseReady := true
	var leaseError error
	if family == familyIPv4 && connection.IPv4 != nil && connection.IPv4.DHCP != nil &&
		*connection.IPv4.DHCP {
		lease, state, status, valid := currentLease(session, module.clock.Now())
		acquisition, validity, leaseReady = state, status, valid
		if valid {
			*settings, *routes, *assignments, leaseError = appendDHCPv4(
				connection,
				*settings,
				*routes,
				*assignments,
				lease,
			)
			if leaseError != nil {
				leaseReady = false
				validity = "invalid"
			}
		}
	}

	if family == familyIPv6 {
		acquisition, validity, leaseReady = module.dhcpv6State(connection)
	}

	return acquisition, validity, leaseReady, leaseError
}

func assignmentLifetimes(
	assignments []interfaceintent.Assignment,
) map[netip.Prefix]netif.OwnedAddressLifetime {
	lifetimes := make(map[netip.Prefix]netif.OwnedAddressLifetime)

	for _, assignment := range assignments {
		if assignment.Kind != interfaceintent.AssignmentDHCPv6IANA ||
			assignment.PreferredUntil == nil ||
			assignment.ValidUntil == nil {
			continue
		}

		lifetimes[assignment.Value] = netif.OwnedAddressLifetime{
			PreferredUntil: *assignment.PreferredUntil,
			ValidUntil:     *assignment.ValidUntil,
		}
	}

	return lifetimes
}

func (module *Module) recordFamilyApply(
	ctx context.Context,
	log *slog.Logger,
	connectionID string,
	family string,
	leaseReady bool,
	err error,
) {
	result := "ready"
	if !leaseReady {
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

func appendGatewayRoute(
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	metric uint32,
	routes *[]netif.OwnedRoute,
	assignments *[]interfaceintent.Assignment,
) {
	if settings.Gateway.IsValid() {
		destination := netip.MustParsePrefix("::/0")
		if family == familyIPv4 {
			destination = netip.MustParsePrefix("0.0.0.0/0")
		}

		*routes = append(
			*routes,
			netif.OwnedRoute{Destination: destination, Gateway: settings.Gateway, Metric: metric},
		)
		*assignments = append(
			*assignments,
			interfaceintent.Assignment{
				ConnectionID: connection.ID,
				Family:       family,
				Kind:         interfaceintent.AssignmentStaticRoute,
				Source:       assignmentSourceConfigured,
				Purpose:      "",
				Value:        netip.Prefix{},
				Route: &interfaceintent.RouteIntent{
					Destination: destination,
					Gateway:     settings.Gateway,
					TableID:     unix.RT_TABLE_MAIN,
					Metric:      metric,
				},
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
			},
		)
	}
}
