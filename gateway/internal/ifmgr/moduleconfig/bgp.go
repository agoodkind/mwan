package moduleconfig

// BGP session configuration combines WAN providers with host IPv6 routes.
// For a Linux tunnel provider it can put the BGP endpoint in the underlay
// routing table. An OVS session does not use that policy routing setup.

import (
	"net/netip"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/bgpsessions"
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

			routes = append(
				routes,
				bgpsessions.ConfiguredRoute{
					Interface: connection.Name,
					Dest:      netip.PrefixFrom(netip.IPv6Unspecified(), 0).String(),
					Via:       via,
					Metric:    ipv6MainMetric(metric),
				},
			)
		}

		for _, route := range family.Routes {
			via := ""
			if route.Gateway.IsValid() {
				via = route.Gateway.String()
			}

			routes = append(
				routes,
				bgpsessions.ConfiguredRoute{
					Interface: connection.Name,
					Dest:      route.Destination.Masked().String(),
					Via:       via,
					Metric:    ipv6MainMetric(route.Metric),
				},
			)
		}
	}

	return routes
}

func buildBGPSessionsConfig(
	shared WANInputs,
	section *config.IfMgrBGPSessionsSection,
	connections []interfaceintent.Connection,
	ovsDataplane bool,
) bgpsessions.Config {
	cfg := bgpsessions.Config{
		OVSDataplane:     ovsDataplane,
		Sessions:         nil,
		StateFile:        "",
		ConfiguredRoutes: configuredIPv6Routes(connections),
	}
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
				WANRef:        wan.WANRef,
				Config:        configured.Session,
				BackupFor:     configured.BackupFor,
				EndpointDest:  "",
				EndpointTable: 0,
			}
			if wan.Tunnel != nil && !ovsDataplane {
				remote := wan.Tunnel.Remote
				session.EndpointDest = netip.PrefixFrom(remote, remote.BitLen()).String()
				session.EndpointTable = tables[wan.Tunnel.Underlay]
			}

			cfg.Sessions = append(cfg.Sessions, session)
		}
	}

	return cfg
}

func bgpRouteMetrics(sessions []config.BGPSession) []int {
	if len(sessions) == 0 {
		return nil
	}

	metrics := make([]int, 0, len(sessions))

	for _, session := range sessions {
		metrics = append(metrics, int(session.Session.RouteMetric))
	}

	return metrics
}
