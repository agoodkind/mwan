//go:build linux && netns

package bgp_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
)

const (
	sessionRouteTable  = 100
	sessionRouteMetric = 50
	// The kernel stores an IPv6 route added without a metric at metric 1024.
	kernelDefaultIPv6Metric = 1024
)

func addDummyLink(t *testing.T, name string, addresses ...string) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		t.Fatalf("add link %s: %v", name, err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("find link %s: %v", name, err)
	}
	for _, address := range addresses {
		parsed, err := netlink.ParseAddr(address)
		if err != nil {
			t.Fatalf("parse address %s: %v", address, err)
		}
		if parsed.IP.To4() == nil {
			// A tentative address cannot be a TCP source address. Duplicate address
			// detection maintains a new address's tentative state.
			parsed.Flags = unix.IFA_F_NODAD
		}
		if err := netlink.AddrAdd(link, parsed); err != nil {
			t.Fatalf("add address %s to %s: %v", address, name, err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("set link %s up: %v", name, err)
	}
	return link
}

// The kernel reports the default route without a destination.
func kernelRoute(t *testing.T, prefix netip.Prefix, link netlink.Link) (netlink.Route, bool) {
	t.Helper()
	family := unix.AF_INET6
	defaultDestination := "::/0"
	if prefix.Addr().Is4() {
		family = unix.AF_INET
		defaultDestination = "0.0.0.0/0"
	}
	filter := &netlink.Route{Table: sessionRouteTable}
	routes, err := netlink.RouteListFiltered(family, filter, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list table %d routes: %v", sessionRouteTable, err)
	}
	for _, route := range routes {
		destination := defaultDestination
		if route.Dst != nil {
			destination = route.Dst.String()
		}
		if destination == prefix.String() && route.LinkIndex == link.Attrs().Index {
			return route, true
		}
	}
	return netlink.Route{}, false
}

func hasKernelRoute(t *testing.T, prefix netip.Prefix, link netlink.Link) bool {
	t.Helper()
	_, found := kernelRoute(t, prefix, link)
	return found
}

func assertKernelRoute(t *testing.T, prefix netip.Prefix, link netlink.Link, gateway netip.Addr, metric int) {
	t.Helper()
	route, found := kernelRoute(t, prefix, link)
	if !found {
		t.Fatalf("kernel route for %s on %s is missing", prefix, link.Attrs().Name)
	}
	if route.Protocol != unix.RTPROT_BGP {
		t.Errorf("route %s protocol = %d, want %d", prefix, route.Protocol, unix.RTPROT_BGP)
	}
	if !route.Gw.Equal(net.IP(gateway.AsSlice())) {
		t.Errorf("route %s gateway = %s, want %s", prefix, route.Gw, gateway)
	}
	if route.Priority != metric {
		t.Errorf("route %s metric = %d, want %d", prefix, route.Priority, metric)
	}
}

func assertNoKernelRoute(t *testing.T, prefix netip.Prefix, link netlink.Link) {
	t.Helper()
	if route, found := kernelRoute(t, prefix, link); found {
		t.Fatalf("kernel route for %s on %s = %v, want none", prefix, link.Attrs().Name, route)
	}
}

func applyFIB(t *testing.T, fib *bgp.FIB, event bgp.PathEvent) {
	t.Helper()
	if err := fib.Apply(t.Context(), event); err != nil {
		t.Fatalf("apply %+v: %v", event, err)
	}
}
