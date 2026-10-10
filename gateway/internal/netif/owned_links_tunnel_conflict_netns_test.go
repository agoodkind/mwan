//go:build linux && netns

package netif

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

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

func TestOwnedTunnelRejectsForeignDeviceWithTemporaryName(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlayLink := addTunnelUnderlay(t, tunnelUnderlayName)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	// The journal stores the reservation that an interrupted creation writes for the configured tunnel.
	const temporaryName = "mw-foreign"
	reconciler.state.Virtuals[tunnelName] = virtualRecord{
		BootID: reconciler.bootID, ConnectionID: tunnelName, Alias: aliasFor(tunnelName) + ":reserved-token",
		TempName: temporaryName, Name: tunnelName, Kind: string(interfaceintent.KindTunnel),
		ParentIndex: underlayLink.Attrs().Index, TunnelProtocol: string(interfaceintent.TunnelProtocol6in4),
		TunnelRemote: tunnelRemoteOuter, TunnelLocal: tunnelLocalOuter,
	}
	if err := reconciler.save(); err != nil {
		t.Fatal(err)
	}
	foreign := netlink.NewLinkAttrs()
	foreign.Name = temporaryName
	if err := netlink.LinkAdd(&netlink.Sittun{
		LinkAttrs: foreign, Link: uint32(underlayLink.Attrs().Index), Proto: unix.IPPROTO_IPV6,
		Local: net.ParseIP(tunnelLocalOuter), Remote: net.ParseIP("198.51.100.9"),
	}); err != nil {
		t.Fatalf("create the foreign sit device: %v", err)
	}
	created := tunnelDevice(t, temporaryName)

	result := reconcileTunnel(t, reconciler, externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil))

	if result.Status != OwnedLinkFailed || result.Err == nil ||
		!strings.Contains(result.Err.Error(), "wrong tunnel identity") {
		t.Fatalf("result with a foreign device at the temporary name = %+v", result)
	}
	kept := tunnelDevice(t, temporaryName)
	if kept.Attrs().Index != created.Attrs().Index || kept.Attrs().Alias != "" ||
		!kept.Remote.Equal(net.ParseIP("198.51.100.9")) {
		t.Fatalf("foreign device changed: index %d became %d, alias %q, remote %s",
			created.Attrs().Index, kept.Attrs().Index, kept.Attrs().Alias, kept.Remote)
	}
	if _, err := netlink.LinkByName(tunnelName); !IsLinkNotFound(err) {
		t.Fatalf("a device has the configured tunnel name: %v", err)
	}
}
