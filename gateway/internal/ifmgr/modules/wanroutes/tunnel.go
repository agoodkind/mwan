package wanroutes

import (
	"fmt"
)

const (
	reasonTunnelLinkAbsent    = "tunnel link absent"
	reasonUnderlayNotReady    = "underlay not ready"
	reasonEndpointRouteAbsent = "endpoint route absent"
	reasonProbePending        = "probe pending"
	reasonProbeFailed         = "probe failed"
	reasonTranslationNotReady = "translation not ready"
	reasonGatewayUnavailable  = "gateway unavailable"
)

func tunnelUnderlay(cfg Config, wan WAN) (WAN, bool) {
	var none WAN
	if wan.Tunnel == nil {
		return none, false
	}
	for _, candidate := range cfg.WANs {
		if candidate.Iface == wan.Tunnel.Underlay {
			return candidate, true
		}
	}
	return none, false
}

func validateTunnelWAN(cfg Config, wan WAN) error {
	if wan.Tunnel == nil {
		return nil
	}
	if !wan.Tunnel.Remote.Is4() {
		return fmt.Errorf("tunnel remote address must be IPv4")
	}
	if wan.TranslationV4 != nil {
		return fmt.Errorf("tunnel provider cannot include an IPv4 family")
	}
	underlay, found := tunnelUnderlay(cfg, wan)
	if !found || underlay.Tunnel != nil || underlay.TranslationV4 == nil {
		return fmt.Errorf("tunnel underlay %q must be a provider with an IPv4 family", wan.Tunnel.Underlay)
	}
	return nil
}
