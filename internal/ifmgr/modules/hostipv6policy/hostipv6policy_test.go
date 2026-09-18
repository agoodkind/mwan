package hostipv6policy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
)

type fakeSysctlRunner struct {
	values      map[string]string
	missingKeys map[string]bool
}

func (f *fakeSysctlRunner) Get(_ context.Context, key string) (string, error) {
	if f.missingKeys[key] {
		return "", os.ErrNotExist
	}
	value, ok := f.values[key]
	if !ok {
		return "", nil
	}
	return value, nil
}

func (f *fakeSysctlRunner) Set(_ context.Context, key, value string) error {
	if f.missingKeys[key] {
		return os.ErrNotExist
	}
	f.values[key] = value
	return nil
}

func (f *fakeSysctlRunner) DryRun() bool { return false }

type fakeRAClient struct {
	solicitCount int
	solicitErr   error
}

type invalidModuleConfig struct{}

func (invalidModuleConfig) ModuleConfigName() string { return "invalid" }

func (f *fakeRAClient) SolicitRA(_ context.Context, _ time.Duration) (*ndp.RouterAdvertisement, error) {
	f.solicitCount++
	if f.solicitErr != nil {
		return nil, f.solicitErr
	}
	return &ndp.RouterAdvertisement{}, nil
}

func (f *fakeRAClient) Close() error { return nil }

func newTestEnv(sysctl netif.SysctlRunner) *ifmgr.Env {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &ifmgr.Env{
		Iface:  "lo",
		Sysctl: sysctl,
		Log:    log,
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(&config.Config{}, log, "mwan-ifmgr")),
	}
}

