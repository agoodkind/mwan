//go:build linux && netns

package npt

import (
	"context"
	"log/slog"
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/nftables"
)

func TestNPTCreatesAndRepairsOwnNATChains(t *testing.T) {
	inIPv4Namespace(t)
	apply := newNFTApplier()
	for _, phase := range []string{"initial", "repeat", "after deletion"} {
		if phase == "after deletion" {
			runNFT(t, "delete", "table", "ip6", "nat")
		}
		if err := apply.Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
			t.Fatalf("%s apply: %v", phase, err)
		}
		conn, err := nftables.New()
		if err != nil {
			t.Fatal(err)
		}
		table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
		for _, wanted := range []*nftables.Chain{
			{Name: preroutingChain, Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest},
			{Name: postroutingChain, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource},
		} {
			got, err := conn.ListChain(table, wanted.Name)
			if err != nil {
				t.Fatalf("%s %s chain: %v", phase, wanted.Name, err)
			}
			if got.Type != nftables.ChainTypeNAT || got.Hooknum == nil || got.Priority == nil ||
				*got.Hooknum != *wanted.Hooknum || *got.Priority != *wanted.Priority ||
				got.Policy == nil || *got.Policy != nftables.ChainPolicyAccept {
				t.Fatalf("%s %s chain has wrong base definition: %+v", phase, wanted.Name, got)
			}
			rules, err := conn.GetRules(table, got)
			if err != nil || len(rules) == 0 {
				t.Fatalf("%s %s rules: count=%d err=%v", phase, wanted.Name, len(rules), err)
			}
		}
	}
}

func TestNPTRejectsIncompatibleBaseChainWithoutReplacingRules(t *testing.T) {
	inIPv4Namespace(t)
	loadIPv4Rules(t, "table ip6 nat {\nchain prerouting { type nat hook prerouting priority 99; policy accept; }\nchain postrouting { type nat hook postrouting priority srcnat; policy accept; }\n}")
	before := runNFT(t, "list", "table", "ip6", "nat")
	err := newNFTApplier().Apply(context.Background(), slog.Default(), desiredForTest())
	if err == nil || !strings.Contains(err.Error(), "prerouting") {
		t.Fatalf("incompatible prerouting chain error = %v", err)
	}
	if after := runNFT(t, "list", "table", "ip6", "nat"); after != before {
		t.Fatalf("failed apply changed ip6 nat table:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestRemoveReverseDNATKeepsOtherRulesInKernel(t *testing.T) {
	inIPv4Namespace(t)
	local := netip.MustParseAddr("2600:1700:2f71:c85::10")
	forward := netip.MustParseAddr("2600:1700:2f71:c85::20")
	other := netip.MustParseAddr("2001:db8:1:20::30")
	var desired desiredRules
	desired.add(buildWANRules(wanRuleInput{
		Iface: "enatt0.3242", External: netip.MustParsePrefix("2600:1700:2f71:c80::/60"),
		OpnsenseEdge: netip.MustParseAddr("3d06:bad:b01:201::1"), MwanbrEdge: netip.MustParseAddr("3d06:bad:b01:200::1"),
		ExtraDNAT: []netip.Addr{local, forward},
	}))
	desired.add(buildWANRules(wanRuleInput{
		Iface: "webpass0", External: netip.MustParsePrefix("2001:db8:1:20::/60"),
		OpnsenseEdge: netip.MustParseAddr("3d06:bad:b01:201::1"), MwanbrEdge: netip.MustParseAddr("3d06:bad:b01:200::1"),
		ExtraDNAT: []netip.Addr{other},
	}))
	if err := newNFTApplier().Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatal(err)
	}
	if err := removeReverseDNAT(context.Background(), slog.Default(), "enatt0.3242", []netip.Addr{local}); err != nil {
		t.Fatal(err)
	}
	conn, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
	rules, err := conn.GetRules(table, &nftables.Chain{Name: preroutingChain, Table: table})
	if err != nil {
		t.Fatal(err)
	}
	want := map[netip.Addr]bool{forward: true, other: true}
	for _, rule := range rules {
		decoded, ok := decodeRule(ifaceNameByIndex, rule.Exprs)
		if !ok || decoded.Op != opDNAT {
			continue
		}
		if decoded.Match.Addr() == local {
			t.Fatal("local address retained reverse DNAT")
		}
		delete(want, decoded.Match.Addr())
	}
	if len(want) != 0 {
		t.Fatalf("forwarding rules removed: %v", want)
	}
}

func runNFT(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("nft", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("nft %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
