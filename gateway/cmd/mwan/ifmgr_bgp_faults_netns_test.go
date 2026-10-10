//go:build linux && firewallnetns

package main

import (
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

const (
	bgpRuntimeClientPort = 18179
	bgpRuntimeRemotePort = 18180
	bgpRuntimeISPPort    = 18181
)

func requireBGPRuntimeIPv4(t *testing.T, run *bgpRuntimeRun, daemon *runtimeDaemon) {
	t.Helper()
	gateway := tunnelRuntimeTopology{gateway: run.gateway}
	destination := net.JoinHostPort(tunnelRuntimeISPGateway, strconv.Itoa(bgpRuntimeISPPort))
	waitTunnelRuntimeTransfer(t, daemon, gateway, run.topology.isp, run.topology.client, "tcp4", destination,
		net.JoinHostPort(tunnelRuntimeClientV4, "0"), tunnelRuntimeRecoverWindow)
}

func requireBGPRuntimeIPv4Unchanged(t *testing.T, run *bgpRuntimeRun, since time.Time) {
	t.Helper()
	entry, found := readTunnelRuntimeInterface(run.read, tunnelRuntimeUnderlay)
	if state := entry.V4.Ownership; !found || state.Routing != "ready" || state.Readiness != "ready" {
		t.Fatalf("%s ipv4 routing=%s readiness=%s, want ready", tunnelRuntimeUnderlay, state.Routing, state.Readiness)
	}
	for _, transition := range entry.Ownership.Transitions {
		evaluation := transition.Operation == "evaluate-routing" || transition.Operation == "evaluate-forwarding"
		if transition.Family == "ipv4" && evaluation && !transition.At.Before(since) {
			t.Fatalf("%s ipv4 changed during the IPv6 fault: %+v", tunnelRuntimeUnderlay, transition)
		}
	}
}

func requireBGPRuntimeExchange(t *testing.T, run *bgpRuntimeRun, daemon *runtimeDaemon, provider bgpRuntimeProvider) {
	t.Helper()
	gateway := tunnelRuntimeTopology{gateway: run.gateway}
	remote := run.topology.remotes[provider.id]
	client := netip.MustParseAddr(tunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(provider.remoteClient())
	if !run.routers[provider.id].kernelRoute(t, netip.MustParsePrefix(bgpRuntimeHome)) {
		t.Fatalf("router has no BGP route to %s", bgpRuntimeHome)
	}
	observed := waitTunnelRuntimeTransfer(t, daemon, gateway, remote, run.topology.client, "tcp6",
		net.JoinHostPort(remoteClient.String(), strconv.Itoa(bgpRuntimeRemotePort)), net.JoinHostPort(client.String(), "0"),
		tunnelRuntimeRecoverWindow)
	if observed != client {
		t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
	}
	observed = waitTunnelRuntimeTransfer(t, daemon, gateway, run.topology.client, remote, "tcp6",
		net.JoinHostPort(client.String(), strconv.Itoa(bgpRuntimeClientPort)), net.JoinHostPort(remoteClient.String(), "0"),
		tunnelRuntimeRecoverWindow)
	if observed != remoteClient {
		t.Fatalf("internal listener observed %s, want the remote client %s", observed, remoteClient)
	}
	requireBGPRuntimeIPv4(t, run, daemon)
}

func bgpRuntimePeerLoss(t *testing.T, run *bgpRuntimeRun, daemon *runtimeDaemon, provider bgpRuntimeProvider) {
	t.Helper()
	router := run.routers[provider.id]
	home := netip.MustParsePrefix(bgpRuntimeHome)
	since := time.Now()
	router.deleteNeighbor(t)
	waitBGPRuntimeGatewayRoute(t, daemon, run.read, provider.device, provider.learned, false)
	waitBGPRuntimeReason(t, daemon, run.read, provider.device, since, bgpRuntimeNotEstablished)
	waitBGPRuntime(t, daemon, run.read, "router without the home prefix", tunnelRuntimeFailureWindow, func() bool {
		return !router.kernelRoute(t, home)
	})
	requireBGPRuntimeFamily(t, run.read, provider.device, "not-ready", "not-ready")
	requireBGPRuntimeIPv4(t, run, daemon)

	router.addNeighbor(t)
	waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeReceived(t, daemon, run.read, router, home)
	requireBGPRuntimeExchange(t, run, daemon, provider)
	requireBGPRuntimeIPv4Unchanged(t, run, since)
}

func bgpRuntimeForwardingLoss(t *testing.T, run *bgpRuntimeRun, daemon *runtimeDaemon, provider bgpRuntimeProvider) {
	t.Helper()
	router := run.routers[provider.id]
	since := time.Now()
	setBGPRuntimeFault(t, run.topology, provider, true)
	waitBGPRuntimeReason(t, daemon, run.read, provider.device, since, bgpRuntimeProbeFailed)
	waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "not-ready", "not-ready", tunnelRuntimeFailureWindow)
	waitBGPRuntimeReceived(t, daemon, run.read, router)
	waitBGPRuntimeSession(t, daemon, run.read, provider.device, "an established state and no advertised prefix", func(session bgpRuntimeSession) bool {
		return session.Established && len(session.Advertised) == 0 && len(session.Accepted) == 1
	})
	requireBGPRuntimeIPv4(t, run, daemon)

	setBGPRuntimeFault(t, run.topology, provider, false)
	waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeReceived(t, daemon, run.read, router, netip.MustParsePrefix(bgpRuntimeHome))
	requireBGPRuntimeIPv4Unchanged(t, run, since)
}

func bgpRuntimeRestart(t *testing.T, run *bgpRuntimeRun, first *runtimeDaemon, provider bgpRuntimeProvider) *runtimeDaemon {
	t.Helper()
	killOwnedRuntimeDaemon(t, first)
	if !bgpRuntimeGatewayRoute(t, provider.device, provider.learned) {
		t.Fatalf("gateway has no BGP route to %s after the daemon process ended", provider.learned)
	}
	second := run.start(t, "bgp-second")
	waitTunnelRuntimeFamily(t, second, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeReceived(t, second, run.read, run.routers[provider.id], netip.MustParsePrefix(bgpRuntimeHome))
	requireBGPRuntimeExchange(t, run, second, provider)
	return second
}

func bgpRuntimeSessionRemoval(t *testing.T, run *bgpRuntimeRun, second *runtimeDaemon, provider bgpRuntimeProvider) {
	t.Helper()
	killOwnedRuntimeDaemon(t, second)
	if !bgpRuntimeGatewayRoute(t, provider.device, provider.learned) {
		t.Fatalf("gateway has no BGP route to %s before the removal", provider.learned)
	}
	provider.session = false
	writeBGPRuntimeNetwork(t, run.networkDir, provider)
	third := run.start(t, "bgp-third")
	waitBGPRuntimeGatewayRoute(t, third, run.read, provider.device, provider.learned, false)
	waitTunnelRuntimeFamily(t, third, run.read, provider.device, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeReceived(t, third, run.read, run.routers[provider.id])
	requireBGPRuntimeIPv4(t, run, third)
}
