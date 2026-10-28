package interfaceintent

const (
	// TunnelProtocolVXLAN selects an Ethernet tunnel with a fixed remote IPv4 endpoint.
	TunnelProtocolVXLAN TunnelProtocol = "vxlan"
	// TunnelVXLANOverhead includes outer IPv4, UDP, VXLAN and inner Ethernet headers.
	TunnelVXLANOverhead = 50
	// MaximumVXLANVNI bounds the network identifier to its 24-bit wire field.
	MaximumVXLANVNI = 16777215
)

// VXLAN specifies the UDP envelope and inclusive source-port range of one routed tunnel.
type VXLAN struct {
	VNI             uint32 `json:"vni"`
	SourcePortMin   uint16 `json:"source_port_min"`
	SourcePortMax   uint16 `json:"source_port_max"`
	DestinationPort uint16 `json:"destination_port"`
	TOS             uint8  `json:"tos"`
}

// Clone gives each configuration consumer independent TTL and VXLAN settings.
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
