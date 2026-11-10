package ifmgr_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
)

func TestForwardingChangeRetiresBeforePublicationAndWaitsForCompletion(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	publication := filepath.Join(t.TempDir(), "applied")
	completed := make(chan []byte, 1)
	var changes ifmgr.ForwardingChanges
	unregister, err := changes.Register(func(context.Context) (func(), error) {
		if err := listener.Close(); err != nil {
			return nil, err
		}
		return func() {
			data, readErr := os.ReadFile(publication)
			if readErr != nil {
				t.Error(readErr)
			}
			completed <- data
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	ctx, finish, err := changes.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if connection, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second); err == nil {
		connection.Close()
		t.Fatal("retired listener accepted a connection before the writer published")
	}
	unregistered := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		unregister()
		close(unregistered)
	}()
	<-started
	select {
	case <-unregistered:
		t.Fatal("unregistration returned while the writer was active")
	case <-time.After(20 * time.Millisecond):
	}
	beginNestedForwardingChange(t, &changes, ctx, finish)
	if err := os.WriteFile(publication, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	finish()
	if data := <-completed; string(data) != "ready\n" {
		t.Fatalf("completion read publication %q", data)
	}
	select {
	case <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("unregistration did not finish after publication")
	}
	finish()
}

func TestForwardingWorkerRetiresIndependentlyAndCancellationPreservesListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var changes ifmgr.ForwardingChanges
	unregister, err := changes.Register(func(context.Context) (func(), error) {
		if err := listener.Close(); err != nil {
			return nil, err
		}
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := changes.Begin(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation returned %v", err)
	}
	connection, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("canceled mutation retired the listener: %v", err)
	}
	connection.Close()
	ctx, finish, err := changes.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	worker := ifmgr.WithoutForwardingChange(ctx)
	if _, _, err := changes.Begin(worker); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("independent worker did not attempt its own retirement: %v", err)
	}
}

func TestForwardingRegistrationWaitsForAnUnacceleratedWriter(t *testing.T) {
	var changes ifmgr.ForwardingChanges
	ctx, finish, err := changes.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	type registrationResult struct {
		unregister func()
		err        error
	}
	registered := make(chan registrationResult, 1)
	go func() {
		unregister, err := changes.Register(func(context.Context) (func(), error) {
			if err := listener.Close(); err != nil {
				return nil, err
			}
			return func() {}, nil
		})
		registered <- registrationResult{unregister: unregister, err: err}
	}()
	select {
	case result := <-registered:
		finish()
		if result.unregister != nil {
			result.unregister()
		}
		t.Fatalf("registration completed before the writer: %v", result.err)
	case <-time.After(20 * time.Millisecond):
	}
	beginNestedForwardingChange(t, &changes, ctx, finish)
	finish()
	result := <-registered
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(result.unregister)
	_, retireFinish, err := changes.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	retireFinish()
	if connection, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second); err == nil {
		connection.Close()
		t.Fatal("registered retirement did not close the listener")
	}
}

func beginNestedForwardingChange(t *testing.T, changes *ifmgr.ForwardingChanges, ctx context.Context, outerFinish func()) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, finish, err := changes.Begin(ctx)
		if err == nil {
			finish()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested writer attempted another retirement: %v", err)
		}
	case <-time.After(time.Second):
		outerFinish()
		t.Fatal("nested writer blocked behind registration or unregistration")
	}
}
