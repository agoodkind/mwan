//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vishvananda/netns"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/yangpub"
)

const (
	TunnelRuntimeChildEnv  = "MWAN_TUNNEL_RUNTIME_TEST_CHILD"
	TunnelRuntimeBinaryEnv = "MWAN_TUNNEL_RUNTIME_TEST_BINARY"

	TunnelRuntimeDevice        = "tun6in4"
	TunnelRuntimeUnderlay      = "enisp0"
	TunnelRuntimeISPLink       = "isp-gw"
	TunnelRuntimeFaultTable    = "tunnelfault"
	TunnelRuntimeMTU           = 1400
	TunnelRuntimeLocal         = "203.0.113.2"
	TunnelRuntimeISPGateway    = "203.0.113.1"
	TunnelRuntimeRemote        = "198.51.100.2"
	TunnelRuntimeInnerLocal    = "2001:db8:6::2"
	TunnelRuntimeInnerRemote   = "2001:db8:6::1"
	TunnelRuntimeClientV4      = "192.0.2.2"
	TunnelRuntimeClientV6      = "2001:db8:b01:fe::2"
	TunnelRuntimeRemoteClient  = "2001:db8:99::2"
	TunnelRuntimeUnderlayTbl   = 100
	TunnelRuntimeRouteProto    = netif.TunnelEndpointRouteProtocol
	TunnelRuntimeTransferSize  = 256 * 1024
	TunnelRuntimeEtherIPv4     = 0x0800
	TunnelRuntimeEtherIPv6     = 0x86dd
	TunnelRuntimeProtocol41    = 41
	TunnelRuntimeProtocolTCP   = 6
	TunnelRuntimeAlternate     = "enalt0"
	TunnelRuntimeAltLink       = "alt-gw"
	TunnelRuntimeAltLocal      = "203.0.113.6"
	TunnelRuntimeAltGateway    = "203.0.113.5"
	TunnelRuntimeAltFar        = "198.51.100.5"
	TunnelRuntimeAltEndpoint   = "198.51.100.6"
	TunnelRuntimeAltMetric     = 100
	TunnelRuntimeFailureWindow = 10 * time.Second
	TunnelRuntimeRecoverWindow = 30 * time.Second
)

type TunnelRuntimeRun struct {
	Topology   TunnelRuntimeTopology
	Read       func() string
	Binary     string
	ConfigPath string
	Root       string
	NetworkDir string
	Ipv4       *TunnelRuntimeIPv4
}

func (run TunnelRuntimeRun) Start(t *testing.T, name string) *RuntimeDaemon {
	t.Helper()
	daemon := StartRuntimeDaemon(t, run.Binary, run.ConfigPath, run.Root, name)
	t.Cleanup(func() { KillOwnedRuntimeDaemon(t, daemon) })
	return daemon
}

func PrepareTunnelRuntime(t *testing.T, gateway netns.NsHandle) TunnelRuntimeRun {
	t.Helper()
	root, networkDir, configPath := PrepareRuntimeDirectories(t)
	return TunnelRuntimeRun{
		Topology:   BuildTunnelRuntimeTopology(t, gateway),
		Read:       nil,
		Binary:     os.Getenv(TunnelRuntimeBinaryEnv),
		ConfigPath: configPath,
		Root:       root,
		NetworkDir: networkDir,
		Ipv4:       new(TunnelRuntimeIPv4),
	}
}

func PrepareRuntimeDirectories(t *testing.T) (root, networkDir, configPath string) {
	t.Helper()
	root = t.TempDir()
	networkDir = filepath.Join(root, "mwan")
	networkdDir := filepath.Join(root, "networkd")
	for _, directory := range []string{networkDir, networkdDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	BindStartupDirectory(t, networkDir, "/etc/mwan")
	BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath = filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n"+
			"[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n"+
			"[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[ifmgr.modules.bgp_sessions]\nstate_file = %q\n"+
			"[wanconfig]\npublish = true\n[bgp]\nrouter_id = \"192.0.2.1\"\n",
		filepath.Join(root, "owned-links.json"),
		filepath.Join(root, "owned-addresses.json"),
		filepath.Join(root, "kernel-policy.json"),
		filepath.Join(root, BgpRuntimeStateFile),
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	SetRuntimeLoopback(t)
	return root, networkDir, configPath
}

func RunTunnelProviderDaemonRuntime(t *testing.T) {
	MountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateway.Close() }()
	run := PrepareTunnelRuntime(t, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	WithPrivateRepository(
		t,
		ctx,
		slog.Default(),
		filepath.Join(run.Root, "repository"),
		func(reader yangpub.Publisher) {
			run.Read = func() string {
				value, found, readErr := reader.ExportJSON(
					ctx,
					yangpub.DatastoreOperational,
					"/ietf-interfaces:*",
				)
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
					"endpoint":  run.Topology.Endpoint,
					"remote":    run.Topology.Remote,
					"alternate": run.Topology.Alternate,
					"gateway":   gateway,
				}
				for label, namespace := range peers {
					SetRuntimeNamespace(t, namespace)
					LogTunnelRuntimeKernelState(t, label)
				}
				SetRuntimeNamespace(t, gateway)
				t.Logf("published state: %s", run.Read())
			}()

			first := TunnelRuntimeStartsNotReady(t, run)
			TunnelRuntimeBecomesReady(t, run, first)
			TunnelRuntimeTransfers(t, run, first)
			TunnelRuntimeRepairsEndpointRoute(t, first)
			TunnelRuntimeProbeFault(t, run, first)
			TunnelRuntimeUnderlayFault(t, run, first)
			second := TunnelRuntimeRestart(t, run, first)
			TunnelRuntimeRemoval(t, run, second)
		},
	)
}
