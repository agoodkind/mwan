//go:build linux && protocol

package commandline

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	protocolRunnerChildEnv  = "MWAN_PROTOCOL_RUNNER_CHILD"
	protocolRunnerBinaryEnv = "MWAN_PROTOCOL_RUNNER_BINARY"
)

func TestProtocolRunnerBootstrap(t *testing.T) {
	if os.Getenv(protocolRunnerChildEnv) == "1" {
		runProtocolRunnerBootstrap(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestProtocolRunnerBootstrap$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		protocolRunnerChildEnv+"=1",
		protocolRunnerBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated protocol runner: %v: %s", err, output)
	}
}

func runProtocolRunnerBootstrap(t *testing.T) {
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
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(
		fixture,
		[]byte(`"goodkind-mwan-steering:link-files": "rendered"`),
		[]byte(`"goodkind-mwan-steering:link-files": "hand-authored"`),
	)
	legacy := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`,
	)
	typed := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "` + runtimeWANMAC + `" }
        }`,
	)
	if !bytes.Contains(fixture, legacy) {
		t.Fatal("network fixture has no external WAN entry")
	}
	fixture = bytes.Replace(fixture, legacy, typed, 1)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := "[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = false\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		"/proc/sys/net/ipv6/conf/all/forwarding",
		[]byte("1"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.namespace.Close()
	downstream := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.namespace.Close()
	provider := newRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-host",
		[]string{"198.51.100.1/24", "2001:db8:2::1/64"},
		[]string{"198.51.100.2/24", "2001:db8:2::2/64"},
		runtimeWANMAC,
	)
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	services := startProtocolServices(t, root)
	defer stopProtocolServices(t, services)
	setRuntimeNamespace(t, gateway)
	addRuntimeDefault(t, "enatt0", "198.51.100.2")
	addRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	addRuntimeRoute(t, gateway, downstream.namespace, "198.51.100.0/24", "192.0.2.1")
	addRuntimeRoute(t, gateway, provider.namespace, "192.0.2.0/29", "198.51.100.1")
	daemon := startRuntimeDaemon(t, os.Getenv(protocolRunnerBinaryEnv), configPath, root, "ifmgr")
	defer stopRuntimeDaemon(t, daemon)
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	waitRuntimeTable(t, daemon, "inet", "mwan_steer", 10*time.Second)
	setRuntimeNamespace(t, provider.namespace)
	assertProtocolServicesRunning(t, services)
	setRuntimeNamespace(t, gateway)
	waitRuntimeTCP(
		t,
		daemon,
		gateway,
		provider.namespace,
		downstream.namespace,
		"tcp4",
		"198.51.100.2:30522",
		"192.0.2.2:0",
		time.Now().Add(15*time.Second),
	)
	setRuntimeNamespace(t, gateway)
	assertRuntimeDaemonRunning(t, daemon)
}
