//go:build linux && netns

package wanroutes

import (
	"testing"

	"goodkind.io/mwan/internal/netif"
)

func TestTunnelReadinessReportsTheFailedDependency(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")

	healthy := map[string]string{"isp": netif.HealthStateHealthy, "other": netif.HealthStateHealthy}
	fixture.writeHealth(t, healthy)
	pending := fixture.reconcile(ctx, t)
	requireTunnelRouting(t, pending, false, reasonProbePending)
	if !pending.Routing["isp"].V4Ready {
		t.Fatalf("a pending tunnel probe changed the underlay: %+v", pending.Routing["isp"])
	}
	requireTunnelEndpointRoute(t, tunnelRouteGateway)
	if got := tableGateways(ctx, t, fixture.module, tunnelRouteTunnelTbl); len(got) != 0 {
		t.Fatalf("tunnel table gateways with a pending probe = %q, want none", got)
	}

	healthy[tunnelRouteID] = netif.HealthStateUnhealthy
	fixture.writeHealth(t, healthy)
	failed := fixture.reconcile(ctx, t)
	requireTunnelRouting(t, failed, false, reasonProbeFailed)
	if !failed.Routing["isp"].V4Ready {
		t.Fatalf("a failed tunnel probe changed the underlay: %+v", failed.Routing["isp"])
	}

	healthy[tunnelRouteID] = netif.HealthStateHealthy
	healthy["isp"] = netif.HealthStateUnhealthy
	fixture.writeHealth(t, healthy)
	requireTunnelRouting(t, fixture.reconcile(ctx, t), false, reasonUnderlayNotReady)

	healthy["isp"] = netif.HealthStateHealthy
	fixture.writeHealth(t, healthy)
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")

	fixture.links.Replace(nil)
	absent := fixture.reconcile(ctx, t)
	requireTunnelRouting(t, absent, false, reasonTunnelLinkAbsent)
	if !absent.Routing["isp"].V4Ready {
		t.Fatalf("an absent tunnel link changed the underlay: %+v", absent.Routing["isp"])
	}

	fixture.links.Replace(fixture.applied)
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
}
