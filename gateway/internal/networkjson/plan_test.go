package networkjson_test

import (
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	planBaseDocument = "testdata/plan-base.json"

	planManagementLink = `{ "name": "enmgmt0", "type": "iana-if-type:other" }`
	planManagementIPv4 = `"ietf-ip:ipv4": { "goodkind-mwan-steering:gateway": "192.0.2.1", ` +
		`"goodkind-mwan-steering:route-metric": 10, "goodkind-mwan-steering:route": [` +
		`{ "destination": "198.18.2.0/24", "gateway": "192.0.2.3", "metric": 50 }] }`
)

type planEdit struct {
	old         string
	replacement string
}

var planWebpassEdits = []planEdit{
	{
		old: `"goodkind-mwan-steering:translation": {` + "\n" + `            "mode": "ietf-nat:napt44",`,
		replacement: `"goodkind-mwan-steering:route-metric": 20, "goodkind-mwan-steering:route": [` +
			`{ "destination": "198.18.0.0/24", "gateway": "203.0.113.1" }, { "destination": "198.18.1.0/24" }], ` +
			`"goodkind-mwan-steering:translation": {` + "\n" + `            "mode": "ietf-nat:napt44",`,
	},
	{
		old: `"goodkind-mwan-steering:dhcp": false,`,
		replacement: `"goodkind-mwan-steering:dhcp": false, "goodkind-mwan-steering:gateway": "2001:db8:beef:200::1", ` +
			`"goodkind-mwan-steering:route": [` +
			`{ "destination": "2001:DB8:A00::/48", "gateway": "2001:db8:beef:200::1", "metric": 2048 }],`,
	},
}

func planDocument(t *testing.T, edits ...planEdit) []byte {
	t.Helper()
	data, err := os.ReadFile(planBaseDocument)
	if err != nil {
		t.Fatalf("read %s: %v", planBaseDocument, err)
	}
	document := string(data)
	for _, edit := range edits {
		if count := strings.Count(document, edit.old); count != 1 {
			t.Fatalf("edit target %q occurs %d times, want 1", edit.old, count)
		}
		document = strings.Replace(document, edit.old, edit.replacement, 1)
	}
	return []byte(document)
}

func decodePlanDocument(t *testing.T, edits ...planEdit) *networkjson.Config {
	t.Helper()
	loaded, err := networkjson.Decode(planDocument(t, edits...))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(loaded.Rejected) != 0 {
		t.Fatalf("Decode rejected %+v", loaded.Rejected)
	}
	return loaded
}

func configuredRoute(destination string, gateway string, metric uint32, source networkjson.RouteSource) networkjson.ConfiguredRoute {
	address := netip.Addr{}
	if gateway != "" {
		address = netip.MustParseAddr(gateway)
	}
	return networkjson.ConfiguredRoute{
		RouteIntent: interfaceintent.RouteIntent{
			Destination: netip.MustParsePrefix(destination),
			Gateway:     address,
			TableID:     254,
			Metric:      metric,
		},
		Source: source,
	}
}

func routeKey(iface string, family networkjson.Family, destination string) networkjson.RouteKey {
	return networkjson.RouteKey{Interface: iface, Family: family, Destination: netip.MustParsePrefix(destination)}
}

func TestConfiguredRoutesProjectEveryOwner(t *testing.T) {
	owners := []struct {
		name string
		link string
	}{
		{name: "external", link: `{ "name": "enmgmt0", "type": "iana-if-type:other", `},
		{
			name: "networkd",
			link: `{ "name": "enmgmt0", "type": "iana-if-type:other", ` +
				`"goodkind-mwan-steering:link-files": "rendered", ` +
				`"goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:78" } }, `,
		},
		{
			name: "mwan",
			link: `{ "name": "enmgmt0", "type": "iana-if-type:other", ` +
				`"goodkind-mwan-steering:connection-id": "management", "goodkind-mwan-steering:owner": "mwan", ` +
				`"goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:78" } }, `,
		},
	}
	want := map[networkjson.RouteKey]networkjson.ConfiguredRoute{
		routeKey("enmgmt0", networkjson.FamilyIPv4, "0.0.0.0/0"): configuredRoute(
			"0.0.0.0/0", "192.0.2.1", 10, networkjson.RouteSourceGateway),
		routeKey("enmgmt0", networkjson.FamilyIPv4, "198.18.2.0/24"): configuredRoute(
			"198.18.2.0/24", "192.0.2.3", 50, networkjson.RouteSourceRoute),
		routeKey("enwebpass0", networkjson.FamilyIPv4, "198.18.0.0/24"): configuredRoute(
			"198.18.0.0/24", "203.0.113.1", 0, networkjson.RouteSourceRoute),
		routeKey("enwebpass0", networkjson.FamilyIPv4, "198.18.1.0/24"): configuredRoute(
			"198.18.1.0/24", "", 0, networkjson.RouteSourceRoute),
		routeKey("enwebpass0", networkjson.FamilyIPv6, "::/0"): configuredRoute(
			"::/0", "2001:db8:beef:200::1", 0, networkjson.RouteSourceGateway),
		routeKey("enwebpass0", networkjson.FamilyIPv6, "2001:db8:a00::/48"): configuredRoute(
			"2001:db8:a00::/48", "2001:db8:beef:200::1", 2048, networkjson.RouteSourceRoute),
	}
	wantKeys := []string{
		"enmgmt0|ipv4|0.0.0.0/0",
		"enmgmt0|ipv4|198.18.2.0/24",
		"enwebpass0|ipv4|198.18.0.0/24",
		"enwebpass0|ipv4|198.18.1.0/24",
		"enwebpass0|ipv6|2001:db8:a00::/48",
		"enwebpass0|ipv6|::/0",
	}
	for _, owner := range owners {
		t.Run(owner.name, func(t *testing.T) {
			management := planEdit{old: planManagementLink, replacement: owner.link + planManagementIPv4 + " }"}
			loaded := decodePlanDocument(t, append(slices.Clone(planWebpassEdits), management)...)
			routes := loaded.ConfiguredRoutes()
			if !maps.Equal(routes, want) {
				t.Fatalf("ConfiguredRoutes:\ngot  %+v\nwant %+v", routes, want)
			}
			keys := make([]string, 0, len(routes))
			for key := range routes {
				keys = append(keys, key.String())
			}
			slices.Sort(keys)
			if !slices.Equal(keys, wantKeys) {
				t.Fatalf("route keys = %q, want %q", keys, wantKeys)
			}
		})
	}
}

