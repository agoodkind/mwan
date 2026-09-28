//go:build linux && netns

package netif

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestMonitorRebindsConfiguredLinkAfterRenameAndRecreation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-a", "obs-peer-a", "02:00:5e:00:53:71")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-a",
		Connection: &interfaceintent.Connection{
			ID: "stable-observer", Name: "obs-a",
			Link: &interfaceintent.Link{
				Kind:  interfaceintent.KindPhysical,
				Match: interfaceintent.Match{HardwareAddress: "02:00:5e:00:53:71"},
			},
		},
	})
	if monitor.IfIndex() != link.Attrs().Index {
		t.Fatalf("initial index = %d, want %d", monitor.IfIndex(), link.Attrs().Index)
	}
	if err := netlink.LinkSetName(link, "obs-renamed"); err != nil {
		t.Fatal(err)
	}
	addObservedAddress(t, link, "192.0.2.71/32")
	first := waitObservedAddress(t, monitor.Events, "192.0.2.71/32")
	if first.ConnectionID != "stable-observer" || first.ActualIface != "obs-renamed" || first.Iface != "obs-a" {
		t.Fatalf("renamed link identity = %+v", first)
	}
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	replacement := addObservedVeth(t, "obs-new", "obs-peer-b", "02:00:5e:00:53:71")
	addObservedAddress(t, replacement, "192.0.2.72/32")
	second := waitObservedAddress(t, monitor.Events, "192.0.2.72/32")
	if second.ConnectionID != "stable-observer" || second.ActualIface != "obs-new" || second.IfIndex != replacement.Attrs().Index {
		t.Fatalf("replacement link identity = %+v", second)
	}
}

func TestLegacyMonitorRebindsConfiguredNameAfterRename(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-legacy", "obs-legacy-peer", "02:00:5e:00:53:81")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-legacy"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index
	})
	if err := netlink.LinkSetName(link, "obs-legacy-old"); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	nextEvent := func() Event {
		select {
		case event := <-monitor.Events:
			if event.CIDR == "192.0.2.81/32" || event.Dest == "203.0.113.0/24" {
				t.Fatalf("renamed-away link was attributed to configured name: %+v", event)
			}
			if event.Snapshot != nil {
				for _, address := range event.Snapshot.Addresses {
					if address.CIDR == "192.0.2.81/32" {
						t.Fatalf("renamed-away address appeared in snapshot: %+v", event)
					}
				}
				for _, route := range event.Snapshot.Routes {
					if route.Dest == "203.0.113.0/24" {
						t.Fatalf("renamed-away route appeared in snapshot: %+v", event)
					}
				}
			}
			return event
		case <-deadline.C:
			t.Fatal("monitor event was not observed")
			return Event{}
		}
	}
	for {
		event := nextEvent()
		if event.Kind == EvResync && event.Snapshot != nil && event.Snapshot.IfIndex == 0 {
			break
		}
	}
	addObservedAddress(t, link, "192.0.2.81/32")
	_, oldDestination, err := net.ParseCIDR("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	second := addObservedVeth(t, "obs-other", "obs-other-peer", "02:00:5e:00:53:83")
	oldRoute := &netlink.Route{
		Dst: oldDestination, Family: netlink.FAMILY_V4,
		Table: 123, Protocol: 42, Scope: netlink.SCOPE_LINK,
		MultiPath: []*netlink.NexthopInfo{
			{LinkIndex: link.Attrs().Index, Hops: 0},
			{LinkIndex: second.Attrs().Index, Hops: 0},
		},
	}
	if err := netlink.RouteAdd(oldRoute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.RouteDel(oldRoute) })
	replacement := addObservedVeth(t, "obs-legacy", "obs-new-peer", "02:00:5e:00:53:82")
	if replacement.Attrs().Index == link.Attrs().Index {
		t.Fatal("replacement reused the renamed link index")
	}
	for {
		event := nextEvent()
		if event.Kind == EvResync && event.Snapshot != nil &&
			event.Snapshot.IfIndex == replacement.Attrs().Index && event.Snapshot.ActualIface == "obs-legacy" {
			break
		}
	}
	addObservedAddress(t, replacement, "192.0.2.82/32")
	for {
		event := nextEvent()
		if event.Kind != EvAddrAdded || event.CIDR != "192.0.2.82/32" {
			continue
		}
		if event.IfIndex != replacement.Attrs().Index || event.ActualIface != "obs-legacy" {
			t.Fatalf("replacement address binding = %+v", event)
		}
		break
	}
	_, newDestination, err := net.ParseCIDR("203.0.114.0/24")
	if err != nil {
		t.Fatal(err)
	}
	newRoute := &netlink.Route{
		Dst: newDestination, Family: netlink.FAMILY_V4,
		Table: 123, Protocol: 42, Scope: netlink.SCOPE_LINK,
		MultiPath: []*netlink.NexthopInfo{
			{LinkIndex: replacement.Attrs().Index, Hops: 0},
			{LinkIndex: second.Attrs().Index, Hops: 0},
		},
	}
	if err := netlink.RouteAdd(newRoute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.RouteDel(newRoute) })
	for {
		event := nextEvent()
		if event.Kind != EvRouteAdded || event.Dest != "203.0.114.0/24" {
			continue
		}
		if event.IfIndex != replacement.Attrs().Index {
			t.Fatalf("replacement route binding = %+v", event)
		}
		return
	}
}

