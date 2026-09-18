package npt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// RenderedTable contains readable rules from the ip6 nat base chains.
type RenderedTable struct {
	Prerouting  []string
	Postrouting []string
}

// HasInterface reports whether either rendered chain contains an exact
// interface match for iface.
func (table RenderedTable) HasInterface(iface string) bool {
	inputToken := fmt.Sprintf(`iif %q`, iface)
	outputToken := fmt.Sprintf(`oif %q`, iface)
	for _, line := range table.Prerouting {
		if strings.Contains(line, inputToken) {
			return true
		}
	}
	for _, line := range table.Postrouting {
		if strings.Contains(line, outputToken) {
			return true
		}
	}
	return false
}

type nftReadConn interface {
	GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error)
}

type nftReader struct {
	newConn   func() (nftReadConn, error)
	ifaceName func(index uint32) (string, bool)
}

func newNFTReader() *nftReader {
	return &nftReader{newConn: defaultNFTReadConn, ifaceName: ifaceNameByIndex}
}

// ifaceNameByIndex resolves a kernel interface index to its name, matching how
// nft renders an index-based `iif`/`oif` match. It returns false when the index
// no longer maps to an interface.
func ifaceNameByIndex(index uint32) (string, bool) {
	iface, err := net.InterfaceByIndex(int(index))
	if err != nil || iface == nil || iface.Name == "" {
		return "", false
	}
	return iface.Name, true
}

func defaultNFTReadConn() (nftReadConn, error) {
	conn, err := nftables.New()
	if err != nil {
		slog.Warn("npt: open nftables read connection failed", "err", err)
		return nil, fmt.Errorf("nftables.New: %w", err)
	}
	return conn, nil
}

// RenderTable reads the live ip6 nat table and returns readable rules for its
// prerouting and postrouting chains. A missing or empty table returns no rules.
func RenderTable(ctx context.Context, log *slog.Logger) (RenderedTable, error) {
	return newNFTReader().renderTable(ctx, log)
}

func (r *nftReader) renderTable(
	ctx context.Context,
	log *slog.Logger,
) (RenderedTable, error) {
	if err := ctx.Err(); err != nil {
		return emptyRenderedTable(), fmt.Errorf("inspect ip6 nat table: %w", err)
	}

	conn, err := r.newConn()
	if err != nil {
		log.WarnContext(ctx, "npt: open nftables read connection failed", "err", err)
		return emptyRenderedTable(), fmt.Errorf("open nftables read connection: %w", err)
	}

	table := &nftables.Table{
		Family: nftables.TableFamilyIPv6,
		Name:   natTableName,
	}
	pre := &nftables.Chain{Name: preroutingChain, Table: table}
	post := &nftables.Chain{Name: postroutingChain, Table: table}

	prerouting, missing, err := r.readChain(ctx, log, conn, table, pre)
	if err != nil {
		return emptyRenderedTable(), err
	}
	if missing {
		return emptyRenderedTable(), nil
	}
	if err := ctx.Err(); err != nil {
		return emptyRenderedTable(), fmt.Errorf("inspect ip6 nat table: %w", err)
	}
	postrouting, missing, err := r.readChain(ctx, log, conn, table, post)
	if err != nil {
		return emptyRenderedTable(), err
	}
	if missing {
		return emptyRenderedTable(), nil
	}
	return RenderedTable{
		Prerouting:  prerouting,
		Postrouting: postrouting,
	}, nil
}

func emptyRenderedTable() RenderedTable {
	return RenderedTable{
		Prerouting:  nil,
		Postrouting: nil,
	}
}

func (r *nftReader) readChain(
	ctx context.Context,
	log *slog.Logger,
	conn nftReadConn,
	table *nftables.Table,
	chain *nftables.Chain,
) ([]string, bool, error) {
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		if isNFTObjectMissing(err) {
			return nil, true, nil
		}
		log.WarnContext(ctx, "npt: read nftables chain failed", "chain", chain.Name, "err", err)
		return nil, false, fmt.Errorf("get rules for %s: %w", chain.Name, err)
	}
	return r.renderRules(rules), false, nil
}

func isNFTObjectMissing(err error) bool {
	if errors.Is(err, unix.ENOENT) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file or directory") ||
		strings.Contains(message, "file does not exist") ||
		strings.Contains(message, "enoent")
}

