// Package forwardingready reports current gateway forwarding readiness over a Unix socket.
package forwardingready

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
)

// State reports whether each address family can forward downstream traffic.
type State struct {
	IPv4 bool `json:"ipv4"`
	IPv6 bool `json:"ipv6"`
}

// Serve writes the current state to each Unix socket connection until ctx ends.
func Serve(ctx context.Context, socketPath string, timeout time.Duration, snapshot func() State) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("serve forwarding readiness: %w", err)
	}
	if timeout <= 0 {
		err := fmt.Errorf("forwarding readiness timeout must be positive")
		slog.ErrorContext(ctx, "forwarding readiness cannot start", "error", err)
		return err
	}
	if snapshot == nil {
		err := fmt.Errorf("forwarding readiness snapshot is nil")
		slog.ErrorContext(ctx, "forwarding readiness cannot start", "error", err)
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		if removeErr := removeStaleSocket(ctx, socketPath, timeout); removeErr != nil {
			slog.ErrorContext(ctx, "forwarding readiness socket unavailable", "path", socketPath, "error", removeErr)
			return fmt.Errorf("prepare forwarding readiness socket: %w", removeErr)
		}
		listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	}
	if err != nil {
		slog.ErrorContext(ctx, "forwarding readiness listen failed", "path", socketPath, "error", err)
		return fmt.Errorf("listen on forwarding readiness socket: %w", err)
	}
	defer func() { _ = listener.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.ErrorContext(ctx, "forwarding readiness accept failed", "error", err)
			return fmt.Errorf("accept forwarding readiness connection: %w", err)
		}
		writeState(ctx, conn, timeout, snapshot)
	}
}

func removeStaleSocket(ctx context.Context, socketPath string, timeout time.Duration) error {
	info, err := os.Lstat(socketPath)
	if err != nil {
		slog.ErrorContext(ctx, "inspect forwarding readiness socket failed", "path", socketPath, "error", err)
		return fmt.Errorf("inspect forwarding readiness socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		err := fmt.Errorf("forwarding readiness path is not a socket")
		slog.ErrorContext(ctx, "forwarding readiness socket path is occupied", "path", socketPath, "error", err)
		return err
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err == nil {
		_ = conn.Close()
		err := fmt.Errorf("forwarding readiness socket is active")
		slog.ErrorContext(ctx, "forwarding readiness socket already active", "path", socketPath, "error", err)
		return err
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		slog.ErrorContext(ctx, "check forwarding readiness socket failed", "path", socketPath, "error", err)
		return fmt.Errorf("check forwarding readiness socket: %w", err)
	}
	if err := os.Remove(socketPath); err != nil {
		slog.ErrorContext(ctx, "remove stale forwarding readiness socket failed", "path", socketPath, "error", err)
		return fmt.Errorf("remove stale forwarding readiness socket: %w", err)
	}
	return nil
}

func writeState(ctx context.Context, conn net.Conn, timeout time.Duration, snapshot func() State) {
	defer func() { _ = conn.Close() }()
	clock := internalclock.Real{}
	if err := conn.SetWriteDeadline(clock.Now().Add(timeout)); err != nil {
		slog.WarnContext(ctx, "set forwarding readiness write deadline failed", "error", err)
		return
	}
	if err := json.NewEncoder(conn).Encode(snapshot()); err != nil {
		slog.WarnContext(ctx, "write forwarding readiness state failed", "error", err)
	}
}

// Read returns one current state from the Unix socket.
func Read(ctx context.Context, socketPath string, timeout time.Duration) (State, error) {
	var state State
	if err := ctx.Err(); err != nil {
		return state, fmt.Errorf("read forwarding readiness: %w", err)
	}
	if timeout <= 0 {
		err := fmt.Errorf("forwarding readiness timeout must be positive")
		slog.ErrorContext(ctx, "forwarding readiness read cannot start", "error", err)
		return state, err
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		slog.DebugContext(ctx, "forwarding readiness dial failed", "path", socketPath, "error", err)
		return state, fmt.Errorf("dial forwarding readiness socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	clock := internalclock.Real{}
	deadline := clock.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		slog.ErrorContext(ctx, "set forwarding readiness read deadline failed", "error", err)
		return state, fmt.Errorf("set forwarding readiness read deadline: %w", err)
	}
	if err := json.NewDecoder(conn).Decode(&state); err != nil {
		slog.DebugContext(ctx, "decode forwarding readiness state failed", "error", err)
		return State{}, fmt.Errorf("decode forwarding readiness state: %w", err)
	}
	return state, nil
}
