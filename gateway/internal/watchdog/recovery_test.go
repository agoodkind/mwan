package watchdog

import (
	"bytes"
	"context"
	"net/mail"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/alert"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/mwan/internal/watchdogconfig"
)

// recoveryTestCfgOverrides applies the BGP failover and email plumbing required
// for triggerBGPFailover and triggerBGPRecovery to run end-to-end against the
// mock ops surface.
func recoveryTestCfgOverrides(cfg *watchdogconfig.Config) {
	cfg.Failover = watchdogconfig.FailoverSection{LXCID: "203"}
	cfg.Email = notify.EmailConfig{
		AlertEmail:    "test@test.com",
		SubjectPrefix: "MWAN",
	}
}

func useSMTPNotifier(t *testing.T, w *watchdog) *testsmtp.Server {
	t.Helper()
	server := testsmtp.Start(t)
	w.notify = notify.FromConfig(notify.EmailConfig{
		MsmtprcPath: server.Path,
		AlertEmail:  "test@test.com",
		MinLevel:    "ERROR",
	}, notify.Config{}, w.log, "mwan-watchdog")
	return server
}

func requireEmailSubject(t *testing.T, server *testsmtp.Server, want string) {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(server.Next(t)))
	if err != nil {
		t.Fatal(err)
	}
	if subject := message.Header.Get("Subject"); subject != want {
		t.Fatalf("The subject is %q; expected %q.", subject, want)
	}
}

func queuedEmailSubject(t *testing.T, server *testsmtp.Server) (string, bool) {
	t.Helper()
	select {
	case data := <-server.Messages:
		message, err := mail.ReadMessage(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return message.Header.Get("Subject"), true
	default:
		return "", false
	}
}

// TestRecoveryEmail_AfterFailover walks failover then a healthy probe cycle
// and asserts that FAILOVER, FAILOVER COMPLETE, and BGP RECOVERED emails are
// all recorded and that failoverActive is cleared after recovery.
func TestRecoveryEmail_AfterFailover(t *testing.T) {
	m := &mockOps{
		vmRunning: true,
		guestResults: map[string]ops.GuestExecResult{
			"113|ping|-c|2|-W|3|1.1.1.1":               {ExitCode: 0},
			"113|ping6|-c|2|-W|3|2606:4700:4700::1111": {ExitCode: 0},
		},
		// Failover requires established BGP sessions on LXC 203 before withdrawing routes from primary VM 113; recovery requires established sessions on VM 113.
		bgpStatusByVMID: map[string]*mwanv1.GetBGPStatusResponse{
			"113": {AllEstablished: true},
			"203": {AllEstablished: true, AnnouncingIpv4: true},
		},
	}
	w := newTestWatchdog(t, m, recoveryTestCfgOverrides)
	server := useSMTPNotifier(t, w)
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

	requireEmailSubject(t, server, "ERROR: BGP FAILOVER: withdrawing routes from VM 113, announcing on LXC 203")
	requireEmailSubject(t, server, "ERROR: BGP FAILOVER COMPLETE: LXC 203 announcing")
	requireEmailSubject(t, server, "ERROR: RECOVERED: BGP RECOVERED: primary VM 113 accepted the announcement; backup remains available")
	if subject, found := queuedEmailSubject(t, server); found {
		t.Fatalf("An extra email arrived after the recovery email: %q.", subject)
	}
	if w.notify.Active("bgp-failover", w.cfg.MwanVMID) {
		t.Fatal("The bgp-failover alert remained active after recovery.")
	}

	// Recovery leaves the backup announced until FRR selects the primary.
	if len(m.withdrawRoutesCalls) != 1 || m.withdrawRoutesCalls[0] != "113" {
		t.Fatalf("expected only primary withdrawal during failover; got %v",
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
	server := useSMTPNotifier(t, w)

	ctx := context.Background()
	w.maybeTriggerRecovery(ctx, w.cfg)

	if subject, found := queuedEmailSubject(t, server); found {
		t.Fatalf("%q: an email arrived while the watchdog was not in failover", subject)
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
		guestResults: map[string]ops.GuestExecResult{
			"113|ping|-c|2|-W|3|1.1.1.1":               {ExitCode: 1},
			"113|ping6|-c|2|-W|3|2606:4700:4700::1111": {ExitCode: 1},
		},
		bgpStatusByVMID: map[string]*mwanv1.GetBGPStatusResponse{
			"113": {AllEstablished: true},
			"203": {AllEstablished: true},
		},
	}
	w := newTestWatchdog(t, m, recoveryTestCfgOverrides)
	server := useSMTPNotifier(t, w)

	ctx := context.Background()
	if err := w.triggerBGPFailover(ctx, w.cfg, "backup announces no family"); err == nil {
		t.Fatal("triggerBGPFailover err = nil")
	}
	if len(m.withdrawRoutesCalls) != 0 {
		t.Fatalf("withdrawRoutesCalls = %v, want none", m.withdrawRoutesCalls)
	}
	m.bgpStatusByVMID["203"] = &mwanv1.GetBGPStatusResponse{AllEstablished: true, AnnouncingIpv6: true}
	if err := w.triggerBGPFailover(ctx, w.cfg, "primary unhealthy test"); err != nil {
		t.Fatalf("triggerBGPFailover: %v", err)
	}

	requireEmailSubject(t, server, "ERROR: BGP FAILOVER: withdrawing routes from VM 113, announcing on LXC 203")
	requireEmailSubject(t, server, "ERROR: BGP FAILOVER COMPLETE: LXC 203 announcing")
	w.maybeTriggerRecovery(ctx, w.cfg)

	w.failoverMu.Lock()
	stillActive := w.failoverActive
	w.failoverMu.Unlock()
	if !stillActive {
		t.Fatal("expected failoverActive to stay true while primary unreachable")
	}

	if subject, found := queuedEmailSubject(t, server); found {
		t.Fatalf("%q: a recovery email arrived while the primary remained unreachable", subject)
	}
}
