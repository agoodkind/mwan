//go:build linux && firewallnetns

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pathCasesHealthEnabled = `"enabled":true`

func TestPathRoutingFailureDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, func(t *testing.T, run *bgpRuntimeRun) {
		run.topology = buildBGPRuntimeTopology(t, run.gateway)
		writeBGPRuntimeNetwork(t, run.networkDir)
		config, err := os.OpenFile(run.configPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(config, "[ifmgr.modules.wan.routes]\nhealth_state_file = %q\n", t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if err := config.Close(); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		daemon := run.start(t, "routing-failure")
		waitCombinedTransition(t, daemon, run.read, tunnelRuntimeUnderlay, "ipv4", started, "unavailable with the routing failure",
			func(transition combinedTransition) bool {
				return transition.Current == combinedUnavailable && transition.Dependency == "routing" &&
					strings.Contains(transition.Reason, "health state")
			})
	})
}

func TestPathHealthDisabledDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, func(t *testing.T, run *bgpRuntimeRun) {
		run.topology = buildBGPRuntimeTopology(t, run.gateway)
		network := bgpRuntimeNetwork()
		if count := strings.Count(network, pathCasesHealthEnabled); count != 1 {
			t.Fatalf("health setting occurs %d times, want 1", count)
		}
		network = strings.Replace(network, pathCasesHealthEnabled, `"enabled":false`, 1)
		if err := os.WriteFile(filepath.Join(run.networkDir, "network.json"), []byte(network), 0o600); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		daemon := run.start(t, "health-disabled")
		waitCombinedReady(t, daemon, run.read, tunnelRuntimeUnderlay, "ipv4", started)
		requireBGPRuntimeFamilyV4(t, run.read, tunnelRuntimeUnderlay)
		requireBGPRuntimeIPv4(t, run, daemon)
	})
}

func requireBGPRuntimeFamilyV4(t *testing.T, read func() string, device string) {
	t.Helper()
	entry, found := readTunnelRuntimeInterface(read, device)
	if state := entry.V4.Ownership; !found || state.Routing != "ready" || state.Readiness != "ready" {
		t.Fatalf("%s ipv4 routing=%s readiness=%s, want ready", device, state.Routing, state.Readiness)
	}
}
