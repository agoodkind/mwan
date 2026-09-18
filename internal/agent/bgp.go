package agent

import (
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/config"
)

func newBGPSpeaker(cfg *config.Config, log *slog.Logger) (*bgp.Speaker, error) {
	if !cfg.BGP.Enabled {
		return nil, nil
	}
	bgpCfg := bgp.Config{
		Enabled:          true,
		ASN:              cfg.BGP.ASN,
		RouterID:         cfg.BGP.RouterID,
		NextHopV6:        cfg.BGP.NextHopV6,
		KeepaliveSeconds: cfg.BGP.KeepaliveSeconds,
		HoldSeconds:      cfg.BGP.HoldSeconds,
		ListenPort:       cfg.BGP.ListenPort,
		Announce: bgp.AnnounceConfig{
			IPv4: cfg.BGP.Announce.IPv4,
			IPv6: cfg.BGP.Announce.IPv6,
		},
		GracefulRestart: bgp.GracefulRestartConfig{
			Enabled:             cfg.BGP.GracefulRestart.Enabled,
			RestartTime:         cfg.BGP.GracefulRestart.RestartTime,
			NotificationEnabled: cfg.BGP.GracefulRestart.NotificationEnabled,
		},
	}
	for _, neighbor := range cfg.BGP.Neighbors {
		bgpCfg.Neighbors = append(bgpCfg.Neighbors, bgp.NeighborConfig{Address: neighbor.Address})
	}
	for _, neighbor := range cfg.BGP.NeighborsV6 {
		bgpCfg.NeighborsV6 = append(bgpCfg.NeighborsV6, bgp.NeighborConfig{Address: neighbor.Address})
	}
	for _, prefixText := range cfg.BGP.DynamicNeighbors {
		prefix, err := netip.ParsePrefix(prefixText)
		if err != nil {
			log.Error("parse BGP dynamic neighbor failed", "dynamic_neighbor", prefixText, "error", err)
			return nil, fmt.Errorf("parse BGP dynamic neighbor %q: %w", prefixText, err)
		}
		bgpCfg.DynamicNeighborPrefixes = append(bgpCfg.DynamicNeighborPrefixes, prefix.Masked())
	}
	return bgp.New(bgpCfg, log), nil
}
