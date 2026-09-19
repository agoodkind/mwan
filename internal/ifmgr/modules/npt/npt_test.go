package npt

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
)

// fakeSource is an injected pd.Source: it returns a canned prefix/ok/err per
// iface so the module runs without touching systemd-networkd or netlink.
type fakeSource struct {
	prefixes map[string]netip.Prefix
	ok       map[string]bool
	err      map[string]error
}

func (f *fakeSource) Prefix(_ context.Context, iface string) (netip.Prefix, bool, error) {
	return f.prefixes[iface], f.ok[iface], f.err[iface]
}

// fakeApplier records the desired set the module hands it, so a test can assert
// on the union of rules without a kernel.
type fakeApplier struct {
	calls int
	last  desiredRules
	err   error
}

func (f *fakeApplier) Apply(_ context.Context, _ *slog.Logger, desired desiredRules) error {
	f.calls++
	f.last = desired
	return f.err
}

type addrCall struct {
	iface string
	specs []netif.AddrSpec
}

// recordingNotifier captures alert traffic so EvaluateAlerts can be checked.
type recordingNotifier struct {
	mu       sync.Mutex
	notifies []notify.Event
	resolves []string
}

func (r *recordingNotifier) Notify(_ context.Context, ev notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifies = append(r.notifies, ev)
}

func (r *recordingNotifier) Resolve(_ context.Context, kind, key, _ string, _ ...slog.Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolves = append(r.resolves, kind+"/"+key)
}

func (r *recordingNotifier) Active(_, _ string) bool { return false }

