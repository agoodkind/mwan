package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

// V6Probe sends ICMPv6 Echo Requests from a specific interface and waits
// for the reply. Used by slaac_health and connectivity_probe to verify
// that an interface can actually reach a target, independent of whether
// SLAAC and routes look correct.
//
// Implementation uses golang.org/x/net/icmp to open a raw ICMPv6 socket
// (ipv6:ipv6-icmp), then writes Echo Request and reads matching Echo
// Reply. Matched on identifier+sequence so multiple concurrent probes
// from the same daemon don't cross-talk.
//
// Capabilities required: CAP_NET_RAW.
type V6Probe struct {
	iface string
	log   *slog.Logger
	clock clock
}

// NewV6Probe constructs a V6Probe. log must be non-nil.
func NewV6Probe(iface string, log *slog.Logger) *V6Probe {
	if log == nil {
		log = slog.Default()
	}
	return &V6Probe{
		iface: iface,
		log:   log.With("component", "v6probe", "iface", iface),
		clock: realClock{},
	}
}

// PingICMP6 sends one ICMPv6 Echo Request to target via the configured
// interface and returns the round-trip time on success. The wait is
// bounded by timeout; on timeout returns a [context.DeadlineExceeded].
//
// Source address selection: we open the listen socket with "::"; the
// kernel picks an appropriate source from the iface based on its source-
// address selection algorithm. If the iface has no usable global source
// (e.g. only a deprecated SLAAC), the kernel will refuse the send with
// EADDRNOTAVAIL, which we surface as a typed error.
func (p *V6Probe) PingICMP6(
	ctx context.Context, target netip.Addr, timeout time.Duration,
) (time.Duration, error) {
	op := p.log.With("op", "PingICMP6",
		"target", target.String(), "timeout_ms", timeout.Milliseconds())
	op.DebugContext(ctx, "v6probe: PingICMP6 entry")

	if !target.Is6() {
		return 0, fmt.Errorf("PingICMP6: target %q is not IPv6", target)
	}

	conn, err := icmp.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		p.log.WarnContext(ctx, "v6probe: ListenPacket failed",
			"op", "PingICMP6", "target", target.String(), "timeout_ms", timeout.Milliseconds(), "err", err)
		return 0, fmt.Errorf("ListenPacket: %w", err)
	}
	defer conn.Close()

	if p.iface != "" {
		// Pin egress to the interface with SO_BINDTODEVICE so unicast probes
		// actually leave via this WAN. Without it the kernel routes a unicast
		// target by the main table and the probe can leave via another WAN,
		// giving a verdict that is not interface-specific. Fail the probe if the
		// bind fails rather than run an unpinned probe that could report a dead
		// WAN as healthy.
		if err := p.bindProbeToDevice(ctx, conn); err != nil {
			return 0, fmt.Errorf("PingICMP6: %w", err)
		}
		p.setMulticastInterface(ctx, op, conn)
	}

	// Build Echo Request.
	startTime := p.clock.Now()
	msg, id, seq := newEchoRequest(startTime)
	wb, err := msg.Marshal(nil)
	if err != nil {
		p.log.WarnContext(ctx, "v6probe: marshal echo failed",
			"op", "PingICMP6", "target", target.String(), "timeout_ms", timeout.Milliseconds(), "err", err)
		return 0, fmt.Errorf("marshal echo: %w", err)
	}

	deadline := startTime.Add(timeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		p.log.WarnContext(ctx, "v6probe: SetReadDeadline failed",
			"op", "PingICMP6", "target", target.String(), "timeout_ms", timeout.Milliseconds(), "err", err)
		return 0, fmt.Errorf("SetReadDeadline: %w", err)
	}

	dst := &net.IPAddr{IP: target.AsSlice()}
	if _, err := conn.WriteTo(wb, dst); err != nil {
		p.log.WarnContext(ctx, "v6probe: WriteTo failed",
			"op", "PingICMP6", "target", target.String(), "timeout_ms", timeout.Milliseconds(), "err", err)
		return 0, fmt.Errorf("WriteTo(%s): %w", target, err)
	}
	op.DebugContext(ctx, "v6probe: echo sent", "id", id, "seq", seq)

	rb := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			ctxErr := ctx.Err()
			p.log.WarnContext(ctx, "v6probe: ctx cancelled while waiting for reply",
				"op", "PingICMP6", "target", target.String(), "timeout_ms", timeout.Milliseconds(), "err", ctxErr)
			return 0, fmt.Errorf("PingICMP6: %w", ctxErr)
		default:
		}
		n, peer, err := conn.ReadFrom(rb)
		dur := p.clock.Now().Sub(startTime)
		if err != nil {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				op.DebugContext(ctx, "v6probe: deadline exceeded waiting for reply",
					"waited_ms", dur.Milliseconds())
				return 0, context.DeadlineExceeded
			}
			p.log.WarnContext(ctx, "v6probe: ReadFrom failed",
				"op", "PingICMP6", "target", target.String(), "timeout_ms", timeout.Milliseconds(), "err", err)
			return 0, fmt.Errorf("ReadFrom: %w", err)
		}
		rm, err := icmp.ParseMessage(58, rb[:n])
		if err != nil {
			op.DebugContext(ctx, "v6probe: parse failed (continuing)", "err", err)
			continue
		}
		if rm.Type != ipv6.ICMPTypeEchoReply {
			continue
		}
		echo, ok := rm.Body.(*icmp.Echo)
		if !ok || echo.ID != id || echo.Seq != seq {
			continue
		}
		op.DebugContext(ctx, "v6probe: echo reply received",
			"from", peer.String(), "rtt_ms", dur.Milliseconds())
		return dur, nil
	}
}

