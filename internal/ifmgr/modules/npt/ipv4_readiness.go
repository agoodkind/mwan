package npt

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/wanstate"
)

type ipv4NATRule struct {
	match      netip.Prefix
	target     netip.Addr
	kind       expr.NATType
	masquerade bool
}

// IPv4TranslationReadiness verifies the provider's required IPv4 translations against the kernel rules.
func IPv4TranslationReadiness(ctx context.Context, wan WAN, internal netip.Prefix) wanstate.FamilyTranslation {
	var state wanstate.FamilyTranslation
	if wan.TranslationV4 == nil {
		return state
	}
	state.Mode = string(wan.TranslationV4.Mode)
	if wan.TranslationV4.Mode == config.TranslationNative {
		state.Ready = true
		return state
	}
	if wan.TranslationV4.Mode != config.TranslationNAPT44 {
		state.Reason = "IPv4 translation mode is unsupported"
		return state
	}
	if !internal.IsValid() || !internal.Addr().Is4() {
		state.Reason = "internal IPv4 source network is unavailable"
		return state
	}
	if err := ctx.Err(); err != nil {
		state.Reason = "IPv4 translation readback was canceled"
		return state
	}
	conn, err := nftables.New()
	if err != nil {
		state.Reason = "IPv4 translation rules could not be read: " + err.Error()
		return state
	}
	table := &nftables.Table{Name: "nat", Family: nftables.TableFamilyIPv4}
	post, err := readIPv4NATChain(conn, table, chainPostrouting, wan.Iface)
	if err != nil {
		state.Reason = err.Error()
		return state
	}
	masquerade := false
	for _, rule := range post {
		if rule.masquerade && rule.match == internal.Masked() {
			masquerade = true
			break
		}
	}
	if !masquerade {
		state.Reason = fmt.Sprintf("IPv4 masquerade for %s from %s is absent", wan.Iface, internal.Masked())
		return state
	}
	for _, mapping := range wan.TranslationV4.StaticMappings {
		if !ipv4MappingReady(post, mapping.Internal, mapping.External, expr.NATTypeSourceNAT) {
			state.Reason = fmt.Sprintf("IPv4 source mapping %s to %s on %s is absent or overridden", mapping.Internal, mapping.External, wan.Iface)
			return state
		}
	}
	if len(wan.TranslationV4.StaticMappings) != 0 {
		pre, err := readIPv4NATChain(conn, table, chainPrerouting, wan.Iface)
		if err != nil {
			state.Reason = err.Error()
			return state
		}
		for _, mapping := range wan.TranslationV4.StaticMappings {
			if !ipv4MappingReady(pre, mapping.External, mapping.Internal, expr.NATTypeDestNAT) {
				state.Reason = fmt.Sprintf("IPv4 destination mapping %s to %s on %s is absent or overridden", mapping.External, mapping.Internal, wan.Iface)
				return state
			}
		}
	}
	if err := ctx.Err(); err != nil {
		state.Reason = "IPv4 translation readback was canceled"
		return state
	}
	state.Ready = true
	return state
}

func ipv4MappingReady(rules []ipv4NATRule, source, target netip.Addr, kind expr.NATType) bool {
	for _, rule := range rules {
		if rule.match.Contains(source) {
			return !rule.masquerade && rule.kind == kind && rule.match.Bits() == 32 && rule.match.Addr() == source && rule.target == target
		}
	}
	return false
}

func readIPv4NATChain(conn *nftables.Conn, table *nftables.Table, direction ruleChain, iface string) ([]ipv4NATRule, error) {
	chain, err := conn.ListChain(table, direction.String())
	if err != nil {
		slog.Warn("npt: read IPv4 NAT chain failed", "chain", direction.String(), "iface", iface, "err", err)
		return nil, fmt.Errorf("IPv4 %s chain could not be read: %w", direction, err)
	}
	hook, priority := nftables.ChainHookPostrouting, nftables.ChainPriorityNATSource
	if direction == chainPrerouting {
		hook, priority = nftables.ChainHookPrerouting, nftables.ChainPriorityNATDest
	}
	if chain.Type != nftables.ChainTypeNAT || chain.Hooknum == nil || *chain.Hooknum != *hook || chain.Priority == nil || *chain.Priority != *priority || chain.Policy == nil || *chain.Policy != nftables.ChainPolicyAccept {
		return nil, fmt.Errorf("IPv4 %s is not the required NAT base chain", direction)
	}
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		slog.Warn("npt: read IPv4 NAT rules failed", "chain", direction.String(), "iface", iface, "err", err)
		return nil, fmt.Errorf("IPv4 %s rules could not be read: %w", direction, err)
	}
	var decoded []ipv4NATRule
	for _, rule := range rules {
		expressions := make([]expr.Any, 0, len(rule.Exprs))
		for _, expression := range rule.Exprs {
			if _, counter := expression.(*expr.Counter); !counter {
				expressions = append(expressions, expression)
			}
		}
		if len(expressions) == 0 {
			continue
		}
		matchedDirection, matchedIface, matched := decodeInterfaceMatch(ifaceNameByIndex, expressions)
		if matched && matchedDirection == direction && matchedIface != iface {
			continue
		}
		if direction == chainPrerouting && ipv4MarkOnlyRule(expressions) {
			continue
		}
		if !matched || matchedDirection != direction {
			return nil, fmt.Errorf("IPv4 %s contains a rule whose effect on %s cannot be verified", direction, iface)
		}
		entry, ok := decodeIPv4NATRule(expressions[2:], direction)
		if !ok {
			return nil, fmt.Errorf("IPv4 %s contains an unsupported translation rule for %s", direction, iface)
		}
		decoded = append(decoded, entry)
	}
	return decoded, nil
}

