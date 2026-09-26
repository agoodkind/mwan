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
	Rules    []string
}

// Set defines one destination set refreshed independently after startup.
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
	input.Rules = append(input.Rules, "ct state established,related accept", `iifname "lo" accept`, "ip protocol icmp accept", "ip6 nexthdr icmpv6 accept")
	input.Rules = append(input.Rules, permitRules(managementPermits(config.ManagementInterface, config.ManagementServices))...)
	input.Rules = append(input.Rules, permitRules(config.LocalPermits)...)
	input.Rules = append(input.Rules, `limit rate 1/second log prefix "nftables input drop: " drop`)
	forward.Rules = []string{`ct state established,related accept`, `limit rate 1/second log prefix "nftables forward drop: " drop`}
	return Ruleset{Chains: []Chain{input, forward, output}, Sets: nil}, nil
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

	input.Rules = append(input.Rules, "ct state established,related accept", `iifname "lo" accept`, "ip protocol icmp accept", "ip6 nexthdr icmpv6 accept")
	input.Rules = append(input.Rules, permitRules(managementPermits(config.ManagementInterface, config.ManagementServices))...)
	input.Rules = append(input.Rules, permitRules(config.LocalPermits)...)
	input.Rules = append(input.Rules, `limit rate 1/second log prefix "nftables input drop: " drop`)

	forward.Rules = append(forward.Rules, "ct state established,related accept")
	forward.Rules = append(forward.Rules, "iifname "+strconv.Quote(config.InternalInterface)+" oifname "+strconv.Quote(config.InternalInterface)+" meta priority & 0xffff0000 == 0x4e500000 accept")
	for _, path := range config.Paths {
		for _, family := range []struct {
			active bool
			name   string
		}{{path.IPv4, "ipv4"}, {path.IPv6, "ipv6"}} {
			if !family.active {
				continue
			}
			forward.Rules = append(forward.Rules,
				"iifname "+strconv.Quote(path.InternalInterface)+" oifname "+strconv.Quote(path.ExternalInterface)+" meta nfproto "+family.name+" accept",
				"iifname "+strconv.Quote(path.ExternalInterface)+" oifname "+strconv.Quote(path.InternalInterface)+" meta nfproto "+family.name+" accept")
		}
	}
	forward.Rules = append(forward.Rules, `limit rate 1/second log prefix "nftables forward drop: " drop`)

	pinMark := uint32(0)
	for _, provider := range config.Providers {
		if provider.Interface == config.PinnedProvider {
			pinMark = provider.Mark
		}
	}
	if config.PinnedProvider != "" {
		ports := "udp sport " + strconv.Itoa(int(config.PinnedSourcePort)) + " udp dport " + strconv.Itoa(int(config.PinnedDestinationPort))
		preNAT.Rules = append(preNAT.Rules, "iifname "+strconv.Quote(config.InternalInterface)+" ip saddr "+config.PinnedSourceIPv4.String()+" "+ports+" ct state new meta mark set "+strconv.FormatUint(uint64(pinMark), 10))
	}
	preMangle.Rules = append(preMangle.Rules, "iifname "+strconv.Quote(config.InternalInterface)+" meta priority & 0xffff0000 == 0x4e500000 meta mark set 0 return")
	for _, provider := range config.Providers {
		iface := strconv.Quote(provider.Interface)
		mark := strconv.FormatUint(uint64(provider.Mark), 10)
		for _, mapping := range provider.StaticMappings {
			preNAT.Rules = append(preNAT.Rules, "iifname "+iface+" ip daddr "+mapping.External.String()+" dnat to "+mapping.Internal.String())
			postNAT.Rules = append(postNAT.Rules, "oifname "+iface+" ip saddr "+mapping.Internal.String()+" snat to "+mapping.External.String())
		}
		preMangle.Rules = append(preMangle.Rules, "iifname "+iface+" ct state new meta mark set "+mark)
	}
	for _, provider := range config.Providers {
		if provider.MasqueradeIPv4 {
			postNAT.Rules = append(postNAT.Rules, "oifname "+strconv.Quote(provider.Interface)+" ip saddr "+config.InternalNetworkIPv4.String()+" masquerade")
		}
	}
	if config.PinnedProvider != "" {
		mark := strconv.FormatUint(uint64(pinMark), 10)
		preMangle.Rules = append(preMangle.Rules,
			"ip daddr @"+config.PinnedSetV4Name+" meta mark set "+mark,
			"ip6 daddr @"+config.PinnedSetV6Name+" meta mark set "+mark,
			"ip6 saddr "+config.PinnedSourceIPv6.String()+"/128 udp sport "+strconv.Itoa(int(config.PinnedSourcePort))+" udp dport "+strconv.Itoa(int(config.PinnedDestinationPort))+" ct state new meta mark set "+mark)
	}
	for _, provider := range config.Providers {
		if provider.ForcedDSCP == 0 {
			continue
		}
		iface := strconv.Quote(config.InternalInterface)
		dscp := strconv.Itoa(int(provider.ForcedDSCP))
		mark := strconv.FormatUint(uint64(provider.Mark), 10)
		preMangle.Rules = append(preMangle.Rules,
			"iifname "+iface+" ip dscp "+dscp+" ct state new meta mark set "+mark,
			"iifname "+iface+" ip6 dscp "+dscp+" ct state new meta mark set "+mark)
	}
	preMangle.Rules = append(preMangle.Rules, "ct state established,related meta mark set ct mark")
	postMangle.Rules = append(postMangle.Rules, "ct mark set meta mark")

	rules := Ruleset{Chains: []Chain{input, forward, output, preNAT, postNAT, preMangle, postMangle}, Sets: nil}
	if config.PinnedProvider != "" {
		rules.Sets = []Set{
			{Table: mangleTable(), Name: config.PinnedSetV4Name, KeyType: "ipv4_addr", Elements: config.PinnedIPv4},
			{Table: mangleTable(), Name: config.PinnedSetV6Name, KeyType: "ipv6_addr", Elements: config.PinnedIPv6},
		}
	}
	return rules, nil
}

func permitRules(permits []TransportPermit) []string {
	rules := make([]string, 0, len(permits))
	for _, permit := range permits {
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
		rules = append(rules, line+" accept")
	}
	return rules
}

func managementPermits(iface string, services []Service) []TransportPermit {
	var permits []TransportPermit
	for _, service := range services {
		if len(service.Sources) == 0 {
			permits = append(permits,
				TransportPermit{InputInterface: iface, Family: IPv4, Source: netip.Prefix{}, Destination: netip.Prefix{}, Protocol: service.Protocol, SourcePort: 0, DestinationPort: service.Port},
				TransportPermit{InputInterface: iface, Family: IPv6, Source: netip.Prefix{}, Destination: netip.Prefix{}, Protocol: service.Protocol, SourcePort: 0, DestinationPort: service.Port})
			continue
		}
		for _, source := range service.Sources {
			permits = append(permits, TransportPermit{
				InputInterface: iface, Family: familyOf(source), Source: source, Destination: netip.Prefix{},
				Protocol: service.Protocol, SourcePort: 0, DestinationPort: service.Port,
			})
		}
	}
	return permits
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
			lines = append(lines, "  "+rule)
		}
	}
	for _, set := range r.Sets {
		lines = append(lines, fmt.Sprintf("%s %s set %s: %s interval auto-merge", set.Table.Family, set.Table.Name, set.Name, set.KeyType))
	}
	return strings.Join(lines, "\n") + "\n"
}
