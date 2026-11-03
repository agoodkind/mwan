package watchdog_test

import (
	"bytes"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	alertChildEnv          = "MWAN_WATCHDOG_ALERT_TEST_CHILD"
	alertDirectoryEnv      = "MWAN_WATCHDOG_ALERT_TEST_DIRECTORY"
	alertMsmtprcEnv        = "MWAN_WATCHDOG_ALERT_TEST_MSMTPRC"
	alertPrimaryAgentEnv   = "MWAN_WATCHDOG_ALERT_TEST_PRIMARY_AGENT"
	alertFailoverAgentEnv  = "MWAN_WATCHDOG_ALERT_TEST_FAILOVER_AGENT"
	alertVMID              = "4300"
	alertTotalDelaySeconds = 4
	alertTargetV4          = "192.0.2.1"
	alertTargetV6          = "2001:db8::1"
	alertPingState         = "ping-state"
	alertPingPending       = "ping-pending"
	alertMessageWait       = 90 * time.Second
	alertCommandQM         = "qm"
	alertCommandPing       = "ping"
	alertCommandPing6      = "ping6"
	alertStateGateway      = 2
	alertTotalReason       = "The hypervisor IPv4 and IPv6 probes failed. The watchdog did not find a recent configuration change."
)

func runConnectivityAlertChild(t *testing.T) {
	t.Helper()

	directory := os.Getenv(alertDirectoryEnv)
	cfg := &watchdogconfig.Config{}
	cfg.MwanVMID = alertVMID
	cfg.Network.PingTargetIPv4 = alertTargetV4
	cfg.Network.PingTargetIPv6 = alertTargetV6
	cfg.Failover.LXCID = "4301"
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
	cfg.Watchdog.ConnectivityTimeoutSeconds = alertTotalDelaySeconds
	cfg.Watchdog.MwanAgentTCPAddr = os.Getenv(alertPrimaryAgentEnv)
	cfg.Failover.AgentTCPAddr = os.Getenv(alertFailoverAgentEnv)

	if err := watchdog.Run(cfg, watchdog.Options{}); err != nil {
		t.Fatalf("watchdog.Run failed: %v.", err)
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
	case "guest":
		status := 1
		if os.Getenv(alertFailoverAgentEnv) != "" && len(arguments) > 2 && arguments[2] != alertVMID {
			status = 0
		}
		if len(arguments) > 2 && arguments[2] == alertVMID {
			var err error
			status, err = readConnectivityAlertState(alertStateGateway)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
		}
		fmt.Printf("{\"exited\":1,\"exitcode\":%d}\n", status)
	case "listsnapshot", "snapshot", "delsnapshot":
	default:
		return 1
	}
	return 0
}

func runConnectivityAlertPing(family int) int {
	status, err := readConnectivityAlertState(family)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return status
}

func readConnectivityAlertState(index int) (int, error) {
	data, err := os.ReadFile(filepath.Join(os.Getenv(alertDirectoryEnv), alertPingState))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return 0, fmt.Errorf("The state file field list does not contain three fields: %v.", fields)
	}
	return strconv.Atoi(fields[index])
}

func installConnectivityAlertCommands(t *testing.T) string {
	t.Helper()

	commands := t.TempDir()
	for _, name := range []string{alertCommandQM, alertCommandPing, alertCommandPing6} {
		if err := os.Symlink(os.Args[0], filepath.Join(commands, name)); err != nil {
			t.Fatalf("The test could not create the command symlink %s: %v.", name, err)
		}
	}
	return commands
}

func setConnectivityAlertPings(t *testing.T, directory string, v4, v6, gateway int) {
	t.Helper()

	temporaryPath := filepath.Join(directory, alertPingPending+".next")
	if err := os.WriteFile(temporaryPath, fmt.Appendf(nil, "%d %d %d\n", v4, v6, gateway), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, alertPingPending)); err != nil {
		t.Fatal(err)
	}
}

func requireConnectivityRecovery(t *testing.T, server *testsmtp.Server, wantSubject string, targets []string, lossSetAt, recoverySetAt time.Time) {
	t.Helper()

	body := requireConnectivityAlert(t, server, wantSubject, alertVMID)
	for _, target := range targets {
		if !strings.Contains(body, target) {
			t.Fatalf("The body does not contain target %q: %s", target, body)
		}
	}
	firstFailed := requireObservedTime(t, body, "First_failed_probe_observed_at")
	recovered := requireObservedTime(t, body, "Recovery_probe_observed_at")
	if firstFailed.Before(lossSetAt.Truncate(time.Second)) || firstFailed.After(recoverySetAt) {
		t.Fatalf("The first failed probe observation at %s falls outside the window from %s to %s.", firstFailed, lossSetAt, recoverySetAt)
	}
	if recovered.Before(recoverySetAt.Truncate(time.Second)) || recovered.After(time.Now()) {
		t.Fatalf("The recovery probe observation at %s falls outside the window from %s to now.", recovered, recoverySetAt)
	}
	match := regexp.MustCompile(`Observed_elapsed: ([0-9hms]+)`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("The body does not contain Observed_elapsed: %s", body)
	}
	elapsed, err := time.ParseDuration(match[1])
	if err != nil {
		t.Fatal(err)
	}
	if difference := elapsed - recovered.Sub(firstFailed); difference < -time.Second || difference > time.Second {
		t.Fatalf("The observed elapsed time %s differs from the observation difference %s.", elapsed, recovered.Sub(firstFailed))
	}
}

