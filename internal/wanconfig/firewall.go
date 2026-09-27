package wanconfig

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
