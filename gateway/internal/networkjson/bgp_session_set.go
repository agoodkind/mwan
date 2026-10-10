package networkjson

import (
	"fmt"

	"goodkind.io/mwan/internal/connectionid"
)

func configuredIPv6RouteMetrics(loaded *Config) map[uint32]string {
	owners := make(map[uint32]string)
	for _, connection := range loaded.Connections {
		if connection.IPv6 == nil {
			continue
		}
		label := "a configured ipv6 route of interface " + connection.Name
		family := connection.IPv6.Family
		if family.RouteMetric != nil {
			owners[mainRouteMetric("ipv6", *family.RouteMetric)] = label
		} else if family.Gateway.IsValid() {
			owners[mainRouteMetric("ipv6", 0)] = label
		}
		for _, route := range family.Routes {
			owners[mainRouteMetric("ipv6", route.Metric)] = label
		}
	}
	return owners
}

func checkBGPSessionSet(loaded *Config, names []string) error {
	known := make(map[connectionid.ID]bool, len(loaded.Connections))
	for _, connection := range loaded.Connections {
		known[connection.ID] = true
	}
	metricOwners := configuredIPv6RouteMetrics(loaded)
	for _, name := range names {
		for _, session := range loaded.WAN[name].BGPSessions {
			label := fmt.Sprintf("wan %s bgp-session %s", name, session.Session.Name)
			metric := session.Session.RouteMetric
			if owner, taken := metricOwners[metric]; taken {
				return fmt.Errorf("%s: route-metric %d is already taken by %s", label, metric, owner)
			}
			metricOwners[metric] = label
			for prefix, backedUp := range session.BackupFor {
				for _, id := range backedUp {
					if id.String() == name {
						return fmt.Errorf("%s: export %s backup-for %s is the session's own connection", label, prefix, id)
					}
					if !known[id] {
						return fmt.Errorf("%s: export %s backup-for %s is not a connection ID", label, prefix, id)
					}
				}
			}
		}
	}
	return nil
}
