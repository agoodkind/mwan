//go:build linux && netns

package wanroutes

import (
	"net"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"goodkind.io/mwan/internal/netif"
)

const (
	repairEndpointRouteDeleted = "owned tunnel endpoint route deleted"
	repairPolicyRuleDeleted    = "owned policy rule deleted"
	repairMainDefaultChanged   = "provider main default route changed"
	tunnelRouteTunnelMarkPrio  = 200
	tunnelRouteTunnelMark      = 2
)

func (fixture *tunnelRouteFixture) drainRepairs() {
	for {
		select {
		case <-fixture.repairs:
		default:
			return
		}
	}
}

// After Init, each interface monitor replays the main default route of its link, and the module
// requests one repair with the main default reason for each replayed route.
func (fixture *tunnelRouteFixture) repairsUntil(t *testing.T, boundary, counted, state string, settled func() bool) int {
	t.Helper()
	expired := time.After(20 * time.Second)
	count := 0
	for {
		select {
		case reason := <-fixture.repairs:
			if reason == boundary && settled() {
				return count
			}
			if reason == counted {
				count++
			}
		case <-expired:
			t.Fatalf("no repair request with reason %q and %s", boundary, state)
		}
	}
}

func endpointRouteUses(t *testing.T, gateway string) bool {
	t.Helper()
	endpoints := tunnelEndpointRoutes(t, tunnelRouteUnderTbl)
	return len(endpoints) == 1 && endpoints[0].Gw.Equal(net.ParseIP(gateway))
}

func TestTunnelEndpointRouteDeletionRequestsRepair(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	setTunnelRouteNeighbour(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteSecondMAC)
	if err := fixture.module.Init(ctx, fixture.module.Env); err != nil {
		t.Fatalf("Init: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	requested := false
	for !requested {
		if time.Now().After(deadline) {
			t.Fatal("deletion of the endpoint route requested no reconcile pass")
		}
		fixture.reconcile(ctx, t)
		owned := requireTunnelEndpointRoute(t, tunnelRouteGateway)
		if err := netlink.RouteDel(&owned); err != nil {
			t.Fatalf("delete the endpoint route: %v", err)
		}
		requested = fixture.repairRequested(repairEndpointRouteDeleted, time.Second)
	}
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
	requireTunnelEndpointRoute(t, tunnelRouteGateway)

	fixture.drainRepairs()
	setTunnelRouteDefault(t, tunnelRouteUnderlay, tunnelRouteGateway, tunnelRouteUnderMetric, false)
	fixture.repairsUntil(t, repairMainDefaultChanged, repairEndpointRouteDeleted, "no endpoint route without an underlay gateway", func() bool {
		return len(tunnelEndpointRoutes(t, tunnelRouteUnderTbl)) == 0
	})
	setTunnelRouteDefault(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteUnderMetric, true)
	replacedRoute := "the endpoint route through " + tunnelRouteSecondGW
	own := fixture.repairsUntil(t, repairMainDefaultChanged, repairEndpointRouteDeleted, replacedRoute, func() bool {
		return endpointRouteUses(t, tunnelRouteSecondGW)
	})
	if own != 0 {
		t.Fatalf("the module's own deletion of the endpoint route requested %d repair passes, want none", own)
	}

	replaced := requireTunnelEndpointRoute(t, tunnelRouteSecondGW)
	if err := netlink.RouteDel(&replaced); err != nil {
		t.Fatalf("delete the endpoint route: %v", err)
	}
	if !fixture.repairRequested(repairEndpointRouteDeleted, 20*time.Second) {
		t.Fatal("deletion of the endpoint route by another writer requested no reconcile pass")
	}
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
	requireTunnelEndpointRoute(t, tunnelRouteSecondGW)
}

func tunnelMarkRule(t *testing.T) (netlink.Rule, bool) {
	t.Helper()
	rules, err := netlink.RuleList(netlink.FAMILY_V6)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.Priority == tunnelRouteTunnelMarkPrio {
			return rule, true
		}
	}
	return netlink.Rule{}, false
}

func TestTunnelRuleRemovalByTheModuleRequestsNoRepair(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	if err := fixture.module.Init(ctx, fixture.module.Env); err != nil {
		t.Fatalf("Init: %v", err)
	}
	deleted := netif.RuleEvent{Family: familyV6, TableID: tunnelRouteTunnelTbl, Priority: tunnelRouteTunnelMarkPrio, Mark: tunnelRouteTunnelMark}
	deadline := time.Now().Add(20 * time.Second)
	requested := false
	for !requested {
		if time.Now().After(deadline) {
			t.Fatal("deletion of the tunnel mark rule by another writer requested no reconcile pass")
		}
		requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
		rule, found := tunnelMarkRule(t)
		if !found {
			t.Fatal("a ready tunnel has no IPv6 mark rule")
		}
		if !fixture.module.ownsDesiredRuleDeletion(ctx, fixture.module.Log, deleted) {
			t.Fatal("the rule monitor does not classify the mark rule of a ready tunnel as an owned rule")
		}
		if err := netlink.RuleDel(&rule); err != nil {
			t.Fatalf("delete the tunnel mark rule: %v", err)
		}
		requested = fixture.repairRequested(repairPolicyRuleDeleted, time.Second)
	}
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
	fixture.drainRepairs()

	fixture.writeHealth(t, map[string]string{"isp": netif.HealthStateUnhealthy, "other": netif.HealthStateHealthy, tunnelRouteID: netif.HealthStateHealthy})
	requireTunnelRouting(t, fixture.reconcile(ctx, t), false, reasonUnderlayNotReady)
	if rule, found := tunnelMarkRule(t); found {
		t.Fatalf("the tunnel mark rule remains with an unhealthy underlay: %+v", rule)
	}
	if fixture.module.ownsDesiredRuleDeletion(ctx, fixture.module.Log, deleted) {
		t.Fatal("the rule monitor classifies the module's own rule removal as a deletion by another writer")
	}
	if fixture.repairRequested(repairPolicyRuleDeleted, time.Second) {
		t.Fatal("the module's own rule removal requested a repair pass")
	}
}
