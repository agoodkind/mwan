//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TestBGPTunnelVPSPeerDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPTunnelVPSPeerDaemonRuntime)
}

func runBGPTunnelVPSPeerDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	undeclared := netip.MustParsePrefix(bgpRuntimeUndeclared)
	home := netip.MustParsePrefix(bgpRuntimeHome)
	gatewayAddress := netip.MustParseAddr(tunnelRuntimeInnerLocal)
	vpsTunnel := netip.MustParseAddr(tunnelRuntimeInnerRemote)
	vpsUpstream := netip.MustParseAddr(bgpUpstreamVPSAddress)
	upstreamAddress := netip.MustParseAddr(bgpUpstreamAddress)
	topology := buildBGPUpstreamRuntime(t, run)
	removeTunnelRuntimeReturnRoute(t, topology.tunnelRuntimeTopology)
	writeBGPTunnelRuntimeNetwork(t, run.networkDir, bgpTunnelRuntimeSession{peer: tunnelRuntimeInnerRemote, remoteASN: bgpRuntimeRemoteASN})
	daemon := run.start(t, "bgp-vps")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	requireBGPRuntimeIPv4(t, run, daemon)

	vps := startBGPTransitRouter(t, topology.endpoint, "198.51.100.1", bgpRuntimeRemoteASN, []netip.Addr{vpsTunnel, vpsUpstream},
		bgpTransitNeighbor{address: gatewayAddress, asn: bgpRuntimeLocalASN},
		bgpTransitNeighbor{address: upstreamAddress, asn: bgpUpstreamASN})
	startBGPUpstreamProcess(t, topology.upstream, run.gateway)
	upstream, err := netlink.NewHandleAt(topology.upstream)
	if err != nil {
		t.Fatalf("open the upstream namespace: %v", err)
	}
	t.Cleanup(func() { _ = upstream.Close() })

	exchange := func() {
		t.Helper()
		session := waitBGPRuntimeSession(t, daemon, run.read, tunnelRuntimeDevice, "the default route and one rejection", func(session bgpRuntimeSession) bool {
			return session.Established && slices.Equal(session.Accepted, []string{defaultRoute.String()}) && len(session.Rejections) == 1
		})
		if rejection := session.Rejections[0]; rejection.Prefix != undeclared.String() || rejection.Reason != bgpRuntimeLengthBounds {
			t.Fatalf("published rejection = %+v, want %s with reason %q", rejection, undeclared, bgpRuntimeLengthBounds)
		}
		waitBGPRuntimeGatewayRoute(t, daemon, run.read, tunnelRuntimeDevice, defaultRoute, true)
		if bgpRuntimeGatewayRoute(t, tunnelRuntimeDevice, undeclared) {
			t.Fatalf("gateway installed the undeclared prefix %s", undeclared)
		}
		waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntime(t, daemon, run.read, "the home prefix at the VPS and at the upstream", tunnelRuntimeRecoverWindow, func() bool {
			return slices.Equal(vps.received(t, gatewayAddress), []netip.Prefix{home}) && bgpNamespaceRoute(t, upstream, home)
		})
		if received := vps.received(t, upstreamAddress); !slices.Equal(received, []netip.Prefix{defaultRoute, undeclared}) {
			t.Fatalf("VPS received %v from the upstream, want %s and %s", received, defaultRoute, undeclared)
		}
		requireBGPUpstreamExchange(t, daemon, topology)
		requireBGPRuntimeIPv4(t, run, daemon)
	}
	exchange()

	since := time.Now()
	setTunnelRuntimeFault(t, topology.tunnelRuntimeTopology, true)
	waitBGPRuntime(t, daemon, run.read, "upstream without the home prefix", tunnelRuntimeRecoverWindow, func() bool {
		return len(vps.received(t, gatewayAddress)) == 0 && !bgpNamespaceRoute(t, upstream, home)
	})
	waitBGPRuntimeSession(t, daemon, run.read, tunnelRuntimeDevice, "no established state", func(session bgpRuntimeSession) bool {
		return !session.Established && len(session.Accepted) == 0
	})
	waitBGPRuntimeReason(t, daemon, run.read, tunnelRuntimeDevice, since, bgpRuntimeNotEstablished)
	waitBGPRuntimeGatewayRoute(t, daemon, run.read, tunnelRuntimeDevice, defaultRoute, false)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	if !bgpNamespaceRoute(t, vps.handle, defaultRoute) {
		t.Fatalf("VPS has no BGP default route from the upstream during the tunnel loss")
	}
	requireBGPRuntimeIPv4(t, run, daemon)

	setTunnelRuntimeFault(t, topology.tunnelRuntimeTopology, false)
	exchange()
	requireBGPRuntimeIPv4Unchanged(t, run, since)
}
