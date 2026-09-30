//go:build linux && netns

package mainv4

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

func TestMainDHCPv4AssignmentLifecycle(t *testing.T) {
	const childEnv = "MWAN_MAIN_DHCPV4_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestMainDHCPv4AssignmentLifecycle$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated main DHCPv4 test: %v: %s", err, output)
		}
		return
	}
	const iface = "main-dhcp4"
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
	stateFile := filepath.Join(t.TempDir(), "main-dhcpv4.json")
	moduleInterface, err := New(Config{Iface: iface, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module := moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default()}); err != nil {
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
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, false)
	client := netif.StartDHCPClient(ctx, slog.Default(), netif.DHCPConfig{
		Iface:           iface,
		DiscoverTimeout: time.Minute,
	})
	moduleInterface, err = New(Config{Iface: iface, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module = moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client}); err != nil {
		t.Fatal(err)
	}
	oldAddress, err := netlink.ParseAddr("192.0.2.9/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, oldAddress); err != nil {
		t.Fatal(err)
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err == nil {
		t.Fatal("changed-IP legacy address was accepted")
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "192.0.2.9/24"
	}) {
		t.Fatal("unidentified legacy address was removed")
	}
	if err := netlink.AddrDel(link, oldAddress); err != nil {
		t.Fatal(err)
	}
	legacyAddress, err := netlink.ParseAddr("192.0.2.8/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, legacyAddress); err != nil {
		t.Fatal(err)
	}
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, true)
	recoveryInterface, err := New(Config{Iface: iface, StateFile: stateFile})
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
	assertMainDHCPv4(t, link, true)
	moduleInterface, err = New(Config{Iface: iface, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module = moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client}); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, false)
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, true)
	lease.State = netif.LeaseRebinding
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, true)
	lease.State = netif.LeaseExpired
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, false)
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
	assertMainDHCPv4(t, link, false)
	lease.LinkIndex = link.Attrs().Index
	lease.LinkHardwareAddr = append(net.HardwareAddr(nil), link.Attrs().HardwareAddr...)
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, true)
	staleLease.State = netif.LeaseExpired
	if err := module.OnDHCPLease(ctx, slog.Default(), staleLease); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, true)
	const newIface = "main-new"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: newIface}}); err != nil {
		t.Fatal(err)
	}
	newClient := netif.StartDHCPClient(ctx, slog.Default(), netif.DHCPConfig{
		Iface:           newIface,
		DiscoverTimeout: time.Minute,
	})
	moduleInterface, err = New(Config{Iface: newIface, StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module = moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: newIface, Log: slog.Default(), DHCP: newClient}); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, link, false)
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
	leaseA.ExpiresAt = time.Now().Add(time.Hour)
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseA); err != nil {
		t.Fatal(err)
	}
	assertMainDHCPv4(t, newLink, true)
	foreignB := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(198, 51, 100, 9), Mask: net.CIDRMask(24, 32)}}
	if err := netlink.AddrAdd(newLink, foreignB); err != nil {
		t.Fatal(err)
	}
	leaseB := leaseA
	leaseB.InvalidationEpoch++
	leaseB.IP = net.IPv4(198, 51, 100, 9)
	leaseB.Routes = []netif.LeaseRoute{{
		Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Gateway:     net.IPv4(198, 51, 100, 1),
	}}
	if err := module.OnDHCPLease(ctx, slog.Default(), leaseB); err == nil {
		t.Fatal("conflicting assignment after invalidation was accepted")
	}
	assertMainDHCPv4(t, newLink, false)
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
	moduleInterface, err = New(Config{StateFile: stateFile})
	if err != nil {
		t.Fatal(err)
	}
	module = moduleInterface.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: newIface, Log: slog.Default()}); err != nil {
		t.Fatal(err)
	}
	addresses, err = netlink.AddrList(newLink, unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(addresses, func(address netlink.Addr) bool {
		return address.IPNet.String() == "198.51.100.9/24"
	}) {
		t.Fatal("disabled DHCP retained prior address")
	}
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(routes, func(route netlink.Route) bool {
		return route.Protocol == netif.OwnedDHCPv4RouteProtocol && route.LinkIndex == newLink.Attrs().Index
	}) {
		t.Fatal("disabled DHCP retained prior route")
	}
}

func assertMainDHCPv4(t *testing.T, link netlink.Link, assigned bool) {
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
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
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
