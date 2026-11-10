//go:build linux

package firewall

import (
	"context"
	"fmt"
)

type establishedAcceptance string

const (
	acceptAllForwarded       establishedAcceptance = "accept"
	acceptEstablished        establishedAcceptance = "ct state established accept"
	acceptEstablishedRelated establishedAcceptance = "ct state established,related accept"
	acceptRelatedEstablished establishedAcceptance = "ct state related,established accept"
)

// InspectNative checks additional hooks for the confirmed established flows
// admitted by the native worker. It never changes another writer's policy.
func InspectNative(ctx context.Context, desired Ruleset) error {
	if _, err := Inspect(ctx, desired); err != nil {
		return err
	}
	readback, err := readKernel(ctx)
	if err != nil {
		return err
	}
	for _, item := range readback.Items {
		if item.Chain == nil || item.Chain.Hook == "" || nativeOwnedChain(item, desired) {
			continue
		}
		if err := inspectNativeExtraChain(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func nativeOwnedChain(item kernelItem, desired Ruleset) bool {
	for _, chain := range desired.Chains {
		if item.Chain.Family == chain.Table.Family && item.Chain.Table == chain.Table.Name && item.Chain.Name == chain.Name {
			return true
		}
	}
	return false
}

func inspectNativeExtraChain(ctx context.Context, item kernelItem) error {
	chain := item.Chain
	layer3 := chain.Family == "inet" || chain.Family == "ip" || chain.Family == "ip6"
	if layer3 && (chain.Hook == "input" || chain.Hook == "output") {
		// Native route certification excludes locally delivered packets.
		return nil
	}
	if layer3 && chain.Type == "nat" && (chain.Hook == "prerouting" || chain.Hook == "postrouting") {
		// Established packets reuse their certified conntrack NAT binding.
		return nil
	}
	output, err := runNFT(ctx, nil, "list", "chain", chain.Family, chain.Table, chain.Name)
	if err != nil {
		return err
	}
	rules := readRuleLines(string(output))
	if len(rules) == 0 && chain.Policy == "accept" {
		return nil
	}
	if layer3 && chain.Type == "filter" && chain.Hook == "forward" && len(rules) != 0 {
		// No earlier condition may select a different verdict or packet mark.
		switch establishedAcceptance(normalizeRule(rules[0])) {
		case acceptAllForwarded, acceptEstablished, acceptEstablishedRelated, acceptRelatedEstablished:
			return nil
		}
	}
	return fmt.Errorf("native admission cannot certify additional %s %s chain %s (%s/%s)", chain.Family, chain.Table, chain.Name, chain.Type, chain.Hook)
}
