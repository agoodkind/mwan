package wanroutes

const (
	dependencyTunnel        = "tunnel"
	dependencyUnderlay      = "underlay"
	dependencyEndpointRoute = "endpoint-route"
	dependencyBGPSession    = "bgp-session"
	dependencyBGPRoute      = "bgp-route"
	dependencyTranslation   = "translation"
	dependencyGateway       = "gateway"
	dependencyProbe         = "probe"
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