func TestMonitorReportsIPv6AddressAndDefaultRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-family", "obs-family-peer", "02:00:5e:00:53:84")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-family"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index
	})
	addObservedAddress(t, link, "2001:db8:84::1/64")
	ipv6 := waitObservedAddress(t, monitor.Events, "2001:db8:84::1/64")
	if ipv6.Family != "inet6" || ipv6.IfIndex != link.Attrs().Index {
		t.Fatalf("IPv6 address event = %+v", ipv6)
	}
	addObservedAddress(t, link, "192.0.2.84/24")
	waitObservedAddress(t, monitor.Events, "192.0.2.84/24")
	defaultRoute := &netlink.Route{
		LinkIndex: link.Attrs().Index, Family: netlink.FAMILY_V4,
		Gw: net.ParseIP("192.0.2.1"), Table: 123, Protocol: 42,
	}
	if err := netlink.RouteAdd(defaultRoute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.RouteDel(defaultRoute) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-monitor.Events:
			if event.Kind != EvRouteAdded || event.Dest != "default" {
				continue
			}
			if event.Family != "inet" || event.Via != "192.0.2.1" || event.IfIndex != link.Attrs().Index {
				t.Fatalf("default route event = %+v", event)
			}
			goto checkIPv6Route
		case <-deadline.C:
			t.Fatal("default route was not observed")
		}
	}
checkIPv6Route:
	_, destination, err := net.ParseCIDR("2001:db8:85::/64")
	if err != nil {
		t.Fatal(err)
	}
	ipv6Route := &netlink.Route{
		LinkIndex: link.Attrs().Index, Dst: destination, Family: netlink.FAMILY_V6,
		Table: 123, Protocol: 42, Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(ipv6Route); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.RouteDel(ipv6Route) })
	for {
		select {
		case event := <-monitor.Events:
			if event.Kind != EvRouteAdded || event.Dest != "2001:db8:85::/64" {
				continue
			}
			if event.Family != "inet6" {
				t.Fatalf("IPv6 route addition = %+v", event)
			}
			goto deleteIPv6Route
		case <-deadline.C:
			t.Fatal("IPv6 route addition was not observed")
		}
	}
