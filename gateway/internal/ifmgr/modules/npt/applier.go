package npt

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	natTableName     = "nat"
	postroutingChain = "postrouting"
	preroutingChain  = "prerouting"

	// ipsDstNat is IPS_DST_NAT (bit 5) from linux/netfilter/nf_conntrack_common.h:
	// the conntrack status bit set once a flow has been destination-NATed. The
	// guard rule matches it so a hairpinned DNAT reply is not re-SNATed. This is
	// the value nft's `ct status dnat` compiles to, matching update-npt.sh.
	ipsDstNat uint32 = 0x20

	// IPv6 header field offsets and length, relative to the network header.
	ip6SaddrOffset uint32 = 8
	ip6DaddrOffset uint32 = 24
	ip6AddrLen     uint32 = 16

	ctStatusLen uint32 = 4
)

// nftConn is the subset of *nftables.Conn the applier drives. Injecting it lets
// tests capture the batch (rules added, chain flushes, and the single Flush)
// without opening a kernel netlink socket.
type nftConn interface {
	ListTablesOfFamily(family nftables.TableFamily) ([]*nftables.Table, error)
	ListChainsOfTableFamily(family nftables.TableFamily) ([]*nftables.Chain, error)
	AddTable(t *nftables.Table) *nftables.Table
	AddChain(c *nftables.Chain) *nftables.Chain
	FlushChain(c *nftables.Chain)
	AddRule(r *nftables.Rule) *nftables.Rule
	Flush() error
}

// applier commits a desired ip6 nat rule set. The module depends on this
// interface so its tests can substitute a fake that records the desired set.
type applier interface {
	Apply(ctx context.Context, log *slog.Logger, desired desiredRules) error
}

// nftApplier translates a desired rule set into google/nftables operations and
// replaces both chains in one atomic transaction.
type nftApplier struct {
	newConn func() (nftConn, error)
}

type nftRuleRemover interface {
	GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error)
	DelRule(rule *nftables.Rule) error
	Flush() error
}

func removeReverseDNAT(ctx context.Context, log *slog.Logger, iface string, local []netip.Addr) error {
	conn, err := nftables.New()
	if err != nil {
		log.ErrorContext(ctx, "npt: open nftables connection for DNAT preparation failed", "iface", iface, "err", err)
		return fmt.Errorf("open nftables netlink connection: %w", err)
	}
	return removeReverseDNATRules(ctx, log, conn, iface, local)
}

func removeReverseDNATRules(ctx context.Context, log *slog.Logger, conn nftRuleRemover, iface string, local []netip.Addr) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("remove reverse DNAT: %w", err)
	}
	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
	chain := &nftables.Chain{Name: preroutingChain, Table: table}
	rules, err := conn.GetRules(table, chain)
	if err != nil && isNFTObjectMissing(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read NPT prerouting rules: %w", err)
	}
	pending := make(map[netip.Addr]bool, len(local))
	for _, address := range local {
		pending[address] = true
	}
	removed := false
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		decoded, ok := decodeRule(ifaceNameByIndex, rule.Exprs)
		if !ok || decoded.Chain != chainPrerouting || decoded.Iface != iface || decoded.Op != opDNAT || decoded.Match.Bits() != 128 || !pending[decoded.Match.Addr()] {
			continue
		}
		if err := conn.DelRule(rule); err != nil {
			return fmt.Errorf("delete reverse DNAT for %s on %s: %w", decoded.Match.Addr(), iface, err)
		}
		removed = true
	}
	if !removed {
		return nil
	}
	if err := conn.Flush(); err != nil {
		log.WarnContext(ctx, "npt: remove reverse DNAT failed", "iface", iface, "err", err)
		return fmt.Errorf("commit reverse DNAT removal: %w", err)
	}
	return nil
}

// newNFTApplier returns the production applier backed by a real netlink
// connection opened per Apply call.
func newNFTApplier() *nftApplier {
	return &nftApplier{newConn: defaultNFTConn}
}

func defaultNFTConn() (nftConn, error) {
	conn, err := nftables.New()
	if err != nil {
		slog.Warn("npt: open nftables netlink connection failed", "err", err)
		return nil, fmt.Errorf("nftables.New: %w", err)
	}
	return conn, nil
}

