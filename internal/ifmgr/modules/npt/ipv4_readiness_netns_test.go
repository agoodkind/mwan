//go:build linux && netns

package npt

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/wanstate"
)

func TestIPv4TranslationReadinessKernel(t *testing.T) {
	const masquerade = "oifname \"wan0\" ip saddr 192.0.2.0/24 masquerade"
	const sourceMapping = "oifname \"wan0\" ip saddr 192.0.2.10 snat to 198.51.100.10"
	const destinationMapping = "iifname \"wan0\" ip daddr 198.51.100.10 dnat to 192.0.2.10"
	cases := []struct {
		name     string
		post     string
		pre      string
		mappings bool
		ready    bool
		reason   string
		priority string
	}{
		{name: "wrong-hook-priority", post: masquerade, priority: "99", reason: "base chain"},
		{name: "masquerade", post: masquerade, ready: true},
		{name: "wrong-interface", post: strings.ReplaceAll(masquerade, "wan0", "wan1"), reason: "masquerade"},
		{name: "wrong-source", post: strings.ReplaceAll(masquerade, "192.0.2.0/24", "192.0.3.0/24"), reason: "masquerade"},
		{name: "narrow-source", post: strings.ReplaceAll(masquerade, "/24", "/25"), reason: "masquerade"},
		{name: "missing-masquerade", reason: "masquerade"},
		{name: "static-mappings", post: sourceMapping + "; " + masquerade, pre: destinationMapping, mappings: true, ready: true},
		{name: "missing-source-mapping", post: masquerade, pre: destinationMapping, mappings: true, reason: "source mapping"},
		{name: "shadowed-source-mapping", post: masquerade + "; " + sourceMapping, pre: destinationMapping, mappings: true, reason: "overridden"},
		{name: "missing-destination-mapping", post: sourceMapping + "; " + masquerade, mappings: true, reason: "destination mapping"},
		{name: "wrong-destination-target", post: sourceMapping + "; " + masquerade, pre: strings.ReplaceAll(destinationMapping, "192.0.2.10", "192.0.2.11"), mappings: true, reason: "destination mapping"},
		{name: "mark-pin-and-static", post: sourceMapping + "; " + masquerade, pre: "iifname \"lan0\" ip saddr 192.0.2.1 udp sport 51820 udp dport 51821 ct state new meta mark set 2; " + destinationMapping, mappings: true, ready: true},
		{name: "extra-predicate", post: strings.ReplaceAll(masquerade, "ip saddr", "meta mark 2 ip saddr"), reason: "unsupported"},
		{name: "earlier-return", post: "return; " + masquerade, reason: "cannot be verified"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			inIPv4Namespace(t)
			priority := test.priority
			if priority == "" {
				priority = "srcnat"
			}
			rules := fmt.Sprintf("table ip nat { chain prerouting { type nat hook prerouting priority dstnat; policy accept; %s; }; chain postrouting { type nat hook postrouting priority %s; policy accept; %s; }; }", test.pre, priority, test.post)
			loadIPv4Rules(t, rules)
			wan := WAN{WANRef: ifmgr.WANRef{Name: "provider", Iface: "wan0"}, TranslationV4: &config.IPv4Translation{Mode: config.TranslationNAPT44}}
			if test.mappings {
				wan.TranslationV4.StaticMappings = []config.StaticMapping{{Internal: netip.MustParseAddr("192.0.2.10"), External: netip.MustParseAddr("198.51.100.10")}}
			}
			state := IPv4TranslationReadiness(context.Background(), wan, netip.MustParsePrefix("192.0.2.0/24"))
			if state.Ready != test.ready || !strings.Contains(state.Reason, test.reason) {
				t.Fatalf("readiness = %+v, want ready=%v and reason containing %q", state, test.ready, test.reason)
			}
			loadIPv4Rules(t, "delete table ip nat")
			if actual := IPv4TranslationReadiness(context.Background(), wan, netip.MustParsePrefix("192.0.2.0/24")); actual.Ready {
				t.Fatal("deleted NAT table still reports ready")
			}
			wan.TranslationV4.Mode = config.TranslationNative
			wan.TranslationV4.StaticMappings = nil
			if actual := IPv4TranslationReadiness(context.Background(), wan, netip.Prefix{}); !actual.Ready || actual.Reason != "" {
				t.Fatalf("native without a NAT table = %+v", actual)
			}
		})
	}
}

func inIPv4Namespace(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	previous, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	current, err := netns.New()
	if err != nil {
		previous.Close()
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netns.Set(previous); err != nil {
			t.Error(err)
		}
		current.Close()
		previous.Close()
		runtime.UnlockOSThread()
	})
}

func loadIPv4Rules(t *testing.T, rules string) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "rules.nft")
	if err := os.WriteFile(filename, []byte(rules+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("nft", "-f", filename).CombinedOutput(); err != nil {
		t.Fatalf("nft: %v: %s", err, output)
	}
}

func TestNativeIPv6WaitsForStaleTranslationCleanup(t *testing.T) {
	inIPv4Namespace(t)
	loadIPv4Rules(t, "table ip6 nat { chain prerouting { type nat hook prerouting priority dstnat; policy accept; }; chain postrouting { type nat hook postrouting priority srcnat; policy accept; }; }")
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "wan0"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	translator, err := bpf.New()
	if err != nil {
		t.Fatal(err)
	}
	pair := bpf.PrefixPair{ID: 1, Internal: netip.MustParsePrefix("fd00:1::/48"), External: netip.MustParsePrefix("2001:db8:1::/48")}
	if _, err := translator.Reconcile([]bpf.InterfacePolicy{{IfIndex: link.Attrs().Index, Pairs: []bpf.PrefixPair{pair}}}); err != nil {
		t.Fatal(err)
	}
	if err := translator.Close(); err != nil {
		t.Fatal(err)
	}
	assertFilters := func(want int) {
		t.Helper()
		for _, parent := range []uint32{netlink.HANDLE_MIN_INGRESS, netlink.HANDLE_MIN_EGRESS} {
			filters, err := netlink.FilterList(link, parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(filters) != want {
				t.Fatalf("filter count = %d, want %d", len(filters), want)
			}
		}
	}
	assertFilters(1)
	cfg := testConfig()
	cfg.WANs = []WAN{{WANRef: ifmgr.WANRef{Name: "native", Iface: "wan0"}, Translation: &config.IPv6Translation{Mode: config.TranslationNative}}}
	module, _ := newTestModule(t, cfg)
	store := wanstate.New()
	module.Env.LiveState = store
	module.apply = newNFTApplier()
	module.translator = translator
	if err := module.Reconcile(context.Background(), module.Log); err == nil {
		t.Fatal("closed translator unexpectedly reconciled")
	}
	if store.Snapshot().Translation["native"].V6.Ready {
		t.Fatal("native IPv6 reported ready while stale translation remained attached")
	}
	assertFilters(1)
	translator, err = bpf.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { translator.Close() })
	module.translator = translator
	if err := module.Reconcile(context.Background(), module.Log); err != nil {
		t.Fatal(err)
	}
	assertFilters(0)
	if !store.Snapshot().Translation["native"].V6.Ready {
		t.Fatal("native IPv6 did not become ready after stale translation cleanup")
	}
}
