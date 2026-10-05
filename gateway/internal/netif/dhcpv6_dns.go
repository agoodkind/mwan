package netif

import (
	"net/netip"

	"github.com/insomniacslk/dhcp/dhcpv6"
)

func dhcpv6DNS(message *dhcpv6.Message, enabled bool) []netip.Addr {
	if !enabled {
		return nil
	}
	servers := []netip.Addr{}
	for _, server := range message.Options.DNS() {
		address, ok := netip.AddrFromSlice(server)
		if ok && address.Is6() && !address.IsUnspecified() && !address.IsMulticast() {
			servers = append(servers, address)
		}
	}
	return servers
}
