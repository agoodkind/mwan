package npt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/nptpolicy"
)

func (m *Module) reconcileTranslator(
	ctx context.Context,
	log *slog.Logger,
	pairs map[string]nptpolicy.PrefixPair,
) (map[string]bool, bool, []bpf.InterfacePolicy, error) {
	ready := make(map[string]bool, len(pairs))
	if m.translator == nil {
		return ready, false, nil, nil
	}

	policies, ifIndexes, internalIndex, policyErr := m.interfacePolicies(ctx, log, pairs)
	states, err := m.translator.Reconcile(policies)
	if err != nil {
		log.WarnContext(ctx, "NPTv6 program reconciliation failed", "err", err)
		policyErr = errors.Join(policyErr, fmt.Errorf("reconcile NPTv6 programs: %w", err))
	} else if policyErr == nil {
		m.activePairs = maps.Clone(pairs)
	}

	internalReady := attachmentsReady(states, internalIndex)

	for name, index := range ifIndexes {
		ready[name] = internalReady && attachmentsReady(states, index)
	}

	return ready, err == nil, policies, policyErr
}

func (m *Module) retainCurrentTranslation(
	wan WAN,
	desiredEdges map[ifmgr.NPTEdgeRecord]bool,
	desired *desiredRules,
	rules map[string][]natRule,
	pairs map[string]nptpolicy.PrefixPair,
	delegated map[string]netip.Prefix,
) {
	m.markDesiredEdges(desiredEdges, wan, "")
	previous := m.activeRules[wan.Key()]
	rules[wan.Key()] = previous
	desired.add(previous)
	if pair, hasPair := m.activePairs[wan.Key()]; hasPair {
		pairs[wan.Key()] = pair
		delegated[wan.Key()] = pair.External
	}
}

func (m *Module) addressesNotApplied(wan WAN) bool {
	return wan.Owned && wan.Translation != nil && wan.Translation.Mode == config.TranslationNPTv6 &&
		(m.Env == nil || m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyApplied(
			wan.Key(),
			"ipv6",
		))
}

func isMasquerade(wan WAN) bool {
	return wan.Translation != nil && wan.Translation.Mode == config.TranslationMasquerade
}

func addMasqueradeRules(wan WAN, desired *desiredRules, rules map[string][]natRule) bool {
	rules[wan.Key()] = nil
	if !isMasquerade(wan) {
		return false
	}

	rules[wan.Key()] = []natRule{masqueradeRule(wan.Iface)}
	desired.add(rules[wan.Key()])

	return true
}

func masqueradeRule(iface string) natRule {
	return natRule{
		Chain:  chainPostrouting,
		Iface:  iface,
		Match:  netip.MustParsePrefix("::/0"),
		Op:     opMasquerade,
		ToAddr: netip.Addr{},
		ToPfx:  netip.Prefix{},
	}
}

func global128Address(current netif.CurrentAddr, pd1 netip.Addr) (netip.Addr, bool) {
	if current.Family != "inet6" {
		return netip.Addr{}, false
	}

	prefix, err := netip.ParsePrefix(current.CIDR)
	if err != nil || prefix.Bits() != 128 || !prefix.Addr().IsGlobalUnicast() ||
		prefix.Addr() == pd1 {
		return netip.Addr{}, false
	}

	return prefix.Addr(), true
}

func (m *Module) applyPlan(
	ctx context.Context,
	log *slog.Logger,
	desired desiredRules,
	pairs map[string]nptpolicy.PrefixPair,
	rules map[string][]natRule,
	desiredEdges map[ifmgr.NPTEdgeRecord]bool,
	delegated map[string]netip.Prefix,
	reconcileErr *error,
) {
	applyErr := m.apply.Apply(ctx, log, desired)
	if applyErr != nil {
		*reconcileErr = errors.Join(*reconcileErr, fmt.Errorf("apply: %w", applyErr))
	} else {
		m.activeRules = rules
	}

	bpfReady, nativeReady, desiredPolicies, policyErr := m.reconcileTranslator(ctx, log, pairs)
	*reconcileErr = errors.Join(*reconcileErr, policyErr)
	if err := m.releaseObsoleteEdges(
		ctx,
		log,
		desiredEdges,
		bpfReady,
		desiredPolicies,
	); err != nil {
		*reconcileErr = errors.Join(*reconcileErr, err)
	}

	m.publishLiveState(ctx, log, delegated, desired, applyErr == nil, bpfReady, nativeReady, pairs)
}

func (m *Module) planRules(
	wan WAN,
	policy *config.NPTv6Translation,
	externalPrefix netip.Prefix,
	pd1 netip.Addr,
	extra []netip.Addr,
	local []netip.Addr,
) ([]natRule, nptpolicy.PrefixPair) {
	rules := buildWANRules(wanRuleInput{
		Iface:        wan.Iface,
		External:     externalPrefix,
		OpnsenseEdge: m.opnsenseEdge,
		MwanbrEdge:   m.mwanbrEdge,
		ExtraDNAT:    extra,
	})
	destinationExceptions := append([]netip.Addr{pd1}, extra...)

	for _, addr := range local {
		if externalPrefix.Contains(addr) {
			destinationExceptions = append(destinationExceptions, addr)
		}
	}

	pair := nptpolicy.PrefixPair{
		ID:                    pairID(wan.Key()),
		Internal:              policy.InternalPrefix.Masked(),
		External:              externalPrefix,
		SourceExceptions:      []netip.Addr{m.opnsenseEdge, m.mwanbrEdge},
		DestinationExceptions: destinationExceptions,
	}

	return rules, pair
}
