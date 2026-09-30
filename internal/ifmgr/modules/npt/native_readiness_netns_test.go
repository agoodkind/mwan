//go:build linux && netns

package npt

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/wanstate"
)

func TestNativeIPv6ReadinessAfterNFTFailure(t *testing.T) {
	const pre = `chain prerouting { type nat hook prerouting priority dstnat; policy accept; %s; };`
	const post = `chain postrouting { type nat hook postrouting priority 99; policy accept; %s; };`
	const dnat = `iifname "nativewan" ip6 daddr 2001:db8:1::1 dnat to fd01:203:405::2`
	const snat = `oifname "nativewan" ip6 saddr fd01:203:405::2 snat to 2001:db8:1::1`
	cases := []struct {
		name  string
		pre   string
		post  string
		ready bool
	}{
		{name: "retained-dnat", pre: fmt.Sprintf(pre, dnat), post: fmt.Sprintf(post, "")},
		{name: "missing-prerouting", post: fmt.Sprintf(post, snat)},
		{name: "missing-postrouting", pre: fmt.Sprintf(strings.ReplaceAll(pre, "dstnat", "-99"), dnat)},
		{name: "unrelated-dnat", pre: fmt.Sprintf(pre, strings.ReplaceAll(dnat, "nativewan", "otherwan")), post: fmt.Sprintf(post, ""), ready: true},
		{name: "unknown-relevant-action", pre: fmt.Sprintf(pre, strings.ReplaceAll(dnat, "ip6 daddr", "counter ip6 daddr")), post: fmt.Sprintf(post, "")},
		{name: "unknown-unrelated-action", pre: fmt.Sprintf(pre, strings.ReplaceAll(strings.ReplaceAll(dnat, "nativewan", "otherwan"), "ip6 daddr", "counter ip6 daddr")), post: fmt.Sprintf(post, ""), ready: true},
		{name: "unidentified-rule", pre: fmt.Sprintf(pre, strings.ReplaceAll(dnat, `iifname "nativewan" `, "")), post: fmt.Sprintf(post, "")},
		{name: "empty-incompatible-chain", pre: fmt.Sprintf(pre, ""), post: fmt.Sprintf(post, ""), ready: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			inIPv4Namespace(t)
			loadIPv4Rules(t, fmt.Sprintf("table ip6 nat { %s %s }", test.pre, test.post))
			before := runNFT(t, "list", "table", "ip6", "nat")
			wan := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nativewan"}}
			if err := netlink.LinkAdd(wan); err != nil {
				t.Fatal(err)
			}
			seed, err := bpf.New()
			if err != nil {
				t.Fatal(err)
			}
			pair := bpf.PrefixPair{ID: 1, Internal: netip.MustParsePrefix("fd01:203:405::/48"), External: netip.MustParsePrefix("2001:db8:1::/48")}
			if _, err := seed.Reconcile([]bpf.InterfacePolicy{{IfIndex: wan.Attrs().Index, Pairs: []bpf.PrefixPair{pair}}}); err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			assertNativeFilters(t, wan, 1)
			module, store, ctx := newNativeReadinessModule(t, nil)
			if err := module.Reconcile(ctx, slog.Default()); err == nil {
				t.Fatal("incompatible nft chain unexpectedly reconciled")
			}
			if after := runNFT(t, "list", "table", "ip6", "nat"); after != before {
				t.Fatalf("failed apply changed nft rules: before=%s after=%s", before, after)
			}
			assertNativeFilters(t, wan, 0)
			state := store.Snapshot().Translation["nativewan"].V6
			if state.Ready != test.ready {
				t.Errorf("native readiness with retained nft rules = %+v, want ready=%v", state, test.ready)
			}
			if !test.ready && state.Reason == "" {
				t.Error("unverified native cleanup has no reason")
			}
			runNFT(t, "delete", "table", "ip6", "nat")
			if err := module.Reconcile(ctx, slog.Default()); err != nil {
				t.Fatal(err)
			}
			assertNativeFilters(t, wan, 0)
			rendered, err := RenderTable(ctx, slog.Default())
			if err != nil || rendered.HasInterface("nativewan") {
				t.Fatalf("native nft cleanup: table=%+v err=%v", rendered, err)
			}
			state = store.Snapshot().Translation["nativewan"].V6
			if !state.Ready || state.Reason != "" {
				t.Fatalf("native readiness after nft/BPF cleanup = %+v", state)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_ = module.Reconcile(canceled, slog.Default())
			if state := store.Snapshot().Translation["nativewan"].V6; state.Ready {
				t.Fatalf("unverified nft inspection reported native ready: %+v", state)
			}
		})
	}
}

func TestNativeIPv6ReadinessIgnoresOtherWANPolicyError(t *testing.T) {
	inIPv4Namespace(t)
	wan := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "otherwan"}}
	if err := netlink.LinkAdd(wan); err != nil {
		t.Fatal(err)
	}
	other := WAN{WANRef: ifmgr.WANRef{Name: "otherwan", Iface: "otherwan"}, Translation: &config.IPv6Translation{
		Mode: config.TranslationNPTv6,
		NPT:  &config.NPTv6Translation{InternalPrefix: netip.MustParsePrefix("fd01:203:405::/48"), ExternalSource: config.PrefixConfigured, ExternalPrefix: netip.MustParsePrefix("2001:db8:1::/48")},
	}}
	module, store, ctx := newNativeReadinessModule(t, []WAN{other})
	if err := module.Reconcile(ctx, slog.Default()); err == nil || !strings.Contains(err.Error(), "internal interface") {
		t.Fatalf("missing internal interface policy error = %v", err)
	}
	if state := store.Snapshot().Translation["nativewan"].V6; !state.Ready || state.Reason != "" {
		t.Fatalf("unrelated policy error blocked native readiness: %+v", state)
	}
	if state := store.Snapshot().Translation["otherwan"].V6; state.Ready {
		t.Fatalf("failed NPT policy reported ready: %+v", state)
	}
}

func newNativeReadinessModule(t *testing.T, other []WAN) (*Module, *wanstate.Store, context.Context) {
	t.Helper()
	cfg := Config{
		InternalIface: "absentlan", InternalPrefix: "fd01:203:405::/48", OpnsenseEdgeV6: "fd01:203:405::2", MwanbrEdgeV6: "fd01:203:405::3",
		WANs: append([]WAN{{WANRef: ifmgr.WANRef{Name: "nativewan", Iface: "nativewan"}, Translation: &config.IPv6Translation{Mode: config.TranslationNative}}}, other...),
	}
	created, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	module := created.(*Module)
	store := wanstate.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := module.Init(ctx, &ifmgr.Env{LiveState: store, Log: slog.Default()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := module.translator.(*bpf.Translator).Close(); err != nil {
			t.Error(err)
		}
	})
	return module, store, ctx
}

func assertNativeFilters(t *testing.T, link netlink.Link, want int) {
	t.Helper()
	for _, parent := range []uint32{netlink.HANDLE_MIN_INGRESS, netlink.HANDLE_MIN_EGRESS} {
		filters, err := netlink.FilterList(link, parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(filters) != want {
			t.Fatalf("native WAN filter count = %d, want %d", len(filters), want)
		}
	}
}
