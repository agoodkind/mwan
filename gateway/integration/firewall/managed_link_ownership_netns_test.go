//go:build linux && firewall

package firewall_test

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	ownedRuntimeChildEnv  = "MWAN_OWNED_RUNTIME_TEST_CHILD"
	ownedRuntimeBinaryEnv = "MWAN_OWNED_RUNTIME_TEST_BINARY"
)

func TestOwnedLinksDaemonRuntime(t *testing.T) {
	if os.Getenv(ownedRuntimeChildEnv) == "1" {
		runOwnedLinksDaemonRuntime(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	t.Parallel()
	binary := testenv.BuildTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedLinksDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), ownedRuntimeChildEnv+"=1", ownedRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated owned-link daemon test: %v: %s", err, output)
	}
}

func runOwnedLinksDaemonRuntime(t *testing.T) {
	testenv.MountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n",
		filepath.Join(root, "owned-links.json"),
		filepath.Join(root, "kernel-policy.json"),
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.Namespace.Close()
	lan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer lan.Namespace.Close()
	parentPeer := testenv.NewRuntimePeer(
		t,
		gateway,
		"ownphys0",
		"owned-peer",
		nil,
		nil,
		testenv.OwnedRuntimeParentMAC,
	)
	defer parentPeer.Namespace.Close()
	testenv.SetRuntimeNamespace(t, gateway)
	if err := netlink.LinkAdd(&netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{Name: "foreign-br"},
	}); err != nil {
		t.Fatal(err)
	}
	testenv.WriteOwnedRuntimeNetwork(t, networkDir, true, true)
	binary := os.Getenv(ownedRuntimeBinaryEnv)
	invalidConfigPath := filepath.Join(root, "invalid-config.toml")
	invalidConfig := "[ifmgr]\nrole = \"wan\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = false\n"
	if err := os.WriteFile(invalidConfigPath, []byte(invalidConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	rejected := exec.Command(binary, "ifmgr", "--role", "wan")
	rejected.Env = append(os.Environ(), "MWAN_CONFIG="+invalidConfigPath)
	output, err := rejected.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("state_file")) {
		t.Fatalf("missing owned-link state_file was not rejected: error=%v output=%s", err, output)
	}
	for _, name := range []string{"owned-br", "owned397", "owned398"} {
		if _, err := netlink.LinkByName(name); err == nil {
			t.Fatalf("startup rejection created %s", name)
		}
	}
	files, err := os.ReadDir(networkdDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("startup rejection wrote networkd files: %v", files)
	}
	first := testenv.StartRuntimeDaemon(t, binary, configPath, root, "owned-first")
	defer testenv.KillOwnedRuntimeDaemon(t, first)
	testenv.WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	bridge := testenv.WaitOwnedRuntimeLink(t, first, "owned-br", 10*time.Second)
	vlan := testenv.WaitOwnedRuntimeLink(t, first, "owned397", 10*time.Second)
	removable := testenv.WaitOwnedRuntimeLink(t, first, "owned398", 10*time.Second)
	parent, err := netlink.LinkByName("ownphys0")
	if err != nil {
		t.Fatal(err)
	}
	if bridge.Type() != "bridge" || vlan.Type() != "vlan" ||
		vlan.Attrs().ParentIndex != parent.Attrs().Index ||
		removable.Type() != "vlan" {
		t.Fatalf(
			"owned links have wrong topology: bridge=%+v vlan=%+v removable=%+v",
			bridge,
			vlan,
			removable,
		)
	}
	bridgeIndex, vlanIndex := bridge.Attrs().Index, vlan.Attrs().Index
	testenv.AssertOwnedRuntimeVLANPacket(t, gateway, parentPeer.Namespace, vlan)
	if err := netlink.LinkSetDown(vlan); err != nil {
		t.Fatal(err)
	}
	testenv.WaitOwnedRuntimeLink(t, first, "owned397", 10*time.Second)
	foreign, err := netlink.LinkByName("foreign-br")
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Attrs().Flags&net.FlagUp != 0 {
		t.Fatal("owned reconciliation enabled the foreign bridge")
	}
	testenv.AssertRuntimeDaemonRunning(t, first)
	testenv.KillOwnedRuntimeDaemon(t, first)
	second := testenv.StartRuntimeDaemon(t, binary, configPath, root, "owned-second")
	defer testenv.KillOwnedRuntimeDaemon(t, second)
	testenv.WaitRuntimeTable(t, second, "inet", "filter", 10*time.Second)
	if got := testenv.WaitOwnedRuntimeLink(
		t,
		second,
		"owned-br",
		10*time.Second,
	); got.Attrs().Index != bridgeIndex {
		t.Fatalf("restart recreated bridge: index %d, want %d", got.Attrs().Index, bridgeIndex)
	}
	if got := testenv.WaitOwnedRuntimeLink(
		t,
		second,
		"owned397",
		10*time.Second,
	); got.Attrs().Index != vlanIndex {
		t.Fatalf("restart recreated VLAN: index %d, want %d", got.Attrs().Index, vlanIndex)
	}
	testenv.KillOwnedRuntimeDaemon(t, second)
	testenv.WriteOwnedRuntimeNetwork(t, networkDir, false, true)
	third := testenv.StartRuntimeDaemon(t, binary, configPath, root, "owned-third")
	defer testenv.KillOwnedRuntimeDaemon(t, third)
	testenv.WaitRuntimeTable(t, third, "inet", "filter", 10*time.Second)
	testenv.WaitOwnedRuntimeAbsent(t, third, "owned398", 10*time.Second)
	for _, name := range []string{
		"owned-br",
		"owned397",
		"ownphys0",
		"foreign-br",
		"enmgmt0",
		"enmwanbr0",
	} {
		if _, err := netlink.LinkByName(name); err != nil {
			t.Fatalf("removing one child changed %s: %v", name, err)
		}
	}
	latePeer := testenv.NewRuntimePeer(
		t,
		gateway,
		"latephys0",
		"late-peer",
		nil,
		nil,
		testenv.OwnedRuntimeLateMAC,
	)
	defer latePeer.Namespace.Close()
	testenv.SetRuntimeNamespace(t, gateway)
	lateParent := testenv.WaitOwnedRuntimeLink(t, third, "latephys0", 10*time.Second)
	lateVLAN := testenv.WaitOwnedRuntimeLink(t, third, "late397", 10*time.Second)
	if lateVLAN.Attrs().ParentIndex != lateParent.Attrs().Index {
		t.Fatalf(
			"late VLAN parent index %d, want %d",
			lateVLAN.Attrs().ParentIndex,
			lateParent.Attrs().Index,
		)
	}
	testenv.KillOwnedRuntimeDaemon(t, third)
	testenv.WriteOwnedRuntimeNetwork(t, networkDir, false, false)
	fourth := testenv.StartRuntimeDaemon(t, binary, configPath, root, "owned-fourth")
	defer testenv.KillOwnedRuntimeDaemon(t, fourth)
	testenv.WaitRuntimeTable(t, fourth, "inet", "filter", 10*time.Second)
	for _, name := range []string{"owned-br", "owned397", "late397"} {
		testenv.WaitOwnedRuntimeAbsent(t, fourth, name, 10*time.Second)
	}
	for _, name := range []string{"ownphys0", "latephys0", "foreign-br", "enmgmt0", "enmwanbr0"} {
		if _, err := netlink.LinkByName(name); err != nil {
			t.Fatalf("removing final owned link changed %s: %v", name, err)
		}
	}
}
