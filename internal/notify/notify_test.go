package notify

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// recordFromAttrs builds a slog.Record at LevelError with the given
// message and attrs.
func recordFromAttrs(msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Date(2026, 5, 6, 22, 33, 6, 0, time.UTC), slog.LevelError, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

// TestBuildEmailBodyShape covers the canonical case from the plan: a
// wg failure with the full attr set. The expected layout uses
// the goal shape verbatim because formatting is the contract here.
func TestBuildEmailBodyShape(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"wg: remote wg show failed",
		slog.String("daemon", "ifmgr"),
		slog.String("role", "oob"),
		slog.String("iface", "mbrains"),
		slog.String("trace", "4df84777"),
		slog.String("phase", "periodic-reconcile"),
		slog.String("module", "wg"),
		slog.String("err", "ssh agoodkind@host: exit status 255 (stderr=\"Permission denied (publickey)\")"),
		slog.String("level", "ERROR"),
	)
	bound := []slog.Attr{
		slog.String("build", "bcf4019"),
		slog.String("commit", "bcf4019"),
		slog.String("dirty", "dirty"),
		slog.String("binhash", "b80490fd54b5"),
	}

	got := BuildEmailBody(r, bound)
	want := "wg: remote wg show failed\n\n" +
		"What:    ssh agoodkind@host: exit status 255\n" +
		"         stderr: Permission denied (publickey)\n\n" +
		"Where:   iface=mbrains, role=oob, daemon=ifmgr, phase=periodic-reconcile\n\n" +
		"Trace:   4df84777    Build: bcf4019 (dirty) binhash=b80490fd54b5"

	if got != want {
		t.Fatalf("body mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestBuildEmailBodyMissingOptionalWhere drops iface/daemon and confirms
// the remaining where keys are still emitted in canonical order.
func TestBuildEmailBodyMissingOptionalWhere(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"failover: route lost",
		slog.String("role", "primary"),
		slog.String("phase", "switchover"),
		slog.String("trace", "abc123"),
	)

	got := BuildEmailBody(r, nil)
	if !strings.Contains(got, "Where:   role=primary, phase=switchover") {
		t.Fatalf("Where line missing or malformed:\n%s", got)
	}
	if strings.Contains(got, "iface=") || strings.Contains(got, "daemon=") {
		t.Fatalf("absent keys leaked into body:\n%s", got)
	}
}

// TestBuildEmailBodyExtraKey verifies that an attr outside the
// special-cased set lands as a sorted Key: value line below Where.
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

// TestBuildEmailBodyErrWithoutStderr exercises the err branch where
// there is no stderr=... fragment; the err prints verbatim under
// What:.
func TestBuildEmailBodyErrWithoutStderr(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"dial failed",
		slog.String("err", "context deadline exceeded"),
	)

	got := BuildEmailBody(r, nil)
	if !strings.Contains(got, "What:    context deadline exceeded") {
		t.Fatalf("plain err not rendered cleanly:\n%s", got)
	}
	if strings.Contains(got, "stderr:") {
		t.Fatalf("synthetic stderr line emitted for err without stderr=:\n%s", got)
	}
}

// TestBuildEmailBodyDropsSubjectAndFooterDups confirms level, module,
// time, and caller never appear in the body so the email subject and
// host-snapshot footer stay the only place they show up.
func TestBuildEmailBodyDropsSubjectAndFooterDups(t *testing.T) {
	t.Parallel()
	r := recordFromAttrs(
		"alert",
		slog.String("level", "ERROR"),
		slog.String("module", "wg"),
		slog.String("time", "2026-05-06T22:33:06-07:00"),
		slog.String("caller", "agent.go:42"),
		slog.String("trace", "t1"),
	)

	got := BuildEmailBody(r, nil)
	for _, key := range []string{"level:", "Level:", "module:", "Module:", "time:", "Time:", "caller:", "Caller:"} {
		if strings.Contains(got, key) {
			t.Fatalf("dropped key %q leaked into body:\n%s", key, got)
		}
	}
}
