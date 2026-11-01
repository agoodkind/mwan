package watchdog

import (
	"bytes"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
)

func TestFailoverCompletionAndRecoveryNoticesUseRealEmailBoundary(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	operations := &mockOps{
		vmRunning:       true,
		pingResults:     map[string]bool{"ping:1.1.1.1": true, "ping6:2606:4700:4700::1111": true},
		bgpStatusByVMID: map[string]*mwanv1.GetBGPStatusResponse{"113": {AllEstablished: true}},
	}
	watchdog := newTestWatchdog(t, operations, recoveryTestCfgOverrides)
	manager, err := notify.New(notify.EmailConfig{
		MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[Suburban]", MinLevel: "ERROR",
	}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-watchdog")
	if err != nil {
		t.Fatal(err)
	}
	watchdog.notify = manager
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	watchdog.nowFn = func() time.Time { return now }
	if err := watchdog.triggerBGPFailover(t.Context(), watchdog.cfg, "primary probe failed"); err != nil {
		t.Fatal(err)
	}
	server.Next(t)
	completion := server.Next(t)
	assertWatchdogNotice(t, completion, "Subject: [Suburban] Notice: BGP FAILOVER COMPLETE", "Current observation: 2026-10-31 15:00:00 UTC (UTC+00:00)", "Lxc: 203")
	now = now.Add(2 * time.Minute)
	if err := watchdog.triggerBGPRecovery(t.Context(), watchdog.cfg); err != nil {
		t.Fatal(err)
	}
	server.Next(t)
	recovery := server.Next(t)
	assertWatchdogNotice(t, recovery, "Subject: [Suburban] Recovered: BGP RECOVERED", "Recovery observed: 2026-10-31 15:02:00 UTC (UTC+00:00)", "Lxc: 203")
}

func TestSnapshotRepairsSendNoticesWithoutFailureHistory(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	operations := &mockOps{
		guestLock:      "snapshot-delete",
		delSnapshotErr: errors.New("snapshot delete: " + storageSnapshotMissingMarker),
	}
	watchdog := newTestWatchdog(t, operations)
	manager, err := notify.New(notify.EmailConfig{
		MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[Suburban]", MinLevel: "WARN",
	}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-watchdog")
	if err != nil {
		t.Fatal(err)
	}
	watchdog.notify = manager
	watchdog.nowFn = func() time.Time { return time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC) }
	watchdog.clearStaleGuestLock(t.Context(), "post-snapshot")
	assertWatchdogNotice(t, server.Next(t), "Subject: [Suburban] Notice: cleared a guest lock", "Current observation: 2026-10-31 15:00:00 UTC (UTC+00:00)", "Lock: snapshot-delete")
	if err := watchdog.deleteSnapshot(t.Context(), "known-good-20261031-150000"); err != nil {
		t.Fatal(err)
	}
	assertWatchdogNotice(t, server.Next(t), "Subject: [Suburban] Notice: removed a snapshot entry", "Current observation: 2026-10-31 15:00:00 UTC (UTC+00:00)", "Snapshot: known-good-20261031-150000")
}

func assertWatchdogNotice(t *testing.T, data []byte, subject, observation string, fields ...string) {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains("Subject: "+message.Header.Get("Subject"), subject) {
		t.Fatalf("watchdog notice subject=%q", message.Header.Get("Subject"))
	}
	text := html.UnescapeString(testsmtp.HTML(t, data))
	for _, value := range append(fields, observation, "Vmid: 113") {
		if !strings.Contains(text, value) {
			t.Fatalf("watchdog notice omitted %q:\n%s", value, text)
		}
	}
	for _, field := range []string{"First failure observed", "Latest failure observed", "Last failed observation", "Observed duration"} {
		if strings.Contains(text, field) {
			t.Fatalf("watchdog notice fabricated %q:\n%s", field, text)
		}
	}
}
