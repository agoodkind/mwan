package wanconfig

import "goodkind.io/mwan/internal/interfaceintent"

func tunnelItems(base string, tunnel *interfaceintent.Tunnel) []Item {
	if tunnel == nil {
		return nil
	}
	items := []Item{
		{Path: base + "/protocol", Value: string(tunnel.Protocol)},
		{Path: base + "/underlay", Value: tunnel.Underlay},
		{Path: base + "/remote-address", Value: tunnel.Remote.String()},
	}
	if tunnel.Local.IsValid() {
		items = append(items, Item{Path: base + "/local-address", Value: tunnel.Local.String()})
	}
	if tunnel.TTL != nil {
		items = append(items, Item{Path: base + "/ttl", Value: uintValue(uint64(*tunnel.TTL))})
	}
	if tunnel.VXLAN != nil {
		items = append(
			items,
			Item{Path: base + "/vxlan/vni", Value: uintValue(uint64(tunnel.VXLAN.VNI))},
			Item{Path: base + "/vxlan/source-port-min", Value: uintValue(uint64(tunnel.VXLAN.SourcePortMin))},
			Item{Path: base + "/vxlan/source-port-max", Value: uintValue(uint64(tunnel.VXLAN.SourcePortMax))},
			Item{Path: base + "/vxlan/destination-port", Value: uintValue(uint64(tunnel.VXLAN.DestinationPort))},
			Item{Path: base + "/vxlan/tos", Value: uintValue(uint64(tunnel.VXLAN.TOS))},
		)
	}
	return items
}
