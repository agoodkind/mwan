//go:build linux && (firewall || protocol || firewallnetns)

package commandline

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"
)

const (
	bgpRuntimeClientPort      = 18179
	bgpRuntimeRemotePort      = 18180
	bgpRuntimeISPPort         = 18181
	bgpRuntimeRestartTime     = 10
	bgpRuntimeRestartDuration = bgpRuntimeRestartTime * time.Second
)

func requireBGPRuntimeIPv4(t *testing.T, run *bgpRuntimeRun, daemon *runtimeDaemon) {
	t.Helper()
	gateway := tunnelRuntimeTopology{gateway: run.gateway}
	destination := net.JoinHostPort(tunnelRuntimeISPGateway, strconv.Itoa(bgpRuntimeISPPort))
	waitTunnelRuntimeTransfer(
		t,
		daemon,
		gateway,
		run.topology.isp,
		run.topology.client,
		"tcp4",
		destination,
		net.JoinHostPort(tunnelRuntimeClientV4, "0"),
		tunnelRuntimeRecoverWindow,
	)
}

func requireBGPRuntimeIPv4Unchanged(t *testing.T, run *bgpRuntimeRun, since time.Time) {
	t.Helper()
	entry, found := readTunnelRuntimeInterface(run.read, tunnelRuntimeUnderlay)
	if state := entry.V4.Ownership; !found || state.Routing != "ready" ||
		state.Readiness != "ready" {
		t.Fatalf(
			"%s ipv4 routing=%s readiness=%s, want ready",
			tunnelRuntimeUnderlay,
			state.Routing,
			state.Readiness,
		)
	}
	for _, transition := range entry.Ownership.Transitions {
		evaluation := transition.Operation == "evaluate-routing" ||
			transition.Operation == "evaluate-forwarding"
		if transition.Family == "ipv4" && evaluation && !transition.At.Before(since) {
			t.Fatalf(
				"%s ipv4 changed during the IPv6 fault: %+v",
				tunnelRuntimeUnderlay,
				transition,
			)
		}
	}
}