deleteIPv6Route:
	if err := netlink.RouteDel(ipv6Route); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-monitor.Events:
			if event.Kind != EvRouteDeleted || event.Dest != "2001:db8:85::/64" {
				continue
			}
			if event.Family != "inet6" || event.IfIndex != link.Attrs().Index {
				t.Fatalf("IPv6 route deletion = %+v", event)
			}
			return
		case <-deadline.C:
			t.Fatal("IPv6 route deletion was not observed")
		}
	}
}

func TestMonitorIdentifiesDeletedRouteAmongMatchingDestinations(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-route", "obs-route-peer", "02:00:5e:00:53:85")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-route"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index
	})

	_, destination, err := net.ParseCIDR("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	first := &netlink.Route{
		LinkIndex: link.Attrs().Index, Dst: destination, Family: netlink.FAMILY_V4,
		Table: 123, Protocol: 42, Scope: netlink.SCOPE_LINK, Priority: 77,
	}
	second := &netlink.Route{
		LinkIndex: link.Attrs().Index, Dst: destination, Family: netlink.FAMILY_V4,
		Table: 124, Protocol: 43, Scope: netlink.SCOPE_LINK, Priority: 88,
	}
	for _, route := range []*netlink.Route{first, second} {
		if err := netlink.RouteAdd(route); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = netlink.RouteDel(route) })
	}
	seen := map[int]bool{}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(seen) < 2 {
		select {
		case event := <-monitor.Events:
			if event.Kind == EvRouteAdded && event.Dest == destination.String() {
				seen[event.TableID] = true
			}
		case <-deadline.C:
			t.Fatalf("route additions observed in tables %v", seen)
		}
	}
	if err := netlink.RouteDel(first); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-monitor.Events:
			if event.Kind != EvRouteDeleted || event.Dest != destination.String() {
				continue
			}
			if event.Family != "inet" || event.TableID != 123 || event.Protocol != 42 ||
				event.Metric != 77 || event.IfIndex != link.Attrs().Index ||
				event.Scope != int(netlink.SCOPE_LINK) || len(event.NextHops) != 1 ||
				event.NextHops[0].LinkIndex != link.Attrs().Index {
				t.Fatalf("deleted route identity = %+v", event)
			}
			fresh := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-route"})
			snapshot := waitObservedSnapshot(t, fresh.Events, func(snapshot *Snapshot) bool {
				return snapshot.IfIndex == link.Attrs().Index
			})
			for _, current := range snapshot.Routes {
				if current.Dest != destination.String() {
					continue
				}
				if current.TableID != 124 || current.Protocol != 43 || current.Metric != 88 {
					t.Fatalf("remaining route identity = %+v", current)
				}
				return
			}
			t.Fatalf("route in table 124 missing from snapshot: %+v", snapshot.Routes)
		case <-deadline.C:
			t.Fatal("route deletion was not observed")
		}
	}
}

func TestMonitorDistinguishesSnapshotReplayFromNewAddress(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-replay", "obs-replay-peer", "02:00:5e:00:53:77")
	addObservedAddress(t, link, "192.0.2.77/32")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-replay"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index
	})
	replayed := waitObservedAddress(t, monitor.Events, "192.0.2.77/32")
	if !replayed.SnapshotReplay {
		t.Fatalf("existing address was reported as new: %+v", replayed)
	}
	addObservedAddress(t, link, "192.0.2.78/32")
	added := waitObservedAddress(t, monitor.Events, "192.0.2.78/32")
	if added.SnapshotReplay {
		t.Fatalf("new address was reported as replayed: %+v", added)
	}
}

func TestMonitorReplaysExistingAddressBeforeItsDeletion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-order", "obs-order-peer", "02:00:5e:00:53:79")
	addObservedAddress(t, link, "192.0.2.79/32")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-order"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		for _, address := range snapshot.Addresses {
			if address.CIDR == "192.0.2.79/32" {
				return true
			}
		}
		return false
	})
	address, err := netlink.ParseAddr("192.0.2.79/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(link, address); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	seenReplay := false
	for {
		select {
		case event := <-monitor.Events:
			if event.CIDR != "192.0.2.79/32" {
				continue
			}
			if event.Kind == EvAddrAdded && event.SnapshotReplay {
				seenReplay = true
			}
			if event.Kind == EvAddrDeleted {
				if !seenReplay {
					t.Fatal("address deletion preceded its snapshot replay")
				}
				return
			}
		case <-deadline.C:
			t.Fatal("address deletion was not observed")
		}
	}
}

