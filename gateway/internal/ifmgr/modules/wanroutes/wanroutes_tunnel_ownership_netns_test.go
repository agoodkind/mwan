//go:build linux && netns

package wanroutes

import (
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

func addForeignTableRoute(t *testing.T, destination string, protocol int) {
	t.Helper()
	underlay, err := netlink.LinkByName(tunnelRouteUnderlay)
	if err != nil {
		t.Fatal(err)
	}
	_, network, err := net.ParseCIDR(destination)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		LinkIndex: underlay.Attrs().Index, Dst: network, Gw: net.ParseIP(tunnelRouteGateway),
		Table: tunnelRouteUnderTbl, Protocol: netlink.RouteProtocol(protocol),
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add the foreign route %s protocol %d: %v", destination, protocol, err)
	}
}

func requireForeignTableRoute(t *testing.T, destination string, protocol int) {
	t.Helper()
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tunnelRouteUnderTbl}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == destination && int(route.Protocol) == protocol {
			return
		}
	}
	t.Fatalf("reconciliation deleted the foreign route %s protocol %d: %+v", destination, protocol, routes)
}

func restartTunnelRouteModule(t *testing.T, fixture *tunnelRouteFixture, withTunnel bool, journal ifmgr.TunnelEndpointRoutes) *Module {
	t.Helper()
	created, err := New(tunnelRouteConfig(fixture.healthPath, withTunnel))
	if err != nil {
		t.Fatal(err)
	}
	module := created.(*Module)
	env := testEnvWithStore(fixture.store)
	env.OwnedLinks = fixture.links
	env.TunnelEndpointRoutes = journal
	module.InitBase(env, "module", moduleName)
	module.resolveNextHop = netif.NextHopResolves
	return module
}

func TestTunnelEndpointRouteRemovedWithTheTunnel(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	foreign := map[string]int{
		"198.51.100.77/32": unix.RTPROT_STATIC,
		"198.51.100.78/32": unix.RTPROT_ISIS,
		"198.51.100.79/32": netif.TunnelEndpointRouteProtocol,
	}
	for destination, protocol := range foreign {
		addForeignTableRoute(t, destination, protocol)
	}
	for range 2 {
		fixture.reconcile(ctx, t)
		requireTunnelEndpointRoute(t, tunnelRouteGateway)
		for destination, protocol := range foreign {
			requireForeignTableRoute(t, destination, protocol)
		}
	}

	module := restartTunnelRouteModule(t, fixture, false, openTunnelRouteJournal(t, fixture.journalPath))
	for range 2 {
		if err := module.Reconcile(ctx, module.Log); err != nil {
			t.Fatalf("Reconcile without the tunnel: %v", err)
		}
		if endpoints := tunnelEndpointRoutes(t, tunnelRouteUnderTbl); len(endpoints) != 0 {
			t.Fatalf("endpoint routes after tunnel removal = %+v", endpoints)
		}
		for destination, protocol := range foreign {
			requireForeignTableRoute(t, destination, protocol)
		}
	}
}

func TestRoutesAtTheEndpointDestinationOfAnotherWriterSurvive(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	addForeignTableRoute(t, tunnelRouteEndpoint, unix.RTPROT_ISIS)

	withoutJournal := restartTunnelRouteModule(t, fixture, false, nil)
	recorded := restartTunnelRouteModule(t, fixture, false, openTunnelRouteJournal(t, fixture.journalPath))
	for _, module := range []*Module{withoutJournal, recorded, withoutJournal, recorded} {
		if err := module.Reconcile(ctx, module.Log); err != nil {
			t.Fatalf("Reconcile without a tunnel: %v", err)
		}
		requireForeignTableRoute(t, tunnelRouteEndpoint, unix.RTPROT_ISIS)
	}

	err := fixture.module.Reconcile(ctx, fixture.module.Log)
	if err == nil || !strings.Contains(err.Error(), "conflicts with an unowned route") {
		t.Fatalf("Reconcile with a foreign route at the endpoint destination = %v, want a conflict error", err)
	}
	requireForeignTableRoute(t, tunnelRouteEndpoint, unix.RTPROT_ISIS)
	requireTunnelRouting(t, fixture.store.Snapshot(), false, reasonEndpointRouteAbsent)
}