func requireBGPRuntimeExchange(
	t *testing.T,
	run *bgpRuntimeRun,
	daemon *runtimeDaemon,
	provider bgpRuntimeProvider,
) {
	t.Helper()
	gateway := tunnelRuntimeTopology{gateway: run.gateway}
	remote := run.topology.remotes[provider.id]
	client := netip.MustParseAddr(tunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(provider.remoteClient())
	if !run.routers[provider.id].kernelRoute(t, netip.MustParsePrefix(bgpRuntimeHome)) {
		t.Fatalf("router has no BGP route to %s", bgpRuntimeHome)
	}
	observed := waitTunnelRuntimeTransfer(
		t,
		daemon,
		gateway,
		remote,
		run.topology.client,
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
		gateway,
		run.topology.client,
		remote,
		"tcp6",
		net.JoinHostPort(client.String(), strconv.Itoa(bgpRuntimeClientPort)),
		net.JoinHostPort(remoteClient.String(), "0"),
		tunnelRuntimeRecoverWindow,
	)
	if observed != remoteClient {
		t.Fatalf("internal listener observed %s, want the remote client %s", observed, remoteClient)
	}
	requireBGPRuntimeIPv4(t, run, daemon)
}

func bgpRuntimePeerLoss(
	t *testing.T,
	run *bgpRuntimeRun,
	daemon *runtimeDaemon,
	provider bgpRuntimeProvider,
) {
	t.Helper()
	router := run.routers[provider.id]
	home := netip.MustParsePrefix(bgpRuntimeHome)
	since := time.Now()
	router.deleteNeighbor(t)
	waitBGPRuntimeGatewayRoute(t, daemon, run.read, provider.device, provider.learned, false)
	waitBGPRuntimeReason(t, daemon, run.read, provider.device, since, bgpRuntimeNotEstablished)
	waitBGPRuntime(
		t,
		daemon,
		run.read,
		"router without the home prefix",
		tunnelRuntimeFailureWindow,
		func() bool {
			return !router.kernelRoute(t, home)
		},
	)
	waitTunnelRuntimeFamily(
		t,
		daemon,
		run.read,
		provider.device,
		"ipv6",
		"not-ready",
		"not-ready",
		tunnelRuntimeFailureWindow,
	)
	requireBGPRuntimeIPv4(t, run, daemon)

	router.addNeighbor(t)
	waitTunnelRuntimeFamily(
		t,
		daemon,
		run.read,
		provider.device,
		"ipv6",
		"ready",
		"ready",
		tunnelRuntimeRecoverWindow,
	)
	waitBGPRuntimeReceived(t, daemon, run.read, router, home)
	requireBGPRuntimeExchange(t, run, daemon, provider)
	requireBGPRuntimeIPv4Unchanged(t, run, since)
}

func bgpRuntimeForwardingLoss(
	t *testing.T,
	run *bgpRuntimeRun,
	daemon *runtimeDaemon,
	provider bgpRuntimeProvider,
) {
	t.Helper()
	router := run.routers[provider.id]
	since := time.Now()
	setBGPRuntimeFault(t, run.topology, provider, true)
	waitBGPRuntimeReason(t, daemon, run.read, provider.device, since, bgpRuntimeProbeFailed)
	waitTunnelRuntimeFamily(
		t,
		daemon,
		run.read,
		provider.device,
		"ipv6",
		"not-ready",
		"not-ready",
		tunnelRuntimeFailureWindow,
	)
	waitBGPRuntimeReceived(t, daemon, run.read, router)
	waitBGPRuntimeSession(
		t,
		daemon,
		run.read,
		provider.device,
		"an established state and no advertised prefix",
		func(session bgpRuntimeSession) bool {
			return session.Established && len(session.Advertised) == 0 && len(session.Accepted) == 1
		},
	)
	requireBGPRuntimeIPv4(t, run, daemon)

	setBGPRuntimeFault(t, run.topology, provider, false)
	waitTunnelRuntimeFamily(
		t,
		daemon,
		run.read,
		provider.device,
		"ipv6",
		"ready",
		"ready",
		tunnelRuntimeRecoverWindow,
	)
	waitBGPRuntimeReceived(t, daemon, run.read, router, netip.MustParsePrefix(bgpRuntimeHome))
	requireBGPRuntimeIPv4Unchanged(t, run, since)
}

func bgpRuntimeGracefulRestart(
	t *testing.T,
	run *bgpRuntimeRun,
	first *runtimeDaemon,
	provider bgpRuntimeProvider,
) *runtimeDaemon {
	t.Helper()
	router := run.routers[provider.id]
	home := netip.MustParsePrefix(bgpRuntimeHome)
	router.retainRoutesOnStop()
	stopRuntimeDaemon(t, first)
	waitBGPRuntime(
		t,
		nil,
		run.read,
		"retained routes while the daemon is stopped",
		bgpRuntimeRestartDuration,
		func() bool {
			return router.observedGracefulStop() &&
				bgpRuntimeGatewayRoute(t, provider.device, provider.learned) &&
				router.kernelRoute(t, home) && !router.hasFreshPath(t, home) &&
				slices.Equal(router.received(t), []netip.Prefix{home})
		},
	)
	second := run.start(t, "bgp-graceful")
	waitBGPRuntime(
		t,
		second,
		run.read,
		"the refreshed home prefix at the router",
		tunnelRuntimeRecoverWindow,
		func() bool {
			return router.hasFreshPath(t, home) && router.kernelRoute(t, home)
		},
	)
	waitTunnelRuntimeFamily(
		t,
		second,
		run.read,
		provider.device,
		"ipv6",
		"ready",
		"ready",
		tunnelRuntimeRecoverWindow,
	)
	waitBGPRuntimeSession(
		t,
		second,
		run.read,
		provider.device,
		"the refreshed routes",
		func(session bgpRuntimeSession) bool {
			return session.Established && len(session.Retained) == 0 &&
				slices.Equal(session.Accepted, []string{provider.learned.String()}) &&
				slices.Equal(session.Advertised, []string{home.String()})
		},
	)
	requireBGPRuntimeExchange(t, run, second, provider)
	return second
}