func (r *nftReader) renderRules(rules []*nftables.Rule) []string {
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		expressionCount := 0
		if rule != nil {
			expressionCount = len(rule.Exprs)
			if decoded, ok := decodeRule(r.ifaceName, rule.Exprs); ok {
				lines = append(lines, formatRule(decoded))
				continue
			}
		}
		lines = append(
			lines,
			fmt.Sprintf("# unrecognized rule (%d exprs)", expressionCount),
		)
	}
	return lines
}

func decodeRule(
	ifaceName func(uint32) (string, bool),
	expressions []expr.Any,
) (natRule, bool) {
	chain, iface, ok := decodeInterfaceMatch(ifaceName, expressions)
	if !ok {
		return emptyNatRule(), false
	}

	match, consumed, ok := decodeAddressMatch(expressions[2:], chain)
	if !ok {
		return emptyNatRule(), false
	}

	op, toAddr, toPrefix, ok := decodeAction(expressions[2+consumed:])
	if !ok {
		return emptyNatRule(), false
	}
	return natRule{
		Chain:  chain,
		Iface:  iface,
		Match:  match,
		Op:     op,
		ToAddr: toAddr,
		ToPfx:  toPrefix,
	}, true
}

func emptyNatRule() natRule {
	return natRule{
		Chain:  chainPostrouting,
		Iface:  "",
		Match:  netip.Prefix{},
		Op:     opGuard,
		ToAddr: netip.Addr{},
		ToPfx:  netip.Prefix{},
	}
}

// decodeInterfaceMatch recognizes either form the ip6 nat rules use to select a
// WAN. The Go applier writes an iifname/oifname string match, while nft's `iif
// "name"` from update-npt.sh compiles to an iif/oif match on the interface
// index. Both are rendered by resolving to the interface name.
func decodeInterfaceMatch(
	ifaceName func(uint32) (string, bool),
	expressions []expr.Any,
) (ruleChain, string, bool) {
	if len(expressions) < 2 {
		return chainPostrouting, "", false
	}
	meta, ok := expressions[0].(*expr.Meta)
	if !ok || meta.SourceRegister {
		return chainPostrouting, "", false
	}
	compare, ok := expressions[1].(*expr.Cmp)
	if !ok || compare.Op != expr.CmpOpEq || compare.Register != meta.Register {
		return chainPostrouting, "", false
	}

	chain, byName, ok := chainForMetaKey(meta.Key)
	if !ok {
		return chainPostrouting, "", false
	}

	var iface string
	if byName {
		iface, ok = decodeInterfaceName(compare.Data)
	} else {
		iface, ok = decodeInterfaceIndex(compare.Data, ifaceName)
	}
	if !ok {
		return chainPostrouting, "", false
	}
	return chain, iface, true
}

// chainForMetaKey maps a meta key to the chain it selects and whether the paired
// compare holds an interface name (true) or an interface index (false). The
// third return is false for any key the ip6 nat rules never use.
func chainForMetaKey(key expr.MetaKey) (ruleChain, bool, bool) {
	if key == expr.MetaKeyIIFNAME {
		return chainPrerouting, true, true
	}
	if key == expr.MetaKeyOIFNAME {
		return chainPostrouting, true, true
	}
	if key == expr.MetaKeyIIF {
		return chainPrerouting, false, true
	}
	if key == expr.MetaKeyOIF {
		return chainPostrouting, false, true
	}
	return chainPostrouting, false, false
}

func decodeInterfaceName(data []byte) (string, bool) {
	terminator := bytes.IndexByte(data, 0)
	if terminator < 1 || !allZero(data[terminator:]) {
		return "", false
	}
	return string(data[:terminator]), true
}

func decodeInterfaceIndex(
	data []byte,
	ifaceName func(uint32) (string, bool),
) (string, bool) {
	if len(data) != 4 {
		return "", false
	}
	index := binaryutil.NativeEndian.Uint32(data)
	if index == 0 {
		return "", false
	}
	if ifaceName != nil {
		if name, ok := ifaceName(index); ok {
			return name, true
		}
	}
	return fmt.Sprintf("index %d", index), true
}

