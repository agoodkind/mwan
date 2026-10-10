package ifmgr

import (
	"slices"

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
	name          string
	enabled       bool
	forwarding    bool
	firewall      string
	routing       []wanstate.DependencyResult
	health        familyHealth
	internalReady bool
	moduleFailed  bool
}

func (family familyEvaluation) ready(evaluation routingEvaluation) bool {
	return family.forwarding && evaluation.published && family.internalReady && !family.moduleFailed &&
		family.firewall == firewallReady && family.enabled
}

func (family familyEvaluation) dependencies(evaluation routingEvaluation, memberPublished bool) []wanstate.DependencyResult {
	results := []wanstate.DependencyResult{enabledResult(family.enabled), routingResult(evaluation, memberPublished)}
	results = append(results, family.routing...)
	return append(results,
		probeResult(family.health),
		firewallResult(family.firewall),
		flagResult(pathDependencyInternalLink, pathReasonInternalLink, family.internalReady),
		flagResult(pathDependencyReconcile, pathReasonReconcileFailed, !family.moduleFailed),
	)
}

func (d *Daemon) publishFamilyReadiness(snapshot wanstate.Snapshot, evaluation routingEvaluation, failed forwardingready.State) {
	internal := d.internalForwardingReadiness(forwardingready.State{IPv4: true, IPv6: true})
	for _, connection := range d.cfg.Connections {
		id := connection.ID.String()
		routing, memberPublished := snapshot.Routing[id]
		health, probed := snapshot.Health[id]
		provider := connection.Roles&interfaceintent.RoleProvider != 0
		var ready forwardingready.State
		switch {
		case provider:
			ready = memberForwardingReadiness(routing, health, probed)
		case connection.Roles&interfaceintent.RoleInternal != 0 && connection.Owner == interfaceintent.OwnerMWAN:
			ready = forwardingReadiness(snapshot)
		default:
			continue
		}
		state := snapshot.Connections[id]
		var families []familyEvaluation
		if connection.IPv4 != nil {
			families = append(families, familyEvaluation{
				name: familyNameIPv4, enabled: forwardingFamilyEnabled(connection, connection.IPv4.Family), forwarding: ready.IPv4,
				firewall: state.IPv4.Firewall, routing: slices.Clone(routing.V4Dependencies),
				health:        familyHealth{probed: probed, member: health.Verdict, result: health.V4, verdict: health.V4Verdict},
				internalReady: internal.IPv4, moduleFailed: failed.IPv4,
			})
		}
		if connection.IPv6 != nil {
			families = append(families, familyEvaluation{
				name: familyNameIPv6, enabled: forwardingFamilyEnabled(connection, connection.IPv6.Family), forwarding: ready.IPv6,
				firewall: state.IPv6.Firewall, routing: slices.Clone(routing.V6Dependencies),
				health:        familyHealth{probed: probed, member: health.Verdict, result: health.V6, verdict: health.V6Verdict},
				internalReady: internal.IPv6, moduleFailed: failed.IPv6,
			})
		}
		for _, family := range families {
			familyReady := family.ready(evaluation)
			if provider {
				verdict := pathVerdict(familyReady, family.dependencies(evaluation, memberPublished))
				d.cfg.LiveState.SetFamilyReadiness(id, family.name, readinessVerdict(familyReady), verdict)
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
