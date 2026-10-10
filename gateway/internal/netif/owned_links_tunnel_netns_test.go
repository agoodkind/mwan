//go:build linux && netns

package netif

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedTunnelCreatesConfiguredDevice(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	connection := tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(32)), new(uint32(1400)))

	result := reconcileTunnel(t, reconciler, externalConnection(tunnelUnderlayName), connection)

	requireTunnelReady(t, result)
	if result.Operation != "create" {
		t.Fatalf("operation = %q, want create", result.Operation)
	}
	device := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, device, underlay, tunnelRemoteOuter, 32, 1400)
	if result.IfIndex != device.Attrs().Index {
		t.Fatalf("result index = %d, device index = %d", result.IfIndex, device.Attrs().Index)
	}
	if !strings.HasPrefix(device.Attrs().Alias, "mwan-link:"+tunnelName+":") {
		t.Fatalf("device alias = %q, want the ownership tag of %s", device.Attrs().Alias, tunnelName)
	}
	monitor := NewMonitor(t.Context(), tunnelTestLog(), MonitorConfig{Iface: tunnelName, Connection: &connection})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == device.Attrs().Index && snapshot.ActualIface == tunnelName
	})
}

func TestOwnedTunnelRemovalKeepsUnrecordedDevices(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	underlay := externalConnection(tunnelUnderlayName)
	requireTunnelReady(t, reconcileTunnel(t, reconciler, underlay,
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil)))
	// The unrecorded device has the ownership alias prefix and no journal record.
	foreign := netlink.NewLinkAttrs()
	foreign.Name = "foreign-sit"
	if err := netlink.LinkAdd(&netlink.Sittun{LinkAttrs: foreign, Remote: net.ParseIP("198.51.100.9"), Proto: unix.IPPROTO_IPV6}); err != nil {
		t.Fatalf("create unrecorded sit device: %v", err)
	}
	foreignLink, err := netlink.LinkByName("foreign-sit")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(foreignLink, aliasFor("foreign-sit")); err != nil {
		t.Fatal(err)
	}
	_, fallbackErr := netlink.LinkByName("sit0")
	fallbackPresent := fallbackErr == nil

	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), []interfaceintent.Connection{underlay})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 || results[0].Status != OwnedLinkRemoved || results[0].Name != tunnelName {
		t.Fatalf("removal results = %+v", results)
	}
	if _, err := netlink.LinkByName(tunnelName); !IsLinkNotFound(err) {
		t.Fatalf("removed tunnel still exists: %v", err)
	}
	if _, err := netlink.LinkByName("foreign-sit"); err != nil {
		t.Fatalf("unrecorded sit device was deleted: %v", err)
	}
	if fallbackPresent {
		if _, err := netlink.LinkByName("sit0"); err != nil {
			t.Fatalf("kernel fallback sit0 was deleted: %v", err)
		}
	}
}

func TestOwnedTunnelRestartAdoptsDevice(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	statePath := filepath.Join(t.TempDir(), "links.json")
	connections := []interfaceintent.Connection{
		externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(32)), new(uint32(1400))),
	}
	requireTunnelReady(t, reconcileTunnel(t, newTunnelReconciler(t, statePath), connections...))
	created := tunnelDevice(t, tunnelName)

	result := reconcileTunnel(t, newTunnelReconciler(t, statePath), connections...)

	requireTunnelReady(t, result)
	if result.Operation != "reconcile" {
		t.Fatalf("restart operation = %q, want reconcile", result.Operation)
	}
	adopted := tunnelDevice(t, tunnelName)
	if adopted.Attrs().Index != created.Attrs().Index || result.IfIndex != created.Attrs().Index {
		t.Fatalf("restart recreated the tunnel: index %d became %d", created.Attrs().Index, adopted.Attrs().Index)
	}
	requireTunnelAttributes(t, adopted, underlay, tunnelRemoteOuter, 32, 1400)
}

func TestOwnedTunnelWaitsForUnderlay(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	statePath := filepath.Join(t.TempDir(), "links.json")
	reconciler := newTunnelReconciler(t, statePath)
	independent := interfaceintent.Connection{
		ID: "independent", Name: "independent", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	connections := []interfaceintent.Connection{
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil),
		independent,
		externalConnection(tunnelUnderlayName),
	}

	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), connections)
	if err != nil || len(results) != 2 {
		t.Fatalf("missing underlay reconcile: results=%+v err=%v", results, err)
	}

	tunnelResult := results[0]
	if tunnelResult.Status != OwnedLinkWaiting || tunnelResult.Operation != "wait-for-underlay" ||
		tunnelResult.Dependency != tunnelUnderlayName || tunnelResult.Err == nil ||
		!strings.Contains(tunnelResult.Err.Error(), "underlay "+tunnelUnderlayName) {
		t.Fatalf("missing underlay result = %+v", tunnelResult)
	}
	if results[1].Name != independent.Name || results[1].Status != OwnedLinkReady {
		t.Fatalf("the tunnel without an underlay blocked the independent link: %+v", results[1])
	}
	if _, err := netlink.LinkByName(tunnelName); !IsLinkNotFound(err) {
		t.Fatalf("tunnel exists without its underlay: %v", err)
	}

	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	result := reconcileTunnel(t, reconciler, connections...)

	requireTunnelReady(t, result)
	device := tunnelDevice(t, tunnelName)
	if device.Attrs().ParentIndex != underlay.Attrs().Index {
		t.Fatalf("underlay index = %d, want %d", device.Attrs().ParentIndex, underlay.Attrs().Index)
	}
}
