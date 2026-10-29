//go:build linux

package netif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"runtime"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const forwardingMACLength = 6

// PacketSelector preserves the packet fields used by the kernel's input-route lookup.
type PacketSelector struct {
	Source         netip.AddrPort
	Destination    netip.AddrPort
	Protocol       uint8
	InputInterface int
	Mark           uint32
	TrafficClass   uint8
	FlowLabel      uint32
}

// ForwardingPath contains current kernel observations rather than configured next hops.
type ForwardingPath struct {
	Route    netlink.Route
	Link     netlink.Link
	Neighbor netlink.Neigh
}

// EgressObservationReason distinguishes unresolved packet-selection evidence.
type EgressObservationReason uint8

const (
	// EgressObservationMultipath requires observation of a selected ECMP output.
	EgressObservationMultipath EgressObservationReason = iota + 1
	// EgressObservationNexthopGroup also tracks the referenced kernel group ID.
	EgressObservationNexthopGroup
	// EgressObservationMultipleRoutes rejects an arbitrary choice between replies.
	EgressObservationMultipleRoutes
	// EgressObservationUIDMapping rejects an unmapped or ambiguous IPv6 input UID.
	EgressObservationUIDMapping
)

// EgressObservationRequiredError prevents a synthetic route query from certifying
// a path that can also depend on the actual packet's RX hash.
type EgressObservationRequiredError struct {
	Reason    EgressObservationReason
	NexthopID uint32
}

// Error reports the packet evidence that route lookup cannot establish.
func (e *EgressObservationRequiredError) Error() string {
	if e.Reason == EgressObservationUIDMapping {
		return "forwarding path requires an actual egress observation: IPv6 input UID has no unambiguous mapping in the caller's user namespace"
	}
	return fmt.Sprintf("forwarding path requires an actual egress observation: reason=%d nexthop_id=%d", e.Reason, e.NexthopID)
}

