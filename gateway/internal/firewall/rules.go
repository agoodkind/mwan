package firewall

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Table identifies an nftables family and table.
type Table struct {
	Family string
	Name   string
}

// Chain defines one firewall-owned base chain.
type Chain struct {
	Table    Table
	Name     string
	Type     string
	Hook     string
	Priority string
	Policy   string
	Rules    []Rule
}

// Set defines a named nftables set. The destination refresher updates its elements.
type Set struct {
	Table    Table
	Name     string
	KeyType  string
	Elements []netip.Prefix
}

// Ruleset is the complete desired content of the firewall-owned chains.
// Existing set elements remain under the destination refresher's control.
type Ruleset struct {
	Chains []Chain
	Sets   []Set
}

const (
	loopbackInterface = "lo"
	classMatch        = "meta priority & 0xffff0000 == 0x4e500000"
	stateNew          = "new"
	stateEstablished  = "established"
)

func filterTable() Table { return Table{Family: "inet", Name: "filter"} }
func natTable() Table    { return Table{Family: "ip", Name: "nat"} }
func mangleTable() Table { return Table{Family: "inet", Name: "mangle"} }

// CompileBaseline creates the restrictive policy that startup can install
// before the full provider document passes validation.
func CompileBaseline(config BaselineConfig) (Ruleset, error) {
	if err := config.Validate(); err != nil {
		return Ruleset{}, err
	}
	input := Chain{Table: filterTable(), Name: "input", Type: "filter", Hook: "input", Priority: "filter", Policy: "drop", Rules: nil}
	forward := Chain{Table: filterTable(), Name: "forward", Type: "filter", Hook: "forward", Priority: "filter", Policy: "drop", Rules: nil}
	output := Chain{Table: filterTable(), Name: "output", Type: "filter", Hook: "output", Priority: "filter", Policy: "accept", Rules: nil}
	input.Rules = inputRules(config.ManagementInterface, config.ManagementServices, config.LocalPermits)
	forward.Rules = []Rule{establishedRule(), dropLogRule("forward")}
	return finishRuleset(Ruleset{Chains: []Chain{input, forward, output}, Sets: nil}), nil
}

