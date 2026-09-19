package bgp

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

type recordedRouteWriter struct {
	installs []netif.RouteSpec
	deletes  []netif.RouteSpec
	routes   map[int][]netif.CurrentRoute
}

func (w *recordedRouteWriter) ReconcileTableRoute(
	_ context.Context,
	_ *slog.Logger,
	route netif.RouteSpec,
) error {
	w.installs = append(w.installs, route)
	return nil
}

func (w *recordedRouteWriter) DeleteTableRoute(
	_ context.Context,
	_ *slog.Logger,
	route netif.RouteSpec,
) error {
	w.deletes = append(w.deletes, route)
	return nil
}

func (w *recordedRouteWriter) ListProtocolRoutes(
	_ context.Context,
	_ *slog.Logger,
	_ string,
	tableID int,
	_ int,
) ([]netif.CurrentRoute, error) {
	return w.routes[tableID], nil
}

func TestFIBApplyInstallsEveryTable(t *testing.T) {
	t.Parallel()

	tables := []int{100, 200, 300}
	writer := &recordedRouteWriter{routes: make(map[int][]netif.CurrentRoute)}
	fib := newFIB(FIBConfig{Tables: tables, InternalIface: "vmbr250"}, testFIBLogger(), writer)
	event := PathEvent{
		Peer:      "router-2",
		Prefix:    netip.MustParsePrefix("3d06:bad:b01:4::/64"),
		NextHop:   netip.MustParseAddr("3d06:bad:b01:fe::5"),
		Withdrawn: false,
	}

	if err := fib.Apply(context.Background(), event); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got, want := len(writer.installs), len(tables); got != want {
		t.Fatalf("install count = %d, want %d", got, want)
	}
	for index, tableID := range tables {
		route := writer.installs[index]
		if route.TableID != tableID {
			t.Fatalf("install[%d] table = %d, want %d", index, route.TableID, tableID)
		}
		if route.Protocol != unix.RTPROT_BGP {
			t.Fatalf("install[%d] protocol = %d, want %d", index, route.Protocol, unix.RTPROT_BGP)
		}
		if route.Via != event.NextHop.String() || route.Dev != "vmbr250" {
			t.Fatalf("install[%d] route = %#v", index, route)
		}
	}
}

func TestFIBWithdrawDeletesOnlyWithdrawnPrefix(t *testing.T) {
	t.Parallel()

	tables := []int{100, 200}
	writer := &recordedRouteWriter{routes: make(map[int][]netif.CurrentRoute)}
	fib := newFIB(FIBConfig{Tables: tables, InternalIface: "vmbr250"}, testFIBLogger(), writer)
	first := PathEvent{
		Peer:      "router-1",
		Prefix:    netip.MustParsePrefix("3d06:bad:b01::/64"),
		NextHop:   netip.MustParseAddr("3d06:bad:b01:fe::2"),
		Withdrawn: false,
	}
	second := PathEvent{
		Peer:      "router-2",
		Prefix:    netip.MustParsePrefix("3d06:bad:b01:4::/64"),
		NextHop:   netip.MustParseAddr("3d06:bad:b01:fe::5"),
		Withdrawn: false,
	}

	if err := fib.Apply(context.Background(), first); err != nil {
		t.Fatalf("apply first path: %v", err)
	}
	if err := fib.Apply(context.Background(), second); err != nil {
		t.Fatalf("apply second path: %v", err)
	}
	withdraw := PathEvent{Peer: first.Peer, Prefix: first.Prefix, Withdrawn: true}
	if err := fib.Apply(context.Background(), withdraw); err != nil {
		t.Fatalf("withdraw first path: %v", err)
	}

	if got, want := len(writer.deletes), len(tables); got != want {
		t.Fatalf("delete count = %d, want %d", got, want)
	}
	for index, route := range writer.deletes {
		if route.Dest != first.Prefix.String() {
			t.Fatalf("delete[%d] destination = %s, want %s", index, route.Dest, first.Prefix)
		}
		if route.Dest == second.Prefix.String() {
			t.Fatalf("delete[%d] removed the retained prefix", index)
		}
	}
}

func TestFIBSweepStaleRemovesOnlyUndesiredRoutes(t *testing.T) {
	t.Parallel()

	desired := PathEvent{
		Peer:      "router-1",
		Prefix:    netip.MustParsePrefix("3d06:bad:b01::/64"),
		NextHop:   netip.MustParseAddr("3d06:bad:b01:fe::2"),
		Withdrawn: false,
	}
	stale := netif.CurrentRoute{Dest: "3d06:bad:b01:4::/64", Via: "3d06:bad:b01:fe::5", Dev: "vmbr250", Metric: 0}
	writer := &recordedRouteWriter{routes: map[int][]netif.CurrentRoute{
		100: {
			{Dest: desired.Prefix.String(), Via: desired.NextHop.String(), Dev: "vmbr250", Metric: 0},
			stale,
		},
	}}
	fib := newFIB(FIBConfig{Tables: []int{100}, InternalIface: "vmbr250"}, testFIBLogger(), writer)

	if err := fib.Apply(context.Background(), desired); err != nil {
		t.Fatalf("apply desired path: %v", err)
	}
	writer.installs = nil
	if err := fib.SweepStale(context.Background()); err != nil {
		t.Fatalf("unarmed SweepStale returned error: %v", err)
	}
	if got := len(writer.deletes); got != 0 {
		t.Fatalf("unarmed SweepStale delete count = %d, want 0", got)
	}
	fib.ArmSweep()
	if err := fib.SweepStale(context.Background()); err != nil {
		t.Fatalf("SweepStale returned error: %v", err)
	}

	if got, want := len(writer.deletes), 1; got != want {
		t.Fatalf("delete count = %d, want %d", got, want)
	}
	if got, want := writer.deletes[0].Dest, stale.Dest; got != want {
		t.Fatalf("deleted destination = %s, want %s", got, want)
	}
}

func testFIBLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
