package netif

import (
	"context"
	"errors"
	"net"
	"os"
)

// ProbeSetupError lets consumers reject local initialization failures with the original diagnostics.
type ProbeSetupError struct {
	cause error
}

// Error preserves the production probe diagnostic.
func (failure *ProbeSetupError) Error() string {
	return failure.cause.Error()
}

// Unwrap preserves inspection of the original failure with [errors.Is] and [errors.As].
func (failure *ProbeSetupError) Unwrap() error {
	return failure.cause
}

func probeSetupError(cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return &ProbeSetupError{cause: cause}
}

func probeDialError(cause error) error {
	var socketError *os.SyscallError
	if errors.As(cause, &socketError) && socketError.Syscall == "socket" {
		return probeSetupError(cause)
	}
	return cause
}

func dialHTTPProbe(ctx context.Context, dialer *net.Dialer, network, address string) (net.Conn, error) {
	connection, err := dialer.DialContext(ctx, network, address)
	return connection, probeDialError(err)
}
