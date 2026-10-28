//go:build linux && netns

package wanroutes

import (
	"bytes"
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

func (fixture *tunnelRouteFixture) writeHealth(t *testing.T, states map[string]string) {
	t.Helper()
	var contents bytes.Buffer
	for connectionID, state := range states {
		contents.WriteString(connectionID + ":" + state + "\n")
	}
	if err := os.WriteFile(fixture.healthPath, contents.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *tunnelRouteFixture) reconcile(ctx context.Context, t *testing.T) wanstate.Snapshot {
	t.Helper()
	if err := fixture.module.Reconcile(ctx, fixture.module.Log); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return fixture.store.Snapshot()
}

func (fixture *tunnelRouteFixture) repairRequested(want string, window time.Duration) bool {
	expired := time.After(window)
	for {
		select {
		case reason := <-fixture.repairs:
			if reason == want {
				return true
			}
		case <-expired:
			return false
		}
	}
}

func tunnelEndpointRoutes(t *testing.T, tableID int) []netlink.Route {
	t.Helper()
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list table %d: %v", tableID, err)
	}
	var endpoints []netlink.Route
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == tunnelRouteEndpoint {
			endpoints = append(endpoints, route)
		}
	}
	return endpoints
}

func requireTunnelEndpointRoute(t *testing.T, gateway string) netlink.Route {
	t.Helper()
	underlay, err := netlink.LinkByName(tunnelRouteUnderlay)
	if err != nil {
		t.Fatal(err)
	}
	endpoints := tunnelEndpointRoutes(t, tunnelRouteUnderTbl)
	if len(endpoints) != 1 || !endpoints[0].Gw.Equal(net.ParseIP(gateway)) || endpoints[0].LinkIndex != underlay.Attrs().Index ||
		int(endpoints[0].Protocol) != netif.TunnelEndpointRouteProtocol {
		t.Fatalf("table %d endpoint routes = %+v, want %s via %s dev %s protocol %d",
			tunnelRouteUnderTbl, endpoints, tunnelRouteEndpoint, gateway, tunnelRouteUnderlay, netif.TunnelEndpointRouteProtocol)
	}
	for _, tableID := range []int{tunnelRouteTunnelTbl, tunnelRouteOtherTbl, unix.RT_TABLE_MAIN} {
		if misplaced := tunnelEndpointRoutes(t, tableID); len(misplaced) != 0 {
			t.Fatalf("table %d has endpoint routes %+v", tableID, misplaced)
		}
	}
	return endpoints[0]
}

func requireTunnelRouting(t *testing.T, snapshot wanstate.Snapshot, ready bool, reason string) {
	t.Helper()
	routing := snapshot.Routing[tunnelRouteID]
	if routing.V6Ready != ready || routing.V6Reason != reason || routing.V4Ready || routing.V4Reason != "" {
		t.Fatalf("tunnel routing = %+v, want IPv6 ready=%t reason=%q and no IPv4 family", routing, ready, reason)
	}
	if other := snapshot.Routing["other"]; !other.V4Ready || other.V6Ready {
		t.Fatalf("other provider routing = %+v, want IPv4 ready and no IPv6 family", other)
	}
	if underlay := snapshot.Routing["isp"]; underlay.V6Ready {
		t.Fatalf("IPv4-only underlay reported IPv6 ready: %+v", underlay)
	}
}