// The Configs prerouting pin changes only the packet mark, which cannot bypass an unconditional address mapping.
func ipv4MarkOnlyRule(expressions []expr.Any) bool {
	for _, expression := range expressions {
		switch value := expression.(type) {
		case *expr.Meta:
			if value.SourceRegister && value.Key != expr.MetaKeyMARK {
				return false
			}
		case *expr.Payload:
			if value.OperationType != expr.PayloadLoad {
				return false
			}
		case *expr.Ct:
			if value.SourceRegister {
				return false
			}
		case *expr.Cmp, *expr.Bitwise, *expr.Immediate:
		default:
			return false
		}
	}
	return true
}

func decodeIPv4AddressMatch(expressions []expr.Any, direction ruleChain) (netip.Prefix, int, bool) {
	if len(expressions) < 3 {
		return netip.Prefix{}, 0, false
	}
	payload, ok := expressions[0].(*expr.Payload)
	offset := uint32(12)
	if direction == chainPrerouting {
		offset = 16
	}
	if !ok || payload.OperationType != expr.PayloadLoad || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != offset || payload.Len < 1 || payload.Len > 4 {
		return netip.Prefix{}, 0, false
	}
	bits, compareIndex := int(payload.Len)*8, 1
	if bitwise, ok := expressions[1].(*expr.Bitwise); ok {
		if bitwise.SourceRegister != payload.DestRegister || bitwise.DestRegister != payload.DestRegister || bitwise.Len != payload.Len || len(bitwise.Xor) != int(payload.Len) || !allZero(bitwise.Xor) {
			return netip.Prefix{}, 0, false
		}
		ones, width := net.IPMask(bitwise.Mask).Size()
		if width != int(payload.Len)*8 {
			return netip.Prefix{}, 0, false
		}
		bits, compareIndex = ones, 2
	}
	if len(expressions) <= compareIndex+1 {
		return netip.Prefix{}, 0, false
	}
	compare, ok := expressions[compareIndex].(*expr.Cmp)
	if !ok || compare.Op != expr.CmpOpEq || compare.Register != payload.DestRegister || len(compare.Data) != int(payload.Len) {
		return netip.Prefix{}, 0, false
	}
	var address [4]byte
	copy(address[:], compare.Data)
	prefix := netip.PrefixFrom(netip.AddrFrom4(address), bits)
	if prefix != prefix.Masked() {
		return netip.Prefix{}, 0, false
	}
	return prefix, compareIndex + 1, true
}

func decodeIPv4NATRule(expressions []expr.Any, direction ruleChain) (ipv4NATRule, bool) {
	var result ipv4NATRule
	prefix, consumed, ok := decodeIPv4AddressMatch(expressions, direction)
	if !ok {
		return result, false
	}
	result.match = prefix
	action := expressions[consumed:]
	if len(action) == 1 {
		masquerade, ok := action[0].(*expr.Masq)
		if !ok || direction != chainPostrouting || masquerade.ToPorts || masquerade.RegProtoMin != 0 || masquerade.RegProtoMax != 0 {
			return result, false
		}
		result.masquerade = true
		return result, true
	}
	if len(action) != 2 {
		return result, false
	}
	immediate, ok := action[0].(*expr.Immediate)
	if !ok || len(immediate.Data) != 4 {
		return result, false
	}
	nat, ok := action[1].(*expr.NAT)
	if !ok || nat.Family != unix.NFPROTO_IPV4 || nat.Prefix || !natHasNoExtraFlags(nat) || nat.RegAddrMin != immediate.Register || (nat.RegAddrMax != 0 && nat.RegAddrMax != immediate.Register) {
		return result, false
	}
	expectedKind := expr.NATTypeSourceNAT
	if direction == chainPrerouting {
		expectedKind = expr.NATTypeDestNAT
	}
	if nat.Type != expectedKind {
		return result, false
	}
	result.kind = nat.Type
	result.target = netip.AddrFrom4([4]byte(immediate.Data))
	return result, true
}
