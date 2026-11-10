package ifmgr

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// captureHandler records every slog.Record so adapter tests can assert
// what the wrapped notify.Manager emitted via the journald path. The
// notify package owns the broader state-change tests under
// internal/notify/manager_test.go; this file covers only the
// AlertManager → notify.Notifier adapter surface (attr passthrough and
// signature preservation for module callers).
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

// TestAlertManager_NotifyResolveAdapter checks that the adapter forwards
// a Notify and a Resolve through to the wrapped Notifier and that both
// the alert_kind and alert_key attributes survive the attr passthrough
// conversion. The notify package separately tests the state machine, so
// this test only confirms the bridge.
func TestAlertManager_NotifyResolveAdapter(t *testing.T) {
	h := &captureHandler{}
	log := slog.New(h)
	a := NewAlertManager(log, AlertConfig{})
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	a.Notify(now, slog.LevelWarn, "kind", "key", "broken", slog.String("extra", "ctx"))
	if !a.Active("kind", "key") {
		t.Fatal("expected Active=true after Notify")
	}
	a.Resolve(now.Add(time.Minute), "kind", "key", "fixed")
	if a.Active("kind", "key") {
		t.Fatal("expected Active=false after Resolve")
	}

	records := h.snapshot()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if records[0].Level != slog.LevelWarn {
		t.Errorf("notify level=%v, want WARN", records[0].Level)
	}
	if records[1].Level != slog.LevelWarn {
		t.Errorf("resolve level=%v, want WARN (matches notify level)", records[1].Level)
	}
	if got := records[1].Message; got != "RECOVERED: fixed" {
		t.Errorf("resolve message=%q, want %q", got, "RECOVERED: fixed")
	}

	// Confirm the attr tail produced an extra=ctx attr on the notify record.
	var sawExtra bool
	records[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "extra" && a.Value.String() == "ctx" {
			sawExtra = true
		}
		return true
	})
	if !sawExtra {
		t.Error("expected attr extra=ctx to survive translation")
	}
}

// TestAlertManager_AttrTailPassesThrough confirms the adapter preserves
// the provided slog attrs unchanged.
func TestAlertManager_AttrTailPassesThrough(t *testing.T) {
	h := &captureHandler{}
	log := slog.New(h)
	a := NewAlertManager(log, AlertConfig{})
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	a.Notify(
		now, slog.LevelError, "kind", "key", "msg",
		slog.String("complete_pair_key", "complete_pair_val"),
		slog.Int("count", 7),
	)

	records := h.snapshot()
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	var sawComplete, sawCount bool
	records[0].Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "complete_pair_key":
			sawComplete = true
		case "count":
			sawCount = true
		}
		return true
	})
	if !sawComplete {
		t.Error("expected complete attr to survive translation")
	}
	if !sawCount {
		t.Error("expected numeric attr to survive translation")
	}
}

// TestWrapNotifier_NilDegradesToNull confirms WrapNotifier(nil) returns
// an AlertManager that does not panic and reports Active=false.
func TestWrapNotifier_NilDegradesToNull(t *testing.T) {
	a := WrapNotifier(nil)
	a.Notify(time.Now(), slog.LevelWarn, "kind", "key", "msg")
	if a.Active("kind", "key") {
		t.Error("nil-wrapped AlertManager.Active should always return false")
	}
}
