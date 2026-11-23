//go:build linux && firewall

package commandline

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestBGPTunnelDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPTunnelDaemonRuntime)
}

func runBGPTunnelDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(bgpRuntimeHome)
	client := netip.MustParseAddr(tunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(tunnelRuntimeRemoteClient)
	peer := netip.MustParseAddr(tunnelRuntimeInnerRemote)
	topology := buildTunnelRuntimeTopology(t, run.gateway)
	removeTunnelRuntimeReturnRoute(t, topology)
	if err := os.WriteFile(
		filepath.Join(run.networkDir, "network.json"),
		[]byte(bgpTunnelRuntimeNetwork(t)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	daemon := run.start(t, "bgp-tunnel")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	router := startBGPRuntimeRouter(
		t,
		topology.endpoint,
		"far6in4",
		"198.51.100.1",
		bgpRuntimeRemoteASN,
		peer,
		netip.MustParseAddr(tunnelRuntimeInnerLocal),
		bgpRuntimeLocalASN,
	)
	router.announce(t, defaultRoute, peer)
	established := func(session bgpRuntimeSession) bool {
		return session.Established &&
			slices.Equal(session.Accepted, []string{defaultRoute.String()})
	}
	exchange := func() {
		t.Helper()
		waitBGPRuntimeSession(
			t,
			daemon,
			run.read,
			tunnelRuntimeDevice,
			"an established state and the default route",
			established,
		)
		waitTunnelRuntimeFamily(
			t,
			daemon,
			run.read,
			tunnelRuntimeDevice,
			"ipv6",
			"ready",
			"ready",
			tunnelRuntimeRecoverWindow,
		)
		waitBGPRuntimeReceived(t, daemon, run.read, router, home)
		observed := waitTunnelRuntimeTransfer(
			t,
			daemon,
			topology,
			topology.remote,
			topology.client,
			"tcp6",
			net.JoinHostPort(remoteClient.String(), strconv.Itoa(bgpRuntimeRemotePort)),
			net.JoinHostPort(client.String(), "0"),
			tunnelRuntimeRecoverWindow,
		)
		if observed != client {
			t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
		}
		observed = waitTunnelRuntimeTransfer(
			t,
			daemon,
			topology,
			topology.client,
			topology.remote,
			"tcp6",
			net.JoinHostPort(client.String(), strconv.Itoa(bgpRuntimeClientPort)),
			net.JoinHostPort(remoteClient.String(), "0"),
			tunnelRuntimeRecoverWindow,
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

	setTunnelRuntimeFault(t, topology, true)
	waitBGPRuntimeSession(
		t,
		daemon,
		run.read,
		tunnelRuntimeDevice,
		"no established state",
		func(session bgpRuntimeSession) bool {
			return !session.Established && len(session.Accepted) == 0
		},
	)
	waitTunnelRuntimeFamily(
		t,
		daemon,
		run.read,
		tunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		tunnelRuntimeRecoverWindow,
	)
	waitBGPRuntimeGatewayRoute(t, daemon, run.read, tunnelRuntimeDevice, defaultRoute, false)
	waitBGPRuntime(
		t,
		daemon,
		run.read,
		"endpoint without the home prefix",
		tunnelRuntimeRecoverWindow,
		func() bool {
			return !router.kernelRoute(t, home)
		},
	)

	setTunnelRuntimeFault(t, topology, false)
	exchange()
}
