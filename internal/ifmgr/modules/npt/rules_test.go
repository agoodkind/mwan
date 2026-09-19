package npt

import (
	"net/netip"
	"reflect"
	"testing"
)

// wanInputForTest mirrors the production NPT path in update-npt.sh:180-217 for a
// single WAN, so the expected rule slice below can be checked cell by cell.
func wanInputForTest() wanRuleInput {
	return wanRuleInput{
		Iface:        "enatt0.3242",
		PD60:         netip.MustParsePrefix("2600:1700:2f71:c80::/60"),
		Internal:     netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdge: netip.MustParseAddr("3d06:bad:b01:201::1"),
		MwanbrEdge:   netip.MustParseAddr("3d06:bad:b01:200::1"),
		ExtraDNAT:    []netip.Addr{netip.MustParseAddr("2600:1700:2f71:c85::abcd")},
	}
}

// TestBuildWANRulesMatchesShell asserts the ordered typed rule set for one WAN
// reproduces the NPT branch of update-npt.sh exactly, including guard-first
// order, the NETMAP vs single-/128 split, the <pd>::1 derivation, and one DNAT
// per extra global /128 on the iface.
func TestBuildWANRulesMatchesShell(t *testing.T) {
	t.Parallel()

	iface := "enatt0.3242"
	pd1 := netip.MustParseAddr("2600:1700:2f71:c80::1")
	edge := netip.MustParseAddr("3d06:bad:b01:201::1")
	mwanbr := netip.MustParseAddr("3d06:bad:b01:200::1")
	pd60 := netip.MustParsePrefix("2600:1700:2f71:c80::/60")
	internal := netip.MustParsePrefix("3d06:bad:b01::/60")
	extra := netip.MustParseAddr("2600:1700:2f71:c85::abcd")

	want := []natRule{
		// postrouting, in order (guard MUST be first).
		{Chain: chainPostrouting, Iface: iface, Match: netip.PrefixFrom(edge, 128), Op: opGuard},
		{Chain: chainPostrouting, Iface: iface, Match: netip.PrefixFrom(edge, 128), Op: opSNAT, ToAddr: pd1},
		{Chain: chainPostrouting, Iface: iface, Match: netip.PrefixFrom(mwanbr, 128), Op: opSNAT, ToAddr: pd1},
		{Chain: chainPostrouting, Iface: iface, Match: internal, Op: opSNATPrefix, ToPfx: pd60},
		// prerouting.
		{Chain: chainPrerouting, Iface: iface, Match: netip.PrefixFrom(pd1, 128), Op: opDNAT, ToAddr: edge},
		{Chain: chainPrerouting, Iface: iface, Match: pd60, Op: opDNATPrefix, ToPfx: internal},
		{Chain: chainPrerouting, Iface: iface, Match: netip.PrefixFrom(extra, 128), Op: opDNAT, ToAddr: edge},
	}

	got := buildWANRules(wanInputForTest())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildWANRules mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestBuildWANRulesNoExtra checks a WAN with no extra global /128s yields exactly
// the four postrouting and two prerouting rules, no trailing DNAT.
func TestBuildWANRulesNoExtra(t *testing.T) {
	t.Parallel()

	in := wanInputForTest()
	in.ExtraDNAT = nil
	got := buildWANRules(in)
	if len(got) != 6 {
		t.Fatalf("rule count = %d, want 6 (no extra /128)", len(got))
	}
	if got[len(got)-1].Op != opDNATPrefix {
		t.Fatalf("last rule op = %v, want opDNATPrefix", got[len(got)-1].Op)
	}
}

// TestPDHostOne pins the <pd>::1 derivation: the /60 network address with host
// ::1, matching ${TARGET_PREFIX%/*}1 in the shell.
func TestPDHostOne(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"2600:1700:2f71:c80::/60": "2600:1700:2f71:c80::1",
		"2607:f598:d3e0:130::/60": "2607:f598:d3e0:130::1",
	}
	for pfx, want := range cases {
		got := pdHostOne(netip.MustParsePrefix(pfx))
		if got.String() != want {
			t.Fatalf("pdHostOne(%s) = %s, want %s", pfx, got, want)
		}
	}
}
