//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type TunnelRuntimeIPv4State struct {
	Routing   string
	Readiness string
	Carrying  bool
}

func TunnelRuntimeIPv4Providers() map[string]string {
	return map[string]string{
		TunnelRuntimeUnderlay:  TunnelRuntimeLocal,
		TunnelRuntimeAlternate: TunnelRuntimeAltLocal,
	}
}

func TunnelRuntimeClientsV4() []string {
	return []string{TunnelRuntimeClientV4, "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6"}
}

func TunnelRuntimeIPv4Selection() (string, error) {
	output, err := exec.Command(
		"nft",
		"list",
		"chain",
		"inet",
		"mwan_steer",
		"prerouting",
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read the steering chain: %w: %s", err, output)
	}
	selection := TunnelRuntimeIPv4Assignments(string(output))
	if selection == "" {
		return "", fmt.Errorf("the steering chain has no IPv4 mark assignment: %s", output)
	}
	return selection, nil
}

func TunnelRuntimeIPv4Assignments(chain string) string {
	var assignments []string
	for _, line := range strings.Split(chain, "\n") {
		_, assignment, found := strings.Cut(line, "meta mark set")
		assignment = strings.TrimSpace(assignment)
		if found && strings.Contains(line, "ip saddr") &&
			!slices.Contains(assignments, assignment) {
			assignments = append(assignments, assignment)
		}
	}
	slices.Sort(assignments)
	return strings.Join(assignments, "; ")
}

func WaitTunnelRuntimeIPv4Selection(
	t *testing.T,
	daemon *RuntimeDaemon,
	description string,
	accept func(string) bool,
) {
	t.Helper()
	deadline := time.Now().Add(TunnelRuntimeRecoverWindow)
	var selection string
	var err error
	for time.Now().Before(deadline) {
		selection, err = TunnelRuntimeIPv4Selection()
		if err == nil && accept(selection) {
			return
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"IPv4 steering assignment %q (%v), want %s; daemon=%s",
		selection,
		err,
		description,
		TunnelRuntimeLog(t, daemon),
	)
}

func ReadTunnelRuntimeIPv4State(run TunnelRuntimeRun) map[string]TunnelRuntimeIPv4State {
	state := make(map[string]TunnelRuntimeIPv4State)
	for provider := range TunnelRuntimeIPv4Providers() {
		entry, found := ReadTunnelRuntimeInterface(run.Read, provider)
		if !found {
			continue
		}
		state[provider] = TunnelRuntimeIPv4State{
			Routing:   entry.V4.Ownership.Routing,
			Readiness: entry.V4.Ownership.Readiness,
			Carrying:  entry.Steering.State.Carrying,
		}
	}
	return state
}

func WaitTunnelRuntimeIPv4Baseline(t *testing.T, daemon *RuntimeDaemon, run TunnelRuntimeRun) {
	t.Helper()
	for provider := range TunnelRuntimeIPv4Providers() {
		WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			provider,
			"ipv4",
			"ready",
			"ready",
			TunnelRuntimeRecoverWindow,
		)
	}
	WaitTunnelRuntimeIPv4Selection(
		t,
		daemon,
		"the assignment before the first tunnel fault",
		func(selection string) bool {
			return selection == run.Ipv4.Selection
		},
	)
}