// testConfig is two providers the configuration assigns a translation prefix,
// so each one's missing delegation is a fault npt alerts on.
func testConfig() Config {
	return Config{
		InternalPrefix: "3d06:bad:b01::/60",
		OpnsenseEdgeV6: "3d06:bad:b01:201::1",
		MwanbrEdgeV6:   "3d06:bad:b01:200::1",
		WANs: []WAN{
			{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, NptPrefix: "2001:db8:a::/60"},
			{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"}, NptPrefix: "2001:db8:b::/60"},
		},
	}
}

// configWithAnUntranslatedProvider adds a third provider the configuration
// assigns no translation prefix: an IPv4-only link, or one whose ISP delegates
// nothing. It never carries a delegation, so its absence is not a fault.
func configWithAnUntranslatedProvider() Config {
	cfg := testConfig()
	cfg.WANs = append(cfg.WANs, WAN{
		WANRef:    ifmgr.WANRef{Name: "astound", Iface: "astound0"},
		NptPrefix: "",
	})
	return cfg
}

func newTestModule(t *testing.T, cfg Config) (*Module, *recordingNotifier) {
	t.Helper()
	mod, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m, ok := mod.(*Module)
	if !ok {
		t.Fatalf("New returned %T, want *Module", mod)
	}
	if err := m.parse(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	notifier := &recordingNotifier{mu: sync.Mutex{}, notifies: nil, resolves: nil}
	m.Env = &ifmgr.Env{
		Iface: "", Sysctl: nil, Log: slog.Default(),
		Alerts: ifmgr.WrapNotifier(notifier), Monitor: nil, DHCP: nil, RA: nil,
	}
	m.Log = slog.Default()
	return m, notifier
}

// addrList wraps a set of CIDRs as the netif.CurrentAddr shape ListAddrs returns.
func addrList(cidrs ...string) []netif.CurrentAddr {
	out := make([]netif.CurrentAddr, 0, len(cidrs))
	for _, cidr := range cidrs {
		fam := "inet6"
		if !strings.Contains(cidr, ":") {
			fam = "inet"
		}
		out = append(out, netif.CurrentAddr{CIDR: cidr, Family: fam, Flags: 0})
	}
	return out
}

// TestReconcileAppliesUnion checks the happy path: both WANs get a live /60, the
// applier is called exactly once with the union, each WAN's <pd>::1/128 is
// ensured on its iface, and an extra global /128 becomes one reverse DNAT while
// <pd>::1 and a link-local address are excluded.
func TestReconcileAppliesUnion(t *testing.T) {
	t.Parallel()

	m, _ := newTestModule(t, testConfig())
	src := &fakeSource{
		prefixes: map[string]netip.Prefix{
			"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56"),
			"webpass0":    netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok:  map[string]bool{"enatt0.3242": true, "webpass0": true},
		err: map[string]error{},
	}
	m.src = src

	var addrCalls []addrCall
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, iface string, specs []netif.AddrSpec) error {
		addrCalls = append(addrCalls, addrCall{iface: iface, specs: specs})
		return nil
	}
	m.listAddrs = func(_ context.Context, _ *slog.Logger, iface string) ([]netif.CurrentAddr, error) {
		if iface == "enatt0.3242" {
			return addrList(
				"2600:1700:2f71:c80::1/128",    // <pd>::1, must be excluded
				"2600:1700:2f71:c85::abcd/128", // extra global, becomes a DNAT
				"fe80::1/128",                  // link-local, must be excluded
			), nil
		}
		return addrList("2001:db8:1:20::1/128"), nil
	}
	app := &fakeApplier{calls: 0, last: desiredRules{Postrouting: nil, Prerouting: nil}, err: nil}
	m.apply = app

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if app.calls != 1 {
		t.Fatalf("applier called %d times, want 1", app.calls)
	}
	// Two WANs * 4 postrouting rules.
	if len(app.last.Postrouting) != 8 {
		t.Fatalf("postrouting rule count = %d, want 8", len(app.last.Postrouting))
	}
	// att: dnat <pd>::1 + dnat-prefix + one extra DNAT = 3; webpass: 2. Total 5.
	if len(app.last.Prerouting) != 5 {
		t.Fatalf("prerouting rule count = %d, want 5", len(app.last.Prerouting))
	}

	// The extra /128 on att becomes a DNAT to the OPNsense edge.
	extra := netip.MustParseAddr("2600:1700:2f71:c85::abcd")
	foundExtra := false
	for _, rule := range app.last.Prerouting {
		if rule.Op == opDNAT && rule.Match == netip.PrefixFrom(extra, 128) {
			foundExtra = true
			if rule.ToAddr != m.opnsenseEdge {
				t.Fatalf("extra DNAT target = %s, want %s", rule.ToAddr, m.opnsenseEdge)
			}
		}
		// <pd>::1 must NOT appear as an extra DNAT (it has its own dedicated rule,
		// but there must be exactly one such match, not a duplicate from the scan).
	}
	if !foundExtra {
		t.Fatal("extra global /128 did not produce a reverse DNAT rule")
	}

	// <pd>::1/128 ensured on each WAN iface.
	wantEnsured := map[string]string{
		"enatt0.3242": "2600:1700:2f71:c80::1/128",
		"webpass0":    "2001:db8:1:20::1/128",
	}
	seen := map[string]bool{}
	for _, call := range addrCalls {
		if len(call.specs) != 1 {
			t.Fatalf("reconcileAddrs(%s) got %d specs, want 1", call.iface, len(call.specs))
		}
		want := wantEnsured[call.iface]
		if call.specs[0].CIDR != want {
			t.Fatalf("reconcileAddrs(%s) CIDR = %s, want %s", call.iface, call.specs[0].CIDR, want)
		}
		seen[call.iface] = true
	}
	if !seen["enatt0.3242"] || !seen["webpass0"] {
		t.Fatalf("not every WAN had its <pd>::1 ensured: %v", seen)
	}
}

// TestReconcilePDMissSkipsWAN checks a WAN with no delegated prefix is skipped
// (its rules absent from the union) and recorded so EvaluateAlerts fires a WARN.
func TestReconcilePDMissSkipsWAN(t *testing.T) {
	t.Parallel()

	m, notifier := newTestModule(t, testConfig())
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{
			"webpass0": netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok:  map[string]bool{"enatt0.3242": false, "webpass0": true},
		err: map[string]error{},
	}
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	app := &fakeApplier{calls: 0, last: desiredRules{Postrouting: nil, Prerouting: nil}, err: nil}
	m.apply = app

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Only webpass programmed: 4 postrouting rules, not 8.
	if len(app.last.Postrouting) != 4 {
		t.Fatalf("postrouting rule count = %d, want 4 (att skipped)", len(app.last.Postrouting))
	}
	for _, rule := range app.last.Postrouting {
		if rule.Iface == "enatt0.3242" {
			t.Fatal("att rules present despite PD miss")
		}
	}

	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.notifies) != 1 {
		t.Fatalf("alert count = %d, want 1", len(notifier.notifies))
	}
	ev := notifier.notifies[0]
	if ev.Level != slog.LevelWarn {
		t.Fatalf("alert level = %v, want WARN", ev.Level)
	}
	if ev.Key != "enatt0.3242" {
		t.Fatalf("alert key = %q, want the att iface", ev.Key)
	}
}

