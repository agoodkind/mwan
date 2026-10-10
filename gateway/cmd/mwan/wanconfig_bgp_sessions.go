package main

import (
	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/yangpub"
)

func bgpSessionLiveItems(name string, sessions []bgp.SessionState) []yangpub.Item {
	var items []yangpub.Item
	for _, session := range sessions {
		base := "/ietf-interfaces:interfaces/interface[name='" + name +
			"']/ietf-ip:ipv6/goodkind-mwan-steering:ownership-family-state/bgp-session[name='" + session.Name + "']"
		items = append(items,
			yangpub.Item{Path: base + "/name", Value: session.Name},
			yangpub.Item{Path: base + "/state", Value: session.FSMState},
			yangpub.Item{Path: base + "/established", Value: boolValue(session.Established)},
		)
		for _, prefix := range session.Accepted {
			items = append(items, yangpub.Item{Path: base + "/accepted-prefix", Value: prefix.String()})
		}
		for _, prefix := range session.Advertised {
			items = append(items, yangpub.Item{Path: base + "/advertised-prefix", Value: prefix.String()})
		}
		for _, rejection := range session.Rejected {
			path := base + "/rejection[prefix='" + rejection.Prefix.String() + "']"
			items = append(items,
				yangpub.Item{Path: path + "/prefix", Value: rejection.Prefix.String()},
				yangpub.Item{Path: path + "/reason", Value: rejection.Reason},
			)
		}
	}
	return items
}