// Apply replaces the full contents of the postrouting and prerouting chains
// with desired. Both chains are flushed and refilled inside one netlink batch,
// committed by a single Conn.Flush, so nftables applies the swap atomically and
// no packet ever sees an empty chain. This is the traffic-continuity guarantee.
func (a *nftApplier) Apply(ctx context.Context, log *slog.Logger, desired desiredRules) error {
	conn, err := a.newConn()
	if err != nil {
		return err
	}

	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
	accept := nftables.ChainPolicyAccept
	post := &nftables.Chain{
		Name: postroutingChain, Table: table,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource,
		Type: nftables.ChainTypeNAT, Policy: &accept,
	}
	pre := &nftables.Chain{
		Name: preroutingChain, Table: table,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
		Type: nftables.ChainTypeNAT, Policy: &accept,
	}
	if err := ensureNATStructure(ctx, log, conn, table, pre, post); err != nil {
		return err
	}

	conn.FlushChain(post)
	for _, rule := range desired.Postrouting {
		exprs, buildErr := ruleExprs(rule)
		if buildErr != nil {
			return fmt.Errorf("build postrouting rule %s: %w", rule, buildErr)
		}
		conn.AddRule(&nftables.Rule{
			Table: table, Chain: post, Position: 0, Handle: 0, Flags: 0, Exprs: exprs, UserData: nil,
		})
	}

	conn.FlushChain(pre)
	for _, rule := range desired.Prerouting {
		exprs, buildErr := ruleExprs(rule)
		if buildErr != nil {
			return fmt.Errorf("build prerouting rule %s: %w", rule, buildErr)
		}
		conn.AddRule(&nftables.Rule{
			Table: table, Chain: pre, Position: 0, Handle: 0, Flags: 0, Exprs: exprs, UserData: nil,
		})
	}

	if err := conn.Flush(); err != nil {
		log.WarnContext(ctx, "npt: nft flush failed", "err", err)
		return fmt.Errorf("nft flush: %w", err)
	}
	return nil
}

func ensureNATStructure(ctx context.Context, log *slog.Logger, conn nftConn, table *nftables.Table, chains ...*nftables.Chain) error {
	tables, err := conn.ListTablesOfFamily(table.Family)
	if err != nil {
		log.WarnContext(ctx, "npt: list IPv6 tables failed", "err", err)
		return fmt.Errorf("list IPv6 tables: %w", err)
	}
	tableExists := false
	for _, current := range tables {
		if current.Name == table.Name {
			tableExists = true
			break
		}
	}
	if !tableExists {
		conn.AddTable(table)
	}

	existing, err := conn.ListChainsOfTableFamily(table.Family)
	if err != nil {
		log.WarnContext(ctx, "npt: list IPv6 chains failed", "err", err)
		return fmt.Errorf("list IPv6 chains: %w", err)
	}
	for _, wanted := range chains {
		found := false
		for _, current := range existing {
			if current.Table == nil || current.Table.Name != table.Name || current.Name != wanted.Name {
				continue
			}
			found = true
			if !sameNATChain(current, wanted) {
				return fmt.Errorf("ip6 nat chain %s has incompatible type, hook, priority, or policy", wanted.Name)
			}
			break
		}
		if !found {
			conn.AddChain(wanted)
		}
	}
	return nil
}

func sameNATChain(current, wanted *nftables.Chain) bool {
	return current.Type == wanted.Type &&
		current.Hooknum != nil && wanted.Hooknum != nil && *current.Hooknum == *wanted.Hooknum &&
		current.Priority != nil && wanted.Priority != nil && *current.Priority == *wanted.Priority &&
		current.Policy != nil && wanted.Policy != nil && *current.Policy == *wanted.Policy
}

// ruleExprs translates one typed natRule into its ordered nftables expressions:
// the interface match, the address match, then the NAT action.
func ruleExprs(rule natRule) ([]expr.Any, error) {
	exprs := make([]expr.Any, 0, 8)
	exprs = append(exprs, ifaceMatchExprs(rule)...)

	offset := ip6SaddrOffset
	if rule.Chain == chainPrerouting {
		offset = ip6DaddrOffset
	}
	exprs = append(exprs, addrMatchExprs(rule.Match, offset)...)

	action, err := actionExprs(rule)
	if err != nil {
		return nil, err
	}
	return append(exprs, action...), nil
}

