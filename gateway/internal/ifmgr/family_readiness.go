package ifmgr

import (
	"goodkind.io/mwan/internal/forwardingready"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/wanstate"
)

func memberForwardingReadiness(routing wanstate.MemberRouting, health wanstate.MemberHealth) forwardingready.State {
	if health.Verdict != wanstate.HealthHealthy {
		return forwardingready.State{IPv4: false, IPv6: false}
	}
	return forwardingready.State{
		IPv4: routing.V4Ready && health.V4 == wanstate.ProbePass,
		IPv6: routing.V6Ready && health.V6 == wanstate.ProbePass,
	}
}

func (d *Daemon) publishFamilyReadiness(snapshot wanstate.Snapshot, routingFresh bool, failed forwardingready.State) {
	internal := d.internalForwardingReadiness(forwardingready.State{IPv4: true, IPv6: true})
	for _, connection := range d.cfg.Connections {
		id := connection.ID.String()
		var ready forwardingready.State
		switch {
		case connection.Roles&interfaceintent.RoleProvider != 0:
			ready = memberForwardingReadiness(snapshot.Routing[id], snapshot.Health[id])
		case connection.Roles&interfaceintent.RoleInternal != 0 && connection.Owner == interfaceintent.OwnerMWAN:
			ready = forwardingReadiness(snapshot)
		default:
			continue
		}
		ready.IPv4 = ready.IPv4 && routingFresh && internal.IPv4 && !failed.IPv4
		ready.IPv6 = ready.IPv6 && routingFresh && internal.IPv6 && !failed.IPv6
		state := snapshot.Connections[id]
		if connection.IPv4 != nil {
			ready.IPv4 = ready.IPv4 && state.IPv4.Firewall == "ready" && forwardingFamilyEnabled(connection, connection.IPv4.Family)
			d.cfg.LiveState.SetReadiness(id, "ipv4", readinessVerdict(ready.IPv4))
		}
		if connection.IPv6 != nil {
			ready.IPv6 = ready.IPv6 && state.IPv6.Firewall == "ready" && forwardingFamilyEnabled(connection, connection.IPv6.Family)
			d.cfg.LiveState.SetReadiness(id, "ipv6", readinessVerdict(ready.IPv6))
		}
	}
}

func forwardingFamilyEnabled(connection interfaceintent.Connection, family interfaceintent.Family) bool {
	if connection.Enabled != nil && !*connection.Enabled {
		return false
	}
	if family.Enabled != nil && !*family.Enabled {
		return false
	}
	return family.Forwarding == nil || *family.Forwarding
}

func readinessVerdict(ready bool) string {
	if ready {
		return "ready"
	}
	return "not-ready"
}
