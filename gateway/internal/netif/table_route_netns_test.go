//go:build linux && netns

package netif_test

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

const (
	tableRouteTestTable  = 100
	tableRouteDest       = "2001:db8:100::/48"
	tableRouteLowMetric  = 50
	tableRouteHighMetric = 200
)

func addTableRouteLink(t *testing.T, name string, address string) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		t.Fatalf("add link %s: %v", name, err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("find link %s: %v", name, err)
	}
	parsed, err := netlink.ParseAddr(address)
	if err != nil {
		t.Fatalf("parse address %s: %v", address, err)
	}
	if err := netlink.AddrAdd(link, parsed); err != nil {
		t.Fatalf("add address %s to %s: %v", address, name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("set link %s up: %v", name, err)
	}
	return link
}

func tableRouteLinks(t *testing.T) map[int]bool {
	t.Helper()
	filter := &netlink.Route{Table: tableRouteTestTable}
	routes, err := netlink.RouteListFiltered(unix.AF_INET6, filter, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list table %d routes: %v", tableRouteTestTable, err)
	}
	links := make(map[int]bool)
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == tableRouteDest {
			links[route.LinkIndex] = true
		}
	}
	return links
}

func TestDeleteTableRouteMatchesDeviceMetricAndGateway(t *testing.T) {
	const childEnv = "MWAN_DELETE_TABLE_ROUTE_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestDeleteTableRouteMatchesDeviceMetricAndGateway$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated table route test: %v: %s", err, output)
		}
		return
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lowLink := addTableRouteLink(t, "route-low0", "2001:db8:ffff::1/64")
	highLink := addTableRouteLink(t, "route-high0", "2001:db8:fffe::1/64")
	low := netif.RouteSpec{
		Family: "inet6", Dest: tableRouteDest, Via: "2001:db8:ffff::2", Dev: "route-low0",
		TableID: tableRouteTestTable, Metric: tableRouteLowMetric, Protocol: unix.RTPROT_BGP,
	}
	high := netif.RouteSpec{
		Family: "inet6", Dest: tableRouteDest, Via: "2001:db8:fffe::2", Dev: "route-high0",
		TableID: tableRouteTestTable, Metric: tableRouteHighMetric, Protocol: unix.RTPROT_BGP,
	}
	for _, route := range []netif.RouteSpec{low, high} {
		if err := netif.ReconcileTableRoute(t.Context(), log, route); err != nil {
			t.Fatalf("install %+v: %v", route, err)
		}
	}

	wrongGateway := high
	wrongGateway.Via = "2001:db8:fffe::3"
	if err := netif.DeleteTableRoute(t.Context(), log, wrongGateway); err != nil {
		t.Fatalf("delete with another gateway: %v", err)
	}
	wrongMetric := high
	wrongMetric.Via = ""
	wrongMetric.Metric = tableRouteLowMetric
	if err := netif.DeleteTableRoute(t.Context(), log, wrongMetric); err != nil {
		t.Fatalf("delete with another metric: %v", err)
	}
	links := tableRouteLinks(t)
	if !links[lowLink.Attrs().Index] || !links[highLink.Attrs().Index] {
		t.Fatalf("route devices after two requests that match no route = %v, want both routes", links)
	}

	// A request without a device removes the lower-metric route on route-low0.
	deviceOnly := netif.RouteSpec{
		Family: "inet6", Dest: tableRouteDest, Dev: "route-high0",
		TableID: tableRouteTestTable, Protocol: unix.RTPROT_BGP,
	}
	if err := netif.DeleteTableRoute(t.Context(), log, deviceOnly); err != nil {
		t.Fatalf("delete the route on route-high0: %v", err)
	}
	links = tableRouteLinks(t)
	if !links[lowLink.Attrs().Index] || links[highLink.Attrs().Index] {
		t.Fatalf("route devices after the delete on route-high0 = %v, want only route-low0", links)
	}

	absentDevice := deviceOnly
	absentDevice.Dev = "route-gone0"
	if err := netif.DeleteTableRoute(t.Context(), log, absentDevice); err != nil {
		t.Fatalf("delete on an absent device: %v", err)
	}
	if links := tableRouteLinks(t); !links[lowLink.Attrs().Index] {
		t.Fatalf("route devices after the delete on an absent device = %v, want route-low0", links)
	}
}
