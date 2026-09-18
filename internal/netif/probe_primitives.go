package netif

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
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
	log := slog.With(
		"component", "ping4",
		"iface", iface,
		"target", target.String(),
		"timeout_ms", timeout.Milliseconds(),
	)
	log.DebugContext(ctx, "netif: Ping4 entry")

	if !target.Is4() {
		log.WarnContext(ctx, "netif: Ping4 target is not IPv4")
		return 0, fmt.Errorf("Ping4: target %q is not IPv4", target)
	}

	listenConfig := net.ListenConfig{}
	if iface != "" {
		listenConfig.Control = bindToDevice(iface)
	}
	connection, err := listenConfig.ListenPacket(ctx, "ip4:icmp", "0.0.0.0")
	if err != nil {
		log.WarnContext(ctx, "netif: Ping4 ListenPacket failed", "err", err)
		return 0, fmt.Errorf("Ping4 ListenPacket: %w", err)
	}
	defer func() {
		_ = connection.Close()
	}()

	startTime := probeClock.Now()
	message, id, sequence := newEchoRequest4(startTime)
	packet, err := message.Marshal(nil)
	if err != nil {
		log.WarnContext(ctx, "netif: Ping4 marshal echo failed", "err", err)
		return 0, fmt.Errorf("Ping4 marshal echo: %w", err)
	}
	deadline := startTime.Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		log.WarnContext(ctx, "netif: Ping4 SetReadDeadline failed", "err", err)
		return 0, fmt.Errorf("Ping4 SetReadDeadline: %w", err)
	}
	destination := &net.IPAddr{IP: target.AsSlice()}
	if _, err := connection.WriteTo(packet, destination); err != nil {
		log.WarnContext(ctx, "netif: Ping4 WriteTo failed", "err", err)
		return 0, fmt.Errorf("Ping4 WriteTo(%s): %w", target, err)
	}
	log.DebugContext(ctx, "netif: Ping4 echo sent", "id", id, "seq", sequence)

	readBuffer := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			log.WarnContext(ctx, "netif: Ping4 context cancelled", "err", ctx.Err())
			return 0, fmt.Errorf("Ping4: %w", ctx.Err())
		default:
		}

		bytesRead, peer, readError := connection.ReadFrom(readBuffer)
		roundTripTime := probeClock.Now().Sub(startTime)
		if readError != nil {
			var networkError net.Error
			if errors.As(readError, &networkError) && networkError.Timeout() {
				// A cancelled context (with or without its own deadline)
				// surfaces here as a read timeout; report it as the context
				// error, not a plain deadline, so callers can tell the two
				// apart.
				if ctxErr := ctx.Err(); ctxErr != nil {
					return 0, fmt.Errorf("Ping4: %w", ctxErr)
				}
				return 0, context.DeadlineExceeded
			}
			log.WarnContext(ctx, "netif: Ping4 ReadFrom failed", "err", readError)
			return 0, fmt.Errorf("Ping4 ReadFrom: %w", readError)
		}
		message, parseError := icmp.ParseMessage(
			icmpV4Protocol, readBuffer[:bytesRead],
		)
		if parseError != nil {
			log.DebugContext(
				ctx, "netif: Ping4 parse failed",
				"err", parseError,
			)
			continue
		}
		if !matchesEchoReply4(message, id, sequence) {
			continue
		}
		// Require the reply to come from the target. Two concurrent probes
		// from this process can collide on id and the 15-bit sequence, so the
		// peer address is what keeps one probe from accepting another's reply.
		if !peerMatchesTarget(peer, target) {
			continue
		}
		log.DebugContext(
			ctx, "netif: Ping4 echo reply received",
			"from", peer.String(),
			"rtt_ms", roundTripTime.Milliseconds(),
		)
		return roundTripTime, nil
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

func httpGet(
	ctx context.Context,
	iface string,
	family string,
	url string,
	timeout time.Duration,
	readBody bool,
	operation string,
) (HTTPResult, error) {
	log := slog.With(
		"component", strings.ToLower(operation),
		"iface", iface,
		"family", family,
		"url", url,
	)
	network, err := httpNetwork(family)
	if err != nil {
		return HTTPResult{}, fmt.Errorf("%s family: %w", operation, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.WarnContext(ctx, "netif: HTTP request failed", "err", err)
		return HTTPResult{}, fmt.Errorf("%s request: %w", operation, err)
	}

	client := &http.Client{Timeout: timeout}
	if iface != "" || family != "" {
		dialer := &net.Dialer{}
		if iface != "" {
			dialer.Control = bindToDevice(iface)
		}
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			transportError := fmt.Errorf(
				"%s: default transport has type %T",
				operation,
				http.DefaultTransport,
			)
			log.ErrorContext(
				ctx, "netif: HTTP default transport is incompatible",
				"err", transportError,
			)
			return HTTPResult{}, transportError
		}
		transport := defaultTransport.Clone()
		transport.DialContext = func(
			dialContext context.Context,
			_ string,
			address string,
		) (net.Conn, error) {
			return dialer.DialContext(dialContext, network, address)
		}
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}

	response, err := client.Do(request)
	if err != nil {
		log.WarnContext(ctx, "netif: HTTP GET failed", "err", err)
		return HTTPResult{}, fmt.Errorf("%s GET %q: %w", operation, url, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	result := HTTPResult{StatusCode: response.StatusCode, Body: ""}
	if !readBody {
		return result, nil
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, httpResponseMaxLen+1))
	if err != nil {
		return HTTPResult{}, fmt.Errorf("%s read %q response body: %w", operation, url, err)
	}
	if len(body) > httpResponseMaxLen {
		return HTTPResult{}, fmt.Errorf(
			"%s GET %q: response body exceeds 4 KiB",
			operation,
			url,
		)
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
