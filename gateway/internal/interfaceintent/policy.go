package interfaceintent

import (
	"net/netip"

	"goodkind.io/mwan/internal/connectionid"
)

// PolicyFamilyIPv4 and PolicyFamilyIPv6 identify rule address families.
const (
	PolicyFamilyIPv4 = "ipv4"
	PolicyFamilyIPv6 = "ipv6"
)

// PolicyRuleKind selects a firewall mark or source prefix match.
type PolicyRuleKind string

const (
	// PolicyRuleFwmark selects a provider firewall mark match.
	PolicyRuleFwmark PolicyRuleKind = "fwmark"
	// PolicyRuleSource selects a provider source prefix match.
	PolicyRuleSource PolicyRuleKind = "source"
)

// PolicySourceKind classifies a source selector as absent, configured, or runtime.
type PolicySourceKind string

const (
	// PolicySourceNone indicates that the rule has no source selector.
	PolicySourceNone PolicySourceKind = "none"
	// PolicySourceConfigured identifies a configured source selector.
	PolicySourceConfigured PolicySourceKind = "configured"
	// PolicySourceRuntime identifies a source selector read from translation state.
	PolicySourceRuntime PolicySourceKind = "runtime"
)

// PolicyCondition defines a prerequisite for installing a provider rule.
type PolicyCondition string

const (
	// PolicyConditionTranslation requires the family's translation readiness flag.
	PolicyConditionTranslation PolicyCondition = "translation-ready"
	// PolicyConditionGateway requires a nonempty default gateway for the rule's family.
	PolicyConditionGateway PolicyCondition = "default-gateway-discovered"
	// PolicyConditionHealth requires a healthy provider state.
	PolicyConditionHealth PolicyCondition = "provider-healthy"
	// PolicyConditionSourcePrefix requires a valid external prefix in NPTv6 translation
	// state.
	PolicyConditionSourcePrefix PolicyCondition = "translated-source-published"
)

// PolicySource classifies a source selector and stores its configured prefix.
type PolicySource struct {
	Kind   PolicySourceKind
	Prefix netip.Prefix
}

// IPv4PolicySource converts a valid address to a configured host prefix.
// An empty address selects PolicySourceNone.
// A nonempty invalid address selects PolicySourceConfigured with an invalid Prefix.
func IPv4PolicySource(address string) *PolicySource {
	if address == "" {
		return &PolicySource{Kind: PolicySourceNone, Prefix: netip.Prefix{}}
	}
	source := PolicySource{Kind: PolicySourceConfigured, Prefix: netip.Prefix{}}
	if parsed, err := netip.ParseAddr(address); err == nil {
		source.Prefix = netip.PrefixFrom(parsed, parsed.BitLen())
	}
	return &source
}

// IPv6PolicySource returns PolicySourceNone when prefixTranslation is false.
// With prefix translation, externalConfigured selects a masked configured prefix.
// With prefix translation, an unconfigured prefix selects PolicySourceRuntime.
func IPv6PolicySource(prefixTranslation bool, externalConfigured bool, external netip.Prefix) *PolicySource {
	if !prefixTranslation {
		return &PolicySource{Kind: PolicySourceNone, Prefix: netip.Prefix{}}
	}
	if externalConfigured {
		return &PolicySource{Kind: PolicySourceConfigured, Prefix: external.Masked()}
	}
	return &PolicySource{Kind: PolicySourceRuntime, Prefix: netip.Prefix{}}
}

// NewPolicyProvider creates provider policy input from configuration.
func NewPolicyProvider(
	id connectionid.ID,
	tableID int,
	mark uint32,
	markPriority int,
	sourcePriority int,
	ipv4Configured bool,
	ipv4Address string,
	ipv6Configured bool,
	prefixTranslation bool,
	externalConfigured bool,
	external netip.Prefix,
) PolicyProvider {
	provider := PolicyProvider{
		ConnectionID:   id,
		TableID:        tableID,
		Mark:           mark,
		MarkPriority:   markPriority,
		SourcePriority: sourcePriority,
		IPv4:           nil,
		IPv6:           nil,
	}
	if ipv4Configured {
		provider.IPv4 = IPv4PolicySource(ipv4Address)
	}
	if ipv6Configured {
		provider.IPv6 = IPv6PolicySource(prefixTranslation, externalConfigured, external)
	}
	return provider
}

// PolicyProvider stores a provider's routing table, mark, priorities, and family selectors.
// A nil IPv4 or IPv6 disables rule generation for that family.
type PolicyProvider struct {
	ConnectionID   connectionid.ID
	TableID        int
	Mark           uint32
	MarkPriority   int
	SourcePriority int
	IPv4           *PolicySource
	IPv6           *PolicySource
}

// PolicyRule defines a provider rule and its activation conditions.
// The daemon requires every activation condition to pass before installing the rule.
type PolicyRule struct {
	ConnectionID         connectionid.ID
	Family               string
	Kind                 PolicyRuleKind
	Priority             int
	TableID              int
	Mark                 uint32
	Source               netip.Prefix
	SourceKind           PolicySourceKind
	ActivationConditions []PolicyCondition
}

// PolicyFamilyConditions returns the shared activation conditions for a family.
func PolicyFamilyConditions() []PolicyCondition {
	return []PolicyCondition{PolicyConditionTranslation, PolicyConditionGateway, PolicyConditionHealth}
}

// ConfiguredPolicyRules returns mark and source rules for each configured family.
// IPv4 rules precede IPv6 rules.
// Mark rules precede source rules within each family.
func ConfiguredPolicyRules(provider PolicyProvider) []PolicyRule {
	families := []struct {
		name   string
		source *PolicySource
	}{
		{name: PolicyFamilyIPv4, source: provider.IPv4},
		{name: PolicyFamilyIPv6, source: provider.IPv6},
	}
	var rules []PolicyRule
	for _, family := range families {
		if family.source == nil {
			continue
		}
		rules = append(rules, PolicyRule{
			ConnectionID:         provider.ConnectionID,
			Family:               family.name,
			Kind:                 PolicyRuleFwmark,
			Priority:             provider.MarkPriority,
			TableID:              provider.TableID,
			Mark:                 provider.Mark,
			Source:               netip.Prefix{},
			SourceKind:           PolicySourceNone,
			ActivationConditions: PolicyFamilyConditions(),
		})
		if family.source.Kind != PolicySourceConfigured && family.source.Kind != PolicySourceRuntime {
			continue
		}
		conditions := PolicyFamilyConditions()
		if family.name == PolicyFamilyIPv6 {
			// IPv6 source rules require an external prefix from NPTv6 translation state.
			conditions = append(conditions, PolicyConditionSourcePrefix)
		}
		source := netip.Prefix{}
		if family.source.Kind == PolicySourceConfigured {
			source = family.source.Prefix
		}
		rules = append(rules, PolicyRule{
			ConnectionID:         provider.ConnectionID,
			Family:               family.name,
			Kind:                 PolicyRuleSource,
			Priority:             provider.SourcePriority,
			TableID:              provider.TableID,
			Mark:                 0,
			Source:               source,
			SourceKind:           family.source.Kind,
			ActivationConditions: conditions,
		})
	}
	return rules
}
