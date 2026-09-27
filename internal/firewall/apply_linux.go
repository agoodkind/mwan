//go:build linux

// Package firewall compiles and installs the gateway's nftables rules.
package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Apply installs the desired chains in one nftables transaction. It creates
// missing structures and preserves existing destination-set elements.
func Apply(ctx context.Context, desired Ruleset) error {
	_, err := ApplyWithReport(ctx, desired)
	return err
}

// ApplyResult records destination sets created by a successful transaction.
type ApplyResult struct {
	CreatedSets []Set
}

// ApplyWithReport installs the policy and reports newly created sets.
func ApplyWithReport(ctx context.Context, desired Ruleset) (ApplyResult, error) {
	readback, err := readKernel(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	for _, chain := range desired.Chains {
		if item, found := kernelChain(readback.Items, chain); found {
			if err := checkDefinitions(kernelRuleset{Items: []kernelItem{item}}, Ruleset{Chains: []Chain{chain}, Sets: nil}); err != nil {
				return ApplyResult{}, err
			}
		}
	}
	for _, set := range desired.Sets {
		if item, found := kernelSet(readback.Items, set); found {
			if err := checkDefinitions(kernelRuleset{Items: []kernelItem{item}}, Ruleset{Chains: nil, Sets: []Set{set}}); err != nil {
				return ApplyResult{}, err
			}
		}
	}
	result := ApplyResult{CreatedSets: nil}
	var script strings.Builder
	tables := make(map[Table]bool)
	for _, chain := range desired.Chains {
		if !tables[chain.Table] {
			fmt.Fprintf(&script, "add table %s %s\n", chain.Table.Family, chain.Table.Name)
			tables[chain.Table] = true
		}
		fmt.Fprintf(&script, "add chain %s %s %s { type %s hook %s priority %s; policy %s; }\n",
			chain.Table.Family, chain.Table.Name, chain.Name, chain.Type, chain.Hook, chain.Priority, chain.Policy)
	}
	for _, set := range desired.Sets {
		if !tables[set.Table] {
			fmt.Fprintf(&script, "add table %s %s\n", set.Table.Family, set.Table.Name)
			tables[set.Table] = true
		}
		if writeSet(&script, set, readback.Items) {
			result.CreatedSets = append(result.CreatedSets, set)
		}
	}
	for _, chain := range desired.Chains {
		fmt.Fprintf(&script, "flush chain %s %s %s\n", chain.Table.Family, chain.Table.Name, chain.Name)
		for _, rule := range chain.Rules {
			fmt.Fprintf(&script, "add rule %s %s %s %s\n", chain.Table.Family, chain.Table.Name, chain.Name, rule)
		}
	}
	if _, err := runNFT(ctx, []byte(script.String()), "-f", "-"); err != nil {
		slog.ErrorContext(ctx, "apply firewall rules failed", "err", err)
		return ApplyResult{}, fmt.Errorf("apply firewall rules: %w", err)
	}
	return result, nil
}

func writeSet(script *strings.Builder, set Set, existing []kernelItem) bool {
	fmt.Fprintf(script, "add set %s %s %s { type %s; flags interval; auto-merge; }\n", set.Table.Family, set.Table.Name, set.Name, set.KeyType)
	if _, found := kernelSet(existing, set); found {
		return false
	}
	if len(set.Elements) == 0 {
		return true
	}
	values := make([]string, 0, len(set.Elements))
	for _, prefix := range set.Elements {
		values = append(values, prefix.String())
	}
	fmt.Fprintf(script, "add element %s %s %s { %s }\n", set.Table.Family, set.Table.Name, set.Name, strings.Join(values, ", "))
	return true
}
