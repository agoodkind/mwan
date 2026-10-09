package provider_test

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const (
	firewallBlock = `      "firewall": {
        "management-interface": "enmgmt0",
        "management-service": [
          { "protocol": "tcp", "port": 22 },
          { "protocol": "tcp", "port": 50052, "allowed-source": ["192.0.2.0/24"] }
        ],
        "pinned-provider": "att",
        "pinned-source-v4": "192.0.2.1",
        "pinned-source-port": 51820,
        "pinned-destination-port": 51821,
        "pinned-set-v4-name": "att_pinned_v4",
        "pinned-set-v6-name": "att_pinned_v6",
        "pinned-v4": ["198.51.100.0/24"],
        "pinned-v6": ["2001:db8:100::/48"]
      },
`
	delegatedPrefixRequest = `"goodkind-mwan-steering:dhcp": true, ` +
		`"goodkind-mwan-steering:delegation": { "duid-type": "link-layer-time", ` +
		`"duid": "00:01:00:01:2a:5b:3c:4d:02:00:5e:00:53:01" },`

	natPreroutingChain     = "ip|nat|prerouting"
	manglePostroutingKey   = "inet|mangle|postrouting"
	pinnedSourceRuleKey    = "ip|nat|prerouting|pinned-source-mark|ipv4"
	firstDNATRuleKey       = "ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.2"
	secondDNATRuleKey      = "ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.3"
	saveMarkRuleKey        = "inet|mangle|postrouting|save-mark|all"
	masqueradeRuleKey      = "ip|nat|postrouting|masquerade|enwebpass0"
	forwardOutboundRuleKey = "inet|filter|forward|forward-outbound|enatt0,ipv6"
	classUnmarkRuleKey     = "inet|mangle|prerouting|internal-class-unmark|all"
	webpassSourceRuleKey   = "webpass|ipv6|source"
	webpassExternalPrefix  = "2001:db8:beef:200::/60"

	providerPolicyRuleCount = 6
)

type networkPolicyRule struct {
	ConnectionID         string
	Family               string
	Kind                 string
	Priority             int64
	TableID              int64
	Mark                 *int64
	Source               *string
	SourceKind           string
	ActivationConditions []string
}

type networkFirewallChain struct {
	Family    string
	Table     string
	Chain     string
	RuleOrder []string
}

type networkFirewallRule struct {
	Key             string
	Family          string
	Table           string
	Chain           string
	Purpose         string
	Scope           string
	Action          string
	Expression      string
	Source          *string
	Destination     *string
	Interface       *string
	OutputInterface *string
	Mark            *int64
}

type networkFirewallSet struct {
	Family   string
	Table    string
	Set      string
	KeyType  string
	Elements []string
}

func nullableInteger(t *testing.T, value tftypes.Value, name string) *int64 {
	t.Helper()
	if attribute(t, value, name).IsNull() {
		return nil
	}
	return pointerTo(integer(t, value, name))
}

func decodeNetworkPolicyRules(t *testing.T, state tftypes.Value) map[string]networkPolicyRule {
	t.Helper()
	result := map[string]networkPolicyRule{}
	for key, value := range objectMap(t, state, "policy_rules") {
		result[key] = networkPolicyRule{
			ConnectionID:         text(t, value, "connection_id"),
			Family:               text(t, value, "family"),
			Kind:                 text(t, value, "kind"),
			Priority:             integer(t, value, "priority"),
			TableID:              integer(t, value, "table_id"),
			Mark:                 nullableInteger(t, value, "mark"),
			Source:               nullableText(t, value, "source"),
			SourceKind:           text(t, value, "source_kind"),
			ActivationConditions: stringList(t, elements(t, value, "activation_conditions")),
		}
	}
	return result
}

func decodeNetworkFirewallChains(t *testing.T, state tftypes.Value) map[string]networkFirewallChain {
	t.Helper()
	result := map[string]networkFirewallChain{}
	for key, value := range objectMap(t, state, "firewall_chains") {
		result[key] = networkFirewallChain{
			Family:    text(t, value, "family"),
			Table:     text(t, value, "table"),
			Chain:     text(t, value, "chain"),
			RuleOrder: stringList(t, elements(t, value, "rule_order")),
		}
	}
	return result
}

