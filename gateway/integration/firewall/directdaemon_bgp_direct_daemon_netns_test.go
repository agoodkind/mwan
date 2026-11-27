//go:build linux && firewall

package firewall_test

import (
	"net/netip"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestBGPDirectDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *testenv.BgpRuntimeRun) {
			provider := testenv.BgpRuntimeDirectProvider(
				testenv.BgpRuntimeLocalASN,
				testenv.BgpRuntimeRemoteASN,
				"::/0",
			)
			daemon := runBGPDirectDaemonRuntime(t, run, provider)
			testBGPDirectCrashAndRemoval(t, run, daemon, provider)
		},
	)
}

func TestBGPDirectIBGPDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(
		t,
		func(t *testing.T, run *testenv.BgpRuntimeRun) {
			runBGPDirectDaemonRuntime(
				t,
				run,
				testenv.BgpRuntimeDirectProvider(
					testenv.BgpRuntimeLocalASN,
					testenv.BgpRuntimeLocalASN,
					"2001:db8:a:99::/64",
				),
			)
		},
	)
}

func runBGPDirectDaemonRuntime(
	t *testing.T,
	run *testenv.BgpRuntimeRun,
	provider testenv.BgpRuntimeProvider,
) *testenv.RuntimeDaemon {
	provider.RestartTime = testenv.BgpRuntimeRestartTime
	run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway, provider)
	testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir, provider)
	daemon := run.Start(t, "bgp-first")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	testenv.BgpRuntimeBecomesReady(t, run, daemon, provider)
	testenv.RequireBGPRuntimeExchange(t, run, daemon, provider)
	testenv.BgpRuntimePeerLoss(t, run, daemon, provider)
	testenv.BgpRuntimeForwardingLoss(t, run, daemon, provider)
	return testenv.BgpRuntimeGracefulRestart(t, run, daemon, provider)
}

func testBGPDirectCrashAndRemoval(
	t *testing.T,
	run *testenv.BgpRuntimeRun,
	daemon *testenv.RuntimeDaemon,
	provider testenv.BgpRuntimeProvider,
) {
	t.Helper()
	testenv.KillOwnedRuntimeDaemon(t, daemon)
	if !testenv.BgpRuntimeGatewayRoute(t, provider.Device, provider.Learned) {
		t.Fatalf("gateway lost %s when the daemon was killed", provider.Learned)
	}
	restarted := run.Start(t, "bgp-after-crash")
	testenv.WaitTunnelRuntimeFamily(
		t,
		restarted,
		run.Read,
		provider.Device,
		"ipv6",
		"ready",
		"ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	router := run.Routers[provider.Id]
	testenv.WaitBGPRuntimeReceived(
		t,
		restarted,
		run.Read,
		router,
		netip.MustParsePrefix(testenv.BgpRuntimeHome),
	)
	testenv.RequireBGPRuntimeExchange(t, run, restarted, provider)

	testenv.KillOwnedRuntimeDaemon(t, restarted)
	if !testenv.BgpRuntimeGatewayRoute(t, provider.Device, provider.Learned) {
		t.Fatalf("gateway lost %s before session removal", provider.Learned)
	}
	provider.Session = false
	testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir, provider)
	removed := run.Start(t, "bgp-without-session")
	testenv.WaitBGPRuntimeGatewayRoute(
		t,
		removed,
		run.Read,
		provider.Device,
		provider.Learned,
		false,
	)
	testenv.WaitTunnelRuntimeFamily(
		t,
		removed,
		run.Read,
		provider.Device,
		"ipv6",
		"not-ready",
		"not-ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	testenv.WaitBGPRuntimeReceived(t, removed, run.Read, router)
	testenv.RequireBGPRuntimeIPv4(t, run, removed)
}
