package networkjson

import (
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/firewall"
)

// FirewallChainKey identifies a chain by family, table, and chain name.
type FirewallChainKey struct {
	Family string
	Table  string
	Chain  string
}

// String joins the encoded family, table, and chain with "|".
func (k FirewallChainKey) String() string {
	return encodeKey(k.Family, k.Table, k.Chain)
}

// FirewallRuleKey identifies a rule by family, table, chain, purpose, and scope.
type FirewallRuleKey struct {
	Family  string
	Table   string
	Chain   string
	Purpose firewall.RulePurpose
	Scope   string
}

// String joins the encoded family, table, chain, purpose, and scope with "|".
func (k FirewallRuleKey) String() string {
	return encodeKey(k.Family, k.Table, k.Chain, string(k.Purpose), k.Scope)
}

// FirewallSetKey identifies a set by family, table, and set name.
type FirewallSetKey struct {
	Family string
	Table  string
	Set    string
}

// String joins the encoded family, table, and set with "|".
func (k FirewallSetKey) String() string {
	return encodeKey(k.Family, k.Table, k.Set)
}

// FirewallChain includes a compiled chain and its rule keys in compiler order.
type FirewallChain struct {
	firewall.Chain
	RuleOrder []FirewallRuleKey
}

// FirewallPlan indexes compiled chains, rules, and sets by their keys.
type FirewallPlan struct {
	Chains map[FirewallChainKey]FirewallChain
	Rules  map[FirewallRuleKey]firewall.Rule
	Sets   map[FirewallSetKey]firewall.Set
}

// FirewallPlan indexes the compiled firewall by chain, rule, and set keys.
// FirewallPlan returns empty maps for configurations without firewall ownership.
// FirewallPlan rejects duplicate keys.
func (c *Config) FirewallPlan() (FirewallPlan, error) {
	plan := FirewallPlan{
		Chains: make(map[FirewallChainKey]FirewallChain),
		Rules:  make(map[FirewallRuleKey]firewall.Rule),
		Sets:   make(map[FirewallSetKey]firewall.Set),
	}
	compiled, err := firewall.Compile(c.Firewall)
	if err != nil {
		slog.Error("networkjson: firewall compile failed", "err", err)
		return FirewallPlan{}, fmt.Errorf("steering-group/firewall: %w", err)
	}
	for _, chain := range compiled.Chains {
		chainKey := FirewallChainKey{Family: chain.Table.Family, Table: chain.Table.Name, Chain: chain.Name}
		if _, duplicated := plan.Chains[chainKey]; duplicated {
			return FirewallPlan{}, fmt.Errorf("firewall chain %s is compiled twice", chainKey)
		}
		order := make([]FirewallRuleKey, 0, len(chain.Rules))
		for _, rule := range chain.Rules {
			ruleKey := FirewallRuleKey{
				Family: chainKey.Family, Table: chainKey.Table, Chain: chainKey.Chain,
				Purpose: rule.Purpose, Scope: rule.Scope,
			}
			if _, duplicated := plan.Rules[ruleKey]; duplicated {
				return FirewallPlan{}, fmt.Errorf("firewall rule %s is compiled twice", ruleKey)
			}
			plan.Rules[ruleKey] = rule
			order = append(order, ruleKey)
		}
		plan.Chains[chainKey] = FirewallChain{Chain: chain, RuleOrder: order}
	}
	for _, set := range compiled.Sets {
		setKey := FirewallSetKey{Family: set.Table.Family, Table: set.Table.Name, Set: set.Name}
		if _, duplicated := plan.Sets[setKey]; duplicated {
			return FirewallPlan{}, fmt.Errorf("firewall set %s is compiled twice", setKey)
		}
		plan.Sets[setKey] = set
	}
	return plan, nil
}
