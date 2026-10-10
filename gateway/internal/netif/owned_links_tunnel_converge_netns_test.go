//go:build linux && netns

package netif

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedTunnelConvergesChangedConfiguration(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	movedUnderlay := addTunnelUnderlay(t, "tun-moved")
	statePath := filepath.Join(t.TempDir(), "links.json")
	reconciler := newTunnelReconciler(t, statePath)
	underlays := []interfaceintent.Connection{externalConnection(tunnelUnderlayName), externalConnection("tun-moved")}
	original := tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(32)), new(uint32(1400)))
	requireTunnelReady(t, reconcileTunnel(t, reconciler, append(underlays, original)...))
	created := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, created, underlay, tunnelRemoteOuter, 32, 1400)

	changed := tunnelConnection(tunnelUnderlayName, "198.51.100.7", new(uint8(48)), new(uint32(1300)))
	result := reconcileTunnel(t, reconciler, append(underlays, changed)...)

	requireTunnelReady(t, result)
	converged := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, converged, underlay, "198.51.100.7", 48, 1300)
	if converged.Attrs().Index != created.Attrs().Index {
		t.Fatalf("endpoint change replaced the device: index %d became %d",
			created.Attrs().Index, converged.Attrs().Index)
	}

	// The second change selects a different underlay. The second change removes the configured hop limit and MTU.
	rebound := tunnelConnection("tun-moved", "198.51.100.7", nil, nil)
	requireTunnelReady(t, reconcileTunnel(t, reconciler, append(underlays, rebound)...))
	moved := tunnelDevice(t, tunnelName)
	if moved.Attrs().ParentIndex != movedUnderlay.Attrs().Index {
		t.Fatalf("underlay index = %d, want %d", moved.Attrs().ParentIndex, movedUnderlay.Attrs().Index)
	}
	if moved.Ttl != interfaceintent.DefaultTunnelTTL {
		t.Fatalf("hop limit without a configured TTL = %d, want %d", moved.Ttl, interfaceintent.DefaultTunnelTTL)
	}
	if moved.Attrs().Index != created.Attrs().Index {
		t.Fatalf("underlay change replaced the device: index %d became %d",
			created.Attrs().Index, moved.Attrs().Index)
	}

	// The kernel must not receive a link change when the configuration has not changed.
	updates := make(chan netlink.LinkUpdate, 16)
	done := make(chan struct{})
	defer close(done)
	if err := netlink.LinkSubscribe(updates, done); err != nil {
		t.Fatal(err)
	}
	requireTunnelReady(t, reconcileTunnel(t, reconciler, append(underlays, rebound)...))
	quiet := time.After(500 * time.Millisecond)
	for {
		select {
		case update := <-updates:
			// The kernel reports delayed state changes of the underlay devices on the same subscription.
			if update.Attrs().Name == tunnelName {
				t.Fatalf("unchanged configuration modified %s", tunnelName)
			}
		case <-quiet:
			return
		}
	}
}

func modifyTunnelExternally(t *testing.T, device *netlink.Sittun, ttl uint8, pathMTUDiscovery uint8) *netlink.Sittun {
	t.Helper()
	attrs := netlink.NewLinkAttrs()
	attrs.Name, attrs.Index = device.Attrs().Name, device.Attrs().Index
	if err := netlink.LinkModify(&netlink.Sittun{
		LinkAttrs: attrs, Link: uint32(device.Attrs().ParentIndex), Local: device.Local, Remote: device.Remote,
		Ttl: ttl, PMtuDisc: pathMTUDiscovery, Proto: unix.IPPROTO_IPV6,
	}); err != nil {
		t.Fatalf("modify %s outside the link manager: %v", device.Attrs().Name, err)
	}
	return tunnelDevice(t, device.Attrs().Name)
}

