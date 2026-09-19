package npt

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

type fakeReadConn struct {
	rules map[string][]*nftables.Rule
	errs  map[string]error
}

func (f *fakeReadConn) GetRules(
	_ *nftables.Table,
	chain *nftables.Chain,
) ([]*nftables.Rule, error) {
	return f.rules[chain.Name], f.errs[chain.Name]
}

func TestRenderTableRoundTripsRuleExprs(t *testing.T) {
	t.Parallel()

	iface := "enatt0.3242"
	publicPrefix := netip.MustParsePrefix("2600:1700:2f71:c80::/60")
	internalPrefix := netip.MustParsePrefix("3d06:bad:b01::/60")
	publicAddress := netip.MustParseAddr("2600:1700:2f71:c80::1")
	internalAddress := netip.MustParseAddr("3d06:bad:b01:fe::2")

	prerouting := []*nftables.Rule{
		encodedRuleForTest(t, natRule{
			Chain:  chainPrerouting,
			Iface:  iface,
			Match:  netip.PrefixFrom(publicAddress, 128),
			Op:     opDNAT,
			ToAddr: internalAddress,
		}),
		encodedRuleForTest(t, natRule{
			Chain: chainPrerouting,
			Iface: iface,
			Match: publicPrefix,
			Op:    opDNATPrefix,
			ToPfx: internalPrefix,
		}),
	}
	postrouting := []*nftables.Rule{
		encodedRuleForTest(t, natRule{
			Chain: chainPostrouting,
			Iface: iface,
			Match: netip.PrefixFrom(internalAddress, 128),
			Op:    opGuard,
		}),
		encodedRuleForTest(t, natRule{
			Chain:  chainPostrouting,
			Iface:  iface,
			Match:  netip.PrefixFrom(internalAddress, 128),
			Op:     opSNAT,
			ToAddr: publicAddress,
		}),
		encodedRuleForTest(t, natRule{
			Chain: chainPostrouting,
			Iface: iface,
			Match: internalPrefix,
			Op:    opSNATPrefix,
			ToPfx: publicPrefix,
		}),
	}

	fake := &fakeReadConn{
		rules: map[string][]*nftables.Rule{
			preroutingChain:  prerouting,
			postroutingChain: postrouting,
		},
		errs: nil,
	}
	reader := &nftReader{newConn: func() (nftReadConn, error) {
		return fake, nil
	}}
	got, err := reader.renderTable(context.Background(), discardLogger())
	if err != nil {
		t.Fatalf("renderTable returned error: %v", err)
	}

	want := RenderedTable{
		Prerouting: []string{
			`iif "enatt0.3242" ip6 daddr 2600:1700:2f71:c80::1 dnat to 3d06:bad:b01:fe::2`,
			`iif "enatt0.3242" ip6 daddr 2600:1700:2f71:c80::/60 dnat prefix to 3d06:bad:b01::/60`,
		},
		Postrouting: []string{
			`oif "enatt0.3242" ip6 saddr 3d06:bad:b01:fe::2 ct status dnat return`,
			`oif "enatt0.3242" ip6 saddr 3d06:bad:b01:fe::2 snat to 2600:1700:2f71:c80::1`,
			`oif "enatt0.3242" ip6 saddr 3d06:bad:b01::/60 snat prefix to 2600:1700:2f71:c80::/60`,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("renderTable mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestRenderedTableHasInterface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		table RenderedTable
		iface string
		want  bool
	}{
		{
			name: "prerouting exact match",
			table: RenderedTable{
				Prerouting: []string{
					`iif "enatt0" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
			},
			iface: "enatt0",
			want:  true,
		},
		{
			name: "postrouting exact match",
			table: RenderedTable{
				Postrouting: []string{
					`oif "enatt0" ip6 saddr fd00::/64 snat prefix to 2001:db8::/64`,
				},
			},
			iface: "enatt0",
			want:  true,
		},
		{
			name: "absent interface",
			table: RenderedTable{
				Prerouting: []string{
					`iif "enatt0" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
			},
			iface: "enwebpass0",
			want:  false,
		},
		{
			name: "shorter substring collision",
			table: RenderedTable{
				Prerouting: []string{
					`iif "enatt0.3242" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
			},
			iface: "enatt0",
			want:  false,
		},
		{
			name: "longer substring collision",
			table: RenderedTable{
				Postrouting: []string{
					`oif "enatt0" ip6 saddr fd00::/64 snat prefix to 2001:db8::/64`,
				},
			},
			iface: "enatt0.3242",
			want:  false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := testCase.table.HasInterface(testCase.iface)
			if got != testCase.want {
				t.Fatalf(
					"HasInterface(%q) = %v, want %v",
					testCase.iface,
					got,
					testCase.want,
				)
			}
		})
	}
}

func TestRenderTableFallsBackForUnrecognizedRule(t *testing.T) {
	t.Parallel()

	fake := &fakeReadConn{
		rules: map[string][]*nftables.Rule{
			preroutingChain: {
				{Exprs: []expr.Any{&expr.Counter{}}},
			},
		},
		errs: nil,
	}
	reader := &nftReader{newConn: func() (nftReadConn, error) {
		return fake, nil
	}}
	got, err := reader.renderTable(context.Background(), discardLogger())
	if err != nil {
		t.Fatalf("renderTable returned error: %v", err)
	}
	want := []string{"# unrecognized rule (1 exprs)"}
	if !reflect.DeepEqual(got.Prerouting, want) {
		t.Fatalf("prerouting = %v, want %v", got.Prerouting, want)
	}
}

func TestRenderTableTreatsMissingTableAsEmpty(t *testing.T) {
	t.Parallel()

	fake := &fakeReadConn{
		rules: nil,
		errs: map[string]error{
			preroutingChain: fmt.Errorf("receiveAckAware: %w", unix.ENOENT),
		},
	}
	reader := &nftReader{newConn: func() (nftReadConn, error) {
		return fake, nil
	}}
	got, err := reader.renderTable(context.Background(), discardLogger())
	if err != nil {
		t.Fatalf("renderTable returned error: %v", err)
	}
	if !reflect.DeepEqual(got, emptyRenderedTable()) {
		t.Fatalf("renderTable = %#v, want empty table", got)
	}
}

func TestRenderTableRejectsRandomizedNAT(t *testing.T) {
	t.Parallel()

	rule := encodedRuleForTest(t, natRule{
		Chain:  chainPostrouting,
		Iface:  "enatt0",
		Match:  netip.MustParsePrefix("3d06:bad:b01:fe::2/128"),
		Op:     opSNAT,
		ToAddr: netip.MustParseAddr("2600:1700:2f71:c80::1"),
	})
	for _, expression := range rule.Exprs {
		if nat, ok := expression.(*expr.NAT); ok {
			nat.Random = true
		}
	}

	got := renderChainForTest(t, postroutingChain, rule)
	want := []string{fmt.Sprintf("# unrecognized rule (%d exprs)", len(rule.Exprs))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("render = %v, want %v", got, want)
	}
}

func TestRenderTableRejectsEqualPrefixRegisters(t *testing.T) {
	t.Parallel()

	rule := encodedRuleForTest(t, natRule{
		Chain: chainPostrouting,
		Iface: "enatt0",
		Match: netip.MustParsePrefix("3d06:bad:b01::/60"),
		Op:    opSNATPrefix,
		ToPfx: netip.MustParsePrefix("2600:1700:2f71:c80::/60"),
	})
	// Collapse both NETMAP range registers onto register 1, the runtime shape
	// where the second Immediate overwrites the first.
	for _, expression := range rule.Exprs {
		switch typed := expression.(type) {
		case *expr.Immediate:
			typed.Register = 1
		case *expr.NAT:
			typed.RegAddrMin = 1
			typed.RegAddrMax = 1
		}
	}

	got := renderChainForTest(t, postroutingChain, rule)
	want := []string{fmt.Sprintf("# unrecognized rule (%d exprs)", len(rule.Exprs))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("render = %v, want %v", got, want)
	}
}

// TestRenderTableDecodesKernelForm exercises the rule shapes the live table
// actually holds when update-npt.sh writes it via nft: interface matched by
// index (meta iif/oif, not iifname/oifname), single NAT with RegAddrMax equal to
// RegAddrMin, and the guard masking IPS_DST_NAT (0x20). The applier round-trip
// test cannot catch these because the kernel normalizes what it echoes back.
func TestRenderTableDecodesKernelForm(t *testing.T) {
	t.Parallel()

	const ifindex uint32 = 4
	iface := "enatt0"
	edge := netip.MustParseAddr("3d06:bad:b01:201::2")
	pd1 := netip.MustParseAddr("3d06:bad:b01:2300::1")
	internal := netip.MustParsePrefix("3d06:bad:b01:210::/60")
	pd := netip.MustParsePrefix("3d06:bad:b01:2300::/60")

	fake := &fakeReadConn{
		rules: map[string][]*nftables.Rule{
			preroutingChain: {
				{Exprs: kernelSingleNATExprs(chainPrerouting, ifindex, netip.PrefixFrom(pd1, 128), expr.NATTypeDestNAT, edge)},
				{Exprs: kernelPrefixNATExprs(chainPrerouting, ifindex, pd, expr.NATTypeDestNAT, internal)},
			},
			postroutingChain: {
				{Exprs: kernelGuardExprs(ifindex, netip.PrefixFrom(edge, 128))},
				{Exprs: kernelSingleNATExprs(chainPostrouting, ifindex, netip.PrefixFrom(edge, 128), expr.NATTypeSourceNAT, pd1)},
				{Exprs: kernelPrefixNATExprs(chainPostrouting, ifindex, internal, expr.NATTypeSourceNAT, pd)},
			},
		},
		errs: nil,
	}
	reader := &nftReader{
		newConn: func() (nftReadConn, error) { return fake, nil },
		ifaceName: func(index uint32) (string, bool) {
			if index == ifindex {
				return iface, true
			}
			return "", false
		},
	}

	got, err := reader.renderTable(context.Background(), discardLogger())
	if err != nil {
		t.Fatalf("renderTable returned error: %v", err)
	}
	want := RenderedTable{
		Prerouting: []string{
			`iif "enatt0" ip6 daddr 3d06:bad:b01:2300::1 dnat to 3d06:bad:b01:201::2`,
			`iif "enatt0" ip6 daddr 3d06:bad:b01:2300::/60 dnat prefix to 3d06:bad:b01:210::/60`,
		},
		Postrouting: []string{
			`oif "enatt0" ip6 saddr 3d06:bad:b01:201::2 ct status dnat return`,
			`oif "enatt0" ip6 saddr 3d06:bad:b01:201::2 snat to 3d06:bad:b01:2300::1`,
			`oif "enatt0" ip6 saddr 3d06:bad:b01:210::/60 snat prefix to 3d06:bad:b01:2300::/60`,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("renderTable mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestDecodeInterfaceIndexFallsBackToNumber checks that an index with no current
// interface still renders, using the numeric index.
func TestDecodeInterfaceIndexFallsBackToNumber(t *testing.T) {
	t.Parallel()

	fake := &fakeReadConn{
		rules: map[string][]*nftables.Rule{
			postroutingChain: {
				{Exprs: kernelSingleNATExprs(
					chainPostrouting, 9,
					netip.MustParsePrefix("3d06:bad:b01:201::2/128"),
					expr.NATTypeSourceNAT,
					netip.MustParseAddr("3d06:bad:b01:2300::1"),
				)},
			},
		},
		errs: nil,
	}
	reader := &nftReader{
		newConn:   func() (nftReadConn, error) { return fake, nil },
		ifaceName: func(uint32) (string, bool) { return "", false },
	}
	got, err := reader.renderTable(context.Background(), discardLogger())
	if err != nil {
		t.Fatalf("renderTable returned error: %v", err)
	}
	want := []string{`oif "index 9" ip6 saddr 3d06:bad:b01:201::2 snat to 3d06:bad:b01:2300::1`}
	if !reflect.DeepEqual(got.Postrouting, want) {
		t.Fatalf("postrouting = %v, want %v", got.Postrouting, want)
	}
}

func kernelIfaceMatchExprs(chain ruleChain, ifindex uint32) []expr.Any {
	key := expr.MetaKeyOIF
	if chain == chainPrerouting {
		key = expr.MetaKeyIIF
	}
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(ifindex)},
	}
}

func kernelAddrMatchExprs(chain ruleChain, match netip.Prefix) []expr.Any {
	offset := ip6SaddrOffset
	if chain == chainPrerouting {
		offset = ip6DaddrOffset
	}
	return addrMatchExprs(match, offset)
}

func kernelSingleNATExprs(
	chain ruleChain,
	ifindex uint32,
	match netip.Prefix,
	natType expr.NATType,
	to netip.Addr,
) []expr.Any {
	exprs := kernelIfaceMatchExprs(chain, ifindex)
	exprs = append(exprs, kernelAddrMatchExprs(chain, match)...)
	exprs = append(
		exprs,
		&expr.Immediate{Register: 1, Data: addr16(to)},
		// The kernel echoes RegAddrMax equal to RegAddrMin for a single address.
		&expr.NAT{Type: natType, Family: unix.NFPROTO_IPV6, RegAddrMin: 1, RegAddrMax: 1},
	)
	return exprs
}

func kernelPrefixNATExprs(
	chain ruleChain,
	ifindex uint32,
	match netip.Prefix,
	natType expr.NATType,
	to netip.Prefix,
) []expr.Any {
	to = to.Masked()
	exprs := kernelIfaceMatchExprs(chain, ifindex)
	exprs = append(exprs, kernelAddrMatchExprs(chain, match)...)
	exprs = append(
		exprs,
		&expr.Immediate{Register: 1, Data: addr16(to.Addr())},
		&expr.Immediate{Register: 2, Data: addr16(lastAddr(to))},
		&expr.NAT{Type: natType, Family: unix.NFPROTO_IPV6, RegAddrMin: 1, RegAddrMax: 2, Prefix: true},
	)
	return exprs
}

func kernelGuardExprs(ifindex uint32, match netip.Prefix) []expr.Any {
	exprs := kernelIfaceMatchExprs(chainPostrouting, ifindex)
	exprs = append(exprs, kernelAddrMatchExprs(chainPostrouting, match)...)
	return append(exprs, guardExprs()...)
}

func renderChainForTest(t *testing.T, chainName string, rules ...*nftables.Rule) []string {
	t.Helper()

	fake := &fakeReadConn{
		rules: map[string][]*nftables.Rule{chainName: rules},
		errs:  nil,
	}
	reader := &nftReader{newConn: func() (nftReadConn, error) {
		return fake, nil
	}}
	got, err := reader.renderTable(context.Background(), discardLogger())
	if err != nil {
		t.Fatalf("renderTable returned error: %v", err)
	}
	if chainName == preroutingChain {
		return got.Prerouting
	}
	return got.Postrouting
}

func encodedRuleForTest(t *testing.T, rule natRule) *nftables.Rule {
	t.Helper()

	expressions, err := ruleExprs(rule)
	if err != nil {
		t.Fatalf("ruleExprs(%s) returned error: %v", rule, err)
	}
	return &nftables.Rule{Exprs: expressions}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRenderIntended pins the text form of the intended ruleset the
// management surface serves: one heading per chain, rules in program
// order, in the same wording the inspector renders live rules.
func TestRenderIntended(t *testing.T) {
	t.Parallel()
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
	want := "chain prerouting:\n" +
		"  iif \"enatt0\" ip6 daddr 2001:db8:a::/60 dnat prefix to 3d06:bad:b01::/60\n" +
		"chain postrouting:\n" +
		"  oif \"enatt0\" ip6 saddr 3d06:bad:b01::/60 snat prefix to 2001:db8:a::/60\n"
	if got := renderIntended(desired); got != want {
		t.Fatalf("renderIntended =\n%s\nwant:\n%s", got, want)
	}
}

var _ nftReadConn = (*fakeReadConn)(nil)
