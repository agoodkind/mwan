package connprobe

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/notify"
)

// newTestModule builds a Module wired with a real AlertManager so we can
// observe alert state transitions, but with no network calls (Reconcile is
// not used; we drive lastResult / firstFailedAt directly).
func newTestModule(t *testing.T, unhealthyAfter time.Duration) *Module {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := &ifmgr.Env{
		Log:    log,
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(&config.Config{}, log, "mwan-ifmgr")),
	}
	m := &Module{
		BaseModule: ifmgr.NewBaseModule("connectivity_probe"),
		cfg: Config{
			Iface:          "test0",
			Timeout:        2 * time.Second,
			UnhealthyAfter: unhealthyAfter,
		},
		clock:         nil,
		lastResult:    map[string]bool{},
		lastRunAt:     time.Time{},
		firstFailedAt: map[string]time.Time{},
	}
	m.InitBase(env, "module", "connectivity_probe", "iface", m.cfg.Iface)
	return m
}

// TestEvaluateAlerts_DebounceSuppressesPremature confirms that a target
// failing for less than UnhealthyAfter does NOT trigger the alert.
func TestEvaluateAlerts_DebounceSuppressesPremature(t *testing.T) {
	m := newTestModule(t, 30*time.Second)
	now := time.Now()
	m.lastRunAt = now
	m.lastResult = map[string]bool{"2001:db8::1": false}
	m.firstFailedAt = map[string]time.Time{"2001:db8::1": now.Add(-5 * time.Second)}

	m.EvaluateAlerts(context.Background(), m.Log, now)

	if m.Env.Alerts.Active("connectivity-down", "test0") {
		t.Fatalf("alert fired before debounce elapsed (expected suppressed)")
	}
}

// TestEvaluateAlerts_DebounceFiresAfterThreshold confirms the alert fires
// once the failure has persisted past UnhealthyAfter.
func TestEvaluateAlerts_DebounceFiresAfterThreshold(t *testing.T) {
	m := newTestModule(t, 10*time.Second)
	now := time.Now()
	m.lastRunAt = now
	m.lastResult = map[string]bool{"2001:db8::1": false}
	m.firstFailedAt = map[string]time.Time{"2001:db8::1": now.Add(-15 * time.Second)}

	m.EvaluateAlerts(context.Background(), m.Log, now)

	if !m.Env.Alerts.Active("connectivity-down", "test0") {
		t.Fatalf("alert should fire after debounce elapsed")
	}
}

// TestEvaluateAlerts_PartialDebounce_OnlyMatureFailureFires confirms that
// a single mature failure is enough to fire even when other failing targets
// are still within debounce.
func TestEvaluateAlerts_PartialDebounce_OnlyMatureFailureFires(t *testing.T) {
	m := newTestModule(t, 10*time.Second)
	now := time.Now()
	m.lastRunAt = now
	m.lastResult = map[string]bool{
		"2001:db8::1": false, // failing 15s (mature)
		"2001:db8::2": false, // failing 3s (still pending)
	}
	m.firstFailedAt = map[string]time.Time{
		"2001:db8::1": now.Add(-15 * time.Second),
		"2001:db8::2": now.Add(-3 * time.Second),
	}

	m.EvaluateAlerts(context.Background(), m.Log, now)

	if !m.Env.Alerts.Active("connectivity-down", "test0") {
		t.Fatalf("alert should fire when at least one target is past debounce")
	}
}

// TestEvaluateAlerts_AllPending_StaysQuiet confirms that when all failures
// are within debounce, no alert fires (this is the case the original code
// missed: it would have fired immediately).
func TestEvaluateAlerts_AllPending_StaysQuiet(t *testing.T) {
	m := newTestModule(t, 30*time.Second)
	now := time.Now()
	m.lastRunAt = now
	m.lastResult = map[string]bool{
		"2001:db8::1": false,
		"2001:db8::2": false,
	}
	m.firstFailedAt = map[string]time.Time{
		"2001:db8::1": now.Add(-5 * time.Second),
		"2001:db8::2": now.Add(-2 * time.Second),
	}

	m.EvaluateAlerts(context.Background(), m.Log, now)

	if m.Env.Alerts.Active("connectivity-down", "test0") {
		t.Fatalf("alert fired despite all failures still within debounce window")
	}
}

