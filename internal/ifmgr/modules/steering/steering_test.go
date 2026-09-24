package steering

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/wanstate"
)

// fakeApplier records the rule set the module hands it, so a test can assert on
// the computed rules without a kernel.
type fakeApplier struct {
	calls int
	last  []steerRule
	err   error
}

func (f *fakeApplier) Apply(_ context.Context, _ *slog.Logger, desired []steerRule) error {
	f.calls++
	f.last = desired
	return f.err
}

func testConfig() Config {
	return Config{
		InternalIface:   "enmwanbr0",
		InternalNetV4:   "10.250.250.0/29",
		InternalPrefix:  "3d06:bad:b01::/60",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		HashMode:        "random",
		HealthStateFile: "/run/mwan-health.state",
		Members:         membersForTest(),
	}
}

func newTestModule(t *testing.T, cfg Config, health netif.HealthStates) (*Module, *fakeApplier) {
	t.Helper()
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	typed, isModule := module.(*Module)
	if !isModule {
		t.Fatalf("New returned %T, want *Module", module)
	}
	if err := typed.parse(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	store := wanstate.New()
	routing := make(map[string]wanstate.MemberRouting)
	translation := make(map[string]wanstate.MemberTranslation)
	for _, member := range cfg.Members {
		routing[member.Name] = wanstate.MemberRouting{V4Ready: true, V6Ready: true}
		translation[member.Name] = wanstate.MemberTranslation{V4: wanstate.FamilyTranslation{Ready: true}, V6: wanstate.FamilyTranslation{Ready: true}}
	}
	store.SetRouting(0, routing)
	store.SetTranslation(translation)
	typed.Env = &ifmgr.Env{Log: slog.Default(), Alerts: ifmgr.WrapNotifier(notify.NullNotifier{}), LiveState: store}
	typed.Log = slog.Default()
	typed.readHealth = func(string) (netif.HealthStates, error) { return health, nil }
	applier := &fakeApplier{calls: 0, last: nil, err: nil}
	typed.apply = applier
	return typed, applier
}

// TestReconcileProgramsTheActiveTierSplit is the happy path: both first-tier
// providers are healthy, so one reconcile hands the applier three rules whose
// map spreads across both marks.
func TestReconcileProgramsTheActiveTierSplit(t *testing.T) {
	t.Parallel()

	module, applier := newTestModule(t, testConfig(), netif.HealthStates{
		"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	})
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if applier.calls != 1 {
		t.Fatalf("applier called %d times, want 1", applier.calls)
	}
	assignments := 0
	for index, rule := range applier.last {
		if rule.DropIface != "" {
			continue
		}
		assignments++
		want := balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}}
		if !reflect.DeepEqual(rule.Assign, want) {
			t.Fatalf("rule %d balancer = %#v, want %#v", index, rule.Assign, want)
		}
	}
	if assignments != 3 {
		t.Fatalf("assignment count = %d, want 3", assignments)
	}
}

// TestReconcileWithNoHealthyProviderProgramsNothing pins the empty case: the
// applier is still called, so the previous split is cleared, but it is handed
// no rules.
func TestReconcileWithNoHealthyProviderProgramsNothing(t *testing.T) {
	t.Parallel()

	module, applier := newTestModule(t, testConfig(), netif.HealthStates{
		"att": netif.HealthStateUnhealthy, "webpass": netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateUnhealthy,
	})
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if applier.calls != 1 {
		t.Fatalf("applier called %d times, want 1", applier.calls)
	}
	for _, rule := range applier.last {
		if rule.DropIface == "" || len(rule.AllowedMarks) != 0 {
			t.Fatalf("ineligible family kept a steering rule: %v", rule)
		}
	}
}

// TestReconcileWrapsApplyError asserts a failing applier surfaces through
// Reconcile, so a rejected chain program reaches the daemon's reconcile loop
// as an error rather than being mistaken for a completed pass.
func TestReconcileWrapsApplyError(t *testing.T) {
	t.Parallel()

	module, applier := newTestModule(t, testConfig(), netif.HealthStates{
		"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	})
	applier.err = errors.New("apply rejected")
	err := module.Reconcile(context.Background(), slog.Default())
	if err == nil {
		t.Fatal("Reconcile returned nil error, want the wrapped apply error")
	}
	if !errors.Is(err, applier.err) {
		t.Fatalf("Reconcile error = %v, want it to wrap %v", err, applier.err)
	}
}

// TestModuleDisablesWithoutMembers checks Init self-disables when the provider
// list is empty, so the wan role can list the module unconditionally.
func TestModuleDisablesWithoutMembers(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Members = nil
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &ifmgr.Env{Log: slog.Default(), Alerts: ifmgr.WrapNotifier(notify.NullNotifier{})}
	initErr := module.Init(context.Background(), env)
	if initErr == nil {
		t.Fatal("Init should disable when no providers are configured")
	}
	if !strings.Contains(initErr.Error(), "disabled") {
		t.Fatalf("Init error = %v, want the disabled sentinel", initErr)
	}
}

// TestParseRejectsAnUnknownHashMode pins the loud failure: a hash mode the
// module cannot program stops the daemon rather than silently balancing some
// other way.
func TestParseRejectsAnUnknownHashMode(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.HashMode = "round-robin"
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	typed, isModule := module.(*Module)
	if !isModule {
		t.Fatalf("New returned %T, want *Module", module)
	}
	if parseErr := typed.parse(); parseErr == nil {
		t.Fatal("parse accepted an unknown hash mode")
	}
}

// TestParseRejectsAWeightSumThatCannotBeProgrammed pins the bound on the map: a
// weight the kernel would turn into a runaway element list is a configuration
// error, not something to attempt.
func TestParseRejectsAWeightSumThatCannotBeProgrammed(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Members[0].Weight = maxSlots
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	typed, isModule := module.(*Module)
	if !isModule {
		t.Fatalf("New returned %T, want *Module", module)
	}
	if parseErr := typed.parse(); parseErr == nil {
		t.Fatal("parse accepted a weight sum above the bound")
	}
}

// TestNoTeardownMethod is the traffic-continuity guard: the module must not
// expose a stop or teardown that would empty the chain on exit. The kernel keeps
// marking on the last programmed rules across a binary swap.
func TestNoTeardownMethod(t *testing.T) {
	t.Parallel()

	module := &Module{}
	typ := reflect.TypeOf(module)
	for _, name := range []string{"Stop", "Close", "Teardown", "Shutdown", "Remove"} {
		if _, found := typ.MethodByName(name); found {
			t.Fatalf("Module exposes %q; steering must not clear its chain on stop", name)
		}
	}
}

func TestFamilyRulesUseIndependentReadyTiers(t *testing.T) {
	t.Parallel()
	module, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := module.(*Module)
	if err := m.parse(); err != nil {
		t.Fatal(err)
	}
	store := wanstate.New()
	m.Env = &ifmgr.Env{LiveState: store}
	store.SetRouting(0, map[string]wanstate.MemberRouting{
		"att":          {V4Ready: true, V6Ready: true},
		"webpass":      {V4Ready: true, V6Ready: false},
		"monkeybrains": {V4Ready: true, V6Ready: true},
	})
	translations := map[string]wanstate.MemberTranslation{
		"att":          {V4: wanstate.FamilyTranslation{Ready: true}, V6: wanstate.FamilyTranslation{Ready: false}},
		"webpass":      {V4: wanstate.FamilyTranslation{Ready: true}},
		"monkeybrains": {V4: wanstate.FamilyTranslation{Ready: true}, V6: wanstate.FamilyTranslation{Ready: true}},
	}
	store.SetTranslation(translations)
	rules := m.desiredRules(netif.HealthStates{})
	v6Assignments := 0
	for _, rule := range rules {
		if rule.DropIface != "" {
			continue
		}
		if rule.Source.Addr().Is4() {
			if !reflect.DeepEqual(rule.Assign.Slots, []uint32{1, 2}) {
				t.Fatalf("IPv4 changed tier: %+v", rule.Assign)
			}
		} else {
			v6Assignments++
			if rule.Assign.Mark != 3 {
				t.Fatalf("IPv6 selected an unready or absent family: %+v", rule.Assign)
			}
		}
	}
	if v6Assignments == 0 {
		t.Fatal("no IPv6 assignment")
	}
	translations["monkeybrains"] = wanstate.MemberTranslation{V4: wanstate.FamilyTranslation{Ready: true}}
	store.SetTranslation(translations)
	rules = m.desiredRules(netif.HealthStates{})
	for _, rule := range rules {
		if rule.DropIface == "" && rule.Source.Addr().Is6() {
			t.Fatalf("IPv6 readiness loss kept assignment: %v", rule)
		}
	}
}
