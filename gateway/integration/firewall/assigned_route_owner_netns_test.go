//go:build linux && firewall

package firewall_test

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	bgpTableOwnerInternalReturn = "2001:db8:b01:10::/60"
	bgpTableOwnerStaleCopy      = "2001:db8:a:98::/64"
	bgpTableOwnerInternalDevice = "enmwanbr0"
	bgpTableOwnerHoldWindow     = 3 * time.Second
)

// Each provider table needs a return route for internal clients.
func TestBGPTableKeepsInternalReturnRoutesDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPTableOwnerDaemonRuntime)
}

func runBGPTableOwnerDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	provider := testenv.BgpRuntimeDirectProvider(
		testenv.BgpRuntimeLocalASN,
		testenv.BgpRuntimeRemoteASN,
		"2001:db8:a:99::/64",
	)
	run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway, provider)
	testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir, provider)
	daemon := run.Start(t, "bgp-table-owner")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)

	internal := netip.MustParsePrefix(bgpTableOwnerInternalReturn)
	internalGateway := netip.MustParseAddr(testenv.TunnelRuntimeClientV6)
	stale := netip.MustParsePrefix(bgpTableOwnerStaleCopy)
	addBGPTableOwnerRoute(
		t,
		provider.Table,
		internal,
		internalGateway,
		bgpTableOwnerInternalDevice,
		0,
	)
	addBGPTableOwnerRoute(
		t,
		provider.Table,
		stale,
		provider.Peer(),
		provider.Device,
		provider.Metric,
	)
	// Trigger reconciliation with a route event on the internal link.
	addBGPTableOwnerRoute(
		t,
		unix.RT_TABLE_MAIN,
		internal,
		internalGateway,
		bgpTableOwnerInternalDevice,
		0,
	)

	testenv.WaitBGPRuntime(
		t,
		daemon,
		run.Read,
		"removal of the stale copy "+stale.String(),
		testenv.TunnelRuntimeRecoverWindow,
		func() bool {
			return !bgpTableOwnerRoutePresent(t, provider.Table, stale, provider.Device)
		},
	)
	deadline := time.Now().Add(bgpTableOwnerHoldWindow)
	for time.Now().Before(deadline) {
		if !bgpTableOwnerRoutePresent(t, provider.Table, internal, bgpTableOwnerInternalDevice) {
			t.Fatalf(
				"table %d lost the internal return route %s on %s; daemon=%s",
				provider.Table,
				internal,
				bgpTableOwnerInternalDevice,
				testenv.TunnelRuntimeLog(t, daemon),
			)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func addBGPTableOwnerRoute(
	t *testing.T,
	table int,
	prefix netip.Prefix,
	gateway netip.Addr,
	device string,
	metric int,
) {
	t.Helper()
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		Family:    netlink.FAMILY_V6,
		Table:     table,
		Protocol:  unix.RTPROT_BGP,
		Priority:  metric,
		Dst:       &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), 128)},
		Gw:        gateway.AsSlice(),
		LinkIndex: link.Attrs().Index,
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add %s via %s on %s to table %d: %v", prefix, gateway, device, table, err)
	}
}

func bgpTableOwnerRoutePresent(t *testing.T, table int, prefix netip.Prefix, device string) bool {
	t.Helper()
	filter := &netlink.Route{Table: table, Protocol: unix.RTPROT_BGP}
	routes, err := netlink.RouteListFiltered(
		netlink.FAMILY_V6,
		filter,
		netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL,
	)
	if err != nil {
		t.Fatalf("list table %d BGP routes: %v", table, err)
	}
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == prefix.String() &&
			route.LinkIndex == link.Attrs().Index {
			return true
		}
	}
	return false
}
