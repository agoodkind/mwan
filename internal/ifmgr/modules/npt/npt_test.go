package npt

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

// configWithAnUntranslatedProvider adds a third provider the configuration
// assigns no translation prefix: an IPv4-only link, or one whose ISP delegates
// nothing. It never carries a delegation, so its absence is not a fault.
func configWithAnUntranslatedProvider() Config {
	cfg := testConfig()
	cfg.WANs = append(cfg.WANs, WAN{
		WANRef:      ifmgr.WANRef{Name: "astound", Iface: "astound0"},
		Translation: nil,
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
	// Two WANs * 3 edge exception postrouting rules.
	if len(app.last.Postrouting) != 6 {
		t.Fatalf("postrouting rule count = %d, want 6", len(app.last.Postrouting))
	}
	// Each WAN has an edge DNAT, and att has one extra address DNAT.
	if len(app.last.Prerouting) != 3 {
		t.Fatalf("prerouting rule count = %d, want 3", len(app.last.Prerouting))
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

func TestReconcileClassifiesDHCPv6LocalAddresses(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.WANs = cfg.WANs[:1]
	cfg.WANs[0].Owned = true
	m, _ := newTestModule(t, cfg)
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56")},
		ok:       map[string]bool{"enatt0.3242": true},
	}
	m.Env.Delegations = netif.NewDHCPv6PDStore()
	m.Env.OwnedAddresses = &ifmgr.OwnedAddressResults{}
	m.Env.OwnedAddresses.Replace()
	m.Env.OwnedAddresses.Set("att", "2600:1700:2f71:c80::1/128")
	m.Env.OwnedAddresses.SetFamilyApplied("att", "ipv6")
	localInside := netip.MustParseAddr("2600:1700:2f71:c85::10")
	localOutside := netip.MustParseAddr("2001:db8:abcd::10")
	forward := netip.MustParseAddr("2600:1700:2f71:c85::20")
	expired := netip.MustParseAddr("2600:1700:2f71:c85::30")
	m.Env.Delegations.Set("enatt0.3242", netif.DHCPv6PDLease{
		Addresses: []netif.DelegatedAddress{
			{Address: localInside, ValidUntil: time.Now().Add(time.Hour)},
			{Address: localOutside, ValidUntil: time.Now().Add(time.Hour)},
			{Address: expired, ValidUntil: time.Now().Add(-time.Hour)},
		},
	})
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) {
		return addrList(
			"2600:1700:2f71:c80::1/128",
			localInside.String()+"/128",
			localOutside.String()+"/128",
			forward.String()+"/128",
			expired.String()+"/128",
		), nil
	}
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	app := &fakeApplier{}
	m.apply = app

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if app.calls != 1 {
		t.Fatalf("applier calls = %d, want 1", app.calls)
	}
	wantDNAT := map[netip.Addr]bool{forward: true, expired: true}
	for _, rule := range app.last.Prerouting {
		if rule.Match.Addr() == localInside || rule.Match.Addr() == localOutside {
			t.Fatalf("local IA_NA address %s received reverse DNAT", rule.Match.Addr())
		}
		if wantDNAT[rule.Match.Addr()] {
			delete(wantDNAT, rule.Match.Addr())
		}
	}
	if len(wantDNAT) != 0 {
		t.Fatalf("forwarding addresses missing reverse DNAT: %v", wantDNAT)
	}
	built, present, err := m.buildWANDesired(context.Background(), slog.Default(), cfg.WANs[0], true)
	if err != nil || !present {
		t.Fatalf("buildWANDesired: present=%t, err=%v", present, err)
	}
	wantExceptions := map[netip.Addr]bool{
		netip.MustParseAddr("2600:1700:2f71:c80::1"): true,
		localInside: true,
		forward:     true,
		expired:     true,
	}
	for _, address := range built.pair.DestinationExceptions {
		if !wantExceptions[address] {
			t.Fatalf("unexpected destination exception: %s", address)
		}
		delete(wantExceptions, address)
	}
	if len(wantExceptions) != 0 {
		t.Fatalf("missing destination exceptions: %v", wantExceptions)
	}
}

func TestReconcilePreservesLegacyAddressClassification(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.WANs = cfg.WANs[:1]
	m, _ := newTestModule(t, cfg)
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56")},
		ok:       map[string]bool{"enatt0.3242": true},
	}
	local := netip.MustParseAddr("2600:1700:2f71:c85::10")
	m.Env.Delegations = netif.NewDHCPv6PDStore()
	m.Env.Delegations.Set("enatt0.3242", netif.DHCPv6PDLease{
		Addresses: []netif.DelegatedAddress{{Address: local, ValidUntil: time.Now().Add(time.Hour)}},
	})
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) {
		return addrList(local.String() + "/128"), nil
	}
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	app := &fakeApplier{}
	m.apply = app

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, rule := range app.last.Prerouting {
		if rule.Match == netip.PrefixFrom(local, 128) && rule.Op == opDNAT {
			return
		}
	}
	t.Fatal("legacy global /128 did not receive reverse DNAT")
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

func TestReconcileRetainsLocalExceptionUntilAddressRemoval(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.WANs = cfg.WANs[:1]
	cfg.WANs[0].Owned = true
	m, _ := newTestModule(t, cfg)
	m.src = &fakeSource{
		prefixes: map[string]netip.Prefix{"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56")},
		ok:       map[string]bool{"enatt0.3242": true},
	}
	local := netip.MustParseAddr("2600:1700:2f71:c85::10")
	m.Env.Delegations = netif.NewDHCPv6PDStore()
	m.Env.Delegations.Set("enatt0.3242", netif.DHCPv6PDLease{
		Addresses: []netif.DelegatedAddress{{Address: local, ValidUntil: time.Now().Add(time.Hour)}},
	})
	addressInstalled := true
	m.listAddrs = func(_ context.Context, _ *slog.Logger, _ string) ([]netif.CurrentAddr, error) {
		if addressInstalled {
			return addrList(local.String() + "/128"), nil
		}
		return nil, nil
	}
	m.Env.OwnedAddresses = &ifmgr.OwnedAddressResults{}
	markApplied := func() {
		m.Env.OwnedAddresses.Replace()
		m.Env.OwnedAddresses.Set("att", "2600:1700:2f71:c80::1/128")
		m.Env.OwnedAddresses.SetFamilyApplied("att", "ipv6")
	}
	markApplied()
	app := &fakeApplier{}
	m.apply = app

	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	m.Env.Delegations.Delete("enatt0.3242")
	m.Env.OwnedAddresses.Replace()
	if err := m.Reconcile(context.Background(), slog.Default()); err == nil {
		t.Fatal("failed address reconciliation did not retain NPT state")
	}
	if app.calls != 1 {
		t.Fatalf("NPT applied %d times after address failure, want 1", app.calls)
	}

	markApplied()
	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("reconcile with lingering local address: %v", err)
	}
	for _, rule := range app.last.Prerouting {
		if rule.Match == netip.PrefixFrom(local, 128) {
			t.Fatal("lingering local address received reverse DNAT")
		}
	}
	if !m.previousLocal["enatt0.3242"][local] {
		t.Fatal("NPT forgot local purpose while address remained installed")
	}

	addressInstalled = false
	markApplied()
	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("reconcile after address removal: %v", err)
	}
	if m.previousLocal["enatt0.3242"][local] {
		t.Fatal("NPT retained local purpose after address removal")
	}
}

