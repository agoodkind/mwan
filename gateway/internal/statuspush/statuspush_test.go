package statuspush_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net"
	"testing"
	"time"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/statuspush"
)

// discardLogger keeps test output readable; every assertion below is on
// delivered state, never on a log line.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loopbackPair starts the real Listener on a loopback socket and returns a real
// Sender pointed at it. vsock needs a hypervisor, so the transport is the one
// thing substituted; the framing, the encoding, the accept loop, and the
// latest-status bookkeeping are production code.
func loopbackPair(t *testing.T) (*statuspush.Sender, *statuspush.Listener) {
	t.Helper()

	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	address := socket.Addr().String()

	listener := statuspush.NewListenerWithListen(
		func() (net.Listener, error) { return socket, nil },
		discardLogger(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = listener.Run(ctx)
	}()

	sender := statuspush.NewSenderWithDial(
		func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", address)
		},
		discardLogger(),
	)
	return sender, listener
}

// waitForStatus polls until the listener has recorded a status whose active
// tier matches, or the deadline passes. The push is asynchronous by design, so
// the test waits for the effect rather than sleeping a fixed span.
func waitForStatus(
	t *testing.T,
	listener *statuspush.Listener,
	wantTier uint8,
) (statuspush.Status, time.Time) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, receivedAt, ok := listener.Latest()
		if ok && status.ActiveTier == wantTier {
			return status, receivedAt
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no status with active tier %d arrived within the deadline", wantTier)
	return statuspush.Status{}, time.Time{}
}

func TestListenerHoldsNothingBeforeTheFirstPush(t *testing.T) {
	t.Parallel()

	_, listener := loopbackPair(t)

	if _, _, ok := listener.Latest(); ok {
		t.Fatal("Latest reported a status before any push arrived")
	}
}

func TestRoundTripKeepsTheLatestStatus(t *testing.T) {
	t.Parallel()

	sender, listener := loopbackPair(t)
	sentAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	sender.Send(context.Background(), statuspush.Status{
		SentAt:     sentAt,
		ActiveTier: 0,
		Providers: map[string]string{
			"att": "healthy", "webpass": "healthy", "monkeybrains": "healthy",
		},
	})
	first, _ := waitForStatus(t, listener, 0)
	if got := first.Providers["att"]; got != "healthy" {
		t.Fatalf("att verdict = %q, want healthy", got)
	}
	if !first.SentAt.Equal(sentAt) {
		t.Fatalf("sent_at = %s, want %s", first.SentAt, sentAt)
	}

	sender.Send(context.Background(), statuspush.Status{
		SentAt:     sentAt.Add(10 * time.Second),
		ActiveTier: 1,
		Providers: map[string]string{
			"att": "unhealthy", "webpass": "unhealthy", "monkeybrains": "healthy",
		},
	})
	second, receivedAt := waitForStatus(t, listener, 1)
	if got := second.Providers["att"]; got != "unhealthy" {
		t.Fatalf("att verdict after failover = %q, want unhealthy", got)
	}
	if got := len(second.Providers); got != 3 {
		t.Fatalf("provider count = %d, want 3", got)
	}
	if receivedAt.IsZero() {
		t.Fatal("received time is zero on a status that arrived")
	}
}

func TestMalformedLineLeavesTheLastGoodStatus(t *testing.T) {
	t.Parallel()

	sender, listener := loopbackPair(t)
	sender.Send(context.Background(), statuspush.Status{
		SentAt:     time.Now(),
		ActiveTier: 2,
		Providers:  map[string]string{"astound": "healthy"},
	})
	good, _ := waitForStatus(t, listener, 2)

	conn, err := net.Dial("tcp", listener.Address())
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A rejected line must not clear what the watchdog knows. Give the accept
	// loop time to have processed and discarded it, then re-read.
	time.Sleep(100 * time.Millisecond)
	after, _, ok := listener.Latest()
	if !ok {
		t.Fatal("a malformed line cleared the stored status")
	}
	if after.ActiveTier != good.ActiveTier {
		t.Fatalf("active tier = %d, want the last good %d", after.ActiveTier, good.ActiveTier)
	}
}

