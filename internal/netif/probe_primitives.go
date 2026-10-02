package netif

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

const (
	icmpV4Protocol     = 1
	httpResponseMaxLen = 4 * 1024
)

type httpAddressFamily string

const (
	httpFamilyAny httpAddressFamily = ""
	httpFamilyV4  httpAddressFamily = "inet"
	httpFamilyV6  httpAddressFamily = "inet6"
)

// HTTPResult is the observed status and trimmed response body from HTTPGet.
type HTTPResult struct {
	StatusCode int
	Body       string
}

// Ping4 keeps IPv4 reachability checks in-process so callers do not depend on
// platform ping binaries.
func Ping4(
	ctx context.Context, iface string, target netip.Addr, timeout time.Duration,
) (time.Duration, error) {
	return ping4(ctx, iface, target, timeout, realClock{})
}

func ping4(
	ctx context.Context,
	iface string,
	target netip.Addr,
	timeout time.Duration,
	probeClock clock,
) (time.Duration, error) {
	result, err := ping4Source(ctx, iface, netip.Addr{}, target, timeout, probeClock, false)
	return result.RoundTripTime, err
}

// PingProbeResult includes the matched reply destination as the actual request source.
type PingProbeResult struct {
	RoundTripTime time.Duration
	Source        netip.Addr
}

// PingProbe binds an optional source and records matched echo reply metadata.
func PingProbe(ctx context.Context, iface string, source, target netip.Addr, timeout time.Duration) (PingProbeResult, error) {
	if target.Is4() {
		return ping4Source(ctx, iface, source, target, timeout, realClock{}, true)
	}
	return NewV6Probe(iface, slog.Default()).pingICMP6Source(ctx, source, target, timeout, true)
}

func ping4Source(ctx context.Context, iface string, source, target netip.Addr, timeout time.Duration, probeClock clock, observe bool) (PingProbeResult, error) {
	log := slog.With(
		"component", "ping4",
		"iface", iface,
		"target", target.String(),
		"timeout_ms", timeout.Milliseconds(),
	)
	log.DebugContext(ctx, "netif: Ping4 entry")

	if !target.Is4() {
		log.WarnContext(ctx, "netif: Ping4 target is not IPv4")
		return PingProbeResult{}, fmt.Errorf("Ping4: target %q is not IPv4", target)
	}

	listenConfig := net.ListenConfig{}
	if iface != "" {
		listenConfig.Control = bindToDevice(iface)
	}
	listenAddress := "0.0.0.0"
	if source.IsValid() {
		listenAddress = source.String()
	}
	connection, err := listenConfig.ListenPacket(ctx, "ip4:icmp", listenAddress)
	if err != nil {
		log.WarnContext(ctx, "netif: Ping4 ListenPacket failed", "err", err)
		return PingProbeResult{}, fmt.Errorf("Ping4 ListenPacket: %w", err)
	}
	defer func() {
		_ = connection.Close()
	}()

	packetConnection := ipv4.NewPacketConn(connection)
	if observe {
		if err := packetConnection.SetControlMessage(ipv4.FlagDst, true); err != nil {
			return PingProbeResult{}, fmt.Errorf("Ping4 source metadata: %w", err)
		}
	}
	startTime := probeClock.Now()
	message, id, sequence := newEchoRequest4(startTime)
	packet, err := message.Marshal(nil)
	if err != nil {
		log.WarnContext(ctx, "netif: Ping4 marshal echo failed", "err", err)
		return PingProbeResult{}, fmt.Errorf("Ping4 marshal echo: %w", err)
	}
	deadline := startTime.Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		log.WarnContext(ctx, "netif: Ping4 SetReadDeadline failed", "err", err)
		return PingProbeResult{}, fmt.Errorf("Ping4 SetReadDeadline: %w", err)
	}
	destination := &net.IPAddr{IP: target.AsSlice()}
	if _, err := connection.WriteTo(packet, destination); err != nil {
		log.WarnContext(ctx, "netif: Ping4 WriteTo failed", "err", err)
		return PingProbeResult{}, fmt.Errorf("Ping4 WriteTo(%s): %w", target, err)
	}
	log.DebugContext(ctx, "netif: Ping4 echo sent", "id", id, "seq", sequence)

	readBuffer := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			log.WarnContext(ctx, "netif: Ping4 context cancelled", "err", ctx.Err())
			return PingProbeResult{}, fmt.Errorf("Ping4: %w", ctx.Err())
		default:
		}

		var bytesRead int
		var peer net.Addr
		var readError error
		var control *ipv4.ControlMessage
		if observe {
			bytesRead, control, peer, readError = packetConnection.ReadFrom(readBuffer)
		} else {
			bytesRead, peer, readError = connection.ReadFrom(readBuffer)
		}
		roundTripTime := probeClock.Now().Sub(startTime)
		if readError != nil {
			return PingProbeResult{}, classifyPingReadError(ctx, readError)
		}
		// Require the reply to come from the target. Two concurrent probes
		// from this process can collide on id and the 15-bit sequence, so the
		// peer address is what keeps one probe from accepting another's reply.
		if !matchesProbeReply4(readBuffer[:bytesRead], peer, target, id, sequence) {
			continue
		}
		log.DebugContext(
			ctx, "netif: Ping4 echo reply received",
			"from", peer.String(),
			"rtt_ms", roundTripTime.Milliseconds(),
		)
		result := PingProbeResult{RoundTripTime: roundTripTime, Source: netip.Addr{}}
		if observe && control != nil {
			result.Source, _ = netip.AddrFromSlice(control.Dst)
		}
		return result, nil
	}
}

