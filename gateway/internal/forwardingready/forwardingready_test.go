package forwardingready_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/forwardingready"
)

const testTimeout = time.Second

func TestServeReadsCurrentStateAndStops(t *testing.T) {
	directory, err := os.MkdirTemp("", "forwardingready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "ready.sock")

	var mu sync.Mutex
	state := forwardingready.State{}
	snapshot := func() forwardingready.State {
		mu.Lock()
		defer mu.Unlock()
		return state
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- forwardingready.Serve(ctx, socketPath, testTimeout, snapshot) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		got, readErr := forwardingready.Read(context.Background(), socketPath, testTimeout)
		if readErr == nil {
			if got != (forwardingready.State{}) {
				t.Fatalf("initial state = %+v, want both families false", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	state = forwardingready.State{IPv4: true, IPv6: true}
	mu.Unlock()
	got, err := forwardingready.Read(context.Background(), socketPath, testTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if got != (forwardingready.State{IPv4: true, IPv6: true}) {
		t.Fatalf("updated state = %+v, want both families true", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
	if _, err := forwardingready.Read(context.Background(), socketPath, testTimeout); err == nil {
		t.Fatal("Read succeeded after server shutdown")
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
}

func TestServeReplacesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "ready.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- forwardingready.Serve(ctx, socketPath, testTimeout, func() forwardingready.State {
			return forwardingready.State{IPv4: true}
		})
	}()
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err := forwardingready.Read(context.Background(), socketPath, testTimeout)
		if err == nil {
			if state != (forwardingready.State{IPv4: true}) {
				t.Fatalf("state after stale socket replacement = %+v", state)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not replace stale socket: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeDoesNotReplaceActiveSocket(t *testing.T) {
	directory, err := os.MkdirTemp("", "fr-active-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "ready.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := forwardingready.Serve(ctx, socketPath, testTimeout, func() forwardingready.State {
		return forwardingready.State{}
	}); err == nil {
		t.Fatal("Serve replaced an active socket")
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("active listener stopped accepting connections: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsNonpositiveTimeout(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "ready.sock")
	ctx := context.Background()
	if err := forwardingready.Serve(ctx, socketPath, 0, func() forwardingready.State {
		return forwardingready.State{}
	}); err == nil {
		t.Fatal("Serve accepted a zero timeout")
	}
	if _, err := forwardingready.Read(ctx, socketPath, 0); err == nil {
		t.Fatal("Read accepted a zero timeout")
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("invalid Serve created a socket: %v", err)
	}
}