func TestUnmarshalStatusRoundTripsAnEncodedStatus(t *testing.T) {
	t.Parallel()

	sent := statuspush.Status{
		SentAt:     time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		ActiveTier: 1,
		Providers:  map[string]string{"att": "unhealthy", "webpass": "healthy"},
	}
	line, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	got, err := statuspush.UnmarshalStatus(append(line, '\n'))
	if err != nil {
		t.Fatalf("UnmarshalStatus: %v", err)
	}

	if !got.SentAt.Equal(sent.SentAt) || got.ActiveTier != sent.ActiveTier {
		t.Fatalf("decoded = %+v, want %+v", got, sent)
	}
	if !maps.Equal(got.Providers, sent.Providers) {
		t.Fatalf("providers = %v, want %v", got.Providers, sent.Providers)
	}
}

func TestUnmarshalStatusRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "{not json", `"a string"`} {
		status, err := statuspush.UnmarshalStatus([]byte(input))
		if err == nil {
			t.Errorf("UnmarshalStatus accepted %q", input)
		}
		if status.Providers != nil || status.ActiveTier != 0 || !status.SentAt.IsZero() {
			t.Errorf("UnmarshalStatus(%q) returned a populated status: %+v", input, status)
		}
	}
}

func TestNewStatusPicksTheLowestHealthyTier(t *testing.T) {
	t.Parallel()

	members := []netif.TierMember{
		{Name: "webpass", Tier: 0},
		{Name: "att", Tier: 1},
		{Name: "monkeybrains", Tier: 2},
	}
	states := netif.HealthStates{
		"webpass":      "unhealthy",
		"att":          "unhealthy",
		"monkeybrains": "healthy",
		"unlisted":     "healthy",
	}
	sentAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	status := statuspush.NewStatus(sentAt, members, states)

	if status.ActiveTier != 2 {
		t.Fatalf("active tier = %d, want 2", status.ActiveTier)
	}
	if !status.SentAt.Equal(sentAt) {
		t.Fatalf("sent_at = %s, want %s", status.SentAt, sentAt)
	}
	want := map[string]string{
		"webpass": "unhealthy", "att": "unhealthy", "monkeybrains": "healthy",
	}
	if !maps.Equal(status.Providers, want) {
		t.Fatalf("providers = %v, want %v", status.Providers, want)
	}
}

func TestNewStatusReadsAnUnrecognizedVerdictAsUnknown(t *testing.T) {
	t.Parallel()

	members := []netif.TierMember{{Name: "webpass", Tier: 0}, {Name: "att", Tier: 1}}
	states := netif.HealthStates{"webpass": "garbled"}

	status := statuspush.NewStatus(time.Time{}, members, states)

	if got := status.Providers["webpass"]; got != "unknown" {
		t.Fatalf("webpass verdict = %q, want unknown", got)
	}
	if got := status.Providers["att"]; got != "unknown" {
		t.Fatalf("att verdict = %q, want unknown", got)
	}
	if status.ActiveTier != 0 {
		t.Fatalf("active tier = %d, want 0 because unknown reads healthy", status.ActiveTier)
	}
}

func TestSendToNothingIsNotFatal(t *testing.T) {
	t.Parallel()

	// The hypervisor may be running an older watchdog with no listener. That
	// costs one failed dial and nothing else: no panic, no block, no error the
	// probe cycle has to handle.
	sender := statuspush.NewSenderWithDial(
		func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", "127.0.0.1:1")
		},
		discardLogger(),
	)
	done := make(chan struct{})
	go func() {
		sender.Send(context.Background(), statuspush.Status{
			SentAt:     time.Now(),
			ActiveTier: 0,
			Providers:  map[string]string{"att": "healthy"},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked when the receiver was absent")
	}
}
