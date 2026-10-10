package ifmgr

import (
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	pathDependencyStartup       = "startup"
	pathDependencyConfiguration = "configuration"
	pathDependencyRouting       = "routing"
	pathDependencyProbe         = "probe"
	pathDependencyFirewall      = "firewall"
	pathDependencyInternalLink  = "internal-link"
	pathDependencyReconcile     = "reconcile"
	pathDependencyForwarding    = "forwarding"

	pathReasonStartup          = "first evaluation not complete"
	pathReasonFamilyDisabled   = "address family disabled"
	pathReasonFamilyNotRouted  = "address family has no routing configuration"
	pathReasonRoutingFailed    = "routing evaluation failed"
	pathReasonRoutingNotReady  = "routing not ready"
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

type pathInputs struct {
	enabled        bool
	configured     bool
	routingKnown   bool
	routingFresh   bool
	routingFailure string
	routingReady   bool
	dependency     string
	reason         string
	healthKnown    bool
	health         wanstate.Health
	probe          wanstate.ProbeResult
	familyHealth   wanstate.Health
	firewall       string
	internalReady  bool
	moduleFailed   bool
}

func starting(dependency, reason string) wanstate.PathVerdict {
	return wanstate.PathVerdict{State: wanstate.PathStarting, Dependency: dependency, Reason: reason}
}

func unavailable(dependency, reason string) wanstate.PathVerdict {
	return wanstate.PathVerdict{State: wanstate.PathUnavailable, Dependency: dependency, Reason: reason}
}

func undecided() (wanstate.PathVerdict, bool) {
	return wanstate.PathVerdict{State: "", Dependency: "", Reason: ""}, false
}

func (in pathInputs) probePending() bool {
	return in.healthKnown && in.health != wanstate.HealthHealthy && in.health != wanstate.HealthUnhealthy
}

func routingVerdict(in pathInputs) (wanstate.PathVerdict, bool) {
	switch {
	case !in.routingFresh || !in.routingKnown:
		reason := pathReasonRoutingFailed
		if in.routingFailure != "" {
			reason += ": " + in.routingFailure
		}
		return unavailable(pathDependencyRouting, reason), true
	case !in.configured:
		return unavailable(pathDependencyConfiguration, pathReasonFamilyNotRouted), true
	case in.routingReady:
		return undecided()
	case in.reason == pathReasonProbePending || (in.dependency == pathDependencyProbe && in.probePending()):
		return starting(pathDependencyProbe, pathReasonProbePending), true
	case in.dependency == "" || in.reason == "":
		return unavailable(pathDependencyRouting, pathReasonRoutingNotReady), true
	}
	return unavailable(in.dependency, in.reason), true
}

func probeVerdict(in pathInputs) (wanstate.PathVerdict, bool) {
	familyProbed := in.probe == wanstate.ProbePass || in.probe == wanstate.ProbeFail
	switch {
	case !in.healthKnown:
		return undecided()
	case in.probePending():
		return starting(pathDependencyProbe, pathReasonProbePending), true
	case in.health != wanstate.HealthHealthy:
		return unavailable(pathDependencyProbe, pathReasonProbeFailed), true
	case familyProbed && in.familyHealth == wanstate.HealthUnhealthy:
		return unavailable(pathDependencyProbe, pathReasonProbeFailed), true
	case familyProbed && in.familyHealth != wanstate.HealthHealthy:
		return starting(pathDependencyProbe, pathReasonProbePending), true
	}
	return undecided()
}

func pathVerdict(ready bool, in pathInputs) wanstate.PathVerdict {
	if ready {
		return wanstate.PathVerdict{State: wanstate.PathReady, Dependency: "", Reason: ""}
	}
	if !in.enabled {
		return unavailable(pathDependencyConfiguration, pathReasonFamilyDisabled)
	}
	if verdict, decided := routingVerdict(in); decided {
		return verdict
	}
	if verdict, decided := probeVerdict(in); decided {
		return verdict
	}
	switch {
	case in.firewall == firewallUnknown:
		return starting(pathDependencyFirewall, pathReasonFirewallPending)
	case in.firewall != firewallReady:
		return unavailable(pathDependencyFirewall, pathReasonFirewallNotReady)
	case !in.internalReady:
		return unavailable(pathDependencyInternalLink, pathReasonInternalLink)
	case in.moduleFailed:
		return unavailable(pathDependencyReconcile, pathReasonReconcileFailed)
	}
	return unavailable(pathDependencyForwarding, pathReasonForwarding)
}

func (d *Daemon) publishStartingPaths() {
	if d.role != roleWAN || d.cfg.LiveState == nil {
		return
	}
	for _, connection := range d.cfg.Connections {
		if connection.Roles&interfaceintent.RoleProvider == 0 {
			continue
		}
		id := connection.ID.String()
		if connection.IPv4 != nil {
			d.cfg.LiveState.SetPathReadiness(id, familyNameIPv4, starting(pathDependencyStartup, pathReasonStartup))
		}
		if connection.IPv6 != nil {
			d.cfg.LiveState.SetPathReadiness(id, familyNameIPv6, starting(pathDependencyStartup, pathReasonStartup))
		}
	}
}
