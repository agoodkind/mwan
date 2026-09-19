package notify

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// captureHandler is a minimal slog.Handler that records every Record it
// receives so tests can assert both the level/message of each emit and
// the total number of emits across a window. The Manager logs through
// this handler when its Sink is nil, which lets tests drive the
// state-change semantics without wiring up an email Sender.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

func (h *captureHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func newManagerForTest() (*Manager, *captureHandler) {
	h := &captureHandler{}
	log := slog.New(h)
	return mustManager(log, Config{RepeatEvery: 0, PerKind: nil}, nil), h
}

// mustManager is the test-only helper that constructs a Manager and
// fails the test setup if the precondition checks fire. Real callers
// use the New constructor and surface the error through their startup
// error path.
func mustManager(log *slog.Logger, cfg Config, sink Sink) *Manager {
	m, err := newManager(log, cfg, sink)
	if err != nil {
		panic(err)
	}
	return m
}

func TestResolveEmitsAtNotifyLevelWarn(t *testing.T) {
	m, h := newManagerForTest()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	m.notifyAt(context.Background(), now, slog.LevelWarn, "wg", "peer-EYvlZyou", "stalled")
	m.resolveAt(context.Background(), now.Add(time.Minute), "wg", "peer-EYvlZyou", "back to good")

	records := h.snapshot()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	resolve := records[1]
	if resolve.Level != slog.LevelWarn {
		t.Errorf("resolve level=%v, want WARN", resolve.Level)
	}
	wantMsg := "RECOVERED: back to good"
	if resolve.Message != wantMsg {
		t.Errorf("resolve message=%q, want %q", resolve.Message, wantMsg)
	}
}

func TestResolveEmitsAtNotifyLevelError(t *testing.T) {
	m, h := newManagerForTest()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	m.notifyAt(context.Background(), now, slog.LevelError, "wg", "peer-jz3eKGui", "remote wg show failed")
	m.resolveAt(context.Background(), now.Add(time.Minute), "wg", "peer-jz3eKGui", "remote wg show ok")

	records := h.snapshot()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	resolve := records[1]
	if resolve.Level != slog.LevelError {
		t.Errorf("resolve level=%v, want ERROR", resolve.Level)
	}
	wantMsg := "RECOVERED: remote wg show ok"
	if resolve.Message != wantMsg {
		t.Errorf("resolve message=%q, want %q", resolve.Message, wantMsg)
	}
}

func TestResolveOnUnknownKeyIsNoOp(t *testing.T) {
	m, h := newManagerForTest()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	// Must not panic and must emit nothing: there is no active alert
	// for this (kind, key), so Resolve has no recovery to announce.
	m.resolveAt(context.Background(), now, "wg", "never-fired", "spurious")

	if got := len(h.snapshot()); got != 0 {
		t.Errorf("emitted %d records on unknown-key Resolve, want 0", got)
	}
}

func TestResolveDoesNotDoublePrefixRecovered(t *testing.T) {
	m, h := newManagerForTest()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	m.notifyAt(context.Background(), now, slog.LevelError, "kind", "key", "broken")
	// Caller has already added the prefix; Manager must not stack it.
	m.resolveAt(context.Background(), now.Add(time.Minute), "kind", "key", "RECOVERED: fixed itself")

	records := h.snapshot()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if got := records[1].Message; got != "RECOVERED: fixed itself" {
		t.Errorf("resolve message=%q, want %q", got, "RECOVERED: fixed itself")
	}
}

// TestManager_StateChangeOnlyByDefault drives Notify many times across
// simulated time with RepeatEvery=0 and no per-kind override. Only the
// initial transition should emit; subsequent Notify calls collapse
// into the already-active state.
func TestManager_StateChangeOnlyByDefault(t *testing.T) {
	h := &captureHandler{}
	log := slog.New(h)
	m := mustManager(log, Config{RepeatEvery: 0, PerKind: nil}, nil)

	start := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	for i := range 240 {
		now := start.Add(time.Duration(i) * time.Minute)
		m.notifyAt(context.Background(), now, slog.LevelWarn, "wg-peer-stalled", "peer1", "stalled")
	}

	if got := h.count(); got != 1 {
		t.Fatalf("expected exactly 1 emit (transition only), got %d", got)
	}
}

// TestManager_RepeatEveryGlobal drives Notify every minute for two
// hours with RepeatEvery=30m. Expect 4 emits: the transition at t=0,
// then repeats at t=30m, t=60m, t=90m. The Notify at t=120m is exactly
// at the next boundary; with our minute-stepped clock the boundary at
// t=120m is also a repeat tick, so we drive Notify for i in [0, 120) to
// land on 4 emits.
func TestManager_RepeatEveryGlobal(t *testing.T) {
	h := &captureHandler{}
	log := slog.New(h)
	m := mustManager(log, Config{RepeatEvery: 30 * time.Minute, PerKind: nil}, nil)

	start := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	for i := range 120 {
		now := start.Add(time.Duration(i) * time.Minute)
		m.notifyAt(context.Background(), now, slog.LevelWarn, "wg-peer-stalled", "peer1", "stalled")
	}

	if got := h.count(); got != 4 {
		t.Fatalf("expected 4 emits (transition + 3 repeats), got %d", got)
	}
}

// TestManager_PerKindOverride verifies that the PerKind map
// short-circuits the global RepeatEvery on a per-alert-kind basis.
// PerKind says wg-peer-stalled repeats every 24h, so during a 2h
// window only the transition emit fires. A different kind under the
// same window falls back to the 30m global cadence and emits 4 times.
func TestManager_PerKindOverride(t *testing.T) {
	h := &captureHandler{}
	log := slog.New(h)

	m := mustManager(log, Config{
		RepeatEvery: 30 * time.Minute,
		PerKind: map[string]time.Duration{
			"wg-peer-stalled": 24 * time.Hour,
		},
	}, nil)

	start := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	for i := range 120 {
		now := start.Add(time.Duration(i) * time.Minute)
		m.notifyAt(context.Background(), now, slog.LevelWarn, "wg-peer-stalled", "peer1", "stalled")
	}
	stalledCount := h.count()
	if stalledCount != 1 {
		t.Fatalf("wg-peer-stalled with 24h override: expected 1 emit, got %d", stalledCount)
	}

	for i := range 120 {
		now := start.Add(time.Duration(i) * time.Minute)
		m.notifyAt(context.Background(), now, slog.LevelWarn, "wg-reconcile-failed", "peer1", "reconcile failed")
	}
	totalCount := h.count()
	reconcileCount := totalCount - stalledCount
	if reconcileCount != 4 {
		t.Fatalf("wg-reconcile-failed using global 30m: expected 4 emits, got %d", reconcileCount)
	}
}

// TestManager_NotifyEventShape exercises the typed Event entry point so
// the locked types from the plan stay covered.
func TestManager_NotifyEventShape(t *testing.T) {
	h := &captureHandler{}
	log := slog.New(h)
	m := mustManager(log, Config{RepeatEvery: 0, PerKind: nil}, nil)

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	m.Notify(context.Background(), Event{
		Now:     now,
		Level:   slog.LevelWarn,
		Kind:    "vsock-fallback",
		Key:     "950:50051",
		Message: "vsock unavailable, used TCP fallback",
		Fields:  []slog.Attr{slog.String("vmid", "950"), slog.Int("port", 50051)},
	})
	if !m.Active("vsock-fallback", "950:50051") {
		t.Fatal("expected alert to be Active after Notify")
	}
	m.Resolve(context.Background(), "vsock-fallback", "950:50051", "vsock back online")
	if m.Active("vsock-fallback", "950:50051") {
		t.Fatal("expected alert to be cleared after Resolve")
	}
	records := h.snapshot()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
}

// TestNullNotifier verifies the no-op Notifier degrades cleanly when
// callers are constructed without an [email] section.
func TestNullNotifier(t *testing.T) {
	var n Notifier = NullNotifier{}
	n.Notify(context.Background(), Event{Kind: "k", Key: "v", Message: "m"})
	n.Resolve(context.Background(), "k", "v", "m")
	if n.Active("k", "v") {
		t.Fatal("NullNotifier.Active should always return false")
	}
}