// Compile builds all firewall-owned chains and destination sets.
func Compile(config Config) (Ruleset, error) {
	if err := config.Validate(); err != nil {
		return Ruleset{}, err
	}
	if !config.Enabled {
		return Ruleset{Chains: nil, Sets: nil}, nil
	}
	input := Chain{Table: filterTable(), Name: "input", Type: "filter", Hook: "input", Priority: "filter", Policy: "drop", Rules: nil}
	forward := Chain{Table: filterTable(), Name: "forward", Type: "filter", Hook: "forward", Priority: "filter", Policy: "drop", Rules: nil}
	output := Chain{Table: filterTable(), Name: "output", Type: "filter", Hook: "output", Priority: "filter", Policy: "accept", Rules: nil}
	preNAT := Chain{Table: natTable(), Name: "prerouting", Type: "nat", Hook: "prerouting", Priority: "dstnat", Policy: "accept", Rules: nil}
	postNAT := Chain{Table: natTable(), Name: "postrouting", Type: "nat", Hook: "postrouting", Priority: "srcnat", Policy: "accept", Rules: nil}
	preMangle := Chain{Table: mangleTable(), Name: "prerouting", Type: "filter", Hook: "prerouting", Priority: "mangle", Policy: "accept", Rules: nil}
	postMangle := Chain{Table: mangleTable(), Name: "postrouting", Type: "filter", Hook: "postrouting", Priority: "mangle", Policy: "accept", Rules: nil}

	input.Rules = inputRules(config.ManagementInterface, config.ManagementServices, config.LocalPermits)
	forward.Rules = forwardRules(config)

	internal := strconv.Quote(config.InternalInterface)
	pinMark := uint32(0)
	for _, provider := range config.Providers {
		if provider.Interface == config.PinnedProvider {
			pinMark = provider.Mark
		}
	}
	pinPorts := "udp sport " + strconv.Itoa(int(config.PinnedSourcePort)) + " udp dport " + strconv.Itoa(int(config.PinnedDestinationPort))
	pinMarkText := strconv.FormatUint(uint64(pinMark), 10)
	if config.PinnedProvider != "" {
		expression := "iifname " + internal + " ip saddr " + config.PinnedSourceIPv4.String() + " " + pinPorts + " ct state new meta mark set " + pinMarkText
		preNAT.Rules = append(preNAT.Rules, newRule(PurposePinnedSourceMark, string(IPv4), ActionMark, expression).
			in(config.InternalInterface).from(hostPrefix(config.PinnedSourceIPv4)).marked(pinMark))
	}
	preMangle.Rules = append(preMangle.Rules, newRule(PurposeInternalClassUnmark, scopeAll, ActionReturn,
		"iifname "+internal+" "+classMatch+" meta mark set 0 return").in(config.InternalInterface).marked(0))
	for _, provider := range config.Providers {
		iface := strconv.Quote(provider.Interface)
		mark := strconv.FormatUint(uint64(provider.Mark), 10)
		for _, mapping := range provider.StaticMappings {
			scope := ruleScope(provider.Interface, mapping.External.String())
			preNAT.Rules = append(preNAT.Rules, newRule(PurposeStaticMappingDNAT, scope, ActionDNAT,
				"iifname "+iface+" ip daddr "+mapping.External.String()+" dnat to "+mapping.Internal.String()).
				in(provider.Interface).to(hostPrefix(mapping.External)))
			postNAT.Rules = append(postNAT.Rules, newRule(PurposeStaticMappingSNAT, scope, ActionSNAT,
				"oifname "+iface+" ip saddr "+mapping.Internal.String()+" snat to "+mapping.External.String()).
				out(provider.Interface).from(hostPrefix(mapping.Internal)))
		}
		preMangle.Rules = append(preMangle.Rules, newRule(PurposeProviderMark, ruleScope(provider.Interface), ActionMark,
			"iifname "+iface+" ct state new meta mark set "+mark).in(provider.Interface).marked(provider.Mark))
	}
	for _, provider := range config.Providers {
		if provider.MasqueradeIPv4 {
			postNAT.Rules = append(postNAT.Rules, newRule(PurposeMasquerade, ruleScope(provider.Interface), ActionMasquerade,
				"oifname "+strconv.Quote(provider.Interface)+" ip saddr "+config.InternalNetworkIPv4.String()+" masquerade").
				out(provider.Interface).from(config.InternalNetworkIPv4))
		}
	}
	if config.PinnedProvider != "" {
		preMangle.Rules = append(preMangle.Rules,
			newRule(PurposePinnedDestinationMark, string(IPv4), ActionMark,
				"ip daddr @"+config.PinnedSetV4Name+" meta mark set "+pinMarkText).marked(pinMark),
			newRule(PurposePinnedDestinationMark, string(IPv6), ActionMark,
				"ip6 daddr @"+config.PinnedSetV6Name+" meta mark set "+pinMarkText).marked(pinMark),
			newRule(PurposePinnedSourceMark, string(IPv6), ActionMark,
				"ip6 saddr "+config.PinnedSourceIPv6.String()+"/128 "+pinPorts+" ct state new meta mark set "+pinMarkText).
				from(hostPrefix(config.PinnedSourceIPv6)).marked(pinMark))
	}
	for _, provider := range config.Providers {
		if provider.ForcedDSCP == 0 {
			continue
		}
		dscp := strconv.Itoa(int(provider.ForcedDSCP))
		mark := strconv.FormatUint(uint64(provider.Mark), 10)
		preMangle.Rules = append(preMangle.Rules,
			newRule(PurposeForcedDSCPMark, ruleScope(provider.Interface, string(IPv4)), ActionMark,
				"iifname "+internal+" ip dscp "+dscp+" ct state new meta mark set "+mark).
				in(config.InternalInterface).marked(provider.Mark),
			newRule(PurposeForcedDSCPMark, ruleScope(provider.Interface, string(IPv6)), ActionMark,
				"iifname "+internal+" ip6 dscp "+dscp+" ct state new meta mark set "+mark).
				in(config.InternalInterface).marked(provider.Mark))
	}
	preMangle.Rules = append(preMangle.Rules,
		newRule(PurposeRestoreMark, stateNew, ActionMark, "ct state new ct mark != 0x00000000 meta mark set ct mark"),
		newRule(PurposeRestoreMark, stateEstablished, ActionMark, "ct state established,related meta mark set ct mark"))
	postMangle.Rules = append(postMangle.Rules, newRule(PurposeSaveMark, scopeAll, ActionSaveMark, "ct mark set meta mark"))

	rules := Ruleset{Chains: []Chain{input, forward, output, preNAT, postNAT, preMangle, postMangle}, Sets: nil}
	if config.PinnedProvider != "" {
		rules.Sets = []Set{
			{Table: mangleTable(), Name: config.PinnedSetV4Name, KeyType: "ipv4_addr", Elements: config.PinnedIPv4},
			{Table: mangleTable(), Name: config.PinnedSetV6Name, KeyType: "ipv6_addr", Elements: config.PinnedIPv6},
		}
	}
	return finishRuleset(rules), nil
}

func finishRuleset(rules Ruleset) Ruleset {
	for _, chain := range rules.Chains {
		disambiguate(chain.Rules)
	}
	return rules
}

func establishedRule() Rule {
	return newRule(PurposeEstablished, scopeAll, ActionAccept, "ct state established,related accept")
}

func dropLogRule(chain string) Rule {
	return newRule(PurposeDropLog, scopeAll, ActionDrop, `limit rate 1/second log prefix "nftables `+chain+` drop: " drop`)
}

func inputRules(managementInterface string, services []Service, permits []TransportPermit) []Rule {
	rules := []Rule{
		establishedRule(),
		newRule(PurposeLoopback, scopeAll, ActionAccept, "iifname "+strconv.Quote(loopbackInterface)+" accept").in(loopbackInterface),
		newRule(PurposeICMP, string(IPv4), ActionAccept, "ip protocol icmp accept"),
		newRule(PurposeICMP, string(IPv6), ActionAccept, "ip6 nexthdr icmpv6 accept"),
	}
	rules = append(rules, managementRules(managementInterface, services)...)
	for _, permit := range permits {
		rules = append(rules, permitRule(PurposeLocalPermit, localPermitScope(permit), permit))
	}
	return append(rules, dropLogRule("input"))
}

