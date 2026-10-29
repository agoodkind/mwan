//go:build linux && netns

package netif

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
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

func TestMonitorRebindsPhysicalLinkAfterIndexReuse(t *testing.T) {
	const childEnv = "MWAN_INDEX_REUSE_TEST_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestMonitorRebindsPhysicalLinkAfterIndexReuse$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated index reuse test: %v: %s", err, output)
		}
		return
	}

	const configuredName = "obs-reuse"
	const replacementName = "obs-replaced"
	const mac = "02:00:5e:00:53:93"
	const connectionID = "index-reuse-observer"
	first := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: configuredName}, PeerName: "obs-peer-old",
	}
	if err := netlink.LinkAdd(first); err != nil {
		t.Fatal(err)
	}
	firstLink, err := netlink.LinkByName(configuredName)
	if err != nil {
		t.Fatal(err)
	}
	hardware, err := net.ParseMAC(mac)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetHardwareAddr(firstLink, hardware); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(firstLink); err != nil {
		t.Fatal(err)
	}
	oldIndex := firstLink.Attrs().Index
	connection := &interfaceintent.Connection{
		ID: connectionID, Name: configuredName,
		Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{HardwareAddress: mac},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: configuredName, Connection: connection})
	initial := waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == oldIndex
	})
	if initial.ConnectionID != connectionID || initial.ActualIface != configuredName {
		t.Fatalf("initial binding = %+v", initial)
	}
	addObservedAddress(t, firstLink, "192.0.2.93/32")
	oldAddress := waitObservedAddress(t, monitor.Events, "192.0.2.93/32")
	if oldAddress.IfIndex != oldIndex || oldAddress.ConnectionID != connectionID {
		t.Fatalf("initial address identity = %+v", oldAddress)
	}
	if err := netlink.LinkDel(firstLink); err != nil {
		t.Fatal(err)
	}
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == 0
	})

	unrelated := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: "obs-unrelated", Index: oldIndex},
		PeerName:  "obs-peer-other",
	}
	if err := netlink.LinkAdd(unrelated); err != nil {
		t.Fatalf("request unrelated index %d: %v", oldIndex, err)
	}
	unrelatedLink, err := netlink.LinkByName("obs-unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedLink.Attrs().Index != oldIndex {
		t.Fatalf("kernel assigned unrelated index %d, want %d", unrelatedLink.Attrs().Index, oldIndex)
	}
	unrelatedHardware, err := net.ParseMAC("02:00:5e:00:53:95")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetHardwareAddr(unrelatedLink, unrelatedHardware); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(unrelatedLink); err != nil {
		t.Fatal(err)
	}
	addObservedAddress(t, unrelatedLink, "192.0.2.95/32")
	unrelatedDeadline := time.NewTimer(100 * time.Millisecond)
	defer unrelatedDeadline.Stop()
	watchingUnrelated := true
	for watchingUnrelated {
		select {
		case event := <-monitor.Events:
			if event.CIDR == "192.0.2.95/32" ||
				event.Snapshot != nil && event.Snapshot.IfIndex == oldIndex {
				t.Fatalf("unrelated link attributed to configured connection: %+v", event)
			}
		case <-unrelatedDeadline.C:
			if monitor.IfIndex() != 0 {
				t.Fatalf("unrelated link bound at index %d", monitor.IfIndex())
			}
			watchingUnrelated = false
		}
	}
	unrelatedMonitor := NewMonitor(ctx, logger, MonitorConfig{Iface: configuredName, Connection: connection})
	unbound := waitObservedSnapshot(t, unrelatedMonitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == 0
	})
	if unbound.ActualIface != "" || len(unbound.Addresses) != 0 {
		t.Fatalf("unrelated link entered configured snapshot: %+v", unbound)
	}
	if err := netlink.LinkDel(unrelatedLink); err != nil {
		t.Fatal(err)
	}

	replacement := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: replacementName, Index: oldIndex},
		PeerName:  "obs-peer-new",
	}
	if err := netlink.LinkAdd(replacement); err != nil {
		t.Fatalf("request replacement index %d: %v", oldIndex, err)
	}
	replacementLink, err := netlink.LinkByName(replacementName)
	if err != nil {
		t.Fatal(err)
	}
	if replacementLink.Attrs().Index != oldIndex {
		t.Fatalf("kernel assigned replacement index %d, want %d", replacementLink.Attrs().Index, oldIndex)
	}
	if err := netlink.LinkSetHardwareAddr(replacementLink, hardware); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(replacementLink); err != nil {
		t.Fatal(err)
	}
	bound := waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == oldIndex && snapshot.ActualIface == replacementName
	})
	if bound.ConnectionID != connectionID {
		t.Fatalf("replacement binding = %+v", bound)
	}
	for _, address := range bound.Addresses {
		if address.CIDR == "192.0.2.93/32" {
			t.Fatalf("old address appeared on replacement: %+v", bound)
		}
	}
	addObservedAddress(t, replacementLink, "192.0.2.94/32")
	newAddress := waitObservedAddress(t, monitor.Events, "192.0.2.94/32")
	if newAddress.IfIndex != oldIndex || newAddress.ConnectionID != connectionID ||
		newAddress.ActualIface != replacementName || newAddress.Iface != configuredName {
		t.Fatalf("replacement address identity = %+v", newAddress)
	}
	fresh := NewMonitor(ctx, logger, MonitorConfig{Iface: configuredName, Connection: connection})
	final := waitObservedSnapshot(t, fresh.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == oldIndex && snapshot.ActualIface == replacementName
	})
	if final.ConnectionID != connectionID {
		t.Fatalf("fresh replacement binding = %+v", final)
	}
	var foundNew bool
	for _, address := range final.Addresses {
		if address.CIDR == "192.0.2.93/32" {
			t.Fatalf("old address appeared in fresh snapshot: %+v", final)
		}
		if address.CIDR == "192.0.2.94/32" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("fresh snapshot omitted replacement address: %+v", final)
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

func TestMonitorRebindsTypedBridgeAfterRename(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	addBridge := func(name string) netlink.Link {
		t.Helper()
		if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = netlink.LinkDel(link) })
		return link
	}
	old := addBridge("obs-bridge")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-bridge",
		Connection: &interfaceintent.Connection{
			ID: "bridge-observer", Name: "obs-bridge",
			Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
		},
	})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == old.Attrs().Index && snapshot.ActualIface == "obs-bridge"
	})
	if err := netlink.LinkSetName(old, "obs-bridge-old"); err != nil {
		t.Fatal(err)
	}
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == 0
	})
	if monitor.IfIndex() != 0 {
		t.Fatalf("renamed bridge remains bound at index %d", monitor.IfIndex())
	}
	addObservedAddress(t, old, "192.0.2.86/32")
	_, oldDestination, err := net.ParseCIDR("203.0.113.86/32")
	if err != nil {
		t.Fatal(err)
	}
	oldRoute := &netlink.Route{
		LinkIndex: old.Attrs().Index, Dst: oldDestination, Family: netlink.FAMILY_V4,
		Table: 123, Protocol: 42, Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(oldRoute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.RouteDel(oldRoute) })

	replacement := addBridge("obs-bridge")
	if replacement.Attrs().Index == old.Attrs().Index {
		t.Fatal("replacement bridge reused the renamed bridge index")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	nextEvent := func() Event {
		t.Helper()
		select {
		case event := <-monitor.Events:
			if event.CIDR == "192.0.2.86/32" || event.Dest == "203.0.113.86/32" {
				t.Fatalf("renamed bridge event was attributed to configured bridge: %+v", event)
			}
			if event.Snapshot != nil {
				for _, address := range event.Snapshot.Addresses {
					if address.CIDR == "192.0.2.86/32" {
						t.Fatalf("renamed bridge address appeared in snapshot: %+v", event)
					}
				}
				for _, route := range event.Snapshot.Routes {
					if route.Dest == "203.0.113.86/32" {
						t.Fatalf("renamed bridge route appeared in snapshot: %+v", event)
					}
				}
			}
			return event
		case <-deadline.C:
			t.Fatal("replacement bridge event was not observed")
			return Event{}
		}
	}
	for {
		event := nextEvent()
		if event.Kind == EvResync && event.Snapshot != nil &&
			event.Snapshot.IfIndex == replacement.Attrs().Index &&
			event.Snapshot.ConnectionID == "bridge-observer" &&
			event.Snapshot.ActualIface == "obs-bridge" {
			break
		}
	}
	addObservedAddress(t, replacement, "192.0.2.87/32")
	for {
		event := nextEvent()
		if event.Kind != EvAddrAdded || event.CIDR != "192.0.2.87/32" {
			continue
		}
		if event.IfIndex != replacement.Attrs().Index ||
			event.ConnectionID != "bridge-observer" || event.ActualIface != "obs-bridge" {
			t.Fatalf("replacement bridge address identity = %+v", event)
		}
		break
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
	logFile, err := os.CreateTemp(t.TempDir(), "monitor-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, nil))
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
	overflowDeadline := time.Now().Add(5 * time.Second)
	for {
		log, err := os.ReadFile(logFile.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(log), "monitor: Events channel full; requesting snapshot") {
			break
		}
		if time.Now().After(overflowDeadline) {
			t.Fatal("monitor did not report event-channel overflow")
		}
		time.Sleep(20 * time.Millisecond)
	}
	kernelAddresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, address := range kernelAddresses {
		if strings.HasPrefix(address.IPNet.String(), "198.18.1.") {
			want[address.IPNet.String()] = true
		}
	}
	if len(want) != burstSize {
		t.Fatalf("kernel burst address set has %d entries, want %d", len(want), burstSize)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	snapshotMatched := false
	replayed := map[string]bool{}
	for len(replayed) < len(want) {
		select {
		case event := <-monitor.Events:
			if event.Kind == EvAddrAdded && event.SnapshotReplay && snapshotMatched {
				if want[event.CIDR] {
					replayed[event.CIDR] = true
				}
				continue
			}
			if event.Kind != EvResync || event.Snapshot == nil {
				continue
			}
			if event.Snapshot.IfIndex != link.Attrs().Index {
				t.Fatalf("recovery snapshot link index = %d, want %d", event.Snapshot.IfIndex, link.Attrs().Index)
			}
			observed := map[string]bool{}
			for _, address := range event.Snapshot.Addresses {
				if strings.HasPrefix(address.CIDR, "198.18.1.") {
					observed[address.CIDR] = true
				}
			}
			snapshotMatched = len(observed) == len(want)
			if snapshotMatched {
				for address := range want {
					if !observed[address] {
						snapshotMatched = false
						break
					}
				}
			}
			clear(replayed)
		case <-deadline.C:
			t.Fatalf("recovery snapshot matched=%t, replayed=%d of %d kernel addresses",
				snapshotMatched, len(replayed), len(want))
		}
	}
	const nextAddress = "198.18.1.161/32"
	logBeforeDelta, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	warningsBeforeDelta := strings.Count(string(logBeforeDelta), "monitor: Events channel full; requesting snapshot")
	addObservedAddress(t, link, nextAddress)
	deltaDeadline := time.NewTimer(5 * time.Second)
	defer deltaDeadline.Stop()
	for {
		select {
		case event := <-monitor.Events:
			if event.Kind == EvAddrAdded && event.CIDR == nextAddress {
				if event.IfIndex != link.Attrs().Index || event.SnapshotReplay {
					t.Fatalf("post-resync address delta = %+v", event)
				}
				return
			}
		case <-deltaDeadline.C:
			logAfterDelta, err := os.ReadFile(logFile.Name())
			if err != nil {
				t.Fatal(err)
			}
			warningsAfterDelta := strings.Count(string(logAfterDelta), "monitor: Events channel full; requesting snapshot")
			t.Fatalf("post-resync address delta was not observed; overflow warnings before=%d after=%d", warningsBeforeDelta, warningsAfterDelta)
		}
	}
}

func TestMonitorReportsAmbiguousLinkFailureAndRecovers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const hardwareAddress = "02:00:5e:00:53:75"
	link := addObservedVeth(t, "obs-failure", "obs-fail-peer", hardwareAddress)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{
		Iface: "obs-failure",
		Connection: &interfaceintent.Connection{
			ID: "ambiguous-observer", Name: "obs-failure",
			Link: &interfaceintent.Link{
				Kind:  interfaceintent.KindPhysical,
				Match: interfaceintent.Match{HardwareAddress: hardwareAddress},
			},
		},
	})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool { return snapshot.IfIndex == link.Attrs().Index })
	duplicate := addObservedVeth(t, "obs-duplicate", "obs-dupe-peer", hardwareAddress)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var stale, failed Event
	for failed.Kind != EvObservationFailed {
		select {
		case event := <-monitor.Events:
			switch event.Kind {
			case EvObservationStale:
				stale = event
			case EvObservationFailed:
				failed = event
			}
		case <-deadline.C:
			t.Fatalf("ambiguous link did not report snapshot failure; stale=%+v failed=%+v", stale, failed)
		}
	}
	if stale.Kind != EvObservationStale || !strings.Contains(failed.Reason, "matches 2 kernel links") || stale.ObservedAt.IsZero() || failed.ObservedAt.IsZero() {
		t.Fatalf("ambiguous link status = stale %+v, failed %+v", stale, failed)
	}
	if err := netlink.LinkDel(duplicate); err != nil {
		t.Fatal(err)
	}
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool { return snapshot.IfIndex == link.Attrs().Index })

	queuedDuplicate := addObservedVeth(t, "obs-duplicate2", "obs-dupe-peer2", hardwareAddress)
	defer func() { _ = netlink.LinkDel(queuedDuplicate) }()
	queuedDeadline := time.Now().Add(5 * time.Second)
	for len(monitor.Events) < 2 {
		if time.Now().After(queuedDeadline) {
			t.Fatal("ambiguous link did not queue failure statuses before cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	for len(monitor.Events) != 0 {
		if time.Now().After(queuedDeadline) {
			t.Fatal("monitor did not clear queued failure statuses after cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	monitor.setSubscriptionReady(0, false, "address subscription closed after cancellation")
	select {
	case event := <-monitor.Events:
		t.Fatalf("monitor reported subscription failure after cancellation: %+v", event)
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
	hardware, err := net.ParseMAC(mac)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name, HardwareAddr: hardware}, PeerName: peerName}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	identity := link.Attrs().PermHWAddr
	if len(identity) == 0 {
		identity = link.Attrs().HardwareAddr
	}
	if !strings.EqualFold(identity.String(), mac) {
		t.Fatalf("link %s identity MAC = %s, want %s", name, identity, mac)
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
	var recent []string
	for {
		select {
		case event := <-events:
			if event.Kind == EvAddrAdded && event.CIDR == address {
				return event
			}
			summary := fmt.Sprintf("%s cidr=%s iface=%s index=%d", event.Kind, event.CIDR, event.ActualIface, event.IfIndex)
			if event.Snapshot != nil {
				containsAddress := false
				for _, observed := range event.Snapshot.Addresses {
					if observed.CIDR == address {
						containsAddress = true
						break
					}
				}
				summary += fmt.Sprintf(" snapshot-index=%d address-count=%d contains-target=%t", event.Snapshot.IfIndex, len(event.Snapshot.Addresses), containsAddress)
			}
			recent = append(recent, summary)
			if len(recent) > 20 {
				recent = recent[len(recent)-20:]
			}
		case <-deadline.C:
			t.Fatalf("address %s was not observed; recent monitor events: %v", address, recent)
		}
	}
}
