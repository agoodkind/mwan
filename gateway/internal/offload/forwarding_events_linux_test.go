//go:build linux && netns

package offload

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	forwardingEventsChild            = "MWAN_FORWARDING_EVENTS_CHILD"
	forwardingEventsTimeout          = 5 * time.Second
	forwardingEventsPollInterval     = time.Millisecond
	forwardingEventsInterface        = "fw-events"
	forwardingEventsIPv6Mode         = "1"
	forwardingEventsCoalescedChanges = 5
)

func TestForwardingEventsObserveNeighborsAndRoutes(t *testing.T) {
	if !forwardingEventsNamespace(t) {
		return
	}
	link := forwardingEventsLink(t)
	ctx, cancel := context.WithTimeout(t.Context(), forwardingEventsTimeout)
	defer cancel()
	source := forwardingEventsSource(t, ctx)
	for _, test := range []struct {
		family   int
		neighbor string
		route    string
	}{
		{unix.AF_INET, "192.0.2.2", "198.51.100.0/24"},
		{unix.AF_INET6, "2001:db8:1::2", "2001:db8:2::/64"},
	} {
		neighbor := &netlink.Neigh{
			LinkIndex: link.Attrs().Index, Family: test.family, IP: net.ParseIP(test.neighbor),
			HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 2}, State: unix.NUD_PERMANENT,
		}
		previous := source.Generation()
		if err := netlink.NeighSet(neighbor); err != nil {
			t.Fatal(err)
		}
		awaitForwardingGeneration(t, ctx, source, previous)
		previous = source.Generation()
		neighbor.HardwareAddr = net.HardwareAddr{2, 0, 0, 0, 0, 3}
		if err := netlink.NeighSet(neighbor); err != nil {
			t.Fatal(err)
		}
		awaitForwardingGeneration(t, ctx, source, previous)
		previous = source.Generation()
		if err := netlink.NeighDel(neighbor); err != nil {
			t.Fatal(err)
		}
		awaitForwardingGeneration(t, ctx, source, previous)
		_, destination, err := net.ParseCIDR(test.route)
		if err != nil {
			t.Fatal(err)
		}
		route := &netlink.Route{
			LinkIndex: link.Attrs().Index, Family: test.family, Dst: destination,
			Scope: netlink.SCOPE_LINK, Table: unix.RT_TABLE_MAIN,
		}
		previous = source.Generation()
		if err := netlink.RouteAdd(route); err != nil {
			t.Fatal(err)
		}
		awaitForwardingGeneration(t, ctx, source, previous)
		previous = source.Generation()
		if err := netlink.RouteDel(route); err != nil {
			t.Fatal(err)
		}
		awaitForwardingGeneration(t, ctx, source, previous)
	}
	if err := source.Err(); err != nil {
		t.Fatalf("valid kernel changes stopped forwarding notifications: %v", err)
	}
}

func TestForwardingEventsCoalesceWithoutLosingGeneration(t *testing.T) {
	if !forwardingEventsNamespace(t) {
		return
	}
	link := forwardingEventsLink(t)
	ctx, cancel := context.WithTimeout(t.Context(), forwardingEventsTimeout)
	defer cancel()
	source := forwardingEventsSource(t, ctx)
	initial := source.Generation()
	previous := initial
	for index := range forwardingEventsCoalescedChanges {
		if err := netlink.NeighSet(&netlink.Neigh{
			LinkIndex: link.Attrs().Index, Family: unix.AF_INET, IP: net.ParseIP("192.0.2.2"),
			HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, byte(index + 2)}, State: unix.NUD_PERMANENT,
		}); err != nil {
			t.Fatal(err)
		}
		awaitForwardingGeneration(t, ctx, source, previous)
		previous = source.Generation()
	}
	if source.Generation() < initial+forwardingEventsCoalescedChanges {
		t.Fatal("coalesced changes did not retain every observed generation")
	}
	select {
	case _, open := <-source.Changes:
		if !open {
			t.Fatal("forwarding source stopped after valid changes")
		}
	default:
		t.Fatal("forwarding source did not notify the coalesced changes")
	}
	select {
	case <-source.Changes:
		t.Fatal("forwarding source did not coalesce pending changes")
	default:
	}
}

func TestForwardingEventsPublishStickyKernelFailure(t *testing.T) {
	if !forwardingEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), forwardingEventsTimeout)
	defer cancel()
	source := forwardingEventsSource(t, ctx)
	initial := source.Generation()
	// A notification socket cannot accept a query response as change evidence.
	if _, err := source.socket.Send(mdnetlink.Message{Header: mdnetlink.Header{
		Type: unix.RTM_GETLINK, Flags: mdnetlink.Request | mdnetlink.Acknowledge,
	}}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTicker(forwardingEventsPollInterval)
	defer timer.Stop()
	for source.Err() == nil {
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal("kernel rejection did not publish a sticky forwarding error")
		}
	}
	var loss *ForwardingEventLossError
	if !errors.As(source.Err(), &loss) {
		t.Fatalf("kernel rejection did not retain the loss error: %v", source.Err())
	}
	awaitForwardingGeneration(t, ctx, source, initial)
	select {
	case failure, open := <-source.Errors:
		if !open || source.Err() != failure {
			t.Fatalf("forwarding failure notification differs from sticky failure: %v", failure)
		}
	case <-ctx.Done():
		t.Fatal("forwarding source did not publish its terminal error")
	}
}

func TestForwardingEventsCancellation(t *testing.T) {
	if !forwardingEventsNamespace(t) {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	source := forwardingEventsSource(t, ctx)
	cancel()
	select {
	case _, open := <-source.Changes:
		if open {
			t.Fatal("canceled forwarding source published a mutation")
		}
	case <-time.After(forwardingEventsTimeout):
		t.Fatal("cancellation did not interrupt the forwarding receive")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("repeated forwarding source close: %v", err)
	}
	if failure, open := <-source.Errors; open {
		t.Fatalf("cancellation reported forwarding event loss: %v", failure)
	}
	if source.Err() != nil || source.Generation() != 0 {
		t.Fatalf("cancellation changed forwarding evidence: generation=%d error=%v", source.Generation(), source.Err())
	}
}

func forwardingEventsNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(forwardingEventsChild) == t.Name() {
		return true
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "-test.run=^"+t.Name()+"$", "-test.v")
	command.Env = append(os.Environ(), forwardingEventsChild+"="+t.Name())
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated forwarding events: %v: %s", err, output)
	}
	t.Log(string(output))
	return false
}

func forwardingEventsSource(t *testing.T, ctx context.Context) *ForwardingEvents {
	t.Helper()
	source, err := NewForwardingEvents(ctx, conntrackReceiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	return source
}

func forwardingEventsLink(t *testing.T) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: forwardingEventsInterface}}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(forwardingEventsInterface)
	if err != nil {
		t.Fatal(err)
	}
	// Automatic IPv6 address creation would add unrelated setup notifications.
	mode := filepath.Join("/proc/sys/net/ipv6/conf", forwardingEventsInterface, "addr_gen_mode")
	if err := os.WriteFile(mode, []byte(forwardingEventsIPv6Mode), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	return link
}

func awaitForwardingGeneration(t *testing.T, ctx context.Context, source *ForwardingEvents, previous uint64) {
	t.Helper()
	timer := time.NewTicker(forwardingEventsPollInterval)
	defer timer.Stop()
	for source.Generation() <= previous {
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatalf("forwarding generation did not change after %d: %v", previous, source.Err())
		}
	}
}
