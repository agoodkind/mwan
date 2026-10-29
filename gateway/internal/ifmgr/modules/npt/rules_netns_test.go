//go:build linux && netns

package npt

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"reflect"
	"testing"

	"github.com/vishvananda/netlink"
)

const (
	renderedGuardLine  = `oif "enatt0.3242" ip6 saddr 3d06:bad:b01:201::1 ct status dnat return`
	renderedSNATLine   = `oif "enatt0.3242" ip6 saddr 3d06:bad:b01:201::1 snat to 2600:1700:2f71:c80::1`
	renderedMwanbrLine = `oif "enatt0.3242" ip6 saddr 3d06:bad:b01:200::1 snat to 2600:1700:2f71:c80::1`
	renderedDNATLine   = `iif "enatt0.3242" ip6 daddr 2600:1700:2f71:c80::1 dnat to 3d06:bad:b01:201::1`
	renderedExtraLine  = `iif "enatt0.3242" ip6 daddr 2600:1700:2f71:c85::abcd dnat to 3d06:bad:b01:201::1`
)

func desiredForTest() desiredRules {
	var desired desiredRules
	desired.add(buildWANRules(wanInputForTest()))
	return desired
}

func renderFromKernel(t *testing.T) RenderedTable {
	t.Helper()
	rendered, err := RenderTable(context.Background(), slog.Default())
	if err != nil {
		t.Fatalf("RenderTable returned error: %v", err)
	}
	return rendered
}

func assertRendered(t *testing.T, got RenderedTable, wantPrerouting []string, wantPostrouting []string) {
	t.Helper()
	if !reflect.DeepEqual(got.Prerouting, wantPrerouting) {
		t.Fatalf("prerouting rules = %q, want %q", got.Prerouting, wantPrerouting)
	}
	if !reflect.DeepEqual(got.Postrouting, wantPostrouting) {
		t.Fatalf("postrouting rules = %q, want %q", got.Postrouting, wantPostrouting)
	}
}

func addDummyLink(t *testing.T, name string) netlink.Link {
	t.Helper()
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	return link
}

