package wanconfig

// WANCONFIG management readers see Linux tunnel intent as YANG path and
// value pairs. A VXLAN VNI such as 42 becomes options[name='vni']/value.

import "goodkind.io/mwan/gateway/interfaceintent"

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
			Item{
				Path:  base + "/options[name='vni']/value",
				Value: uintValue(uint64(tunnel.VXLAN.VNI)),
			},
			Item{
				Path:  base + "/options[name='source-port-min']/value",
				Value: uintValue(uint64(tunnel.VXLAN.SourcePortMin)),
			},
			Item{
				Path:  base + "/options[name='source-port-max']/value",
				Value: uintValue(uint64(tunnel.VXLAN.SourcePortMax)),
			},
			Item{
				Path:  base + "/options[name='destination-port']/value",
				Value: uintValue(uint64(tunnel.VXLAN.DestinationPort)),
			},
			Item{
				Path:  base + "/options[name='tos']/value",
				Value: uintValue(uint64(tunnel.VXLAN.TOS)),
			},
		)
	}

	return items
}
