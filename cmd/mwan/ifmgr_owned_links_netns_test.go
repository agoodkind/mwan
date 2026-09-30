//go:build linux && firewallnetns

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	ownedRuntimeChildEnv  = "MWAN_OWNED_RUNTIME_TEST_CHILD"
	ownedRuntimeBinaryEnv = "MWAN_OWNED_RUNTIME_TEST_BINARY"
	ownedRuntimeParentMAC = "02:00:5e:39:70:01"
	ownedRuntimeLateMAC   = "02:00:5e:39:70:02"
)

func TestOwnedLinksDaemonRuntime(t *testing.T) {
	if os.Getenv(ownedRuntimeChildEnv) == "1" {
		runOwnedLinksDaemonRuntime(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedLinksDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), ownedRuntimeChildEnv+"=1", ownedRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated owned-link daemon test: %v: %s", err, output)
	}
}

func runOwnedLinksDaemonRuntime(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
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
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n", filepath.Join(root, "owned-links.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	parentPeer := newRuntimePeer(t, gateway, "ownphys0", "owned-peer", nil, nil, ownedRuntimeParentMAC)
	defer parentPeer.namespace.Close()
	setRuntimeNamespace(t, gateway)
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "foreign-br"}}); err != nil {
		t.Fatal(err)
	}
	writeOwnedRuntimeNetwork(t, networkDir, true, true)
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
	first := startRuntimeDaemon(t, binary, configPath, root, "owned-first")
	defer killOwnedRuntimeDaemon(t, first)
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	bridge := waitOwnedRuntimeLink(t, first, "owned-br", 10*time.Second)
	vlan := waitOwnedRuntimeLink(t, first, "owned397", 10*time.Second)
	removable := waitOwnedRuntimeLink(t, first, "owned398", 10*time.Second)
	parent, err := netlink.LinkByName("ownphys0")
	if err != nil {
		t.Fatal(err)
	}
	if bridge.Type() != "bridge" || vlan.Type() != "vlan" || vlan.Attrs().ParentIndex != parent.Attrs().Index || removable.Type() != "vlan" {
		t.Fatalf("owned links have wrong topology: bridge=%+v vlan=%+v removable=%+v", bridge, vlan, removable)
	}
	bridgeIndex, vlanIndex := bridge.Attrs().Index, vlan.Attrs().Index
	assertOwnedRuntimeVLANPacket(t, gateway, parentPeer.namespace, vlan)
	assertRuntimeDaemonRunning(t, first)
	killOwnedRuntimeDaemon(t, first)
	second := startRuntimeDaemon(t, binary, configPath, root, "owned-second")
	defer killOwnedRuntimeDaemon(t, second)
	waitRuntimeTable(t, second, "inet", "filter", 10*time.Second)
	if got := waitOwnedRuntimeLink(t, second, "owned-br", 10*time.Second); got.Attrs().Index != bridgeIndex {
		t.Fatalf("restart recreated bridge: index %d, want %d", got.Attrs().Index, bridgeIndex)
	}
	if got := waitOwnedRuntimeLink(t, second, "owned397", 10*time.Second); got.Attrs().Index != vlanIndex {
		t.Fatalf("restart recreated VLAN: index %d, want %d", got.Attrs().Index, vlanIndex)
	}
	killOwnedRuntimeDaemon(t, second)
	writeOwnedRuntimeNetwork(t, networkDir, false, true)
	third := startRuntimeDaemon(t, binary, configPath, root, "owned-third")
	defer killOwnedRuntimeDaemon(t, third)
	waitRuntimeTable(t, third, "inet", "filter", 10*time.Second)
	waitOwnedRuntimeAbsent(t, third, "owned398", 10*time.Second)
	for _, name := range []string{"owned-br", "owned397", "ownphys0", "foreign-br", "enmgmt0", "enmwanbr0"} {
		if _, err := netlink.LinkByName(name); err != nil {
			t.Fatalf("removing one child changed %s: %v", name, err)
		}
	}
	latePeer := newRuntimePeer(t, gateway, "latephys0", "late-peer", nil, nil, ownedRuntimeLateMAC)
	defer latePeer.namespace.Close()
	setRuntimeNamespace(t, gateway)
	lateParent := waitOwnedRuntimeLink(t, third, "latephys0", 10*time.Second)
	lateVLAN := waitOwnedRuntimeLink(t, third, "late397", 10*time.Second)
	if lateVLAN.Attrs().ParentIndex != lateParent.Attrs().Index {
		t.Fatalf("late VLAN parent index %d, want %d", lateVLAN.Attrs().ParentIndex, lateParent.Attrs().Index)
	}
	killOwnedRuntimeDaemon(t, third)
	writeOwnedRuntimeNetwork(t, networkDir, false, false)
	fourth := startRuntimeDaemon(t, binary, configPath, root, "owned-fourth")
	defer killOwnedRuntimeDaemon(t, fourth)
	waitRuntimeTable(t, fourth, "inet", "filter", 10*time.Second)
	for _, name := range []string{"owned-br", "owned397", "late397"} {
		waitOwnedRuntimeAbsent(t, fourth, name, 10*time.Second)
	}
	for _, name := range []string{"ownphys0", "latephys0", "foreign-br", "enmgmt0", "enmwanbr0"} {
		if _, err := netlink.LinkByName(name); err != nil {
			t.Fatalf("removing final owned link changed %s: %v", name, err)
		}
	}
}

