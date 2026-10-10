package wanconfig

import (
	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/config"
)

func bgpSessionItems(base string, sessions []config.BGPSession) []Item {
	var items []Item
	for _, configured := range sessions {
		session := configured.Session
		path := base + "/bgp-session[name='" + session.Name + "']"
		items = append(items,
			Item{Path: path + "/peer-address", Value: session.PeerAddress.String()},
			Item{Path: path + "/peer-port", Value: uintValue(uint64(session.PeerPort))},
			Item{Path: path + "/local-as", Value: uintValue(uint64(session.LocalASN))},
			Item{Path: path + "/remote-as", Value: uintValue(uint64(session.RemoteASN))},
			Item{Path: path + "/router-id", Value: session.RouterID.String()},
			Item{Path: path + "/keepalive", Value: uintValue(uint64(session.KeepaliveSeconds))},
			Item{Path: path + "/hold", Value: uintValue(uint64(session.HoldSeconds))},
			Item{Path: path + "/route-metric", Value: uintValue(uint64(session.RouteMetric))},
		)
		if session.LocalAddress.IsValid() {
			items = append(items, Item{Path: path + "/local-address", Value: session.LocalAddress.String()})
		}
		if session.MultihopTTL != 0 {
			items = append(items, Item{Path: path + "/multihop-ttl", Value: uintValue(uint64(session.MultihopTTL))})
		}
		for _, rule := range session.Import {
			rulePath := path + "/import[prefix='" + rule.Prefix.String() + "']"
			items = append(items,
				Item{Path: rulePath + "/min-length", Value: uintValue(uint64(rule.MinLength))},
				Item{Path: rulePath + "/max-length", Value: uintValue(uint64(rule.MaxLength))},
			)
		}
		for _, rule := range session.Export {
			items = append(items, bgpExportItems(path, rule, configured)...)
		}
	}
	return items
}

func bgpExportItems(sessionPath string, rule bgpsession.ExportRule, configured config.BGPSession) []Item {
	path := sessionPath + "/export[prefix='" + rule.Prefix.String() + "']"
	items := []Item{
		{Path: path + "/mode", Value: rule.Mode.String()},
		{Path: path + "/next-hop", Value: rule.NextHop.String()},
	}
	for _, id := range configured.BackupFor[rule.Prefix] {
		items = append(items, Item{Path: path + "/backup-for", Value: id.String()})
	}
	if rule.MED != nil {
		items = append(items, Item{Path: path + "/med", Value: uintValue(uint64(*rule.MED))})
	}
	if rule.LocalPreference != nil {
		items = append(items, Item{Path: path + "/local-preference", Value: uintValue(uint64(*rule.LocalPreference))})
	}
	if rule.PrependCount != 0 {
		items = append(items, Item{Path: path + "/prepend-count", Value: uintValue(uint64(rule.PrependCount))})
	}
	for _, community := range rule.Communities {
		items = append(items, Item{Path: path + "/community", Value: community.String()})
	}
	for _, community := range rule.LargeCommunities {
		items = append(items, Item{Path: path + "/large-community", Value: community.String()})
	}
	return items
}
