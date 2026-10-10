//go:build linux && netns

package wanroutes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const journalEndpointRoutesKey = "endpoint_routes"

func readTunnelRouteJournal(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal map[string]json.RawMessage
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatalf("decode the ownership journal: %v: %s", err, data)
	}
	return journal
}

func requireEndpointReceipts(t *testing.T, fixture *tunnelRouteFixture, want int) {
	t.Helper()
	absent := filepath.Join(filepath.Dir(fixture.journalPath), "absent.json")
	receipts, err := netif.InspectOwnedRelease(tunnelRouteID, absent, fixture.journalPath, absent)
	if err != nil {
		t.Fatal(err)
	}
	if receipts.OrdinaryObjects != want || receipts.Released() != (want == 0) {
		t.Fatalf("release receipts of the tunnel connection = %+v, want %d endpoint routes", receipts, want)
	}
}

func TestTunnelEndpointRouteAdoptedAfterAnOlderReleaseSavedTheJournal(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	fixture.reconcile(ctx, t)
	requireTunnelEndpointRoute(t, tunnelRouteGateway)
	requireEndpointReceipts(t, fixture, 1)

	// A release without endpoint route records saves the journal after decoding it without the endpoint
	// route list.
	journal := readTunnelRouteJournal(t, fixture.journalPath)
	if _, recorded := journal[journalEndpointRoutesKey]; !recorded {
		t.Fatalf("the journal has no endpoint route record: %v", journal)
	}
	delete(journal, journalEndpointRoutesKey)
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	requireEndpointReceipts(t, fixture, 0)

	upgraded := restartTunnelRouteModule(t, fixture, true, openTunnelRouteJournal(t, fixture.journalPath))
	if err := upgraded.Reconcile(ctx, upgraded.Log); err != nil {
		t.Fatalf("Reconcile after the journal lost the record: %v", err)
	}
	requireTunnelRouting(t, fixture.store.Snapshot(), true, "")
	requireTunnelEndpointRoute(t, tunnelRouteGateway)
	requireEndpointReceipts(t, fixture, 1)

	removed := restartTunnelRouteModule(t, fixture, false, openTunnelRouteJournal(t, fixture.journalPath))
	if err := removed.Reconcile(ctx, removed.Log); err != nil {
		t.Fatalf("Reconcile without the tunnel: %v", err)
	}
	if endpoints := tunnelEndpointRoutes(t, tunnelRouteUnderTbl); len(endpoints) != 0 {
		t.Fatalf("endpoint routes after tunnel removal = %+v", endpoints)
	}
	requireEndpointReceipts(t, fixture, 0)
}

func TestForeignRouteAfterARecordedFailedEndpointWriteSurvives(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	journal := openTunnelRouteJournal(t, fixture.journalPath)
	// The kernel rejects a gateway outside every connected network after the journal write.
	unreachable := netif.RouteSpec{
		Family: familyV4, Dest: tunnelRouteEndpoint, Via: "10.9.9.9", Dev: tunnelRouteUnderlay,
		TableID: tunnelRouteUnderTbl, Protocol: netif.TunnelEndpointRouteProtocol,
	}
	if err := journal.EnsureTunnelEndpointRoute(ctx, fixture.module.Log, tunnelRouteID, unreachable); err == nil {
		t.Fatal("the kernel accepted an endpoint route through an unreachable gateway")
	}
	requireEndpointReceipts(t, fixture, 1)
	addForeignTableRoute(t, tunnelRouteEndpoint, unix.RTPROT_STATIC)

	module := restartTunnelRouteModule(t, fixture, true, journal)
	err := module.Reconcile(ctx, module.Log)
	if err == nil || !strings.Contains(err.Error(), "conflicts with an unowned route") {
		t.Fatalf("Reconcile with a foreign route at a recorded destination = %v, want a conflict error", err)
	}
	requireForeignTableRoute(t, tunnelRouteEndpoint, unix.RTPROT_STATIC)
	requireTunnelRouting(t, fixture.store.Snapshot(), false, reasonEndpointRouteAbsent)
}

func TestEndpointRouteDeletionAfterAFailedWriteRequestsRepair(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	setTunnelRouteNeighbour(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteSecondMAC)
	if err := fixture.module.Init(ctx, fixture.module.Env); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// The test repeats route deletion because a route subscription can start after Init returns.
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
	fixture.reconcile(ctx, t)
	installed := requireTunnelEndpointRoute(t, tunnelRouteGateway)

	// The journal write for the changed gateway fails because a directory occupies the journal path.
	// The route through the first gateway still exists in the kernel.
	if err := os.Remove(fixture.journalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.journalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	replaceTunnelRouteDefault(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteUnderMetric)
	if err := fixture.module.Reconcile(ctx, fixture.module.Log); err == nil {
		t.Fatal("Reconcile succeeded without a writable journal")
	}
	requireTunnelEndpointRoute(t, tunnelRouteGateway)
	fixture.drainRepairs()
	if err := netlink.RouteDel(&installed); err != nil {
		t.Fatalf("delete the endpoint route: %v", err)
	}
	if !fixture.repairRequested(repairEndpointRouteDeleted, 10*time.Second) {
		t.Fatal("deletion of the endpoint route after a failed write requested no reconcile pass")
	}
}