func TestConfiguredRoutesRejectGatewayWithDefaultRouteEntry(t *testing.T) {
	management := planEdit{
		old: planManagementLink,
		replacement: `{ "name": "enmgmt0", "type": "iana-if-type:other", "ietf-ip:ipv4": { ` +
			`"goodkind-mwan-steering:gateway": "192.0.2.1", ` +
			`"goodkind-mwan-steering:route": [{ "destination": "0.0.0.0/0", "gateway": "192.0.2.3" }] } }`,
	}
	_, err := networkjson.Decode(planDocument(t, management))
	want := "interface enmgmt0: ipv4 route destination 0.0.0.0/0 is configured twice"
	if err == nil || err.Error() != want {
		t.Fatalf("Decode error = %v, want %q", err, want)
	}
}

func providerDefault(iface string, family networkjson.Family, tableID uint32, destination string) networkjson.ProviderDefault {
	return networkjson.ProviderDefault{
		Interface:           iface,
		Family:              family,
		TableID:             tableID,
		InternalDestination: netip.MustParsePrefix(destination),
		InternalInterface:   "enmwanbr0",
	}
}

func requireProviderDefaults(t *testing.T, loaded *networkjson.Config, want map[string]networkjson.ProviderDefault) {
	t.Helper()
	defaults, err := loaded.ProviderDefaults()
	if err != nil {
		t.Fatalf("ProviderDefaults: %v", err)
	}
	got := make(map[string]networkjson.ProviderDefault, len(defaults))
	for key, value := range defaults {
		if key.Family != value.Family || key.TableID != value.TableID {
			t.Fatalf("key %s disagrees with value %+v", key, value)
		}
		got[key.String()] = value
	}
	if !maps.Equal(got, want) {
		t.Fatalf("ProviderDefaults:\ngot  %+v\nwant %+v", got, want)
	}
}

func baseProviderDefaults() map[string]networkjson.ProviderDefault {
	return map[string]networkjson.ProviderDefault{
		"webpass|ipv4|200":      providerDefault("enwebpass0", networkjson.FamilyIPv4, 200, "192.0.2.0/29"),
		"webpass|ipv6|200":      providerDefault("enwebpass0", networkjson.FamilyIPv6, 200, "2001:db8:b01:fe::2/128"),
		"att|ipv4|100":          providerDefault("enatt0", networkjson.FamilyIPv4, 100, "192.0.2.0/29"),
		"att|ipv6|100":          providerDefault("enatt0", networkjson.FamilyIPv6, 100, "2001:db8:b01:fe::2/128"),
		"monkeybrains|ipv4|300": providerDefault("enmbrains0", networkjson.FamilyIPv4, 300, "192.0.2.0/29"),
	}
}

func TestProviderDefaultsProjectTranslatedFamilies(t *testing.T) {
	requireProviderDefaults(t, decodePlanDocument(t), baseProviderDefaults())
}

func TestProviderDefaultsIgnoreMainTableGatewayAndMetric(t *testing.T) {
	requireProviderDefaults(t, decodePlanDocument(t, planWebpassEdits...), baseProviderDefaults())
}

