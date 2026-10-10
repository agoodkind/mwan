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

// A default route through the tunnel does not show that the remote endpoint forwards packets.
// A tunnel provider requires a healthy probe verdict before selection.
// The routing module clears the IPv6 gateway of each excluded tunnel in current.
// The returned map lists the unmet dependency of each excluded tunnel by connection ID.
// Callers must lock the module.
func (m *Module) excludeUnreadyTunnelFamilies(current gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation) map[string]string {
	reasons := make(map[string]string)
	for _, wan := range m.cfg.WANs {
		if wan.Tunnel == nil {
			continue
		}
		underlay, found := tunnelUnderlay(m.cfg, wan)
		state := health.State(wan.Key())
		reason := ""
		switch {
		case !m.tunnelLinkApplied(wan):
			reason = reasonTunnelLinkAbsent
		case !found || !familyReady(underlay, current[underlay.Key()], health, translations[underlay.Key()], familyV4):
			reason = reasonUnderlayNotReady
		case !m.endpointRouteInstalled(wan):
			reason = reasonEndpointRouteAbsent
		case state == netif.HealthStateUnhealthy:
			reason = reasonProbeFailed
		case state != netif.HealthStateHealthy:
			reason = reasonProbePending
		}
		if reason == "" {
			continue
		}
		reasons[wan.Key()] = reason
		gateway := current[wan.Key()]
		gateway.V6 = ""
		current[wan.Key()] = gateway
	}
	return reasons
}

func (m *Module) endpointRouteInstalled(wan WAN) bool {
	_, installed := m.endpointRoutes[wan.Key()]
	return installed
}

// The reason is empty for a ready family and for an unconfigured family.
func familyReason(wan WAN, gateways gatewaySet, health netif.HealthStates, translation wanstate.MemberTranslation, family string, tunnelReason string) string {
	if !familyConfigured(wan, family) {
		return ""
	}
	gateway := gateways.V4
	translationReady := translation.V4.Ready
	if family == familyV6 {
		gateway = gateways.V6
		translationReady = translation.V6.Ready
		if tunnelReason != "" {
			return tunnelReason
		}
	}
	switch {
	case !translationReady:
		return reasonTranslationNotReady
	case gateway == "":
		return reasonGatewayUnavailable
	case !netif.HealthIsHealthy(health.State(wan.Key())):
		return reasonProbeFailed
	}
	return ""
}
