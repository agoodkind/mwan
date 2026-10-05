//go:build linux && netns

package oobv4

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

func TestOOBDHCPv4AssignmentLifecycle(t *testing.T) {
	const childEnv = "MWAN_OOB_DHCPV4_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOOBDHCPv4AssignmentLifecycle$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated OOB DHCPv4 test: %v: %s", err, output)
		}
		return
	}
	const iface = "oob-dhcp4"
	const tableID = 500
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := netif.StartDHCPClient(ctx, slog.Default(), netif.DHCPConfig{
		Iface:           iface,
		DiscoverTimeout: time.Minute,
	})
	stateFile := filepath.Join(t.TempDir(), "oob-dhcpv4.json")
	moduleInterface, err := New(Config{
		Iface: iface, OOBTableID: tableID,
		StateFile: stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	module := moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client}); err != nil {
		t.Fatal(err)
	}
	lease := netif.LeaseInfo{
		State:     netif.LeaseBound,
		LinkIndex: link.Attrs().Index, LinkHardwareAddr: append(net.HardwareAddr(nil), link.Attrs().HardwareAddr...),
		IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24,
		Routes: []netif.LeaseRoute{{
			Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			Gateway:     net.IPv4(192, 0, 2, 1),
		}},
	}
	legacyAddress, err := netlink.ParseAddr("192.0.2.8/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, legacyAddress); err != nil {
		t.Fatal(err)
	}
	legacyRoute := &netlink.Route{
		LinkIndex: link.Attrs().Index, Table: tableID, Family: unix.AF_INET,
		Priority: 0, Protocol: 99, Gw: net.IPv4(192, 0, 2, 254),
	}
	if err := netlink.RouteAdd(legacyRoute); err != nil {
		t.Fatal(err)
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err == nil {
		t.Fatal("foreign default route was adopted")
	}
	if err := netlink.RouteDel(legacyRoute); err != nil {
		t.Fatal(err)
	}
	legacySpec := netif.RouteSpec{
		Family: "inet", Dest: "default", Via: "192.0.2.254", Dev: iface,
		TableID: tableID, Metric: 0, Protocol: 0,
	}
	if err := netif.ReconcileTableDefault(ctx, slog.Default(), legacySpec); err != nil {
		t.Fatal(err)
	}
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(routes, func(route netlink.Route) bool {
		return route.Protocol == unix.RTPROT_BOOT && route.LinkIndex == link.Attrs().Index &&
			route.Priority == 0 && route.Gw.Equal(net.IPv4(192, 0, 2, 254))
	}) {
		t.Fatal("old route writer did not create the expected kernel route")
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, true)
	routes, err = netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(routes, func(route netlink.Route) bool { return route.Protocol == unix.RTPROT_BOOT }) {
		t.Fatal("legacy default route remained after replacement")
	}
	recoveryInterface, err := New(Config{Iface: iface, OOBTableID: tableID, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	recoveryModule := recoveryInterface.(*Module)
	if err := recoveryModule.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client, DHCPRecoveryPending: true}); err != nil {
		t.Fatal(err)
	}
	startupExpiry := netif.LeaseInfo{State: netif.LeaseExpired}
	if err := recoveryModule.OnDHCPLease(ctx, slog.Default(), startupExpiry); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, true)
	moduleInterface, err = New(Config{Iface: iface, OOBTableID: tableID, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module = moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client}); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, false)
	if err := netlink.AddrAdd(link, legacyAddress); err != nil {
		t.Fatal(err)
	}
	if err := netif.ReconcileTableDefault(ctx, slog.Default(), legacySpec); err != nil {
		t.Fatal(err)
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err == nil {
		t.Fatal("legacy adoption repeated after journal migration")
	}
	legacySpec.Via = ""
	if err := netif.ReconcileTableDefault(ctx, slog.Default(), legacySpec); err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(link, legacyAddress); err != nil {
		t.Fatal(err)
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, true)
	lease.State = netif.LeaseRenewing
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, true)
	lease.State = netif.LeaseExpired
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, false)
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "index-spacer"}}); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: iface}}); err != nil {
		t.Fatal(err)
	}
	link, err = netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	lease.State = netif.LeaseBound
	staleLease := lease
	if link.Attrs().Index == staleLease.LinkIndex {
		t.Fatal("replacement reused the original link index")
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, false)
	lease.LinkIndex = link.Attrs().Index
	lease.LinkHardwareAddr = append(net.HardwareAddr(nil), link.Attrs().HardwareAddr...)
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, true)
	staleLease.State = netif.LeaseExpired
	if err := module.OnDHCPLease(ctx, slog.Default(), staleLease); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, true)
	foreign := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(198, 51, 100, 8), Mask: net.CIDRMask(24, 32)}}
	if err := netlink.AddrAdd(link, foreign); err != nil {
		t.Fatal(err)
	}
	const newIface = "oob-new"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: newIface}}); err != nil {
		t.Fatal(err)
	}
	const otherIface = "other-dhcp4"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: otherIface}}); err != nil {
		t.Fatal(err)
	}
	otherLink, err := netlink.LinkByName(otherIface)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(otherLink); err != nil {
		t.Fatal(err)
	}
	otherLease := lease
	otherLease.IP = net.IPv4(203, 0, 113, 8)
	otherLease.Routes = []netif.LeaseRoute{{
		Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Gateway:     net.IPv4(203, 0, 113, 1),
	}}
	journal, err := netif.NewOwnedDHCPv4Reconciler(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Reconcile(ctx, "other", otherIface, otherIface, tableID+1, 0, &otherLease); err != nil {
		t.Fatal(err)
	}
	newClient := netif.StartDHCPClient(ctx, slog.Default(), netif.DHCPConfig{
		Iface:           newIface,
		DiscoverTimeout: time.Minute,
	})
	moduleInterface, err = New(Config{Iface: newIface, OOBTableID: tableID, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module = moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: newIface, Log: slog.Default(), DHCP: newClient}); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, link, tableID, false)
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == foreign.IPNet.String()
	}) {
		t.Fatal("foreign address was removed during interface rename")
	}
	otherAddresses, err := netlink.AddrList(otherLink, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(otherAddresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "203.0.113.8/24"
	}) {
		t.Fatal("another consumer's address was removed during interface rename")
	}
	otherRoutes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID + 1}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(otherRoutes, func(route netlink.Route) bool {
		return route.Protocol == netif.OwnedDHCPv4RouteProtocol && route.LinkIndex == otherLink.Attrs().Index
	}) {
		t.Fatal("another consumer's route was removed during interface rename")
	}
	newLink, err := netlink.LinkByName(newIface)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(newLink); err != nil {
		t.Fatal(err)
	}
	leaseA := lease
	leaseA.LinkIndex = newLink.Attrs().Index
	leaseA.LinkHardwareAddr = append(net.HardwareAddr(nil), newLink.Attrs().HardwareAddr...)
	leaseA.ExpiresAt = time.Now().Add(time.Second)
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseA); err != nil {
		t.Fatal(err)
	}
	assertOOBDHCPv4(t, newLink, tableID, true)
	foreignB := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(198, 51, 100, 9), Mask: net.CIDRMask(24, 32)}}
	if err := netlink.AddrAdd(newLink, foreignB); err != nil {
		t.Fatal(err)
	}
	leaseB := leaseA
	leaseB.IP = net.IPv4(198, 51, 100, 9)
	leaseB.Routes = []netif.LeaseRoute{{
		Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Gateway:     net.IPv4(198, 51, 100, 1),
	}}
	leaseB.ExpiresAt = time.Now().Add(time.Hour)
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseB); err == nil {
		t.Fatal("conflicting replacement address was accepted before prior expiry")
	}
	assertOOBDHCPv4(t, newLink, tableID, true)
	time.Sleep(time.Until(leaseA.ExpiresAt) + 20*time.Millisecond)
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseB); err == nil {
		t.Fatal("conflicting replacement address was accepted")
	}
	assertOOBDHCPv4(t, newLink, tableID, false)
	if err := netlink.AddrDel(newLink, foreignB); err != nil {
		t.Fatal(err)
	}
	leaseB.State = netif.LeaseRenewing
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseB); err != nil {
		t.Fatal(err)
	}
	addresses, err = netlink.AddrList(newLink, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "198.51.100.9/24"
	}) {
		t.Fatal("renewing snapshot did not retry the failed assignment")
	}
	foreignC := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(203, 0, 113, 9), Mask: net.CIDRMask(24, 32)}}
	if err := netlink.AddrAdd(newLink, foreignC); err != nil {
		t.Fatal(err)
	}
	leaseC := leaseB
	leaseC.State = netif.LeaseBound
	leaseC.InvalidationEpoch++
	leaseC.IP = net.IPv4(203, 0, 113, 9)
	leaseC.Routes = []netif.LeaseRoute{{
		Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Gateway:     net.IPv4(203, 0, 113, 1),
	}}
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseC); err == nil {
		t.Fatal("conflicting assignment after invalidation was accepted")
	}
	addresses, err = netlink.AddrList(newLink, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "198.51.100.9/24"
	}) {
		t.Fatal("invalidated assignment survived failed replacement")
	}
	if err := netlink.AddrDel(newLink, foreignC); err != nil {
		t.Fatal(err)
	}
	leaseC.State = netif.LeaseRebinding
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseC); err != nil {
		t.Fatal(err)
	}
	addresses, err = netlink.AddrList(newLink, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "203.0.113.9/24"
	}) {
		t.Fatal("rebinding snapshot did not retry the failed assignment")
	}
	leaseC.State = netif.LeaseBound
	leaseC.ExpiresAt = time.Now().Add(250 * time.Millisecond)
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseC); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(leaseC.ExpiresAt) + 20*time.Millisecond)
	if err := module.Reconcile(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	addresses, err = netlink.AddrList(newLink, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "203.0.113.9/24"
	}) {
		t.Fatal("periodic reconciliation retained expired assignment")
	}
}

func assertOOBDHCPv4(t *testing.T, link netlink.Link, tableID int, assigned bool) {
	t.Helper()
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	addressCount := 0
	for _, address := range addresses {
		if address.IPNet.String() == "192.0.2.8/24" {
			addressCount++
		}
	}
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	routeCount := 0
	for _, route := range routes {
		if route.Protocol == netif.OwnedDHCPv4RouteProtocol {
			routeCount++
		}
	}
	want := 0
	if assigned {
		want = 1
	}
	if addressCount != want || routeCount != want {
		t.Fatalf("owned address count %d and route count %d, want %d", addressCount, routeCount, want)
	}
}
