package networkjson

import (
	"fmt"
	"net/netip"

	"goodkind.io/mwan/internal/interfaceintent"
)

func buildIntentTunnel(entry ifaceEntry) (*interfaceintent.Tunnel, error) {
	wire := entry.Link.Tunnel
	if entry.Owner != string(interfaceintent.OwnerMWAN) {
		return nil, fmt.Errorf("interface %s: tunnel link requires owner mwan", entry.Name)
	}
	if err := rejectTunnelLinkLeaves(entry); err != nil {
		return nil, err
	}
	if wire.Protocol != string(interfaceintent.TunnelProtocol6in4) {
		return nil, fmt.Errorf("interface %s: link/tunnel/protocol %q is not supported", entry.Name, wire.Protocol)
	}
	if wire.Underlay == "" {
		return nil, fmt.Errorf("interface %s: link/tunnel/underlay is required", entry.Name)
	}
	if wire.RemoteAddress == "" {
		return nil, fmt.Errorf("interface %s: link/tunnel/remote-address is required", entry.Name)
	}
	remote, err := parseTunnelEndpoint(entry.Name, "remote-address", wire.RemoteAddress)
	if err != nil {
		return nil, err
	}
	local := netip.Addr{}
	if wire.LocalAddress != "" {
		local, err = parseTunnelEndpoint(entry.Name, "local-address", wire.LocalAddress)
		if err != nil {
			return nil, err
		}
	}
	if wire.TTL != nil && *wire.TTL == 0 {
		return nil, fmt.Errorf("interface %s: link/tunnel/ttl must be between 1 and 255", entry.Name)
	}
	if entry.Link.MTU != nil && *entry.Link.MTU < interfaceintent.IPv6MinimumMTU {
		return nil, fmt.Errorf("interface %s: 6in4 tunnel mtu %d is below the IPv6 minimum %d",
			entry.Name, *entry.Link.MTU, interfaceintent.IPv6MinimumMTU)
	}
	if err := rejectTunnelIPv4(entry); err != nil {
		return nil, err
	}
	return &interfaceintent.Tunnel{
		Protocol: interfaceintent.TunnelProtocol6in4, Underlay: wire.Underlay,
		Remote: remote, Local: local, TTL: wire.TTL,
	}, nil
}

func rejectTunnelLinkLeaves(entry ifaceEntry) error {
	link := entry.Link
	if entry.Type == "iana-if-type:bridge" {
		return fmt.Errorf("interface %s: bridge link cannot include a tunnel", entry.Name)
	}
	if link.VLAN != nil {
		return fmt.Errorf("interface %s: link cannot include both a tunnel and a vlan", entry.Name)
	}
	if link.BridgeMaster != "" {
		return fmt.Errorf("interface %s: link cannot include both a tunnel and a bridge-master", entry.Name)
	}
	if link.Match != nil {
		return fmt.Errorf("interface %s: tunnel link cannot include a device match", entry.Name)
	}
	if link.HardwareAddress != "" {
		return fmt.Errorf("interface %s: tunnel link cannot include a hardware-address", entry.Name)
	}
	return nil
}

func rejectTunnelIPv4(entry ifaceEntry) error {
	family := entry.IPv4
	if family == nil {
		return nil
	}
	dhcp := family.DHCP != nil && *family.DHCP
	if len(family.Address) != 0 || dhcp || family.DHCPv4 != nil || family.Gateway != "" ||
		len(family.Routes) != 0 || len(family.SourceAddresses) != 0 {
		return fmt.Errorf(
			"interface %s: a 6in4 tunnel transports only IPv6 and cannot include ipv4 addresses, DHCP, a gateway, or routes",
			entry.Name)
	}
	return nil
}

func parseTunnelEndpoint(name string, leaf string, raw string) (netip.Addr, error) {
	address, err := parseAddress("interface "+name, "link/tunnel/"+leaf, raw)
	if err != nil {
		return netip.Addr{}, err
	}
	if !address.Is4() || address.IsUnspecified() || address.IsMulticast() {
		return netip.Addr{}, fmt.Errorf(
			"interface %s: link/tunnel/%s %s must be a unicast IPv4 address for a 6in4 tunnel", name, leaf, raw)
	}
	return address, nil
}

func tunnelUnderlay(link *interfaceintent.Link) string {
	if link.Tunnel == nil {
		return ""
	}
	return link.Tunnel.Underlay
}

func linkDependencies(link *interfaceintent.Link) []string {
	return []string{vlanParent(link), link.BridgeMaster, tunnelUnderlay(link)}
}

type tunnelEndpoints struct {
	underlay string
	local    netip.Addr
	remote   netip.Addr
}

func validateTunnels(connections []interfaceintent.Connection, declared map[string]bool, indexes map[string]int) error {
	owners := make(map[tunnelEndpoints]string)
	for _, connection := range connections {
		if connection.Link == nil || connection.Link.Tunnel == nil {
			continue
		}
		tunnel := connection.Link.Tunnel
		if tunnel.Underlay == connection.Name {
			return fmt.Errorf("interface %s: tunnel underlay must be another interface", connection.Name)
		}
		if !declared[tunnel.Underlay] {
			return fmt.Errorf("interface %s: tunnel underlay %s is not declared", connection.Name, tunnel.Underlay)
		}
		index, accepted := indexes[tunnel.Underlay]
		if !accepted {
			return fmt.Errorf("interface %s: tunnel underlay %s was rejected", connection.Name, tunnel.Underlay)
		}
		underlay := connections[index]
		if underlay.Link != nil && underlay.Link.Kind == interfaceintent.KindTunnel {
			return fmt.Errorf("interface %s: tunnel underlay %s is a tunnel", connection.Name, tunnel.Underlay)
		}
		if underlay.Link != nil && underlay.Link.MTU != nil && connection.Link.MTU != nil &&
			uint64(*connection.Link.MTU)+interfaceintent.Tunnel6in4Overhead > uint64(*underlay.Link.MTU) {
			return fmt.Errorf("interface %s: tunnel mtu %d exceeds underlay %s mtu %d less the %d-byte outer header",
				connection.Name, *connection.Link.MTU, tunnel.Underlay, *underlay.Link.MTU,
				interfaceintent.Tunnel6in4Overhead)
		}
		endpoints := tunnelEndpoints{underlay: tunnel.Underlay, local: tunnel.Local, remote: tunnel.Remote}
		if prior, taken := owners[endpoints]; taken {
			local := "unspecified"
			if tunnel.Local.IsValid() {
				local = tunnel.Local.String()
			}
			return fmt.Errorf("interfaces %s and %s configure the same tunnel (underlay %s, local %s, remote %s)",
				prior, connection.Name, tunnel.Underlay, local, tunnel.Remote)
		}
		owners[endpoints] = connection.Name
	}
	return nil
}
