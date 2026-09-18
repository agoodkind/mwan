package watchdog

import (
	"context"
	"strings"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/alert"
	"goodkind.io/mwan/internal/config"
)

// recoveryTestCfgOverrides applies the BGP failover and email plumbing required
// for triggerBGPFailover and triggerBGPRecovery to run end-to-end against the
// mock ops surface.
func recoveryTestCfgOverrides(cfg *config.Config) {
	cfg.BGP = config.BGPSection{Enabled: true}
	cfg.Failover = config.FailoverSection{LXCID: "203"}
	cfg.Email = config.EmailConfig{
		AlertEmail:    "test@test.com",
		SubjectPrefix: "MWAN",
	}
}

// notifyMessagesContaining returns the messages of every notify event
// whose Message field includes any of the given substrings, preserving
// order. Used by recovery tests that need to assert on what the
// watchdog emitted through the notifier boundary.
func notifyMessagesContaining(events []notifyEvent, needles ...string) []string {
	var out []string
	for _, e := range events {
		for _, n := range needles {
			if strings.Contains(e.Message, n) {
				out = append(out, e.Message)
				break
			}
		}
	}
	return out
}

// TestRecoveryEmail_AfterFailover walks failover then a healthy probe cycle
// and asserts that FAILOVER, FAILOVER COMPLETE, and BGP RECOVERED emails are
// all recorded and that failoverActive is cleared after recovery.
func TestRecoveryEmail_AfterFailover(t *testing.T) {
	m := &mockOps{
		vmRunning: true,
		// Healthy host pings let triggerBGPRecovery believe the primary is back.
		pingResults: map[string]bool{
			"ping:1.1.1.1":               true,
			"ping6:2606:4700:4700::1111": true,
		},
		// Primary VM "113" reports BGP fully established; LXC "203" empty default.
		bgpStatusByVMID: map[string]*mwanv1.GetBGPStatusResponse{
			"113": {AllEstablished: true},
		},
	}
	w := newTestWatchdog(t, m, recoveryTestCfgOverrides)
	// Provide a fixed clock so elapsed durations are deterministic in body.
	now := time.Unix(1_700_000_000, 0)
	w.nowFn = func() time.Time { return now }
	// Ensure the rollback coord exists; newTestWatchdog already sets it.
	if w.coord == nil {
		w.coord = &alert.Coord{}
	}

	ctx := context.Background()
	if err := w.triggerBGPFailover(ctx, w.cfg, "test failover"); err != nil {
		t.Fatalf("triggerBGPFailover: %v", err)
	}

	w.failoverMu.Lock()
	if !w.failoverActive {
		w.failoverMu.Unlock()
		t.Fatal("expected failoverActive=true after triggerBGPFailover")
	}
	w.failoverMu.Unlock()

	// Advance the clock so the recovery body shows non-zero elapsed.
	w.nowFn = func() time.Time { return now.Add(2 * time.Minute) }

	w.maybeTriggerRecovery(ctx, w.cfg)

	w.failoverMu.Lock()
	stillActive := w.failoverActive
	w.failoverMu.Unlock()
	if stillActive {
		t.Fatal("expected failoverActive=false after successful recovery")
	}

	fn := fakeNotifierFrom(t, w)
	events := fn.snapshot()
	messages := notifyMessagesContaining(events,
		"BGP FAILOVER", "BGP FAILOVER COMPLETE", "BGP RECOVERED")
	if len(messages) < 3 {
		t.Fatalf("expected 3 notify events (FAILOVER, FAILOVER COMPLETE, RECOVERED); got %d: %v",
			len(messages), messages)
	}
	// The last event the watchdog emits during recovery is the
	// dedicated bgp-recovered Notify; the resolveAt of the active
	// failover precedes it.
	last := events[len(events)-1]
	if !strings.Contains(last.Message, "BGP RECOVERED") {
		t.Fatalf("expected last event to be BGP RECOVERED, got %q", last.Message)
	}
	if last.Kind != "bgp-recovered" {
		t.Fatalf("expected last event kind=bgp-recovered, got %q", last.Kind)
	}
	// Verify the active failover state cleared so a future failover
	// reads as a fresh transition.
	if fn.Active("bgp-failover", w.cfg.MwanVMID) {
		t.Fatal("expected bgp-failover key to be inactive after recovery")
	}

	// Recovery must have moved routes off the LXC and back to the primary VM.
	if len(m.withdrawRoutesCalls) < 2 || m.withdrawRoutesCalls[1] != "203" {
		t.Fatalf("expected withdraw on LXC 203 during recovery; got %v",
			m.withdrawRoutesCalls)
	}
	if len(m.announceRoutesCalls) < 2 || m.announceRoutesCalls[1] != "113" {
		t.Fatalf("expected announce on primary VM 113 during recovery; got %v",
			m.announceRoutesCalls)
	}
}

// TestRecoveryEmail_NoEmailWhenNotInFailover asserts that maybeTriggerRecovery
// is a no-op in steady-state healthy when failoverActive is false.
func TestRecoveryEmail_NoEmailWhenNotInFailover(t *testing.T) {
	m := &mockOps{
		vmRunning: true,
		pingResults: map[string]bool{
			"ping:1.1.1.1":               true,
			"ping6:2606:4700:4700::1111": true,
		},
	}
	w := newTestWatchdog(t, m, recoveryTestCfgOverrides)

	ctx := context.Background()
	w.maybeTriggerRecovery(ctx, w.cfg)

	fn := fakeNotifierFrom(t, w)
	events := fn.snapshot()
	if len(events) != 0 {
		t.Fatalf("expected zero notify events when not in failover; got %d: %+v",
			len(events), events)
	}
	if len(m.announceRoutesCalls) != 0 || len(m.withdrawRoutesCalls) != 0 {
		t.Fatalf("expected zero route ops when not in failover; got announce=%v withdraw=%v",
			m.announceRoutesCalls, m.withdrawRoutesCalls)
	}
}

// TestRecoveryEmail_PrimaryStillUnhealthy asserts that recovery is deferred
// (no RECOVERED email, failoverActive stays true) when the primary cannot be
// reached even though the watchdog is in failover and the trigger fires.
func TestRecoveryEmail_PrimaryStillUnhealthy(t *testing.T) {
	m := &mockOps{
		vmRunning: true,
		// All pings fail: simulates primary VM still down from watchdog's vantage.
		pingResults: map[string]bool{
			"ping:1.1.1.1":               false,
			"ping6:2606:4700:4700::1111": false,
		},
		bgpStatusByVMID: map[string]*mwanv1.GetBGPStatusResponse{
			"113": {AllEstablished: true},
		},
	}
	w := newTestWatchdog(t, m, recoveryTestCfgOverrides)

	ctx := context.Background()
	if err := w.triggerBGPFailover(ctx, w.cfg, "primary unhealthy test"); err != nil {
		t.Fatalf("triggerBGPFailover: %v", err)
	}

	fn := fakeNotifierFrom(t, w)
	preEvents := len(fn.snapshot())
	w.maybeTriggerRecovery(ctx, w.cfg)

	w.failoverMu.Lock()
	stillActive := w.failoverActive
	w.failoverMu.Unlock()
	if !stillActive {
		t.Fatal("expected failoverActive to stay true while primary unreachable")
	}

	post := fn.snapshot()
	for _, e := range post[preEvents:] {
		if strings.Contains(e.Message, "BGP RECOVERED") {
			t.Fatalf("did not expect a BGP RECOVERED notify when primary unreachable; got %q",
				e.Message)
		}
	}
}
