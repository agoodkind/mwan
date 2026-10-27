//go:build linux && netns

package offload

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"
)

const (
	conntrackEventsChild           = "MWAN_CONNTRACK_EVENTS_CHILD"
	conntrackEventsTimeout         = 5 * time.Second
	conntrackEventsCapacity        = 8
	conntrackEventsFlowTimeout     = 120
	conntrackEventsTCPState        = 3
	conntrackEventsMark            = 73
	conntrackEventsDestinationPort = 443
)

func TestConntrackEvents(t *testing.T) {
	if !conntrackEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), conntrackEventsTimeout)
	defer cancel()
	source, err := NewConntrackEvents(ctx, conntrackEventsCapacity, conntrackReceiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	connection := conntrackEventsConnection(t)
	for _, addresses := range [][2]string{
		{"192.0.2.1", "198.51.100.2"},
		{"2001:db8:1::1", "2001:db8:2::2"},
	} {
		for _, protocol := range []uint8{unix.IPPROTO_TCP, unix.IPPROTO_UDP} {
			before := conntrackEventsCreate(t, connection, addresses, protocol, 23456)
			event := awaitConntrackEvent(t, ctx, source)
			if event.Destroyed || event.ReceivedAt.IsZero() || event.Flow.ID != before.ID || event.Flow.TupleOrig != before.TupleOrig || event.Flow.TupleReply != before.TupleReply {
				t.Fatalf("creation did not publish the kernel identity: %+v", event)
			}
			current, err := FreshConntrackFlow(ctx, before)
			if err != nil || current.Mark != before.Mark || current.ID != before.ID {
				t.Fatalf("fresh lookup did not read the current kernel connection: %+v: %v", current, err)
			}
			for _, tuple := range []conntrack.Tuple{before.TupleOrig, before.TupleReply} {
				lookedUp, err := LookupConntrackFlow(ctx, tuple, before.Zone)
				if err != nil || lookedUp.ID != before.ID || lookedUp.TupleOrig != before.TupleOrig || lookedUp.TupleReply != before.TupleReply {
					t.Fatalf("tuple lookup did not identify the existing connection: %+v: %v", lookedUp, err)
				}
			}
			if _, err := LookupConntrackFlow(ctx, before.TupleOrig, 23); !errors.Is(err, unix.ENOENT) {
				t.Fatalf("tuple lookup accepted a different conntrack zone: %v", err)
			}
			stale := before
			stale.ID++
			if _, err := FreshConntrackFlow(ctx, stale); err == nil {
				t.Fatal("fresh lookup accepted a stale connection ID")
			}
			stale = before
			stale.TupleReply.Proto.DestinationPort++
			if _, err := FreshConntrackFlow(ctx, stale); !errors.Is(err, unix.ESTALE) {
				t.Fatalf("fresh lookup accepted a changed reply tuple: %v", err)
			}
			if err := connection.Update(conntrack.Flow{TupleOrig: before.TupleOrig, TupleReply: before.TupleReply, Mark: before.Mark + 1}); err != nil {
				t.Fatalf("update kernel connection %s protocol %d mark: %v", before.TupleOrig.IP.SourceAddress, protocol, err)
			}
			event = awaitConntrackEvent(t, ctx, source)
			if event.Destroyed || event.Flow.ID != before.ID || event.Flow.Mark != before.Mark+1 {
				t.Fatalf("update did not publish the changed kernel mark: %+v", event)
			}
			current, err = FreshConntrackFlow(ctx, event.Flow)
			if err != nil || current.Mark != event.Flow.Mark {
				t.Fatalf("fresh lookup did not preserve the changed kernel mark: %+v: %v", current, err)
			}
			if err := connection.Delete(before); err != nil {
				t.Fatal(err)
			}
			event = awaitConntrackEvent(t, ctx, source)
			if !event.Destroyed || event.Flow.ID != before.ID || event.Flow.TupleOrig != before.TupleOrig {
				t.Fatalf("deletion did not publish the destroyed kernel identity: %+v", event)
			}
			if _, err := FreshConntrackFlow(ctx, before); !errors.Is(err, unix.ENOENT) {
				t.Fatalf("fresh lookup accepted a deleted connection: %v", err)
			}
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("repeated event source close: %v", err)
	}
	if failure, open := <-source.Errors; open {
		t.Fatalf("explicit source close reported loss: %v", failure)
	}
	if _, open := <-source.Events; open {
		t.Fatal("closed source continued producing events")
	}
}

func TestConntrackTimeoutRefresh(t *testing.T) {
	if !conntrackEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), conntrackEventsTimeout)
	defer cancel()
	connection := conntrackEventsConnection(t)
	updater, err := NewConntrackUpdater(conntrackEventsTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := updater.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, addresses := range [][2]string{
		{"192.0.2.1", "198.51.100.2"},
		{"2001:db8:1::1", "2001:db8:2::2"},
	} {
		for _, protocol := range []uint8{unix.IPPROTO_TCP, unix.IPPROTO_UDP} {
			before := conntrackEventsCreate(t, connection, addresses, protocol, 23456)
			stale := before
			stale.ID++
			if err := updater.Refresh(ctx, stale, conntrackEventsFlowTimeout*2); err == nil {
				t.Fatal("timeout refresh accepted a stale connection ID")
			}
			stale = before
			stale.TupleReply.Proto.DestinationPort++
			if err := updater.Refresh(ctx, stale, conntrackEventsFlowTimeout*2); !errors.Is(err, unix.ESTALE) {
				t.Fatalf("timeout refresh accepted a changed reply tuple: %v", err)
			}
			input := before
			input.Timeout, input.Mark, input.Status = 1, 999, 0
			input.ProtoInfo.TCP = nil
			if err := updater.Refresh(ctx, input, conntrackEventsFlowTimeout*2); err != nil {
				t.Fatal(err)
			}
			after, err := FreshConntrackFlow(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			if after.Timeout <= before.Timeout || after.Timeout > conntrackEventsFlowTimeout*2 {
				t.Fatalf("timeout refresh did not extend kernel expiry: %d to %d", before.Timeout, after.Timeout)
			}
			want := before
			want.Timeout = after.Timeout
			if !reflect.DeepEqual(want, after) {
				t.Fatalf("timeout refresh changed other kernel metadata: want=%+v got=%+v", want, after)
			}
			if err := connection.Delete(before); err != nil {
				t.Fatal(err)
			}
			if err := updater.Refresh(ctx, before, conntrackEventsFlowTimeout*2); !errors.Is(err, unix.ENOENT) {
				t.Fatalf("timeout refresh accepted a deleted connection: %v", err)
			}
			if _, err := LookupConntrackFlow(ctx, before.TupleOrig, before.Zone); !errors.Is(err, unix.ENOENT) {
				t.Fatalf("timeout refresh recreated a deleted connection: %v", err)
			}
		}
	}
}

func TestConntrackEventCapacityLoss(t *testing.T) {
	if !conntrackEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), conntrackEventsTimeout)
	defer cancel()
	source, err := NewConntrackEvents(ctx, 1, conntrackReceiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	connection := conntrackEventsConnection(t)
	addresses := [2]string{"192.0.2.1", "198.51.100.2"}
	first := conntrackEventsCreate(t, connection, addresses, unix.IPPROTO_UDP, 23456)
	conntrackEventsCreate(t, connection, addresses, unix.IPPROTO_UDP, 23457)
	timer := time.NewTicker(time.Millisecond)
	defer timer.Stop()
	for source.Err() == nil {
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal("event source did not retain capacity loss before notification")
		}
	}
	select {
	case failure, open := <-source.Errors:
		var loss *ConntrackEventLossError
		if !open || !errors.As(failure, &loss) || !errors.Is(failure, unix.ENOBUFS) {
			t.Fatalf("capacity loss did not invalidate the event source: %v", failure)
		}
		if source.Err() != failure {
			t.Fatalf("event source did not retain the notified failure: %v", source.Err())
		}
	case <-ctx.Done():
		t.Fatal("event source did not report capacity loss")
	}
	event, open := <-source.Events
	if !open || event.Flow.ID != first.ID {
		t.Fatalf("capacity loss discarded the accepted first event: %+v", event)
	}
	if _, open := <-source.Events; open {
		t.Fatal("event source continued after capacity loss")
	}
}

