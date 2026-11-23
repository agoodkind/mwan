//go:build linux && (firewall || protocol || firewallnetns)

package commandline

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type tunnelRuntimeIPv4State struct {
	Routing   string
	Readiness string
	Carrying  bool
}

func tunnelRuntimeIPv4Providers() map[string]string {
	return map[string]string{
		tunnelRuntimeUnderlay:  tunnelRuntimeLocal,
		tunnelRuntimeAlternate: tunnelRuntimeAltLocal,
	}
}

func tunnelRuntimeClientsV4() []string {
	return []string{tunnelRuntimeClientV4, "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6"}
}

func tunnelRuntimeIPv4Selection() (string, error) {
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
	selection := tunnelRuntimeIPv4Assignments(string(output))
	if selection == "" {
		return "", fmt.Errorf("the steering chain has no IPv4 mark assignment: %s", output)
	}
	return selection, nil
}

func tunnelRuntimeIPv4Assignments(chain string) string {
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

func waitTunnelRuntimeIPv4Selection(
	t *testing.T,
	daemon *runtimeDaemon,
	description string,
	accept func(string) bool,
) {
	t.Helper()
	deadline := time.Now().Add(tunnelRuntimeRecoverWindow)
	var selection string
	var err error
	for time.Now().Before(deadline) {
		selection, err = tunnelRuntimeIPv4Selection()
		if err == nil && accept(selection) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"IPv4 steering assignment %q (%v), want %s; daemon=%s",
		selection,
		err,
		description,
		tunnelRuntimeLog(t, daemon),
	)
}

func readTunnelRuntimeIPv4State(run tunnelRuntimeRun) map[string]tunnelRuntimeIPv4State {
	state := make(map[string]tunnelRuntimeIPv4State)
	for provider := range tunnelRuntimeIPv4Providers() {
		entry, found := readTunnelRuntimeInterface(run.read, provider)
		if !found {
			continue
		}
		state[provider] = tunnelRuntimeIPv4State{
			Routing:   entry.V4.Ownership.Routing,
			Readiness: entry.V4.Ownership.Readiness,
			Carrying:  entry.Steering.State.Carrying,
		}
	}
	return state
}

func waitTunnelRuntimeIPv4Baseline(t *testing.T, daemon *runtimeDaemon, run tunnelRuntimeRun) {
	t.Helper()
	for provider := range tunnelRuntimeIPv4Providers() {
		waitTunnelRuntimeFamily(
			t,
			daemon,
			run.read,
			provider,
			"ipv4",
			"ready",
			"ready",
			tunnelRuntimeRecoverWindow,
		)
	}
	waitTunnelRuntimeIPv4Selection(
		t,
		daemon,
		"the assignment before the first tunnel fault",
		func(selection string) bool {
			return selection == run.ipv4.Selection
		},
	)
}
