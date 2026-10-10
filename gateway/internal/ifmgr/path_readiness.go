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

	pathReasonStartup          = "first evaluation not complete"
	pathReasonFamilyDisabled   = "address family disabled"
	pathReasonRoutingFailed    = "routing evaluation failed"
	pathReasonProbePending     = "probe pending"
	pathReasonProbeFailed      = "probe failed"
	pathReasonFirewallPending  = "firewall not evaluated"
	pathReasonFirewallNotReady = "firewall protection not ready"
	pathReasonInternalLink     = "internal link not ready"
	pathReasonReconcileFailed  = "module reconcile failed"
	pathReasonForwarding       = "forwarding not ready"

	firewallUnknown = "unknown"
	firewallReady   = "ready"

	roleWAN        = "wan"
	familyNameIPv4 = "ipv4"
	familyNameIPv6 = "ipv6"
)

type routingEvaluation struct {
	published bool
	failure   error
}

type familyHealth struct {
	probed  bool
	member  wanstate.Health
	result  wanstate.ProbeResult
	verdict wanstate.Health
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

func routingResult(evaluation routingEvaluation, memberPublished bool) wanstate.DependencyResult {
	switch {
	case evaluation.published && memberPublished:
		return met(pathDependencyRouting)
	case evaluation.failure != nil:
		return unmet(pathDependencyRouting, pathReasonRoutingFailed+": "+evaluation.failure.Error())
	}
	return unmet(pathDependencyRouting, pathReasonRoutingFailed)
}

func probeResult(health familyHealth) wanstate.DependencyResult {
	familyProbed := health.result == wanstate.ProbePass || health.result == wanstate.ProbeFail
	switch {
	case !health.probed:
		return met(pathDependencyForwardingProbe)
	case health.member == wanstate.HealthUnhealthy:
		return unmet(pathDependencyForwardingProbe, pathReasonProbeFailed)
	case health.member != wanstate.HealthHealthy:
		return pending(pathDependencyForwardingProbe, pathReasonProbePending)
	case familyProbed && health.verdict == wanstate.HealthUnhealthy:
		return unmet(pathDependencyForwardingProbe, pathReasonProbeFailed)
	case familyProbed && health.verdict != wanstate.HealthHealthy:
		return pending(pathDependencyForwardingProbe, pathReasonProbePending)
	}
	return met(pathDependencyForwardingProbe)
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