func TestConntrackEventCancellation(t *testing.T) {
	if !conntrackEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	source, err := NewConntrackEvents(ctx, conntrackEventsCapacity, conntrackReceiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, open := <-source.Events:
		if open {
			t.Fatal("canceled event source published an event")
		}
	case <-time.After(conntrackEventsTimeout):
		t.Fatal("cancellation did not interrupt the blocked kernel receive")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if failure, open := <-source.Errors; open {
		t.Fatalf("cancellation reported event loss: %v", failure)
	}
	if err := source.Err(); err != nil {
		t.Fatalf("cancellation retained a connection event failure: %v", err)
	}
	if _, err := FreshConntrackFlow(ctx, conntrack.Flow{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fresh lookup attempted a kernel request: %v", err)
	}
}

func TestConntrackReceiveRejectsTruncatedDatagram(t *testing.T) {
	receiver, err := netlink.Dial(unix.NETLINK_USERSOCK, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := receiver.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := receiver.SetReadBuffer(conntrackReceiveBytes * 2); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetReadDeadline(time.Now().Add(conntrackEventsTimeout)); err != nil {
		t.Fatal(err)
	}
	raw, err := receiver.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_USERSOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Close(sender); err != nil {
			t.Error(err)
		}
	})
	if err := unix.Sendmsg(sender, make([]byte, conntrackReceiveBytes+1), nil, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: receiver.PID()}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := receiveConntrackDatagram(raw, make([]byte, conntrackReceiveBytes)); !errors.Is(err, unix.EMSGSIZE) {
		t.Fatalf("oversized netlink datagram did not report truncation: %v", err)
	}
}

func conntrackEventsNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(conntrackEventsChild) == t.Name() {
		return true
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "-test.run=^"+t.Name()+"$", "-test.v")
	command.Env = append(os.Environ(), conntrackEventsChild+"="+t.Name())
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated conntrack events: %v: %s", err, output)
	}
	t.Log(string(output))
	return false
}