func decodeNetworkFirewallRules(t *testing.T, state tftypes.Value) map[string]networkFirewallRule {
	t.Helper()
	result := map[string]networkFirewallRule{}
	for key, value := range objectMap(t, state, "firewall_rules") {
		result[key] = networkFirewallRule{
			Key:             text(t, value, "key"),
			Family:          text(t, value, "family"),
			Table:           text(t, value, "table"),
			Chain:           text(t, value, "chain"),
			Purpose:         text(t, value, "purpose"),
			Scope:           text(t, value, "scope"),
			Action:          text(t, value, "action"),
			Expression:      text(t, value, "expression"),
			Source:          nullableText(t, value, "source"),
			Destination:     nullableText(t, value, "destination"),
			Interface:       nullableText(t, value, "interface"),
			OutputInterface: nullableText(t, value, "output_interface"),
			Mark:            nullableInteger(t, value, "mark"),
		}
	}
	return result
}

func decodeNetworkFirewallSets(t *testing.T, state tftypes.Value) map[string]networkFirewallSet {
	t.Helper()
	result := map[string]networkFirewallSet{}
	for key, value := range objectMap(t, state, "firewall_sets") {
		members := stringList(t, elements(t, value, "elements"))
		slices.Sort(members)
		result[key] = networkFirewallSet{
			Family:   text(t, value, "family"),
			Table:    text(t, value, "table"),
			Set:      text(t, value, "set"),
			KeyType:  text(t, value, "key_type"),
			Elements: members,
		}
	}
	return result
}

func fwmarkPolicyRule(connectionID string, family string, priority int64, tableID int64, mark int64) networkPolicyRule {
	return networkPolicyRule{
		ConnectionID: connectionID, Family: family, Kind: "fwmark", Priority: priority, TableID: tableID,
		Mark: pointerTo(mark), Source: nil, SourceKind: "none",
		ActivationConditions: []string{"translation-ready", "default-gateway-discovered", "provider-healthy"},
	}
}

func webpassSourcePolicyRule(source *string, sourceKind string) networkPolicyRule {
	return networkPolicyRule{
		ConnectionID: "webpass", Family: "ipv6", Kind: "source", Priority: 56, TableID: 200,
		Mark: nil, Source: source, SourceKind: sourceKind,
		ActivationConditions: []string{
			"translation-ready", "default-gateway-discovered", "provider-healthy", "translated-source-published",
		},
	}
}

