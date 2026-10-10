package wanroutes

import "goodkind.io/mwan/internal/wanstate"

const (
	dependencyConfiguration = "configuration"
	dependencyTunnel        = "tunnel"
	dependencyUnderlay      = "underlay"
	dependencyEndpointRoute = "endpoint-route"
	dependencyBGPSession    = "bgp-session"
	dependencyBGPRoute      = "bgp-route"
	dependencyProbe         = "probe"
	dependencyTranslation   = "translation"
	dependencyGateway       = "gateway"

	reasonFamilyNotRouted = "address family has no routing configuration"
)

func reasonDependency(reason string) string {
	switch reason {
	case reasonTunnelLinkAbsent:
		return dependencyTunnel
	case reasonUnderlayNotReady:
		return dependencyUnderlay
	case reasonEndpointRouteAbsent:
		return dependencyEndpointRoute
	case reasonBGPNotEstablished:
		return dependencyBGPSession
	case reasonBGPNoAcceptedRoute:
		return dependencyBGPRoute
	case reasonTranslationNotReady:
		return dependencyTranslation
	case reasonGatewayUnavailable:
		return dependencyGateway
	case reasonProbePending, reasonProbeFailed:
		return dependencyProbe
	}
	return ""
}

func dependencyOrder(wan WAN, family string) []string {
	var order []string
	if family == familyV6 && wan.Tunnel != nil {
		order = append(order, dependencyTunnel, dependencyUnderlay, dependencyEndpointRoute)
	}
	if family == familyV6 && wan.requiresBGP() {
		order = append(order, dependencyBGPSession, dependencyBGPRoute)
	}
	return append(order, dependencyProbe, dependencyTranslation, dependencyGateway)
}

func familyDependencies(wan WAN, family, reason string) []wanstate.DependencyResult {
	if !familyConfigured(wan, family) {
		return []wanstate.DependencyResult{{Name: dependencyConfiguration, State: wanstate.DependencyUnmet, Reason: reasonFamilyNotRouted}}
	}
	results := []wanstate.DependencyResult{{Name: dependencyConfiguration, State: wanstate.DependencyMet, Reason: ""}}
	unmet := reasonDependency(reason)
	state := wanstate.DependencyMet
	for _, name := range dependencyOrder(wan, family) {
		if state == wanstate.DependencyMet && name == unmet {
			result := wanstate.DependencyResult{Name: name, State: wanstate.DependencyUnmet, Reason: reason}
			if reason == reasonProbePending {
				result.State = wanstate.DependencyPending
			}
			results = append(results, result)
			state = wanstate.DependencyNotEvaluated
			continue
		}
		results = append(results, wanstate.DependencyResult{Name: name, State: state, Reason: ""})
	}
	return results
}