func conntrackEventsConnection(t *testing.T) *conntrack.Conn {
	t.Helper()
	connection, err := conntrack.Dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	return connection
}

func conntrackEventsCreate(t *testing.T, connection *conntrack.Conn, addresses [2]string, protocol uint8, sourcePort uint16) conntrack.Flow {
	t.Helper()
	flow := conntrack.NewFlow(protocol, conntrack.StatusConfirmed|conntrack.StatusSeenReply|conntrack.StatusAssured,
		netip.MustParseAddr(addresses[0]), netip.MustParseAddr(addresses[1]), sourcePort,
		conntrackEventsDestinationPort, conntrackEventsFlowTimeout, conntrackEventsMark)
	if protocol == unix.IPPROTO_TCP {
		flow.ProtoInfo.TCP = &conntrack.ProtoInfoTCP{State: conntrackEventsTCPState}
	}
	if err := connection.Create(flow); err != nil {
		t.Fatal(err)
	}
	current, err := connection.Get(conntrack.Flow{TupleOrig: flow.TupleOrig})
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func awaitConntrackEvent(t *testing.T, ctx context.Context, source *ConntrackEvents) ConntrackEvent {
	t.Helper()
	select {
	case event, open := <-source.Events:
		if !open {
			t.Fatal("conntrack event source stopped before the expected notification")
		}
		return event
	case failure := <-source.Errors:
		t.Fatalf("conntrack event source failed: %v", failure)
	case <-ctx.Done():
		t.Fatal("kernel did not publish the expected conntrack event")
	}
	return ConntrackEvent{}
}
