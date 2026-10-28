package networkjson

import (
	"fmt"

	"goodkind.io/mwan/internal/interfaceintent"
)

type linkVXLAN struct {
	VNI             *uint32 `json:"vni"`
	SourcePort      *uint16 `json:"source-port"`
	DestinationPort *uint16 `json:"destination-port"`
	TOS             *uint8  `json:"tos"`
}

func buildTunnelVXLAN(name string, wire *linkTunnel) (*interfaceintent.VXLAN, error) {
	switch interfaceintent.TunnelProtocol(wire.Protocol) {
	case interfaceintent.TunnelProtocol6in4:
		if wire.VXLAN != nil {
			return nil, fmt.Errorf("interface %s: link/tunnel/vxlan requires protocol vxlan", name)
		}
		return nil, nil
	case interfaceintent.TunnelProtocolVXLAN:
		if wire.VXLAN == nil {
			return nil, fmt.Errorf("interface %s: link/tunnel/vxlan is required", name)
		}
	default:
		return nil, fmt.Errorf("interface %s: link/tunnel/protocol %q is not supported", name, wire.Protocol)
	}
	settings := wire.VXLAN
	if settings.VNI == nil || settings.SourcePort == nil || settings.DestinationPort == nil || settings.TOS == nil {
		return nil, fmt.Errorf("interface %s: link/tunnel/vxlan requires vni, source-port, destination-port and tos", name)
	}
	if *settings.VNI > interfaceintent.MaximumVXLANVNI || *settings.SourcePort == 0 || *settings.DestinationPort == 0 {
		return nil, fmt.Errorf("interface %s: link/tunnel/vxlan requires a 24-bit VNI and nonzero UDP ports", name)
	}
	return &interfaceintent.VXLAN{
		VNI: *settings.VNI, SourcePort: *settings.SourcePort, DestinationPort: *settings.DestinationPort, TOS: *settings.TOS,
	}, nil
}

func tunnelOverhead(protocol interfaceintent.TunnelProtocol) uint64 {
	if protocol == interfaceintent.TunnelProtocolVXLAN {
		return interfaceintent.TunnelVXLANOverhead
	}
	return interfaceintent.Tunnel6in4Overhead
}
