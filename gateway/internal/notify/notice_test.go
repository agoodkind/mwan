package notify_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
)

func assertNotice(t *testing.T, alert capturedAlert, subject, observation string) {
	t.Helper()
	if alert.subject != subject {
		t.Fatalf("notice subject=%q, want %q", alert.subject, subject)
	}
	assertContains(t, alert.html, observation, "Lxc: 203", "Vmid: 113")
	for _, field := range []string{"First failure observed", "Latest failure observed", "Last failed observation", "Observed duration", "first_failure", "last_failure", "incident_duration"} {
		if strings.Contains(alert.html, field) {
			t.Fatalf("notice fabricated failure history %q:\n%s", field, alert.html)
		}
	}
}

func TestStandaloneNoticesPreserveSuppressionAndRepeatTiming(t *testing.T) {
	for _, example := range []struct {
		kind, message, subject, label string
		recovery                      bool
	}{
		{"bgp-failover-complete", "The failover LXC is announcing routes.", "[Suburban] Notice: The failover LXC is announcing routes.", "Current observation", false},
		{"bgp-recovered", "The primary VM accepted its route announcement.", "[Suburban] Recovered: The primary VM accepted its route announcement.", "Recovery observed", true},
	} {
		t.Run(example.kind, func(t *testing.T) {
			t.Parallel()
			server := testsmtp.Start(t)
			manager, err := notify.New(notify.EmailConfig{
				MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[Suburban]", MinLevel: "ERROR",
			}, notify.Config{RepeatEvery: 30 * time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-watchdog")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
			event := notify.Event{
				Type: notify.Notice, Now: start, Level: slog.LevelError, Kind: example.kind, Key: "113",
				Message: example.message, IsRecovery: example.recovery,
				Fields: []slog.Attr{slog.String("lxc", "203"), slog.String("vmid", "113")},
			}
			manager.Notify(t.Context(), event)
			assertNotice(t, nextAlert(t, server), example.subject, example.label+": 2026-10-31 08:00:00 PDT (UTC-07:00)")
			if manager.Active(example.kind, "113") {
				t.Fatal("standalone notice opened an incident")
			}
			event.Now = start.Add(time.Minute)
			manager.Notify(t.Context(), event)
			assertSuppressed(t, server)
			event.Now = start.Add(30 * time.Minute)
			manager.Notify(t.Context(), event)
			assertNotice(t, nextAlert(t, server), example.subject, example.label+": 2026-10-31 08:30:00 PDT (UTC-07:00)")
			event.Now = start.Add(31 * time.Minute)
			manager.Notify(t.Context(), event)
			assertSuppressed(t, server)
			event.Kind = "below-threshold-notice"
			event.Level = slog.LevelWarn
			manager.Notify(t.Context(), event)
			assertSuppressed(t, server)
		})
	}
}

func TestNoticeResolveRetainsCompletionDeliveryWithoutFailureHistory(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	manager, err := notify.New(notify.EmailConfig{
		MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", MinLevel: "ERROR",
	}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-notify")
	if err != nil {
		t.Fatal(err)
	}
	event := notify.Event{Type: notify.Notice, Level: slog.LevelError, Kind: "self-test", Key: "test", Message: "The notification self-test started."}
	manager.Notify(t.Context(), event)
	nextAlert(t, server)
	manager.Notify(t.Context(), event)
	assertSuppressed(t, server)
	manager.Resolve(t.Context(), "self-test", "test", "The notification self-test completed.")
	completion := nextAlert(t, server)
	if completion.subject != "Recovered: The notification self-test completed." {
		t.Fatalf("completion subject=%q", completion.subject)
	}
	if strings.Contains(completion.html, "failure observed") || strings.Contains(completion.html, "Observed duration") {
		t.Fatal("notice completion fabricated failure history")
	}
}
