//go:build linux && netns

package netif_test

import (
	"encoding/binary"
	"log/slog"
	"net"
	"net/netip"
	"testing"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/offload"
)

type tableRouteNotification struct {
	messageType mdnetlink.HeaderType
	destination netip.Prefix
	table       uint32
	routeType   uint8
	outputIndex uint32
	gateway     netip.Addr
	metric      uint32
	protocol    uint8
}

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

func tableRouteNotificationAddress(t *testing.T, data []byte, attribute uint16) netip.Addr {
	t.Helper()
	address, valid := netip.AddrFromSlice(data)
	if !valid || len(data) != net.IPv6len {
		t.Fatalf("IPv6 route attribute %d has an invalid address: %x", attribute, data)
	}
	return address
}

func unmarshalTableRouteNotification(t *testing.T, message mdnetlink.Message) tableRouteNotification {
	t.Helper()
	if len(message.Data) < unix.SizeofRtMsg {
		t.Fatalf("route notification omitted its Linux header: %+v", message)
	}
	var header unix.RtMsg
	if _, err := binary.Decode(message.Data[:unix.SizeofRtMsg], binary.NativeEndian, &header); err != nil {
		t.Fatalf("decode route notification header: %v", err)
	}
	if header.Family != unix.AF_INET6 || header.Dst_len > net.IPv6len*8 || header.Src_len > net.IPv6len*8 {
		t.Fatalf("invalid IPv6 route notification header: %+v", header)
	}
	notification := tableRouteNotification{
		messageType: message.Header.Type, destination: netip.Prefix{}, table: uint32(header.Table),
		routeType: header.Type, outputIndex: 0, gateway: netip.Addr{}, metric: 0, protocol: header.Protocol,
	}
	attributes, err := mdnetlink.NewAttributeDecoder(message.Data[unix.SizeofRtMsg:])
	if err != nil {
		t.Fatalf("decode route notification attributes: %v", err)
	}
	attributes.ByteOrder = binary.NativeEndian
	address := netip.IPv6Unspecified()
	seen := make(map[uint16]bool)
	for attributes.Next() {
		attribute := attributes.Type()
		if seen[attribute] {
			t.Fatalf("route notification repeated attribute %d: header=%+v", attribute, header)
		}
		seen[attribute] = true
		switch attribute {
		case unix.RTA_DST:
			address = tableRouteNotificationAddress(t, attributes.Bytes(), attribute)
		case unix.RTA_TABLE:
			notification.table = attributes.Uint32()
		case unix.RTA_OIF:
			notification.outputIndex = attributes.Uint32()
		case unix.RTA_GATEWAY:
			notification.gateway = tableRouteNotificationAddress(t, attributes.Bytes(), attribute)
		case unix.RTA_PRIORITY:
			notification.metric = attributes.Uint32()
		}
	}
	if err := attributes.Err(); err != nil {
		t.Fatalf("decode route notification attribute value: %v", err)
	}
	if header.Dst_len != 0 && !seen[unix.RTA_DST] {
		t.Fatalf("route notification omitted its destination: header=%+v", header)
	}
	notification.destination = netip.PrefixFrom(address, int(header.Dst_len)).Masked()
	return notification
}

func tableRouteNotificationCount(t *testing.T, socket *mdnetlink.Conn, destination netip.Prefix, table uint32) (int, []tableRouteNotification) {
	t.Helper()
	// This socket's acknowledgment follows its already queued multicast messages.
	barrier, err := socket.Send(mdnetlink.Message{
		Header: mdnetlink.Header{Type: mdnetlink.Noop, Flags: mdnetlink.Request | mdnetlink.Acknowledge},
	})
	if err != nil {
		t.Fatalf("send route notification barrier: %v", err)
	}
	count := 0
	notifications := make([]tableRouteNotification, 0)
	for {
		messages, err := socket.Receive()
		if err != nil {
			t.Fatalf("read route notifications before acknowledgment: %v", err)
		}
		for _, message := range messages {
			switch message.Header.Type {
			case unix.RTM_NEWROUTE, unix.RTM_DELROUTE:
				notification := unmarshalTableRouteNotification(t, message)
				notifications = append(notifications, notification)
				t.Logf("IPv6 route notification: message_type=%d destination=%s table=%d route_type=%d output=%d gateway=%s metric=%d protocol=%d",
					notification.messageType, notification.destination, notification.table, notification.routeType,
					notification.outputIndex, notification.gateway, notification.metric, notification.protocol)
				if notification.destination == destination && notification.table == table {
					count++
				}
			case mdnetlink.Error:
				if message.Header.Sequence != barrier.Header.Sequence || len(message.Data) < 4 || binary.NativeEndian.Uint32(message.Data[:4]) != 0 {
					t.Fatalf("route notification barrier failed: %+v", message)
				}
				return count, notifications
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
	destination := netip.PrefixFrom(fixture.selector.Destination.Addr(), net.IPv6len*8)
	want := netif.RouteSpec{
		Family: "inet6", Dest: destination.String(), Via: "", Dev: fixture.outputs[0].Attrs().Name,
		TableID: unix.RT_TABLE_MAIN, Metric: 0, Protocol: 0,
	}
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("install prefix route: %v", err)
	}
	if count, notifications := tableRouteNotificationCount(t, socket, destination, unix.RT_TABLE_MAIN); count != 1 {
		t.Fatalf("initial prefix route notifications = %d, want 1: target=%s table=%d notifications=%+v", count, destination, unix.RT_TABLE_MAIN, notifications)
	}
	assertTableRoutePath(t, fixture, 0, nil)
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("reconcile unchanged prefix route: %v", err)
	}
	if count, notifications := tableRouteNotificationCount(t, socket, destination, unix.RT_TABLE_MAIN); count != 0 {
		t.Fatalf("unchanged prefix route emitted %d target notifications: target=%s table=%d notifications=%+v", count, destination, unix.RT_TABLE_MAIN, notifications)
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
	if count, notifications := tableRouteNotificationCount(t, socket, destination, unix.RT_TABLE_MAIN); count == 0 {
		t.Fatalf("changed prefix route did not emit a target notification: target=%s table=%d notifications=%+v", destination, unix.RT_TABLE_MAIN, notifications)
	}
	t.Logf("changed prefix route advanced forwarding generation from %d to %d", previous, waitTableRouteGeneration(t, events, previous))
	assertTableRoutePath(t, fixture, 1, fixture.gateways[1])
	if err := netif.ReconcileTableRoute(t.Context(), log, want); err != nil {
		t.Fatalf("reconcile unchanged gateway route: %v", err)
	}
	if count, notifications := tableRouteNotificationCount(t, socket, destination, unix.RT_TABLE_MAIN); count != 0 {
		t.Fatalf("unchanged gateway route emitted %d target notifications: target=%s table=%d notifications=%+v", count, destination, unix.RT_TABLE_MAIN, notifications)
	}
}