func TestNetworkReturnsRuleMaps(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	wantPolicyRules := map[string]networkPolicyRule{
		"webpass|ipv4|fwmark":      fwmarkPolicyRule("webpass", "ipv4", 200, 200, 2),
		"webpass|ipv6|fwmark":      fwmarkPolicyRule("webpass", "ipv6", 200, 200, 2),
		webpassSourceRuleKey:       webpassSourcePolicyRule(pointerTo(webpassExternalPrefix), "configured"),
		"att|ipv4|fwmark":          fwmarkPolicyRule("att", "ipv4", 100, 100, 1),
		"att|ipv6|fwmark":          fwmarkPolicyRule("att", "ipv6", 100, 100, 1),
		"monkeybrains|ipv4|fwmark": fwmarkPolicyRule("monkeybrains", "ipv4", 300, 300, 3),
	}
	wantChains := []string{
		"inet|filter|forward", "inet|filter|input", "inet|filter|output", manglePostroutingKey,
		"inet|mangle|prerouting", "ip|nat|postrouting", natPreroutingChain,
	}
	wantRules := map[string]networkFirewallRule{
		pinnedSourceRuleKey: {
			Key: pinnedSourceRuleKey, Family: "ip", Table: "nat", Chain: "prerouting",
			Purpose: "pinned-source-mark", Scope: "ipv4", Action: "mark",
			Expression: `iifname "enmwanbr0" ip saddr 192.0.2.1 udp sport 51820 udp dport 51821 ` +
				`ct state new meta mark set 1`,
			Source: pointerTo("192.0.2.1/32"), Destination: nil, Interface: pointerTo("enmwanbr0"),
			OutputInterface: nil, Mark: pointerTo(int64(1)),
		},
		masqueradeRuleKey: {
			Key: masqueradeRuleKey, Family: "ip", Table: "nat", Chain: "postrouting",
			Purpose: "masquerade", Scope: "enwebpass0", Action: "masquerade",
			Expression: `oifname "enwebpass0" ip saddr 192.0.2.0/29 masquerade`,
			Source:     pointerTo("192.0.2.0/29"), Destination: nil, Interface: nil,
			OutputInterface: pointerTo("enwebpass0"), Mark: nil,
		},
		forwardOutboundRuleKey: {
			Key: forwardOutboundRuleKey, Family: "inet", Table: "filter", Chain: "forward",
			Purpose: "forward-outbound", Scope: "enatt0,ipv6", Action: "accept",
			Expression: `iifname "enmwanbr0" oifname "enatt0" meta nfproto ipv6 accept`,
			Source:     nil, Destination: nil, Interface: pointerTo("enmwanbr0"),
			OutputInterface: pointerTo("enatt0"), Mark: nil,
		},
		firstDNATRuleKey: {
			Key: firstDNATRuleKey, Family: "ip", Table: "nat", Chain: "prerouting",
			Purpose: "static-mapping-dnat", Scope: "enwebpass0,203.0.113.2", Action: "dnat",
			Expression: `iifname "enwebpass0" ip daddr 203.0.113.2 dnat to 192.0.2.2`,
			Source:     nil, Destination: pointerTo("203.0.113.2/32"), Interface: pointerTo("enwebpass0"),
			OutputInterface: nil, Mark: nil,
		},
		saveMarkRuleKey: {
			Key: saveMarkRuleKey, Family: "inet", Table: "mangle", Chain: "postrouting",
			Purpose: "save-mark", Scope: "all", Action: "save-mark", Expression: "ct mark set meta mark",
			Source: nil, Destination: nil, Interface: nil, OutputInterface: nil, Mark: nil,
		},
		classUnmarkRuleKey: {
			Key: classUnmarkRuleKey, Family: "inet", Table: "mangle", Chain: "prerouting",
			Purpose: "internal-class-unmark", Scope: "all", Action: "return",
			Expression: `iifname "enmwanbr0" meta priority & 0xffff0000 == 0x4e500000 meta mark set 0 return`,
			Source:     nil, Destination: nil, Interface: pointerTo("enmwanbr0"),
			OutputInterface: nil, Mark: pointerTo(int64(0)),
		},
	}
	wantSets := map[string]networkFirewallSet{
		"inet|mangle|att_pinned_v4": {
			Family: "inet", Table: "mangle", Set: "att_pinned_v4", KeyType: "ipv4_addr",
			Elements: []string{"198.51.100.0/24"},
		},
		"inet|mangle|att_pinned_v6": {
			Family: "inet", Table: "mangle", Set: "att_pinned_v6", KeyType: "ipv6_addr",
			Elements: []string{"2001:db8:100::/48"},
		},
	}
	for _, document := range []string{networkRoutesDocument, gatewayInstances + "network-min.json"} {
		t.Run(document, func(t *testing.T) {
			t.Parallel()
			content, err := os.ReadFile(document)
			if err != nil {
				t.Fatalf("read %s: %v", document, err)
			}
			state := server.mustReadNetwork(t, string(content))

			if got := text(t, state, "guest_type"); got != "qemu" {
				t.Errorf("guest_type = %q, want qemu", got)
			}
			if got := decodeNetworkPolicyRules(t, state); !reflect.DeepEqual(got, wantPolicyRules) {
				t.Errorf("policy_rules:\ngot  %+v\nwant %+v", got, wantPolicyRules)
			}
			chains := decodeNetworkFirewallChains(t, state)
			rules := decodeNetworkFirewallRules(t, state)
			chainKeys := make([]string, 0, len(chains))
			orderedRuleCount := 0
			for key, chain := range chains {
				chainKeys = append(chainKeys, key)
				orderedRuleCount += len(chain.RuleOrder)
				if identity := chain.Family + "|" + chain.Table + "|" + chain.Chain; identity != key {
					t.Errorf("firewall_chains[%s] has the identity %s", key, identity)
				}
				for _, ruleKey := range chain.RuleOrder {
					rule, found := rules[ruleKey]
					if !found {
						t.Errorf("firewall_chains[%s].rule_order lists the absent rule %s", key, ruleKey)
						continue
					}
					if owner := rule.Family + "|" + rule.Table + "|" + rule.Chain; owner != key {
						t.Errorf("firewall_chains[%s].rule_order lists %s from the chain %s", key, ruleKey, owner)
					}
				}
			}
			slices.Sort(chainKeys)
			if !slices.Equal(chainKeys, wantChains) {
				t.Errorf("firewall_chains keys = %v, want %v", chainKeys, wantChains)
			}
			if orderedRuleCount != len(rules) {
				t.Errorf("rule_order lists %d rules, want the %d firewall_rules entries", orderedRuleCount, len(rules))
			}
			wantNATOrder := []string{pinnedSourceRuleKey, firstDNATRuleKey, secondDNATRuleKey}
			if got := chains[natPreroutingChain].RuleOrder; !slices.Equal(got, wantNATOrder) {
				t.Errorf("%s rule_order = %v, want %v", natPreroutingChain, got, wantNATOrder)
			}
			if got := chains[manglePostroutingKey].RuleOrder; !slices.Equal(got, []string{saveMarkRuleKey}) {
				t.Errorf("%s rule_order = %v, want [%s]", manglePostroutingKey, got, saveMarkRuleKey)
			}
			for key, rule := range rules {
				if rule.Key != key {
					t.Errorf("firewall_rules[%s].key = %s", key, rule.Key)
				}
			}
			for key, want := range wantRules {
				if got := rules[key]; !reflect.DeepEqual(got, want) {
					t.Errorf("firewall_rules[%s]:\ngot  %+v\nwant %+v", key, got, want)
				}
			}
			if got := decodeNetworkFirewallSets(t, state); !reflect.DeepEqual(got, wantSets) {
				t.Errorf("firewall_sets:\ngot  %+v\nwant %+v", got, wantSets)
			}
			if diagnostics := server.validateConfig(t, server.networkConfig(t, state)); len(diagnostics) > 0 {
				t.Errorf("the resource rejects the data source maps: %s", diagnosticText(diagnostics))
			}
		})
	}
}

