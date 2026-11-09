//go:build linux

package ops_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/agent"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/watchdogconfig"
	"google.golang.org/grpc"
)

func TestGuestExecSelectsTheConfiguredGateway(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	startAgent := func(epoch string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "last-deploy")
		if err := os.WriteFile(path, []byte(epoch), 0o600); err != nil {
			t.Fatal(err)
		}

		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		server := grpc.NewServer()
		mwanv1.RegisterMWANAgentServer(server, agent.NewServer(path, logger, nil, nil))
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)

		return listener.Addr().String()
	}
	config := &watchdogconfig.Config{}
	config.MwanVMID = "213"
	config.Failover.LXCID = "216"
	config.Watchdog.MwanAgentTCPAddr = startAgent("101")
	config.Failover.AgentTCPAddr = startAgent("202")
	guest := ops.NewRealOps(config, logger)

	for _, testCase := range []struct {
		name string
		id   string
		want string
	}{
		{name: "primary", id: "213", want: "101"},
		{name: "failover", id: "216", want: "202"},
	} {
		t.Run(
			testCase.name,
			func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				result, err := guest.GuestExec(ctx, testCase.id, "cat", "/var/lib/mwan/last-deploy")
				if err != nil {
					t.Fatalf("guest command: %v", err)
				}

				if result.ExitCode != 0 || result.Stdout != testCase.want {
					t.Fatalf("guest result = %+v, want timestamp %s", result, testCase.want)
				}
			},
		)
	}
}
