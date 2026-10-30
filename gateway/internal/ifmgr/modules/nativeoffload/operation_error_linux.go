//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"fmt"
	"log/slog"
)

// Preserve the cause for recovery while the reporting boundary formats context.
type nativeOperationError struct {
	operation    string
	connectionID uint32
	ifindex      int
	cause        error
}

func (failure *nativeOperationError) Error() string {
	return fmt.Sprintf("%s: %v", failure.operation, failure.cause)
}

func (failure *nativeOperationError) Unwrap() error {
	return failure.cause
}

func (failure *nativeOperationError) LogValue() slog.Value {
	attributes := []slog.Attr{
		slog.String("operation", failure.operation),
		slog.Any("cause", failure.cause),
	}
	if failure.connectionID != 0 {
		attributes = append(attributes, slog.Uint64("connection_id", uint64(failure.connectionID)))
	}
	if failure.ifindex != 0 {
		attributes = append(attributes, slog.Int("ifindex", failure.ifindex))
	}
	return slog.GroupValue(attributes...)
}
