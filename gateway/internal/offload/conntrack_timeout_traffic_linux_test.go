//go:build linux && netns

package offload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	conntrackTrafficFamilyTimeout      = 30 * time.Second
	conntrackTrafficRequestTimeout     = time.Second
	conntrackTrafficIdlePause          = 100 * time.Millisecond
	conntrackTrafficExposureTimeout    = 5 * time.Second
	conntrackTrafficExposureInterval   = 10 * time.Millisecond
	conntrackTrafficWriterJoinTimeout  = 2 * time.Second
	conntrackTrafficBurstPause         = 2 * time.Millisecond
	conntrackTrafficMaximumRefreshes   = 10000
	conntrackTrafficBurstsPerDirection = 1000
	conntrackTrafficControlBursts      = 1
	conntrackTrafficPayloadBytes       = 4096
	conntrackTrafficPayloadValue       = 0xa5
	conntrackTrafficEstablishedSeconds = 432000
	conntrackTrafficRequestedSeconds   = 300
	conntrackTrafficCompetingSeconds   = 1
	conntrackTrafficEstablishedState   = 3
	conntrackTrafficTimeoutPath        = "/proc/sys/net/netfilter/nf_conntrack_tcp_timeout_established"
	conntrackTrafficTable              = "mwan_timeout_traffic"
	conntrackTrafficChain              = "output"
	conntrackTrafficRegister           = 1
	conntrackTrafficZone               = 0
)

func TestConntrackTimeoutRefreshActiveTCP(t *testing.T) {
	if !conntrackEventsNamespace(t) {
		return
	}
	conntrackTrafficNamespace(t)
	for _, family := range []struct {
		name    string
		network string
		address string
	}{
		{name: "IPv4", network: "tcp4", address: "127.0.0.1:0"},
		{name: "IPv6", network: "tcp6", address: "[::1]:0"},
	} {
		t.Run(family.name, func(t *testing.T) {
			conntrackTrafficFamily(t, family.network, family.address)
		})
	}
}