func TestNetworkReturnsNullForRuntimeSource(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	document := readNetworkDocument(t,
		`"goodkind-mwan-steering:dhcp": false,`, delegatedPrefixRequest,
		`"external-source": "configured",`, `"external-source": "delegated"`,
		`"external-prefix": "`+webpassExternalPrefix+`"`, ``,
	)

	rules := decodeNetworkPolicyRules(t, server.mustReadNetwork(t, document))

	want := webpassSourcePolicyRule(nil, "runtime")
	if got := rules[webpassSourceRuleKey]; !reflect.DeepEqual(got, want) {
		t.Errorf("policy_rules[%s]:\ngot  %+v\nwant %+v", webpassSourceRuleKey, got, want)
	}
}

func TestNetworkReturnsEmptyFirewallMapsWithoutFirewall(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)

	state := server.mustReadNetwork(t, readNetworkDocument(t, firewallBlock, ``))

	for _, name := range []string{"firewall_chains", "firewall_rules", "firewall_sets"} {
		found := attribute(t, state, name)
		if found.IsNull() || !found.IsKnown() {
			t.Errorf("%s = %s, want a known empty map", name, found)
			continue
		}
		if entries := objectMap(t, state, name); len(entries) != 0 {
			t.Errorf("%s has %d entries, want none", name, len(entries))
		}
	}
	if got := len(objectMap(t, state, "policy_rules")); got != providerPolicyRuleCount {
		t.Errorf("policy_rules has %d entries, want %d", got, providerPolicyRuleCount)
	}
}

func replaceRuleOrder(t *testing.T, config tftypes.Value, chainKey string, order []string) tftypes.Value {
	t.Helper()
	return replaceEntries(t, config, "firewall_chains", func(chains map[string]tftypes.Value) {
		listType := attribute(t, chains[chainKey], "rule_order").Type()
		keys := make([]tftypes.Value, 0, len(order))
		for _, key := range order {
			keys = append(keys, tftypes.NewValue(tftypes.String, key))
		}
		chains[chainKey] = replaceAttribute(t, chains[chainKey], "rule_order", tftypes.NewValue(listType, keys))
	})
}

