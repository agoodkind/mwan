//go:build linux && firewall

package firewall_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

const pathCasesHealthEnabled = `"enabled":true`

func TestPathRoutingFailureDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *testenv.BgpRuntimeRun) {
			run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway)
			testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir)
			config, err := os.OpenFile(run.ConfigPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprintf(
				config,
				"[ifmgr.modules.wan.routes]\nhealth_state_file = %q\n",
				t.TempDir(),
			); err != nil {
				t.Fatal(err)
			}
			if err := config.Close(); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			daemon := run.Start(t, "routing-failure")
			testenv.WaitCombinedTransition(
				t,
				daemon,
				run.Read,
				testenv.TunnelRuntimeUnderlay,
				"ipv4",
				started,
				"unavailable with the routing failure",
				func(transition testenv.CombinedTransition) bool {
					return transition.Current == testenv.CombinedUnavailable &&
						transition.Dependency == "routing" &&
						strings.Contains(transition.Reason, "health state")
				},
			)
		},
	)
}

func TestPathHealthDisabledDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *testenv.BgpRuntimeRun) {
			run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway)
			network := testenv.BgpRuntimeNetwork()
			if count := strings.Count(network, pathCasesHealthEnabled); count != 1 {
				t.Fatalf("health setting occurs %d times, want 1", count)
			}
			network = strings.Replace(network, pathCasesHealthEnabled, `"enabled":false`, 1)
			if err := os.WriteFile(
				filepath.Join(run.NetworkDir, "network.json"),
				[]byte(network),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			daemon := run.Start(t, "health-disabled")
			testenv.WaitCombinedReady(
				t,
				daemon,
				run.Read,
				testenv.TunnelRuntimeUnderlay,
				"ipv4",
				started,
			)
			requireBGPRuntimeFamilyV4(t, run.Read, testenv.TunnelRuntimeUnderlay)
			testenv.RequireBGPRuntimeIPv4(t, run, daemon)
		},
	)
}

func requireBGPRuntimeFamilyV4(t *testing.T, read func() string, device string) {
	t.Helper()
	entry, found := testenv.ReadTunnelRuntimeInterface(read, device)
	if state := entry.V4.Ownership; !found || state.Routing != "ready" ||
		state.Readiness != "ready" {
		t.Fatalf(
			"%s ipv4 routing=%s readiness=%s, want ready",
			device,
			state.Routing,
			state.Readiness,
		)
	}
}
