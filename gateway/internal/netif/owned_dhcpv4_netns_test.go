//go:build linux && netns

package netif

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestOwnedDHCPv4Lifecycle(t *testing.T) {
	const childEnv = "MWAN_OWNED_DHCPV4_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedDHCPv4Lifecycle$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated DHCPv4 ownership test: %v: %s", err, output)
		}
		return
	}

	const iface = "owned-dhcp4"
	const tableID = 500
	const metric = 398
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: iface}}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	foreignAddress, err := netlink.ParseAddr("198.51.100.99/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, foreignAddress); err != nil {
		t.Fatal(err)
	}
	_, foreignDestination, err := net.ParseCIDR("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	foreignRoute := &netlink.Route{
		LinkIndex: link.Attrs().Index, Table: tableID,
		Dst: foreignDestination, Priority: metric, Protocol: unix.RTPROT_STATIC,
	}
	if err := netlink.RouteAdd(foreignRoute); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(t.TempDir(), "dhcpv4-ownership.json")
	reconciler, err := NewOwnedDHCPv4Reconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defaultDestination := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	_, classlessDestination, err := net.ParseCIDR("203.0.114.1/32")
	if err != nil {
		t.Fatal(err)
	}
	_, connectedDestination, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	_, downstreamDestination, err := net.ParseCIDR("198.51.101.0/24")
	if err != nil {
		t.Fatal(err)
	}
	lease := &LeaseInfo{
		State: LeaseBound, IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24,
		Routes: []LeaseRoute{
			{Destination: defaultDestination, Gateway: net.IPv4(203, 0, 114, 1)},
			{Destination: downstreamDestination, Gateway: net.IPv4(203, 0, 114, 1)},
			{Destination: classlessDestination, Gateway: net.IPv4zero},
			{Destination: connectedDestination, Gateway: net.IPv4zero},
		},
	}
	reconcile := func(current *LeaseInfo) error {
		t.Helper()
		return reconciler.Reconcile(context.Background(), "test-oob-table", "connection-1", iface, tableID, metric, current)
	}
	conflictingAddress, err := netlink.ParseAddr("192.0.2.8/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, conflictingAddress); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(lease); err == nil {
		t.Fatal("foreign address was accepted")
	}
	if err := netlink.AddrDel(link, conflictingAddress); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(lease); err != nil {
		t.Fatal(err)
	}
	assertDHCPv4Objects(t, link, tableID, metric, "192.0.2.8/24", "203.0.114.1", 3)
	if err := reconcile(&LeaseInfo{State: LeaseRenewing}); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(&LeaseInfo{State: LeaseRebinding}); err != nil {
		t.Fatal(err)
	}
	assertDHCPv4Objects(t, link, tableID, metric, "192.0.2.8/24", "203.0.114.1", 3)

	lease.IP = net.IPv4(192, 0, 2, 9)
	lease.Routes[0].Gateway = net.IPv4(203, 0, 114, 2)
	lease.Routes[1].Gateway = net.IPv4(203, 0, 114, 2)
	_, lease.Routes[2].Destination, err = net.ParseCIDR("203.0.114.2/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcile(lease); err != nil {
		t.Fatal(err)
	}
	assertDHCPv4Objects(t, link, tableID, metric, "192.0.2.9/24", "203.0.114.2", 3)
	reconciler, err = NewOwnedDHCPv4Reconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcile(nil); err != nil {
		t.Fatal(err)
	}
	assertDHCPv4Objects(t, link, tableID, metric, "", "", 0)
	mainLease := &LeaseInfo{
		State: LeaseBound, IP: net.IPv4(192, 0, 2, 10), PrefixLen: 24,
		Routes: []LeaseRoute{{Destination: defaultDestination, Gateway: net.IPv4(192, 0, 2, 1)}},
	}
	mainReconcile := func(current *LeaseInfo) error {
		t.Helper()
		return reconciler.Reconcile(context.Background(), "test-main-table", "connection-2", iface, unix.RT_TABLE_MAIN, metric, current)
	}
	if err := mainReconcile(mainLease); err != nil {
		t.Fatal(err)
	}
	mainRoutes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	foundMain := false
	for _, route := range mainRoutes {
		if route.Protocol == OwnedDHCPv4RouteProtocol && route.Priority == metric && route.Gw.Equal(net.IPv4(192, 0, 2, 1)) {
			foundMain = true
		}
	}
	if !foundMain {
		t.Fatal("main-table DHCP default route missing")
	}
	if err := mainReconcile(&LeaseInfo{State: LeaseExpired}); err != nil {
		t.Fatal(err)
	}
	mainRoutes, err = netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range mainRoutes {
		if route.Protocol == OwnedDHCPv4RouteProtocol && route.Priority == metric {
			t.Fatalf("expired main-table route remains: %+v", route)
		}
	}

	conflict := &netlink.Route{
		LinkIndex: link.Attrs().Index, Table: tableID,
		Dst: defaultDestination, Priority: metric, Protocol: unix.RTPROT_STATIC,
	}
	if err := netlink.RouteAdd(conflict); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(lease); err == nil {
		t.Fatal("foreign default route was overwritten")
	}
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	foundForeign := false
	for _, route := range routes {
		if route.Priority == metric && route.Dst != nil && route.Dst.String() == "0.0.0.0/0" &&
			route.Gw == nil && route.Protocol == unix.RTPROT_STATIC {
			foundForeign = true
		}
	}
	if !foundForeign {
		t.Fatal("foreign default route changed")
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IP.Equal(net.IPv4(192, 0, 2, 9)) || address.IP.Equal(net.IPv4(192, 0, 2, 10)) {
			t.Fatalf("expired DHCP address remains: %v", address)
		}
	}
}

func assertDHCPv4Objects(t *testing.T, link netlink.Link, tableID, metric int, prefix, gateway string, routeCount int) {
	t.Helper()
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	foundOwned := prefix == ""
	foundForeign := false
	for _, address := range addresses {
		if address.IPNet.String() == prefix {
			foundOwned = true
		}
		if address.IPNet.String() == "198.51.100.99/32" {
			foundForeign = true
		}
	}
	if !foundOwned || !foundForeign {
		t.Fatalf("addresses = %v, want owned %q and foreign address", addresses, prefix)
	}
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	ownedCount := 0
	foreignCount := 0
	for _, route := range routes {
		if route.Protocol == OwnedDHCPv4RouteProtocol && route.Priority == metric {
			ownedCount++
			if (route.Dst == nil || route.Dst.String() == "0.0.0.0/0") && !route.Gw.Equal(net.ParseIP(gateway)) {
				t.Fatalf("default route = %+v, want gateway %s", route, gateway)
			}
			if route.Dst != nil && route.Dst.String() != "0.0.0.0/0" && route.Gw == nil && route.Scope != netlink.SCOPE_LINK {
				t.Fatalf("on-link route lacks link scope: %+v", route)
			}
		}
		if route.Dst != nil && route.Dst.String() == "203.0.113.0/24" {
			foreignCount++
		}
	}
	if ownedCount != routeCount || foreignCount != 1 {
		t.Fatalf("routes = %v, want %d owned and one foreign", routes, routeCount)
	}
}