func decodeAddressMatch(
	expressions []expr.Any,
	chain ruleChain,
) (netip.Prefix, int, bool) {
	if len(expressions) < 2 {
		return netip.Prefix{}, 0, false
	}
	payload, ok := expressions[0].(*expr.Payload)
	if !ok || !knownAddressPayload(payload, chain) {
		return netip.Prefix{}, 0, false
	}

	prefixBits := 128
	compareIndex := 1
	if bitwise, masked := expressions[1].(*expr.Bitwise); masked {
		var valid bool
		prefixBits, valid = decodePrefixMask(bitwise, payload.DestRegister)
		if !valid || len(expressions) < 3 {
			return netip.Prefix{}, 0, false
		}
		compareIndex = 2
	}

	compare, ok := expressions[compareIndex].(*expr.Cmp)
	if !ok || compare.Op != expr.CmpOpEq || compare.Register != payload.DestRegister {
		return netip.Prefix{}, 0, false
	}
	address, ok := decodeIPv6Address(compare.Data)
	if !ok {
		return netip.Prefix{}, 0, false
	}
	prefix := netip.PrefixFrom(address, prefixBits).Masked()
	if prefix.Addr() != address {
		return netip.Prefix{}, 0, false
	}
	return prefix, compareIndex + 1, true
}

func knownAddressPayload(payload *expr.Payload, chain ruleChain) bool {
	offset := ip6SaddrOffset
	if chain == chainPrerouting {
		offset = ip6DaddrOffset
	}
	return payload.OperationType == expr.PayloadLoad &&
		payload.Base == expr.PayloadBaseNetworkHeader &&
		payload.Offset == offset &&
		payload.Len == ip6AddrLen
}

func decodePrefixMask(bitwise *expr.Bitwise, register uint32) (int, bool) {
	if bitwise.SourceRegister != register ||
		bitwise.DestRegister != register ||
		bitwise.Len != ip6AddrLen ||
		len(bitwise.Xor) != int(ip6AddrLen) ||
		!allZero(bitwise.Xor) {
		return 0, false
	}
	ones, bits := net.IPMask(bitwise.Mask).Size()
	if bits != 128 || ones == 128 {
		return 0, false
	}
	return ones, true
}

func decodeAction(expressions []expr.Any) (natOp, netip.Addr, netip.Prefix, bool) {
	if isGuardAction(expressions) {
		return opGuard, netip.Addr{}, netip.Prefix{}, true
	}
	if len(expressions) == 2 {
		return decodeSingleNAT(expressions)
	}
	if len(expressions) == 3 {
		return decodePrefixNAT(expressions)
	}
	return opGuard, netip.Addr{}, netip.Prefix{}, false
}

func isGuardAction(expressions []expr.Any) bool {
	if len(expressions) != 4 {
		return false
	}
	ct, ok := expressions[0].(*expr.Ct)
	if !ok ||
		ct.SourceRegister ||
		ct.Key != expr.CtKeySTATUS {
		return false
	}
	bitwise, ok := expressions[1].(*expr.Bitwise)
	if !ok ||
		bitwise.SourceRegister != ct.Register ||
		bitwise.DestRegister != ct.Register ||
		bitwise.Len != ctStatusLen ||
		!bytes.Equal(bitwise.Mask, binaryutil.NativeEndian.PutUint32(ipsDstNat)) ||
		!bytes.Equal(bitwise.Xor, binaryutil.NativeEndian.PutUint32(0)) {
		return false
	}
	compare, ok := expressions[2].(*expr.Cmp)
	if !ok ||
		compare.Op != expr.CmpOpNeq ||
		compare.Register != ct.Register ||
		!bytes.Equal(compare.Data, binaryutil.NativeEndian.PutUint32(0)) {
		return false
	}
	verdict, ok := expressions[3].(*expr.Verdict)
	return ok && verdict.Kind == expr.VerdictReturn
}

