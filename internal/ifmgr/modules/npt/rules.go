package npt

import (
	"fmt"
	"net/netip"
	"strings"
)

// natOp is the NAT primitive one desired rule applies. It stays independent of
// nftables encoding so the builder is unit-testable against the shell's exact
// rule set; the applier translates each op into google/nftables expressions.
type natOp int

const (
	// opGuard matches conntrack status dnat and returns, applying no address
	// translation. It protects a hairpinned DNAT flow from being re-SNATed.
	opGuard natOp = iota
	// opSNAT rewrites the source address to a single /128 (ToAddr).
	opSNAT
	// opDNAT rewrites the destination address to a single /128 (ToAddr).
	opDNAT
	// opSNATPrefix NETMAP-translates the matched source prefix onto ToPfx.
	opSNATPrefix
	// opDNATPrefix NETMAP-translates the matched destination prefix onto ToPfx.
	opDNATPrefix
)

func (o natOp) String() string {
	switch o {
	case opGuard:
		return "guard"
	case opSNAT:
		return "snat"
	case opDNAT:
		return "dnat"
	case opSNATPrefix:
		return "snat-prefix"
	case opDNATPrefix:
		return "dnat-prefix"
	}
	return fmt.Sprintf("op(%d)", int(o))
}

// ruleChain names the ip6 nat base chain a rule belongs to. Postrouting rules
// match ip6 saddr and key on oif; prerouting rules match ip6 daddr and key on
// iif.
type ruleChain int

const (
	chainPostrouting ruleChain = iota
	chainPrerouting
)

func (c ruleChain) String() string {
	if c == chainPrerouting {
		return "prerouting"
	}
	return "postrouting"
}

// natRule is one desired ip6 nat rule in typed form, independent of the
// nftables wire encoding. Match is the ip6 saddr prefix (postrouting) or ip6
// daddr prefix (prerouting); a single-address match is a /128. ToAddr carries
// the target for opSNAT/opDNAT; ToPfx carries the target for the NETMAP ops.
type natRule struct {
	Chain  ruleChain
	Iface  string
	Match  netip.Prefix
	Op     natOp
	ToAddr netip.Addr
	ToPfx  netip.Prefix
}

func (r natRule) String() string {
	switch r.Op {
	case opGuard:
		return fmt.Sprintf("%s %s match=%s guard(ct dnat return)", r.Chain, r.Iface, r.Match)
	case opSNAT, opDNAT:
		return fmt.Sprintf("%s %s match=%s %s to %s", r.Chain, r.Iface, r.Match, r.Op, r.ToAddr)
	case opSNATPrefix, opDNATPrefix:
		return fmt.Sprintf("%s %s match=%s %s to %s", r.Chain, r.Iface, r.Match, r.Op, r.ToPfx)
	}
	return fmt.Sprintf("%s %s match=%s %s", r.Chain, r.Iface, r.Match, r.Op)
}

// wanRuleInput is everything the builder needs to compute one WAN's NPT rules.
// PD60 is the live delegated /60, Internal is the shared internal /60, and
// ExtraDNAT is the set of extra global /128s on the iface (excluding <pd>::1),
// each of which gets a reverse DNAT to the OPNsense edge.
type wanRuleInput struct {
	Iface        string
	PD60         netip.Prefix
	Internal     netip.Prefix
	OpnsenseEdge netip.Addr
	MwanbrEdge   netip.Addr
	ExtraDNAT    []netip.Addr
}

// pdHostOne returns the <pd>::1 host address: the /60 network address with the
// low bit set, matching ${TARGET_PREFIX%/*}1 in update-npt.sh.
func pdHostOne(pd60 netip.Prefix) netip.Addr {
	octets := pd60.Masked().Addr().As16()
	octets[15] = 1
	return netip.AddrFrom16(octets)
}

// buildWANRules returns the ordered typed rule set for one WAN, reproducing the
// NPT branch of update-npt.sh:180-217. The order is load-bearing: the ct-status
// guard MUST precede the edge SNAT so a hairpinned DNAT reply is not re-SNATed.
func buildWANRules(in wanRuleInput) []natRule {
	pd60 := in.PD60.Masked()
	internal := in.Internal.Masked()
	pd1 := pdHostOne(pd60)
	edge128 := netip.PrefixFrom(in.OpnsenseEdge, 128)
	mwanbr128 := netip.PrefixFrom(in.MwanbrEdge, 128)
	pd1128 := netip.PrefixFrom(pd1, 128)

	noAddr := netip.Addr{}
	noPfx := netip.Prefix{}

	rules := make([]natRule, 0, 6+len(in.ExtraDNAT))
	rules = append(
		rules,
		natRule{Chain: chainPostrouting, Iface: in.Iface, Match: edge128, Op: opGuard, ToAddr: noAddr, ToPfx: noPfx},
		natRule{Chain: chainPostrouting, Iface: in.Iface, Match: edge128, Op: opSNAT, ToAddr: pd1, ToPfx: noPfx},
		natRule{Chain: chainPostrouting, Iface: in.Iface, Match: mwanbr128, Op: opSNAT, ToAddr: pd1, ToPfx: noPfx},
		natRule{Chain: chainPostrouting, Iface: in.Iface, Match: internal, Op: opSNATPrefix, ToAddr: noAddr, ToPfx: pd60},
		natRule{Chain: chainPrerouting, Iface: in.Iface, Match: pd1128, Op: opDNAT, ToAddr: in.OpnsenseEdge, ToPfx: noPfx},
		natRule{Chain: chainPrerouting, Iface: in.Iface, Match: pd60, Op: opDNATPrefix, ToAddr: noAddr, ToPfx: internal},
	)
	for _, addr := range in.ExtraDNAT {
		rules = append(rules, natRule{
			Chain:  chainPrerouting,
			Iface:  in.Iface,
			Match:  netip.PrefixFrom(addr, 128),
			Op:     opDNAT,
			ToAddr: in.OpnsenseEdge,
			ToPfx:  noPfx,
		})
	}
	return rules
}

// desiredRules is the full contents of the two ip6 nat chains for one reconcile:
// the union of every successful WAN's rules, in WAN order. The applier replaces
// each chain's whole contents with these in one atomic transaction.
type desiredRules struct {
	Postrouting []natRule
	Prerouting  []natRule
}

// renderIntended renders one reconcile's desired rule set as text, one
// chain heading per chain with the rules in program order, for the
// management surface's intended-ruleset leaf.
func renderIntended(desired desiredRules) string {
	var rendered strings.Builder
	rendered.WriteString("chain prerouting:\n")
	for _, rule := range desired.Prerouting {
		rendered.WriteString("  " + formatRule(rule) + "\n")
	}
	rendered.WriteString("chain postrouting:\n")
	for _, rule := range desired.Postrouting {
		rendered.WriteString("  " + formatRule(rule) + "\n")
	}
	return rendered.String()
}

// add appends one WAN's rules into the per-chain desired contents, preserving
// per-WAN order (guard first within each WAN's postrouting rules).
func (d *desiredRules) add(rules []natRule) {
	for _, rule := range rules {
		if rule.Chain == chainPrerouting {
			d.Prerouting = append(d.Prerouting, rule)
			continue
		}
		d.Postrouting = append(d.Postrouting, rule)
	}
}