func ifaceMatchExprs(rule natRule) []expr.Any {
	key := expr.MetaKeyOIFNAME
	if rule.Chain == chainPrerouting {
		key = expr.MetaKeyIIFNAME
	}
	return []expr.Any{
		&expr.Meta{Key: key, SourceRegister: false, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(rule.Iface)},
	}
}

// addrMatchExprs matches ip6 saddr/daddr against p. A /128 is an exact compare;
// a shorter prefix masks the loaded address before comparing to the network.
func addrMatchExprs(p netip.Prefix, offset uint32) []expr.Any {
	p = p.Masked()
	load := &expr.Payload{
		OperationType: expr.PayloadLoad, DestRegister: 1, SourceRegister: 0,
		Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: ip6AddrLen,
		CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
	}
	if p.Bits() == 128 {
		return []expr.Any{
			load,
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr16(p.Addr())},
		}
	}
	mask := net.CIDRMask(p.Bits(), 128)
	return []expr.Any{
		load,
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: ip6AddrLen, Mask: mask, Xor: make([]byte, ip6AddrLen)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr16(p.Addr())},
	}
}

func actionExprs(rule natRule) ([]expr.Any, error) {
	switch rule.Op {
	case opGuard:
		return guardExprs(), nil
	case opSNAT:
		return singleNATExprs(expr.NATTypeSourceNAT, rule.ToAddr), nil
	case opDNAT:
		return singleNATExprs(expr.NATTypeDestNAT, rule.ToAddr), nil
	case opSNATPrefix:
		return prefixNATExprs(expr.NATTypeSourceNAT, rule.ToPfx), nil
	case opDNATPrefix:
		return prefixNATExprs(expr.NATTypeDestNAT, rule.ToPfx), nil
	}
	return nil, fmt.Errorf("npt: unknown nat op %s", rule.Op)
}

// guardExprs matches conntrack status dnat and returns, applying no NAT.
func guardExprs() []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, SourceRegister: false, Key: expr.CtKeySTATUS, Direction: 0},
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: ctStatusLen,
			Mask: binaryutil.NativeEndian.PutUint32(ipsDstNat),
			Xor:  binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Verdict{Kind: expr.VerdictReturn, Chain: ""},
	}
}

// singleNATExprs rewrites to a single /128 address: load the target, then a NAT
// with only the min register and no NETMAP flag.
func singleNATExprs(natType expr.NATType, addr netip.Addr) []expr.Any {
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: addr16(addr)},
		&expr.NAT{
			Type: natType, Family: unix.NFPROTO_IPV6,
			RegAddrMin: 1, RegAddrMax: 0, RegProtoMin: 0, RegProtoMax: 0,
			Random: false, FullyRandom: false, Persistent: false, Prefix: false, Specified: false,
		},
	}
}

// prefixNATExprs NETMAP-translates onto pfx: load the range min (network) and
// max (last address) into two registers, then a NAT with the Prefix flag set.
func prefixNATExprs(natType expr.NATType, pfx netip.Prefix) []expr.Any {
	pfx = pfx.Masked()
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: addr16(pfx.Addr())},
		&expr.Immediate{Register: 2, Data: addr16(lastAddr(pfx))},
		&expr.NAT{
			Type: natType, Family: unix.NFPROTO_IPV6,
			RegAddrMin: 1, RegAddrMax: 2, RegProtoMin: 0, RegProtoMax: 0,
			Random: false, FullyRandom: false, Persistent: false, Prefix: true, Specified: false,
		},
	}
}

// lastAddr returns the highest address inside pfx: the network address with all
// host bits set. It is the NETMAP range maximum.
func lastAddr(pfx netip.Prefix) netip.Addr {
	octets := pfx.Masked().Addr().As16()
	mask := net.CIDRMask(pfx.Bits(), 128)
	for i := range octets {
		octets[i] |= ^mask[i]
	}
	return netip.AddrFrom16(octets)
}

// addr16 returns the 16-byte big-endian form of a, matching the on-wire IPv6
// address layout the payload and NAT registers compare against.
func addr16(a netip.Addr) []byte {
	octets := a.As16()
	return octets[:]
}

// ifname returns the NUL-terminated interface name used for oif/iif compares.
func ifname(name string) []byte {
	return []byte(name + "\x00")
}
