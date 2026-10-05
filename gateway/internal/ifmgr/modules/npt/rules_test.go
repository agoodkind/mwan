package npt

import (
	"net/netip"
	"reflect"
	"testing"
)

// wanInputForTest configures one WAN's edge address exceptions.
func wanInputForTest() wanRuleInput {
	return wanRuleInput{
		Iface:        "enatt0.3242",
		External:     netip.MustParsePrefix("2600:1700:2f71:c80::/60"),
		OpnsenseEdge: netip.MustParseAddr("3d06:bad:b01:201::1"),
		MwanbrEdge:   netip.MustParseAddr("3d06:bad:b01:200::1"),
		ExtraDNAT:    []netip.Addr{netip.MustParseAddr("2600:1700:2f71:c85::abcd")},
	}
}

// TestBuildWANRules keeps the edge exceptions in order and excludes prefix NAT.
func TestBuildWANRules(t *testing.T) {
	t.Parallel()

	iface := "enatt0.3242"
	pd1 := netip.MustParseAddr("2600:1700:2f71:c80::1")
	edge := netip.MustParseAddr("3d06:bad:b01:201::1")
	mwanbr := netip.MustParseAddr("3d06:bad:b01:200::1")
	extra := netip.MustParseAddr("2600:1700:2f71:c85::abcd")

	want := []natRule{
		// postrouting, in order (guard MUST be first).
		{Chain: chainPostrouting, Iface: iface, Match: netip.PrefixFrom(edge, 128), Op: opGuard},
		{Chain: chainPostrouting, Iface: iface, Match: netip.PrefixFrom(edge, 128), Op: opSNAT, ToAddr: pd1},
		{Chain: chainPostrouting, Iface: iface, Match: netip.PrefixFrom(mwanbr, 128), Op: opSNAT, ToAddr: pd1},
		// prerouting.
		{Chain: chainPrerouting, Iface: iface, Match: netip.PrefixFrom(pd1, 128), Op: opDNAT, ToAddr: edge},
		{Chain: chainPrerouting, Iface: iface, Match: netip.PrefixFrom(extra, 128), Op: opDNAT, ToAddr: edge},
	}

	got := buildWANRules(wanInputForTest())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildWANRules mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestBuildWANRulesNoExtra checks the edge exceptions without an extra address.
func TestBuildWANRulesNoExtra(t *testing.T) {
	t.Parallel()

	in := wanInputForTest()
	in.ExtraDNAT = nil
	got := buildWANRules(in)
	if len(got) != 4 {
		t.Fatalf("rule count = %d, want 4 (no extra /128)", len(got))
	}
	if got[len(got)-1].Op != opDNAT {
		t.Fatalf("last rule op = %v, want opDNAT", got[len(got)-1].Op)
	}
}

// TestExternalHostOne checks the first host address for different prefix lengths.
func TestExternalHostOne(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"2600:1700:2f71:c80::/60": "2600:1700:2f71:c80::1",
		"2607:f598:d3e0:130::/64": "2607:f598:d3e0:130::1",
	}
	for pfx, want := range cases {
		got := externalHostOne(netip.MustParsePrefix(pfx))
		if got.String() != want {
			t.Fatalf("externalHostOne(%s) = %s, want %s", pfx, got, want)
		}
	}
}
