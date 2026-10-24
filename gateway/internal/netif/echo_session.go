package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	internalclock "goodkind.io/mwan/internal/clock"
)

type echoResult struct {
	reply PingProbeResult
	err   error
}
type pendingEcho struct {
	start  time.Time
	result chan echoResult
}

// EchoSession permits several unanswered requests on one ICMP connection.
type EchoSession struct {
	connection  net.PacketConn
	v4          *ipv4.PacketConn
	v6          *ipv6.PacketConn
	target      netip.Addr
	identifier  uint16
	sequence    uint16
	mu          sync.Mutex
	pending     map[uint16]pendingEcho
	reader      sync.WaitGroup
	readerError error
	closed      bool
	clock       internalclock.Clock
}

// OpenEchoSession assigns one connection identifier for persistent probes.
func OpenEchoSession(ctx context.Context, iface string, source, target netip.Addr, identifier uint16) (result *EchoSession, failure error) {
	defer func() {
		if failure != nil {
			slog.WarnContext(ctx, "open echo session failed", "interface", iface, "error", failure)
		}
	}()
	if !target.IsValid() || (source.IsValid() && source.Is4() != target.Is4()) {
		return nil, errors.New("echo source and target require matching address families")
	}
	session := new(EchoSession)
	session.target = target
	session.identifier = identifier
	session.pending = make(map[uint16]pendingEcho)
	session.clock = internalclock.Real{}
	if target.Is4() {
		session.connection, session.v4, failure = openEcho4(ctx, iface, source)
	} else {
		session.connection, session.v6, failure = openEcho6(ctx, iface, source)
	}
	if failure != nil {
		return nil, failure
	}
	session.reader.Go(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				session.fail(fmt.Errorf("echo reader panic: %v", recovered))
				slog.ErrorContext(ctx, "echo reader panic", "error", recovered)
			}
		}()
		session.readReplies()
	})
	return session, nil
}

func openEcho4(ctx context.Context, iface string, source netip.Addr) (net.PacketConn, *ipv4.PacketConn, error) {
	configuration := net.ListenConfig{}
	if iface != "" {
		configuration.Control = bindToDevice(iface)
	}
	address := "0.0.0.0"
	if source.IsValid() {
		address = source.String()
	}
	connection, err := configuration.ListenPacket(ctx, "ip4:icmp", address)
	if err != nil {
		slog.WarnContext(ctx, "open IPv4 echo socket failed", "interface", iface, "error", err)
		return nil, nil, fmt.Errorf("open IPv4 echo socket: %w", err)
	}
	packet := ipv4.NewPacketConn(connection)
	if err := packet.SetControlMessage(ipv4.FlagDst, true); err != nil {
		slog.WarnContext(ctx, "enable IPv4 reply destination failed", "interface", iface, "error", err)
		_ = connection.Close()
		return nil, nil, fmt.Errorf("enable IPv4 reply destination: %w", err)
	}
	return connection, packet, nil
}

func openEcho6(ctx context.Context, iface string, source netip.Addr) (net.PacketConn, *ipv6.PacketConn, error) {
	address := "::"
	if source.IsValid() {
		address = source.String()
	}
	connection, err := icmp.ListenPacket("ip6:ipv6-icmp", address)
	if err != nil {
		slog.WarnContext(ctx, "open IPv6 echo socket failed", "interface", iface, "error", err)
		return nil, nil, fmt.Errorf("open IPv6 echo socket: %w", err)
	}
	if iface != "" {
		if err := NewV6Probe(iface, slog.Default()).bindProbeToDevice(ctx, connection); err != nil {
			_ = connection.Close()
			return nil, nil, err
		}
	}
	packet := connection.IPv6PacketConn()
	if err := packet.SetControlMessage(ipv6.FlagDst, true); err != nil {
		slog.WarnContext(ctx, "enable IPv6 reply destination failed", "interface", iface, "error", err)
		_ = connection.Close()
		return nil, nil, fmt.Errorf("enable IPv6 reply destination: %w", err)
	}
	return connection, packet, nil
}