func forwardRules(config Config) []Rule {
	internal := strconv.Quote(config.InternalInterface)
	rules := []Rule{
		establishedRule(),
		newRule(PurposeInternalClassForward, scopeAll, ActionAccept,
			"iifname "+internal+" oifname "+internal+" "+classMatch+" accept").
			in(config.InternalInterface).out(config.InternalInterface),
	}
	for _, path := range config.Paths {
		for _, family := range []struct {
			active bool
			name   Family
		}{{path.IPv4, IPv4}, {path.IPv6, IPv6}} {
			if !family.active {
				continue
			}
			scope := ruleScope(path.ExternalInterface, string(family.name))
			rules = append(rules,
				newRule(PurposeForwardOutbound, scope, ActionAccept,
					"iifname "+strconv.Quote(path.InternalInterface)+" oifname "+strconv.Quote(path.ExternalInterface)+" meta nfproto "+string(family.name)+" accept").
					in(path.InternalInterface).out(path.ExternalInterface),
				newRule(PurposeForwardInbound, scope, ActionAccept,
					"iifname "+strconv.Quote(path.ExternalInterface)+" oifname "+strconv.Quote(path.InternalInterface)+" meta nfproto "+string(family.name)+" accept").
					in(path.ExternalInterface).out(path.InternalInterface))
		}
	}
	return append(rules, dropLogRule("forward"))
}

func permitRule(purpose RulePurpose, scope string, permit TransportPermit) Rule {
	line := "iifname " + strconv.Quote(permit.InputInterface) + " meta nfproto " + string(permit.Family) + " meta l4proto " + permit.Protocol
	if permit.Source.IsValid() {
		line += " " + addressToken(permit.Source, true)
	}
	if permit.Destination.IsValid() {
		line += " " + addressToken(permit.Destination, false)
	}
	if permit.SourcePort != 0 {
		line += " " + permit.Protocol + " sport " + strconv.Itoa(int(permit.SourcePort))
	}
	if permit.DestinationPort != 0 {
		line += " " + permit.Protocol + " dport " + strconv.Itoa(int(permit.DestinationPort))
	}
	return newRule(purpose, scope, ActionAccept, line+" accept").in(permit.InputInterface).from(permit.Source).to(permit.Destination)
}

func localPermitScope(permit TransportPermit) string {
	parts := []string{
		permit.InputInterface, string(permit.Family), permit.Protocol,
		strconv.Itoa(int(permit.SourcePort)), strconv.Itoa(int(permit.DestinationPort)),
	}
	if permit.Source.IsValid() || permit.Destination.IsValid() {
		source := ""
		if permit.Source.IsValid() {
			source = permit.Source.String()
		}
		destination := ""
		if permit.Destination.IsValid() {
			destination = permit.Destination.String()
		}
		parts = append(parts, source, destination)
	}
	return ruleScope(parts...)
}

func managementRules(iface string, services []Service) []Rule {
	var rules []Rule
	for _, service := range services {
		port := strconv.Itoa(int(service.Port))
		if len(service.Sources) == 0 {
			for _, family := range []Family{IPv4, IPv6} {
				permit := TransportPermit{InputInterface: iface, Family: family, Source: netip.Prefix{}, Destination: netip.Prefix{}, Protocol: service.Protocol, SourcePort: 0, DestinationPort: service.Port}
				rules = append(rules, permitRule(PurposeManagementService, ruleScope(service.Protocol, port, string(family)), permit))
			}
			continue
		}
		for _, source := range service.Sources {
			permit := TransportPermit{
				InputInterface: iface, Family: familyOf(source), Source: source, Destination: netip.Prefix{},
				Protocol: service.Protocol, SourcePort: 0, DestinationPort: service.Port,
			}
			rules = append(rules, permitRule(PurposeManagementService, ruleScope(service.Protocol, port, source.String()), permit))
		}
	}
	return rules
}

func addressToken(prefix netip.Prefix, source bool) string {
	field := "daddr"
	if source {
		field = "saddr"
	}
	name := "ip6"
	if prefix.Addr().Is4() {
		name = "ip"
	}
	return name + " " + field + " " + prefix.String()
}

// String formats the desired rules for diagnostics.
func (r Ruleset) String() string {
	var lines []string
	for _, chain := range r.Chains {
		lines = append(lines, fmt.Sprintf("%s %s %s: %s/%s/%s policy %s", chain.Table.Family, chain.Table.Name, chain.Name, chain.Type, chain.Hook, chain.Priority, chain.Policy))
		for _, rule := range chain.Rules {
			lines = append(lines, "  "+rule.Expression)
		}
	}
	for _, set := range r.Sets {
		lines = append(lines, fmt.Sprintf("%s %s set %s: %s interval auto-merge", set.Table.Family, set.Table.Name, set.Name, set.KeyType))
	}
	return strings.Join(lines, "\n") + "\n"
}
