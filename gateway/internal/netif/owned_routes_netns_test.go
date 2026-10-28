//go:build linux && netns

package netif

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedOnLinkRouteLifecycle(t *testing.T) {
	const childEnv = "MWAN_OWNED_ON_LINK_ROUTE_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedOnLinkRouteLifecycle$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated owned route test: %v: %s", err, output)
		}
		return
	}

	const linkName = "owned-route0"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: linkName}}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(linkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr("192.0.2.2/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}

	connection := interfaceintent.Connection{ID: "owned-route-test", Name: linkName}
	ready := OwnedLinkResult{ConnectionID: connection.ID.String(), Name: linkName, ActualName: linkName, IfIndex: link.Attrs().Index, Status: OwnedLinkReady}
	statePath := filepath.Join(t.TempDir(), "owned-routes.json")
	reconciler, err := NewOwnedStaticReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	destination := netip.MustParsePrefix("198.51.100.0/24")
	onLink := OwnedRoute{Destination: destination, Gateway: netip.MustParseAddr("0.0.0.0"), Metric: 397}
	viaGateway := OwnedRoute{Destination: destination, Gateway: netip.MustParseAddr("192.0.2.1"), Metric: 397}
	reconcile := func(routes []OwnedRoute) {
		t.Helper()
		if err := reconciler.ReconcileFamilyRoutes(context.Background(), connection, "ipv4", interfaceintent.Family{}, routes, ready); err != nil {
			t.Fatal(err)
		}
	}
	assertRoutes := func(wantGateway net.IP, wantCount int) {
		t.Helper()
		routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
		if err != nil {
			t.Fatal(err)
		}
		var matched []netlink.Route
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() == destination.String() && route.Priority == 397 {
				matched = append(matched, route)
			}
		}
		if len(matched) != wantCount {
			t.Fatalf("routes for %s = %v, want %d", destination, matched, wantCount)
		}
		if wantCount == 1 && (matched[0].LinkIndex != link.Attrs().Index || matched[0].Protocol != OwnedStaticRouteProtocol || !matched[0].Gw.Equal(wantGateway)) {
			t.Fatalf("route for %s = %+v, want gateway %v", destination, matched[0], wantGateway)
		}
	}

	reconcile([]OwnedRoute{onLink})
	assertRoutes(nil, 1)
	reconcile([]OwnedRoute{onLink})
	assertRoutes(nil, 1)
	restarted, err := NewOwnedStaticReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	reconciler = restarted
	reconcile([]OwnedRoute{onLink})
	assertRoutes(nil, 1)
	reconcile([]OwnedRoute{viaGateway})
	assertRoutes(net.ParseIP("192.0.2.1"), 1)
	reconcile([]OwnedRoute{onLink})
	assertRoutes(nil, 1)
	reconcile(nil)
	assertRoutes(nil, 0)
	conflicting := []OwnedRoute{onLink, viaGateway}
	if err := reconciler.ReconcileFamilyRoutes(context.Background(), connection, "ipv4", interfaceintent.Family{}, conflicting, ready); err == nil {
		t.Fatal("conflicting routes with one destination and metric were accepted")
	}
	assertRoutes(nil, 0)
}

func TestOwnedDefaultRouteUsesLaterOnLinkGatewayRoute(t *testing.T) {
	const childEnv = "MWAN_OWNED_DEFAULT_AFTER_ON_LINK_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedDefaultRouteUsesLaterOnLinkGatewayRoute$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated default route test: %v: %s", err, output)
		}
		return
	}

	const linkName = "owned-dhcp0"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: linkName}}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(linkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	connection := interfaceintent.Connection{ID: "owned-dhcp-test", Name: linkName}
	ready := OwnedLinkResult{ConnectionID: connection.ID.String(), Name: linkName, ActualName: linkName, IfIndex: link.Attrs().Index, Status: OwnedLinkReady}
	settings := interfaceintent.Family{Addresses: []interfaceintent.Address{{Prefix: netip.MustParsePrefix("192.0.2.2/32")}}}
	reconciler, err := NewOwnedStaticReconciler(filepath.Join(t.TempDir(), "owned-dhcp-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	defaultRoute := OwnedRoute{Destination: netip.MustParsePrefix("0.0.0.0/0"), Gateway: netip.MustParseAddr("192.0.2.1"), Metric: 397}
	gatewayRoute := OwnedRoute{Destination: netip.MustParsePrefix("192.0.2.1/32"), Gateway: netip.MustParseAddr("0.0.0.0"), Metric: 397}
	assigned := []OwnedRoute{defaultRoute, gatewayRoute}
	if err := reconciler.ReconcileFamilyRoutes(context.Background(), connection, "ipv4", settings, assigned, ready); err != nil {
		t.Fatalf("install routes supplied default first: %v", err)
	}
	assertDHCPRoutes(t, link.Attrs().Index, true)
	if err := reconciler.ReconcileFamilyRoutes(context.Background(), connection, "ipv4", settings, nil, ready); err != nil {
		t.Fatalf("withdraw classless routes: %v", err)
	}
	assertDHCPRoutes(t, link.Attrs().Index, false)
}

func assertDHCPRoutes(t *testing.T, linkIndex int, installed bool) {
	t.Helper()
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	var matched []netlink.Route
	defaultFound := false
	gatewayFound := false
	for _, route := range routes {
		if route.LinkIndex == linkIndex && route.Protocol == OwnedStaticRouteProtocol && route.Priority == 397 {
			matched = append(matched, route)
			if (route.Dst == nil || route.Dst.String() == "0.0.0.0/0") && route.Gw.Equal(net.ParseIP("192.0.2.1")) {
				defaultFound = true
			}
			if route.Dst != nil && route.Dst.String() == "192.0.2.1/32" && route.Gw == nil {
				gatewayFound = true
			}
		}
	}
	if installed && (len(matched) != 2 || !defaultFound || !gatewayFound) {
		t.Fatalf("owned classless routes = %v, want default and on-link gateway", matched)
	}
	if !installed && len(matched) != 0 {
		t.Fatalf("owned classless routes after withdrawal = %v", matched)
	}
}