// TestNPTRulesRenderFromKernel applies rules to a real kernel and reads them
// back through the production RenderTable.
func TestNPTRulesRenderFromKernel(t *testing.T) {
	t.Run("applied guard, single SNAT, and DNAT rules", func(t *testing.T) {
		inIPv4Namespace(t)
		if err := newNFTApplier().Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
			t.Fatal(err)
		}
		assertRendered(t, renderFromKernel(t),
			[]string{renderedDNATLine, renderedExtraLine},
			[]string{renderedGuardLine, renderedSNATLine, renderedMwanbrLine},
		)
	})

	t.Run("prefix NETMAP rules replace earlier rules", func(t *testing.T) {
		inIPv4Namespace(t)
		apply := newNFTApplier()
		if err := apply.Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
			t.Fatal(err)
		}
		desired := desiredRules{
			Prerouting: []natRule{{
				Chain: chainPrerouting, Iface: "enatt0",
				Match: netip.MustParsePrefix("2001:db8:a::/60"),
				Op:    opDNATPrefix, ToPfx: netip.MustParsePrefix("3d06:bad:b01::/60"),
			}},
			Postrouting: []natRule{{
				Chain: chainPostrouting, Iface: "enatt0",
				Match: netip.MustParsePrefix("3d06:bad:b01::/60"),
				Op:    opSNATPrefix, ToPfx: netip.MustParsePrefix("2001:db8:a::/60"),
			}},
		}
		if err := apply.Apply(context.Background(), slog.Default(), desired); err != nil {
			t.Fatal(err)
		}
		assertRendered(t, renderFromKernel(t),
			[]string{`iif "enatt0" ip6 daddr 2001:db8:a::/60 dnat prefix to 3d06:bad:b01::/60`},
			[]string{`oif "enatt0" ip6 saddr 3d06:bad:b01::/60 snat prefix to 2001:db8:a::/60`},
		)
	})

	t.Run("empty desired set clears both chains", func(t *testing.T) {
		inIPv4Namespace(t)
		apply := newNFTApplier()
		if err := apply.Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
			t.Fatal(err)
		}
		if err := apply.Apply(context.Background(), slog.Default(), desiredRules{}); err != nil {
			t.Fatal(err)
		}
		assertRendered(t, renderFromKernel(t), []string{}, []string{})
		runNFT(t, "list", "chain", "ip6", "nat", preroutingChain)
		runNFT(t, "list", "chain", "ip6", "nat", postroutingChain)
	})

	t.Run("missing table renders empty", func(t *testing.T) {
		inIPv4Namespace(t)
		got := renderFromKernel(t)
		if len(got.Prerouting) != 0 || len(got.Postrouting) != 0 {
			t.Fatalf("rendered table = %#v, want no rules", got)
		}
	})

	t.Run("rules written by nft with interface indexes", func(t *testing.T) {
		inIPv4Namespace(t)
		addDummyLink(t, "enatt0")
		loadIPv4Rules(t, `table ip6 nat {
chain prerouting { type nat hook prerouting priority dstnat; policy accept;
iif "enatt0" ip6 daddr 3d06:bad:b01:2300::1 dnat to 3d06:bad:b01:201::2
iif "enatt0" ip6 daddr 3d06:bad:b01:2300::/60 dnat prefix to 3d06:bad:b01:210::/60
}
chain postrouting { type nat hook postrouting priority srcnat; policy accept;
oif "enatt0" ip6 saddr 3d06:bad:b01:201::2 ct status dnat return
oif "enatt0" ip6 saddr 3d06:bad:b01:201::2 snat to 3d06:bad:b01:2300::1
oif "enatt0" ip6 saddr 3d06:bad:b01:210::/60 snat prefix to 3d06:bad:b01:2300::/60
}
}`)
		assertRendered(t, renderFromKernel(t),
			[]string{
				`iif "enatt0" ip6 daddr 3d06:bad:b01:2300::1 dnat to 3d06:bad:b01:201::2`,
				`iif "enatt0" ip6 daddr 3d06:bad:b01:2300::/60 dnat prefix to 3d06:bad:b01:210::/60`,
			},
			[]string{
				`oif "enatt0" ip6 saddr 3d06:bad:b01:201::2 ct status dnat return`,
				`oif "enatt0" ip6 saddr 3d06:bad:b01:201::2 snat to 3d06:bad:b01:2300::1`,
				`oif "enatt0" ip6 saddr 3d06:bad:b01:210::/60 snat prefix to 3d06:bad:b01:2300::/60`,
			},
		)
	})

	t.Run("interface index without an interface renders the number", func(t *testing.T) {
		inIPv4Namespace(t)
		link := addDummyLink(t, "gonewan")
		index := link.Attrs().Index
		loadIPv4Rules(t, `table ip6 nat {
chain postrouting { type nat hook postrouting priority srcnat; policy accept;
oif "gonewan" ip6 saddr 3d06:bad:b01:201::2 snat to 3d06:bad:b01:2300::1
}
}`)
		if err := netlink.LinkDel(link); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf(`oif "index %d" ip6 saddr 3d06:bad:b01:201::2 snat to 3d06:bad:b01:2300::1`, index)
		got := renderFromKernel(t)
		if !reflect.DeepEqual(got.Postrouting, []string{want}) {
			t.Fatalf("postrouting rules = %q, want %q", got.Postrouting, []string{want})
		}
	})

	t.Run("rule from another program renders as unrecognized", func(t *testing.T) {
		inIPv4Namespace(t)
		loadIPv4Rules(t, `table ip6 nat {
chain prerouting { type nat hook prerouting priority dstnat; policy accept;
counter
}
chain postrouting { type nat hook postrouting priority srcnat; policy accept; }
}`)
		assertRendered(t, renderFromKernel(t), []string{"# unrecognized rule (1 exprs)"}, []string{})
	})

	t.Run("randomized SNAT renders as unrecognized", func(t *testing.T) {
		inIPv4Namespace(t)
		loadIPv4Rules(t, `table ip6 nat {
chain prerouting { type nat hook prerouting priority dstnat; policy accept; }
chain postrouting { type nat hook postrouting priority srcnat; policy accept;
oifname "enatt0" ip6 saddr 3d06:bad:b01:fe::2 snat to 2600:1700:2f71:c80::1 random
}
}`)
		assertRendered(t, renderFromKernel(t), []string{}, []string{"# unrecognized rule (6 exprs)"})
	})
}