// TestReconcileAddrOpErrorSkipsWAN checks a WAN whose address op fails is skipped
// under the same skip-and-alert contract as a PD miss: its rules are absent from
// the union, EvaluateAlerts fires a WARN for it, and it is not falsely resolved.
func TestReconcileAddrOpErrorSkipsWAN(t *testing.T) {
	t.Parallel()

	m, notifier := newTestModule(t, testConfig())
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{
			"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/60"),
			"webpass0":    netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok:  map[string]bool{"enatt0.3242": true, "webpass0": true},
		err: map[string]error{},
	}
	// att's address op fails; webpass succeeds. att must be excluded and alerted.
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, iface string, _ []netif.AddrSpec) error {
		if iface == "enatt0.3242" {
			return errors.New("netlink: address op failed")
		}
		return nil
	}
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	app := &fakeApplier{calls: 0, last: desiredRules{Postrouting: nil, Prerouting: nil}, err: nil}
	m.apply = app

	if err := m.Reconcile(context.Background(), slog.Default()); err == nil {
		t.Fatal("Reconcile should surface the address-op error")
	}
	// Only webpass programmed: 4 postrouting rules, not 8.
	if len(app.last.Postrouting) != 4 {
		t.Fatalf("postrouting rule count = %d, want 4 (att excluded)", len(app.last.Postrouting))
	}
	for _, rule := range app.last.Postrouting {
		if rule.Iface == "enatt0.3242" {
			t.Fatal("att rules present despite address-op failure")
		}
	}

	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.notifies) != 1 {
		t.Fatalf("alert count = %d, want 1", len(notifier.notifies))
	}
	ev := notifier.notifies[0]
	if ev.Level != slog.LevelWarn {
		t.Fatalf("alert level = %v, want WARN", ev.Level)
	}
	if ev.Key != "enatt0.3242" {
		t.Fatalf("alert key = %q, want the att iface", ev.Key)
	}
	for _, resolved := range notifier.resolves {
		if strings.Contains(resolved, "enatt0.3242") {
			t.Fatalf("att was falsely resolved despite the address-op failure: %v", notifier.resolves)
		}
	}
}

// TestEvaluateAlertsIgnoresAProviderExpectingNoDelegation checks that a
// provider the configuration assigns no translation prefix raises nothing while
// it carries no live delegation. It raises no missing-delegation alert, because
// absence is that provider's steady state rather than a fault, and no resolve
// either, because a provider that never alarms has nothing to recover from.
func TestEvaluateAlertsIgnoresAProviderExpectingNoDelegation(t *testing.T) {
	t.Parallel()

	m, notifier := newTestModule(t, configWithAnUntranslatedProvider())
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{
			"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56"),
			"webpass0":    netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok:  map[string]bool{"enatt0.3242": true, "webpass0": true, "astound0": false},
		err: map[string]error{},
	}
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	m.apply = &fakeApplier{calls: 0, last: desiredRules{Postrouting: nil, Prerouting: nil}, err: nil}

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.notifies) != 0 {
		t.Fatalf("alert count = %d, want 0: %+v", len(notifier.notifies), notifier.notifies)
	}
	for _, resolved := range notifier.resolves {
		if strings.Contains(resolved, "astound0") {
			t.Fatalf("a provider with no configured prefix was resolved: %v", notifier.resolves)
		}
	}
}

