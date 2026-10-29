//go:build linux && netns

package netif

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedTunnelReplacesDeviceWithWrongInnerProtocol(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	underlay := addTunnelUnderlay(t, tunnelUnderlayName)
	statePath := filepath.Join(t.TempDir(), "links.json")
	connections := []interfaceintent.Connection{
		externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, nil, nil),
	}
	requireTunnelReady(t, reconcileTunnel(t, newTunnelReconciler(t, statePath), connections...))
	created := tunnelDevice(t, tunnelName)
	// The kernel sets the inner protocol only at creation. The test creates an IPv4-in-IPv4 device with
	// the recorded alias. The test writes the device index to the journal file.
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
	journal, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	recordedIndex := fmt.Sprintf(`"link_index":%d,`, created.Attrs().Index)
	if strings.Count(string(journal), recordedIndex) != 1 {
		t.Fatalf("journal %s lacks one occurrence of %s", journal, recordedIndex)
	}
	replacedIndex := fmt.Sprintf(`"link_index":%d,`, wrong.Attrs().Index)
	journal = []byte(strings.Replace(string(journal), recordedIndex, replacedIndex, 1))
	if err := os.WriteFile(statePath, journal, 0o600); err != nil {
		t.Fatal(err)
	}

	result := reconcileTunnel(t, newTunnelReconciler(t, statePath), connections...)

	requireTunnelReady(t, result)
	replaced := tunnelDevice(t, tunnelName)
	requireTunnelAttributes(t, replaced, underlay, tunnelRemoteOuter, interfaceintent.DefaultTunnelTTL, 1480)
	if replaced.Attrs().Index == wrong.Attrs().Index {
		t.Fatal("the device with the wrong inner protocol was not replaced")
	}
}
