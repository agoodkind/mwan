//go:build linux

package ops

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/agent"
	"goodkind.io/mwan/internal/watchdogconfig"
	"google.golang.org/grpc"
)

func TestGuestExecPingsConfiguredTarget(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := grpc.NewServer()
	mwanv1.RegisterMWANAgentServer(server, agent.NewServer("", logger, nil, nil))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	unavailableListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailableServer := grpc.NewServer()
	go func() { _ = unavailableServer.Serve(unavailableListener) }()
	t.Cleanup(unavailableServer.Stop)

	config := &watchdogconfig.Config{}
	config.MwanVMID = "213"
	config.Watchdog.MwanAgentTCPAddr = listener.Addr().String()
	guest := NewRealOps(config, logger)
	cases := []struct {
		name string
		args []string
	}{
		{name: "ipv4", args: []string{"ping", "-c", "1", "-W", "3", "127.0.0.1"}},
		{name: "ipv6 interface", args: []string{"ping6", "-I", "lo", "-c", "1", "-W", "3", "::1"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, transport := range []string{"vsock", "tcp"} {
				t.Run(transport, func(t *testing.T) {
					address := listener.Addr().String()
					if transport == "tcp" {
						address = unavailableListener.Addr().String()
					}
					guest.testGrpcDialer = func(ctx context.Context, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "tcp", address)
					}
					result, err := guest.GuestExec(context.Background(), config.MwanVMID, testCase.args...)
					if err != nil {
						t.Fatalf("guest ping: %v", err)
					}
					if result.ExitCode != 0 {
						t.Fatalf("guest ping exited %d", result.ExitCode)
					}
				})
			}
		})
	}
}
