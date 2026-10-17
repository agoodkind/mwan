package main

import (
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/bgpsessions"
	"goodkind.io/mwan/internal/interfaceintent"
)

const kernelDefaultIPv6Metric = 1024

func ipv6MainMetric(metric uint32) int {
	if metric == 0 {
		return kernelDefaultIPv6Metric
	}
	return int(metric)
}

func configuredIPv6Routes(connections []interfaceintent.Connection) []bgpsessions.ConfiguredRoute {
	var routes []bgpsessions.ConfiguredRoute
	for _, connection := range connections {
		if connection.IPv6 == nil {
			continue
		}
		family := connection.IPv6.Family
		metric := uint32(0)
		if family.RouteMetric != nil {
			metric = *family.RouteMetric
		}
		if family.Gateway.IsValid() || family.RouteMetric != nil {
			via := ""
			if family.Gateway.IsValid() {
				via = family.Gateway.String()
			}
			routes = append(routes, bgpsessions.ConfiguredRoute{
				Interface: connection.Name, Dest: netip.PrefixFrom(netip.IPv6Unspecified(), 0).String(), Via: via, Metric: ipv6MainMetric(metric),
			})
		}
		for _, route := range family.Routes {
			via := ""
			if route.Gateway.IsValid() {
				via = route.Gateway.String()
			}
			routes = append(routes, bgpsessions.ConfiguredRoute{
				Interface: connection.Name, Dest: route.Destination.Masked().String(), Via: via, Metric: ipv6MainMetric(route.Metric),
			})
		}
	}
	return routes
}

func buildBGPSessionsConfig(
	shared sharedWANInputs,
	section *config.IfMgrBGPSessionsSection,
	connections []interfaceintent.Connection,
) bgpsessions.Config {
	cfg := bgpsessions.Config{Sessions: nil, StateFile: "", ConfiguredRoutes: configuredIPv6Routes(connections)}
	if section != nil {
		cfg.StateFile = section.StateFile
	}
	tables := make(map[string]int, len(shared.WANs))
	for _, wan := range shared.WANs {
		tables[wan.Iface] = wan.TableID
	}
	for _, wan := range shared.WANs {
		for _, configured := range wan.BGPSessions {
			session := bgpsessions.Session{
				WANRef: wan.WANRef, Config: configured.Session, BackupFor: configured.BackupFor,
				EndpointDest: "", EndpointTable: 0,
			}
			if wan.Tunnel != nil {
				remote := wan.Tunnel.Remote
				session.EndpointDest = netip.PrefixFrom(remote, remote.BitLen()).String()
				session.EndpointTable = tables[wan.Tunnel.Underlay]
			}
			cfg.Sessions = append(cfg.Sessions, session)
		}
	}
	return cfg
}

// withSessionRouterID uses [bgp] router_id because the network schema does not define a
// router ID for each session.
func withSessionRouterID(
	settings bgpsessions.Config,
	routerIDText string,
	logger *slog.Logger,
) (bgpsessions.Config, error) {
	if len(settings.Sessions) == 0 {
		return settings, nil
	}
	routerID, err := netip.ParseAddr(routerIDText)
	if err == nil {
		err = bgpsession.ValidateRouterID(routerID)
	}
	if err != nil {
		logger.Warn("ifmgr: BGP session router ID rejected", "router_id", routerIDText, "err", err)
		return settings, fmt.Errorf("invalid [bgp] router_id %q for external bgp sessions: %w", routerIDText, err)
	}
	sessions := make([]bgpsessions.Session, 0, len(settings.Sessions))
	for _, session := range settings.Sessions {
		session.Config.RouterID = routerID
		sessions = append(sessions, session)
	}
	settings.Sessions = sessions
	return settings, nil
}

func bgpRouteMetrics(sessions []config.BGPSession) []int {
	var metrics []int
	for _, session := range sessions {
		metrics = append(metrics, int(session.Session.RouteMetric))
	}
	return metrics
}

func bgpSessionsFromConfig(cfg *config.Config, name string) []config.BGPSession {
	if cfg == nil {
		return nil
	}
	return cfg.IfMgr.WAN[name].BGPSessions
}
