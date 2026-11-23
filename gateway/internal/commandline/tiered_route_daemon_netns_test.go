//go:build linux && firewall

package commandline

import (
	"testing"
	"time"
)

func TestCombinedPathsDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runCombinedPathsDaemonRuntime)
}

func runCombinedPathsDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	provider := combinedDirectProvider()
	combined := &combinedRun{
		run:      run,
		topology: buildCombinedTopology(t, run, provider),
		provider: provider,
	}
	combined.network = writeCombinedNetwork(t, run.networkDir, provider)
	started := time.Now()
	combined.daemon = run.start(t, "combined-first")
	waitRuntimeTable(t, combined.daemon, "inet", "filter", 10*time.Second)
	combinedStartsWithoutPeers(t, combined, started)
	combinedPeersStart(t, combined)
	combinedRouteWithdrawal(t, combined)
	combinedSessionLoss(t, combined)
	combinedTunnelLoss(t, combined)
	combinedUnderlayLoss(t, combined)
	combinedSinglePaths(t, combined)
	combinedOrdinaryFamilyLoss(t, combined)
	combinedRestart(t, combined)
}
