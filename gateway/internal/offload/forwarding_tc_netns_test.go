//go:build linux && netns

package offload

import (
	"context"
	"testing"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

func TestForwardingEventsObserveTrafficClasses(t *testing.T) {
	if !forwardingEventsNamespace(t) {
		return
	}
	link := forwardingEventsLink(t)
	ctx, cancel := context.WithTimeout(t.Context(), forwardingEventsTimeout)
	defer cancel()
	source := forwardingEventsSource(t, ctx)
	queue := netlink.NewHtb(netlink.QdiscAttrs{LinkIndex: link.Attrs().Index, Handle: netlink.MakeHandle(1, 0), Parent: netlink.HANDLE_ROOT})
	if err := netlink.QdiscAdd(queue); err != nil {
		t.Fatal(err)
	}
	class := netlink.NewHtbClass(netlink.ClassAttrs{
		LinkIndex: link.Attrs().Index, Handle: netlink.MakeHandle(1, 1), Parent: queue.Handle,
	}, netlink.HtbClassAttrs{Rate: 1000000, Ceil: 1000000})
	previous := source.Generation()
	if err := netlink.ClassAdd(class); err != nil {
		t.Fatal(err)
	}
	awaitForwardingGeneration(t, ctx, source, previous)
	previous = source.Generation()
	if err := netlink.ClassDel(class); err != nil {
		t.Fatal(err)
	}
	awaitForwardingGeneration(t, ctx, source, previous)
	if err := source.Err(); err != nil {
		t.Fatalf("valid class mutations stopped forwarding observations: %v", err)
	}
}

func TestForwardingEventsObserveTrafficActions(t *testing.T) {
	if !forwardingEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), forwardingEventsTimeout)
	defer cancel()
	source := forwardingEventsSource(t, ctx)
	for _, kind := range []uint16{unix.RTM_NEWACTION, unix.RTM_DELACTION} {
		previous := source.Generation()
		changeForwardingTestAction(t, ctx, kind)
		awaitForwardingGeneration(t, ctx, source, previous)
	}
	if err := source.Err(); err != nil {
		t.Fatalf("valid action mutations stopped forwarding observations: %v", err)
	}
}

func changeForwardingTestAction(t *testing.T, ctx context.Context, kind uint16) {
	t.Helper()
	const index = 123
	attributes := nl.NewRtAttr(nl.TCA_ACT_TAB, nil)
	flags := mdnetlink.Request | mdnetlink.Acknowledge
	if kind == unix.RTM_NEWACTION {
		flags |= mdnetlink.Create | mdnetlink.Excl
		action := &netlink.GenericAction{ActionAttrs: netlink.ActionAttrs{Index: index, Action: netlink.TC_ACT_SHOT}}
		if err := netlink.EncodeActions(attributes, []netlink.Action{action}); err != nil {
			t.Fatal(err)
		}
	} else {
		action := attributes.AddRtAttr(1, nil)
		action.AddRtAttr(nl.TCA_ACT_KIND, nl.ZeroTerminated("gact"))
		action.AddRtAttr(nl.TCA_ACT_INDEX, nl.Uint32Attr(index))
	}
	header := nl.TcActionMsg{}
	socket, err := mdnetlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
	}()
	deadline, present := ctx.Deadline()
	if !present {
		t.Fatal("action fixture requires a deadline")
	}
	if err := socket.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.Execute(mdnetlink.Message{
		Header: mdnetlink.Header{Type: mdnetlink.HeaderType(kind), Flags: flags},
		Data:   append(header.Serialize(), attributes.Serialize()...),
	}); err != nil {
		t.Fatalf("change real traffic-control action: %v", err)
	}
}
