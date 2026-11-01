package notify_test

import (
	"bytes"
	"html"
	"io"
	"log/slog"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
)

type capturedAlert struct {
	subject string
	html    string
}

func nextAlert(t *testing.T, server *testsmtp.Server) capturedAlert {
	t.Helper()
	data := server.Next(t)
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	return capturedAlert{subject: subject, html: html.UnescapeString(testsmtp.HTML(t, data))}
}

func assertContains(t *testing.T, text string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(text, value) {
			t.Fatalf("email omitted %q:\n%s", value, text)
		}
	}
}

func assertSuppressed(t *testing.T, server *testsmtp.Server) {
	t.Helper()
	select {
	case data := <-server.Messages:
		t.Fatalf("suppressed observation delivered an email:\n%s", data)
	default:
	}
}

func TestIncidentEmailsRetainObservationsThroughRecovery(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	var logs bytes.Buffer
	manager, err := notify.New(notify.EmailConfig{
		MsmtprcPath:   server.Path,
		AlertEmail:    "recipient@example.com",
		From:          "suburban@example.com",
		SubjectPrefix: "[Suburban]",
		MinLevel:      "ERROR",
	}, notify.Config{RepeatEvery: 30 * time.Minute}, slog.New(slog.NewTextHandler(&logs, nil)), "mwan-ifmgr")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
	observe := func(now time.Time, message, detail, target string) {
		manager.Notify(t.Context(), notify.Event{
			Now: now, Level: slog.LevelError, Kind: "status-check", Key: "wg0",
			Message: message,
			Fields:  []slog.Attr{slog.String("err", detail), slog.String("target", target), slog.String("iface", "wg0")},
		})
	}
	manager.Notify(t.Context(), notify.Event{Now: start, Kind: "status-check", Key: "unknown", IsRecovery: true, Message: "The check succeeded."})
	assertSuppressed(t, server)
	observe(start, "The status check failed.", "connection refused", "operator@router")
	first := nextAlert(t, server)
	if first.subject != "[Suburban] ERROR: The status check failed." {
		t.Fatalf("failure subject=%q", first.subject)
	}
	assertContains(t, first.html,
		"First failure observed: 2026-10-31 08:00:00 PDT (UTC-07:00)",
		"Current observation: 2026-10-31 08:00:00 PDT (UTC-07:00)",
		"Observed duration: 0 seconds", "connection refused", "operator@router", "Interface: wg0")
	observe(start.Add(10*time.Minute), "The status check still failed.", "network unreachable", "operator@router")
	assertSuppressed(t, server)
	observe(start.Add(30*time.Minute), "The status check failed again.", "check timed out", "operator@router")
	repeat := nextAlert(t, server)
	assertContains(t, repeat.html,
		"First failure observed: 2026-10-31 08:00:00 PDT (UTC-07:00)",
		"Current observation: 2026-10-31 08:30:00 PDT (UTC-07:00)",
		"Observed duration: 30 minutes", "check timed out")
	observe(start.Add(31*time.Minute), "The status check could not authenticate.", "Permission denied (publickey)", "operator@latest-router")
	assertSuppressed(t, server)
	manager.Notify(t.Context(), notify.Event{
		Now: start.Add(65 * time.Minute), Kind: "status-check", Key: "wg0", IsRecovery: true,
		Message: "The status check succeeded and returned a valid peer table.",
		Fields:  []slog.Attr{slog.Int("peer_count", 3), slog.String("target", "operator@recovered-router"), slog.String("iface", "wg0")},
	})
	recovery := nextAlert(t, server)
	if recovery.subject != "[Suburban] Recovered: The status check succeeded and returned a valid peer table." {
		t.Fatalf("recovery subject=%q", recovery.subject)
	}
	assertContains(t, recovery.html,
		"The status check succeeded and returned a valid peer table.",
		"First failure observed: 2026-10-31 08:00:00 PDT (UTC-07:00)",
		"Recovery observed: 2026-10-31 09:05:00 PDT (UTC-07:00)",
		"Latest failure observed: 2026-10-31 08:31:00 PDT (UTC-07:00)",
		"Observed duration: 1 hour, 5 minutes",
		"Last failed observation:", "The status check could not authenticate.",
		"Permission denied (publickey)", "operator@latest-router", "operator@recovered-router", "Interface: wg0", "Peer count: 3")
	for _, alert := range []capturedAlert{first, repeat, recovery} {
		for _, internal := range []string{"alert_kind", "Alert kind", "alert_key", "Alert key", "resolved", "Resolved", "transition", "Transition", "RECOVERED:", "ERROR: RECOVERED"} {
			if strings.Contains(alert.html, internal) {
				t.Fatalf("email included internal state %q:\n%s", internal, alert.html)
			}
		}
	}
	if manager.Active("status-check", "wg0") {
		t.Fatal("recovery did not clear the incident")
	}
	observe(start.Add(2*time.Hour), "The next status check failed.", "second incident", "operator@second-router")
	second := nextAlert(t, server)
	assertContains(t, second.html,
		"First failure observed: 2026-10-31 10:00:00 PDT (UTC-07:00)",
		"Observed duration: 0 seconds", "second incident")
	if strings.Contains(second.html, "operator@latest-router") {
		t.Fatal("second incident retained the previous target")
	}
	assertContains(t, logs.String(), "alert_kind=status-check", "alert_key=wg0", "resolved=true", "transition=true", "transition=false")
}

func TestIncidentKeysRemainIndependent(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	manager, err := notify.New(notify.EmailConfig{
		MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", MinLevel: "ERROR",
	}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-ifmgr")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	manager.Notify(t.Context(), notify.Event{Now: start, Level: slog.LevelWarn, Kind: "below-threshold", Key: "warn", Message: "The warning check failed."})
	manager.Notify(t.Context(), notify.Event{Now: start.Add(time.Minute), Kind: "below-threshold", Key: "warn", IsRecovery: true, Message: "The warning check succeeded."})
	assertSuppressed(t, server)
	for index, pair := range []struct{ kind, key string }{{"check", "a|b"}, {"check|a", "b"}, {"check", "other"}} {
		manager.Notify(t.Context(), notify.Event{
			Now: start.Add(time.Duration(index) * time.Minute), Level: slog.LevelError,
			Kind: pair.kind, Key: pair.key, Message: "The check failed.",
			Fields: []slog.Attr{slog.String("target", pair.key)},
		})
		nextAlert(t, server)
	}
	manager.Notify(t.Context(), notify.Event{Now: start.Add(3 * time.Minute), Kind: "check", Key: "a|b", IsRecovery: true, Message: "The first check succeeded."})
	recovery := nextAlert(t, server)
	assertContains(t, recovery.html, "Target: a|b", "Observed duration: 3 minutes", "UTC (UTC+00:00)")
	if !manager.Active("check|a", "b") || !manager.Active("check", "other") {
		t.Fatal("recovering the first key cleared an independent incident")
	}
}
