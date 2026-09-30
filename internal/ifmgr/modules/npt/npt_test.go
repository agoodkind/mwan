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

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
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

type recordingTranslator struct {
	steps    *[]string
	policies []bpf.InterfacePolicy
	err      error
}

func (r *recordingTranslator) Reconcile(policies []bpf.InterfacePolicy) ([]bpf.AttachmentState, error) {
	*r.steps = append(*r.steps, "bpf")
	r.policies = policies
	return nil, r.err
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
			{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, Translation: testDelegatedTranslation("2001:db8:a::/60")},
			{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"}, Translation: testDelegatedTranslation("2001:db8:b::/60")},
		},
	}
}

func testDelegatedTranslation(expected string) *config.IPv6Translation {
	return &config.IPv6Translation{
		Mode: config.TranslationNPTv6,
		NPT: &config.NPTv6Translation{
			InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
			ExternalSource: config.PrefixDelegated,
			ExpectedPrefix: netip.MustParsePrefix(expected),
		},
	}
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

func TestPrepareLocalIPv6RetainsOtherWANPairWhenPrefixLookupFails(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.WANs[0].Owned = true
	m, _ := newTestModule(t, cfg)
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56")},
		ok:       map[string]bool{"enatt0.3242": true},
	}
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	previous := bpf.PrefixPair{
		ID: pairID("webpass"), Internal: netip.MustParsePrefix("3d06:bad:b01::/60"),
		External:              netip.MustParsePrefix("2001:db8:1:20::/60"),
		DestinationExceptions: []netip.Addr{netip.MustParseAddr("2001:db8:1:20::1")},
	}
	m.activePairs = map[string]bpf.PrefixPair{"webpass": previous}
	m.preparePolicies = func(_ context.Context, _ *slog.Logger, pairs map[string]bpf.PrefixPair) ([]bpf.InterfacePolicy, error) {
		if !reflect.DeepEqual(pairs["webpass"], previous) {
			t.Fatalf("unresolved WAN policy changed: %v", pairs["webpass"])
		}
		if _, ok := pairs["att"]; !ok {
			t.Fatal("target WAN pair missing")
		}
		return []bpf.InterfacePolicy{{IfIndex: 1, Pairs: []bpf.PrefixPair{pairs["att"], pairs["webpass"]}}}, nil
	}
	var steps []string
	m.translator = &recordingTranslator{steps: &steps}
	m.removeDNAT = func(_ context.Context, _ *slog.Logger, _ string, _ []netip.Addr) error {
		steps = append(steps, "nft")
		return nil
	}
	if err := m.prepareLocalIPv6(context.Background(), slog.Default(), "enatt0.3242", []netip.Addr{netip.MustParseAddr("2600:1700:2f71:c85::20")}); err != nil {
		t.Fatalf("prepareLocalIPv6: %v", err)
	}
	if !reflect.DeepEqual(steps, []string{"bpf", "nft"}) {
		t.Fatalf("preparation order = %v", steps)
	}
}

func TestPrepareLocalIPv6StopsBeforeDNATOnBPFError(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.WANs = cfg.WANs[:1]
	cfg.WANs[0].Owned = true
	m, _ := newTestModule(t, cfg)
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56")},
		ok:       map[string]bool{"enatt0.3242": true},
	}
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) { return nil, nil }
	m.preparePolicies = func(_ context.Context, _ *slog.Logger, pairs map[string]bpf.PrefixPair) ([]bpf.InterfacePolicy, error) {
		return []bpf.InterfacePolicy{{IfIndex: 1, Pairs: []bpf.PrefixPair{pairs["att"]}}}, nil
	}
	var steps []string
	m.translator = &recordingTranslator{steps: &steps, err: errors.New("BPF failed")}
	m.removeDNAT = func(_ context.Context, _ *slog.Logger, _ string, _ []netip.Addr) error {
		steps = append(steps, "nft")
		return nil
	}

	err := m.prepareLocalIPv6(context.Background(), slog.Default(), "enatt0.3242", []netip.Addr{netip.MustParseAddr("2600:1700:2f71:c85::20")})
	if err == nil || !strings.Contains(err.Error(), "BPF failed") {
		t.Fatalf("prepareLocalIPv6 error = %v", err)
	}
	if !reflect.DeepEqual(steps, []string{"bpf"}) {
		t.Fatalf("preparation steps = %v, want BPF only", steps)
	}
	steps = nil
	err = m.prepareLocalIPv6(context.Background(), slog.Default(), "enatt0.3242", []netip.Addr{netip.MustParseAddr("2600:1700:2f71:c80::1")})
	if err == nil || !strings.Contains(err.Error(), "conflicts with the NPT edge address") {
		t.Fatalf("edge address preparation error = %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("edge conflict changed translation: %v", steps)
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
