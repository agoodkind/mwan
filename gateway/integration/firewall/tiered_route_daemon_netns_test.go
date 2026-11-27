//go:build linux && firewall

package firewall_test

import (
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestCombinedPathsDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runCombinedPathsDaemonRuntime)
}

func runCombinedPathsDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	provider := testenv.CombinedDirectProvider()
	combined := &testenv.CombinedRun{
		Run:      run,
		Topology: testenv.BuildCombinedTopology(t, run, provider),
		Provider: provider,
	}
	combined.Network = testenv.WriteCombinedNetwork(t, run.NetworkDir, provider)
	testenv.CountCombinedConnectionAttempts(t, combined)
	started := time.Now()
	combined.Daemon = run.Start(t, "combined-first")
	testenv.WaitRuntimeTable(t, combined.Daemon, "inet", "filter", 10*time.Second)
	testenv.CombinedStartsWithoutPeers(t, combined, started)
	testenv.CombinedPeersStart(t, combined)
	testenv.CombinedRouteWithdrawal(t, combined)
	testenv.CombinedSessionLoss(t, combined)
	testenv.CombinedTunnelLoss(t, combined)
	testenv.CombinedUnderlayLoss(t, combined)
	testenv.CombinedSinglePaths(t, combined)
	testenv.CombinedOrdinaryFamilyLoss(t, combined)
	testenv.CombinedRestart(t, combined)
}