func TestProviderDefaultsOmitRejectedProviders(t *testing.T) {
	rejectMonkeybrains := planEdit{old: `"from-prio": 57,`, replacement: ``}
	loaded, err := networkjson.Decode(planDocument(t, rejectMonkeybrains))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(loaded.Rejected) != 1 || loaded.Rejected[0].Interface != "enmbrains0" {
		t.Fatalf("Rejected = %+v, want enmbrains0", loaded.Rejected)
	}
	want := baseProviderDefaults()
	delete(want, "monkeybrains|ipv4|300")
	requireProviderDefaults(t, loaded, want)
}

func TestProviderDefaultsRejectTableIDOutsideUint32(t *testing.T) {
	for _, tableID := range []string{"-1", "4294967296"} {
		loaded := decodePlanDocument(t, planEdit{old: `"table-id": 200,`, replacement: `"table-id": ` + tableID + `,`})
		defaults, err := loaded.ProviderDefaults()
		want := "wan webpass: table-id " + tableID + " is outside 0 to 4294967295"
		if err == nil || err.Error() != want {
			t.Fatalf("ProviderDefaults = %+v, %v; want error %q", defaults, err, want)
		}
	}
}

const (
	planWebpassExternalPrefix = "2001:db8:beef:200::/60"
	planInputChain            = "inet|filter|input"
	planNATPrerouting         = "ip|nat|prerouting"
	planManglePrerouting      = "inet|mangle|prerouting"
	planInstanceDir           = "../../yang/instances"
)

func planFamilyConditions(extra ...interfaceintent.PolicyCondition) []interfaceintent.PolicyCondition {
	conditions := []interfaceintent.PolicyCondition{"translation-ready", "default-gateway-discovered", "provider-healthy"}
	return append(conditions, extra...)
}

func fwmarkPolicyRule(id string, family string, priority int, tableID int, mark uint32) interfaceintent.PolicyRule {
	return interfaceintent.PolicyRule{
		ConnectionID: connectionid.ID(id), Family: family, Kind: "fwmark", Priority: priority, TableID: tableID, Mark: mark,
		Source: netip.Prefix{}, SourceKind: "none", ActivationConditions: planFamilyConditions(),
	}
}

func sourcePolicyRule(
	id string, family string, priority int, tableID int, source netip.Prefix,
	kind interfaceintent.PolicySourceKind, conditions []interfaceintent.PolicyCondition,
) interfaceintent.PolicyRule {
	return interfaceintent.PolicyRule{
		ConnectionID: connectionid.ID(id), Family: family, Kind: "source", Priority: priority, TableID: tableID, Mark: 0,
		Source: source, SourceKind: kind, ActivationConditions: conditions,
	}
}

func basePolicyRules() map[string]interfaceintent.PolicyRule {
	return map[string]interfaceintent.PolicyRule{
		"webpass|ipv4|fwmark": fwmarkPolicyRule("webpass", "ipv4", 200, 200, 2),
		"webpass|ipv6|fwmark": fwmarkPolicyRule("webpass", "ipv6", 200, 200, 2),
		"webpass|ipv6|source": sourcePolicyRule("webpass", "ipv6", 56, 200, netip.MustParsePrefix(planWebpassExternalPrefix),
			"configured", planFamilyConditions("translated-source-published")),
		"att|ipv4|fwmark":          fwmarkPolicyRule("att", "ipv4", 100, 100, 1),
		"att|ipv6|fwmark":          fwmarkPolicyRule("att", "ipv6", 100, 100, 1),
		"monkeybrains|ipv4|fwmark": fwmarkPolicyRule("monkeybrains", "ipv4", 300, 300, 3),
	}
}

func projectedPolicyRules(t *testing.T, loaded *networkjson.Config) map[string]interfaceintent.PolicyRule {
	t.Helper()
	rules, err := loaded.PolicyRules()
	if err != nil {
		t.Fatalf("PolicyRules: %v", err)
	}
	projected := make(map[string]interfaceintent.PolicyRule, len(rules))
	for key, rule := range rules {
		if key.ConnectionID != rule.ConnectionID || string(key.Family) != rule.Family || key.Kind != rule.Kind {
			t.Fatalf("key %s disagrees with rule %+v", key, rule)
		}
		projected[key.String()] = rule
	}
	return projected
}