func TestConntrackTimeoutRefreshCompetingWriter(t *testing.T) {
	if !conntrackEventsNamespace(t) {
		return
	}
	conntrackTrafficNamespace(t)
	ctx, cancel := context.WithTimeout(t.Context(), conntrackTrafficFamilyTimeout)
	defer cancel()
	_, _, flow := conntrackTrafficSockets(t, ctx, "tcp4", "127.0.0.1:0")
	foreground, err := NewConntrackUpdater(conntrackTrafficRequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conntrackTrafficClose(t, foreground)
	background, err := NewConntrackUpdater(conntrackTrafficRequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conntrackTrafficClose(t, background)
	if err := foreground.Refresh(ctx, flow, conntrackTrafficRequestedSeconds); err != nil {
		t.Fatalf("initialize idle TCP timeout: %v", err)
	}
	writerContext, stopWriter := context.WithCancel(ctx)
	started := make(chan struct{})
	finished := make(chan struct{})
	var writerErr error
	writerRefreshes := 0
	go func() {
		defer close(finished)
		for writerRefreshes < conntrackTrafficMaximumRefreshes {
			if writerContext.Err() != nil {
				return
			}
			writerRefreshes++
			if err := background.Refresh(writerContext, flow, conntrackTrafficCompetingSeconds); err != nil {
				if writerContext.Err() == nil {
					writerErr = err
				}
				return
			}
			if writerRefreshes == 1 {
				close(started)
			}
		}
	}()
	joinWriter := func() {
		stopWriter()
		timer := time.NewTimer(conntrackTrafficWriterJoinTimeout)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Fatalf("competing TCP timeout writer did not stop within %s", conntrackTrafficWriterJoinTimeout)
		}
	}
	defer joinWriter()
	select {
	case <-started:
	case <-finished:
		t.Fatalf("competing TCP timeout writer stopped before its first successful refresh: %v", writerErr)
	case <-ctx.Done():
		t.Fatalf("competing TCP timeout writer did not start: %v", ctx.Err())
	}
	var failure *ConntrackTimeoutVerificationError
	refreshes := 0
refreshLoop:
	for refreshes < conntrackTrafficMaximumRefreshes {
		select {
		case <-finished:
			break refreshLoop
		case <-ctx.Done():
			t.Fatalf("competing TCP timeout requests exceeded %s: %v", conntrackTrafficFamilyTimeout, ctx.Err())
		default:
		}
		refreshes++
		if err := foreground.Refresh(ctx, flow, conntrackTrafficRequestedSeconds); err != nil {
			if !errors.As(err, &failure) {
				t.Fatalf("foreground TCP timeout refresh failed without a timeout verification error: %v", err)
			}
			break
		}
	}
	joinWriter()
	if writerErr != nil {
		t.Fatalf("competing TCP timeout writer failed: %v", writerErr)
	}
	if failure == nil {
		t.Fatalf("competing TCP timeout writers did not expose a lower-bound rejection: foreground_calls=%d background_calls=%d", refreshes, writerRefreshes)
	}
	if failure.ConnectionID != flow.ID || failure.RequestedSeconds != conntrackTrafficRequestedSeconds ||
		uint64(failure.ObservedSeconds)+failure.AllowanceSeconds >= uint64(failure.RequestedSeconds) {
		t.Fatalf("competing writer returned an invalid lower-bound rejection: %+v", failure)
	}
	current, err := FreshConntrackFlow(ctx, flow)
	if err != nil {
		t.Fatalf("read original TCP identity after competing writes: %v", err)
	}
	conntrackTrafficEstablished(t, current)
	t.Logf("competing TCP timeout rejection: connection_id=%d requested_seconds=%d observed_seconds=%d allowance_seconds=%d foreground_calls=%d background_calls=%d", failure.ConnectionID, failure.RequestedSeconds, failure.ObservedSeconds, failure.AllowanceSeconds, refreshes, writerRefreshes)
}

func conntrackTrafficNamespace(t *testing.T) {
	t.Helper()
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatalf("bring namespace loopback up: %v", err)
	}
	connection := &nftables.Conn{}
	table := connection.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet, Name: conntrackTrafficTable,
	})
	chain := connection.AddChain(&nftables.Chain{
		Name: conntrackTrafficChain, Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter,
	})
	// Reading ct state activates both families' conntrack hooks in this namespace.
	connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
		&expr.Ct{Key: expr.CtKeySTATE, Register: conntrackTrafficRegister},
		&expr.Counter{},
	}})
	if err := connection.Flush(); err != nil {
		t.Fatalf("activate namespace conntrack with nftables: %v", err)
	}
	t.Cleanup(func() {
		connection.DelTable(table)
		if err := connection.Flush(); err != nil {
			t.Errorf("delete namespace conntrack rule: %v", err)
		}
	})
	configured, err := os.ReadFile(conntrackTrafficTimeoutPath)
	if err != nil {
		t.Fatalf("read namespace established TCP timeout: %v", err)
	}
	seconds, err := strconv.ParseUint(strings.TrimSpace(string(configured)), 10, 32)
	if err != nil {
		t.Fatalf("parse namespace established TCP timeout: %v", err)
	}
	if seconds != conntrackTrafficEstablishedSeconds {
		t.Fatalf("set namespace nf_conntrack_tcp_timeout_established to %d seconds; observed %d", conntrackTrafficEstablishedSeconds, seconds)
	}
	t.Logf("namespace established TCP timeout=%d seconds; requested timeout=%d seconds", seconds, conntrackTrafficRequestedSeconds)
}

