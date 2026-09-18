package ops

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/tracing"
)

func TestGuestExecLogsFallbackAttemptsWithTraceID(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	logger := slog.New(slog.NewTextHandler(&builder, nil))
	realOps := &RealOps{
		log:     logger,
		tcpAddr: "127.0.0.1:1",
		tracker: NewChannelTracker(),
	}
	realOps.testGrpcDialer = func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("vsock down")
	}
	realOps.testTCPDialer = func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("tcp down")
	}

	ctx := tracing.WithTraceID(context.Background(), "trace-ops")
	_, err := realOps.GuestExec(ctx, "123", "ping", "1.1.1.1")
	if !errors.Is(err, ErrGuestExecUnavailable) {
		t.Fatalf("err=%v", err)
	}

	output := builder.String()
	for _, want := range []string{
		"trace_id=trace-ops",
		"channel=vsock",
		"channel=tcp_mgmt",
		"channel=pve_rest",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
}

// TestLockHoldingTimeoutsOutlastProxmox guards the invariant that a qm
// operation holding a Proxmox configuration lock is waited on for longer
// than Proxmox's own failure path, so the ordinary slow case still returns
// a normal command error rather than a silent wait.
func TestLockHoldingTimeoutsOutlastProxmox(t *testing.T) {
	t.Parallel()

	if TimeoutQmLockHolding < minLockHoldingTimeout {
		t.Errorf(
			"lock-holding budget is %s, which is below the %s floor",
			TimeoutQmLockHolding, minLockHoldingTimeout,
		)
	}
}
