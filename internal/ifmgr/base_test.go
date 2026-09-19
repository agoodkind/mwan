package ifmgr

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"goodkind.io/mwan/internal/netif"
)

func TestBaseModuleStoresSharedModuleState(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := &Env{
		Iface:   "test0",
		Sysctl:  nil,
		Log:     log,
		Alerts:  nil,
		Monitor: nil,
		DHCP:    nil,
		RA:      nil,
	}
	base := NewBaseModule("test_module")
	base.InitBase(env, "module", "test_module")

	if got := base.Name(); got != "test_module" {
		t.Fatalf("Name() = %q, want %q", got, "test_module")
	}
	if got := base.Env; got != env {
		t.Fatalf("Env = %p, want %p", got, env)
	}
	if got := base.Log; got == nil {
		t.Fatal("Log is nil after InitBase")
	}

	base.Lock()
	base.Unlock()
}

func TestBaseModuleDefaultLifecycleHooksAreNoOps(t *testing.T) {
	t.Parallel()

	base := NewBaseModule("test_module")
	if err := base.OnKernelEvent(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		netif.Event{},
	); err != nil {
		t.Fatalf("OnKernelEvent returned error: %v", err)
	}
	if err := base.OnDHCPLease(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		netif.LeaseInfo{},
	); err != nil {
		t.Fatalf("OnDHCPLease returned error: %v", err)
	}

	base.EvaluateAlerts(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Now(),
	)
}
