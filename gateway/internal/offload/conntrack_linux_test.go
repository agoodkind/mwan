//go:build linux && netns

package offload_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/offload"
)

const (
	conntrackTestChild           = "MWAN_OFFLOAD_CONNTRACK_TEST_CHILD"
	conntrackTestTimeout         = 5 * time.Second
	conntrackEntryTimeout        = 120
	conntrackTCPStateEstablished = 3
	conntrackLiberalFlags        = 0x0800
)

func TestConntrackUpdater(t *testing.T) {
	if os.Getenv(conntrackTestChild) != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), binary, "-test.run=^TestConntrackUpdater$", "-test.v")
		command.Env = append(os.Environ(), conntrackTestChild+"=1")
		command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated conntrack test: %v: %s", err, output)
		}
		t.Log(string(output))
		return
	}
	connection, err := conntrack.Dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	for _, addresses := range [][2]string{
		{"192.0.2.1", "198.51.100.2"},
		{"2001:db8:1::1", "2001:db8:2::2"},
	} {
		t.Run(addresses[0], func(t *testing.T) {
			testConntrackFamily(t, connection, addresses)
		})
	}
}

func createTrackedFlow(t *testing.T, connection *conntrack.Conn, addresses [2]string, protocol uint8, zone uint16) conntrack.Flow {
	t.Helper()
	flow := conntrack.NewFlow(protocol, conntrack.StatusConfirmed|conntrack.StatusSeenReply|conntrack.StatusAssured,
		netip.MustParseAddr(addresses[0]), netip.MustParseAddr(addresses[1]),
		23456, 443, conntrackEntryTimeout, 73)
	flow.Zone = zone
	if protocol == unix.IPPROTO_TCP {
		flow.ProtoInfo.TCP = &conntrack.ProtoInfoTCP{
			State:               conntrackTCPStateEstablished,
			OriginalWindowScale: 7, ReplyWindowScale: 5,
			OriginalFlags: 0x03ff, ReplyFlags: 0x21ff,
		}
	}
	if err := connection.Create(flow); err != nil {
		t.Fatal(err)
	}
	return readTrackedFlow(t, connection, flow)
}