func TestMonitorResyncsAfterAnUnconsumedEventBurst(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-b", "obs-peer-c", "02:00:5e:00:53:72")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-b"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index
	})
	const burstSize = 160
	for i := 1; i <= burstSize; i++ {
		addObservedAddress(t, link, fmt.Sprintf("198.18.1.%d/32", i))
	}
	time.Sleep(100 * time.Millisecond)
	observed := map[string]bool{}
	recovered := false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !recovered || len(observed) < burstSize {
		select {
		case event := <-monitor.Events:
			if event.Kind == EvResync && event.Snapshot != nil {
				recovered = true
				clear(observed)
				for _, address := range event.Snapshot.Addresses {
					if strings.HasPrefix(address.CIDR, "198.18.1.") {
						observed[address.CIDR] = true
					}
				}
			} else if event.Kind == EvAddrAdded && strings.HasPrefix(event.CIDR, "198.18.1.") {
				observed[event.CIDR] = true
			}
		case <-deadline.C:
			t.Fatalf("recovered address set has %d entries after resync=%t, want %d", len(observed), recovered, burstSize)
		}
	}
	cancel()
}

func TestMonitorReportsEveryNextHopForAWatchedMultipathRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := addObservedVeth(t, "obs-c", "obs-peer-d", "02:00:5e:00:53:73")
	second := addObservedVeth(t, "obs-d", "obs-peer-e", "02:00:5e:00:53:74")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-d"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == second.Attrs().Index
	})
	_, destination, err := net.ParseCIDR("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		Dst: destination, Family: netlink.FAMILY_V4,
		Table: 123, Protocol: 42, Priority: 77, Scope: netlink.SCOPE_LINK,
		MultiPath: []*netlink.NexthopInfo{
			{LinkIndex: first.Attrs().Index, Hops: 0},
			{LinkIndex: second.Attrs().Index, Hops: 1},
		},
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.RouteDel(route) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-monitor.Events:
			if event.Kind != EvRouteAdded || event.Dest != "203.0.113.0/24" {
				continue
			}
			if event.TableID != 123 || event.Protocol != 42 || event.Metric != 77 ||
				len(event.NextHops) != 2 || event.NextHops[1].LinkIndex != second.Attrs().Index {
				t.Fatalf("multipath route observation = %+v", event)
			}
			fresh := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-d"})
			snapshot := waitObservedSnapshot(t, fresh.Events, func(snapshot *Snapshot) bool {
				return snapshot.IfIndex == second.Attrs().Index
			})
			found := false
			for _, current := range snapshot.Routes {
				if current.Dest == "203.0.113.0/24" && current.TableID == 123 && len(current.NextHops) == 2 {
					found = true
				}
			}
			if !found {
				t.Fatalf("complete snapshot omitted the multipath route: %+v", snapshot.Routes)
			}
			return
		case <-deadline.C:
			t.Fatal("multipath route was not observed")
		}
	}
}

func TestMonitorStopsReportingAfterCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := addObservedVeth(t, "obs-e", "obs-peer-f", "02:00:5e:00:53:75")
	ctx, cancel := context.WithCancel(context.Background())
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-e"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index
	})
	addObservedAddress(t, link, "192.0.2.74/32")
	queuedDeadline := time.Now().Add(2 * time.Second)
	for len(monitor.Events) == 0 && time.Now().Before(queuedDeadline) {
		time.Sleep(time.Millisecond)
	}
	if len(monitor.Events) == 0 {
		t.Fatal("monitor did not queue an event before cancellation")
	}
	cancel()
	select {
	case <-monitor.done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
	select {
	case event := <-monitor.Events:
		t.Fatalf("monitor retained a queued event after cancellation: %+v", event)
	default:
	}
	addObservedAddress(t, link, "192.0.2.75/32")
	deadline := time.NewTimer(100 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case event := <-monitor.Events:
			if event.CIDR == "192.0.2.75/32" {
				t.Fatal("monitor reported an address after cancellation")
			}
		case <-deadline.C:
			return
		}
	}
}