// Ping6 keeps one-shot checks on the proven V6Probe path so interface-bound
// behavior remains shared with the failover health probes.
func Ping6(
	ctx context.Context, iface string, target netip.Addr, timeout time.Duration,
) (time.Duration, error) {
	return NewV6Probe(iface, slog.Default()).PingICMP6(ctx, target, timeout)
}

// HTTPCheck uses the probe interface for outbound connections so HTTP and
// ICMP checks observe the same source-routing behavior.
func HTTPCheck(
	ctx context.Context, iface string, url string, timeout time.Duration,
) (int, error) {
	result, err := httpGet(ctx, iface, "", url, timeout, false, "HTTPCheck")
	if err != nil {
		return 0, err
	}
	return result.StatusCode, nil
}

// HTTPCheck6 forces the probe over IPv6 (tcp6) so the HTTP leg contributes to
// the same address family as the ping leg it backs, matching the shell's
// separate curl -6 probe.
func HTTPCheck6(
	ctx context.Context, iface string, url string, timeout time.Duration,
) (int, error) {
	result, err := httpGet(
		ctx, iface, string(httpFamilyV6), url, timeout, false, "HTTPCheck6",
	)
	if err != nil {
		return 0, err
	}
	return result.StatusCode, nil
}

// HTTPCheck4 forces the probe over IPv4 (tcp4), the curl -4 twin of HTTPCheck6.
func HTTPCheck4(
	ctx context.Context, iface string, url string, timeout time.Duration,
) (int, error) {
	result, err := httpGet(
		ctx, iface, string(httpFamilyV4), url, timeout, false, "HTTPCheck4",
	)
	if err != nil {
		return 0, err
	}
	return result.StatusCode, nil
}

// HTTPGet sends one interface-bound request with an optional address family.
// The family accepts "inet" for IPv4, "inet6" for IPv6, or empty for the
// default dual-stack behavior.
func HTTPGet(
	ctx context.Context,
	iface string,
	family string,
	url string,
	timeout time.Duration,
) (HTTPResult, error) {
	return httpGet(ctx, iface, family, url, timeout, true, "HTTPGet")
}