func TestOwnedTunnelCorrectsExternalChanges(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	connections := []interfaceintent.Connection{
		externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil),
	}
	requireTunnelReady(t, reconcileTunnel(t, reconciler, connections...))
	created := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, created, underlay, tunnelRemoteOuter, interfaceintent.DefaultTunnelTTL, 1480)

	if changed := modifyTunnelExternally(t, created, 5, 1); changed.Ttl != 5 {
		t.Fatalf("external hop limit change did not apply: %d", changed.Ttl)
	}
	requireTunnelReady(t, reconcileTunnel(t, reconciler, connections...))
	requireTunnelAttributes(t, tunnelDevice(t, tunnelName), underlay, tunnelRemoteOuter, interfaceintent.DefaultTunnelTTL, 1480)

	// The kernel accepts a cleared Don't Fragment bit only when the hop limit attribute is absent.
	// The netlink library omits a zero hop limit.
	if changed := modifyTunnelExternally(t, created, 0, 0); changed.PMtuDisc != 0 {
		t.Fatalf("external path MTU discovery change did not apply: %d", changed.PMtuDisc)
	}
	requireTunnelReady(t, reconcileTunnel(t, reconciler, connections...))
	corrected := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, corrected, underlay, tunnelRemoteOuter, interfaceintent.DefaultTunnelTTL, 1480)
	if corrected.PMtuDisc != 1 || corrected.Attrs().Index != created.Attrs().Index {
		t.Fatalf("corrected device: path MTU discovery=%d index=%d, want 1 and index %d",
			corrected.PMtuDisc, corrected.Attrs().Index, created.Attrs().Index)
	}
}

func TestOwnedTunnelReplacesDeviceWithWrongInnerProtocol(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	connections := []interfaceintent.Connection{
		externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil),
	}
	requireTunnelReady(t, reconcileTunnel(t, reconciler, connections...))
	created := tunnelDevice(t, tunnelName)
	// The kernel sets the inner protocol only at creation. The test creates a device with the
	// IPv4-in-IPv4 protocol and the recorded alias. The journal record stores the device index.
	if err := netlink.LinkDel(created); err != nil {
		t.Fatal(err)
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = tunnelName
	if err := netlink.LinkAdd(&netlink.Sittun{
		LinkAttrs: attrs, Link: uint32(underlay.Attrs().Index), Proto: unix.IPPROTO_IPIP, PMtuDisc: 1,
		Ttl: interfaceintent.DefaultTunnelTTL, Local: created.Local, Remote: created.Remote,
	}); err != nil {
		t.Fatal(err)
	}
	wrong := tunnelDevice(t, tunnelName)
	if err := netlink.LinkSetAlias(wrong, created.Attrs().Alias); err != nil {
		t.Fatal(err)
	}
	record := reconciler.state.Virtuals[tunnelName]
	record.LinkIndex = wrong.Attrs().Index
	reconciler.state.Virtuals[tunnelName] = record

	result := reconcileTunnel(t, reconciler, connections...)

	requireTunnelReady(t, result)
	replaced := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, replaced, underlay, tunnelRemoteOuter, interfaceintent.DefaultTunnelTTL, 1480)
	if replaced.Attrs().Index == wrong.Attrs().Index {
		t.Fatal("the device with the wrong inner protocol was not replaced")
	}
}

func TestOwnedTunnelWithoutConfiguredMTUUsesUnderlayMTU(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	external := externalConnection(tunnelUnderlayName)
	requireTunnelReady(t, reconcileTunnel(t, reconciler, external,
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(32)), new(uint32(1400)))))
	created := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, created, underlay, tunnelRemoteOuter, 32, 1400)

	requireTunnelReady(t, reconcileTunnel(t, reconciler, external,
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(32)), nil)))

	derived := underlay.Attrs().MTU - interfaceintent.Tunnel6in4Overhead
	restored := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, restored, underlay, tunnelRemoteOuter, 32, derived)
	if restored.Attrs().Index != created.Attrs().Index {
		t.Fatalf("MTU removal replaced the device: index %d became %d", created.Attrs().Index, restored.Attrs().Index)
	}
}

func TestOwnedTunnelRejectsMTUAboveUnderlayLimit(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	tooLarge := uint32(underlay.Attrs().MTU - interfaceintent.Tunnel6in4Overhead + 1)

	result := reconcileTunnel(t, reconciler, externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, &tooLarge))

	if result.Status != OwnedLinkFailed || result.Err == nil ||
		!strings.Contains(result.Err.Error(), "exceeds underlay "+tunnelUnderlayName) {
		t.Fatalf("result for an MTU above the underlay limit = %+v", result)
	}
	if _, err := netlink.LinkByName(tunnelName); !IsLinkNotFound(err) {
		t.Fatalf("tunnel exists with an MTU above the underlay limit: %v", err)
	}
}
