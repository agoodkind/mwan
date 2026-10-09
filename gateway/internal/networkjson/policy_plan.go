package networkjson

import (
	"fmt"
	"log/slog"
	"math"
	"net/netip"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

// PolicyRuleKey identifies a policy rule by connection ID, family, and kind.
type PolicyRuleKey struct {
	ConnectionID connectionid.ID
	Family       Family
	Kind         interfaceintent.PolicyRuleKind
}

// String joins the encoded connection ID, family, and kind with "|".
func (k PolicyRuleKey) String() string {
	return encodeKey(k.ConnectionID.String(), string(k.Family), string(k.Kind))
}

// PolicyRules derives policy rules from c.WAN.
// Delegated IPv6 sources use an invalid prefix.
// PolicyRules rejects routing numbers outside uint32.
// PolicyRules rejects configured sources without valid prefixes.
func (c *Config) PolicyRules() (map[PolicyRuleKey]interfaceintent.PolicyRule, error) {
	rules := make(map[PolicyRuleKey]interfaceintent.PolicyRule)
	for id, entry := range c.WAN {
		numbers := []struct {
			leaf  string
			value int
		}{
			{leaf: "table-id", value: entry.TableID},
			{leaf: "fw-mark", value: entry.FwMark},
			{leaf: "fw-mark-prio", value: entry.FwMarkPrio},
			{leaf: "from-prio", value: entry.FromPrio},
		}
		for _, number := range numbers {
			if number.value < 0 || uint64(number.value) > math.MaxUint32 {
				err := fmt.Errorf("wan %s: %s %d is outside 0 to %d", id, number.leaf, number.value, uint32(math.MaxUint32))
				slog.Error("networkjson: provider routing number outside uint32", "err", err)
				return nil, err
			}
		}
		prefixTranslation := false
		externalConfigured := false
		external := netip.Prefix{}
		if policy := entry.TranslationV6; policy != nil && policy.Mode == config.TranslationNPTv6 && policy.NPT != nil {
			prefixTranslation = true
			externalConfigured = policy.NPT.ExternalSource == config.PrefixConfigured
			external = policy.NPT.ExternalPrefix
		}
		provider := interfaceintent.NewPolicyProvider(
			connectionid.ID(id), entry.TableID, uint32(entry.FwMark), entry.FwMarkPrio, entry.FromPrio,
			entry.TranslationV4 != nil, entry.V4Source,
			entry.TranslationV6 != nil, prefixTranslation, externalConfigured, external,
		)
		for _, rule := range interfaceintent.ConfiguredPolicyRules(provider) {
			if rule.SourceKind == interfaceintent.PolicySourceConfigured && !rule.Source.IsValid() {
				err := fmt.Errorf("wan %s: %s source rule has no configured prefix", id, rule.Family)
				slog.Error("networkjson: configured policy source missing", "err", err)
				return nil, err
			}
			key := PolicyRuleKey{ConnectionID: rule.ConnectionID, Family: Family(rule.Family), Kind: rule.Kind}
			rules[key] = rule
		}
	}
	return rules, nil
}
