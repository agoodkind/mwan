//go:build linux && protocol

package protocol_test

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
	"goodkind.io/mwan/gateway/integration/internal/testenv"
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
	binary := testenv.ProtocolTestBinary(t)
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
          "match": { "hardware-address": "` + testenv.RuntimeWANMAC + `" }
        }`,
	)
	if !bytes.Contains(fixture, legacy) {
		t.Fatal("network fixture has no external WAN entry")
	}
	fixture = bytes.Replace(fixture, legacy, typed, 1)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
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
	downstream := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.Namespace.Close()
	provider := testenv.NewRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-host",
		[]string{"198.51.100.1/24", "2001:db8:2::1/64"},
		[]string{"198.51.100.2/24", "2001:db8:2::2/64"},
		testenv.RuntimeWANMAC,
	)
	defer provider.Namespace.Close()
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	services := testenv.StartProtocolServices(t, root)
	defer testenv.StopProtocolServices(t, services)
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.AddRuntimeDefault(t, "enatt0", "198.51.100.2")
	testenv.AddRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	testenv.AddRuntimeRoute(t, gateway, downstream.Namespace, "198.51.100.0/24", "192.0.2.1")
	testenv.AddRuntimeRoute(t, gateway, provider.Namespace, "192.0.2.0/29", "198.51.100.1")
	daemon := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(protocolRunnerBinaryEnv),
		configPath,
		root,
		"ifmgr",
	)
	defer testenv.StopRuntimeDaemon(t, daemon)
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	testenv.WaitRuntimeTable(t, daemon, "inet", "mwan_steer", 10*time.Second)
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	testenv.AssertProtocolServicesRunning(t, services)
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.WaitRuntimeTCP(
		t,
		daemon,
		gateway,
		provider.Namespace,
		downstream.Namespace,
		"tcp4",
		"198.51.100.2:30522",
		"192.0.2.2:0",
		time.Now().Add(15*time.Second),
	)
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.AssertRuntimeDaemonRunning(t, daemon)
}
