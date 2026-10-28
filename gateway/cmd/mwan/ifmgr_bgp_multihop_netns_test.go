//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	bgpMultihopTTL       = 2
	bgpMultihopHoldSlack = 2 * time.Second
)

func TestBGPTunnelMultihopDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPTunnelMultihopDaemonRuntime)
}

func bgpMultihopKernelGateway(t *testing.T, prefix netip.Prefix) string {
	t.Helper()
	link, err := netlink.LinkByName(tunnelRuntimeDevice)
	if err != nil {
		t.Fatal(err)
	}
	filter := &netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_BGP, LinkIndex: link.Attrs().Index}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, filter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_OIF)
	if err != nil {
		t.Fatalf("list the gateway BGP routes: %v", err)
	}
	for _, route := range routes {
		destination := "::/0"
		if route.Dst != nil {
			destination = route.Dst.String()
		}
		if destination == prefix.String() {
			return route.Gw.String()
		}
	}
	return ""
}

func runBGPTunnelMultihopDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(bgpRuntimeHome)
	gatewayAddress := netip.MustParseAddr(tunnelRuntimeInnerLocal)
	upstreamAddress := netip.MustParseAddr(bgpUpstreamAddress)
	topology := buildBGPUpstreamRuntime(t, run)
	setRuntimeNamespace(t, topology.endpoint)
	addRuntimeDefault(t, bgpUpstreamVPSDevice, bgpUpstreamAddress)
	setRuntimeNamespace(t, run.gateway)
	upstream := startBGPTransitRouter(t, topology.upstream, "198.51.100.9", bgpUpstreamASN, []netip.Addr{upstreamAddress},
		bgpTransitNeighbor{address: gatewayAddress, asn: bgpRuntimeLocalASN, multihopTTL: bgpMultihopTTL})
	upstream.announce(t, defaultRoute, upstreamAddress)

	session := bgpTunnelRuntimeSession{peer: bgpUpstreamAddress, remoteASN: bgpUpstreamASN, routed: bgpUpstreamNetwork}
	writeBGPTunnelRuntimeNetwork(t, run.networkDir, session)
	capture := openBGPExpiryCapture(t, topology)
	since := time.Now()
	first := run.start(t, "bgp-singlehop")
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	waitBGPRuntime(t, first, run.read, "a time-exceeded reply at the VPS for the gateway's TCP SYN to the peer port", tunnelRuntimeRecoverWindow, func() bool {
		return bgpExpiryCaptured(t, capture, gatewayAddress, upstreamAddress)
	})
	waitBGPRuntimeReason(t, first, run.read, tunnelRuntimeDevice, since, bgpRuntimeNotEstablished)
	if entry, _ := readBGPRuntimeInterface(run.read, tunnelRuntimeDevice); len(entry.V6.Ownership.Sessions) != 1 ||
		entry.V6.Ownership.Sessions[0].Established || len(entry.V6.Ownership.Sessions[0].Accepted) != 0 {
		t.Fatalf("session without multihop-ttl = %+v, want one session without an established state", entry.V6.Ownership.Sessions)
	}
	if received := upstream.received(t, gatewayAddress); len(received) != 0 {
		t.Fatalf("upstream received %v from a session without multihop-ttl", received)
	}
	waitTunnelRuntimeFamily(t, first, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	killOwnedRuntimeDaemon(t, first)

	session.multihopTTL = bgpMultihopTTL
	writeBGPTunnelRuntimeNetwork(t, run.networkDir, session)
	daemon := run.start(t, "bgp-multihop")
	exchange := func() {
		t.Helper()
		waitBGPRuntimeSession(t, daemon, run.read, tunnelRuntimeDevice, "an established state and the default route", func(session bgpRuntimeSession) bool {
			return session.Established && slices.Equal(session.Accepted, []string{defaultRoute.String()}) && len(session.Rejections) == 0
		})
		waitBGPRuntimeGatewayRoute(t, daemon, run.read, tunnelRuntimeDevice, defaultRoute, true)
		if gateway := bgpMultihopKernelGateway(t, defaultRoute); gateway != tunnelRuntimeInnerRemote {
			t.Fatalf("gateway default route uses the next hop %q, want the tunnel neighbor %s", gateway, tunnelRuntimeInnerRemote)
		}
		waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntime(t, daemon, run.read, "the home prefix at the upstream", tunnelRuntimeRecoverWindow, func() bool {
			return slices.Equal(upstream.received(t, gatewayAddress), []netip.Prefix{home}) && bgpNamespaceRoute(t, upstream.handle, home)
		})
		requireBGPUpstreamExchange(t, daemon, topology)
		requireBGPRuntimeIPv4(t, run, daemon)
	}
	exchange()

	for _, change := range []struct {
		present bool
		gateway string
	}{{present: true, gateway: bgpMultihopSecondNeighbor}, {present: false, gateway: tunnelRuntimeInnerRemote}} {
		setBGPMultihopSecondNeighbor(t, change.present)
		waitBGPRuntime(t, daemon, run.read, "the default route through "+change.gateway, tunnelRuntimeRecoverWindow, func() bool {
			return bgpMultihopKernelGateway(t, defaultRoute) == change.gateway
		})
		requireBGPUpstreamExchange(t, daemon, topology)
	}

	since = time.Now()
	setTunnelRuntimeFault(t, topology.tunnelRuntimeTopology, true)
	waitBGPRuntime(t, daemon, run.read, "upstream without the home prefix", bgpRuntimeHold*time.Second+bgpMultihopHoldSlack, func() bool {
		return len(upstream.received(t, gatewayAddress)) == 0 && !bgpNamespaceRoute(t, upstream.handle, home)
	})
	waitBGPRuntimeSession(t, daemon, run.read, tunnelRuntimeDevice, "no established state", func(session bgpRuntimeSession) bool {
		return !session.Established && len(session.Accepted) == 0
	})
	waitBGPRuntimeReason(t, daemon, run.read, tunnelRuntimeDevice, since, bgpRuntimeNotEstablished)
	waitBGPRuntimeGatewayRoute(t, daemon, run.read, tunnelRuntimeDevice, defaultRoute, false)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	requireBGPRuntimeIPv4(t, run, daemon)

	setTunnelRuntimeFault(t, topology.tunnelRuntimeTopology, false)
	exchange()
	requireBGPRuntimeIPv4Unchanged(t, run, since)
}