func TestMonitorSnapshotsLinkAppearanceAndDeletion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: "obs-late"})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == 0
	})
	link := addObservedVeth(t, "obs-late", "obs-late-peer", "02:00:5e:00:53:78")
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == link.Attrs().Index && snapshot.ActualIface == "obs-late"
	})
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == 0
	})
}

func TestMonitorRejectsAmbiguousPhysicalAndWrongVirtualLinks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := addObservedVeth(t, "obs-f", "obs-peer-g", "02:00:5e:00:53:76")
	second := addObservedVeth(t, "obs-g", "obs-peer-h", "02:00:5e:00:53:76")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	physical := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-f",
		Connection: &interfaceintent.Connection{
			ID: "ambiguous", Name: "obs-f",
			Link: &interfaceintent.Link{
				Kind:  interfaceintent.KindPhysical,
				Match: interfaceintent.Match{HardwareAddress: "02:00:5e:00:53:76"},
			},
		},
	})
	if physical.IfIndex() != 0 {
		t.Fatalf("ambiguous physical match selected index %d", physical.IfIndex())
	}
	if err := netlink.LinkDel(second); err != nil {
		t.Fatal(err)
	}
	addObservedAddress(t, first, "192.0.2.76/32")
	observed := waitObservedAddress(t, physical.Events, "192.0.2.76/32")
	if observed.IfIndex != first.Attrs().Index {
		t.Fatalf("unique physical match selected index %d", observed.IfIndex)
	}

	parent := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "obs-parent"}}
	if err := netlink.LinkAdd(parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(parent) })
	parentLink, err := netlink.LinkByName("obs-parent")
	if err != nil {
		t.Fatal(err)
	}
	vlan := &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "obs-vlan", ParentIndex: parentLink.Attrs().Index}, VlanId: 101}
	if err := netlink.LinkAdd(vlan); err != nil {
		t.Fatal(err)
	}
	vlanLink, err := netlink.LinkByName("obs-vlan")
	if err != nil {
		t.Fatal(err)
	}
	wrong := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-vlan",
		Connection: &interfaceintent.Connection{
			ID: "wrong-vlan", Name: "obs-vlan",
			Link: &interfaceintent.Link{
				Kind: interfaceintent.KindVLAN,
				VLAN: &interfaceintent.VLAN{Parent: "obs-parent", ID: 102},
			},
		},
	})
	if wrong.IfIndex() != 0 {
		t.Fatalf("wrong VLAN tag selected index %d", wrong.IfIndex())
	}
	correct := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-vlan",
		Connection: &interfaceintent.Connection{
			ID: "correct-vlan", Name: "obs-vlan",
			Link: &interfaceintent.Link{
				Kind: interfaceintent.KindVLAN,
				VLAN: &interfaceintent.VLAN{Parent: "obs-parent", ID: 101},
			},
		},
	})
	if correct.IfIndex() != vlanLink.Attrs().Index {
		t.Fatalf("valid VLAN selected index %d, want %d", correct.IfIndex(), vlanLink.Attrs().Index)
	}
	bridge := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-vlan",
		Connection: &interfaceintent.Connection{
			ID: "not-bridge", Name: "obs-vlan",
			Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
		},
	})
	if bridge.IfIndex() != 0 {
		t.Fatalf("VLAN was accepted as a bridge at index %d", bridge.IfIndex())
	}
}

