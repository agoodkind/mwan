package watchdog_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/watchdog"
)

const (
	statusVMID    = "4100"
	statusBinary  = "/usr/local/bin/mwan"
	statusVerb    = "gateway-status"
	statusPayload = `{"sent_at":"2026-10-04T12:00:00Z","active_tier":2,` +
		`"providers":{"att":"unhealthy","webpass":"healthy"}}`
	argvMismatchExit = 9
)

func installGuestCommand(t *testing.T, stdout string, exitCode int) {
	t.Helper()

	directory := t.TempDir()
	script := fmt.Sprintf(
		"#!/bin/sh\n[ \"$*\" = \"exec %s -- %s %s\" ] || exit %d\nprintf '%%s\\n' '%s'\nexit %d\n",
		statusVMID, statusBinary, statusVerb, argvMismatchExit, stdout, exitCode,
	)
	if err := os.WriteFile(filepath.Join(directory, "pct"), []byte(script), 0o755); err != nil {
		t.Fatalf("write pct: %v", err)
	}
	t.Setenv("PATH", directory)
}

func newGuestStatusSource(t *testing.T) *watchdog.GuestStatusSource {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{GuestType: config.GuestTypeLXC}
	return watchdog.NewGuestStatusSource(
		ops.NewRealOps(cfg, logger),
		statusVMID,
		[]string{statusBinary, statusVerb},
		logger,
	)
}

func TestGuestStatusSourceDecodesTheCommandOutput(t *testing.T) {
	installGuestCommand(t, statusPayload, 0)
	source := newGuestStatusSource(t)

	status, receivedAt, ok := source.Latest()

	if !ok {
		t.Fatal("Latest reported no status for a command that printed one")
	}
	if status.ActiveTier != 2 {
		t.Fatalf("active tier = %d, want 2", status.ActiveTier)
	}
	if got := status.Providers["webpass"]; got != "healthy" {
		t.Fatalf("webpass verdict = %q, want healthy", got)
	}
	wantSentAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if !status.SentAt.Equal(wantSentAt) {
		t.Fatalf("sent_at = %s, want %s", status.SentAt, wantSentAt)
	}
	if receivedAt.IsZero() {
		t.Fatal("received time is zero on a status that was read")
	}
}

func TestGuestStatusSourceReportsNothingOnFailure(t *testing.T) {
	cases := []struct {
		name     string
		stdout   string
		exitCode int
		missing  bool
	}{
		{name: "the exec fails", stdout: statusPayload, exitCode: 0, missing: true},
		{name: "the command exits non-zero", stdout: statusPayload, exitCode: 3, missing: false},
		{name: "the output is not JSON", stdout: "gateway is up", exitCode: 0, missing: false},
		{name: "the output is empty", stdout: "", exitCode: 0, missing: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installGuestCommand(t, tc.stdout, tc.exitCode)
			if tc.missing {
				t.Setenv("PATH", t.TempDir())
			}
			source := newGuestStatusSource(t)

			status, receivedAt, ok := source.Latest()

			if ok {
				t.Fatalf("Latest reported a status for a failing command: %+v", status)
			}
			if len(status.Providers) != 0 || status.ActiveTier != 0 || !status.SentAt.IsZero() {
				t.Fatalf("Latest returned a populated status with ok=false: %+v", status)
			}
			if !receivedAt.IsZero() {
				t.Fatalf("received time = %s, want zero", receivedAt)
			}
		})
	}
}
