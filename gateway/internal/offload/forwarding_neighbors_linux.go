//go:build linux

package offload

import (
	"bytes"
	"net"
	"net/netip"
	"slices"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

const forwardingNeighborCapacity = 1024

type forwardingNeighborKey struct {
	family  int
	index   int
	address netip.Addr
}

type forwardingNeighborCache struct {
	entries map[forwardingNeighborKey]netlink.Neigh
}

func (cache *forwardingNeighborCache) observe(message syscall.NetlinkMessage) bool {
	if message.Header.Type != unix.RTM_NEWNEIGH && message.Header.Type != unix.RTM_DELNEIGH {
		clear(cache.entries)
		return true
	}
	neighbor, valid := forwardingNeighborRecord(message.Data)
	if !valid {
		clear(cache.entries)
		return true
	}
	address, _ := netip.AddrFromSlice(neighbor.IP)
	key := forwardingNeighborKey{family: neighbor.Family, index: neighbor.LinkIndex, address: address}
	previous, present := cache.entries[key]
	if message.Header.Type == unix.RTM_DELNEIGH || !netif.ForwardingNeighborUsable(neighbor) {
		delete(cache.entries, key)
		return true
	}
	equivalent := present && previous.Flags == neighbor.Flags && previous.Type == neighbor.Type &&
		bytes.Equal(previous.HardwareAddr, neighbor.HardwareAddr)
	if !present && len(cache.entries) >= forwardingNeighborCapacity {
		clear(cache.entries)
	}
	// Netlink attribute slices refer to the reusable receive buffer.
	neighbor.IP = slices.Clone(neighbor.IP)
	neighbor.HardwareAddr = slices.Clone(neighbor.HardwareAddr)
	cache.entries[key] = neighbor
	return !equivalent
}

func forwardingNeighborRecord(data []byte) (netlink.Neigh, bool) {
	var empty netlink.Neigh
	if len(data) < forwardingNeighborHeaderBytes {
		return empty, false
	}
	addressBytes := net.IPv4len
	switch data[0] {
	case unix.AF_INET:
	case unix.AF_INET6:
		addressBytes = net.IPv6len
	default:
		return empty, false
	}
	attributes, err := nl.ParseRouteAttr(data[forwardingNeighborHeaderBytes:])
	if err != nil {
		return empty, false
	}
	seen := make(map[uint16]bool, len(attributes))
	for _, attribute := range attributes {
		if seen[attribute.Attr.Type] || !forwardingNeighborAttribute(attribute, addressBytes) {
			return empty, false
		}
		seen[attribute.Attr.Type] = true
	}
	if !seen[unix.NDA_DST] || !seen[unix.NDA_LLADDR] {
		return empty, false
	}
	neighbor, err := netlink.NeighDeserialize(data)
	if err != nil || neighbor.LinkIndex <= 0 {
		return empty, false
	}
	address, valid := netip.AddrFromSlice(neighbor.IP)
	if !valid || address.Is4In6() || address.IsUnspecified() || address.IsMulticast() {
		return empty, false
	}
	return *neighbor, true
}

func forwardingNeighborAttribute(attribute syscall.NetlinkRouteAttr, addressBytes int) bool {
	switch attribute.Attr.Type {
	case unix.NDA_DST:
		return len(attribute.Value) == addressBytes
	case unix.NDA_LLADDR:
		return len(attribute.Value) == 6
	case unix.NDA_CACHEINFO:
		return len(attribute.Value) == 16
	case unix.NDA_PROBES:
		return len(attribute.Value) == 4
	default:
		return false
	}
}
