package ops_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ops"
)

const (
	guestExecWait  = 5 * time.Second
	guestExecAgent = 3 * time.Second
	bootIDPath     = "/proc/sys/kernel/random/boot_id"
)

func installFakeGuestExec(t *testing.T, name, stdout, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, name+".args")
	script := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\nprintf '%%s' '%s'\nprintf '%%s' '%s' >&2\nexit %d\n",
		argsFile, stdout, stderr, exitCode,
	)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestRunInGuestRunsPctExecForLXC(t *testing.T) {
	cases := []struct {
		name       string
		stdout     string
		stderr     string
		exitCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "the command exits zero",
			stdout:     "1700000000\n",
			stderr:     "",
			exitCode:   0,
			wantStdout: "1700000000\n",
			wantStderr: "",
		},
		{
			name:       "stdout and stderr stay on separate streams",
			stdout:     "out text",
			stderr:     "err text",
			exitCode:   0,
			wantStdout: "out text",
			wantStderr: "err text",
		},
		{
			name:       "a non-zero exit is a result and not an error",
			stdout:     "partial",
			stderr:     "cat: missing: No such file or directory\n",
			exitCode:   3,
			wantStdout: "partial",
			wantStderr: "cat: missing: No such file or directory\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := installFakeGuestExec(t, "pct", tc.stdout, tc.stderr, tc.exitCode)

			result, err := ops.RunInGuest(context.Background(), config.GuestTypeLXC,
				guestExecWait, guestExecAgent, 123, "cat", bootIDPath)
			if err != nil {
				t.Fatalf("RunInGuest: %v", err)
			}
			if result.ExitCode != tc.exitCode {
				t.Fatalf("exit code = %d, want %d", result.ExitCode, tc.exitCode)
			}
			if result.Stdout != tc.wantStdout {
				t.Fatalf("stdout = %q, want %q", result.Stdout, tc.wantStdout)
			}
			if result.Stderr != tc.wantStderr {
				t.Fatalf("stderr = %q, want %q", result.Stderr, tc.wantStderr)
			}
			wantArgs := "exec\n123\n--\ncat\n" + bootIDPath + "\n"
			if got := readRecordedArgs(t, argsFile); got != wantArgs {
				t.Fatalf("pct args = %q, want %q", got, wantArgs)
			}
		})
	}
}

func TestRunInGuestFailsWhenPctExecOutlivesTheWait(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "pct"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := ops.RunInGuest(context.Background(), config.GuestTypeLXC,
		200*time.Millisecond, guestExecAgent, 123, "sleep", "60")

	if err == nil || !strings.Contains(err.Error(), "did not exit within") {
		t.Fatalf("err = %v, want one containing %q", err, "did not exit within")
	}
}

func TestGuestExecRunsPctExecFirstForLXC(t *testing.T) {
	argsFile := installFakeGuestExec(t, "pct", "1700000000\n", "", 0)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan struct{}, 1)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_ = connection.Close()
		accepted <- struct{}{}
	}()
	var cfg config.Config
	cfg.GuestType = config.GuestTypeLXC
	cfg.Watchdog.MwanAgentTCPAddr = listener.Addr().String()
	realOps := ops.NewRealOps(&cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := realOps.GuestExec(context.Background(), testVMID, "cat", bootIDPath)
	if err != nil {
		t.Fatalf("GuestExec: %v", err)
	}

	if result.Stdout != "1700000000\n" {
		t.Fatalf("stdout = %q, want the pct exec output", result.Stdout)
	}
	wantArgs := "exec\n123\n--\ncat\n" + bootIDPath + "\n"
	if got := readRecordedArgs(t, argsFile); got != wantArgs {
		t.Fatalf("pct args = %q, want %q", got, wantArgs)
	}
	_ = listener.Close()
	<-acceptDone
	select {
	case <-accepted:
		t.Fatal("GuestExec dialed the management TCP address for an LXC guest")
	default:
	}
	summary := realOps.ExtractTracker().Summary()
	for _, line := range strings.Split(strings.TrimSpace(summary), "\n") {
		fields := strings.Fields(line)
		wantUsed := fields[0] == string(ops.ChanPVE)
		gotUsed := !strings.HasPrefix(fields[1], "NEVER_USED")
		if gotUsed != wantUsed {
			t.Fatalf("channel %s used = %t, want %t in %q", fields[0], gotUsed, wantUsed, summary)
		}
	}
}

func TestRunInGuestRunsQemuGuestExecForQEMU(t *testing.T) {
	qmOutput := `{"exitcode":2,"exited":1,"out-data":"partial","err-data":"boom\n","out-truncated":0}`
	argsFile := installFakeGuestExec(t, "qm", qmOutput, "", 0)

	result, err := ops.RunInGuest(context.Background(), config.GuestTypeQEMU,
		guestExecWait, guestExecAgent, 123, "cat", bootIDPath)
	if err != nil {
		t.Fatalf("RunInGuest: %v", err)
	}
	if result.ExitCode != 2 || result.Stdout != "partial" || result.Stderr != "boom\n" {
		t.Fatalf("result = %+v, want exit 2, stdout partial, stderr boom", result)
	}
	wantArgs := "guest\nexec\n123\n--timeout\n3\n--\ncat\n" + bootIDPath + "\n"
	if got := readRecordedArgs(t, argsFile); got != wantArgs {
		t.Fatalf("qm args = %q, want %q", got, wantArgs)
	}
}
