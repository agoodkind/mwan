//go:build linux

package offload

import (
	"fmt"
	"log/slog"
	"time"
)

// ConntrackTimeoutVerificationError retains the values from the failed readback.
type ConntrackTimeoutVerificationError struct {
	ConnectionID     uint32
	RequestedSeconds uint32
	ObservedSeconds  uint32
	Elapsed          time.Duration
	AllowanceSeconds uint64
}

func (failure *ConntrackTimeoutVerificationError) Error() string {
	return fmt.Sprintf("conntrack %d did not retain the requested timeout", failure.ConnectionID)
}

func (failure *ConntrackTimeoutVerificationError) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("operation", "verify_timeout"),
		slog.Uint64("connection_id", uint64(failure.ConnectionID)),
		slog.Uint64("requested_seconds", uint64(failure.RequestedSeconds)),
		slog.Uint64("observed_seconds", uint64(failure.ObservedSeconds)),
		slog.Duration("elapsed", failure.Elapsed),
		slog.Uint64("allowance_seconds", failure.AllowanceSeconds),
	)
}
