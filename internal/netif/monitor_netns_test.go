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
	cancel()
	select {
	case <-monitor.done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop after cancellation")
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
