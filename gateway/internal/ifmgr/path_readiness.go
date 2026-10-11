package ifmgr

import (
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	pathDependencyStartup         = "startup"
	pathDependencyFamilyEnabled   = "family-enabled"
	pathDependencyRouting         = "routing"
	pathDependencyForwardingProbe = "forwarding-probe"
	pathDependencyFirewall        = "firewall"
	pathDependencyInternalLink    = "internal-link"
	pathDependencyReconcile       = "reconcile"
	pathDependencyForwarding      = "forwarding"

	pathReasonStartup             = "first evaluation not complete"
	pathReasonFamilyDisabled      = "address family disabled"
	pathReasonRoutingFailed       = "routing evaluation failed"
	pathReasonRoutingNotPublished = "routing evaluation not published"
	pathReasonRoutingMemberAbsent = "routing evaluation has no result for the provider"
	pathReasonProbePending        = "probe pending"
	pathReasonProbeFailed         = "probe failed"
	pathReasonFirewallPending     = "firewall not evaluated"
	pathReasonFirewallNotReady    = "firewall protection not ready"
	pathReasonInternalLink        = "internal link not ready"
	pathReasonReconcileFailed     = "module reconcile failed"
	pathReasonForwarding          = "forwarding not ready"

	firewallUnknown = "unknown"
	firewallReady   = "ready"

	roleWAN        = "wan"
	familyNameIPv4 = "ipv4"
	familyNameIPv6 = "ipv6"
)

type routingState uint8

const (
	routingPublished routingState = iota + 1
	routingFailed
	routingNotPublished
	routingMemberAbsent
)

type routingEvaluation struct {
	state   routingState
	failure error
}

func newRoutingEvaluation(published bool, failure error) routingEvaluation {
	switch {
	case published:
		return routingEvaluation{state: routingPublished, failure: nil}
	case failure != nil:
		return routingEvaluation{state: routingFailed, failure: failure}
	}
	return routingEvaluation{state: routingNotPublished, failure: nil}
}

func (evaluation routingEvaluation) forMember(memberPublished bool) routingEvaluation {
	if evaluation.state == routingPublished && !memberPublished {
		evaluation.state = routingMemberAbsent
	}
	return evaluation
}

type probeState uint8

const (
	probeNotConfigured probeState = iota + 1
	probePending
	probeFailed
	probePassed
)

func familyProbeState(probed bool, member wanstate.Health, result wanstate.ProbeResult, family wanstate.Health) probeState {
	memberStates := map[wanstate.Health]probeState{wanstate.HealthHealthy: probePassed, wanstate.HealthUnhealthy: probeFailed}
	memberState, decided := memberStates[member]
	familyState, familyDecided := memberStates[family]
	familyProbed := result == wanstate.ProbePass || result == wanstate.ProbeFail
	switch {
	case !probed:
		return probeNotConfigured
	case !decided:
		return probePending
	case memberState == probeFailed || !familyProbed:
		return memberState
	case !familyDecided:
		return probePending
	}
	return familyState
}

func met(name string) wanstate.DependencyResult {
	return wanstate.DependencyResult{Name: name, State: wanstate.DependencyMet, Reason: ""}
}

func pending(name, reason string) wanstate.DependencyResult {
	return wanstate.DependencyResult{Name: name, State: wanstate.DependencyPending, Reason: reason}
}

func unmet(name, reason string) wanstate.DependencyResult {
	return wanstate.DependencyResult{Name: name, State: wanstate.DependencyUnmet, Reason: reason}
}

func enabledResult(enabled bool) wanstate.DependencyResult {
	if enabled {
		return met(pathDependencyFamilyEnabled)
	}
	return unmet(pathDependencyFamilyEnabled, pathReasonFamilyDisabled)
}

func routingResult(evaluation routingEvaluation) wanstate.DependencyResult {
	switch evaluation.state {
	case routingPublished:
		return met(pathDependencyRouting)
	case routingFailed:
		return unmet(pathDependencyRouting, pathReasonRoutingFailed+": "+evaluation.failure.Error())
	case routingNotPublished:
		return pending(pathDependencyRouting, pathReasonRoutingNotPublished)
	case routingMemberAbsent:
		return unmet(pathDependencyRouting, pathReasonRoutingMemberAbsent)
	}
	return unmet(pathDependencyRouting, pathReasonRoutingFailed)
}

func probeResult(state probeState) wanstate.DependencyResult {
	switch state {
	case probeNotConfigured, probePassed:
		return met(pathDependencyForwardingProbe)
	case probePending:
		return pending(pathDependencyForwardingProbe, pathReasonProbePending)
	case probeFailed:
		return unmet(pathDependencyForwardingProbe, pathReasonProbeFailed)
	}
	return unmet(pathDependencyForwardingProbe, pathReasonProbeFailed)
}

func firewallResult(protection string) wanstate.DependencyResult {
	switch protection {
	case firewallReady:
		return met(pathDependencyFirewall)
	case firewallUnknown:
		return pending(pathDependencyFirewall, pathReasonFirewallPending)
	}
	return unmet(pathDependencyFirewall, pathReasonFirewallNotReady)
}

func flagResult(name, reason string, satisfied bool) wanstate.DependencyResult {
	if satisfied {
		return met(name)
	}
	return unmet(name, reason)
}

func pathVerdict(ready bool, dependencies []wanstate.DependencyResult) wanstate.PathVerdict {
	if ready {
		return wanstate.PathVerdict{State: wanstate.PathReady, Dependency: "", Reason: ""}
	}
	for _, dependency := range dependencies {
		switch dependency.State {
		case wanstate.DependencyPending:
			return wanstate.PathVerdict{State: wanstate.PathStarting, Dependency: dependency.Name, Reason: dependency.Reason}
		case wanstate.DependencyUnmet:
			return wanstate.PathVerdict{State: wanstate.PathUnavailable, Dependency: dependency.Name, Reason: dependency.Reason}
		case wanstate.DependencyMet, wanstate.DependencyNotEvaluated:
		}
	}
	return wanstate.PathVerdict{State: wanstate.PathUnavailable, Dependency: pathDependencyForwarding, Reason: pathReasonForwarding}
}

func (d *Daemon) publishStartingPaths() {
	if d.role != roleWAN || d.cfg.LiveState == nil {
		return
	}
	verdict := wanstate.PathVerdict{State: wanstate.PathStarting, Dependency: pathDependencyStartup, Reason: pathReasonStartup}
	for _, connection := range d.cfg.Connections {
		if connection.Roles&interfaceintent.RoleProvider == 0 {
			continue
		}
		id := connection.ID.String()
		if connection.IPv4 != nil {
			d.cfg.LiveState.SetPathReadiness(id, familyNameIPv4, verdict)
		}
		if connection.IPv6 != nil {
			d.cfg.LiveState.SetPathReadiness(id, familyNameIPv6, verdict)
		}
	}
}
