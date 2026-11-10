//go:build linux && netns

package netif

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestOwnedVXLANTemporaryRecovery(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "matching"
		if foreign {
			name = "foreign"
		}
		t.Run(name, func(t *testing.T) {
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
			record := reserveExistingVXLAN(t, statePath, created)
			if foreign {
				if err := netlink.LinkDel(vxlanDevice(t, record.TempName)); err != nil {
					t.Fatal(err)
				}
				settings := connection.Link.Tunnel.Clone()
				settings.VXLAN.VNI++
				requested, err := requestedVXLAN(record.TempName, 0, settings, underlay)
				if err != nil {
					t.Fatal(err)
				}
				if err := netlink.LinkAdd(requested); err != nil {
					t.Fatal(err)
				}
			}
			result := reconcileVXLAN(t, newTunnelReconciler(t, statePath), connection, external)
			if foreign {
				if result.Status != OwnedLinkFailed || result.Err == nil {
					t.Fatalf("foreign temporary VXLAN was accepted: %+v", result)
				}
				if kept := vxlanDevice(t, record.TempName); kept.Alias != "" || kept.VxlanId != 1 {
					t.Fatalf("foreign temporary VXLAN changed: %+v", kept)
				}
				if _, err := netlink.LinkByName(vxlanName); !IsLinkNotFound(err) {
					t.Fatalf("foreign temporary VXLAN acquired the configured name: %v", err)
				}
				return
			}
			requireTunnelReady(t, result)
			recovered := vxlanDevice(t, vxlanName)
			if recovered.Index != created.Index || recovered.Alias != record.Alias {
				t.Fatal("temporary VXLAN recovery lost kernel identity or ownership")
			}
			requireVXLANAttributes(t, recovered, connection, underlay)
		})
	}
}

func reserveExistingVXLAN(t *testing.T, path string, device *netlink.Vxlan) virtualRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state ownedLinkState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	record := state.Virtuals[vxlanName]
	if err := netlink.LinkSetDown(device); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetName(device, record.TempName); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(vxlanDevice(t, record.TempName), ""); err != nil {
		t.Fatal(err)
	}
	// The journal and untagged kernel device represent interruption before ownership publication.
	record.Complete, record.LinkIndex = false, 0
	state.Virtuals[vxlanName] = record
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestOwnedVXLANRetainsOwnershipAfterCleanupFailure(t *testing.T) {
	if !enterVXLANNamespace(t) {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	addTunnelUnderlay(t, vxlanUnderlayName)
	statePath := filepath.Join(t.TempDir(), "links.json")
	connection := vxlanConnection()
	external := externalConnection(vxlanUnderlayName)
	reconciler := newTunnelReconciler(t, statePath)
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, external))
	created := vxlanDevice(t, vxlanName)
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var capabilities [2]unix.CapUserData
	if err := unix.Capget(&header, &capabilities[0]); err != nil {
		t.Fatal(err)
	}
	reduced := capabilities
	reduced[0].Effective &^= 1 << unix.CAP_NET_ADMIN
	if err := unix.Capset(&header, &reduced[0]); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Capset(&header, &capabilities[0]); err != nil {
			t.Error(err)
		}
	}()
	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), nil, nil)
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkFailed || !errors.Is(results[0].Err, unix.EPERM) {
		t.Fatalf("cleanup without NET_ADMIN did not report retained ownership: results=%+v error=%v", results, err)
	}
	if kept := vxlanDevice(t, vxlanName); kept.Index != created.Index || kept.Alias != created.Alias {
		t.Fatal("failed cleanup changed the VXLAN device")
	}
	if err := unix.Capset(&header, &capabilities[0]); err != nil {
		t.Fatal(err)
	}
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, external))
	requireTunnelReady(t, reconcileVXLAN(t, newTunnelReconciler(t, statePath), connection, external))
	if kept := vxlanDevice(t, vxlanName); kept.Index != created.Index || kept.Alias != created.Alias {
		t.Fatal("cleanup failure discarded durable VXLAN ownership")
	}
}
