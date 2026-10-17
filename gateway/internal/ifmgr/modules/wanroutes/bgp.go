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
		// A session with retained prefixes counts as established. The kernel
		// retains the routes that the previous process installed until the
		// peer sends the prefixes again or the restart time ends. The
		// forwarding probe still decides readiness.
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