func conntrackTrafficFamily(t *testing.T, network, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conntrackTrafficFamilyTimeout)
	defer cancel()
	client, server, flow := conntrackTrafficSockets(t, ctx, network, address)
	payload := bytes.Repeat([]byte{conntrackTrafficPayloadValue}, conntrackTrafficPayloadBytes)
	updater, err := NewConntrackUpdater(conntrackTrafficRequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conntrackTrafficClose(t, updater)
	if err := updater.SetLiberal(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if err := updater.Refresh(ctx, flow, conntrackTrafficRequestedSeconds); err != nil {
		t.Fatalf("idle TCP timeout refresh: %v", err)
	}
	idle, err := FreshConntrackFlow(ctx, flow)
	if err != nil {
		t.Fatal(err)
	}
	conntrackTrafficEstablished(t, idle)
	if idle.Timeout > conntrackTrafficRequestedSeconds || idle.Timeout+1 < conntrackTrafficRequestedSeconds {
		t.Fatalf("idle refresh did not retain the requested timeout: observed=%d requested=%d", idle.Timeout, conntrackTrafficRequestedSeconds)
	}
	t.Logf("idle TCP refresh: id=%d state=%d observed_seconds=%d", idle.ID, idle.ProtoInfo.TCP.State, idle.Timeout)
	controlHash, err := conntrackTrafficTransfer(ctx, server, client, payload, conntrackTrafficControlBursts, nil)
	if err != nil {
		t.Fatalf("send one-way TCP expiry control: %v", err)
	}
	expectedControlHash := sha256.Sum256(payload)
	if !bytes.Equal(controlHash, expectedControlHash[:]) {
		t.Fatalf("one-way TCP expiry control changed payload bytes: expected=%x observed=%x", expectedControlHash, controlHash)
	}
	exposed := conntrackTrafficExpiryExposure(t, ctx, flow)
	t.Logf("packet-driven TCP expiry exposure: id=%d idle_seconds=%d after_one_way_seconds=%d", exposed.ID, idle.Timeout, exposed.Timeout)
	started := make(chan struct{})
	finished := make(chan struct{})
	var trafficResult conntrackTrafficResult
	var trafficErr error
	go func() {
		defer close(finished)
		trafficResult, trafficErr = conntrackTrafficPhases(ctx, client, server, payload, started)
	}()
	defer func() {
		// Closing the sockets interrupts every read or write before joining the worker.
		conntrackTrafficClose(t, client)
		conntrackTrafficClose(t, server)
		<-finished
	}()
	select {
	case <-started:
	case <-finished:
		t.Fatalf("TCP payload exchange stopped before active refresh: %v", trafficErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var refreshErr error
	refreshes := 0
refreshLoop:
	for refreshes < conntrackTrafficMaximumRefreshes {
		select {
		case <-finished:
			break refreshLoop
		case <-ctx.Done():
			break refreshLoop
		default:
		}
		refreshes++
		if refreshErr = updater.Refresh(ctx, flow, conntrackTrafficRequestedSeconds); refreshErr != nil {
			break
		}
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatalf("TCP payload exchange did not finish within %s: %v", conntrackTrafficFamilyTimeout, ctx.Err())
	}
	if trafficErr != nil {
		t.Fatalf("original TCP payload exchange failed: %v", trafficErr)
	}
	expectedHash := sha256.New()
	for range conntrackTrafficBurstsPerDirection {
		if _, err := expectedHash.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, observed := range [][]byte{trafficResult.originalHash, trafficResult.replyHash} {
		if !bytes.Equal(observed, expectedHash.Sum(nil)) {
			t.Fatalf("original TCP payload exchange changed bytes: expected=%x observed=%x", expectedHash.Sum(nil), observed)
		}
	}
	t.Logf("original TCP exchange completed: bytes_per_direction=%d sha256=%x refresh_calls=%d", trafficResult.bytesPerDirection, trafficResult.originalHash, refreshes)
	if refreshes == 0 {
		t.Fatal("TCP traffic completed without an active timeout refresh")
	}
	if refreshErr != nil {
		t.Errorf("active TCP timeout refresh failed: %v", refreshErr)
	}
	current, err := FreshConntrackFlow(ctx, flow)
	if err != nil {
		t.Fatalf("final same-identity TCP lookup: %v", err)
	}
	conntrackTrafficEstablished(t, current)
	t.Logf("final TCP state: id=%d state=%d timeout_seconds=%d", current.ID, current.ProtoInfo.TCP.State, current.Timeout)
}

func conntrackTrafficSockets(t *testing.T, ctx context.Context, network, address string) (net.Conn, net.Conn, conntrack.Flow) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("TCP traffic test requires a deadline")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		t.Fatalf("listen for real TCP traffic: %v", err)
	}
	t.Cleanup(func() { conntrackTrafficClose(t, listener) })
	if err := listener.(*net.TCPListener).SetDeadline(deadline); err != nil {
		t.Fatalf("bound real TCP accept: %v", err)
	}
	client, err := (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	if err != nil {
		t.Fatalf("connect real TCP socket: %v", err)
	}
	t.Cleanup(func() { conntrackTrafficClose(t, client) })
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept real TCP socket: %v", err)
	}
	t.Cleanup(func() { conntrackTrafficClose(t, server) })
	for _, socket := range []net.Conn{client, server} {
		if err := socket.SetDeadline(deadline); err != nil {
			t.Fatalf("bound real TCP socket lifetime: %v", err)
		}
	}
	conntrackTrafficPause(t, ctx)
	original := client.LocalAddr().(*net.TCPAddr).AddrPort()
	destination := client.RemoteAddr().(*net.TCPAddr).AddrPort()
	tuple := conntrack.Tuple{
		IP: conntrack.IPTuple{SourceAddress: original.Addr(), DestinationAddress: destination.Addr()},
		Proto: conntrack.ProtoTuple{
			Protocol: unix.IPPROTO_TCP, SourcePort: original.Port(), DestinationPort: destination.Port(),
		},
	}
	flow, err := LookupConntrackFlow(ctx, tuple, conntrackTrafficZone)
	if err != nil {
		t.Fatal(err)
	}
	conntrackTrafficEstablished(t, flow)
	return client, server, flow
}

func conntrackTrafficExpiryExposure(t *testing.T, ctx context.Context, flow conntrack.Flow) conntrack.Flow {
	t.Helper()
	exposureContext, cancel := context.WithTimeout(ctx, conntrackTrafficExposureTimeout)
	defer cancel()
	ticker := time.NewTicker(conntrackTrafficExposureInterval)
	defer ticker.Stop()
	for {
		current, err := FreshConntrackFlow(exposureContext, flow)
		if err != nil {
			t.Fatalf("read packet-driven TCP expiry control: %v", err)
		}
		conntrackTrafficEstablished(t, current)
		if current.Timeout > conntrackTrafficRequestedSeconds {
			return current
		}
		select {
		case <-ticker.C:
		case <-exposureContext.Done():
			t.Fatalf("TCP packets did not extend expiry above %d seconds within %s: observed_seconds=%d: %v", conntrackTrafficRequestedSeconds, conntrackTrafficExposureTimeout, current.Timeout, exposureContext.Err())
		}
	}
}

type conntrackTrafficResult struct {
	originalHash      []byte
	replyHash         []byte
	bytesPerDirection int
}

func conntrackTrafficPhases(ctx context.Context, client, server net.Conn, payload []byte, started chan<- struct{}) (conntrackTrafficResult, error) {
	replyHash, err := conntrackTrafficTransfer(ctx, server, client, payload, conntrackTrafficBurstsPerDirection, started)
	if err != nil {
		return conntrackTrafficResult{}, fmt.Errorf("complete original TCP download: %w", err)
	}
	originalHash, err := conntrackTrafficTransfer(ctx, client, server, payload, conntrackTrafficBurstsPerDirection, nil)
	if err != nil {
		return conntrackTrafficResult{}, fmt.Errorf("complete original TCP upload: %w", err)
	}
	return conntrackTrafficResult{
		originalHash: originalHash, replyHash: replyHash,
		bytesPerDirection: conntrackTrafficBurstsPerDirection * len(payload),
	}, nil
}

func conntrackTrafficTransfer(ctx context.Context, sender, receiver net.Conn, payload []byte, bursts int, started chan<- struct{}) ([]byte, error) {
	receivedHash := sha256.New()
	received := make([]byte, len(payload))
	timer := time.NewTimer(conntrackTrafficBurstPause)
	defer timer.Stop()
	for index := range bursts {
		written, err := sender.Write(payload)
		if err != nil {
			return nil, fmt.Errorf("write one-way payload: %w", err)
		}
		if written != len(payload) {
			return nil, fmt.Errorf("write one-way payload: bytes=%d: %w", written, io.ErrShortWrite)
		}
		if _, err := io.ReadFull(receiver, received); err != nil {
			return nil, fmt.Errorf("read one-way payload: %w", err)
		}
		if _, err := receivedHash.Write(received); err != nil {
			return nil, err
		}
		if index == 0 && started != nil {
			close(started)
		}
		timer.Reset(conntrackTrafficBurstPause)
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, fmt.Errorf("pace one-way payload: %w", ctx.Err())
		}
	}
	return receivedHash.Sum(nil), nil
}

func conntrackTrafficPause(t *testing.T, ctx context.Context) {
	t.Helper()
	timer := time.NewTimer(conntrackTrafficIdlePause)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func conntrackTrafficEstablished(t *testing.T, flow conntrack.Flow) {
	t.Helper()
	if flow.ID == 0 || flow.ProtoInfo.TCP == nil || flow.ProtoInfo.TCP.State != conntrackTrafficEstablishedState {
		t.Fatalf("real TCP socket did not retain an established conntrack identity: %+v", flow)
	}
}

func conntrackTrafficClose(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close TCP traffic test resource: %v", err)
	}
}