// HTTPProbeSpec configures a direct request and its socket binding.
type HTTPProbeSpec struct {
	Interface string
	Family    string
	Source    netip.Addr
	URL       string
	Method    string
	Timeout   time.Duration
}

// HTTPProbeResult includes the actual connected socket addresses.
type HTTPProbeResult struct {
	HTTPResult
	Source      netip.Addr
	Destination netip.Addr
	Connected   bool
}

// DialProbe opens a source-bound TCP connection with the shared interface binding.
func DialProbe(ctx context.Context, iface string, source netip.Addr, family, address string, timeout time.Duration) (net.Conn, error) {
	network, err := httpNetwork(family)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout}
	if iface != "" {
		dialer.Control = bindToDevice(iface)
	}
	if source.IsValid() {
		dialer.LocalAddr = &net.TCPAddr{IP: source.AsSlice(), Zone: source.Zone(), Port: 0}
	}
	connection, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		slog.WarnContext(ctx, "TCP observation dial failed", "iface", iface, "err", err)
		return nil, fmt.Errorf("TCP observation dial: %w", err)
	}
	return connection, nil
}

// HTTPProbe records socket endpoints and disables proxies and redirects.
func HTTPProbe(ctx context.Context, spec HTTPProbeSpec) (HTTPProbeResult, error) {
	return httpRequest(ctx, spec, true, true)
}

func httpGet(ctx context.Context, iface, family, url string, timeout time.Duration, readBody bool, operation string) (HTTPResult, error) {
	result, err := httpRequest(ctx, HTTPProbeSpec{
		Interface: iface, Family: family, Source: netip.Addr{}, URL: url, Method: http.MethodGet, Timeout: timeout,
	}, readBody, false)
	if err != nil {
		slog.WarnContext(ctx, "HTTP probe failed", "operation", operation, "err", err)
		return HTTPResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	return result.HTTPResult, nil
}

func httpRequest(ctx context.Context, spec HTTPProbeSpec, readBody, direct bool) (observed HTTPProbeResult, requestError error) {
	defer func() {
		if requestError != nil {
			slog.WarnContext(ctx, "HTTP request failed", "method", spec.Method, "err", requestError)
		}
	}()
	network, err := httpNetwork(spec.Family)
	if err != nil {
		return HTTPProbeResult{}, err
	}
	var result HTTPProbeResult
	var socketResult HTTPProbeResult
	var socketMutex sync.Mutex
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		socketMutex.Lock()
		defer socketMutex.Unlock()
		socketResult.Connected = true
		if address, ok := info.Conn.LocalAddr().(*net.TCPAddr); ok {
			socketResult.Source = address.AddrPort().Addr().Unmap()
		}
		if address, ok := info.Conn.RemoteAddr().(*net.TCPAddr); ok {
			socketResult.Destination = address.AddrPort().Addr().Unmap()
		}
	}}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), spec.Method, spec.URL, nil)
	if err != nil {
		return result, fmt.Errorf("HTTP request: %w", err)
	}
	client := &http.Client{Timeout: spec.Timeout}
	if direct {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	}
	if spec.Interface != "" || spec.Family != "" || spec.Source.IsValid() || direct {
		dialer := &net.Dialer{}
		if spec.Interface != "" {
			dialer.Control = bindToDevice(spec.Interface)
		}
		if spec.Source.IsValid() {
			dialer.LocalAddr = &net.TCPAddr{IP: spec.Source.AsSlice(), Zone: spec.Source.Zone(), Port: 0}
		}
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return result, fmt.Errorf("HTTP default transport has type %T", http.DefaultTransport)
		}
		transport := defaultTransport.Clone()
		if direct {
			transport.Proxy = nil
			transport.DisableKeepAlives = true
		}
		transport.DialContext = func(dialContext context.Context, _ string, address string) (net.Conn, error) {
			return dialer.DialContext(dialContext, network, address)
		}
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}
	response, err := client.Do(request)
	socketMutex.Lock()
	result = socketResult
	socketMutex.Unlock()
	if err != nil {
		return result, fmt.Errorf("HTTP %s: %w", spec.Method, err)
	}
	defer func() { _ = response.Body.Close() }()
	result.StatusCode = response.StatusCode
	if !readBody {
		return result, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, httpResponseMaxLen+1))
	if err != nil {
		return result, fmt.Errorf("HTTP response body: %w", err)
	}
	if len(body) > httpResponseMaxLen {
		return result, errors.New("HTTP response body exceeds 4 KiB")
	}
	result.Body = strings.TrimSpace(string(body))
	return result, nil
}

