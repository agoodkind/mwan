//go:build linux && firewallnetns

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	tunnelRuntimeChildEnv  = "MWAN_TUNNEL_RUNTIME_TEST_CHILD"
	tunnelRuntimeBinaryEnv = "MWAN_TUNNEL_RUNTIME_TEST_BINARY"

	tunnelRuntimeDevice       = "tun6in4"
	tunnelRuntimeUnderlay     = "enisp0"
	tunnelRuntimeISPLink      = "isp-gw"
	tunnelRuntimeFaultTable   = "tunnelfault"
	tunnelRuntimeMTU          = 1400
	tunnelRuntimeLocal        = "203.0.113.2"
	tunnelRuntimeISPGateway   = "203.0.113.1"
	tunnelRuntimeRemote       = "198.51.100.2"
	tunnelRuntimeInnerLocal   = "2001:db8:6::2"
	tunnelRuntimeInnerRemote  = "2001:db8:6::1"
	tunnelRuntimeClientV4     = "192.0.2.2"
	tunnelRuntimeClientV6     = "2001:db8:b01:fe::2"
	tunnelRuntimeRemoteClient = "2001:db8:99::2"
	tunnelRuntimeUnderlayTbl  = 100
	tunnelRuntimeRouteProto   = netif.TunnelEndpointRouteProtocol
	tunnelRuntimeTransferSize = 256 * 1024
	tunnelRuntimeEtherIPv4    = 0x0800
	tunnelRuntimeEtherIPv6    = 0x86dd
	tunnelRuntimeProtocol41   = 41
	tunnelRuntimeProtocolTCP  = 6
	tunnelRuntimeAlternate    = "enalt0"
	tunnelRuntimeAltLink      = "alt-gw"
	tunnelRuntimeAltLocal     = "203.0.113.6"
	tunnelRuntimeAltGateway   = "203.0.113.5"
	tunnelRuntimeAltFar       = "198.51.100.5"
	tunnelRuntimeAltEndpoint  = "198.51.100.6"
	tunnelRuntimeAltMetric    = 100
	// The health policy reports a failure after two failed cycles. The health policy probes every second.
	// Each failed cycle adds one probe timeout of 500 milliseconds.
	tunnelRuntimeFailureWindow = 10 * time.Second
	tunnelRuntimeRecoverWindow = 30 * time.Second
)

func TestTunnelProviderDaemonRuntime(t *testing.T) {
	if os.Getenv(tunnelRuntimeChildEnv) == "1" {
		runTunnelProviderDaemonRuntime(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestTunnelProviderDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), tunnelRuntimeChildEnv+"=1", tunnelRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated tunnel provider daemon test: %v: %s", err, output)
	}
}

type tunnelRuntimeRun struct {
	topology   tunnelRuntimeTopology
	read       func() string
	binary     string
	configPath string
	root       string
	networkDir string
	ipv4       *tunnelRuntimeIPv4
}

func (run tunnelRuntimeRun) start(t *testing.T, name string) *runtimeDaemon {
	t.Helper()
	daemon := startRuntimeDaemon(t, run.binary, run.configPath, run.root, name)
	t.Cleanup(func() { killOwnedRuntimeDaemon(t, daemon) })
	return daemon
}

func prepareTunnelRuntime(t *testing.T, gateway netns.NsHandle) tunnelRuntimeRun {
	t.Helper()
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	networkdDir := filepath.Join(root, "networkd")
	for _, directory := range []string{networkDir, networkdDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n"+
		"[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n"+
		"[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = true\n",
		filepath.Join(root, "owned-links.json"), filepath.Join(root, "owned-addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	return tunnelRuntimeRun{
		topology: buildTunnelRuntimeTopology(t, gateway), read: nil, binary: os.Getenv(tunnelRuntimeBinaryEnv),
		configPath: configPath, root: root, networkDir: networkDir, ipv4: new(tunnelRuntimeIPv4),
	}
}

func runTunnelProviderDaemonRuntime(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateway.Close() }()
	run := prepareTunnelRuntime(t, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	reader, closeRepository, err := openPrivateRepository(ctx, slog.Default(),
		selftestFlags{repository: filepath.Join(run.root, "repository"), modelsDir: selftestModelsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	run.read = func() string {
		value, found, readErr := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if readErr != nil || !found {
			return ""
		}
		return value
	}
	defer func() {
		if !t.Failed() {
			return
		}
		peers := map[string]netns.NsHandle{
			"endpoint": run.topology.endpoint, "remote": run.topology.remote, "alternate": run.topology.alternate, "gateway": gateway,
		}
		for label, namespace := range peers {
			setRuntimeNamespace(t, namespace)
			logTunnelRuntimeKernelState(t, label)
		}
		setRuntimeNamespace(t, gateway)
		t.Logf("published state: %s", run.read())
	}()

	first := tunnelRuntimeStartsNotReady(t, run)
	tunnelRuntimeBecomesReady(t, run, first)
	tunnelRuntimeTransfers(t, run, first)
	tunnelRuntimeRepairsEndpointRoute(t, first)
	tunnelRuntimeProbeFault(t, run, first)
	tunnelRuntimeUnderlayFault(t, run, first)
	second := tunnelRuntimeRestart(t, run, first)
	tunnelRuntimeRemoval(t, run, second)
}
