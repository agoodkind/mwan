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

func routingNumber(id string, leaf string, value int) (uint32, error) {
	if value < 0 || uint64(value) > math.MaxUint32 {
		err := fmt.Errorf("wan %s: %s %d is outside 0 to %d", id, leaf, value, uint32(math.MaxUint32))
		slog.Error("networkjson: provider routing number outside uint32", "err", err)
		return 0, err
	}
	return uint32(value), nil
}

// PolicyRules rejects routing numbers outside uint32.
// PolicyRules rejects configured sources without valid prefixes.
// Delegated IPv6 sources use an invalid prefix.
func (c *Config) PolicyRules() (map[PolicyRuleKey]interfaceintent.PolicyRule, error) {
	rules := make(map[PolicyRuleKey]interfaceintent.PolicyRule)
	for id, entry := range c.WAN {
		if _, err := routingNumber(id, "table-id", entry.TableID); err != nil {
			return nil, err
		}
		mark, markErr := routingNumber(id, "fw-mark", entry.FwMark)
		if markErr != nil {
			return nil, markErr
		}
		if _, err := routingNumber(id, "fw-mark-prio", entry.FwMarkPrio); err != nil {
			return nil, err
		}
		if _, err := routingNumber(id, "from-prio", entry.FromPrio); err != nil {
			return nil, err
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
			connectionid.ID(id), entry.TableID, mark, entry.FwMarkPrio, entry.FromPrio,
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
