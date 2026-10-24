package netif

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type EchoSession struct {
	connection net.PacketConn
	target     netip.Addr
	identifier uint16
	sequence   uint16
	mu         sync.Mutex
}

func OpenEchoSession(ctx context.Context, iface string, source, target netip.Addr, identifier uint16) (*EchoSession, error) {
	if !target.IsValid() || (source.IsValid() && source.Is4() != target.Is4()) {
		return nil, fmt.Errorf("echo source and target require matching address families")
	}
	var connection net.PacketConn
	if target.Is4() {
		configuration := net.ListenConfig{}
		if iface != "" {
			configuration.Control = bindToDevice(iface)
		}
		address := "0.0.0.0"
		if source.IsValid() {
			address = source.String()
		}
		opened, err := configuration.ListenPacket(ctx, "ip4:icmp", address)
		if err != nil {
			return nil, fmt.Errorf("open echo session: %w", err)
		}
		connection = opened
		if err := ipv4.NewPacketConn(connection).SetControlMessage(ipv4.FlagDst, true); err != nil {
			_ = connection.Close()
			return nil, err
		}
	} else {
		address := "::"
		if source.IsValid() {
			address = source.String()
		}
		opened, err := icmp.ListenPacket("ip6:ipv6-icmp", address)
		if err != nil {
			return nil, fmt.Errorf("open echo session: %w", err)
		}
		connection = opened
		if iface != "" {
			if err := NewV6Probe(iface, slog.Default()).bindProbeToDevice(ctx, opened); err != nil {
				_ = opened.Close()
				return nil, err
			}
		}
		if err := opened.IPv6PacketConn().SetControlMessage(ipv6.FlagDst, true); err != nil {
			_ = opened.Close()
			return nil, err
		}
	}
	return &EchoSession{connection: connection, target: target, identifier: identifier}, nil
}

func (session *EchoSession) Probe(ctx context.Context, timeout time.Duration) (PingProbeResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PingProbeResult{}, err
	}
	start := time.Now()
	message, _, _ := newEchoRequest(start)
	if session.target.Is4() {
		message, _, _ = newEchoRequest4(start)
	}
	session.sequence++
	echo := message.Body.(*icmp.Echo)
	echo.ID = int(session.identifier)
	echo.Seq = int(session.sequence)
	packet, err := message.Marshal(nil)
	if err != nil {
		return PingProbeResult{}, err
	}
	deadline := start.Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := session.connection.SetDeadline(deadline); err != nil {
		return PingProbeResult{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = session.connection.SetReadDeadline(time.Now()) })
	defer stop()
	destination := &net.IPAddr{IP: session.target.AsSlice(), Zone: session.target.Zone()}
	if _, err := session.connection.WriteTo(packet, destination); err != nil {
		return PingProbeResult{}, err
	}
	buffer := make([]byte, 1500)
	for {
		var count int
		var peer net.Addr
		var source netip.Addr
		var err error
		if session.target.Is4() {
			var control *ipv4.ControlMessage
			count, control, peer, err = ipv4.NewPacketConn(session.connection).ReadFrom(buffer)
			if control != nil {
				source, _ = netip.AddrFromSlice(control.Dst)
			}
		} else {
			var control *ipv6.ControlMessage
			connection := session.connection.(*icmp.PacketConn)
			count, control, peer, err = connection.IPv6PacketConn().ReadFrom(buffer)
			if control != nil {
				source, _ = netip.AddrFromSlice(control.Dst)
			}
		}
		if err != nil {
			return PingProbeResult{}, classifyPingReadError(ctx, err)
		}
		matched := matchesProbeReply6(buffer[:count], echo.ID, echo.Seq) && peerMatchesTarget(peer, session.target)
		if session.target.Is4() {
			matched = matchesProbeReply4(buffer[:count], peer, session.target, echo.ID, echo.Seq)
		}
		if matched {
			return PingProbeResult{RoundTripTime: time.Since(start), Source: source.Unmap()}, nil
		}
	}
}

func (session *EchoSession) Close() error { return session.connection.Close() }
