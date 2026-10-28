package interfaceintent

const (
	TunnelProtocolVXLAN TunnelProtocol = "vxlan"
	TunnelVXLANOverhead                = 50
	MaximumVXLANVNI                    = 16777215
)

type VXLAN struct {
	VNI             uint32 `json:"vni"`
	SourcePort      uint16 `json:"source_port"`
	DestinationPort uint16 `json:"destination_port"`
	TOS             uint8  `json:"tos"`
}

func (tunnel *Tunnel) Clone() *Tunnel {
	if tunnel == nil {
		return nil
	}
	cloned := *tunnel
	if tunnel.TTL != nil {
		cloned.TTL = new(*tunnel.TTL)
	}
	if tunnel.VXLAN != nil {
		cloned.VXLAN = new(*tunnel.VXLAN)
	}
	return &cloned
}