func decodeSingleNAT(
	expressions []expr.Any,
) (natOp, netip.Addr, netip.Prefix, bool) {
	immediate, ok := expressions[0].(*expr.Immediate)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	nat, ok := expressions[1].(*expr.NAT)
	// A single-address NAT has no range: the applier leaves RegAddrMax zero,
	// while the kernel echoes it back equal to RegAddrMin. Accept both.
	if !ok ||
		nat.Family != unix.NFPROTO_IPV6 ||
		nat.Prefix ||
		nat.RegAddrMin != immediate.Register ||
		(nat.RegAddrMax != 0 && nat.RegAddrMax != nat.RegAddrMin) ||
		!natHasNoExtraFlags(nat) {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	op, ok := decodeNATType(nat.Type, false)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	address, ok := decodeIPv6Address(immediate.Data)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	return op, address, netip.Prefix{}, true
}

func decodePrefixNAT(
	expressions []expr.Any,
) (natOp, netip.Addr, netip.Prefix, bool) {
	minimum, ok := expressions[0].(*expr.Immediate)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	maximum, ok := expressions[1].(*expr.Immediate)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	nat, ok := expressions[2].(*expr.NAT)
	if !ok ||
		nat.Family != unix.NFPROTO_IPV6 ||
		!nat.Prefix ||
		minimum.Register == maximum.Register ||
		nat.RegAddrMin != minimum.Register ||
		nat.RegAddrMax != maximum.Register ||
		!natHasNoExtraFlags(nat) {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	op, ok := decodeNATType(nat.Type, true)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	minimumAddress, ok := decodeIPv6Address(minimum.Data)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	maximumAddress, ok := decodeIPv6Address(maximum.Data)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	prefix, ok := prefixFromRange(minimumAddress, maximumAddress)
	if !ok {
		return opGuard, netip.Addr{}, netip.Prefix{}, false
	}
	return op, netip.Addr{}, prefix, true
}

// natHasNoExtraFlags reports whether nat carries only the address translation
// the applier emits: no randomization, persistence, or port-range registers. A
// rule with any of those is not one this module writes, so the decoder treats it
// as unrecognized rather than rendering it as a plain address NAT.
func natHasNoExtraFlags(nat *expr.NAT) bool {
	return !nat.Random &&
		!nat.FullyRandom &&
		!nat.Persistent &&
		!nat.Specified &&
		nat.RegProtoMin == 0 &&
		nat.RegProtoMax == 0
}

func decodeNATType(natType expr.NATType, prefix bool) (natOp, bool) {
	switch natType {
	case expr.NATTypeSourceNAT:
		if prefix {
			return opSNATPrefix, true
		}
		return opSNAT, true
	case expr.NATTypeDestNAT:
		if prefix {
			return opDNATPrefix, true
		}
		return opDNAT, true
	default:
		return opGuard, false
	}
}

func prefixFromRange(minimum netip.Addr, maximum netip.Addr) (netip.Prefix, bool) {
	minimumOctets := minimum.As16()
	maximumOctets := maximum.As16()
	prefixBits := 0
	for i := range minimumOctets {
		difference := minimumOctets[i] ^ maximumOctets[i]
		if difference == 0 {
			prefixBits += 8
			continue
		}
		for bit := 7; bit >= 0; bit-- {
			if difference&(1<<bit) != 0 {
				break
			}
			prefixBits++
		}
		break
	}

	prefix := netip.PrefixFrom(minimum, prefixBits).Masked()
	if prefix.Addr() != minimum || lastAddr(prefix) != maximum {
		return netip.Prefix{}, false
	}
	return prefix, true
}

func decodeIPv6Address(data []byte) (netip.Addr, bool) {
	if len(data) != int(ip6AddrLen) {
		return netip.Addr{}, false
	}
	var octets [16]byte
	copy(octets[:], data)
	return netip.AddrFrom16(octets), true
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func formatRule(rule natRule) string {
	ifaceKeyword := "oif"
	addressKeyword := "saddr"
	if rule.Chain == chainPrerouting {
		ifaceKeyword = "iif"
		addressKeyword = "daddr"
	}

	match := rule.Match.String()
	if rule.Match.Bits() == 128 {
		match = rule.Match.Addr().String()
	}

	action := "ct status dnat return"
	switch rule.Op {
	case opSNAT:
		action = "snat to " + rule.ToAddr.String()
	case opDNAT:
		action = "dnat to " + rule.ToAddr.String()
	case opSNATPrefix:
		action = "snat prefix to " + rule.ToPfx.String()
	case opDNATPrefix:
		action = "dnat prefix to " + rule.ToPfx.String()
	case opGuard:
	}
	return fmt.Sprintf(
		`%s %q ip6 %s %s %s`,
		ifaceKeyword,
		rule.Iface,
		addressKeyword,
		match,
		action,
	)
}

var _ nftReadConn = (*nftables.Conn)(nil)
