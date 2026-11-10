//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"slices"
	"testing"
	"time"
)

type bgpRepeatedSession struct {
	device  string
	learned netip.Prefix
	router  *bgpRuntimeRouter
}

func TestBGPRepeatedASNDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPRepeatedASNDaemonRuntime)
}

func addBGPRepeatedDirectProvider(t *testing.T, run *bgpRuntimeRun, provider bgpRuntimeProvider) {
	t.Helper()
	router := newRuntimePeer(t, run.gateway, provider.device, provider.routerDevice,
		[]string{provider.local().String() + "/64"}, []string{provider.peer().String() + "/64"}, "")
	remote := newTunnelRuntimeNamespace(t)
	t.Cleanup(func() { _ = router.namespace.Close() })
	t.Cleanup(func() { _ = remote.Close() })
	linkTunnelRuntimeNamespaces(t, router.namespace, provider.routerDevice+"-far", []string{provider.network + ":99::1/64"},
		remote, "remote-host", []string{provider.remoteClient() + "/64"})
	addRuntimeDefault(t, "remote-host", provider.network+":99::1")
	setRuntimeNamespace(t, router.namespace)
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	setRuntimeNamespace(t, run.gateway)
	run.topology.routers[provider.id], run.topology.remotes[provider.id] = router.namespace, remote
}

func requireBGPRepeatedIntact(t *testing.T, run *bgpRuntimeRun, session bgpRepeatedSession, since time.Time) {
	t.Helper()
	entry, found := readBGPRuntimeInterface(run.read, session.device)
	if !found || len(entry.V6.Ownership.Sessions) != 1 {
		t.Fatalf("%s publishes the sessions %+v, want one session", session.device, entry.V6.Ownership.Sessions)
	}
	published := entry.V6.Ownership.Sessions[0]
	if !published.Established || !slices.Equal(published.Accepted, []string{session.learned.String()}) {
		t.Fatalf("%s session = %+v, want an established state and the accepted prefix %s", session.device, published, session.learned)
	}
	if !bgpRuntimeGatewayRoute(t, session.device, session.learned) {
		t.Fatalf("gateway has no BGP route to %s on %s", session.learned, session.device)
	}
	if received := session.router.received(t); !slices.Equal(received, []netip.Prefix{netip.MustParsePrefix(bgpRuntimeHome)}) {
		t.Fatalf("router of %s received %v, want %s", session.device, received, bgpRuntimeHome)
	}
	requireBGPRuntimeIPv6Unchanged(t, run.read, session.device, since)
}

func runBGPRepeatedASNDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	home := netip.MustParsePrefix(bgpRuntimeHome)
	vpsAddress := netip.MustParseAddr(tunnelRuntimeInnerRemote)
	direct := bgpRuntimeProvider{
		id: "direct", device: "enbgp1", routerDevice: "rtr-b", network: "2001:db8:c", table: 300, mark: 3, tier: 0, metric: 310,
		localASN: bgpRuntimeLocalASN, remoteASN: bgpRuntimeRemoteASN, learned: netip.MustParsePrefix("2001:db8:c:99::/64"),
		exports: []bgpRuntimeExport{{prefix: bgpRuntimeHome, backupFor: ""}}, session: true,
	}
	topology := buildBGPUpstreamRuntime(t, run)
	removeTunnelRuntimeReturnRoute(t, topology.tunnelRuntimeTopology)
	setRuntimeNamespace(t, topology.endpoint)
	addRuntimeDefault(t, bgpUpstreamVPSDevice, bgpUpstreamAddress)
	setRuntimeNamespace(t, run.gateway)
	addBGPRepeatedDirectProvider(t, run, direct)
	writeBGPTunnelRuntimeNetwork(t, run.networkDir, bgpTunnelRuntimeSession{peer: tunnelRuntimeInnerRemote, remoteASN: bgpRuntimeRemoteASN, tier: 1}, direct)
	daemon := run.start(t, "bgp-repeated")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)

	directRouter := run.startRouter(t, direct)
	directRouter.announce(t, direct.learned, direct.peer())
	tunnelRouter := startBGPRuntimeRouter(t, topology.endpoint, "far6in4", "198.51.100.1", bgpRuntimeRemoteASN, vpsAddress,
		netip.MustParseAddr(tunnelRuntimeInnerLocal), bgpRuntimeLocalASN)
	tunnelRouter.announce(t, netip.MustParsePrefix("::/0"), vpsAddress)
	sessions := []bgpRepeatedSession{
		{device: direct.device, learned: direct.learned, router: directRouter},
		{device: tunnelRuntimeDevice, learned: netip.MustParsePrefix("::/0"), router: tunnelRouter},
	}
	ready := func(session bgpRepeatedSession) {
		t.Helper()
		waitBGPRuntimeSession(t, daemon, run.read, session.device, "an established state and the accepted prefix", func(published bgpRuntimeSession) bool {
			return published.Established && slices.Equal(published.Accepted, []string{session.learned.String()})
		})
		waitBGPRuntimeGatewayRoute(t, daemon, run.read, session.device, session.learned, true)
		waitTunnelRuntimeFamily(t, daemon, run.read, session.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntimeReceived(t, daemon, run.read, session.router, home)
	}
	for _, session := range sessions {
		ready(session)
	}
	requireBGPRuntimeExchange(t, run, daemon, direct)

	for index, lost := range sessions {
		other := sessions[1-index]
		since := time.Now()
		lost.router.deleteNeighbor(t)
		waitBGPRuntimeSession(t, daemon, run.read, lost.device, "no established state", func(published bgpRuntimeSession) bool {
			return !published.Established && len(published.Accepted) == 0
		})
		waitBGPRuntimeReason(t, daemon, run.read, lost.device, since, bgpRuntimeNotEstablished)
		waitBGPRuntimeGatewayRoute(t, daemon, run.read, lost.device, lost.learned, false)
		waitTunnelRuntimeFamily(t, daemon, run.read, lost.device, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
		requireBGPRepeatedIntact(t, run, other, since)
		requireBGPRuntimeIPv4(t, run, daemon)

		lost.router.addNeighbor(t)
		ready(lost)
		requireBGPRepeatedIntact(t, run, other, since)
	}
	requireBGPRuntimeExchange(t, run, daemon, direct)
}
