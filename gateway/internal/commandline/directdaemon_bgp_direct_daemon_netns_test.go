//go:build linux && firewall

package commandline

import (
	"testing"
	"time"
)

func TestBGPDirectDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *bgpRuntimeRun) {
			runBGPDirectDaemonRuntime(
				t,
				run,
				bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, "::/0"),
			)
		},
	)
}

func TestBGPDirectIBGPDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *bgpRuntimeRun) {
			runBGPDirectDaemonRuntime(
				t,
				run,
				bgpRuntimeDirectProvider(
					bgpRuntimeLocalASN,
					bgpRuntimeLocalASN,
					"2001:db8:a:99::/64",
				),
			)
		},
	)
}

func runBGPDirectDaemonRuntime(t *testing.T, run *bgpRuntimeRun, provider bgpRuntimeProvider) {
	provider.restartTime = bgpRuntimeRestartTime
	run.topology = buildBGPRuntimeTopology(t, run.gateway, provider)
	writeBGPRuntimeNetwork(t, run.networkDir, provider)
	daemon := run.start(t, "bgp-first")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	bgpRuntimeBecomesReady(t, run, daemon, provider)
	requireBGPRuntimeExchange(t, run, daemon, provider)
	bgpRuntimePeerLoss(t, run, daemon, provider)
	bgpRuntimeForwardingLoss(t, run, daemon, provider)
	bgpRuntimeGracefulRestart(t, run, daemon, provider)
}
