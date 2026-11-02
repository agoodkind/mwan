package watchdog_test

import (
	"bytes"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/mwan/internal/watchdog"
	"goodkind.io/mwan/internal/watchdogconfig"
)

const (
	alertChildEnv     = "MWAN_WATCHDOG_ALERT_TEST_CHILD"
	alertDirectoryEnv = "MWAN_WATCHDOG_ALERT_TEST_DIRECTORY"
	alertMsmtprcEnv   = "MWAN_WATCHDOG_ALERT_TEST_MSMTPRC"
	alertVMID         = "4300"
	alertPingState    = "ping-state"
	alertPingPending  = "ping-pending"
	alertMessageWait  = 90 * time.Second
	alertCommandQM    = "qm"
	alertCommandPing  = "ping"
	alertCommandPing6 = "ping6"
)

func runConnectivityAlertChild(t *testing.T) {
	t.Helper()

	directory := os.Getenv(alertDirectoryEnv)
	cfg := &watchdogconfig.Config{}
	cfg.MwanVMID = alertVMID
	cfg.Network.PingTargetIPv4 = "192.0.2.1"
	cfg.Network.PingTargetIPv6 = "2001:db8::1"
	cfg.Email = notify.EmailConfig{
		MsmtprcPath: os.Getenv(alertMsmtprcEnv),
		AlertEmail:  "recipient@example.com",
		MinLevel:    "ERROR",
	}
	cfg.Watchdog.LogFile = filepath.Join(directory, "watchdog.log")
	cfg.Watchdog.JSONLogFile = filepath.Join(directory, "watchdog.json")
	cfg.Watchdog.RollbackStateFile = filepath.Join(directory, "rollback.state")
	cfg.Watchdog.RollbackLockFile = filepath.Join(directory, "rollback.lock")
	cfg.Watchdog.CheckIntervalHealthy = 1
	cfg.Watchdog.CheckIntervalDegraded = 1
	cfg.Watchdog.AlertCooldownSeconds = 3600

	if err := watchdog.Run(cfg, watchdog.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case alertCommandQM:
		os.Exit(runConnectivityAlertQM(os.Args[1:]))
	case alertCommandPing:
		os.Exit(runConnectivityAlertPing(0))
	case alertCommandPing6:
		os.Exit(runConnectivityAlertPing(1))
	}
	os.Exit(m.Run())
}

func runConnectivityAlertQM(arguments []string) int {
	if len(arguments) == 0 {
		return 1
	}
	switch arguments[0] {
	case "status":
		directory := os.Getenv(alertDirectoryEnv)
		data, err := os.ReadFile(filepath.Join(directory, alertPingPending))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if err := os.WriteFile(filepath.Join(directory, alertPingState), data, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Println("status: running")
	case "agent":
		fmt.Println("thawed")
	case "listsnapshot", "snapshot", "delsnapshot":
	default:
		return 1
	}
	return 0
}

func runConnectivityAlertPing(family int) int {
	data, err := os.ReadFile(filepath.Join(os.Getenv(alertDirectoryEnv), alertPingState))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		fmt.Fprintln(os.Stderr, fields)
		return 2
	}
	status, err := strconv.Atoi(fields[family])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return status
}

func installConnectivityAlertCommands(t *testing.T) string {
	t.Helper()

	commands := t.TempDir()
	for _, name := range []string{alertCommandQM, alertCommandPing, alertCommandPing6} {
		if err := os.Symlink(os.Args[0], filepath.Join(commands, name)); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
	return commands
}

func setConnectivityAlertPings(t *testing.T, directory string, v4, v6 int) {
	t.Helper()

	temporaryPath := filepath.Join(directory, alertPingPending+".next")
	if err := os.WriteFile(temporaryPath, fmt.Appendf(nil, "%d %d\n", v4, v6), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, alertPingPending)); err != nil {
		t.Fatal(err)
	}
}

func requireConnectivityAlert(t *testing.T, server *testsmtp.Server, wantSubject, wantBody string) {
	t.Helper()

	var data []byte
	select {
	case data = <-server.Messages:
	case <-time.After(alertMessageWait):
		t.Fatalf("message with subject %q absent", wantSubject)
	}
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if subject := message.Header.Get("Subject"); subject != wantSubject {
		t.Fatalf("subject = %q, want %q", subject, wantSubject)
	}
	if body := testsmtp.HTML(t, data); !strings.Contains(body, wantBody) {
		t.Fatalf("body missing %q: %s", wantBody, body)
	}
}

func TestRunNotifiesConnectivityLossAndRecovery(t *testing.T) {
	if os.Getenv(alertChildEnv) == "1" {
		runConnectivityAlertChild(t)
		return
	}
	server := testsmtp.Start(t)
	directory := t.TempDir()
	setConnectivityAlertPings(t, directory, 0, 0)
	commands := installConnectivityAlertCommands(t)

	var output bytes.Buffer
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(),
		"PATH="+commands,
		alertChildEnv+"=1",
		alertDirectoryEnv+"="+directory,
		alertMsmtprcEnv+"="+server.Path,
	)
	child.Stdout = &output
	child.Stderr = &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := child.Process.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("signal watchdog: %v", err)
		}
		if err := child.Wait(); err != nil {
			t.Errorf("watchdog exit: %v: %s", err, output.String())
		}
	})

	setConnectivityAlertPings(t, directory, 1, 0)
	requireConnectivityAlert(t, server, "ERROR: connectivity partial loss", "IPv4")
	setConnectivityAlertPings(t, directory, 0, 1)
	requireConnectivityAlert(t, server, "ERROR: RECOVERED: connectivity partial loss", "IPv4")
	requireConnectivityAlert(t, server, "ERROR: connectivity partial loss", "IPv6")
	setConnectivityAlertPings(t, directory, 1, 0)
	requireConnectivityAlert(t, server, "ERROR: RECOVERED: connectivity partial loss", "IPv6")
	requireConnectivityAlert(t, server, "ERROR: connectivity partial loss", "IPv4")
	setConnectivityAlertPings(t, directory, 0, 0)
	requireConnectivityAlert(t, server, "ERROR: RECOVERED: connectivity partial loss", "IPv4")
	setConnectivityAlertPings(t, directory, 0, 1)
	requireConnectivityAlert(t, server, "ERROR: connectivity partial loss", "IPv6")
	setConnectivityAlertPings(t, directory, 1, 1)
	requireConnectivityAlert(t, server, "ERROR: connectivity total loss", "Total connectivity loss, no recent config change")
	setConnectivityAlertPings(t, directory, 0, 1)
	requireConnectivityAlert(t, server, "ERROR: RECOVERED: connectivity total loss", alertVMID)
	setConnectivityAlertPings(t, directory, 1, 1)
	requireConnectivityAlert(t, server, "ERROR: connectivity total loss", "Total connectivity loss, no recent config change")
	setConnectivityAlertPings(t, directory, 0, 0)
	requireConnectivityAlert(t, server, "ERROR: RECOVERED: connectivity partial loss", "IPv6")
	requireConnectivityAlert(t, server, "ERROR: RECOVERED: connectivity total loss", alertVMID)
}
