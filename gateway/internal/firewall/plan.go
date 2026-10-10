package firewall

import (
	"net/netip"
	"strconv"
	"strings"
)

// RulePurpose classifies compiled firewall rules.
type RulePurpose string

// Purpose and scope identify each rule that Compile emits within its chain.
const (
	PurposeEstablished           RulePurpose = "established"
	PurposeLoopback              RulePurpose = "loopback"
	PurposeICMP                  RulePurpose = "icmp"
	PurposeManagementService     RulePurpose = "management-service"
	PurposeLocalPermit           RulePurpose = "local-permit"
	PurposeDropLog               RulePurpose = "drop-log"
	PurposeInternalClassForward  RulePurpose = "internal-class-forward"
	PurposeForwardOutbound       RulePurpose = "forward-outbound"
	PurposeForwardInbound        RulePurpose = "forward-inbound"
	PurposePinnedSourceMark      RulePurpose = "pinned-source-mark"
	PurposeStaticMappingDNAT     RulePurpose = "static-mapping-dnat"
	PurposeStaticMappingSNAT     RulePurpose = "static-mapping-snat"
	PurposeMasquerade            RulePurpose = "masquerade"
	PurposeInternalClassUnmark   RulePurpose = "internal-class-unmark"
	PurposeProviderMark          RulePurpose = "provider-mark"
	PurposePinnedDestinationMark RulePurpose = "pinned-destination-mark"
	PurposeForcedDSCPMark        RulePurpose = "forced-dscp-mark"
	PurposeRestoreMark           RulePurpose = "restore-mark"
	PurposeKeepRelatedMark       RulePurpose = "keep-related-mark"
	PurposeSaveMark              RulePurpose = "save-mark"
)

// RuleAction classifies a rule's verdict, mark update, or address translation.
type RuleAction string

// ActionAccept through ActionMasquerade define the compiled rule action values.
const (
	ActionAccept     RuleAction = "accept"
	ActionDrop       RuleAction = "drop"
	ActionReturn     RuleAction = "return"
	ActionMark       RuleAction = "mark"
	ActionSaveMark   RuleAction = "save-mark"
	ActionDNAT       RuleAction = "dnat"
	ActionSNAT       RuleAction = "snat"
	ActionMasquerade RuleAction = "masquerade"
)

const (
	scopeAll            = "all"
	scopeSeparator      = ","
	occurrenceSeparator = "#"
)

// Rule stores an nftables expression and metadata for a compiled rule.
// Purpose and Scope identify the rule within its chain.
type Rule struct {
	Purpose         RulePurpose
	Scope           string
	Action          RuleAction
	Expression      string
	Interface       string
	OutputInterface string
	Source          netip.Prefix
	Destination     netip.Prefix
	Mark            *uint32
}

func newRule(purpose RulePurpose, scope string, action RuleAction, expression string) Rule {
	return Rule{
		Purpose:         purpose,
		Scope:           scope,
		Action:          action,
		Expression:      expression,
		Interface:       "",
		OutputInterface: "",
		Source:          netip.Prefix{},
		Destination:     netip.Prefix{},
		Mark:            nil,
	}
}

func (r Rule) in(iface string) Rule {
	r.Interface = iface
	return r
}

func (r Rule) out(iface string) Rule {
	r.OutputInterface = iface
	return r
}

func (r Rule) from(source netip.Prefix) Rule {
	r.Source = source
	return r
}

func (r Rule) to(destination netip.Prefix) Rule {
	r.Destination = destination
	return r
}

func (r Rule) marked(mark uint32) Rule {
	r.Mark = &mark
	return r
}

func hostPrefix(address netip.Addr) netip.Prefix {
	return netip.PrefixFrom(address, address.BitLen())
}

// ruleScope escapes %, commas, and # to distinguish key text from separators.
func ruleScope(parts ...string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ReplaceAll(part, "%", "%25")
		part = strings.ReplaceAll(part, scopeSeparator, "%2C")
		escaped = append(escaped, strings.ReplaceAll(part, occurrenceSeparator, "%23"))
	}
	return strings.Join(escaped, scopeSeparator)
}

type ruleIdentity struct {
	purpose RulePurpose
	scope   string
}

// Duplicate purpose and scope pairs receive #2, #3, and later suffixes in slice order.
func disambiguate(rules []Rule) {
	seen := make(map[ruleIdentity]int, len(rules))
	for i := range rules {
		identity := ruleIdentity{purpose: rules[i].Purpose, scope: rules[i].Scope}
		seen[identity]++
		if occurrence := seen[identity]; occurrence > 1 {
			rules[i].Scope += occurrenceSeparator + strconv.Itoa(occurrence)
		}
	}
}
