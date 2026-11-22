package notify

import (
	"bytes"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/testsmtp"
)

func TestNotifyDeliversConfiguredAlert(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	const recipient = "recipient@example.com"
	manager, err := New(
		EmailConfig{
			AlertEmail:    recipient,
			SubjectPrefix: "[gateway]",
			MinLevel:      "ERROR",
			MsmtprcPath:   server.Path,
		},
		Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"mwan-ifmgr",
	)
	if err != nil {
		t.Fatal(err)
	}

	manager.Notify(
		t.Context(),
		Event{
			Level:   slog.LevelError,
			Kind:    "gateway-health",
			Key:     "wan0",
			Message: "WAN probe failed",
		},
	)
	accepted := server.Next(t)
	message, err := mail.ReadMessage(bytes.NewReader(accepted))
	if err != nil {
		t.Fatal(err)
	}

	if got := message.Header.Get("To"); got != recipient {
		t.Fatalf("To = %q, want %q", got, recipient)
	}

	const subject = "[gateway] ERROR: WAN probe failed"
	if got := message.Header.Get("Subject"); got != subject {
		t.Fatalf("Subject = %q, want %q", got, subject)
	}

	if html := testsmtp.HTML(t, accepted); !strings.Contains(html, "mwan-ifmgr") {
		t.Fatal("alert email omitted the caller name")
	}
}

func recordFromAttrs(msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Date(2026, 5, 6, 22, 33, 6, 0, time.UTC), slog.LevelError, msg, 0)
	r.AddAttrs(attrs...)

	return r
}

func TestBuildEmailBodyShape(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"Service status check failed",
		slog.String("daemon", "ifmgr"),
		slog.String("role", "wan"),
		slog.String("iface", "mbrains"),
		slog.String("trace", "4df84777"),
		slog.String("phase", "periodic-reconcile"),
		slog.String("module", "service-check"),
		slog.String(
			"err",
			"ssh agoodkind@host: exit status 255 (stderr=\"Permission denied (publickey)\")",
		),
		slog.String("level", "ERROR"),
	)
	bound := []slog.Attr{
		slog.String("build", "bcf4019"),
		slog.String("commit", "bcf4019"),
		slog.String("dirty", "dirty"),
		slog.String("binhash", "b80490fd54b5"),
	}

	got := BuildEmailBody(r, bound)
	want := "Service status check failed\n\n" +
		"What:    ssh agoodkind@host: exit status 255\n" +
		"         stderr: Permission denied (publickey)\n\n" +
		"Where:   iface=mbrains, role=wan, daemon=ifmgr, phase=periodic-reconcile\n\n" +
		"Trace:   4df84777    Build: bcf4019 (dirty) binhash=b80490fd54b5"

	if got != want {
		t.Fatalf("body mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBuildEmailBodyMissingOptionalWhere(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"route lost",
		slog.String("role", "wan"),
		slog.String("phase", "periodic-reconcile"),
		slog.String("trace", "abc123"),
	)

	got := BuildEmailBody(r, nil)
	if !strings.Contains(got, "Where:   role=wan, phase=periodic-reconcile") {
		t.Fatalf("Where line missing or malformed:\n%s", got)
	}

	if strings.Contains(got, "iface=") || strings.Contains(got, "daemon=") {
		t.Fatalf("absent keys leaked into body:\n%s", got)
	}
}

func TestBuildEmailBodyExtraKey(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"probe failed",
		slog.String("iface", "wan0"),
		slog.String("remote_addr", "10.0.0.1"),
		slog.String("trace", "deadbeef"),
		slog.String("commit", "abc1234"),
	)

	got := BuildEmailBody(r, nil)
	if !strings.Contains(got, "Remote_addr: 10.0.0.1") {
		t.Fatalf("extra key not rendered:\n%s", got)
	}

	traceIdx := strings.Index(got, "Trace:")
	extraIdx := strings.Index(got, "Remote_addr:")
	if traceIdx < 0 || extraIdx < 0 || traceIdx > extraIdx {
		t.Fatalf("section order wrong (trace=%d extra=%d):\n%s", traceIdx, extraIdx, got)
	}
}

func TestBuildEmailBodyErrWithoutStderr(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs("dial failed", slog.String("err", "context deadline exceeded"))

	got := BuildEmailBody(r, nil)
	if !strings.Contains(got, "What:    context deadline exceeded") {
		t.Fatalf("plain err not rendered cleanly:\n%s", got)
	}

	if strings.Contains(got, "stderr:") {
		t.Fatalf("synthetic stderr line emitted for err without stderr=:\n%s", got)
	}
}

func TestBuildEmailBodyDropsSubjectAndFooterDups(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"alert",
		slog.String("level", "ERROR"),
		slog.String("module", "service-check"),
		slog.String("time", "2026-05-06T22:33:06-07:00"),
		slog.String("caller", "agent.go:42"),
		slog.String("trace", "t1"),
	)

	got := BuildEmailBody(r, nil)

	for _, key := range []string{
		"level:",
		"Level:",
		"module:",
		"Module:",
		"time:",
		"Time:",
		"caller:",
		"Caller:",
	} {
		if strings.Contains(got, key) {
			t.Fatalf("dropped key %q leaked into body:\n%s", key, got)
		}
	}
}
