//go:build linux && netns

package netif

import (
	"errors"
	"fmt"
	"net"
	"os"
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
	statePath := filepath.Join(t.TempDir(), "links.json")
	reconciler := newTunnelReconciler(t, statePath)
	underlay := externalConnection(tunnelUnderlayName)
	original := tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil)
	requireTunnelReady(t, reconcileTunnel(t, reconciler, underlay, original))
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
	// A restarted link manager adopts the device only with the journal record of the device.
	restarted := reconcileTunnel(t, newTunnelReconciler(t, statePath), underlay, original)
	requireTunnelReady(t, restarted)
	if restarted.Operation != "reconcile" || restarted.IfIndex != created.Attrs().Index {
		t.Fatalf("restart after the failed update = %+v, want adoption of index %d", restarted, created.Attrs().Index)
	}
}

func TestOwnedTunnelRejectsForeignDeviceWithTemporaryName(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlayLink := addTunnelUnderlay(t, tunnelUnderlayName)
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	// An interrupted creation writes the configured tunnel's reservation to the journal file.
	const temporaryName = "mw-foreign"
	reservation := fmt.Sprintf(`{"virtuals":{%q:{"boot_id":%q,"connection_id":%q,`+
		`"alias":"mwan-link:%s:reserved-token","temp_name":%q,"name":%q,"kind":"tunnel",`+
		`"parent_index":%d,"complete":false,"tunnel_protocol":"6in4","tunnel_remote":%q,"tunnel_local":%q}},`+
		`"memberships":{}}`,
		tunnelName, strings.TrimSpace(string(bootID)), tunnelName, tunnelName, temporaryName, tunnelName,
		underlayLink.Attrs().Index, tunnelRemoteOuter, tunnelLocalOuter)
	statePath := filepath.Join(t.TempDir(), "links.json")
	if err := os.WriteFile(statePath, []byte(reservation), 0o600); err != nil {
		t.Fatal(err)
	}
	reconciler := newTunnelReconciler(t, statePath)
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
