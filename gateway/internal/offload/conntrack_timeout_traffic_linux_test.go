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
	conntrackTrafficBurstPause         = 2 * time.Millisecond
	conntrackTrafficMaximumRefreshes   = 10000
	conntrackTrafficBurstsPerDirection = 1000
	conntrackTrafficControlBursts      = 1
	conntrackTrafficPayloadBytes       = 4096
	conntrackTrafficPayloadValue       = 0xa5
	conntrackTrafficEstablishedSeconds = 432000
	conntrackTrafficRequestedSeconds   = 300
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
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("TCP traffic test requires a deadline")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		t.Fatalf("listen for real TCP traffic: %v", err)
	}
	defer conntrackTrafficClose(t, listener)
	if err := listener.(*net.TCPListener).SetDeadline(deadline); err != nil {
		t.Fatalf("bound real TCP accept: %v", err)
	}
	client, err := (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	if err != nil {
		t.Fatalf("connect real TCP socket: %v", err)
	}
	defer conntrackTrafficClose(t, client)
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept real TCP socket: %v", err)
	}
	defer conntrackTrafficClose(t, server)
	for _, socket := range []net.Conn{client, server} {
		if err := socket.SetDeadline(deadline); err != nil {
			t.Fatalf("bound real TCP socket lifetime: %v", err)
		}
	}
	payload := bytes.Repeat([]byte{conntrackTrafficPayloadValue}, conntrackTrafficPayloadBytes)
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
	// A pause permits a pure ACK before the readback without another payload reply.
	conntrackTrafficPause(t, ctx)
	exposed, err := FreshConntrackFlow(ctx, flow)
	if err != nil {
		t.Fatalf("read packet-driven TCP expiry control: %v", err)
	}
	conntrackTrafficEstablished(t, exposed)
	if exposed.Timeout <= conntrackTrafficRequestedSeconds {
		t.Skipf("inconclusive fixture: one-way TCP traffic did not expose expiry above %d seconds after idle refresh; observed_seconds=%d; active refresh did not run", conntrackTrafficRequestedSeconds, exposed.Timeout)
	}
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
	var failure *ConntrackTimeoutVerificationError
	var afterFailure conntrack.Flow
	var freshErr error
	if refreshErr != nil {
		if !errors.As(refreshErr, &failure) {
			t.Errorf("active TCP refresh returned an unexpected error: %v", refreshErr)
		} else {
			afterFailure, freshErr = FreshConntrackFlow(ctx, flow)
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
	if failure != nil {
		t.Errorf("active TCP timeout rejection: connection_id=%d requested_seconds=%d observed_seconds=%d elapsed=%s allowance_seconds=%d", failure.ConnectionID, failure.RequestedSeconds, failure.ObservedSeconds, failure.Elapsed, failure.AllowanceSeconds)
		if freshErr != nil {
			t.Fatalf("fresh same-ID lookup after timeout rejection: %v", freshErr)
		}
		conntrackTrafficEstablished(t, afterFailure)
		t.Logf("fresh TCP state after rejection: id=%d state=%d timeout_seconds=%d", afterFailure.ID, afterFailure.ProtoInfo.TCP.State, afterFailure.Timeout)
		return
	}
	if refreshErr == nil {
		t.Skipf("inconclusive baseline after packet-driven expiry exposure: %d refresh calls did not reject rate-limited one-way TCP traffic", refreshes)
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