func requirePolicyRules(t *testing.T, loaded *networkjson.Config, want map[string]interfaceintent.PolicyRule) {
	t.Helper()
	got := projectedPolicyRules(t, loaded)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PolicyRules:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestPolicyRulesProjectConfiguredProviders(t *testing.T) {
	requirePolicyRules(t, decodePlanDocument(t), basePolicyRules())
}

func TestPolicyRulesUpdateMutableFieldsUnderTheSameKeys(t *testing.T) {
	loaded := decodePlanDocument(t,
		planEdit{old: `"fw-mark-prio": 200,`, replacement: `"fw-mark-prio": 210,`},
		planEdit{old: `"from-prio": 56,`, replacement: `"from-prio": 66,`},
		planEdit{old: `"table-id": 200,`, replacement: `"table-id": 220,`},
		planEdit{old: `"fw-mark": 2,`, replacement: `"fw-mark": 7,`},
		planEdit{old: `"external-prefix": "` + planWebpassExternalPrefix + `"`, replacement: `"external-prefix": "2001:db8:beef:300::/60"`},
	)
	want := basePolicyRules()
	want["webpass|ipv4|fwmark"] = fwmarkPolicyRule("webpass", "ipv4", 210, 220, 7)
	want["webpass|ipv6|fwmark"] = fwmarkPolicyRule("webpass", "ipv6", 210, 220, 7)
	want["webpass|ipv6|source"] = sourcePolicyRule("webpass", "ipv6", 66, 220, netip.MustParsePrefix("2001:db8:beef:300::/60"),
		"configured", planFamilyConditions("translated-source-published"))
	requirePolicyRules(t, loaded, want)
}

func TestPolicyRulesAddAndRemoveRules(t *testing.T) {
	staticLink := planEdit{
		old: `"goodkind-mwan-steering:dhcp": true,` + "\n" + `          "goodkind-mwan-steering:translation": {` + "\n",
		replacement: `"address": [{ "ip": "203.0.113.2", "prefix-length": 29 }], "goodkind-mwan-steering:dhcp": false, ` +
			`"goodkind-mwan-steering:gateway": "203.0.113.1",` + "\n" + `          "goodkind-mwan-steering:translation": {` + "\n",
	}
	withSource := basePolicyRules()
	withSource["webpass|ipv4|source"] = sourcePolicyRule("webpass", "ipv4", 56, 200, netip.MustParsePrefix("203.0.113.2/32"),
		"configured", planFamilyConditions())
	requirePolicyRules(t, decodePlanDocument(t, staticLink), withSource)

	rejected, err := networkjson.Decode(planDocument(t, planEdit{old: `"from-prio": 57,`, replacement: ``}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	withoutMonkeybrains := basePolicyRules()
	delete(withoutMonkeybrains, "monkeybrains|ipv4|fwmark")
	requirePolicyRules(t, rejected, withoutMonkeybrains)
}

func TestPolicyRulesRekeyOnConnectionIdentity(t *testing.T) {
	loaded := decodePlanDocument(t, planEdit{
		old:         `"name": "enmbrains0",`,
		replacement: `"name": "enmbrains0", "goodkind-mwan-steering:connection-id": "up|link%1",`,
	})
	want := basePolicyRules()
	delete(want, "monkeybrains|ipv4|fwmark")
	want["up%7Clink%251|ipv4|fwmark"] = fwmarkPolicyRule("up|link%1", "ipv4", 300, 300, 3)
	requirePolicyRules(t, loaded, want)
}

func TestPolicyRulesLeaveDelegatedSourceUnresolved(t *testing.T) {
	loaded := decodePlanDocument(t,
		planEdit{
			old: `"goodkind-mwan-steering:dhcp": false,`,
			replacement: `"goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:delegation": ` +
				`{ "hint": "::/56", "duid-type": "link-layer-time", "duid": "00:01:2a:5b:3c:4d:02:00:5e:00:53:01" },`,
		},
		planEdit{old: `"external-source": "configured",`, replacement: `"external-source": "delegated",`},
		planEdit{old: `"external-prefix": "` + planWebpassExternalPrefix + `"`, replacement: `"expected-prefix": "` + planWebpassExternalPrefix + `"`},
	)
	want := basePolicyRules()
	want["webpass|ipv6|source"] = sourcePolicyRule("webpass", "ipv6", 56, 200, netip.Prefix{},
		"runtime", planFamilyConditions("translated-source-published"))
	requirePolicyRules(t, loaded, want)
}

func TestPolicyRulesMaskConfiguredExternalPrefix(t *testing.T) {
	loaded := decodePlanDocument(t)
	loaded.WAN["webpass"].TranslationV6.NPT.ExternalPrefix = netip.MustParsePrefix("2001:db8:beef:200::7/60")
	requirePolicyRules(t, loaded, basePolicyRules())
}

func TestPolicyRulesClassifySourceByExternalSource(t *testing.T) {
	loaded := decodePlanDocument(t)
	loaded.WAN["webpass"].TranslationV6.NPT.ExternalSource = config.PrefixDelegated
	want := basePolicyRules()
	want["webpass|ipv6|source"] = sourcePolicyRule("webpass", "ipv6", 56, 200, netip.Prefix{},
		"runtime", planFamilyConditions("translated-source-published"))
	requirePolicyRules(t, loaded, want)

	loaded.WAN["webpass"].TranslationV6.NPT.ExternalSource = config.PrefixConfigured
	loaded.WAN["webpass"].TranslationV6.NPT.ExternalPrefix = netip.Prefix{}
	rules, err := loaded.PolicyRules()
	wantErr := "wan webpass: ipv6 source rule has no configured prefix"
	if err == nil || err.Error() != wantErr {
		t.Fatalf("PolicyRules = %+v, %v; want error %q", rules, err, wantErr)
	}
}

func TestPolicyRulesRejectRoutingNumbersOutsideUint32(t *testing.T) {
	loaded := decodePlanDocument(t, planEdit{old: `"fw-mark-prio": 200,`, replacement: `"fw-mark-prio": 4294967296,`})
	rules, err := loaded.PolicyRules()
	want := "wan webpass: fw-mark-prio 4294967296 is outside 0 to 4294967295"
	if err == nil || err.Error() != want {
		t.Fatalf("PolicyRules = %+v, %v; want error %q", rules, err, want)
	}
}

func projectedFirewall(t *testing.T, loaded *networkjson.Config) networkjson.FirewallPlan {
	t.Helper()
	plan, err := loaded.FirewallPlan()
	if err != nil {
		t.Fatalf("FirewallPlan: %v", err)
	}
	return plan
}

func firewallRules(plan networkjson.FirewallPlan) map[string]firewall.Rule {
	rules := make(map[string]firewall.Rule, len(plan.Rules))
	for key, rule := range plan.Rules {
		rules[key.String()] = rule
	}
	return rules
}

func firewallRuleOrder(t *testing.T, plan networkjson.FirewallPlan, chain string) []string {
	t.Helper()
	for key, projected := range plan.Chains {
		if key.String() != chain {
			continue
		}
		order := make([]string, 0, len(projected.RuleOrder))
		for _, ruleKey := range projected.RuleOrder {
			order = append(order, ruleKey.String())
		}
		return order
	}
	t.Fatalf("plan has no chain %s", chain)
	return nil
}

func sortedKeys(rules map[string]firewall.Rule) []string {
	return slices.Sorted(maps.Keys(rules))
}

func requireGolden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("%s differs:\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func planFixtures() map[string]string {
	return map[string]string{
		"plan-base":   planBaseDocument,
		"network-min": filepath.Join(planInstanceDir, "network-min.json"),
		"network-lxc": filepath.Join(planInstanceDir, "network-lxc.json"),
	}
}

func decodeFixture(t *testing.T, path string) *networkjson.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	loaded, err := networkjson.Decode(data)
	if err != nil {
		t.Fatalf("Decode %s: %v", path, err)
	}
	return loaded
}

// TestCompiledFirewallTextMatchesGolden compares compiled text with golden files.
func TestCompiledFirewallTextMatchesGolden(t *testing.T) {
	for name, path := range planFixtures() {
		t.Run(name, func(t *testing.T) {
			compiled, err := firewall.Compile(decodeFixture(t, path).Firewall)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			requireGolden(t, "firewall-"+name, compiled.String())
		})
	}
}

func TestCompiledBaselineTextMatchesGolden(t *testing.T) {
	for _, name := range []string{"plan-base", "network-lxc"} {
		t.Run(name, func(t *testing.T) {
			baseline, err := networkjson.LoadBaseline(planFixtures()[name])
			if err != nil || baseline == nil {
				t.Fatalf("LoadBaseline = %+v, %v", baseline, err)
			}
			compiled, err := firewall.CompileBaseline(*baseline)
			if err != nil {
				t.Fatalf("CompileBaseline: %v", err)
			}
			requireGolden(t, "firewall-baseline-"+name, compiled.String())
		})
	}
}

func TestFirewallPlanRendersTheCompiledRuleset(t *testing.T) {
	for name, path := range planFixtures() {
		t.Run(name, func(t *testing.T) {
			loaded := decodeFixture(t, path)
			plan := projectedFirewall(t, loaded)
			compiled, err := firewall.Compile(loaded.Firewall)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			ordered := 0
			for index, chain := range compiled.Chains {
				key := networkjson.FirewallChainKey{Family: chain.Table.Family, Table: chain.Table.Name, Chain: chain.Name}
				projected, found := plan.Chains[key]
				if !found {
					t.Fatalf("plan omits chain %s", key)
				}
				rules := make([]firewall.Rule, 0, len(projected.RuleOrder))
				for _, ruleKey := range projected.RuleOrder {
					rule, found := plan.Rules[ruleKey]
					if !found || ruleKey.Family != key.Family || ruleKey.Table != key.Table || ruleKey.Chain != key.Chain {
						t.Fatalf("chain %s orders rule %s outside its rules", key, ruleKey)
					}
					rules = append(rules, rule)
				}
				ordered += len(rules)
				compiled.Chains[index].Rules = rules
			}
			if ordered != len(plan.Rules) || len(plan.Chains) != len(compiled.Chains) || len(plan.Sets) != len(compiled.Sets) {
				t.Fatalf("plan has %d chains, %d rules, %d sets; rule_order lists %d rules", len(plan.Chains), len(plan.Rules), len(plan.Sets), ordered)
			}
			requireGolden(t, "firewall-"+name, compiled.String())
		})
	}
}

func TestFirewallPlanKeysAndRuleOrder(t *testing.T) {
	plan := projectedFirewall(t, decodePlanDocument(t))
	wantInput := []string{
		"inet|filter|input|established|all",
		"inet|filter|input|loopback|all",
		"inet|filter|input|icmp|ipv4",
		"inet|filter|input|icmp|ipv6",
		"inet|filter|input|management-service|tcp,22,ipv4",
		"inet|filter|input|management-service|tcp,22,ipv6",
		"inet|filter|input|management-service|tcp,50052,192.0.2.0/24",
		"inet|filter|input|local-permit|enwebpass0,ipv4,udp,67,68",
		"inet|filter|input|local-permit|enwebpass0,ipv6,udp,547,546",
		"inet|filter|input|local-permit|enatt0,ipv4,udp,67,68",
		"inet|filter|input|local-permit|enatt0,ipv6,udp,547,546",
		"inet|filter|input|local-permit|enmbrains0,ipv4,udp,67,68",
		"inet|filter|input|local-permit|enmbrains0,ipv6,udp,547,546",
		"inet|filter|input|local-permit|enmwanbr0,ipv4,tcp,0,179",
		"inet|filter|input|local-permit|enmwanbr0,ipv4,udp,0,3784",
		"inet|filter|input|local-permit|enmwanbr0,ipv4,udp,0,3785",
		"inet|filter|input|local-permit|enmwanbr0,ipv6,tcp,0,179",
		"inet|filter|input|local-permit|enmwanbr0,ipv6,udp,0,3784",
		"inet|filter|input|local-permit|enmwanbr0,ipv6,udp,0,3785",
		"inet|filter|input|drop-log|all",
	}
	if got := firewallRuleOrder(t, plan, planInputChain); !slices.Equal(got, wantInput) {
		t.Fatalf("input rule_order = %q, want %q", got, wantInput)
	}
	wantMangle := []string{
		"inet|mangle|prerouting|internal-class-unmark|all",
		"inet|mangle|prerouting|provider-mark|enwebpass0",
		"inet|mangle|prerouting|provider-mark|enatt0",
		"inet|mangle|prerouting|provider-mark|enmbrains0",
		"inet|mangle|prerouting|pinned-destination-mark|ipv4",
		"inet|mangle|prerouting|pinned-destination-mark|ipv6",
		"inet|mangle|prerouting|pinned-source-mark|ipv6",
		"inet|mangle|prerouting|forced-dscp-mark|enatt0,ipv4",
		"inet|mangle|prerouting|forced-dscp-mark|enatt0,ipv6",
		"inet|mangle|prerouting|restore-mark|new",
		"inet|mangle|prerouting|restore-mark|established",
	}
	if got := firewallRuleOrder(t, plan, planManglePrerouting); !slices.Equal(got, wantMangle) {
		t.Fatalf("mangle prerouting rule_order = %q, want %q", got, wantMangle)
	}
	rules := firewallRules(plan)
	attMark := uint32(1)
	wantRules := map[string]firewall.Rule{
		"inet|mangle|prerouting|provider-mark|enatt0": {
			Purpose: "provider-mark", Scope: "enatt0", Action: "mark",
			Expression: `iifname "enatt0" ct state new meta mark set 1`,
			Interface:  "enatt0", OutputInterface: "", Source: netip.Prefix{}, Destination: netip.Prefix{}, Mark: &attMark,
		},
		"ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.2": {
			Purpose: "static-mapping-dnat", Scope: "enwebpass0,203.0.113.2", Action: "dnat",
			Expression: `iifname "enwebpass0" ip daddr 203.0.113.2 dnat to 192.0.2.2`,
			Interface:  "enwebpass0", OutputInterface: "", Source: netip.Prefix{},
			Destination: netip.MustParsePrefix("203.0.113.2/32"), Mark: nil,
		},
		"ip|nat|postrouting|masquerade|enwebpass0": {
			Purpose: "masquerade", Scope: "enwebpass0", Action: "masquerade",
			Expression: `oifname "enwebpass0" ip saddr 192.0.2.0/29 masquerade`,
			Interface:  "", OutputInterface: "enwebpass0", Source: netip.MustParsePrefix("192.0.2.0/29"),
			Destination: netip.Prefix{}, Mark: nil,
		},
		"inet|filter|input|management-service|tcp,50052,192.0.2.0/24": {
			Purpose: "management-service", Scope: "tcp,50052,192.0.2.0/24", Action: "accept",
			Expression: `iifname "enmgmt0" meta nfproto ipv4 meta l4proto tcp ip saddr 192.0.2.0/24 tcp dport 50052 accept`,
			Interface:  "enmgmt0", OutputInterface: "", Source: netip.MustParsePrefix("192.0.2.0/24"),
			Destination: netip.Prefix{}, Mark: nil,
		},
	}
	for key, want := range wantRules {
		if got, found := rules[key]; !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("rule %s = %+v, want %+v", key, got, want)
		}
	}
	wantSets := map[string][]netip.Prefix{
		"inet|mangle|att_pinned_v4": {netip.MustParsePrefix("198.51.100.0/24")},
		"inet|mangle|att_pinned_v6": {netip.MustParsePrefix("2001:db8:100::/48")},
	}
	gotSets := make(map[string][]netip.Prefix, len(plan.Sets))
	for key, set := range plan.Sets {
		gotSets[key.String()] = set.Elements
	}
	if !reflect.DeepEqual(gotSets, wantSets) {
		t.Fatalf("sets = %+v, want %+v", gotSets, wantSets)
	}
}

func TestFirewallPlanUpdatesMutableFieldsUnderTheSameKeys(t *testing.T) {
	base := firewallRules(projectedFirewall(t, decodePlanDocument(t)))
	plan := projectedFirewall(t, decodePlanDocument(t,
		planEdit{old: `"fw-mark": 1,`, replacement: `"fw-mark": 9,`},
		planEdit{old: `"pinned-v4": ["198.51.100.0/24"]`, replacement: `"pinned-v4": ["198.51.100.0/24", "203.0.113.128/25"]`},
	))
	changed := firewallRules(plan)
	if !slices.Equal(sortedKeys(changed), sortedKeys(base)) {
		t.Fatalf("rule keys changed:\ngot  %q\nwant %q", sortedKeys(changed), sortedKeys(base))
	}
	key := "inet|mangle|prerouting|provider-mark|enatt0"
	if mark := changed[key].Mark; mark == nil || *mark != 9 || *base[key].Mark != 1 {
		t.Fatalf("rule %s mark = %v, want 9 after 1", key, mark)
	}
	if got, want := changed[key].Expression, `iifname "enatt0" ct state new meta mark set 9`; got != want {
		t.Fatalf("rule %s expression = %q, want %q", key, got, want)
	}
	untouched := "inet|mangle|prerouting|provider-mark|enwebpass0"
	if !reflect.DeepEqual(changed[untouched], base[untouched]) {
		t.Fatalf("rule %s changed: %+v", untouched, changed[untouched])
	}
	setKey := networkjson.FirewallSetKey{Family: "inet", Table: "mangle", Set: "att_pinned_v4"}
	wantElements := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.128/25")}
	if got := plan.Sets[setKey].Elements; !slices.Equal(got, wantElements) {
		t.Fatalf("set %s elements = %v, want %v", setKey, got, wantElements)
	}
}

func TestFirewallPlanRekeysOnSourceIdentity(t *testing.T) {
	base := sortedKeys(firewallRules(projectedFirewall(t, decodePlanDocument(t))))
	plan := projectedFirewall(t, decodePlanDocument(t,
		planEdit{old: `"port": 22 }`, replacement: `"port": 2222 }`},
		planEdit{old: `"from-prio": 55,` + "\n" + `          "forced-dscp": 8`, replacement: `"from-prio": 55`},
		planEdit{old: `"pinned-set-v4-name": "att_pinned_v4"`, replacement: `"pinned-set-v4-name": "att_routes_v4"`},
	))
	want := make([]string, 0, len(base))
	for _, key := range base {
		switch key {
		case "inet|filter|input|management-service|tcp,22,ipv4":
			want = append(want, "inet|filter|input|management-service|tcp,2222,ipv4")
		case "inet|filter|input|management-service|tcp,22,ipv6":
			want = append(want, "inet|filter|input|management-service|tcp,2222,ipv6")
		case "inet|mangle|prerouting|forced-dscp-mark|enatt0,ipv4", "inet|mangle|prerouting|forced-dscp-mark|enatt0,ipv6":
		default:
			want = append(want, key)
		}
	}
	slices.Sort(want)
	if got := sortedKeys(firewallRules(plan)); !slices.Equal(got, want) {
		t.Fatalf("rule keys:\ngot  %q\nwant %q", got, want)
	}
	renamed := networkjson.FirewallSetKey{Family: "inet", Table: "mangle", Set: "att_routes_v4"}
	removed := networkjson.FirewallSetKey{Family: "inet", Table: "mangle", Set: "att_pinned_v4"}
	if _, found := plan.Sets[renamed]; !found {
		t.Fatalf("sets omit %s: %+v", renamed, plan.Sets)
	}
	if _, found := plan.Sets[removed]; found {
		t.Fatalf("sets retain %s", removed)
	}
}

func TestFirewallPlanReorderChangesOnlyRuleOrder(t *testing.T) {
	base := projectedFirewall(t, decodePlanDocument(t))
	firstService := `{ "protocol": "tcp", "port": 22 }`
	secondService := `{ "protocol": "tcp", "port": 50052, "allowed-source": ["192.0.2.0/24"] }`
	firstMapping := `{ "external": "203.0.113.2", "internal": "192.0.2.2" }`
	secondMapping := `{ "external": "203.0.113.3", "internal": "192.0.2.3" }`
	reordered := projectedFirewall(t, decodePlanDocument(t,
		planEdit{
			old:         firstService + ",\n          " + secondService,
			replacement: secondService + ",\n          " + firstService,
		},
		planEdit{
			old:         firstMapping + ",\n              " + secondMapping,
			replacement: secondMapping + ",\n              " + firstMapping,
		},
	))
	if !reflect.DeepEqual(firewallRules(reordered), firewallRules(base)) {
		t.Fatalf("reordering changed rule identities or values:\ngot  %+v\nwant %+v", firewallRules(reordered), firewallRules(base))
	}
	wantInput := firewallRuleOrder(t, base, planInputChain)
	wantInput[4], wantInput[5], wantInput[6] = wantInput[6], wantInput[4], wantInput[5]
	if got := firewallRuleOrder(t, reordered, planInputChain); !slices.Equal(got, wantInput) {
		t.Fatalf("input rule_order = %q, want %q", got, wantInput)
	}
	wantNAT := []string{
		"ip|nat|prerouting|pinned-source-mark|ipv4",
		"ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.3",
		"ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.2",
	}
	if got := firewallRuleOrder(t, reordered, planNATPrerouting); !slices.Equal(got, wantNAT) {
		t.Fatalf("nat prerouting rule_order = %q, want %q", got, wantNAT)
	}
}

func TestFirewallPlanNumbersIndistinguishableDuplicates(t *testing.T) {
	loaded := decodePlanDocument(t)
	permits := loaded.Firewall.LocalPermits
	duplicated := slices.Clone(permits)
	duplicated = append(duplicated, permits[0], permits[0])
	loaded.Firewall.LocalPermits = duplicated
	plan := projectedFirewall(t, loaded)
	order := firewallRuleOrder(t, plan, planInputChain)
	first := "inet|filter|input|local-permit|enwebpass0,ipv4,udp,67,68"
	wantTail := []string{first + "#2", first + "#3", "inet|filter|input|drop-log|all"}
	if got := order[len(order)-len(wantTail):]; !slices.Equal(got, wantTail) || order[7] != first {
		t.Fatalf("input rule_order = %q, want %s first and tail %q", order, first, wantTail)
	}
	rules := firewallRules(plan)
	if rules[first].Expression != rules[first+"#2"].Expression || rules[first].Expression != rules[first+"#3"].Expression {
		t.Fatalf("duplicate rules differ: %+v, %+v, %+v", rules[first], rules[first+"#2"], rules[first+"#3"])
	}

	// Removing the first permit changes the last duplicate's suffix from #3 to #2.
	loaded.Firewall.LocalPermits = duplicated[1:]
	remaining := firewallRuleOrder(t, projectedFirewall(t, loaded), planInputChain)
	wantRemaining := []string{first, first + "#2", "inet|filter|input|drop-log|all"}
	if got := remaining[len(remaining)-len(wantRemaining):]; !slices.Equal(got, wantRemaining) {
		t.Fatalf("input rule_order tail = %q, want %q", got, wantRemaining)
	}
}

func TestFirewallPlanIsEmptyWithoutFirewallOwnership(t *testing.T) {
	plan := projectedFirewall(t, decodeFixture(t, filepath.Join(planInstanceDir, "network-freeform.json")))
	if len(plan.Chains) != 0 || len(plan.Rules) != 0 || len(plan.Sets) != 0 {
		t.Fatalf("plan = %+v, want empty maps", plan)
	}
}

func requireSameProjections(t *testing.T, want *networkjson.Config, got *networkjson.Config) {
	t.Helper()
	if wantRules, gotRules := projectedPolicyRules(t, want), projectedPolicyRules(t, got); !reflect.DeepEqual(wantRules, gotRules) {
		t.Fatalf("PolicyRules differ:\nwant %+v\ngot  %+v", wantRules, gotRules)
	}
	if wantPlan, gotPlan := projectedFirewall(t, want), projectedFirewall(t, got); !reflect.DeepEqual(wantPlan, gotPlan) {
		t.Fatalf("FirewallPlan differs:\nwant %+v\ngot  %+v", wantPlan, gotPlan)
	}
}

func TestRuleProjectionsIgnoreDocumentFormatting(t *testing.T) {
	paths := planFixtures()
	paths["network-freeform"] = filepath.Join(planInstanceDir, "network-freeform.json")
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			canonical, err := networkjson.Canonicalize(data)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if string(canonical) == string(data) {
				t.Fatalf("%s is already canonical", path)
			}
			reformatted, err := networkjson.Decode(canonical)
			if err != nil {
				t.Fatalf("Decode canonical: %v", err)
			}
			requireSameProjections(t, decodeFixture(t, path), reformatted)
		})
	}
}
