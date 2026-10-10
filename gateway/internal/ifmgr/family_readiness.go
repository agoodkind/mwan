package ifmgr

import (
	"goodkind.io/mwan/internal/forwardingready"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/wanstate"
)

func familyProbeReady(known bool, verdict wanstate.Health, probe wanstate.ProbeResult, family wanstate.Health) bool {
	if !known {
		return true
	}
	if verdict != wanstate.HealthHealthy {
		return false
	}
	return probe == wanstate.ProbeNone || family == wanstate.HealthHealthy
}

func memberForwardingReadiness(routing wanstate.MemberRouting, health wanstate.MemberHealth, known bool) forwardingready.State {
	return forwardingready.State{
		IPv4: routing.V4Ready && familyProbeReady(known, health.Verdict, health.V4, health.V4Verdict),
		IPv6: routing.V6Ready && familyProbeReady(known, health.Verdict, health.V6, health.V6Verdict),
	}
}

type familyEvaluation struct {
	settings *interfaceintent.Family
	name     string
	ready    bool
	firewall string
	inputs   pathInputs
}

func (d *Daemon) publishFamilyReadiness(snapshot wanstate.Snapshot, routingFresh bool, failed forwardingready.State, routingFailure string) {
	internal := d.internalForwardingReadiness(forwardingready.State{IPv4: true, IPv6: true})
	for _, connection := range d.cfg.Connections {
		id := connection.ID.String()
		routing, routingKnown := snapshot.Routing[id]
		health, healthKnown := snapshot.Health[id]
		provider := connection.Roles&interfaceintent.RoleProvider != 0
		var ready forwardingready.State
		switch {
		case provider:
			ready = memberForwardingReadiness(routing, health, healthKnown)
		case connection.Roles&interfaceintent.RoleInternal != 0 && connection.Owner == interfaceintent.OwnerMWAN:
			ready = forwardingReadiness(snapshot)
		default:
			continue
		}
		state := snapshot.Connections[id]
		shared := pathInputs{
			enabled: false, configured: false, routingKnown: routingKnown, routingFresh: routingFresh, routingFailure: routingFailure,
			routingReady: false, dependency: "", reason: "", healthKnown: healthKnown, health: health.Verdict,
			probe: "", familyHealth: "", firewall: "", internalReady: false, moduleFailed: false,
		}
		var families []familyEvaluation
		if connection.IPv4 != nil {
			inputs := shared
			inputs.routingReady, inputs.dependency, inputs.reason = routing.V4Ready, routing.V4Dependency, routing.V4Reason
			inputs.probe, inputs.familyHealth = health.V4, health.V4Verdict
			inputs.internalReady, inputs.moduleFailed, inputs.configured = internal.IPv4, failed.IPv4, routing.V4Configured
			families = append(families, familyEvaluation{
				settings: &connection.IPv4.Family, name: familyNameIPv4, firewall: state.IPv4.Firewall, inputs: inputs,
				ready: ready.IPv4 && routingFresh && internal.IPv4 && !failed.IPv4,
			})
		}
		if connection.IPv6 != nil {
			inputs := shared
			inputs.routingReady, inputs.dependency, inputs.reason = routing.V6Ready, routing.V6Dependency, routing.V6Reason
			inputs.probe, inputs.familyHealth = health.V6, health.V6Verdict
			inputs.internalReady, inputs.moduleFailed, inputs.configured = internal.IPv6, failed.IPv6, routing.V6Configured
			families = append(families, familyEvaluation{
				settings: &connection.IPv6.Family, name: familyNameIPv6, firewall: state.IPv6.Firewall, inputs: inputs,
				ready: ready.IPv6 && routingFresh && internal.IPv6 && !failed.IPv6,
			})
		}
		for _, family := range families {
			family.inputs.enabled = forwardingFamilyEnabled(connection, *family.settings)
			family.inputs.firewall = family.firewall
			familyReady := family.ready && family.firewall == firewallReady && family.inputs.enabled
			if provider {
				d.cfg.LiveState.SetFamilyReadiness(id, family.name, readinessVerdict(familyReady), pathVerdict(familyReady, family.inputs))
				continue
			}
			d.cfg.LiveState.SetReadiness(id, family.name, readinessVerdict(familyReady))
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