// TestEvaluateAlertsResolvesWhenTheExpectedDelegationReturns checks the alert a
// configured translation prefix earns: that provider warns while its delegation
// is absent and resolves once the delegation comes back, while the provider the
// configuration assigns no prefix stays silent through both passes.
func TestEvaluateAlertsResolvesWhenTheExpectedDelegationReturns(t *testing.T) {
	t.Parallel()

	m, notifier := newTestModule(t, configWithAnUntranslatedProvider())
	src := &fakeSource{
		prefixes: map[string]netip.Prefix{
			"webpass0": netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok:  map[string]bool{"enatt0.3242": false, "webpass0": true, "astound0": false},
		err: map[string]error{},
	}
	m.src = src
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	m.apply = &fakeApplier{calls: 0, last: desiredRules{Postrouting: nil, Prerouting: nil}, err: nil}

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())

	notifier.mu.Lock()
	firstPassNotifies := len(notifier.notifies)
	var firstPassKey string
	if firstPassNotifies > 0 {
		firstPassKey = notifier.notifies[0].Key
	}
	notifier.mu.Unlock()
	if firstPassNotifies != 1 {
		t.Fatalf("alert count = %d, want 1 for the provider expecting a delegation", firstPassNotifies)
	}
	if firstPassKey != "enatt0.3242" {
		t.Fatalf("alert key = %q, want the att iface", firstPassKey)
	}

	// The delegation comes back on the provider that expects one.
	src.prefixes["enatt0.3242"] = netip.MustParsePrefix("2600:1700:2f71:c80::/56")
	src.ok["enatt0.3242"] = true

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.notifies) != 1 {
		t.Fatalf("alert count = %d after recovery, want the single first-pass alert: %+v",
			len(notifier.notifies), notifier.notifies)
	}
	wantResolve := alertKindPDMissing + "/enatt0.3242"
	resolved := false
	for _, entry := range notifier.resolves {
		if entry == wantResolve {
			resolved = true
		}
		if strings.Contains(entry, "astound0") {
			t.Fatalf("a provider with no configured prefix was resolved: %v", notifier.resolves)
		}
	}
	if !resolved {
		t.Fatalf("no %q resolve after the delegation returned: %v", wantResolve, notifier.resolves)
	}
}

// TestEvaluateAlertsClearsAStaleAlertWhenTheExpectationIsRetired checks the
// transition the no-prefix branch must not strand: a WAN alerts while it
// expects a delegation and has none, the configuration is then changed to
// name no npt-prefix for it, and the next EvaluateAlerts pass must clear that
// alert rather than leave it active forever. It drives a real
// notify.Manager (via ifmgr.WrapNotifier) rather than the recordingNotifier
// fake, because the fake's Active always reports false and cannot exercise
// the Active-guarded resolve this contract depends on.
func TestEvaluateAlertsClearsAStaleAlertWhenTheExpectationIsRetired(t *testing.T) {
	t.Parallel()

	built, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m, ok := built.(*Module)
	if !ok {
		t.Fatalf("New returned %T, want *Module", built)
	}
	if err := m.parse(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	alerts := ifmgr.WrapNotifier(notify.FromConfig(&config.Config{}, slog.Default(), "npt-test"))
	m.Env = &ifmgr.Env{
		Iface: "", Sysctl: nil, Log: slog.Default(),
		Alerts: alerts, Monitor: nil, DHCP: nil, RA: nil,
	}
	m.Log = slog.Default()
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{
			"webpass0": netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok:  map[string]bool{"enatt0.3242": false, "webpass0": true},
		err: map[string]error{},
	}
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	m.apply = &fakeApplier{calls: 0, last: desiredRules{Postrouting: nil, Prerouting: nil}, err: nil}

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())
	if !alerts.Active(alertKindPDMissing, "enatt0.3242") {
		t.Fatal("setup: alert should be active before the expectation is retired")
	}

	// The operator reconfigures att as an untranslated provider: no npt-prefix.
	m.cfg.WANs[0].NptPrefix = ""

	m.EvaluateAlerts(context.Background(), slog.Default(), time.Now())
	if alerts.Active(alertKindPDMissing, "enatt0.3242") {
		t.Fatal("alert stayed active after att's npt-prefix was retired")
	}
}

// TestModuleDisablesWithoutWANs checks Init self-disables when the shared WAN
// list is empty, like wan.routes.
func TestModuleDisablesWithoutWANs(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.WANs = nil
	mod, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &ifmgr.Env{
		Iface: "", Sysctl: nil, Log: slog.Default(),
		Alerts: ifmgr.WrapNotifier(notify.NullNotifier{}), Monitor: nil, DHCP: nil, RA: nil,
	}
	err = mod.Init(context.Background(), env)
	if err == nil {
		t.Fatal("Init should disable when no WANs are configured")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Init error = %v, want the disabled sentinel", err)
	}
}

// TestNoTeardownMethod is the traffic-continuity guard: the module must not
// expose a stop/close/teardown that would flush the chains on exit. The kernel
// keeps forwarding on the last programmed rules across a binary swap.
func TestNoTeardownMethod(t *testing.T) {
	t.Parallel()

	m := &Module{}
	typ := reflect.TypeOf(m)
	for _, name := range []string{"Stop", "Close", "Teardown", "Shutdown", "Remove"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Fatalf("Module exposes %q; NPT must not tear rules down on stop", name)
		}
	}
}
