//go:build linux && netns

package netif_test

import (
	"encoding/binary"
	"log/slog"
	"net"
	"testing"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/offload"
)

func tableRouteNotificationSocket(t *testing.T) *mdnetlink.Conn {
	t.Helper()
	socket, err := mdnetlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		t.Fatalf("open route notification socket: %v", err)
	}
	t.Cleanup(func() {
		if err := socket.Close(); err != nil {
			t.Errorf("close route notification socket: %v", err)
		}
	})
	if err := socket.JoinGroup(unix.RTNLGRP_IPV6_ROUTE); err != nil {
		t.Fatalf("subscribe IPv6 route notifications: %v", err)
	}
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("route notification test requires a test deadline")
	}
	if err := socket.SetDeadline(deadline); err != nil {
		t.Fatalf("set route notification deadline: %v", err)
	}
	return socket
}

func tableRouteNotificationCount(t *testing.T, socket *mdnetlink.Conn) int {
	t.Helper()
	// This socket's acknowledgment follows its already queued multicast messages.
	barrier, err := socket.Send(mdnetlink.Message{
		Header: mdnetlink.Header{Type: mdnetlink.Noop, Flags: mdnetlink.Request | mdnetlink.Acknowledge},
	})
	if err != nil {
		t.Fatalf("send route notification barrier: %v", err)
	}
	count := 0
	for {
		messages, err := socket.Receive()
		if err != nil {
			t.Fatalf("read route notifications before acknowledgment: %v", err)
		}
		for _, message := range messages {
			switch message.Header.Type {
			case unix.RTM_NEWROUTE, unix.RTM_DELROUTE:
				count++
			case mdnetlink.Error:
				if message.Header.Sequence != barrier.Header.Sequence || len(message.Data) < 4 || binary.NativeEndian.Uint32(message.Data[:4]) != 0 {
					t.Fatalf("route notification barrier failed: %+v", message)
				}
				return count
			case mdnetlink.Overrun:
				t.Fatal("kernel route notifications lost messages")
			case mdnetlink.Noop, mdnetlink.Done:
				t.Fatalf("unexpected route notification control message %d", message.Header.Type)
			default:
				t.Fatalf("unexpected route notification type %d", message.Header.Type)
			}
		}
	}
}

func waitTableRouteGeneration(t *testing.T, events *offload.ForwardingEvents, previous uint64) uint64 {
	t.Helper()
	for events.Generation() <= previous {
		select {
		case <-events.Changes:
		case err := <-events.Errors:
			t.Fatalf("forwarding notification failed: %v", err)
		case <-t.Context().Done():
			t.Fatalf("forwarding generation did not advance after %d: %v", previous, t.Context().Err())
		}
	}
	if err := events.Err(); err != nil {
		t.Fatalf("forwarding notification evidence failed: %v", err)
	}
	return events.Generation()
}

func assertTableRoutePath(t *testing.T, fixture forwardPathFixture, expected int, gateway net.IP) {
	t.Helper()
	path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), fixture.selector)
	if err != nil || !usable || path.Route.LinkIndex != fixture.outputs[expected].Attrs().Index || !path.Route.Gw.Equal(gateway) {
		t.Fatalf("prefix forwarding path: usable=%v path=%+v err=%v, want output=%s gateway=%s", usable, path, err, fixture.outputs[expected].Attrs().Name, gateway)
	}
}

func TestReconcileTableRoutePreservesForwardingEvidence(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_TABLE_ROUTE_NOTIFICATIONS_CHILD") {
		return
	}
	log := slog.Default()
	settings := netif.NewProcSysctlRunner(log, false)
	// Disable automatic link-local addresses before creating the isolated links.
	if err := settings.Set(t.Context(), "net.ipv6.conf.default.addr_gen_mode", "1"); err != nil {
		t.Fatal(err)
	}
	fixture := newForwardPathFixture(t, unix.AF_INET6, "rn")
	fixture.selector.Mark = 0
	for index, output := range fixture.outputs {
		if err := netlink.NeighSet(&netlink.Neigh{
			LinkIndex: output.Attrs().Index, Family: unix.AF_INET6, State: unix.NUD_PERMANENT,
			IP: fixture.selector.Destination.Addr().AsSlice(), HardwareAddr: fixture.peers[index].Attrs().HardwareAddr,
		}); err != nil {
			t.Fatalf("install prefix destination neighbor: %v", err)
		}
	}
	socket := tableRouteNotificationSocket(t)
	want := netif.RouteSpec{
		Family: "inet6", Dest: fixture.selector.Destination.Addr().String() + "/128", Via: "", Dev: fixture.outputs[0].Attrs().Name,
		TableID: unix.RT_TABLE_MAIN, Metric: 0, Protocol: 0,
	}
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("install prefix route: %v", err)
	}
	if count := tableRouteNotificationCount(t, socket); count != 1 {
		t.Fatalf("initial prefix route notifications = %d, want 1", count)
	}
	assertTableRoutePath(t, fixture, 0, nil)
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("reconcile unchanged prefix route: %v", err)
	}
	if count := tableRouteNotificationCount(t, socket); count != 0 {
		t.Fatalf("unchanged prefix route emitted %d notifications", count)
	}
	// A fresh production observer measures the change independently of the barrier.
	events, err := offload.NewForwardingEvents(t.Context(), 1<<20)
	if err != nil {
		t.Fatalf("start forwarding evidence notifications: %v", err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close forwarding evidence notifications: %v", err)
		}
	})
	previous := events.Generation()
	want.Dev, want.Via = fixture.outputs[1].Attrs().Name, fixture.gateways[1].String()
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("change prefix forwarding path: %v", err)
	}
	if count := tableRouteNotificationCount(t, socket); count == 0 {
		t.Fatal("changed prefix route did not emit a notification")
	}
	t.Logf("changed prefix route advanced forwarding generation from %d to %d", previous, waitTableRouteGeneration(t, events, previous))
	assertTableRoutePath(t, fixture, 1, fixture.gateways[1])
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("reconcile unchanged gateway route: %v", err)
	}
	if count := tableRouteNotificationCount(t, socket); count != 0 {
		t.Fatalf("unchanged gateway route emitted %d notifications", count)
	}
}