func moveEntry(entries map[string]tftypes.Value, from string, to string) {
	entries[to] = entries[from]
	delete(entries, from)
}

func TestNetworkConfigValidatesRuleMaps(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	config := server.networkConfig(t, server.mustReadNetwork(t, readNetworkDocument(t)))
	unknown := func(name string) tftypes.Value {
		return replaceAttribute(t, config, name,
			tftypes.NewValue(attribute(t, config, name).Type(), tftypes.UnknownValue))
	}
	omitted := config
	for _, name := range networkRuleMapNames() {
		omitted = replaceAttribute(t, omitted, name, tftypes.NewValue(attribute(t, config, name).Type(), nil))
	}
	number := func(value int64) tftypes.Value {
		return tftypes.NewValue(tftypes.Number, value)
	}
	const (
		policyKey = "att|ipv4|fwmark"
		setKey    = "inet|mangle|att_pinned_v4"
	)

	cases := []struct {
		name   string
		config tftypes.Value
		want   string
	}{
		{name: "omitted rule maps", config: omitted, want: ""},
		{name: "unknown firewall rules", config: unknown("firewall_rules"), want: ""},
		{name: "unknown firewall chains", config: unknown("firewall_chains"), want: ""},
		{name: "unknown policy rules", config: unknown("policy_rules"), want: ""},
		{
			name: "mismatched policy key",
			config: replaceEntries(t, config, "policy_rules", func(rules map[string]tftypes.Value) {
				moveEntry(rules, policyKey, "att|ipv4|source")
			}),
			want: `The key "att|ipv4|source" must equal "` + policyKey + `"`,
		},
		{
			name: "invalid policy source kind",
			config: replaceEntries(t, config, "policy_rules", func(rules map[string]tftypes.Value) {
				rules[policyKey] = replaceAttribute(t, rules[policyKey], "source_kind",
					tftypes.NewValue(tftypes.String, "learned"))
			}),
			want: `The value "learned" must be one of ["none" "configured" "runtime"]`,
		},
		{
			name: "policy priority outside uint32",
			config: replaceEntries(t, config, "policy_rules", func(rules map[string]tftypes.Value) {
				rules[policyKey] = replaceAttribute(t, rules[policyKey], "priority", number(4294967296))
			}),
			want: "The value 4294967296 must be between 0 and 4294967295.",
		},
		{
			name: "mismatched firewall chain key",
			config: replaceEntries(t, config, "firewall_chains", func(chains map[string]tftypes.Value) {
				moveEntry(chains, "inet|filter|output", "inet|filter|egress")
			}),
			want: `The key "inet|filter|egress" must equal "inet|filter|output"`,
		},
		{
			name: "mismatched firewall rule key",
			config: replaceEntries(t, config, "firewall_rules", func(rules map[string]tftypes.Value) {
				moveEntry(rules, saveMarkRuleKey, "inet|mangle|postrouting|save-mark|other")
			}),
			want: `The key "inet|mangle|postrouting|save-mark|other" must equal "` + saveMarkRuleKey + `"`,
		},
		{
			name: "mismatched firewall rule key attribute",
			config: replaceEntries(t, config, "firewall_rules", func(rules map[string]tftypes.Value) {
				rules[saveMarkRuleKey] = replaceAttribute(t, rules[saveMarkRuleKey], "key",
					tftypes.NewValue(tftypes.String, firstDNATRuleKey))
			}),
			want: `The key "` + saveMarkRuleKey + `" must equal "` + firstDNATRuleKey + `"`,
		},
		{
			name: "invalid firewall action",
			config: replaceEntries(t, config, "firewall_rules", func(rules map[string]tftypes.Value) {
				rules[saveMarkRuleKey] = replaceAttribute(t, rules[saveMarkRuleKey], "action",
					tftypes.NewValue(tftypes.String, "reject"))
			}),
			want: `The value "reject" must be one of`,
		},
		{
			name: "firewall mark outside uint32",
			config: replaceEntries(t, config, "firewall_rules", func(rules map[string]tftypes.Value) {
				rules[pinnedSourceRuleKey] = replaceAttribute(t, rules[pinnedSourceRuleKey], "mark", number(-1))
			}),
			want: "The value -1 must be between 0 and 4294967295.",
		},
		{
			name: "mismatched firewall set key",
			config: replaceEntries(t, config, "firewall_sets", func(sets map[string]tftypes.Value) {
				moveEntry(sets, setKey, "inet|mangle|renamed")
			}),
			want: `The key "inet|mangle|renamed" must equal "` + setKey + `"`,
		},
		{
			name:   "rule missing from the chain order",
			config: replaceRuleOrder(t, config, natPreroutingChain, []string{pinnedSourceRuleKey, firstDNATRuleKey}),
			want: `The rule_order list of the chain "` + natPreroutingChain + `" must contain the rule key "` +
				secondDNATRuleKey + `".`,
		},
		{
			name: "duplicated chain order entry",
			config: replaceRuleOrder(t, config, natPreroutingChain,
				[]string{pinnedSourceRuleKey, firstDNATRuleKey, secondDNATRuleKey, firstDNATRuleKey}),
			want: `The chain "` + natPreroutingChain + `" lists the rule key "` + firstDNATRuleKey + `" more than once.`,
		},
		{
			name: "chain order entry without a rule",
			config: replaceRuleOrder(t, config, manglePostroutingKey,
				[]string{saveMarkRuleKey, "inet|mangle|postrouting|save-mark|absent"}),
			want: `The rule key "inet|mangle|postrouting|save-mark|absent" has no firewall_rules entry.`,
		},
		{
			name: "chain order entry from another chain",
			config: replaceRuleOrder(t, config, manglePostroutingKey,
				[]string{saveMarkRuleKey, firstDNATRuleKey}),
			want: `The rule key "` + firstDNATRuleKey + `" belongs to the chain "` + natPreroutingChain +
				`", not "` + manglePostroutingKey + `".`,
		},
		{
			name: "rule without a chain",
			config: replaceEntries(t, config, "firewall_chains", func(chains map[string]tftypes.Value) {
				delete(chains, manglePostroutingKey)
			}),
			want: `The rule_order list of the chain "` + manglePostroutingKey + `" must contain the rule key "` +
				saveMarkRuleKey + `".`,
		},
	}
	for _, test := range cases {
		diagnostics := server.validateConfig(t, test.config)
		if test.want == "" {
			if len(diagnostics) > 0 {
				t.Errorf("%s: diagnostics = %q, want none", test.name, diagnosticText(diagnostics))
			}
			continue
		}
		if !strings.Contains(diagnosticText(diagnostics), test.want) {
			t.Errorf("%s: diagnostics = %q, want %q", test.name, diagnosticText(diagnostics), test.want)
		}
	}
}

func TestNetworkConfigReordersRulesInPlace(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	created := server.networkConfig(t, server.mustReadNetwork(t, readNetworkDocument(t)))
	reordered := replaceRuleOrder(t, created, natPreroutingChain,
		[]string{secondDNATRuleKey, firstDNATRuleKey, pinnedSourceRuleKey})
	if diagnostics := server.validateConfig(t, reordered); len(diagnostics) > 0 {
		t.Fatalf("validate the reordered chain: %s", diagnosticText(diagnostics))
	}

	state := server.planAndApply(t, tftypes.NewValue(server.resource.ValueType(), nil), created)
	state = server.planAndApply(t, state, reordered)

	if got, want := attribute(t, state, "firewall_rules"), attribute(t, created, "firewall_rules"); !got.Equal(want) {
		t.Errorf("firewall_rules changed after a reorder:\ngot  %s\nwant %s", got, want)
	}
	wantOrder := []string{secondDNATRuleKey, firstDNATRuleKey, pinnedSourceRuleKey}
	if got := decodeNetworkFirewallChains(t, state)[natPreroutingChain].RuleOrder; !slices.Equal(got, wantOrder) {
		t.Errorf("%s rule_order = %v, want %v", natPreroutingChain, got, wantOrder)
	}
}
