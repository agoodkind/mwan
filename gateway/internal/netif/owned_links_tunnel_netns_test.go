//go:build linux && netns

package netif

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	tunnelChildEnv      = "MWAN_OWNED_TUNNEL_CHILD"
	tunnelName          = "mwan6in4"
	tunnelUnderlayName  = "tun-under"
	tunnelLocalOuter    = "192.0.2.1"
	tunnelRemoteOuter   = "192.0.2.2"
	tunnelLocalInner    = "2001:db8:41::1"
	tunnelRemoteInner   = "2001:db8:41::2"
	tunnelCaptureWindow = 5 * time.Second
)

// enterTunnelNamespace reruns the calling test in a child process with a
// private network namespace. enterTunnelNamespace reports whether the caller is
// the child process.
func enterTunnelNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(tunnelChildEnv) == t.Name() {
		return true
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), tunnelChildEnv+"="+t.Name())
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated tunnel test: %v: %s", err, output)
	}
	return false
}

func tunnelTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func addTunnelUnderlay(t *testing.T, name string) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		t.Fatalf("create underlay %s: %v", name, err)
	}
	underlay, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	return underlay
}

func externalConnection(name string) interfaceintent.Connection {
	return interfaceintent.Connection{ID: connectionid.ID(name), Name: name, Owner: interfaceintent.OwnerExternal}
}

func tunnelConnection(underlay string, remote string, ttl *uint8, mtu *uint32) interfaceintent.Connection {
	return namedTunnelConnection(tunnelName, underlay, remote, ttl, mtu)
}

func namedTunnelConnection(name string, underlay string, remote string, ttl *uint8, mtu *uint32) interfaceintent.Connection {
	return interfaceintent.Connection{
		ID: connectionid.ID(name), Name: name, Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindTunnel, MTU: mtu,
			Tunnel: &interfaceintent.Tunnel{
				Protocol: interfaceintent.TunnelProtocol6in4, Underlay: underlay,
				Remote: netip.MustParseAddr(remote), Local: netip.MustParseAddr(tunnelLocalOuter), TTL: ttl,
			},
		},
	}
}

func newTunnelReconciler(t *testing.T, statePath string) *OwnedLinkReconciler {
	t.Helper()
	reconciler, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func reconcileTunnel(t *testing.T, reconciler *OwnedLinkReconciler, connections ...interfaceintent.Connection) OwnedLinkResult {
	t.Helper()
	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), connections)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, result := range results {
		if result.Name == tunnelName {
			return result
		}
	}
	t.Fatalf("no result for %s in %+v", tunnelName, results)
	return OwnedLinkResult{}
}

func requireTunnelReady(t *testing.T, result OwnedLinkResult) {
	t.Helper()
	if result.Status != OwnedLinkReady || result.Err != nil {
		t.Fatalf("tunnel result = %+v", result)
	}
}

func tunnelDevice(t *testing.T, name string) *netlink.Sittun {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	device, ok := link.(*netlink.Sittun)
	if !ok {
		t.Fatalf("%s has kernel type %s, want sit", name, link.Type())
	}
	return device
}

func requireTunnelAttributes(t *testing.T, device *netlink.Sittun, underlay netlink.Link, remote string, ttl uint8, mtu int) {
	t.Helper()
	if !device.Remote.Equal(net.ParseIP(remote)) || !device.Local.Equal(net.ParseIP(tunnelLocalOuter)) {
		t.Fatalf("tunnel endpoints local=%s remote=%s, want local=%s remote=%s",
			device.Local, device.Remote, tunnelLocalOuter, remote)
	}
	if device.Ttl != ttl {
		t.Fatalf("tunnel TTL = %d, want %d", device.Ttl, ttl)
	}
	if device.Attrs().MTU != mtu {
		t.Fatalf("tunnel MTU = %d, want %d", device.Attrs().MTU, mtu)
	}
	if device.Attrs().ParentIndex != underlay.Attrs().Index {
		t.Fatalf("tunnel underlay index = %d, want %d (%s)",
			device.Attrs().ParentIndex, underlay.Attrs().Index, underlay.Attrs().Name)
	}
	if device.Proto != unix.IPPROTO_IPV6 {
		t.Fatalf("tunnel inner protocol = %d, want %d", device.Proto, unix.IPPROTO_IPV6)
	}
	if device.Attrs().Flags&net.FlagUp == 0 {
		t.Fatal("tunnel is administratively down")
	}
}

