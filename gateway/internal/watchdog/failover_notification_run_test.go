package watchdog_test

import (
	"bytes"
	"encoding/json"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/internal/testsmtp"
)

const (
	failoverEmailStart    = "ERROR: BGP FAILOVER: withdrawing routes from VM 4300, announcing on LXC 4301"
	failoverEmailComplete = "ERROR: BGP FAILOVER COMPLETE: LXC 4301 announcing"
	failoverEmailRecovery = "ERROR: RECOVERED: BGP RECOVERED: primary VM 4300 accepted the announcement; backup remains available"
)

func TestRunSendsOneCompletionAndRecoveryEmailPerFailoverCycle(t *testing.T) {
	if os.Getenv(alertChildEnv) == "1" {
		runConnectivityAlertChild(t)
		return
	}
	server := testsmtp.Start(t)
	directory := t.TempDir()
	setConnectivityAlertPings(t, directory, 0, 0, 1)
	commands := installConnectivityAlertCommands(t)
	primary, primaryAddr := startAgent(t)
	failover, failoverAddr := startAgent(t)
	failover.mu.Lock()
	failover.pingSuccess = true
	failover.mu.Unlock()

	var output bytes.Buffer
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(),
		"PATH="+commands,
		alertChildEnv+"=1",
		alertDirectoryEnv+"="+directory,
		alertMsmtprcEnv+"="+server.Path,
		alertPrimaryAgentEnv+"="+primaryAddr,
		alertFailoverAgentEnv+"="+failoverAddr,
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

	requireSubject := func(want string) string {
		t.Helper()
		select {
		case data := <-server.Messages:
			message, err := mail.ReadMessage(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if subject := message.Header.Get("Subject"); subject != want {
				t.Fatalf("The subject is %q; expected %q.", subject, want)
			}
			return testsmtp.HTML(t, data)
		case <-time.After(alertMessageWait):
			t.Fatalf("The message with subject %q did not arrive.", want)
		}
		return ""
	}
	setConnectivityAlertPings(t, directory, 1, 1, 1)
	requireSubject(failoverEmailStart)
	requireSubject(failoverEmailComplete)
	firstCompletionAt := time.Now()
	deadline := time.Now().Add(alertMessageWait)
	for primary.count("WithdrawRoutes") < 2 {
		if !time.Now().Before(deadline) {
			t.Fatalf("primary agent received %d WithdrawRoutes calls before the deadline; want at least 2", primary.count("WithdrawRoutes"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(10 * time.Second)
	minimum := time.Since(firstCompletionAt) - 2*time.Second
	setConnectivityAlertPings(t, directory, 0, 0, 1)
	body := requireSubject(failoverEmailRecovery)
	match := regexp.MustCompile(`Time_in_failover: ([0-9hms.]+)`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("The recovery email body does not contain Time_in_failover: %s.", body)
	}
	reported, err := time.ParseDuration(match[1])
	if err != nil {
		t.Fatal(err)
	}
	if reported < minimum {
		t.Fatalf("The recovery email reports %s in failover, which is shorter than the minimum of %s. A repeated attempt replaced the first start time.", reported, minimum)
	}
	setConnectivityAlertPings(t, directory, 1, 1, 1)
	requireSubject(failoverEmailStart)
	requireSubject(failoverEmailComplete)
	setConnectivityAlertPings(t, directory, 0, 0, 1)
	requireSubject(failoverEmailRecovery)
	select {
	case data := <-server.Messages:
		message, err := mail.ReadMessage(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("An extra email arrived after the second recovery: %q", message.Header.Get("Subject"))
	case <-time.After(3 * time.Second):
	}
	requireBGPRecoveredEvents(t, filepath.Join(directory, "watchdog.json"), 2)
}

func requireBGPRecoveredEvents(t *testing.T, path string, want int) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for line := range strings.SplitSeq(string(content), "\n") {
		if !strings.Contains(line, `"bgp-recovered"`) {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if string(event["alert_kind"]) != `"bgp-recovered"` {
			continue
		}
		count++
		if string(event["component"]) != `"notify"` {
			t.Fatalf("bgp-recovered component = %s, want notify; event: %s", event["component"], line)
		}
		if keys := strings.Count(line, `"component":`); keys != 1 {
			t.Fatalf("bgp-recovered component key count = %d, want 1; event: %s", keys, line)
		}
		if string(event["alert_key"]) != `"`+alertVMID+`"` {
			t.Fatalf("The bgp-recovered event does not contain field %q. Event: %s", "alert_key", line)
		}
		for _, field := range []string{"recovery_elapsed", "original_reason", "time_in_failover", "lxc", "vmid"} {
			if _, found := event[field]; !found {
				t.Fatalf("The bgp-recovered event does not contain field %q. Event: %s", field, line)
			}
		}
	}
	if count != want {
		t.Fatalf("The watchdog JSON log contains %d bgp-recovered events; expected %d. Log: %s", count, want, content)
	}
}