// bindProbeToDevice pins the raw ICMPv6 socket to p.iface via SO_BINDTODEVICE so
// unicast probes egress that interface, matching the shell's `ping6 -I <iface>`
// and the v4 probe. The socket is a raw ip6:ipv6-icmp endpoint, so the
// underlying conn is a [net.IPConn] whose fd accepts the sockopt.
func (p *V6Probe) bindProbeToDevice(ctx context.Context, conn *icmp.PacketConn) error {
	ipConn, ok := conn.IPv6PacketConn().PacketConn.(*net.IPConn)
	if !ok {
		err := fmt.Errorf(
			"bind v6 probe: unexpected packet conn type %T",
			conn.IPv6PacketConn().PacketConn,
		)
		p.log.WarnContext(ctx, "v6probe: bind to device failed", "iface", p.iface, "err", err)
		return err
	}
	syscallConn, err := ipConn.SyscallConn()
	if err != nil {
		p.log.WarnContext(ctx, "v6probe: SyscallConn failed", "iface", p.iface, "err", err)
		return fmt.Errorf("bind v6 probe: SyscallConn: %w", err)
	}
	var bindErr error
	controlErr := syscallConn.Control(func(fileDescriptor uintptr) {
		bindErr = unix.SetsockoptString(
			int(fileDescriptor),
			unix.SOL_SOCKET,
			unix.SO_BINDTODEVICE,
			p.iface,
		)
	})
	if controlErr != nil {
		p.log.WarnContext(ctx, "v6probe: socket control failed", "iface", p.iface, "err", controlErr)
		return fmt.Errorf("bind v6 probe: access socket for %q: %w", p.iface, controlErr)
	}
	if bindErr != nil {
		p.log.WarnContext(ctx, "v6probe: SO_BINDTODEVICE failed", "iface", p.iface, "err", bindErr)
		return fmt.Errorf("bind v6 probe: setsockopt SO_BINDTODEVICE %q: %w", p.iface, bindErr)
	}
	return nil
}

// setMulticastInterface keeps the multicast egress interface pinned to the same
// device for parity with the multicast and link-local targets slaac_health uses.
// It is best-effort: SO_BINDTODEVICE already pins unicast egress, so a failure
// here does not change the unicast health verdict.
func (p *V6Probe) setMulticastInterface(
	ctx context.Context, op *slog.Logger, conn *icmp.PacketConn,
) {
	netIface, err := net.InterfaceByName(p.iface)
	if err != nil {
		op.DebugContext(ctx, "v6probe: InterfaceByName failed (non-fatal)",
			"iface", p.iface, "err", err)
		return
	}
	pktConn := ipv6.NewPacketConn(conn.IPv6PacketConn().PacketConn)
	if err := pktConn.SetMulticastInterface(netIface); err != nil {
		op.DebugContext(ctx, "v6probe: SetMulticastInterface failed (non-fatal)",
			"iface", p.iface, "err", err)
	}
}

func newEchoRequest(startTime time.Time) (icmp.Message, int, int) {
	id := os.Getpid() & 0xffff
	seq := int(startTime.UnixNano() & 0x7fff)
	return icmp.Message{
		Type: ipv6.ICMPTypeEchoRequest,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: []byte("mwan-v6probe"),
		},
	}, id, seq
}
