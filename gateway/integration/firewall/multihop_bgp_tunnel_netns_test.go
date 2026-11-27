//go:build linux && firewall

package firewall_test

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	bgpMultihopTTL       = 2
	bgpMultihopHoldSlack = 2 * time.Second
)

func TestBGPTunnelMultihopDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPTunnelMultihopDaemonRuntime)
}

func bgpMultihopKernelGateway(t *testing.T, prefix netip.Prefix) string {
	t.Helper()
	link, err := netlink.LinkByName(testenv.TunnelRuntimeDevice)
	if err != nil {
		t.Fatal(err)
	}
	filter := &netlink.Route{
		Table:     unix.RT_TABLE_MAIN,
		Protocol:  unix.RTPROT_BGP,
		LinkIndex: link.Attrs().Index,
	}
	routes, err := netlink.RouteListFiltered(
		netlink.FAMILY_V6,
		filter,
		netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_OIF,
	)
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

func runBGPTunnelMultihopDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(testenv.BgpRuntimeHome)
	gatewayAddress := netip.MustParseAddr(testenv.TunnelRuntimeInnerLocal)
	upstreamAddress := netip.MustParseAddr(testenv.BgpUpstreamAddress)
	topology := testenv.BuildBGPUpstreamRuntime(t, run)
	testenv.SetRuntimeNamespace(t, topology.Endpoint)
	testenv.AddRuntimeDefault(t, testenv.BgpUpstreamVPSDevice, testenv.BgpUpstreamAddress)
	testenv.SetRuntimeNamespace(t, run.Gateway)
	upstream := testenv.StartBGPTransitRouter(
		t,
		topology.Upstream,
		"198.51.100.9",
		testenv.BgpUpstreamASN,
		[]netip.Addr{upstreamAddress},
		testenv.BgpTransitNeighbor{
			Address:     gatewayAddress,
			Asn:         testenv.BgpRuntimeLocalASN,
			MultihopTTL: bgpMultihopTTL,
		},
	)
	upstream.Announce(t, defaultRoute, upstreamAddress)

	session := testenv.BgpTunnelRuntimeSession{
		Peer:      testenv.BgpUpstreamAddress,
		RemoteASN: testenv.BgpUpstreamASN,
		Routed:    testenv.BgpUpstreamNetwork,
	}
	testenv.WriteBGPTunnelRuntimeNetwork(t, run.NetworkDir, session)
	capture := testenv.OpenBGPExpiryCapture(t, topology)
	since := time.Now()
	first := run.Start(t, "bgp-singlehop")
	testenv.WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	testenv.WaitBGPRuntime(
		t,
		first,
		run.Read,
		"a time-exceeded reply at the VPS for the gateway's TCP SYN to the peer port",
		testenv.TunnelRuntimeRecoverWindow,
		func() bool {
			return testenv.BgpExpiryCaptured(t, capture, gatewayAddress, upstreamAddress)
		},
	)
	testenv.WaitBGPRuntimeReason(
		t,
		first,
		run.Read,
		testenv.TunnelRuntimeDevice,
		since,
		testenv.BgpRuntimeNotEstablished,
	)
	if entry, _ := testenv.ReadBGPRuntimeInterface(
		run.Read,
		testenv.TunnelRuntimeDevice,
	); len(entry.V6.Ownership.Sessions) != 1 ||
		entry.V6.Ownership.Sessions[0].Established ||
		len(entry.V6.Ownership.Sessions[0].Accepted) != 0 {
		t.Fatalf(
			"session without multihop-ttl = %+v, want one session without an established state",
			entry.V6.Ownership.Sessions,
		)
	}
	if received := upstream.Received(t, gatewayAddress); len(received) != 0 {
		t.Fatalf("upstream received %v from a session without multihop-ttl", received)
	}
	testenv.WaitTunnelRuntimeFamily(
		t,
		first,
		run.Read,
		testenv.TunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	testenv.KillOwnedRuntimeDaemon(t, first)

	session.MultihopTTL = bgpMultihopTTL
	testenv.WriteBGPTunnelRuntimeNetwork(t, run.NetworkDir, session)
	daemon := run.Start(t, "bgp-multihop")
	exchange := func() {
		t.Helper()
		testenv.WaitBGPRuntimeSession(
			t,
			daemon,
			run.Read,
			testenv.TunnelRuntimeDevice,
			"an established state and the default route",
			func(session testenv.BgpRuntimeSession) bool {
				return session.Established &&
					slices.Equal(session.Accepted, []string{defaultRoute.String()}) &&
					len(session.Rejections) == 0
			},
		)
		testenv.WaitBGPRuntimeGatewayRoute(
			t,
			daemon,
			run.Read,
			testenv.TunnelRuntimeDevice,
			defaultRoute,
			true,
		)
		if gateway := bgpMultihopKernelGateway(
			t,
			defaultRoute,
		); gateway != testenv.TunnelRuntimeInnerRemote {
			t.Fatalf(
				"gateway default route uses the next hop %q, want the tunnel neighbor %s",
				gateway,
				testenv.TunnelRuntimeInnerRemote,
			)
		}
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
		testenv.WaitBGPRuntime(
			t,
			daemon,
			run.Read,
			"the home prefix at the upstream",
			testenv.TunnelRuntimeRecoverWindow,
			func() bool {
				return slices.Equal(upstream.Received(t, gatewayAddress), []netip.Prefix{home}) &&
					testenv.BgpNamespaceRoute(t, upstream.Handle, home)
			},
		)
		testenv.RequireBGPUpstreamExchange(t, daemon, topology)
		testenv.RequireBGPRuntimeIPv4(t, run, daemon)
	}
	exchange()

	for _, change := range []struct {
		present bool
		gateway string
	}{
		{present: true, gateway: testenv.BgpMultihopSecondNeighbor},
		{present: false, gateway: testenv.TunnelRuntimeInnerRemote},
	} {
		testenv.SetBGPMultihopSecondNeighbor(t, change.present)
		testenv.WaitBGPRuntime(
			t,
			daemon,
			run.Read,
			"the default route through "+change.gateway,
			testenv.TunnelRuntimeRecoverWindow,
			func() bool {
				return bgpMultihopKernelGateway(t, defaultRoute) == change.gateway
			},
		)
		testenv.RequireBGPUpstreamExchange(t, daemon, topology)
	}

	since = time.Now()
	testenv.SetTunnelRuntimeFault(t, topology.TunnelRuntimeTopology, true)
	testenv.WaitBGPRuntime(
		t,
		daemon,
		run.Read,
		"upstream without the home prefix",
		testenv.BgpRuntimeHold*time.Second+bgpMultihopHoldSlack,
		func() bool {
			return len(upstream.Received(t, gatewayAddress)) == 0 &&
				!testenv.BgpNamespaceRoute(t, upstream.Handle, home)
		},
	)
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
	testenv.WaitBGPRuntimeReason(
		t,
		daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		since,
		testenv.BgpRuntimeNotEstablished,
	)
	testenv.WaitBGPRuntimeGatewayRoute(
		t,
		daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		defaultRoute,
		false,
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
	testenv.RequireBGPRuntimeIPv4(t, run, daemon)

	testenv.SetTunnelRuntimeFault(t, topology.TunnelRuntimeTopology, false)
	exchange()
	testenv.RequireBGPRuntimeIPv4Unchanged(t, run, since)
}