// ForwardPathLookup preserves the existing packet mark and never selects a provider.
// A false result leaves forwarding in the kernel. A resolved candidate accompanies
// EgressObservationRequiredError when the kernel supplies one.
func ForwardPathLookup(ctx context.Context, log *slog.Logger, packet PacketSelector) (path ForwardingPath, usable bool, resultErr error) {
	defer func() {
		var requirement *EgressObservationRequiredError
		if resultErr != nil && !errors.As(resultErr, &requirement) {
			log.WarnContext(ctx, "forwarding path lookup failed", "source", packet.Source, "destination", packet.Destination,
				"input_interface", packet.InputInterface, "mark", packet.Mark, "err", resultErr)
		}
	}()
	if err := packet.validate(); err != nil {
		return path, false, err
	}
	if err := ctx.Err(); err != nil {
		return path, false, fmt.Errorf("start forwarding path lookup: %w", err)
	}
	if packet.Source.Addr().Is6() {
		// UID projection, policy inspection and sends share the caller's namespaces.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	handle, err := netlink.NewHandle(unix.NETLINK_ROUTE)
	if err != nil {
		return path, false, fmt.Errorf("open forwarding route socket: %w", err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close forwarding route socket: %w", err))
			usable = false
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := deadline.Sub(realClock{}.Now())
		if remaining <= 0 {
			return path, false, context.DeadlineExceeded
		}
		if err := handle.SetSocketTimeout(remaining); err != nil {
			return path, false, fmt.Errorf("set forwarding route timeout: %w", err)
		}
	}
	if _, err := handle.LinkByIndex(packet.InputInterface); err != nil {
		return path, false, fmt.Errorf("inspect input interface: %w", err)
	}
	uid, err := forwardingPacketUID(handle, packet.family())
	if err != nil {
		return path, false, err
	}
	selected, requirement, supported, err := resolveForwardingRoute(ctx, packet, uid)
	if err != nil {
		return path, false, fmt.Errorf("resolve forwarding route: %w", err)
	}
	if !supported {
		return path, false, nil
	}
	link, err := handle.LinkByIndex(selected.LinkIndex)
	if err != nil {
		return path, false, fmt.Errorf("inspect forwarding output: %w", err)
	}
	attributes := link.Attrs()
	if attributes.Flags&net.FlagUp == 0 || attributes.RawFlags&unix.IFF_LOWER_UP == 0 || !forwardingMACValid(attributes.HardwareAddr) {
		return path, false, nil
	}
	nextHop := selected.Gw
	if len(nextHop) == 0 {
		nextHop = packet.Destination.Addr().AsSlice()
	}
	neighbors, err := handle.NeighList(selected.LinkIndex, packet.family())
	if err != nil {
		return path, false, fmt.Errorf("inspect forwarding neighbor on interface %d for %s: %w", selected.LinkIndex, nextHop, err)
	}
	neighbor, resolved := forwardingNeighbor(neighbors, selected.LinkIndex, nextHop)
	if !resolved {
		return path, false, nil
	}
	if err := ctx.Err(); err != nil {
		return path, false, fmt.Errorf("complete forwarding path lookup: %w", err)
	}
	path = ForwardingPath{Route: selected, Link: link, Neighbor: neighbor}
	if err := uid.verify(handle); err != nil {
		return path, false, err
	}
	if requirement != nil {
		return path, false, requirement
	}
	return path, true, nil
}

func (p PacketSelector) validate() error {
	if p.InputInterface <= 0 || p.InputInterface > math.MaxInt32 {
		return fmt.Errorf("forwarding lookup requires an input interface index")
	}
	for _, address := range []netip.Addr{p.Source.Addr(), p.Destination.Addr()} {
		if !address.IsValid() || address.Is4In6() || address.IsUnspecified() || address.IsMulticast() || address.Zone() != "" {
			return fmt.Errorf("forwarding lookup requires unambiguous unicast packet addresses")
		}
	}
	if p.Source.Addr().Is4() != p.Destination.Addr().Is4() {
		return fmt.Errorf("forwarding lookup requires one address family")
	}
	if p.Protocol != unix.IPPROTO_TCP && p.Protocol != unix.IPPROTO_UDP {
		return fmt.Errorf("forwarding lookup requires TCP or UDP packet ports")
	}
	if p.FlowLabel > 0xfffff || (p.Source.Addr().Is4() && p.FlowLabel != 0) {
		return fmt.Errorf("forwarding flow label requires IPv6 and a 20-bit value")
	}
	return nil
}

func (p PacketSelector) family() int {
	if p.Source.Addr().Is4() {
		return unix.AF_INET
	}
	return unix.AF_INET6
}

func forwardingRouteAbsent(err error) bool {
	return isNoRouteError(err) || errors.Is(err, unix.EACCES)
}

func forwardingRouteSupported(route netlink.Route) bool {
	return route.Type == unix.RTN_UNICAST && route.Encap == nil && route.Via == nil &&
		route.NewDst == nil && route.MPLSDst == nil && route.Flags&(unix.RTNH_F_DEAD|unix.RTNH_F_LINKDOWN) == 0
}

func forwardingMACValid(address net.HardwareAddr) bool {
	var zero [forwardingMACLength]byte
	return len(address) == forwardingMACLength && address[0]&1 == 0 && !bytes.Equal(address, zero[:])
}

func forwardingNeighbor(neighbors []netlink.Neigh, index int, nextHop net.IP) (netlink.Neigh, bool) {
	for _, neighbor := range neighbors {
		if neighbor.LinkIndex != index || !neighbor.IP.Equal(nextHop) {
			continue
		}
		resolved := neighStateResolved(neighbor.State) && neighbor.State&(unix.NUD_FAILED|unix.NUD_INCOMPLETE) == 0 &&
			neighbor.Flags&unix.NTF_PROXY == 0 && forwardingMACValid(neighbor.HardwareAddr)
		return neighbor, resolved
	}
	return netlink.Neigh{}, false
}
