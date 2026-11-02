//go:build linux

package watchdog_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/agent"
	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/watchdog"
	"goodkind.io/mwan/internal/watchdogconfig"
	"google.golang.org/grpc"
)

func TestWatchdogUsesFailoverGuestProbe(t *testing.T) {
	const childEnvironment = "MWAN_FAILOVER_PROBE_TEST_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWatchdogUsesFailoverGuestProbe$", "-test.v")
		child.Env = append(os.Environ(), childEnvironment+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("watchdog failover probe: %v: %s", err, output)
		}
		return
	}

	directory := t.TempDir()
	qm := filepath.Join(directory, "qm")
	build := exec.Command("go", "build", "-o", qm, "./testdata/qm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build guest status helper: %v: %s", err, output)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	speaker := bgp.New(bgp.Config{
		Enabled:    true,
		ASN:        65001,
		RouterID:   "10.0.0.4",
		ListenPort: -1,
		Announce:   bgp.AnnounceConfig{IPv4: []string{"0.0.0.0/0"}},
	}, logger)
	if err := speaker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = speaker.Stop() })
	server := grpc.NewServer()
	mwanv1.RegisterMWANAgentServer(server, agent.NewServer("", logger, speaker, nil))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	logPath := filepath.Join(directory, "watchdog.log")
	config := &watchdogconfig.Config{}
	config.MwanVMID = "213"
	config.GuestType = watchdogconfig.GuestTypeQEMU
	config.Failover.LXCID = "216"
	config.Failover.AgentTCPAddr = listener.Addr().String()
	config.Watchdog.VsockCID = 1
	config.Watchdog.VsockPort = 1
	config.Watchdog.LogFile = logPath
	config.Watchdog.RollbackLockFile = filepath.Join(directory, "rollback.lock")
	config.Watchdog.RollbackStateFile = filepath.Join(directory, "rollback.state")
	config.Network.LastDeployPath = "/var/lib/mwan/last-deploy"
	config.Network.LastChangePath = "/var/run/mwan-last-change"
	config.Network.PingTargetIPv4 = "127.0.0.1"
	config.Network.PingTargetIPv6 = "::1"

	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if speaker.Status(context.Background()).Announcing {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	if err := watchdog.Run(config, watchdog.Options{RedTeam: "proxmox-routing", RedTeamLive: true, RedTeamIterations: 8}); err != nil {
		t.Fatal(err)
	}
	if !speaker.Status(context.Background()).Announcing {
		data, _ := os.ReadFile(logPath)
		t.Fatalf("failover did not announce its route:\n%s", data)
	}
}
