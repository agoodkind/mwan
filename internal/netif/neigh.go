package netif

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// resolvedNeighStates are the NUD states in which the kernel holds a usable
// link-layer address for a neighbour. INCOMPLETE and FAILED mean resolution
// is pending or has given up, so a route via that neighbour black-holes.
const resolvedNeighStates = unix.NUD_REACHABLE | unix.NUD_STALE |
	unix.NUD_DELAY | unix.NUD_PROBE | unix.NUD_PERMANENT | unix.NUD_NOARP

// discardPort is the UDP port the resolution nudge targets. Nothing needs to
// listen there; the datagram exists only to make the kernel attempt
// neighbour resolution for the destination.
const discardPort = "9"

// neighStateResolved reports whether a neighbour cache state carries a
// usable link-layer address.
func neighStateResolved(state int) bool {
	return state&resolvedNeighStates != 0
}

// NextHopResolves reports whether addr resolves in the neighbour table on
// dev. It first nudges the kernel with one UDP datagram to the address so an
// absent or expired entry gets a fresh resolution attempt, then reads the
// neighbour table. A missing entry, INCOMPLETE, or FAILED all count as
// unresolved; resolution triggered by the nudge lands in the table well
// before the caller's next reconcile tick.
func NextHopResolves(
	ctx context.Context, log *slog.Logger, dev string, addr string,
) (bool, error) {
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		log.WarnContext(ctx, "next hop address does not parse", "addr", addr, "err", err)
		return false, fmt.Errorf("next hop %q: %w", addr, err)
	}
	nudgeNeighbour(ctx, log, parsed)

	link, err := netlink.LinkByName(dev)
	if err != nil {
		log.WarnContext(ctx, "next hop link lookup failed", "dev", dev, "err", err)
		return false, fmt.Errorf("link %q: %w", dev, err)
	}
	family := unix.AF_INET6
	if parsed.Is4() {
		family = unix.AF_INET
	}
	neighbours, err := netlink.NeighList(link.Attrs().Index, family)
	if err != nil {
		log.WarnContext(ctx, "neighbour list failed", "dev", dev, "err", err)
		return false, fmt.Errorf("neighbour list on %q: %w", dev, err)
	}
	for _, neighbour := range neighbours {
		entry, ok := netip.AddrFromSlice(neighbour.IP)
		if !ok {
			continue
		}
		if entry.Unmap() == parsed.Unmap() {
			resolved := neighStateResolved(neighbour.State)
			log.DebugContext(ctx, "next hop neighbour state",
				"dev", dev, "addr", addr,
				"state", neighbour.State, "resolved", resolved)
			return resolved, nil
		}
	}
	log.DebugContext(ctx, "next hop has no neighbour entry", "dev", dev, "addr", addr)
	return false, nil
}

// nudgeNeighbour sends one UDP datagram to addr so the kernel starts
// neighbour resolution for it. Best effort: a send failure only means the
// neighbour table is read without a fresh resolution attempt.
func nudgeNeighbour(ctx context.Context, log *slog.Logger, addr netip.Addr) {
	network := "udp6"
	if addr.Is4() {
		network = "udp4"
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(
		ctx, network, net.JoinHostPort(addr.String(), discardPort),
	)
	if err != nil {
		log.DebugContext(ctx, "neighbour nudge dial failed",
			"addr", addr.String(), "err", err)
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte{0}); err != nil {
		log.DebugContext(ctx, "neighbour nudge write failed",
			"addr", addr.String(), "err", err)
	}
}