func TestMonitorTracksRenamedVLANByParentTagAndProtocol(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "obs-v-parent"}}
	if err := netlink.LinkAdd(parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(parent) })
	parentLink, err := netlink.LinkByName("obs-v-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(parentLink); err != nil {
		t.Fatal(err)
	}
	makeVLAN := func(name string, protocol netlink.VlanProtocol) netlink.Link {
		vlan := &netlink.Vlan{
			LinkAttrs: netlink.LinkAttrs{Name: name, ParentIndex: parentLink.Attrs().Index},
			VlanId:    101, VlanProtocol: protocol,
		}
		if err := netlink.LinkAdd(vlan); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	otherProtocol := makeVLAN("obs-v-ad", netlink.VLAN_PROTOCOL_8021AD)
	configured := makeVLAN("obs-vlan", netlink.VLAN_PROTOCOL_8021Q)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-vlan",
		Connection: &interfaceintent.Connection{
			ID: "vlan-observer", Name: "obs-vlan",
			Link: &interfaceintent.Link{
				Kind: interfaceintent.KindVLAN,
				VLAN: &interfaceintent.VLAN{Parent: "obs-v-parent", ID: 101},
			},
		},
	})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == configured.Attrs().Index
	})
	if err := netlink.LinkSetName(configured, "obs-v-renamed"); err != nil {
		t.Fatal(err)
	}
	addObservedAddress(t, otherProtocol, "192.0.2.90/32")
	addObservedAddress(t, configured, "192.0.2.89/32")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-monitor.Events:
			if event.CIDR == "192.0.2.90/32" {
				t.Fatalf("802.1ad address attributed to 802.1Q connection: %+v", event)
			}
			if event.Kind != EvAddrAdded || event.CIDR != "192.0.2.89/32" {
				continue
			}
			if event.ConnectionID != "vlan-observer" || event.IfIndex != configured.Attrs().Index ||
				event.ActualIface != "obs-v-renamed" {
				t.Fatalf("renamed VLAN address identity = %+v", event)
			}
			goto verifyOtherProtocol
		case <-deadline.C:
			t.Fatal("renamed VLAN address was not observed")
		}
	}
verifyOtherProtocol:
	if err := netlink.LinkDel(configured); err != nil {
		t.Fatal(err)
	}
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == 0
	})
	addObservedAddress(t, otherProtocol, "192.0.2.91/32")
	otherDeadline := time.NewTimer(100 * time.Millisecond)
	defer otherDeadline.Stop()
	for {
		select {
		case event := <-monitor.Events:
			if event.CIDR == "192.0.2.91/32" || event.Snapshot != nil && event.Snapshot.IfIndex == otherProtocol.Attrs().Index {
				t.Fatalf("802.1ad link replaced configured 802.1Q connection: %+v", event)
			}
		case <-otherDeadline.C:
			return
		}
	}
}

func waitObservedSnapshot(t *testing.T, events <-chan Event, matches func(*Snapshot) bool) *Snapshot {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.Kind == EvResync && event.Snapshot != nil && matches(event.Snapshot) {
				return event.Snapshot
			}
		case <-deadline.C:
			t.Fatal("complete kernel snapshot was not observed")
		}
	}
}

func addObservedVeth(t *testing.T, name string, peerName string, mac string) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peerName}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	hardware, err := net.ParseMAC(mac)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetHardwareAddr(link, hardware); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(link) })
	return link
}

func addObservedAddress(t *testing.T, link netlink.Link, address string) {
	t.Helper()
	parsed, err := netlink.ParseAddr(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, parsed); err != nil {
		t.Fatal(err)
	}
}

func waitObservedAddress(t *testing.T, events <-chan Event, address string) Event {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.Kind == EvAddrAdded && event.CIDR == address {
				return event
			}
		case <-deadline.C:
			t.Fatalf("address %s was not observed", address)
		}
	}
}
