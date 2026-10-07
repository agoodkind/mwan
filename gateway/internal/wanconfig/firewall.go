package wanconfig

import "goodkind.io/mwan/internal/config"

const guestTypePath = interfacesPath + "/goodkind-mwan-steering:guest-type"

func guestTypeItems(g Gateway) []Item {
	if g.GuestType != config.GuestTypeLXC {
		return nil
	}
	return []Item{{Path: guestTypePath, Value: string(g.GuestType)}}
}

func firewallItems(g Gateway) []Item {
	cfg := g.Firewall
	if !cfg.Enabled {
		return nil
	}
	base := steeringGroupPath + "/firewall"
	var items []Item
	if cfg.ManagementInterface != "" {
		items = append(items, Item{Path: base + "/management-interface", Value: cfg.ManagementInterface})
	}
	items = append(items,
		Item{Path: base + "/pinned-set-v4-name", Value: cfg.PinnedSetV4Name},
		Item{Path: base + "/pinned-set-v6-name", Value: cfg.PinnedSetV6Name},
	)
	for _, service := range cfg.ManagementServices {
		path := base + "/management-service[protocol='" + service.Protocol +
			"'][port='" + uintValue(uint64(service.Port)) + "']"
		items = append(items, Item{Path: path, Value: ""})
		for _, source := range service.Sources {
			items = append(items, Item{Path: path + "/allowed-source", Value: source.String()})
		}
	}
	items = append(items, firewallPinItems(g, base)...)
	for _, prefix := range cfg.PinnedIPv4 {
		items = append(items, Item{Path: base + "/pinned-v4", Value: prefix.String()})
	}
	for _, prefix := range cfg.PinnedIPv6 {
		items = append(items, Item{Path: base + "/pinned-v6", Value: prefix.String()})
	}
	return items
}

func firewallPinItems(g Gateway, base string) []Item {
	cfg := g.Firewall
	if cfg.PinnedProvider == "" {
		return nil
	}
	items := make([]Item, 0, 4)
	for _, member := range g.Members {
		if member.Iface != cfg.PinnedProvider {
			continue
		}
		if g.PinnedConnectionID != "" {
			items = append(items, Item{Path: base + "/pinned-connection-id", Value: g.PinnedConnectionID})
		} else {
			providerName := member.ProviderName
			if providerName == "" {
				providerName = member.Name
			}
			items = append(items, Item{Path: base + "/pinned-provider", Value: providerName})
		}
		break
	}
	return append(items,
		Item{Path: base + "/pinned-source-v4", Value: cfg.PinnedSourceIPv4.String()},
		Item{Path: base + "/pinned-source-port", Value: uintValue(uint64(cfg.PinnedSourcePort))},
		Item{Path: base + "/pinned-destination-port", Value: uintValue(uint64(cfg.PinnedDestinationPort))},
	)
}