func TestReconcileWithdrawsOtherWANAfterOwnedAddressFailure(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	for i := range cfg.WANs {
		cfg.WANs[i].Owned = true
	}
	m, _ := newTestModule(t, cfg)
	src := &fakeSource{
		prefixes: map[string]netip.Prefix{
			"enatt0.3242": netip.MustParsePrefix("2600:1700:2f71:c80::/56"),
			"webpass0":    netip.MustParsePrefix("2001:db8:1:20::/60"),
		},
		ok: map[string]bool{"enatt0.3242": true, "webpass0": true},
	}
	m.src = src
	local := netip.MustParseAddr("2600:1700:2f71:c85::10")
	m.Env.Delegations = netif.NewDHCPv6PDStore()
	m.Env.OwnedAddresses = &ifmgr.OwnedAddressResults{}
	m.Env.OwnedAddresses.Replace()
	m.Env.OwnedAddresses.Set("att", "2600:1700:2f71:c80::1/128")
	m.Env.OwnedAddresses.Set("webpass", "2001:db8:1:20::1/128")
	m.Env.OwnedAddresses.SetFamilyApplied("att", "ipv6")
	m.Env.OwnedAddresses.SetFamilyApplied("webpass", "ipv6")
	m.listAddrs = func(_ context.Context, _ *slog.Logger, iface string) ([]netif.CurrentAddr, error) {
		if iface == "enatt0.3242" {
			return addrList("2600:1700:2f71:c80::1/128", local.String()+"/128"), nil
		}
		return addrList("2001:db8:1:20::1/128"), nil
	}
	m.reconcileAddrs = func(_ context.Context, _ *slog.Logger, _ string, _ []netif.AddrSpec) error { return nil }
	app := &fakeApplier{}
	m.apply = app
	if err := m.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if len(app.last.Prerouting) != 3 {
		t.Fatalf("initial reverse DNAT rules = %d, want 3", len(app.last.Prerouting))
	}
	for _, wan := range cfg.WANs {
		built, present, err := m.buildWANDesired(context.Background(), slog.Default(), wan, true)
		if err != nil || !present {
			t.Fatalf("buildWANDesired(%s): present=%t, err=%v", wan.Key(), present, err)
		}
		if m.activePairs == nil {
			m.activePairs = make(map[string]bpf.PrefixPair)
		}
		m.activePairs[wan.Key()] = built.pair
	}
	m.Env.Delegations.Set("enatt0.3242", netif.DHCPv6PDLease{
		Addresses: []netif.DelegatedAddress{{Address: local, ValidUntil: time.Now().Add(time.Hour)}},
	})
	m.preparePolicies = func(_ context.Context, _ *slog.Logger, _ map[string]bpf.PrefixPair) ([]bpf.InterfacePolicy, error) {
		return nil, nil
	}
	m.translator = &recordingTranslator{steps: &[]string{}}
	m.removeDNAT = func(_ context.Context, _ *slog.Logger, _ string, _ []netip.Addr) error { return nil }
	if err := m.prepareLocalIPv6(context.Background(), slog.Default(), "enatt0.3242", []netip.Addr{local}); err != nil {
		t.Fatalf("prepareLocalIPv6: %v", err)
	}
	if !slices.Contains(m.activePairs["att"].DestinationExceptions, local) {
		t.Fatal("pending local address lacks a BPF exception")
	}
	m.translator = nil

	src.ok["webpass0"] = false
	m.Env.OwnedAddresses.Replace()
	m.Env.OwnedAddresses.SetFamilyApplied("webpass", "ipv6")
	if err := m.Reconcile(context.Background(), slog.Default()); err == nil {
		t.Fatal("failed address family did not report an error")
	}
	if app.calls != 2 {
		t.Fatalf("applier calls = %d, want 2", app.calls)
	}
	if len(app.last.Postrouting) != 3 || len(app.last.Prerouting) != 1 {
		t.Fatalf("rules after webpass withdrawal = %+v, want att edge rules only", app.last)
	}
	for _, rule := range app.last.Prerouting {
		if rule.Iface != "enatt0.3242" || rule.Match.Addr() == local {
			t.Fatalf("failed att address received DNAT or webpass remained: %s", rule)
		}
	}
	if len(m.activeRules["webpass"]) != 0 {
		t.Fatal("withdrawn webpass rules remain cached")
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
	// Only webpass's three edge exception rules are programmed.
	if len(app.last.Postrouting) != 3 {
		t.Fatalf("postrouting rule count = %d, want 3 (att skipped)", len(app.last.Postrouting))
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
	// Only webpass's three edge exception rules are programmed.
	if len(app.last.Postrouting) != 3 {
		t.Fatalf("postrouting rule count = %d, want 3 (att excluded)", len(app.last.Postrouting))
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
	m.cfg.WANs[0].Translation = nil

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
