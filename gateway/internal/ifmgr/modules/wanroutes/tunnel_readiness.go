package wanroutes

import (
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

func (m *Module) tunnelLinkApplied(wan WAN) bool {
	if m.Env == nil || m.Env.OwnedLinks == nil {
		return false
	}
	result, found := m.Env.OwnedLinks.Get(wan.Key())
	return found && result.Status == netif.OwnedLinkReady
}

// Callers must lock the module.
func (m *Module) excludeUnreadyTunnelFamilies(current gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation) map[string]string {
	reasons := make(map[string]string)
	for _, wan := range m.cfg.WANs {
		if wan.Tunnel == nil && !wan.requiresBGP() {
			continue
		}
		reason := m.tunnelPathReason(wan, current, health, translations)
		if reason == "" && wan.requiresBGP() {
			reason = bgpReason(m.bgpSessionStates(wan))
		}
		if reason == "" && wan.Tunnel != nil {
			reason = tunnelProbeReason(health.State(wan.Key()))
		}
		if reason == "" {
			continue
		}
		reasons[wan.Key()] = reason
		current[wan.Key()] = current[wan.Key()].withoutV6()
	}
	return reasons
}

func (m *Module) tunnelPathReason(wan WAN, current gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation) string {
	if wan.Tunnel == nil {
		return ""
	}
	underlay, found := tunnelUnderlay(m.cfg, wan)
	switch {
	case !m.tunnelLinkApplied(wan):
		return reasonTunnelLinkAbsent
	case !found || !familyReady(underlay, current[underlay.Key()], health, translations[underlay.Key()], familyV4):
		return reasonUnderlayNotReady
	case !m.endpointRouteInstalled(wan):
		return reasonEndpointRouteAbsent
	}
	return ""
}

func tunnelProbeReason(state string) string {
	if state == netif.HealthStateUnhealthy {
		return reasonProbeFailed
	}
	if state != netif.HealthStateHealthy {
		return reasonProbePending
	}
	return ""
}

func (m *Module) endpointRouteInstalled(wan WAN) bool {
	_, installed := m.endpointRoutes[wan.Key()]
	return installed
}

func familyReason(wan WAN, gateways gatewaySet, health netif.HealthStates, translation wanstate.MemberTranslation, family string, tunnelReason string) string {
	if !familyConfigured(wan, family) {
		return ""
	}
	gatewayKnown := gateways.V4 != ""
	translationReady := translation.V4.Ready
	if family == familyV6 {
		gatewayKnown = gateways.V6 != "" || gateways.V6Routed
		translationReady = translation.V6.Ready
		if tunnelReason != "" {
			return tunnelReason
		}
	}
	switch {
	case !translationReady:
		return reasonTranslationNotReady
	case !gatewayKnown:
		return reasonGatewayUnavailable
	case !netif.HealthIsHealthy(health.State(wan.Key())):
		return reasonProbeFailed
	}
	return ""
}
