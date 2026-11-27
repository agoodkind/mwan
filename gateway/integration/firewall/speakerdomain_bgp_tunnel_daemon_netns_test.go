//go:build linux && firewall

package firewall_test

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestBGPTunnelDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPTunnelDaemonRuntime)
}

func runBGPTunnelDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(testenv.BgpRuntimeHome)
	client := netip.MustParseAddr(testenv.TunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(testenv.TunnelRuntimeRemoteClient)
	peer := netip.MustParseAddr(testenv.TunnelRuntimeInnerRemote)
	topology := testenv.BuildTunnelRuntimeTopology(t, run.Gateway)
	testenv.RemoveTunnelRuntimeReturnRoute(t, topology)
	if err := os.WriteFile(
		filepath.Join(run.NetworkDir, "network.json"),
		[]byte(testenv.BgpTunnelRuntimeNetwork(t)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	daemon := run.Start(t, "bgp-tunnel")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	router := testenv.StartBGPRuntimeRouter(
		t,
		topology.Endpoint,
		"far6in4",
		"198.51.100.1",
		testenv.BgpRuntimeRemoteASN,
		peer,
		netip.MustParseAddr(testenv.TunnelRuntimeInnerLocal),
		testenv.BgpRuntimeLocalASN,
	)
	router.Announce(t, defaultRoute, peer)
	established := func(session testenv.BgpRuntimeSession) bool {
		return session.Established &&
			slices.Equal(session.Accepted, []string{defaultRoute.String()})
	}
	exchange := func() {
		t.Helper()
		testenv.WaitBGPRuntimeSession(
			t,
			daemon,
			run.Read,
			testenv.TunnelRuntimeDevice,
			"an established state and the default route",
			established,
		)
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			testenv.TunnelRuntimeDevice,
			"ipv6",
			"ready",
			"ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
		testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, router, home)
		observed := testenv.WaitTunnelRuntimeTransfer(
			t,
			daemon,
			topology,
			topology.Remote,
			topology.Client,
			"tcp6",
			net.JoinHostPort(remoteClient.String(), strconv.Itoa(testenv.BgpRuntimeRemotePort)),
			net.JoinHostPort(client.String(), "0"),
			testenv.TunnelRuntimeRecoverWindow,
		)
		if observed != client {
			t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
		}
		observed = testenv.WaitTunnelRuntimeTransfer(
			t,
			daemon,
			topology,
			topology.Client,
			topology.Remote,
			"tcp6",
			net.JoinHostPort(client.String(), strconv.Itoa(testenv.BgpRuntimeClientPort)),
			net.JoinHostPort(remoteClient.String(), "0"),
			testenv.TunnelRuntimeRecoverWindow,
		)
		if observed != remoteClient {
			t.Fatalf(
				"internal listener observed %s, want the remote client %s",
				observed,
				remoteClient,
			)
		}
	}
	exchange()

	testenv.SetTunnelRuntimeFault(t, topology, true)
	testenv.WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		"no established state",
		func(session testenv.BgpRuntimeSession) bool {
			return !session.Established && len(session.Accepted) == 0
		},
	)
	testenv.WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	testenv.WaitBGPRuntimeGatewayRoute(
		t,
		daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		defaultRoute,
		false,
	)
	testenv.WaitBGPRuntime(
		t,
		daemon,
		run.Read,
		"endpoint without the home prefix",
		testenv.TunnelRuntimeRecoverWindow,
		func() bool {
			return !router.KernelRoute(t, home)
		},
	)

	testenv.SetTunnelRuntimeFault(t, topology, false)
	exchange()
}
