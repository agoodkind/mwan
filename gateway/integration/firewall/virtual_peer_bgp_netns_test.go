//go:build linux && firewall

package firewall_test

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
)

func TestBGPTunnelVPSPeerDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPTunnelVPSPeerDaemonRuntime)
}

func runBGPTunnelVPSPeerDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	undeclared := netip.MustParsePrefix(testenv.BgpRuntimeUndeclared)
	home := netip.MustParsePrefix(testenv.BgpRuntimeHome)
	gatewayAddress := netip.MustParseAddr(testenv.TunnelRuntimeInnerLocal)
	vpsTunnel := netip.MustParseAddr(testenv.TunnelRuntimeInnerRemote)
	vpsUpstream := netip.MustParseAddr(testenv.BgpUpstreamVPSAddress)
	upstreamAddress := netip.MustParseAddr(testenv.BgpUpstreamAddress)
	topology := testenv.BuildBGPUpstreamRuntime(t, run)
	testenv.RemoveTunnelRuntimeReturnRoute(t, topology.TunnelRuntimeTopology)
	testenv.WriteBGPTunnelRuntimeNetwork(
		t,
		run.NetworkDir,
		testenv.BgpTunnelRuntimeSession{
			Peer:      testenv.TunnelRuntimeInnerRemote,
			RemoteASN: testenv.BgpRuntimeRemoteASN,
		},
	)
	daemon := run.Start(t, "bgp-vps")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	testenv.RequireBGPRuntimeIPv4(t, run, daemon)

	vps := testenv.StartBGPTransitRouter(
		t,
		topology.Endpoint,
		"198.51.100.1",
		testenv.BgpRuntimeRemoteASN,
		[]netip.Addr{vpsTunnel, vpsUpstream},
		testenv.BgpTransitNeighbor{Address: gatewayAddress, Asn: testenv.BgpRuntimeLocalASN},
		testenv.BgpTransitNeighbor{Address: upstreamAddress, Asn: testenv.BgpUpstreamASN},
	)
	testenv.StartBGPUpstreamProcess(t, topology.Upstream, run.Gateway)
	upstream, err := netlink.NewHandleAt(topology.Upstream)
	if err != nil {
		t.Fatalf("open the upstream namespace: %v", err)
	}
	t.Cleanup(func() { _ = upstream.Close() })

	exchange := func() {
		t.Helper()
		session := testenv.WaitBGPRuntimeSession(
			t,
			daemon,
			run.Read,
			testenv.TunnelRuntimeDevice,
			"the default route and one rejection",
			func(session testenv.BgpRuntimeSession) bool {
				return session.Established &&
					slices.Equal(session.Accepted, []string{defaultRoute.String()}) &&
					len(session.Rejections) == 1
			},
		)
		if rejection := session.Rejections[0]; rejection.Prefix != undeclared.String() ||
			rejection.Reason != testenv.BgpRuntimeLengthBounds {
			t.Fatalf(
				"published rejection = %+v, want %s with reason %q",
				rejection,
				undeclared,
				testenv.BgpRuntimeLengthBounds,
			)
		}
		testenv.WaitBGPRuntimeGatewayRoute(
			t,
			daemon,
			run.Read,
			testenv.TunnelRuntimeDevice,
			defaultRoute,
			true,
		)
		if testenv.BgpRuntimeGatewayRoute(t, testenv.TunnelRuntimeDevice, undeclared) {
			t.Fatalf("gateway installed the undeclared prefix %s", undeclared)
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
			"the home prefix at the VPS and at the upstream",
			testenv.TunnelRuntimeRecoverWindow,
			func() bool {
				return slices.Equal(vps.Received(t, gatewayAddress), []netip.Prefix{home}) &&
					testenv.BgpNamespaceRoute(t, upstream, home)
			},
		)
		if received := vps.Received(t, upstreamAddress); !slices.Equal(
			received,
			[]netip.Prefix{defaultRoute, undeclared},
		) {
			t.Fatalf(
				"VPS received %v from the upstream, want %s and %s",
				received,
				defaultRoute,
				undeclared,
			)
		}
		testenv.RequireBGPUpstreamExchange(t, daemon, topology)
		testenv.RequireBGPRuntimeIPv4(t, run, daemon)
	}
	exchange()

	since := time.Now()
	testenv.SetTunnelRuntimeFault(t, topology.TunnelRuntimeTopology, true)
	testenv.WaitBGPRuntime(
		t,
		daemon,
		run.Read,
		"upstream without the home prefix",
		testenv.TunnelRuntimeRecoverWindow,
		func() bool {
			return len(vps.Received(t, gatewayAddress)) == 0 &&
				!testenv.BgpNamespaceRoute(t, upstream, home)
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
	if !testenv.BgpNamespaceRoute(t, vps.Handle, defaultRoute) {
		t.Fatalf("VPS has no BGP default route from the upstream during the tunnel loss")
	}
	testenv.RequireBGPRuntimeIPv4(t, run, daemon)

	testenv.SetTunnelRuntimeFault(t, topology.TunnelRuntimeTopology, false)
	exchange()
	testenv.RequireBGPRuntimeIPv4Unchanged(t, run, since)
}