func requireDelayedTotalAlert(t *testing.T, server *testsmtp.Server, lossSetAt time.Time, wantDetail []string) {
	t.Helper()

	body := requireConnectivityAlert(t, server, "ERROR: hypervisor probe total loss", alertTotalReason)
	receivedAt := time.Now()
	for _, want := range wantDetail {
		if !strings.Contains(body, want) {
			t.Fatalf("The body does not contain %q: %s", want, body)
		}
	}
	for _, unsupported := range []string{"Proxmox", "OPNsense", "external outage"} {
		if strings.Contains(body, unsupported) {
			t.Fatalf("The body contains %q, which assigns a cause: %s", unsupported, body)
		}
	}
	firstFailed := requireObservedTime(t, body, "First_failed_probe_observed_at")
	if firstFailed.Before(lossSetAt.Truncate(time.Second)) || firstFailed.After(lossSetAt.Add(alertTotalDelaySeconds*time.Second)) {
		t.Fatalf("The first failed probe observation at %s does not follow the loss time %s.", firstFailed, lossSetAt)
	}
	if delay := receivedAt.Sub(firstFailed); delay < (alertTotalDelaySeconds-1)*time.Second {
		t.Fatalf("The total alert arrived %s after the first failed probe observation at %s, sooner than expected.", delay, firstFailed)
	}
}

func requireObservedTime(t *testing.T, body, label string) time.Time {
	t.Helper()

	match := regexp.MustCompile(label + `: ([0-9TZ:-]+)`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("The body does not contain %s: %s", label, body)
	}
	observedAt, err := time.Parse(time.RFC3339, match[1])
	if err != nil {
		t.Fatal(err)
	}
	return observedAt
}

func requireConnectivityAlert(t *testing.T, server *testsmtp.Server, wantSubject, wantBody string) string {
	t.Helper()

	var data []byte
	select {
	case data = <-server.Messages:
	case <-time.After(alertMessageWait):
		t.Fatalf("The message with subject %q did not arrive.", wantSubject)
	}
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if subject := message.Header.Get("Subject"); subject != wantSubject {
		t.Fatalf("The subject is %q; expected %q.", subject, wantSubject)
	}
	body := testsmtp.HTML(t, data)
	for _, want := range []string{wantBody, "Probe_source: hypervisor"} {
		if !strings.Contains(body, want) {
			t.Fatalf("The body does not contain %q: %s", want, body)
		}
	}
	return body
}

func TestRunNotifiesConnectivityLossAndRecovery(t *testing.T) {
	if os.Getenv(alertChildEnv) == "1" {
		runConnectivityAlertChild(t)
		return
	}
	server := testsmtp.Start(t)
	directory := t.TempDir()
	setConnectivityAlertPings(t, directory, 0, 0, 1)
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
			t.Errorf("The test could not signal the watchdog: %v.", err)
		}
		if err := child.Wait(); err != nil {
			t.Errorf("The watchdog did not exit successfully: %v. Output: %s", err, output.String())
		}
	})

	targetV4 := []string{alertTargetV4}
	targetV6 := []string{alertTargetV6}
	targetsBoth := []string{alertTargetV4, alertTargetV6}
	gatewayReachable := 1
	setAt := func(v4, v6 int) time.Time {
		setTime := time.Now()
		setConnectivityAlertPings(t, directory, v4, v6, gatewayReachable)
		return setTime
	}

	v4LossAt := setAt(1, 0)
	requireConnectivityAlert(t, server, "ERROR: hypervisor probe partial loss", "IPv4")
	v6LossAt := setAt(0, 1)
	requireConnectivityRecovery(t, server, "ERROR: RECOVERED: hypervisor probe partial loss", targetV4, v4LossAt, v6LossAt)
	requireConnectivityAlert(t, server, "ERROR: hypervisor probe partial loss", "IPv6")
	v4LossAt = setAt(1, 0)
	requireConnectivityRecovery(t, server, "ERROR: RECOVERED: hypervisor probe partial loss", targetV6, v6LossAt, v4LossAt)
	requireConnectivityAlert(t, server, "ERROR: hypervisor probe partial loss", "IPv4")
	healthyAt := setAt(0, 0)
	requireConnectivityRecovery(t, server, "ERROR: RECOVERED: hypervisor probe partial loss", targetV4, v4LossAt, healthyAt)
	v6LossAt = setAt(0, 1)
	requireConnectivityAlert(t, server, "ERROR: hypervisor probe partial loss", "IPv6")
	totalLossAt := setAt(1, 1)
	requireDelayedTotalAlert(t, server, totalLossAt, []string{
		"Gateway default-route probe: failed",
		"Failover: The watchdog attempted failover, but failover did not trigger.",
	})
	v4BackAt := setAt(0, 1)
	requireConnectivityRecovery(t, server, "ERROR: RECOVERED: hypervisor probe total loss",
		append(targetsBoth, "Last_loss_reason: "+alertTotalReason, "Gateway default-route probe: failed"), totalLossAt, v4BackAt)
	gatewayReachable = 0
	totalLossAt = setAt(1, 1)
	requireDelayedTotalAlert(t, server, totalLossAt, []string{
		"Gateway default-route probe: succeeded",
		"Failover: The watchdog did not attempt failover.",
	})
	healthyAt = setAt(0, 0)
	requireConnectivityRecovery(t, server, "ERROR: RECOVERED: hypervisor probe partial loss", targetV6, v6LossAt, healthyAt)
	requireConnectivityRecovery(t, server, "ERROR: RECOVERED: hypervisor probe total loss",
		append(targetsBoth, "Last_loss_reason: "+alertTotalReason, "Gateway default-route probe: succeeded"), totalLossAt, healthyAt)
}
