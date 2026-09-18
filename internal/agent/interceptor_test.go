package agent

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/tracing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func TestUnaryTraceInterceptorUsesIncomingTraceID(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil)).
		With(slog.String(tracing.RunIDKey, "run-test")).
		With(slog.String(tracing.ComponentKey, "agent"))

	listener := bufconn.Listen(bufSize)
	server := grpc.NewServer(
		grpc.UnaryInterceptor(unaryTraceInterceptor(logger)),
	)
	mwanv1.RegisterMWANAgentServer(
		server,
		NewServer("/nonexistent", testLogger(t), nil, nil),
	)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop() })

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := mwanv1.NewMWANAgentClient(conn)
	ctx := metadata.NewOutgoingContext(
		context.Background(),
		metadata.Pairs("x-trace-id", "trace-abc"),
	)
	if _, err := client.GetConfigState(ctx, &mwanv1.GetConfigStateRequest{}); err != nil {
		t.Fatal(err)
	}

	output := buffer.String()
	if !strings.Contains(output, "trace_id=trace-abc") {
		t.Fatalf("output=%q", output)
	}
	if !strings.Contains(output, "grpc request started") {
		t.Fatalf("output=%q", output)
	}
	if !strings.Contains(output, "rpc_method=/mwan.v1.MWANAgent/GetConfigState") {
		t.Fatalf("output=%q", output)
	}
}