func TestInitRejectsInvalidAcceptRA(t *testing.T) {
	t.Parallel()

	module, err := New(Config{
		MissingIfaceGracePeriod: time.Minute,
		Policies:                []InterfacePolicy{{Name: "vmbr0", AcceptRA: 3}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := module.Init(context.Background(), newTestEnv(&fakeSysctlRunner{values: map[string]string{}})); err == nil {
		t.Fatal("Init returned nil error for invalid accept_ra")
	}
}

func TestReconcileWaitsDuringMissingIfaceGracePeriod(t *testing.T) {
	t.Parallel()

	baseTime := time.Unix(100, 0)
	sysctl := &fakeSysctlRunner{
		values: map[string]string{},
		missingKeys: map[string]bool{
			acceptRAKey("vmbr0"): true,
		},
	}
	module, err := New(Config{
		MissingIfaceGracePeriod: 2 * time.Minute,
		Policies:                []InterfacePolicy{{Name: "vmbr0", AcceptRA: 2}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	concreteModule := module.(*Module)
	concreteModule.now = func() time.Time { return baseTime }
	if err := concreteModule.Init(context.Background(), newTestEnv(sysctl)); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := concreteModule.Reconcile(context.Background(), concreteModule.Log); err != nil {
		t.Fatalf("Reconcile returned error during grace period: %v", err)
	}
	concreteModule.now = func() time.Time { return baseTime.Add(3 * time.Minute) }
	if err := concreteModule.Reconcile(context.Background(), concreteModule.Log); err == nil {
		t.Fatal("Reconcile returned nil error after missing iface grace expired")
	}
}

func TestReconcileCleansDeniedRADefault(t *testing.T) {
	t.Parallel()

	sysctl := &fakeSysctlRunner{values: map[string]string{
		acceptRAKey("vmbr4"):       "1",
		autoconfKey("vmbr4"):       "1",
		acceptRADefRtrKey("vmbr4"): "1",
	}}
	module, err := New(Config{
		Policies: []InterfacePolicy{{
			Name:             "vmbr4",
			AcceptRA:         0,
			AutoConf:         false,
			AcceptRADefRtr:   false,
			CleanupRADefault: true,
		}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	concreteModule := module.(*Module)
	deleteCallCount := 0
	concreteModule.findMainRADefault = func(context.Context, string) (*netif.CurrentRoute, error) {
		return &netif.CurrentRoute{Dest: "default", Via: "fe80::1", Dev: "vmbr4"}, nil
	}
	concreteModule.deleteMainRADefaults = func(context.Context, *slog.Logger, string) (int, error) {
		deleteCallCount++
		return 1, nil
	}
	if err := concreteModule.Init(context.Background(), newTestEnv(sysctl)); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := concreteModule.Reconcile(context.Background(), concreteModule.Log); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := sysctl.values[acceptRAKey("vmbr4")]; got != "0" {
		t.Fatalf("accept_ra got %q, want %q", got, "0")
	}
	if got := sysctl.values[autoconfKey("vmbr4")]; got != "0" {
		t.Fatalf("autoconf got %q, want %q", got, "0")
	}
	if got := sysctl.values[acceptRADefRtrKey("vmbr4")]; got != "0" {
		t.Fatalf("accept_ra_defrtr got %q, want %q", got, "0")
	}
	if deleteCallCount != 1 {
		t.Fatalf("deleteMainRADefaults call count = %d, want 1", deleteCallCount)
	}
}

func TestReconcileSolicitsAllowedIfaceRA(t *testing.T) {
	t.Parallel()

	sysctl := &fakeSysctlRunner{values: map[string]string{
		acceptRAKey("vmbr0"):       "0",
		autoconfKey("vmbr0"):       "0",
		acceptRADefRtrKey("vmbr0"): "0",
	}}
	module, err := New(Config{
		Policies: []InterfacePolicy{{
			Name:           "vmbr0",
			AcceptRA:       2,
			AutoConf:       true,
			AcceptRADefRtr: true,
			SolicitRA:      true,
		}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	concreteModule := module.(*Module)
	lookupCount := 0
	concreteModule.findMainRADefault = func(context.Context, string) (*netif.CurrentRoute, error) {
		lookupCount++
		if lookupCount == 1 {
			return nil, nil
		}
		return &netif.CurrentRoute{Dest: "default", Via: "fe80::2", Dev: "vmbr0"}, nil
	}
	fakeClient := &fakeRAClient{}
	concreteModule.newRAClient = func(string, *slog.Logger) (routerSoliciter, error) {
		return fakeClient, nil
	}
	if err := concreteModule.Init(context.Background(), newTestEnv(sysctl)); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := concreteModule.Reconcile(context.Background(), concreteModule.Log); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := sysctl.values[acceptRAKey("vmbr0")]; got != "2" {
		t.Fatalf("accept_ra got %q, want %q", got, "2")
	}
	if got := sysctl.values[autoconfKey("vmbr0")]; got != "1" {
		t.Fatalf("autoconf got %q, want %q", got, "1")
	}
	if got := sysctl.values[acceptRADefRtrKey("vmbr0")]; got != "1" {
		t.Fatalf("accept_ra_defrtr got %q, want %q", got, "1")
	}
	if fakeClient.solicitCount != 1 {
		t.Fatalf("SolicitRA call count = %d, want 1", fakeClient.solicitCount)
	}
}

func TestNewRejectsInvalidConfigType(t *testing.T) {
	t.Parallel()

	_, err := New(invalidModuleConfig{})
	if err == nil {
		t.Fatal("New returned nil error for invalid config type")
	}
}

func TestInitReturnsDisabledSentinelWhenPoliciesEmpty(t *testing.T) {
	t.Parallel()

	module, err := New(Config{MissingIfaceGracePeriod: time.Minute})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	initErr := module.Init(context.Background(), newTestEnv(&fakeSysctlRunner{values: map[string]string{}}))
	if initErr == nil {
		t.Fatal("Init returned nil error for empty Policies, want ErrModuleDisabled")
	}
	if !errors.Is(initErr, ifmgr.ErrModuleDisabled) {
		t.Fatalf("Init returned err=%v, want errors.Is(err, ifmgr.ErrModuleDisabled)", initErr)
	}
}