func readTrackedFlow(t *testing.T, connection *conntrack.Conn, flow conntrack.Flow) conntrack.Flow {
	t.Helper()
	current, err := connection.Get(conntrack.Flow{TupleOrig: flow.TupleOrig, Zone: flow.Zone})
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func testConntrackFamily(t *testing.T, connection *conntrack.Conn, addresses [2]string) {
	t.Helper()
	before := createTrackedFlow(t, connection, addresses, unix.IPPROTO_TCP, 0)
	otherZone := createTrackedFlow(t, connection, addresses, unix.IPPROTO_TCP, 23)
	udp := createTrackedFlow(t, connection, addresses, unix.IPPROTO_UDP, 0)
	updater, err := offload.NewConntrackUpdater(conntrackTestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = updater.Close() })

	invalid := []struct {
		name   string
		change func(*conntrack.Flow)
	}{
		{"wrong-id", func(flow *conntrack.Flow) { flow.ID ^= 1 }},
		{"missing-id", func(flow *conntrack.Flow) { flow.ID = 0 }},
		{"missing-entry", func(flow *conntrack.Flow) { flow.TupleOrig.Proto.SourcePort++ }},
		{"missing-port", func(flow *conntrack.Flow) { flow.TupleOrig.Proto.SourcePort = 0 }},
		{"missing-address", func(flow *conntrack.Flow) { flow.TupleOrig.IP.SourceAddress = netip.Addr{} }},
		{"mixed-family", func(flow *conntrack.Flow) {
			address := netip.MustParseAddr("203.0.113.1")
			if flow.TupleOrig.IP.SourceAddress.Is4() {
				address = netip.MustParseAddr("2001:db8:3::1")
			}
			flow.TupleOrig.IP.DestinationAddress = address
		}},
		{"mixed-reply-family", func(flow *conntrack.Flow) {
			flow.TupleReply.IP.SourceAddress = netip.MustParseAddr("2001:db8:3::1")
			flow.TupleReply.IP.DestinationAddress = netip.MustParseAddr("2001:db8:3::2")
			if flow.TupleOrig.IP.SourceAddress.Is6() {
				flow.TupleReply.IP.SourceAddress = netip.MustParseAddr("203.0.113.1")
				flow.TupleReply.IP.DestinationAddress = netip.MustParseAddr("203.0.113.2")
			}
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			flow := before
			test.change(&flow)
			if err := updater.SetLiberal(t.Context(), flow); err == nil {
				t.Fatal("invalid identity updated a connection")
			}
			requireSameTrackedFlow(t, before, readTrackedFlow(t, connection, before), false)
			requireSameTrackedFlow(t, otherZone, readTrackedFlow(t, connection, otherZone), false)
		})
	}
	if err := updater.SetLiberal(t.Context(), udp); err == nil {
		t.Fatal("UDP entry accepted TCP flag update")
	}
	requireSameTrackedFlow(t, udp, readTrackedFlow(t, connection, udp), false)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := updater.SetLiberal(canceled, before); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update: %v", err)
	}
	requireSameTrackedFlow(t, before, readTrackedFlow(t, connection, before), false)

	// These stale caller values must never replace current kernel metadata.
	input := before
	input.Timeout = 1
	input.Mark = 999
	input.Status = 0
	input.ProtoInfo.TCP = &conntrack.ProtoInfoTCP{State: 1}
	input.CountersOrig.Bytes = 999
	if err := updater.SetLiberal(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	requireSameTrackedFlow(t, before, readTrackedFlow(t, connection, before), true)
	requireSameTrackedFlow(t, otherZone, readTrackedFlow(t, connection, otherZone), false)
	if err := updater.SetLiberal(t.Context(), otherZone); err != nil {
		t.Fatal(err)
	}
	requireSameTrackedFlow(t, otherZone, readTrackedFlow(t, connection, otherZone), true)
	if err := updater.SetLiberal(t.Context(), before); err != nil {
		t.Fatalf("repeated flags update: %v", err)
	}
	requireSameTrackedFlow(t, before, readTrackedFlow(t, connection, before), true)
	if err := connection.Delete(before); err != nil {
		t.Fatal(err)
	}
	if err := updater.SetLiberal(t.Context(), before); err == nil {
		t.Fatal("deleted entry accepted an update")
	}
	if _, err := connection.Get(conntrack.Flow{TupleOrig: before.TupleOrig}); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("update recreated a deleted entry: %v", err)
	}
	if err := updater.Close(); err != nil {
		t.Fatal(err)
	}
	if err := updater.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if err := updater.SetLiberal(t.Context(), otherZone); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed updater accepted a request: %v", err)
	}
}

func requireSameTrackedFlow(t *testing.T, before, after conntrack.Flow, liberal bool) {
	t.Helper()
	// The kernel reports remaining whole seconds; the test never extends expiry.
	if after.Timeout > before.Timeout || after.Timeout+10 < before.Timeout {
		t.Fatalf("timeout changed beyond elapsed test time: %d to %d", before.Timeout, after.Timeout)
	}
	want := before
	want.Timeout = after.Timeout
	if liberal {
		if before.ProtoInfo.TCP == nil {
			t.Fatal("TCP entry did not provide protocol state")
		}
		protocol := *before.ProtoInfo.TCP
		protocol.OriginalFlags |= conntrackLiberalFlags
		protocol.ReplyFlags |= conntrackLiberalFlags
		want.ProtoInfo.TCP = &protocol
	}
	if !reflect.DeepEqual(want, after) {
		t.Fatalf("conntrack metadata differs:\nwant: %+v TCP=%+v\ngot: %+v TCP=%+v", want, want.ProtoInfo.TCP, after, after.ProtoInfo.TCP)
	}
}
