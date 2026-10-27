package vpp

import (
	"fmt"
	"net"
	"net/netip"

	"goodkind.io/mwan/internal/interfaceintent"
)

// Policy uses the router's network metadata for VPP interfaces and translation.
type Policy struct {
	Connections []interfaceintent.Connection `json:"Connections"`
	NAT64       *NAT64Policy                 `json:"NAT64"`
	Neighbors   []Neighbor                   `json:"Neighbors"`
}

// NAT64Policy keeps translation prefixes and pool addresses independent of process resources.
type NAT64Policy struct {
	Inside  string       `json:"Inside"`
	Outside string       `json:"Outside"`
	Prefix  netip.Prefix `json:"Prefix"`
	Pool    []netip.Addr `json:"Pool"`
	VRF     uint32       `json:"VRF"`
	PoolVRF uint32       `json:"PoolVRF"`
}

// Neighbor binds a static IP address and Ethernet MAC to a VPP interface.
type Neighbor struct {
	Interface string     `json:"Interface"`
	Address   netip.Addr `json:"Address"`
	MAC       string     `json:"MAC"`
}

// Validate rejects unsupported network policy before VPP resource reconciliation.
func (policy Policy) Validate() error {
	connections := make(map[string]bool, len(policy.Connections))
	ids := make(map[string]bool, len(policy.Connections))
	for _, connection := range policy.Connections {
		if err := validateConnectionIdentity(connection, connections, ids); err != nil {
			return err
		}
		if err := validateConnectionPolicy(connection); err != nil {
			return err
		}
	}
	for _, neighbor := range policy.Neighbors {
		if err := validateNeighborPolicy(neighbor, connections); err != nil {
			return err
		}
	}
	return validateNAT64Policy(policy.NAT64, connections)
}

func validateConnectionIdentity(connection interfaceintent.Connection, connections, ids map[string]bool) error {
	if connection.ID == "" || connection.Name == "" || connections[connection.Name] || ids[connection.ID.String()] {
		return fmt.Errorf("VPP interfaces require unique names and connection IDs")
	}
	connections[connection.Name] = true
	ids[connection.ID.String()] = true
	return nil
}

func validateConnectionPolicy(connection interfaceintent.Connection) error {
	if connection.Owner != interfaceintent.OwnerExternal {
		return fmt.Errorf("VPP interface %s requires external ownership at the Linux boundary", connection.Name)
	}
	if err := validateConnectionLink(connection.Name, connection.Link); err != nil {
		return err
	}
	if connection.IPv4 != nil && (connection.IPv4.DHCPv4 != nil || len(connection.IPv4.SourceAddresses) != 0) {
		return fmt.Errorf("VPP interface %s does not support Linux IPv4 client or source-address policy", connection.Name)
	}
	if err := validateIPv6Policy(connection.Name, connection.IPv6); err != nil {
		return err
	}
	for _, family := range []*interfaceintent.Family{ipv4Family(connection.IPv4), ipv6Family(connection.IPv6)} {
		if err := validateFamilyPolicy(connection.Name, family); err != nil {
			return err
		}
	}
	return nil
}

func validateConnectionLink(name string, link *interfaceintent.Link) error {
	if link == nil {
		return nil
	}
	linuxTopology := link.VLAN != nil || link.Tunnel != nil || link.BridgeMaster != ""
	linuxDiscovery := link.Match.Driver != "" || link.Match.HardwareAddress != ""
	if link.Kind != interfaceintent.KindPhysical || linuxTopology || linuxDiscovery {
		return fmt.Errorf("VPP interface %s requires a TAP resource and cannot configure Linux link discovery, VLANs, bridges, or tunnels", name)
	}
	return nil
}

