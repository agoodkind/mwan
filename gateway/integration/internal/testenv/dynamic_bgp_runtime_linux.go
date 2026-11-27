//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"context"
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
	"goodkind.io/mwan/gateway/internal/yangpub"
)

const BgpRuntimeStateFile = "bgp-sessions.json"

type BgpRuntimeRun struct {
	Gateway    netns.NsHandle
	Read       func() string
	Binary     string
	ConfigPath string
	Root       string
	NetworkDir string
	Topology   BgpRuntimeTopology
	Routers    map[string]*BgpRuntimeRouter
}

func (run *BgpRuntimeRun) Start(t *testing.T, name string) *RuntimeDaemon {
	t.Helper()
	daemon := StartRuntimeDaemon(t, run.Binary, run.ConfigPath, run.Root, name)
	t.Cleanup(func() { KillOwnedRuntimeDaemon(t, daemon) })
	return daemon
}

func (run *BgpRuntimeRun) StartRouter(t *testing.T, provider BgpRuntimeProvider) *BgpRuntimeRouter {
	t.Helper()
	router := StartBGPRuntimeRouterWithRestart(
		t,
		run.Topology.Routers[provider.Id],
		provider.RouterDevice,
		"198.51.100.1",
		provider.RemoteASN,
		provider.Peer(),
		provider.Local(),
		provider.LocalASN,
		uint32(provider.RestartTime),
	)
	run.Routers[provider.Id] = router
	return router
}

func MountPrivateRuntimeDirectories(t *testing.T) {
	t.Helper()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/run", "/dev/shm"} {
		if err := unix.Mount("tmpfs", target, "tmpfs", 0, ""); err != nil {
			t.Fatalf("mount private %s: %v", target, err)
		}
	}
}

func RunBGPRuntimeIsolated(t *testing.T, body func(*testing.T, *BgpRuntimeRun)) {
	if os.Getenv(TunnelRuntimeChildEnv) != "1" {
		t.Parallel()
		binary := ProtocolTestBinary(t)
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
		}
		child.Env = append(
			os.Environ(),
			TunnelRuntimeChildEnv+"=1",
			TunnelRuntimeBinaryEnv+"="+binary,
		)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated BGP daemon test: %v: %s", err, output)
		}
		return
	}
	MountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateway.Close() }()
	root, networkDir, configPath := PrepareRuntimeDirectories(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	WithPrivateRepository(
		t,
		ctx,
		slog.Default(),
		filepath.Join(root, "repository"),
		func(reader yangpub.Publisher) {
			run := &BgpRuntimeRun{
				Gateway:    gateway,
				Binary:     os.Getenv(TunnelRuntimeBinaryEnv),
				ConfigPath: configPath,
				Root:       root,
				NetworkDir: networkDir,
				Routers:    make(map[string]*BgpRuntimeRouter),
				Read: func() string {
					value, found, readErr := reader.ExportJSON(
						ctx,
						yangpub.DatastoreOperational,
						"/ietf-interfaces:*",
					)
					if readErr != nil || !found {
						return ""
					}
					return value
				},
			}
			defer func() {
				if !t.Failed() {
					return
				}
				namespaces := map[string]netns.NsHandle{"gateway": gateway}
				for id, namespace := range run.Topology.Routers {
					namespaces["router "+id] = namespace
				}
				for label, namespace := range namespaces {
					SetRuntimeNamespace(t, namespace)
					LogTunnelRuntimeKernelState(t, label)
				}
				SetRuntimeNamespace(t, gateway)
				t.Logf("published state: %s", run.Read())
			}()
			body(t, run)
		},
	)
}

func SetBGPRuntimeFault(
	t *testing.T,
	topology BgpRuntimeTopology,
	provider BgpRuntimeProvider,
	dropped bool,
) {
	t.Helper()
	SetRuntimeNamespace(t, topology.Routers[provider.Id])
	if dropped {
		RunRuntimeNFT(t, "add", "table", "ip6", BgpRuntimeFaultTable)
		RunRuntimeNFT(
			t,
			"add",
			"chain",
			"ip6",
			BgpRuntimeFaultTable,
			"forward",
			"{ type filter hook forward priority 0; }",
		)
		RunRuntimeNFT(t, "add", "rule", "ip6", BgpRuntimeFaultTable, "forward", "drop")
	} else {
		RunRuntimeNFT(t, "delete", "table", "ip6", BgpRuntimeFaultTable)
	}
	SetRuntimeNamespace(t, topology.Gateway)
}
