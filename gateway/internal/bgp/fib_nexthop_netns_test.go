//go:build linux && netns

package bgp_test

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/agoodkind/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
)

const nextHopChildEnv = "MWAN_BGP_NEXT_HOP_CHILD"

func nextHopNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(nextHopChildEnv) == "1" {
		return true
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), nextHopChildEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated next hop test: %v: %s", err, output)
	}
	return false
}

func addNextHopRoute(t *testing.T, link netlink.Link, table int, destination netip.Prefix, gateway netip.Addr) {
	t.Helper()
	network := &net.IPNet{IP: net.IP(destination.Addr().AsSlice()), Mask: net.CIDRMask(destination.Bits(), destination.Addr().BitLen())}
	route := &netlink.Route{Table: table, Dst: network, LinkIndex: link.Attrs().Index}
	if gateway.IsValid() {
		route.Gw = net.IP(gateway.AsSlice())
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add the route %v: %v", route, err)
	}
}

func TestFIBResolvesNextHopOnlyWithOption(t *testing.T) {
	if !nextHopNamespace(t) {
		return
	}
	link := addDummyLink(t, "bgpint0", "2001:db8:ffff::1/64")
	nextHop := netip.MustParseAddr("2001:db8:ffff::2")
	neighbor := netip.MustParseAddr("2001:db8:ffff::3")
	speakerPrefix := netip.MustParsePrefix("2001:db8:5::/48")
	sessionPrefix := netip.MustParsePrefix("2001:db8:6::/48")
	host := netip.PrefixFrom(nextHop, nextHop.BitLen())
	addNextHopRoute(t, link, sessionRouteTable, host, netip.Addr{})
	addNextHopRoute(t, link, unix.RT_TABLE_MAIN, host, neighbor)

	speakerFIB := bgp.NewFIB(bgp.FIBConfig{Tables: []int{sessionRouteTable}, InternalIface: link.Attrs().Name}, sessionLogger())
	applyFIB(t, speakerFIB, bgp.PathEvent{Peer: "internal", Prefix: speakerPrefix, NextHop: nextHop})
	assertKernelRoute(t, speakerPrefix, link, nextHop, kernelDefaultIPv6Metric)

	sessionFIB := bgp.NewFIB(bgp.FIBConfig{
		Tables: []int{sessionRouteTable}, InternalIface: link.Attrs().Name, Metric: sessionRouteMetric, ResolveNextHop: true,
	}, sessionLogger())
	applyFIB(t, sessionFIB, bgp.PathEvent{Peer: "external", Prefix: sessionPrefix, NextHop: nextHop})
	assertKernelRoute(t, sessionPrefix, link, neighbor, sessionRouteMetric)
}

func TestFIBRefreshKeepsCoveringRouteAndRestoresMissingRoute(t *testing.T) {
	if !nextHopNamespace(t) {
		return
	}
	link := addDummyLink(t, "bgpint0", "2001:db8:ffff::1/64")
	neighbor := netip.MustParseAddr("2001:db8:ffff::3")
	covering := netip.MustParsePrefix("2001:db8:50::/64")
	nextHop := netip.MustParseAddr("2001:db8:50::1")
	addNextHopRoute(t, link, unix.RT_TABLE_MAIN, covering, neighbor)
	learnedFilter := &netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_BGP}
	fib := bgp.NewFIB(bgp.FIBConfig{
		Tables: []int{unix.RT_TABLE_MAIN}, InternalIface: link.Attrs().Name, Metric: sessionRouteMetric, ResolveNextHop: true,
	}, sessionLogger())
	applyFIB(t, fib, bgp.PathEvent{Peer: "external", Prefix: covering, NextHop: nextHop})
	refresh := func() {
		t.Helper()
		rejected, err := fib.Refresh(t.Context())
		if err != nil || len(rejected) != 0 {
			t.Fatalf("refresh rejected %v with error %v, want no rejection", rejected, err)
		}
		routes, err := netlink.RouteListFiltered(unix.AF_INET6, learnedFilter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
		if err != nil || len(routes) != 1 || !routes[0].Gw.Equal(net.IP(neighbor.AsSlice())) || routes[0].Priority != sessionRouteMetric {
			t.Fatalf("BGP routes = %v (%v), want one route to %s through %s", routes, err, covering, neighbor)
		}
	}
	refresh()
	refresh()

	learned, err := netlink.RouteListFiltered(unix.AF_INET6, learnedFilter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil || len(learned) != 1 {
		t.Fatalf("BGP routes = %v (%v), want one route", learned, err)
	}
	if err := netlink.RouteDel(&learned[0]); err != nil {
		t.Fatalf("delete the learned route: %v", err)
	}
	refresh()
}