func validateIPv6Policy(name string, family *interfaceintent.IPv6) error {
	if family == nil {
		return nil
	}
	clientPolicy := family.DHCPv6 != nil || family.Delegation != nil || len(family.ForwardingAddresses) != 0
	discoveryPolicy := family.AcceptRA != nil && *family.AcceptRA || family.AutoConf != nil && *family.AutoConf
	routerPolicy := family.AcceptRADefaultRoute != nil && *family.AcceptRADefaultRoute || family.UseRADNS != nil && *family.UseRADNS
	if clientPolicy || discoveryPolicy || routerPolicy {
		return fmt.Errorf("VPP interface %s does not support Linux IPv6 discovery, delegation, or forwarding-address policy", name)
	}
	return nil
}

func validateFamilyPolicy(name string, family *interfaceintent.Family) error {
	if family == nil {
		return nil
	}
	if family.DHCP != nil && *family.DHCP || family.Gateway.IsValid() {
		return fmt.Errorf("VPP interface %s requires static addresses and explicit VPP routes", name)
	}
	disabled := family.Enabled != nil && !*family.Enabled || family.Forwarding != nil && !*family.Forwarding
	resolverPolicy := len(family.DNS) != 0 || len(family.SearchDomains) != 0
	if disabled || resolverPolicy || family.RouteMetric != nil {
		return fmt.Errorf("VPP interface %s requires enabled forwarding and cannot configure Linux resolver or route metric policy", name)
	}
	for _, route := range family.Routes {
		if route.TableID != 0 {
			return fmt.Errorf("VPP route %s requires table-id 0; this module does not create forwarding tables", route.Destination)
		}
	}
	return nil
}

func validateNeighborPolicy(neighbor Neighbor, connections map[string]bool) error {
	mac, err := net.ParseMAC(neighbor.MAC)
	if !connections[neighbor.Interface] || !neighbor.Address.IsValid() || neighbor.Address.IsMulticast() || neighbor.Address.IsUnspecified() || err != nil || len(mac) != 6 {
		return fmt.Errorf("VPP neighbor %s requires a declared interface, unicast address, and Ethernet MAC", neighbor.Address)
	}
	return nil
}

func validateNAT64Policy(nat *NAT64Policy, connections map[string]bool) error {
	if nat == nil {
		return nil
	}
	if nat.VRF != 0 || nat.PoolVRF != 0 && nat.PoolVRF != ^uint32(0) {
		return fmt.Errorf("NAT64 requires table-id 0 and pool-table-id 0 or 4294967295; this module does not create forwarding tables")
	}
	if nat.Inside == nat.Outside || !connections[nat.Inside] || !connections[nat.Outside] {
		return fmt.Errorf("NAT64 requires distinct declared inside and outside interfaces")
	}
	if err := validateNAT64Prefix(nat.Prefix); err != nil {
		return err
	}
	return validateNAT64Pool(nat.Pool)
}

func validateNAT64Prefix(prefix netip.Prefix) error {
	if !prefix.IsValid() || !prefix.Addr().Is6() || prefix != prefix.Masked() {
		return fmt.Errorf("NAT64 requires a canonical IPv6 prefix")
	}
	switch prefix.Bits() {
	case 32, 40, 48, 56, 64, 96:
	default:
		return fmt.Errorf("NAT64 prefix length must be 32, 40, 48, 56, 64, or 96")
	}
	return nil
}

func validateNAT64Pool(pool []netip.Addr) error {
	if len(pool) == 0 {
		return fmt.Errorf("NAT64 requires at least one IPv4 pool address")
	}
	seen := make(map[netip.Addr]bool, len(pool))
	for _, address := range pool {
		if !address.Is4() || !address.IsGlobalUnicast() || seen[address] {
			return fmt.Errorf("NAT64 pool address %s must be a unique unicast IPv4 address", address)
		}
		seen[address] = true
	}
	return nil
}

func ipv4Family(family *interfaceintent.IPv4) *interfaceintent.Family {
	if family == nil {
		return nil
	}
	return &family.Family
}

func ipv6Family(family *interfaceintent.IPv6) *interfaceintent.Family {
	if family == nil {
		return nil
	}
	return &family.Family
}
