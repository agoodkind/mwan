//go:build linux && firewall

package commandline

import (
	"net/netip"
	"testing"
	"time"
)

func TestBGPDirectDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *bgpRuntimeRun) {
			provider := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, "::/0")
			daemon := runBGPDirectDaemonRuntime(t, run, provider)
			testBGPDirectCrashAndRemoval(t, run, daemon, provider)
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

func runBGPDirectDaemonRuntime(
	t *testing.T,
	run *bgpRuntimeRun,
	provider bgpRuntimeProvider,
) *runtimeDaemon {
	provider.restartTime = bgpRuntimeRestartTime
	run.topology = buildBGPRuntimeTopology(t, run.gateway, provider)
	writeBGPRuntimeNetwork(t, run.networkDir, provider)
	daemon := run.start(t, "bgp-first")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	bgpRuntimeBecomesReady(t, run, daemon, provider)
	requireBGPRuntimeExchange(t, run, daemon, provider)
	bgpRuntimePeerLoss(t, run, daemon, provider)
	bgpRuntimeForwardingLoss(t, run, daemon, provider)
	return bgpRuntimeGracefulRestart(t, run, daemon, provider)
}

func testBGPDirectCrashAndRemoval(
	t *testing.T,
	run *bgpRuntimeRun,
	daemon *runtimeDaemon,
	provider bgpRuntimeProvider,
) {
	t.Helper()
	killOwnedRuntimeDaemon(t, daemon)
	if !bgpRuntimeGatewayRoute(t, provider.device, provider.learned) {
		t.Fatalf("gateway lost %s when the daemon was killed", provider.learned)
	}
	restarted := run.start(t, "bgp-after-crash")
	waitTunnelRuntimeFamily(
		t,
		restarted,
		run.read,
		provider.device,
		"ipv6",
		"ready",
		"ready",
		tunnelRuntimeRecoverWindow,
	)
	router := run.routers[provider.id]
	waitBGPRuntimeReceived(t, restarted, run.read, router, netip.MustParsePrefix(bgpRuntimeHome))
	requireBGPRuntimeExchange(t, run, restarted, provider)

	killOwnedRuntimeDaemon(t, restarted)
	if !bgpRuntimeGatewayRoute(t, provider.device, provider.learned) {
		t.Fatalf("gateway lost %s before session removal", provider.learned)
	}
	provider.session = false
	writeBGPRuntimeNetwork(t, run.networkDir, provider)
	removed := run.start(t, "bgp-without-session")
	waitBGPRuntimeGatewayRoute(t, removed, run.read, provider.device, provider.learned, false)
	waitTunnelRuntimeFamily(
		t,
		removed,
		run.read,
		provider.device,
		"ipv6",
		"not-ready",
		"not-ready",
		tunnelRuntimeRecoverWindow,
	)
	waitBGPRuntimeReceived(t, removed, run.read, router)
	requireBGPRuntimeIPv4(t, run, removed)
}
