//go:build linux && firewallnetns

package main

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
	"goodkind.io/mwan/internal/yangpub"
)

const bgpRuntimeStateFile = "bgp-sessions.json"

type bgpRuntimeRun struct {
	gateway    netns.NsHandle
	read       func() string
	binary     string
	configPath string
	root       string
	networkDir string
	topology   bgpRuntimeTopology
	routers    map[string]*bgpRuntimeRouter
}

func (run *bgpRuntimeRun) start(t *testing.T, name string) *runtimeDaemon {
	t.Helper()
	daemon := startRuntimeDaemon(t, run.binary, run.configPath, run.root, name)
	t.Cleanup(func() { killOwnedRuntimeDaemon(t, daemon) })
	return daemon
}

func (run *bgpRuntimeRun) startRouter(t *testing.T, provider bgpRuntimeProvider) *bgpRuntimeRouter {
	t.Helper()
	router := startBGPRuntimeRouter(t, run.topology.routers[provider.id], provider.routerDevice, "198.51.100.1",
		provider.remoteASN, provider.peer(), provider.local(), provider.localASN)
	run.routers[provider.id] = router
	return router
}

func runBGPRuntimeIsolated(t *testing.T, body func(*testing.T, *bgpRuntimeRun)) {
	if os.Getenv(tunnelRuntimeChildEnv) != "1" {
		binary := protocolTestBinary(t)
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
		child.Env = append(os.Environ(), tunnelRuntimeChildEnv+"=1", tunnelRuntimeBinaryEnv+"="+binary)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated BGP daemon test: %v: %s", err, output)
		}
		return
	}
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
	root, networkDir, configPath := prepareRuntimeDirectories(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	reader, closeRepository, err := openPrivateRepository(ctx, slog.Default(),
		selftestFlags{repository: filepath.Join(root, "repository"), modelsDir: selftestModelsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	run := &bgpRuntimeRun{
		gateway: gateway, binary: os.Getenv(tunnelRuntimeBinaryEnv), configPath: configPath, root: root, networkDir: networkDir,
		routers: make(map[string]*bgpRuntimeRouter),
		read: func() string {
			value, found, readErr := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
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
		for id, namespace := range run.topology.routers {
			namespaces["router "+id] = namespace
		}
		for label, namespace := range namespaces {
			setRuntimeNamespace(t, namespace)
			logTunnelRuntimeKernelState(t, label)
		}
		setRuntimeNamespace(t, gateway)
		t.Logf("published state: %s", run.read())
	}()
	body(t, run)
}

func setBGPRuntimeFault(t *testing.T, topology bgpRuntimeTopology, provider bgpRuntimeProvider, dropped bool) {
	t.Helper()
	setRuntimeNamespace(t, topology.routers[provider.id])
	if dropped {
		runRuntimeNFT(t, "add", "table", "ip6", bgpRuntimeFaultTable)
		runRuntimeNFT(t, "add", "chain", "ip6", bgpRuntimeFaultTable, "forward", "{ type filter hook forward priority 0; }")
		runRuntimeNFT(t, "add", "rule", "ip6", bgpRuntimeFaultTable, "forward", "drop")
	} else {
		runRuntimeNFT(t, "delete", "table", "ip6", bgpRuntimeFaultTable)
	}
	setRuntimeNamespace(t, topology.gateway)
}