func httpNetwork(family string) (string, error) {
	switch httpAddressFamily(family) {
	case httpFamilyAny:
		return "tcp", nil
	case httpFamilyV4:
		return "tcp4", nil
	case httpFamilyV6:
		return "tcp6", nil
	default:
		return "", fmt.Errorf("unsupported address family %q", family)
	}
}

func bindToDevice(
	iface string,
) func(network string, address string, connection syscall.RawConn) error {
	return func(_ string, _ string, connection syscall.RawConn) error {
		var bindError error
		controlError := connection.Control(func(fileDescriptor uintptr) {
			bindError = unix.SetsockoptString(
				int(fileDescriptor),
				unix.SOL_SOCKET,
				unix.SO_BINDTODEVICE,
				iface,
			)
		})
		if controlError != nil {
			slog.Warn(
				"netif: bindToDevice socket control failed",
				"iface", iface,
				"err", controlError,
			)
			return fmt.Errorf("access socket for interface %q: %w", iface, controlError)
		}
		if bindError != nil {
			slog.Warn(
				"netif: bindToDevice setsockopt failed",
				"iface", iface,
				"err", bindError,
			)
			return fmt.Errorf("bind socket to interface %q: %w", iface, bindError)
		}
		return nil
	}
}

func newEchoRequest4(startTime time.Time) (icmp.Message, int, int) {
	id := os.Getpid() & 0xffff
	sequence := int(startTime.UnixNano() & 0x7fff)
	return icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  sequence,
			Data: []byte("mwan-v4probe"),
		},
	}, id, sequence
}

func matchesEchoReply4(message *icmp.Message, id int, sequence int) bool {
	if message.Type != ipv4.ICMPTypeEchoReply {
		return false
	}
	echo, ok := message.Body.(*icmp.Echo)
	if !ok {
		return false
	}
	return echo.ID == id && echo.Seq == sequence
}

// peerMatchesTarget reports whether the reply's source address is the target we
// pinged. ReadFrom on an ip4:icmp socket yields a [net.IPAddr], so compare the
// address bytes; an unexpected peer type is treated as no match.
func peerMatchesTarget(peer net.Addr, target netip.Addr) bool {
	ipAddr, ok := peer.(*net.IPAddr)
	if !ok {
		return false
	}
	from, ok := netip.AddrFromSlice(ipAddr.IP)
	if !ok {
		return false
	}
	return from.Unmap() == target.Unmap()
}

func classifyPingReadError(ctx context.Context, err error) error {
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		if ctx.Err() != nil {
			slog.WarnContext(ctx, "ICMP observation canceled", "err", ctx.Err())
			return fmt.Errorf("ICMP observation: %w", ctx.Err())
		}
		return context.DeadlineExceeded
	}
	slog.WarnContext(ctx, "ICMP reply read failed", "err", err)
	return fmt.Errorf("ICMP reply read: %w", err)
}

func matchesProbeReply4(packet []byte, peer net.Addr, target netip.Addr, id, sequence int) bool {
	message, err := icmp.ParseMessage(icmpV4Protocol, packet)
	return err == nil && matchesEchoReply4(message, id, sequence) && peerMatchesTarget(peer, target)
}
