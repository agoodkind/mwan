package netif

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
)

// OnLinkMappedAddresses excludes ordinary addresses and uses only connected IPv4 subnets.
func OnLinkMappedAddresses(current []CurrentAddr, mapped []netip.Addr) ([]netip.Addr, error) {
	var subnets []netip.Prefix
	assigned := make(map[netip.Addr]bool)
	for _, address := range current {
		if address.Family != "inet" {
			continue
		}
		prefix, err := netip.ParsePrefix(address.CIDR)
		if err != nil {
			slog.Warn("mapped address subnet parse failed", "cidr", address.CIDR, "err", err)
			return nil, fmt.Errorf("parse link address %q: %w", address.CIDR, err)
		}
		if prefix.Bits() < 32 {
			subnets = append(subnets, prefix.Masked())
			assigned[prefix.Addr()] = true
		}
	}
	var selected []netip.Addr
	for _, address := range mapped {
		if !assigned[address] && slices.ContainsFunc(subnets, func(prefix netip.Prefix) bool { return prefix.Contains(address) }) {
			selected = append(selected, address)
		}
	}
	return selected, nil
}
