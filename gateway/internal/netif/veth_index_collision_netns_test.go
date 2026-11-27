//go:build linux && netns

// Interface indexes are local to each network namespace. The local veth
// reports its remote peer index as ParentIndex, which can match the local
// VLAN index even though the two interfaces have no dependency.
//
//	Local namespace             Remote namespace
//	VLAN (ifindex 12)
//	veth (ifindex 13) <------> peer (ifindex 12)
package netif

import (
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/interfaceintent"
)

func TestOwnedVLANPrunesWithRemoteVethIndexCollision(t *testing.T) {
	const childEnvironment = "MWAN_OWNED_VLAN_REMOTE_VETH_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}

		child := exec.Command(
			os.Args[0],
			"-test.v",
			"-test.run=^TestOwnedVLANPrunesWithRemoteVethIndexCollision$",
		)
		child.Env = append(os.Environ(), childEnvironment+"=1")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		output, err := child.CombinedOutput()
		t.Logf("%s", output)
		if err != nil {
			t.Fatal(err)
		}

		return
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	local, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	remote, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if err := netns.Set(local); err != nil {
		t.Fatal(err)
	}

	localHandle, err := netlink.NewHandleAt(local)
	if err != nil {
		t.Fatal(err)
	}
	defer localHandle.Close()
	remoteHandle, err := netlink.NewHandleAt(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer remoteHandle.Close()

	mac, err := net.ParseMAC("02:00:5e:39:70:01")
	if err != nil {
		t.Fatal(err)
	}

	if err := localHandle.LinkAdd(&netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{
			Name:         "diag-parent",
			HardwareAddr: mac,
		},
	}); err != nil {
		t.Fatal(err)
	}

	parentLink, err := localHandle.LinkByName("diag-parent")
	if err != nil {
		t.Fatal(err)
	}

	if err := localHandle.LinkSetUp(parentLink); err != nil {
		t.Fatal(err)
	}

	parent := interfaceintent.Connection{
		ID:    "diag-parent",
		Name:  "diag-parent",
		Owner: interfaceintent.OwnerExternal,
		Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{HardwareAddress: mac.String()},
		},
	}
	owned := interfaceintent.Connection{
		ID:    "diag-vlan",
		Name:  "diag-vlan",
		Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindVLAN,
			VLAN: &interfaceintent.VLAN{Parent: parent.Name, ID: 397},
		},
	}
	journal := filepath.Join(t.TempDir(), "links.json")
	reconciler, err := NewOwnedLinkReconciler(journal)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	results, err := reconciler.Reconcile(
		t.Context(),
		logger,
		[]interfaceintent.Connection{parent, owned},
		nil,
	)
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("install VLAN: results=%+v error=%v", results, err)
	}

	vlan, err := localHandle.LinkByName(owned.Name)
	if err != nil {
		t.Fatal(err)
	}

	if err := remoteHandle.LinkAdd(&netlink.Veth{
		LinkAttrs:     netlink.LinkAttrs{Name: "diag-remote", Index: vlan.Attrs().Index},
		PeerName:      "diag-unrelated",
		PeerNamespace: netlink.NsFd(local),
	}); err != nil {
		t.Fatal(err)
	}

	peer, err := remoteHandle.LinkByName("diag-remote")
	if err != nil {
		t.Fatal(err)
	}

	unrelated, err := localHandle.LinkByName("diag-unrelated")
	if err != nil {
		t.Fatal(err)
	}

	attributes := unrelated.Attrs()
	t.Logf(
		"VLAN index=%d alias=%s; remote peer index=%d; local veth index=%d parent_index=%d netnsid=%d master_index=%d",
		vlan.Attrs().Index,
		vlan.Attrs().Alias,
		peer.Attrs().Index,
		attributes.Index,
		attributes.ParentIndex,
		attributes.NetNsID,
		attributes.MasterIndex,
	)
	if attributes.ParentIndex != vlan.Attrs().Index || peer.Attrs().Index != vlan.Attrs().Index ||
		attributes.NetNsID < 0 || attributes.MasterIndex != 0 {
		t.Fatal("kernel did not create the requested cross-namespace index collision")
	}

	restarted, err := NewOwnedLinkReconciler(journal)
	if err != nil {
		t.Fatal(err)
	}

	results, err = restarted.Reconcile(
		t.Context(),
		logger,
		[]interfaceintent.Connection{parent},
		nil,
	)
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkRemoved {
		t.Fatalf("prune with unrelated veth: results=%+v error=%v", results, err)
	}

	if _, err := localHandle.LinkByName(owned.Name); !IsLinkNotFound(err) {
		t.Fatalf("expected kernel to remove VLAN: %v", err)
	}

	if _, err := localHandle.LinkByName("diag-unrelated"); err != nil {
		t.Fatalf("unrelated veth must remain: %v", err)
	}
}