// Probe preserves the session identifier while giving each request its own deadline.
func (session *EchoSession) Probe(ctx context.Context, timeout time.Duration) (reply PingProbeResult, failure error) {
	defer func() {
		if failure != nil && !errors.Is(failure, context.Canceled) && !errors.Is(failure, context.DeadlineExceeded) {
			slog.WarnContext(ctx, "echo request failed", "target", session.target, "error", failure)
		}
	}()
	if err := ctx.Err(); err != nil {
		return reply, fmt.Errorf("echo request canceled: %w", err)
	}
	start := session.clock.Now()
	session.mu.Lock()
	if session.closed || session.readerError != nil {
		session.mu.Unlock()
		return reply, errors.New("echo session is closed")
	}
	session.sequence++
	sequence := session.sequence
	if _, exists := session.pending[sequence]; exists {
		session.mu.Unlock()
		return reply, errors.New("echo sequence space is full")
	}
	pending := pendingEcho{start: start, result: make(chan echoResult, 1)}
	session.pending[sequence] = pending
	session.mu.Unlock()
	defer func() { session.mu.Lock(); delete(session.pending, sequence); session.mu.Unlock() }()
	message, _, _ := newEchoRequest(start)
	if session.target.Is4() {
		message, _, _ = newEchoRequest4(start)
	}
	echo, ok := message.Body.(*icmp.Echo)
	if !ok {
		return reply, errors.New("echo request requires an ICMP echo body")
	}
	echo.ID = int(session.identifier)
	echo.Seq = int(sequence)
	packet, err := message.Marshal(nil)
	if err != nil {
		return reply, fmt.Errorf("encode echo request: %w", err)
	}
	destination := &net.IPAddr{IP: session.target.AsSlice(), Zone: session.target.Zone()}
	if _, err := session.connection.WriteTo(packet, destination); err != nil {
		return reply, fmt.Errorf("write echo request: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-pending.result:
		return result.reply, result.err
	case <-ctx.Done():
		return reply, fmt.Errorf("echo request canceled: %w", ctx.Err())
	case <-timer.C:
		return reply, context.DeadlineExceeded
	}
}

func (session *EchoSession) readPacket(buffer []byte) (int, netip.Addr, net.Addr, error) {
	var source netip.Addr
	if session.v4 != nil {
		count, control, peer, err := session.v4.ReadFrom(buffer)
		if control != nil {
			source, _ = netip.AddrFromSlice(control.Dst)
		}
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Warn("read IPv4 echo packet failed", "target", session.target, "error", err)
			}
			return count, source.Unmap(), peer, fmt.Errorf("read IPv4 echo packet: %w", err)
		}
		return count, source.Unmap(), peer, nil
	}
	count, control, peer, err := session.v6.ReadFrom(buffer)
	if control != nil {
		source, _ = netip.AddrFromSlice(control.Dst)
	}
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			slog.Warn("read IPv6 echo packet failed", "target", session.target, "error", err)
		}
		return count, source, peer, fmt.Errorf("read IPv6 echo packet: %w", err)
	}
	return count, source, peer, nil
}

func (session *EchoSession) readReplies() {
	buffer := make([]byte, 1500)
	protocol := 58
	if session.target.Is4() {
		protocol = icmpV4Protocol
	}
	for {
		count, source, peer, err := session.readPacket(buffer)
		if err != nil {
			session.fail(fmt.Errorf("read echo reply: %w", err))
			return
		}
		message, err := icmp.ParseMessage(protocol, buffer[:count])
		if err != nil {
			continue
		}
		echo, ok := message.Body.(*icmp.Echo)
		if !ok || echo.ID != int(session.identifier) || !peerMatchesTarget(peer, session.target) {
			continue
		}
		matched := matchesProbeReply6(buffer[:count], echo.ID, echo.Seq)
		if session.target.Is4() {
			matched = matchesProbeReply4(buffer[:count], peer, session.target, echo.ID, echo.Seq)
		}
		if !matched {
			continue
		}
		if echo.Seq < 0 || echo.Seq > math.MaxUint16 {
			continue
		}
		sequence := uint16(echo.Seq)
		session.mu.Lock()
		pending, exists := session.pending[sequence]
		if exists {
			delete(session.pending, sequence)
			pending.result <- echoResult{reply: PingProbeResult{RoundTripTime: session.clock.Now().Sub(pending.start), Source: source}, err: nil}
		}
		session.mu.Unlock()
	}
}

func (session *EchoSession) fail(err error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.readerError = err
	for sequence, pending := range session.pending {
		pending.result <- echoResult{reply: PingProbeResult{RoundTripTime: 0, Source: netip.Addr{}}, err: err}
		delete(session.pending, sequence)
	}
}

// Close interrupts pending requests and waits for the reply reader to stop.
func (session *EchoSession) Close() error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	session.mu.Unlock()
	err := session.connection.Close()
	session.reader.Wait()
	if err != nil {
		slog.Warn("close echo session failed", "error", err)
		return fmt.Errorf("close echo socket: %w", err)
	}
	return nil
}