func TestOwnedTunnelCreatesConfiguredDevice(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	statePath := filepath.Join(t.TempDir(), "links.json")
	reconciler := newTunnelReconciler(t, statePath)
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
	journal := newTunnelReconciler(t, statePath)
	record, recorded := journal.state.Virtuals[connection.ID.String()]
	if !recorded || !record.Complete || record.LinkIndex != device.Attrs().Index ||
		record.Alias == "" || record.Alias != device.Attrs().Alias {
		t.Fatalf("journal record = %+v, device alias = %q index = %d",
			record, device.Attrs().Alias, device.Attrs().Index)
	}
	monitor := NewMonitor(t.Context(), tunnelTestLog(), MonitorConfig{Iface: tunnelName, Connection: &connection})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == device.Attrs().Index && snapshot.ActualIface == tunnelName
	})
}

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

func TestOwnedTunnelsExchangeRemoteEndpoints(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	underlay := externalConnection(tunnelUnderlayName)
	const secondName, secondRemote = "mwan6in4b", "198.51.100.7"
	reconcileBoth := func(firstRemote string, otherRemote string) map[string]OwnedLinkResult {
		results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), []interfaceintent.Connection{
			underlay,
			tunnelConnection(tunnelUnderlayName, firstRemote, nil, nil),
			namedTunnelConnection(secondName, tunnelUnderlayName, otherRemote, nil, nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		byName := make(map[string]OwnedLinkResult, len(results))
		for _, result := range results {
			byName[result.Name] = result
		}
		return byName
	}
	created := reconcileBoth(tunnelRemoteOuter, secondRemote)
	requireTunnelReady(t, created[tunnelName])
	requireTunnelReady(t, created[secondName])
	secondIndex := tunnelDevice(t, secondName).Attrs().Index

	firstPass := reconcileBoth(secondRemote, tunnelRemoteOuter)

	if result := firstPass[tunnelName]; result.Status != OwnedLinkWaiting || result.Operation != "wait-for-tunnel" ||
		result.Err == nil || !strings.Contains(result.Err.Error(), secondName) {
		t.Fatalf("first exchange pass result for %s = %+v", tunnelName, result)
	}
	requireTunnelReady(t, firstPass[secondName])
	second := tunnelDevice(t, secondName)
	if !second.Remote.Equal(net.ParseIP(tunnelRemoteOuter)) || second.Attrs().Index != secondIndex {
		t.Fatalf("%s after the first pass: remote=%s index=%d, want remote=%s index=%d",
			secondName, second.Remote, second.Attrs().Index, tunnelRemoteOuter, secondIndex)
	}

	secondPass := reconcileBoth(secondRemote, tunnelRemoteOuter)

	requireTunnelReady(t, secondPass[tunnelName])
	requireTunnelReady(t, secondPass[secondName])
	if first := tunnelDevice(t, tunnelName); !first.Remote.Equal(net.ParseIP(secondRemote)) {
		t.Fatalf("%s remote after the exchange = %s, want %s", tunnelName, first.Remote, secondRemote)
	}
	if second := tunnelDevice(t, secondName); second.Attrs().Index != secondIndex {
		t.Fatalf("%s index after the exchange = %d, want %d", secondName, second.Attrs().Index, secondIndex)
	}
}

func TestOwnedTunnelKeepsDeviceWhenUnrecordedTunnelHasEndpoints(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlayLink := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	underlay := externalConnection(tunnelUnderlayName)
	requireTunnelReady(t, reconcileTunnel(t, reconciler, underlay,
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil)))
	created := tunnelDevice(t, tunnelName)
	foreign := netlink.NewLinkAttrs()
	foreign.Name = "foreign-sit"
	if err := netlink.LinkAdd(&netlink.Sittun{
		LinkAttrs: foreign, Link: uint32(underlayLink.Attrs().Index), Proto: unix.IPPROTO_IPV6,
		Local: net.ParseIP(tunnelLocalOuter), Remote: net.ParseIP("198.51.100.9"),
	}); err != nil {
		t.Fatalf("create unrecorded sit device: %v", err)
	}

	result := reconcileTunnel(t, reconciler, underlay,
		tunnelConnection(tunnelUnderlayName, "198.51.100.9", nil, nil))

	if result.Status != OwnedLinkFailed || !errors.Is(result.Err, unix.EEXIST) {
		t.Fatalf("result with the endpoints on an unrecorded device = %+v", result)
	}
	kept := tunnelDevice(t, tunnelName)
	if kept.Attrs().Index != created.Attrs().Index || !kept.Remote.Equal(net.ParseIP(tunnelRemoteOuter)) {
		t.Fatalf("failed update changed the device: index %d became %d, remote %s",
			created.Attrs().Index, kept.Attrs().Index, kept.Remote)
	}
	if _, recorded := reconciler.state.Virtuals[tunnelName]; !recorded {
		t.Fatal("failed update removed the journal record")
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
	if _, recorded := reconciler.state.Virtuals[tunnelName]; recorded {
		t.Fatal("removed tunnel kept its journal record")
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
	if _, reserved := newTunnelReconciler(t, statePath).state.Virtuals[tunnelName]; reserved {
		t.Fatal("the tunnel without an underlay left a journal reservation")
	}

	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	result := reconcileTunnel(t, reconciler, connections...)

	requireTunnelReady(t, result)
	device := tunnelDevice(t, tunnelName)
	if device.Attrs().ParentIndex != underlay.Attrs().Index {
		t.Fatalf("underlay index = %d, want %d", device.Attrs().ParentIndex, underlay.Attrs().Index)
	}
}

// outerPacket stores one IPv4 packet from an underlay capture and the packet's inner IPv6 addresses.
type outerPacket struct {
	source      string
	destination string
	protocol    int
	innerSource string
	innerTarget string
}

func captureOuterPackets(t *testing.T, capture *packet.Conn, complete func([]outerPacket) bool) []outerPacket {
	t.Helper()
	if err := capture.SetReadDeadline(time.Now().Add(tunnelCaptureWindow)); err != nil {
		t.Fatal(err)
	}
	var packets []outerPacket
	buffer := make([]byte, 65536)
	for !complete(packets) {
		length, _, err := capture.ReadFrom(buffer)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return packets
		}
		if err != nil {
			t.Fatalf("read captured packet: %v", err)
		}
		if length == 0 || buffer[0]>>4 != ipv4.Version {
			continue
		}
		outer, err := ipv4.ParseHeader(buffer[:length])
		if err != nil {
			t.Fatalf("parse captured IPv4 header: %v", err)
		}
		captured := outerPacket{source: outer.Src.String(), destination: outer.Dst.String(), protocol: outer.Protocol}
		if outer.Protocol == unix.IPPROTO_IPV6 {
			inner, err := ipv6.ParseHeader(buffer[outer.Len:length])
			if err != nil {
				t.Fatalf("parse encapsulated IPv6 header: %v", err)
			}
			captured.innerSource, captured.innerTarget = inner.Src.String(), inner.Dst.String()
		}
		packets = append(packets, captured)
	}
	return packets
}

func exchangeOverTunnel(t *testing.T, listenerNamespace netns.NsHandle, listenAddress string, dialerNamespace netns.NsHandle, home netns.NsHandle) {
	t.Helper()
	if err := netns.Set(listenerNamespace); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp6", net.JoinHostPort(listenAddress, "0"))
	if err != nil {
		t.Fatalf("listen on %s: %v", listenAddress, err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() {
		accepted, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer accepted.Close()
		request := make([]byte, len("request"))
		if _, err := io.ReadFull(accepted, request); err != nil {
			served <- err
			return
		}
		_, err = accepted.Write([]byte("reply"))
		served <- err
	}()
	if err := netns.Set(dialerNamespace); err != nil {
		t.Fatal(err)
	}
	dialed, err := net.DialTimeout("tcp6", listener.Addr().String(), tunnelCaptureWindow)
	if restoreErr := netns.Set(home); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err != nil {
		t.Fatalf("dial %s through the tunnel: %v", listener.Addr(), err)
	}
	defer dialed.Close()
	if err := dialed.SetDeadline(time.Now().Add(tunnelCaptureWindow)); err != nil {
		t.Fatal(err)
	}
	if _, err := dialed.Write([]byte("request")); err != nil {
		t.Fatalf("send through the tunnel: %v", err)
	}
	reply := make([]byte, len("reply"))
	if _, err := io.ReadFull(dialed, reply); err != nil || string(reply) != "reply" {
		t.Fatalf("reply through the tunnel = %q, err = %v", reply, err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve through the tunnel: %v", err)
	}
}

func TestOwnedTunnelCarriesIPv6InsideIPv4(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	// The namespace switches below apply to the calling thread only.
	runtime.LockOSThread()
	home, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(home); err != nil {
		t.Fatal(err)
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: tunnelUnderlayName}, PeerName: "tun-peer",
		PeerNamespace: netlink.NsFd(int(peerNamespace)),
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatal(err)
	}
	underlay, err := netlink.LinkByName(tunnelUnderlayName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(underlay, mustTunnelAddress(t, tunnelLocalOuter+"/30")); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.NewHandleAt(peerNamespace)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peerUnderlay, err := peer.LinkByName("tun-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.AddrAdd(peerUnderlay, mustTunnelAddress(t, tunnelRemoteOuter+"/30")); err != nil {
		t.Fatal(err)
	}
	if err := peer.LinkSetUp(peerUnderlay); err != nil {
		t.Fatal(err)
	}
	peerAttrs := netlink.NewLinkAttrs()
	peerAttrs.Name = "peer6in4"
	peerTunnel := &netlink.Sittun{
		LinkAttrs: peerAttrs, Link: uint32(peerUnderlay.Attrs().Index), Proto: unix.IPPROTO_IPV6,
		Local: net.ParseIP(tunnelRemoteOuter), Remote: net.ParseIP(tunnelLocalOuter),
	}
	if err := peer.LinkAdd(peerTunnel); err != nil {
		t.Fatalf("create the peer sit device: %v", err)
	}
	peerDevice, err := peer.LinkByName("peer6in4")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.AddrAdd(peerDevice, mustTunnelAddress(t, tunnelRemoteInner+"/64")); err != nil {
		t.Fatal(err)
	}
	if err := peer.LinkSetUp(peerDevice); err != nil {
		t.Fatal(err)
	}

	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	requireTunnelReady(t, reconcileTunnel(t, reconciler, externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(64)), nil)))
	if err := netlink.AddrAdd(tunnelDevice(t, tunnelName), mustTunnelAddress(t, tunnelLocalInner+"/64")); err != nil {
		t.Fatal(err)
	}

	underlayInterface, err := net.InterfaceByName(tunnelUnderlayName)
	if err != nil {
		t.Fatal(err)
	}
	// The kernel delivers transmitted packets only to sockets that receive every protocol.
	capture, err := packet.Listen(underlayInterface, packet.Datagram, unix.ETH_P_ALL, nil)
	if err != nil {
		t.Fatalf("open the underlay capture: %v", err)
	}
	defer capture.Close()

	exchangeOverTunnel(t, peerNamespace, tunnelRemoteInner, home, home)
	exchangeOverTunnel(t, home, tunnelLocalInner, peerNamespace, home)

	sawOutbound := func(captured outerPacket) bool {
		return captured.source == tunnelLocalOuter && captured.destination == tunnelRemoteOuter &&
			captured.innerSource == tunnelLocalInner && captured.innerTarget == tunnelRemoteInner
	}
	sawInbound := func(captured outerPacket) bool {
		return captured.source == tunnelRemoteOuter && captured.destination == tunnelLocalOuter &&
			captured.innerSource == tunnelRemoteInner && captured.innerTarget == tunnelLocalInner
	}
	bothDirections := func(packets []outerPacket) bool {
		outbound, inbound := false, false
		for _, captured := range packets {
			outbound = outbound || sawOutbound(captured)
			inbound = inbound || sawInbound(captured)
		}
		return outbound && inbound
	}
	packets := captureOuterPackets(t, capture, bothDirections)
	if !bothDirections(packets) {
		t.Fatalf("underlay capture lacks encapsulated packets in both directions: %+v", packets)
	}
	for _, captured := range packets {
		if captured.protocol != unix.IPPROTO_IPV6 {
			t.Fatalf("underlay packet uses IPv4 protocol %d, want %d: %+v", captured.protocol, unix.IPPROTO_IPV6, captured)
		}
	}
}

func mustTunnelAddress(t *testing.T, cidr string) *netlink.Addr {
	t.Helper()
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if address.IP.To4() == nil {
		// The exchange starts immediately. A tentative IPv6 address rejects connections.
		address.Flags = unix.IFA_F_NODAD
	}
	return address
}
