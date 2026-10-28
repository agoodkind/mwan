package networkjson

import (
	"fmt"

	"goodkind.io/mwan/internal/interfaceintent"
)

type linkVXLAN struct {
	VNI             *uint32 `json:"vni"`
	SourcePortMin   *uint16 `json:"source-port-min"`
	SourcePortMax   *uint16 `json:"source-port-max"`
	DestinationPort *uint16 `json:"destination-port"`
	TOS             *uint8  `json:"tos"`
}

type vxlanReceiver struct {
	vni  uint32
	port uint16
}

func validateVXLANReceiver(name string, settings *interfaceintent.VXLAN, receivers map[vxlanReceiver]string) error {
	if settings == nil {
		return nil
	}
	// Fixed IPv4 VXLAN receivers share the namespace's VNI and UDP port lookup.
	key := vxlanReceiver{vni: settings.VNI, port: settings.DestinationPort}
	if prior, found := receivers[key]; found {
		return fmt.Errorf("interfaces %s and %s share VXLAN receiver VNI %d and destination UDP port %d", prior, name, key.vni, key.port)
	}
	receivers[key] = name
	return nil
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
	if settings.VNI == nil || settings.SourcePortMin == nil || settings.SourcePortMax == nil || settings.DestinationPort == nil || settings.TOS == nil {
		return nil, fmt.Errorf("interface %s: link/tunnel/vxlan requires vni, source-port-min, source-port-max, destination-port and tos", name)
	}
	if *settings.VNI > interfaceintent.MaximumVXLANVNI || *settings.SourcePortMin == 0 || *settings.DestinationPort == 0 {
		return nil, fmt.Errorf("interface %s: link/tunnel/vxlan requires a 24-bit VNI and nonzero UDP ports", name)
	}
	if *settings.SourcePortMin >= *settings.SourcePortMax {
		return nil, fmt.Errorf("interface %s: link/tunnel/vxlan requires source-port-min less than source-port-max", name)
	}
	return &interfaceintent.VXLAN{
		VNI: *settings.VNI, SourcePortMin: *settings.SourcePortMin, SourcePortMax: *settings.SourcePortMax,
		DestinationPort: *settings.DestinationPort, TOS: *settings.TOS,
	}, nil
}

func tunnelOverhead(protocol interfaceintent.TunnelProtocol) uint64 {
	if protocol == interfaceintent.TunnelProtocolVXLAN {
		return interfaceintent.TunnelVXLANOverhead
	}
	return interfaceintent.Tunnel6in4Overhead
}
