package ifmgr

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/tracing"
)

// fakeModule is a Module implementation that records every dispatch the
// daemon makes into it. Used to verify the framework's lifecycle wiring
// without depending on any real netlink/iface operations.
type fakeModule struct {
	name      string
	initCount int32
	reconcile int32
	kernelEv  int32
	dhcpEv    int32
	alertEv   int32
	traceSeen int32
	mu        sync.Mutex
	lastEnv   *Env
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Init(_ context.Context, env *Env) error {
	atomic.AddInt32(&f.initCount, 1)
	f.mu.Lock()
	f.lastEnv = env
	f.mu.Unlock()
	return nil
}

func (f *fakeModule) Reconcile(ctx context.Context, _ *slog.Logger) error {
	atomic.AddInt32(&f.reconcile, 1)
	if tracing.TraceID(ctx) != "" {
		atomic.AddInt32(&f.traceSeen, 1)
	}
	return nil
}

func (f *fakeModule) OnKernelEvent(_ context.Context, _ *slog.Logger, _ netif.Event) error {
	atomic.AddInt32(&f.kernelEv, 1)
	return nil
}

func (f *fakeModule) OnDHCPLease(_ context.Context, _ *slog.Logger, _ netif.LeaseInfo) error {
	atomic.AddInt32(&f.dhcpEv, 1)
	return nil
}

func (f *fakeModule) EvaluateAlerts(_ context.Context, _ *slog.Logger, _ time.Time) {
	atomic.AddInt32(&f.alertEv, 1)
}

// withFakeRole registers a role+module pair scoped to one test, restoring
// the previous registration on cleanup.
func withFakeRole(t *testing.T, role, modName string, mod Module) {
	t.Helper()

	// Save previous registration if any, then install ours.
	prev, hadPrev := Lookup(modName)
	registryMu.Lock()
	registry[modName] = func(ModuleConfig) (Module, error) { return mod, nil }
	registryMu.Unlock()

	prevRole, hadRole := roleModules[role]
	roleModules[role] = []string{modName}

	t.Cleanup(func() {
		registryMu.Lock()
		if hadPrev {
			registry[modName] = prev
		} else {
			delete(registry, modName)
		}
		registryMu.Unlock()
		if hadRole {
			roleModules[role] = prevRole
		} else {
			delete(roleModules, role)
		}
	})
}

func TestDaemonInitAndReconcile(t *testing.T) {
	mod := &fakeModule{name: "fake1"}
	withFakeRole(t, "test-role", "fake1", mod)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := NewDaemon(log, DaemonConfig{
		Role:              "test-role",
		Iface:             "lo", // exists on every Linux box
		ReconcileInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Init runs once, Reconcile runs at least twice (initial + at least one tick).
	if got := atomic.LoadInt32(&mod.initCount); got != 1 {
		t.Errorf("initCount=%d, want 1", got)
	}
	if got := atomic.LoadInt32(&mod.reconcile); got < 2 {
		t.Errorf("reconcile count=%d, want >=2", got)
	}
	if got := atomic.LoadInt32(&mod.traceSeen); got < 1 {
		t.Errorf("traceSeen=%d, want >=1", got)
	}
	// Env was populated.
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.lastEnv == nil {
		t.Fatal("Env not provided to module Init")
	}
	if mod.lastEnv.Iface != "lo" {
		t.Errorf("env.Iface=%q want lo", mod.lastEnv.Iface)
	}
	if mod.lastEnv.Alerts == nil {
		t.Error("env.Alerts is nil")
	}
	if mod.lastEnv.Sysctl == nil {
		t.Error("env.Sysctl is nil")
	}
	if mod.lastEnv.Monitor == nil {
		t.Error("env.Monitor is nil")
	}
}

func TestUnknownRoleErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := NewDaemon(log, DaemonConfig{
		Role:              "no-such-role",
		Iface:             "lo",
		ReconcileInterval: 50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestUnknownModuleErrors(t *testing.T) {
	registryMu.Lock()
	prev, hadPrev := roleModules["test-role-with-missing"]
	roleModules["test-role-with-missing"] = []string{"definitely-not-registered"}
	registryMu.Unlock()
	t.Cleanup(func() {
		if hadPrev {
			roleModules["test-role-with-missing"] = prev
		} else {
			delete(roleModules, "test-role-with-missing")
		}
	})

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := NewDaemon(log, DaemonConfig{
		Role:              "test-role-with-missing",
		Iface:             "lo",
		ReconcileInterval: 50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected error for unregistered module")
	}
}

func TestAlertManagerNotifyTransitionAndRepeat(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	am := NewAlertManager(log, AlertConfig{RepeatEvery: 100 * time.Millisecond})

	now := time.Now()
	if am.Active("kind", "key") {
		t.Fatal("should not be active before first Notify")
	}
	am.Notify(now, slog.LevelWarn, "kind", "key", "first")
	if !am.Active("kind", "key") {
		t.Fatal("should be active after Notify")
	}

	// Resolve clears.
	am.Resolve(now, "kind", "key", "fixed")
	if am.Active("kind", "key") {
		t.Fatal("should not be active after Resolve")
	}
}
