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

type dependencyRule struct {
	name    string
	applies func(wan WAN, family string) bool
	states  map[string]wanstate.DependencyState
}

func appliesAlways(WAN, string) bool {
	return true
}

func appliesV6Tunnel(wan WAN, family string) bool {
	return family == familyV6 && wan.Tunnel != nil
}

func appliesV6BGP(wan WAN, family string) bool {
	return family == familyV6 && wan.requiresBGP()
}

func unmetBy(reason string) map[string]wanstate.DependencyState {
	return map[string]wanstate.DependencyState{reason: wanstate.DependencyUnmet}
}

func dependencyRules() []dependencyRule {
	probeStates := map[string]wanstate.DependencyState{
		reasonProbePending: wanstate.DependencyPending, reasonProbeFailed: wanstate.DependencyUnmet,
	}
	return []dependencyRule{
		{name: dependencyTunnel, applies: appliesV6Tunnel, states: unmetBy(reasonTunnelLinkAbsent)},
		{name: dependencyUnderlay, applies: appliesV6Tunnel, states: unmetBy(reasonUnderlayNotReady)},
		{name: dependencyEndpointRoute, applies: appliesV6Tunnel, states: unmetBy(reasonEndpointRouteAbsent)},
		{name: dependencyBGPSession, applies: appliesV6BGP, states: unmetBy(reasonBGPNotEstablished)},
		{name: dependencyBGPRoute, applies: appliesV6BGP, states: unmetBy(reasonBGPNoAcceptedRoute)},
		{name: dependencyProbe, applies: appliesAlways, states: probeStates},
		{name: dependencyTranslation, applies: appliesAlways, states: unmetBy(reasonTranslationNotReady)},
		{name: dependencyGateway, applies: appliesAlways, states: unmetBy(reasonGatewayUnavailable)},
	}
}

func familyDependencies(wan WAN, family, reason string) []wanstate.DependencyResult {
	if !familyConfigured(wan, family) {
		return []wanstate.DependencyResult{{Name: dependencyConfiguration, State: wanstate.DependencyUnmet, Reason: reasonFamilyNotRouted}}
	}
	results := []wanstate.DependencyResult{{Name: dependencyConfiguration, State: wanstate.DependencyMet, Reason: ""}}
	following := wanstate.DependencyMet
	for _, rule := range dependencyRules() {
		if !rule.applies(wan, family) {
			continue
		}
		state, reported := rule.states[reason]
		if following != wanstate.DependencyMet || !reported {
			results = append(results, wanstate.DependencyResult{Name: rule.name, State: following, Reason: ""})
			continue
		}
		results = append(results, wanstate.DependencyResult{Name: rule.name, State: state, Reason: reason})
		following = wanstate.DependencyNotEvaluated
	}
	return results
}
