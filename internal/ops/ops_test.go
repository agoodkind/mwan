package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/tracing"
)

// newRealOpsWithGuestNetworkDown returns a RealOps whose vsock and TCP dials
// both fail, so every GuestExec reaches the qm fallback, which is the path
// these tests exercise.
func newRealOpsWithGuestNetworkDown(logger *slog.Logger) *RealOps {
	realOps := &RealOps{
		log:     logger,
		tcpAddr: "127.0.0.1:1",
		tracker: NewChannelTracker(),
	}
	realOps.testGrpcDialer = func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("vsock down")
	}
	realOps.testTCPDialer = func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("tcp down")
	}
	return realOps
}

// installFakeQm puts a `qm` script first on PATH. The script records its
// arguments one per line in the returned file, prints stdout, and exits with
// exitCode, which stands in for the Proxmox command on the hypervisor.
func installFakeQm(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "qm.args")
	stdoutFile := filepath.Join(dir, "qm.stdout")
	if err := os.WriteFile(stdoutFile, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\ncat '%s'\nexit %d\n",
		argsFile, stdoutFile, exitCode,
	)
	if err := os.WriteFile(filepath.Join(dir, "qm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestGuestExecLogsFallbackAttemptsWithTraceID(t *testing.T) {
	installFakeQm(t, "QEMU guest agent is not running", 255)

	var builder strings.Builder
	logger := slog.New(slog.NewTextHandler(&builder, nil))
	realOps := newRealOpsWithGuestNetworkDown(logger)

	ctx := tracing.WithTraceID(context.Background(), "trace-ops")
	_, err := realOps.GuestExec(ctx, "123", "ping", "1.1.1.1")
	if err == nil {
		t.Fatal("GuestExec succeeded with every channel down")
	}

	output := builder.String()
	for _, want := range []string{
		"trace_id=trace-ops",
		"channel=vsock",
		"channel=tcp_mgmt",
		"channel=pve_rest",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
}

// qmChannelStatus returns the tracker's summary line for the qm channel.
func qmChannelStatus(t *testing.T, tracker *ChannelTracker) string {
	t.Helper()
	for line := range strings.SplitSeq(tracker.Summary(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), string(ChanPVE)) {
			return line
		}
	}
	t.Fatalf("no %s line in %q", ChanPVE, tracker.Summary())
	return ""
}

// TestGuestExecFallsBackToQmGuestExec drives GuestExec with vsock and the
// management network down, so the answer comes from `qm guest exec`, and
// checks both what the caller gets back and what the channel tracker records.
func TestGuestExecFallsBackToQmGuestExec(t *testing.T) {
	cases := []struct {
		name       string
		qmStdout   string
		qmExit     int
		wantResult GuestExecResult
		wantErr    string
	}{
		{
			name:       "the command exits zero",
			qmStdout:   `{"exitcode":0,"exited":1,"out-data":"1700000000\n"}`,
			qmExit:     0,
			wantResult: GuestExecResult{ExitCode: 0, Stdout: "1700000000\n"},
			wantErr:    "",
		},
		{
			name:       "exited printed as a JSON boolean",
			qmStdout:   `{"exitcode":0,"exited":true,"out-data":"1700000000"}`,
			qmExit:     0,
			wantResult: GuestExecResult{ExitCode: 0, Stdout: "1700000000"},
			wantErr:    "",
		},
		{
			name:       "the command exits non-zero",
			qmStdout:   `{"exitcode":1,"exited":1}`,
			qmExit:     0,
			wantResult: GuestExecResult{ExitCode: 1, Stdout: ""},
			wantErr:    "",
		},
		{
			name:       "the guest agent is down",
			qmStdout:   "QEMU guest agent is not running",
			qmExit:     255,
			wantResult: GuestExecResult{ExitCode: 1, Stdout: ""},
			wantErr:    "QEMU guest agent is not running",
		},
		{
			name:       "the command outlives the agent timeout",
			qmStdout:   `{"exited":0,"pid":4242}`,
			qmExit:     0,
			wantResult: GuestExecResult{ExitCode: 1, Stdout: ""},
			wantErr:    "did not exit",
		},
		{
			name:       "the command exits without an exit code",
			qmStdout:   `{"exited":1,"signal":9}`,
			qmExit:     0,
			wantResult: GuestExecResult{ExitCode: 1, Stdout: ""},
			wantErr:    "without an exit code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := installFakeQm(t, tc.qmStdout, tc.qmExit)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			realOps := newRealOpsWithGuestNetworkDown(logger)

			result, err := realOps.GuestExec(
				context.Background(), "123", "cat", "/var/lib/mwan/last-deploy",
			)

			if result != tc.wantResult {
				t.Fatalf("result = %+v, want %+v", result, tc.wantResult)
			}
			status := qmChannelStatus(t, realOps.ExtractTracker())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if !strings.Contains(status, "OK") {
					t.Fatalf("qm channel = %q, want OK", status)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				if !strings.Contains(status, "FAIL") {
					t.Fatalf("qm channel = %q, want FAIL", status)
				}
			}

			recorded, readErr := os.ReadFile(argsFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantArgs := "guest\nexec\n123\n--timeout\n30\n--\ncat\n/var/lib/mwan/last-deploy\n"
			if string(recorded) != wantArgs {
				t.Fatalf("qm args = %q, want %q", recorded, wantArgs)
			}
		})
	}
}

// TestLockHoldingTimeoutsOutlastProxmox guards the invariant that a qm
// operation holding a Proxmox configuration lock is waited on for longer
// than Proxmox's own failure path, so the ordinary slow case still returns
// a normal command error rather than a silent wait.
func TestLockHoldingTimeoutsOutlastProxmox(t *testing.T) {
	t.Parallel()

	if TimeoutQmLockHolding < minLockHoldingTimeout {
		t.Errorf(
			"lock-holding budget is %s, which is below the %s floor",
			TimeoutQmLockHolding, minLockHoldingTimeout,
		)
	}
}
