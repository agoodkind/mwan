//go:build linux && netns

package netif

import (
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestOwnedVXLANReconcilesAndRestarts(t *testing.T) {
	if !enterVXLANNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, vxlanUnderlayName)
	statePath := filepath.Join(t.TempDir(), "links.json")
	connection := vxlanConnection()
	external := externalConnection(vxlanUnderlayName)
	reconciler := newTunnelReconciler(t, statePath)
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, external))
	created := vxlanDevice(t, vxlanName)
	requireVXLANAttributes(t, created, connection, underlay)
	requireTunnelReady(t, reconcileVXLAN(t, newTunnelReconciler(t, statePath), connection, external))
	repeated := vxlanDevice(t, vxlanName)
	if repeated.Index != created.Index || repeated.Alias != created.Alias {
		t.Fatal("repeat reconciliation or restart replaced the owned VXLAN")
	}
	connection.Link.Tunnel.VXLAN.VNI = 16777215
	connection.Link.Tunnel.VXLAN.SourcePortMin = 49154
	connection.Link.Tunnel.VXLAN.SourcePortMax = 49155
	connection.Link.Tunnel.VXLAN.DestinationPort = 4790
	connection.Link.Tunnel.VXLAN.TOS = 64
	connection.Link.Tunnel.TTL = new(uint8(32))
	connection.Link.Tunnel.Remote = netip.MustParseAddr("198.51.100.9")
	connection.Link.MTU = new(uint32(1400))
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, external))
	changed := vxlanDevice(t, vxlanName)
	requireVXLANAttributes(t, changed, connection, underlay)
	if changed.Index == created.Index {
		t.Fatal("VXLAN envelope update did not replace the immutable device")
	}
	requireTunnelReady(t, reconcileVXLAN(t, newTunnelReconciler(t, statePath), connection, external))
	if restarted := vxlanDevice(t, vxlanName); restarted.Index != changed.Index || restarted.Alias != changed.Alias {
		t.Fatal("restart did not retain the updated VXLAN identity")
	}
}

func TestOwnedVXLANRejectsForeignReceiverAndInvalidMTU(t *testing.T) {
	if !enterVXLANNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, vxlanUnderlayName)
	connection := vxlanConnection()
	external := externalConnection(vxlanUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, external))
	created := vxlanDevice(t, vxlanName)
	attributes := netlink.NewLinkAttrs()
	attributes.Name = "foreign-vx"
	if err := netlink.LinkAdd(&netlink.Vxlan{
		LinkAttrs: attributes, VxlanId: 1, VtepDevIndex: underlay.Attrs().Index,
		Group: net.ParseIP("198.51.100.9"), SrcAddr: net.ParseIP(tunnelLocalOuter), Port: 4790, Learning: false,
	}); err != nil {
		t.Fatal(err)
	}
	foreign := vxlanDevice(t, "foreign-vx")
	connection.Link.Tunnel.VXLAN.VNI = 1
	connection.Link.Tunnel.VXLAN.DestinationPort = 4790
	result := reconcileVXLAN(t, reconciler, connection, external)
	if result.Status != OwnedLinkFailed || !errors.Is(result.Err, unix.EEXIST) {
		t.Fatalf("foreign receiver conflict=%+v", result)
	}
	if kept := vxlanDevice(t, vxlanName); kept.Index != created.Index || kept.VxlanId != 0 || kept.Alias != created.Alias {
		t.Fatal("foreign conflict changed the owned VXLAN")
	}
	if kept := vxlanDevice(t, "foreign-vx"); kept.Index != foreign.Index || kept.Alias != "" {
		t.Fatal("foreign VXLAN changed")
	}
	connection = vxlanConnection()
	connection.Link.MTU = new(uint32(1451))
	result = reconcileVXLAN(t, reconciler, connection, external)
	if result.Status != OwnedLinkFailed || result.Err == nil {
		t.Fatalf("oversized VXLAN MTU accepted: %+v", result)
	}
	if kept := vxlanDevice(t, vxlanName); kept.Index != created.Index || kept.MTU != 1450 {
		t.Fatal("invalid MTU changed the owned VXLAN")
	}
}

func TestOwnedVXLANDisablesAndRemovesOnlyOwnedDevice(t *testing.T) {
	if !enterVXLANNamespace(t) {
		return
	}
	addTunnelUnderlay(t, vxlanUnderlayName)
	connection := vxlanConnection()
	external := externalConnection(vxlanUnderlayName)
	statePath := filepath.Join(t.TempDir(), "links.json")
	reconciler := newTunnelReconciler(t, statePath)
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, external))
	connection.Enabled = new(false)
	requireTunnelReady(t, reconcileVXLAN(t, newTunnelReconciler(t, statePath), connection, external))
	if vxlanDevice(t, vxlanName).Flags&net.FlagUp != 0 {
		t.Fatal("disabled VXLAN remains administratively up")
	}
	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), nil, nil)
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkRemoved {
		t.Fatalf("VXLAN removal: results=%+v error=%v", results, err)
	}
	if _, err := netlink.LinkByName(vxlanName); !IsLinkNotFound(err) {
		t.Fatalf("owned VXLAN survived removal: %v", err)
	}
	if _, err := netlink.LinkByName(vxlanUnderlayName); err != nil {
		t.Fatalf("external underlay changed: %v", err)
	}
}

func TestOwnedVXLANRejectsInvalidSourcePortRanges(t *testing.T) {
	if !enterVXLANNamespace(t) {
		return
	}
	addTunnelUnderlay(t, vxlanUnderlayName)
	external := externalConnection(vxlanUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, vxlanConnection(), external))
	created := vxlanDevice(t, vxlanName)
	for _, entry := range []struct {
		name             string
		minimum, maximum uint16
	}{
		{name: "zero-minimum", minimum: 0, maximum: vxlanSourcePortMax},
		{name: "zero-maximum", minimum: vxlanSourcePortMin, maximum: 0},
		{name: "equal", minimum: vxlanSourcePortMin, maximum: vxlanSourcePortMin},
		{name: "reversed", minimum: vxlanSourcePortMax, maximum: vxlanSourcePortMin},
	} {
		t.Run(entry.name, func(t *testing.T) {
			connection := vxlanConnection()
			connection.Link.Tunnel.VXLAN.SourcePortMin = entry.minimum
			connection.Link.Tunnel.VXLAN.SourcePortMax = entry.maximum
			result := reconcileVXLAN(t, reconciler, connection, external)
			if result.Status != OwnedLinkFailed || result.Err == nil {
				t.Fatalf("invalid VXLAN source-port range %d..%d accepted: %+v", entry.minimum, entry.maximum, result)
			}
			if kept := vxlanDevice(t, vxlanName); kept.Index != created.Index || kept.Alias != created.Alias ||
				kept.PortLow != vxlanSourcePortMin || kept.PortHigh != vxlanSourcePortMax {
				t.Fatal("invalid source-port range changed the owned VXLAN")
			}
		})
	}
}
