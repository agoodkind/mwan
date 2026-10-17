package wanroutes

import "goodkind.io/mwan/internal/bgp"

const (
	reasonBGPNotEstablished  = "bgp session not established"
	reasonBGPNoAcceptedRoute = "bgp no accepted route"
)

func (w WAN) requiresBGP() bool {
	return len(w.BGPRouteMetrics) > 0
}

func (g gatewaySet) withoutV6() gatewaySet {
	g.V6 = ""
	g.V6Routed = false
	return g
}

func (m *Module) bgpSessionStates(wan WAN) []bgp.SessionState {
	if m.Env == nil || m.Env.LiveState == nil {
		return nil
	}
	return m.Env.LiveState.BGPSessions(wan.Key())
}

func bgpReason(sessions []bgp.SessionState) string {
	if len(sessions) == 0 {
		return reasonBGPNotEstablished
	}
	acceptedCount := 0
	for _, session := range sessions {
		// A session that kept the routes of the previous process counts as
		// established until the peer sends the routes again or the restart
		// ends. The forwarding probe still decides readiness.
		if !session.Established && len(session.Retained) == 0 {
			return reasonBGPNotEstablished
		}
		acceptedCount += len(session.Accepted) + len(session.Retained)
	}
	if acceptedCount == 0 {
		return reasonBGPNoAcceptedRoute
	}
	return ""
}