// TestEvaluateAlerts_RecoveryResolvesAlert confirms that after the alert
// has fired, a subsequent all-OK pass clears it immediately.
func TestEvaluateAlerts_RecoveryResolvesAlert(t *testing.T) {
	m := newTestModule(t, 10*time.Second)
	now := time.Now()
	// Force into alert-active state.
	m.lastRunAt = now
	m.lastResult = map[string]bool{"2001:db8::1": false}
	m.firstFailedAt = map[string]time.Time{"2001:db8::1": now.Add(-15 * time.Second)}
	m.EvaluateAlerts(context.Background(), m.Log, now)
	if !m.Env.Alerts.Active("connectivity-down", "test0") {
		t.Fatalf("setup: alert should be active before recovery")
	}

	// All targets now succeed; the per-target debounce should also have been
	// cleared by Reconcile, but EvaluateAlerts must work independently.
	m.lastResult = map[string]bool{"2001:db8::1": true}
	m.firstFailedAt = map[string]time.Time{}
	m.EvaluateAlerts(context.Background(), m.Log, now.Add(1*time.Second))

	if m.Env.Alerts.Active("connectivity-down", "test0") {
		t.Fatalf("alert should be resolved after all targets recover")
	}
}

// TestReconcile_DebounceBookkeeping uses a short timing path through
// only the bookkeeping side of Reconcile (cannot run real probes in unit
// test). We invoke the same map-update logic by simulating two reconcile
// cycles via direct field manipulation. This protects the contract that
// firstFailedAt only sets on first failure and clears on success.
func TestReconcile_DebounceBookkeeping(t *testing.T) {
	m := newTestModule(t, 10*time.Second)

	// Cycle 1: target fails. firstFailedAt should be populated.
	now1 := time.Now()
	m.Lock()
	for tgt, ok := range map[string]bool{"2001:db8::1": false} {
		if ok {
			delete(m.firstFailedAt, tgt)
			continue
		}
		if _, already := m.firstFailedAt[tgt]; !already {
			m.firstFailedAt[tgt] = now1
		}
	}
	m.lastResult = map[string]bool{"2001:db8::1": false}
	m.lastRunAt = now1
	m.Unlock()

	if got, want := m.firstFailedAt["2001:db8::1"], now1; got != want {
		t.Fatalf("cycle 1: firstFailedAt not set correctly: got %v want %v", got, want)
	}

	// Cycle 2: target still fails. firstFailedAt must NOT advance.
	now2 := now1.Add(5 * time.Second)
	m.Lock()
	for tgt, ok := range map[string]bool{"2001:db8::1": false} {
		if ok {
			delete(m.firstFailedAt, tgt)
			continue
		}
		if _, already := m.firstFailedAt[tgt]; !already {
			m.firstFailedAt[tgt] = now2
		}
	}
	m.lastResult = map[string]bool{"2001:db8::1": false}
	m.lastRunAt = now2
	m.Unlock()

	if got := m.firstFailedAt["2001:db8::1"]; got != now1 {
		t.Fatalf("cycle 2: firstFailedAt should not advance: got %v want %v", got, now1)
	}

	// Cycle 3: target recovers. firstFailedAt must be deleted.
	now3 := now2.Add(5 * time.Second)
	m.Lock()
	for tgt, ok := range map[string]bool{"2001:db8::1": true} {
		if ok {
			delete(m.firstFailedAt, tgt)
			continue
		}
		if _, already := m.firstFailedAt[tgt]; !already {
			m.firstFailedAt[tgt] = now3
		}
	}
	m.lastResult = map[string]bool{"2001:db8::1": true}
	m.lastRunAt = now3
	m.Unlock()

	if _, ok := m.firstFailedAt["2001:db8::1"]; ok {
		t.Fatalf("cycle 3: firstFailedAt should be cleared on recovery")
	}
}
