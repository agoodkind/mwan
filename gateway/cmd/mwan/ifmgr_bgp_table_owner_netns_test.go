//go:build linux && firewallnetns

package main

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"golang.org/x/sys/unix"
)

const (
	bgpTableOwnerInternalReturn = "2001:db8:b01:10::/60"
	bgpTableOwnerStaleCopy      = "2001:db8:a:98::/64"
	bgpTableOwnerInternalDevice = "enmwanbr0"
	bgpTableOwnerHoldWindow     = 3 * time.Second
)

// The agent BGP route installer needs return routes in every provider table
// for replies to internal clients.
func TestBGPTableKeepsInternalReturnRoutesDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPTableOwnerDaemonRuntime)
}

func runBGPTableOwnerDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	provider := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, "2001:db8:a:99::/64")
	run.topology = buildBGPRuntimeTopology(t, run.gateway, provider)
	writeBGPRuntimeNetwork(t, run.networkDir, provider)
	daemon := run.start(t, "bgp-table-owner")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)

	internal := netip.MustParsePrefix(bgpTableOwnerInternalReturn)
	internalGateway := netip.MustParseAddr(tunnelRuntimeClientV6)
	stale := netip.MustParsePrefix(bgpTableOwnerStaleCopy)
	addBGPTableOwnerRoute(t, provider.table, internal, internalGateway, bgpTableOwnerInternalDevice, 0)
	addBGPTableOwnerRoute(t, provider.table, stale, provider.peer(), provider.device, provider.metric)
	// A main-table route event on the internal interface requests a reconcile pass.
	addBGPTableOwnerRoute(t, unix.RT_TABLE_MAIN, internal, internalGateway, bgpTableOwnerInternalDevice, 0)

	waitBGPRuntime(t, daemon, run.read, "removal of the stale copy "+stale.String(), tunnelRuntimeRecoverWindow, func() bool {
		return !bgpTableOwnerRoutePresent(t, provider.table, stale, provider.device)
	})
	deadline := time.Now().Add(bgpTableOwnerHoldWindow)
	for time.Now().Before(deadline) {
		if !bgpTableOwnerRoutePresent(t, provider.table, internal, bgpTableOwnerInternalDevice) {
			t.Fatalf("table %d lost the internal return route %s on %s; daemon=%s",
				provider.table, internal, bgpTableOwnerInternalDevice, tunnelRuntimeLog(t, daemon))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func addBGPTableOwnerRoute(t *testing.T, table int, prefix netip.Prefix, gateway netip.Addr, device string, metric int) {
	t.Helper()
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		Family: netlink.FAMILY_V6, Table: table, Protocol: unix.RTPROT_BGP, Priority: metric,
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
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, filter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		t.Fatalf("list table %d BGP routes: %v", table, err)
	}
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == prefix.String() && route.LinkIndex == link.Attrs().Index {
			return true
		}
	}
	return false
}