func killOwnedRuntimeDaemon(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	if daemon.stopped {
		return
	}
	daemon.stopped = true
	if err := daemon.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := daemon.command.Wait(); err == nil {
		t.Fatal("killed daemon exited successfully")
	}
}

func writeOwnedRuntimeNetwork(t *testing.T, directory string, includeSecond, includeOwned bool) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte(`"goodkind-mwan-steering:link-files": "rendered"`), []byte(`"goodkind-mwan-steering:link-files": "hand-authored"`))
	legacyWAN := []byte(`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`)
	typedWAN := []byte(`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "02:00:5e:00:53:aa" }
        }`)
	if !bytes.Contains(fixture, legacyWAN) {
		t.Fatal("test fixture has no hand-authored enatt0 entry")
	}
	fixture = bytes.Replace(fixture, legacyWAN, typedWAN, 1)
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	add := func(value string) {
		entries = append(entries, json.RawMessage(value))
	}
	add(`{"name":"ownphys0","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"ownphys0","goodkind-mwan-steering:owner":"external","goodkind-mwan-steering:link":{"match":{"hardware-address":"` + ownedRuntimeParentMAC + `"}}}`)
	if includeOwned {
		add(`{"name":"owned-br","type":"iana-if-type:bridge","goodkind-mwan-steering:connection-id":"owned-br","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{}}`)
		add(`{"name":"owned397","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"owned397","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"ownphys0","id":397}}}`)
		if includeSecond {
			add(`{"name":"owned398","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"owned398","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"ownphys0","id":398}}}`)
		}
	}
	add(`{"name":"latephys0","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"latephys0","goodkind-mwan-steering:owner":"external","goodkind-mwan-steering:link":{"match":{"hardware-address":"` + ownedRuntimeLateMAC + `"}}}`)
	if includeOwned {
		add(`{"name":"late397","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"late397","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"latephys0","id":397}}}`)
	}
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "network.json"), output, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitOwnedRuntimeLink(t *testing.T, daemon *runtimeDaemon, name string, timeout time.Duration) netlink.Link {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(name)
		if err == nil {
			return link
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("link %s was not created: %s", name, runtimeLogTail(t, daemon, 40))
	return nil
}

func waitOwnedRuntimeAbsent(t *testing.T, daemon *runtimeDaemon, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := netlink.LinkByName(name); netif.IsLinkNotFound(err) {
			return
		} else if err != nil {
			t.Fatalf("inspect link %s: %v", name, err)
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("link %s was not removed: %s", name, runtimeLogTail(t, daemon, 40))
}

func assertOwnedRuntimeVLANPacket(t *testing.T, gateway, peer netns.NsHandle, gatewayVLAN netlink.Link) {
	t.Helper()
	configureRuntimeLink(t, gatewayVLAN.Attrs().Name, []string{"10.39.7.1/24"})
	setRuntimeNamespace(t, peer)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index}, VlanId: 397}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "peer397", []string{"10.39.7.2/24"})
	listener, err := net.ListenPacket("udp4", "10.39.7.2:39700")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, gateway)
	connection, err := net.DialTimeout("udp4", "10.39.7.2:39700", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("owned-vlan")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, peer)
	if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, _, err := listener.ReadFrom(buffer)
	setRuntimeNamespace(t, gateway)
	if err != nil || string(buffer[:count]) != "owned-vlan" {
		t.Fatalf("VLAN packet: count=%d data=%q error=%v", count, buffer[:count], err)
	}
}
