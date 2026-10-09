//go:build tofu

package provider_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	tofuFixtureDir    = "testdata/networkplan"
	tofuBaseDocument  = "network-routes.json"
	tofuConfigAddress = "mwan_network_config.gateway"
	tofuFileAddress   = "terraform_data.file"
	tofuActionUpdate  = "update"
	tofuActionNoOp    = "no-op"
	tofuDecodeError   = "must be a canonical same-family network prefix"

	tofuModule         = "main.tf"
	tofuDeferredModule = "deferred.tf"
	tofuDataAddress    = "data.mwan_network.gateway"
	tofuActionCreate   = "create"
	tofuSchemaError    = "Invalid network schema"
	tofuNull           = "null"
)

type tofuPlan struct {
	ResourceChanges []tofuResourceChange `json:"resource_changes"`
}

type tofuResourceChange struct {
	Address string     `json:"address"`
	Change  tofuChange `json:"change"`
}

type tofuChange struct {
	Actions      []string        `json:"actions"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
	AfterUnknown json.RawMessage `json:"after_unknown"`
}

type tofuWorkspace struct {
	binary string
	dir    string
}

type tofuVariant struct {
	document     string
	configAction string
	fileAction   string
	changed      map[string]string
	lines        []string
}

type tofuRejection struct {
	document string
	want     []string
}

type tofuRule struct {
	family      string
	table       string
	chain       string
	purpose     string
	scope       string
	action      string
	expression  string
	source      string
	destination string
	iface       string
	output      string
	mark        string
}

func TestTofuPlan(t *testing.T) {
	workspace := newTofuWorkspace(t, tofuModule)
	workspace.apply(t, tofuBaseDocument)
	workspace.checkVariants(t, tofuVariants())
	workspace.checkVariants(t, tofuRuleVariants())

	for _, rejection := range tofuRejections() {
		t.Run(strings.TrimSuffix(rejection.document, ".json"), func(t *testing.T) {
			output, err := workspace.run(t, "plan", "-input=false", "-no-color",
				"-var", "network_file="+tofuDocument(t, rejection.document))
			if err == nil {
				t.Fatalf("tofu plan succeeded for %s:\n%s", rejection.document, output)
			}
			normalized := normalizeTofuText(output)
			for _, want := range rejection.want {
				if !strings.Contains(normalized, want) {
					t.Errorf("tofu plan output lacks %q:\n%s", want, output)
				}
			}
		})
	}

	shorthand := newTofuWorkspace(t, tofuModule)
	shorthand.apply(t, "gateway.json")
	shorthand.checkVariants(t, []tofuVariant{{
		document:     "gatewaymetric.json",
		configAction: tofuActionUpdate,
		fileAction:   tofuActionUpdate,
		changed:      map[string]string{"routes[enmgmt0|ipv4|0.0.0.0/0]": tofuGatewayRoute("20")},
		lines: []string{
			`~ "enmgmt0|ipv4|0.0.0.0/0" = {`,
			`~ metric = 10 -> 20`,
			`# (6 unchanged attributes hidden)`,
		},
	}})
}

func TestTofuPlanDefersUnknownContent(t *testing.T) {
	const invalidDocument = "unknownmember.json"
	workspace := newTofuWorkspace(t, tofuDeferredModule)

	plan, text := workspace.plan(t, invalidDocument)

	config := findTofuChange(t, plan, tofuConfigAddress)
	checkTofuAction(t, config, tofuActionCreate)
	unknown := flatTofuUnknown(t, config.Change.AfterUnknown)
	for _, name := range networkMapNames() {
		if !unknown[name] {
			t.Errorf("%s.%s is known in the plan, want an unknown value", tofuConfigAddress, name)
		}
	}
	wantLine := "# " + tofuDataAddress + " will be read during apply"
	if !strings.Contains(normalizeTofuText(text), wantLine) {
		t.Errorf("the rendered plan lacks %q:\n%s", wantLine, text)
	}
	if strings.Contains(text, tofuSchemaError) {
		t.Errorf("the plan reports a schema error for unknown content:\n%s", text)
	}

	output, err := workspace.run(t, "apply", "-auto-approve", "-input=false", "-no-color",
		"-var", "network_file="+tofuDocument(t, invalidDocument))
	if err == nil {
		t.Fatalf("tofu apply succeeded for %s:\n%s", invalidDocument, output)
	}
	for _, want := range []string{tofuSchemaError, "unknown-member"} {
		if !strings.Contains(normalizeTofuText(output), want) {
			t.Errorf("tofu apply output lacks %q:\n%s", want, output)
		}
	}

	workspace.apply(t, tofuBaseDocument)
	plan, text = workspace.plan(t, tofuBaseDocument)
	checkTofuAction(t, findTofuChange(t, plan, tofuConfigAddress), tofuActionNoOp)
	if !strings.HasPrefix(normalizeTofuText(text), "No changes.") {
		t.Errorf("the rendered plan does not start with No changes.:\n%s", text)
	}
}

func flatTofuUnknown(t *testing.T, afterUnknown json.RawMessage) map[string]bool {
	t.Helper()
	var attributes map[string]json.RawMessage
	if err := json.Unmarshal(afterUnknown, &attributes); err != nil {
		t.Fatalf("decode after_unknown %s: %v", afterUnknown, err)
	}
	unknown := map[string]bool{}
	for name, value := range attributes {
		unknown[name] = string(value) == "true"
	}
	return unknown
}

func tofuRejections() []tofuRejection {
	return []tofuRejection{
		{document: "invalid.json", want: []string{tofuDecodeError, `"198.18.1.1/24"`}},
		{document: "syntax.json", want: []string{"Invalid network document"}},
		{
			document: "duplicate.json",
			want:     []string{"Invalid network document", `object member "name" appears more than once`},
		},
		{
			document: "rejected.json",
			want:     []string{"Rejected provider entry", "enmbrains0", "from-prio is required"},
		},
		{document: "unknownmember.json", want: []string{tofuSchemaError, "unknown-member"}},
		{document: "invalidenum.json", want: []string{tofuSchemaError, "bogus"}},
		{document: "outofrange.json", want: []string{tofuSchemaError, "256"}},
		{document: "missingmandatory.json", want: []string{tofuSchemaError, "type"}},
		{document: "explicitnull.json", want: []string{tofuSchemaError, "uint8 value"}},
	}
}

// tofuDocument generates these documents from edits instead of reading fixture files.
func tofuEdits() map[string][]string {
	return map[string][]string{
		"unknownmember.json": {
			networkManagementEntry,
			`{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }`,
		},
		"invalidenum.json": {`"hash-mode": "source"`, `"hash-mode": "bogus"`},
		"outofrange.json": {
			`"ping-count": 3,` + "\n" + `            "success-threshold": 2,`,
			`"ping-count": 256,` + "\n" + `            "success-threshold": 2,`,
		},
		"missingmandatory.json": {networkManagementEntry, `{ "name": "enmgmt0" }`},
		"explicitnull.json":     {`"forced-dscp": 8`, `"forced-dscp": null`},
		"policyadded.json": {
			`"ietf-ip:ipv6": {` + "\n" + `          "goodkind-mwan-steering:translation": { "mode": "native" }`,
			`"ietf-ip:ipv6": { "goodkind-mwan-steering:translation": { "mode": "ietf-nat:nptv6", "nptv6": { ` +
				`"internal-prefix": "2001:db8:b01::/60", "external-source": "configured", ` +
				`"external-prefix": "2001:db8:beef:100::/60" } }`,
		},
		"policyremoved.json": {
			`"mode": "ietf-nat:nptv6",`, `"mode": "native"`,
			`"nptv6": {` + "\n" +
				`              "internal-prefix": "2001:db8:b01::/60",` + "\n" +
				`              "external-source": "configured",` + "\n" +
				`              "external-prefix": "2001:db8:beef:200::/60"` + "\n" +
				`            }`, ``,
		},
		"policychanged.json": {`"fw-mark-prio": 100,`, `"fw-mark-prio": 110,`},
		"policyrenamed.json": {
			`"name": "att",`, `"name": "attfiber",`,
			`"pinned-provider": "att",`, `"pinned-provider": "attfiber",`,
		},
		"ruleadded.json": {
			`{ "protocol": "tcp", "port": 22 },`,
			`{ "protocol": "tcp", "port": 22 }, { "protocol": "udp", "port": 161 },`,
		},
		"ruleremoved.json": {`{ "protocol": "tcp", "port": 22 },`, ``},
		"masqueraded.json": {
			`"ietf-ip:ipv4": {` + "\n" + `          "goodkind-mwan-steering:translation": { "mode": "native" }`,
			`"ietf-ip:ipv4": { "goodkind-mwan-steering:translation": { "mode": "ietf-nat:napt44" }`,
		},
		"rulechanged.json": {`"fw-mark": 3,`, `"fw-mark": 5,`},
		"rulereordered.json": {
			`{ "external": "203.0.113.2", "internal": "192.0.2.2" },` + "\n" +
				`              { "external": "203.0.113.3", "internal": "192.0.2.3" }`,
			`{ "external": "203.0.113.3", "internal": "192.0.2.3" },` + "\n" +
				`              { "external": "203.0.113.2", "internal": "192.0.2.2" }`,
		},
		"setchanged.json": {`"pinned-v4": ["198.51.100.0/24"],`, `"pinned-v4": ["198.51.100.0/24", "203.0.113.128/25"],`},
	}
}

func tofuPriorEntries() map[string]map[string]string {
	return map[string]map[string]string{
		"policyremoved.json": {
			"policy_rules[webpass|ipv6|source]": tofuSourceRule("webpass", "56", "2001:db8:beef:200::/60", "200"),
		},
		"policychanged.json": {
			"policy_rules[att|ipv4|fwmark]": tofuFwmarkRule("att", "ipv4", "1", "100", "100"),
			"policy_rules[att|ipv6|fwmark]": tofuFwmarkRule("att", "ipv6", "1", "100", "100"),
		},
		"policyrenamed.json": {
			"policy_rules[att|ipv4|fwmark]": tofuFwmarkRule("att", "ipv4", "1", "100", "100"),
			"policy_rules[att|ipv6|fwmark]": tofuFwmarkRule("att", "ipv6", "1", "100", "100"),
		},
		"ruleremoved.json": {
			"firewall_rules[" + tofuServiceRule("tcp", "22", "ipv4").key() + "]": tofuServiceRule("tcp", "22", "ipv4").json(),
			"firewall_rules[" + tofuServiceRule("tcp", "22", "ipv6").key() + "]": tofuServiceRule("tcp", "22", "ipv6").json(),
		},
		"rulechanged.json": {
			"firewall_rules[" + tofuProviderMarkRule("enmbrains0", "3").key() + "]": tofuProviderMarkRule("enmbrains0", "3").json(),
			"policy_rules[monkeybrains|ipv4|fwmark]":                                tofuFwmarkRule("monkeybrains", "ipv4", "3", "300", "300"),
		},
		"rulereordered.json": {
			"firewall_chains[ip|nat|prerouting]": tofuChain("ip", "nat", "prerouting",
				"ip|nat|prerouting|pinned-source-mark|ipv4",
				"ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.2",
				"ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.3"),
		},
		"setchanged.json": {
			"firewall_sets[inet|mangle|att_pinned_v4]": tofuPinnedSet(`"198.51.100.0/24"`),
		},
	}
}

func tofuPolicyRule(connectionID string, family string, kind string, mark string, priority string, source string, tableID string) string {
	conditions := `"translation-ready","default-gateway-discovered","provider-healthy"`
	sourceKind := "none"
	if kind == "source" {
		conditions += `,"translated-source-published"`
		sourceKind = "configured"
	}
	return `{"activation_conditions":[` + conditions + `],"connection_id":"` + connectionID +
		`","family":"` + family + `","kind":"` + kind + `","mark":` + mark + `,"priority":` + priority +
		`,"source":` + source + `,"source_kind":"` + sourceKind + `","table_id":` + tableID + `}`
}

func tofuFwmarkRule(connectionID string, family string, mark string, priority string, tableID string) string {
	return tofuPolicyRule(connectionID, family, "fwmark", mark, priority, tofuNull, tableID)
}

func tofuSourceRule(connectionID string, priority string, source string, tableID string) string {
	return tofuPolicyRule(connectionID, "ipv6", "source", tofuNull, priority, `"`+source+`"`, tableID)
}

func (r tofuRule) chainKey() string {
	return r.family + "|" + r.table + "|" + r.chain
}

func (r tofuRule) key() string {
	return r.chainKey() + "|" + r.purpose + "|" + r.scope
}

func (r tofuRule) json() string {
	expression, err := json.Marshal(r.expression)
	if err != nil {
		panic(err)
	}
	return `{"action":"` + r.action + `","chain":"` + r.chain + `","destination":` + r.destination +
		`,"expression":` + string(expression) + `,"family":"` + r.family + `","interface":` + r.iface +
		`,"key":"` + r.key() + `","mark":` + r.mark + `,"output_interface":` + r.output + `,"purpose":"` + r.purpose + `","scope":"` + r.scope +
		`","source":` + r.source + `,"table":"` + r.table + `"}`
}

func tofuServiceRule(protocol string, port string, family string) tofuRule {
	return tofuRule{
		family: "inet", table: "filter", chain: "input", purpose: "management-service",
		scope: protocol + "," + port + "," + family, action: "accept",
		expression: `iifname "enmgmt0" meta nfproto ` + family + ` meta l4proto ` + protocol + ` ` + protocol +
			` dport ` + port + ` accept`,
		source: tofuNull, destination: tofuNull, iface: `"enmgmt0"`, output: tofuNull, mark: tofuNull,
	}
}

func tofuProviderMarkRule(iface string, mark string) tofuRule {
	return tofuRule{
		family: "inet", table: "mangle", chain: "prerouting", purpose: "provider-mark", scope: iface,
		action: "mark", expression: `iifname "` + iface + `" ct state new meta mark set ` + mark,
		source: tofuNull, destination: tofuNull, iface: `"` + iface + `"`, output: tofuNull, mark: mark,
	}
}

func tofuMasqueradeRule(iface string, source string) tofuRule {
	return tofuRule{
		family: "ip", table: "nat", chain: "postrouting", purpose: "masquerade", scope: iface,
		action: "masquerade", expression: `oifname "` + iface + `" ip saddr ` + source + ` masquerade`,
		source: `"` + source + `"`, destination: tofuNull, iface: tofuNull, output: `"` + iface + `"`, mark: tofuNull,
	}
}

func tofuChain(family string, table string, chain string, ruleOrder ...string) string {
	return `{"chain":"` + chain + `","family":"` + family + `","rule_order":["` +
		strings.Join(ruleOrder, `","`) + `"],"table":"` + table + `"}`
}

func tofuPinnedSet(elements string) string {
	return `{"elements":[` + elements + `],"family":"inet","key_type":"ipv4_addr","set":"att_pinned_v4","table":"mangle"}`
}

func tofuInputChain(services ...string) string {
	order := []string{
		"inet|filter|input|established|all",
		"inet|filter|input|loopback|all",
		"inet|filter|input|icmp|ipv4",
		"inet|filter|input|icmp|ipv6",
	}
	order = append(order, services...)
	order = append(order, tofuInputPermits()...)
	return tofuChain("inet", "filter", "input", order...)
}

func tofuInputPermits() []string {
	const prefix = "inet|filter|input|local-permit|"
	var order []string
	for _, provider := range []string{"enwebpass0", "enatt0", "enmbrains0"} {
		order = append(order, prefix+provider+",ipv4,udp,67,68", prefix+provider+",ipv6,udp,547,546")
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		internal := prefix + "enmwanbr0," + family
		order = append(order, internal+",tcp,0,179", internal+",udp,0,3784", internal+",udp,0,3785")
	}
	return append(order, "inet|filter|input|drop-log|all")
}

func tofuRuleVariants() []tofuVariant {
	sshIPv4, sshIPv6 := tofuServiceRule("tcp", "22", "ipv4"), tofuServiceRule("tcp", "22", "ipv6")
	snmpIPv4, snmpIPv6 := tofuServiceRule("udp", "161", "ipv4"), tofuServiceRule("udp", "161", "ipv6")
	agentRule := "inet|filter|input|management-service|tcp,50052,192.0.2.0/24"
	markRule := tofuProviderMarkRule("enmbrains0", "5")
	attMasquerade := tofuMasqueradeRule("enatt0", "192.0.2.0/29")
	firstDNAT := "ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.2"
	secondDNAT := "ip|nat|prerouting|static-mapping-dnat|enwebpass0,203.0.113.3"
	firstSNAT := "ip|nat|postrouting|static-mapping-snat|enwebpass0,203.0.113.2"
	secondSNAT := "ip|nat|postrouting|static-mapping-snat|enwebpass0,203.0.113.3"
	return []tofuVariant{
		{
			document:     "policyadded.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"policy_rules[att|ipv6|source]": tofuSourceRule("att", "55", "2001:db8:beef:100::/60", "100"),
			},
			lines: []string{
				`~ policy_rules = {`,
				`+ "att|ipv6|source" = {`,
				`+ source = "2001:db8:beef:100::/60"`,
			},
		},
		{
			document:     "policyremoved.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{"policy_rules[webpass|ipv6|source]": ""},
			lines: []string{
				`~ policy_rules = {`,
				`- "webpass|ipv6|source" = {`,
				`} -> null,`,
			},
		},
		{
			document:     "policychanged.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"policy_rules[att|ipv4|fwmark]": tofuFwmarkRule("att", "ipv4", "1", "110", "100"),
				"policy_rules[att|ipv6|fwmark]": tofuFwmarkRule("att", "ipv6", "1", "110", "100"),
			},
			lines: []string{
				`~ "att|ipv4|fwmark" = {`,
				`~ "att|ipv6|fwmark" = {`,
				`~ priority = 100 -> 110`,
			},
		},
		{
			document:     "policyrenamed.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"interfaces[enatt0]": `{"connection_id":"attfiber","enabled":null,"name":"enatt0","owner":"networkd",` +
					`"provider_name":"attfiber","roles":["provider"],"type":"iana-if-type:other"}`,
				"provider_defaults[att|ipv4|100]":      "",
				"provider_defaults[att|ipv6|100]":      "",
				"provider_defaults[attfiber|ipv4|100]": tofuProviderDefault("attfiber", "ipv4", "192.0.2.0/29"),
				"provider_defaults[attfiber|ipv6|100]": tofuProviderDefault("attfiber", "ipv6", "2001:db8:b01:fe::2/128"),
				"policy_rules[att|ipv4|fwmark]":        "",
				"policy_rules[att|ipv6|fwmark]":        "",
				"policy_rules[attfiber|ipv4|fwmark]":   tofuFwmarkRule("attfiber", "ipv4", "1", "100", "100"),
				"policy_rules[attfiber|ipv6|fwmark]":   tofuFwmarkRule("attfiber", "ipv6", "1", "100", "100"),
			},
			lines: []string{
				`- "att|ipv4|fwmark" = {`,
				`+ "attfiber|ipv4|fwmark" = {`,
				`+ connection_id = "attfiber"`,
			},
		},
		{
			document:     "ruleadded.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"firewall_rules[" + snmpIPv4.key() + "]": snmpIPv4.json(),
				"firewall_rules[" + snmpIPv6.key() + "]": snmpIPv6.json(),
				"firewall_chains[inet|filter|input]": tofuInputChain(
					sshIPv4.key(), sshIPv6.key(), snmpIPv4.key(), snmpIPv6.key(), agentRule),
			},
			lines: []string{
				`~ firewall_rules = {`,
				`+ "inet|filter|input|management-service|udp,161,ipv4" = {`,
				`+ "inet|filter|input|management-service|udp,161,ipv6" = {`,
				`~ rule_order = [`,
			},
		},
		{
			document:     "ruleremoved.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"firewall_rules[" + sshIPv4.key() + "]": "",
				"firewall_rules[" + sshIPv6.key() + "]": "",
				"firewall_chains[inet|filter|input]":    tofuInputChain(agentRule),
			},
			lines: []string{
				`- "inet|filter|input|management-service|tcp,22,ipv4" = {`,
				`- "inet|filter|input|management-service|tcp,22,ipv6" = {`,
				`~ rule_order = [`,
			},
		},
		{
			document:     "masqueraded.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"firewall_rules[" + attMasquerade.key() + "]": attMasquerade.json(),
				"firewall_chains[ip|nat|postrouting]": tofuChain("ip", "nat", "postrouting",
					firstSNAT, secondSNAT,
					tofuMasqueradeRule("enwebpass0", "192.0.2.0/29").key(),
					attMasquerade.key(),
					tofuMasqueradeRule("enmbrains0", "192.0.2.0/29").key()),
			},
			lines: []string{
				`+ "ip|nat|postrouting|masquerade|enatt0" = {`,
				`+ action = "masquerade"`,
				`+ output_interface = "enatt0"`,
				`+ source = "192.0.2.0/29"`,
			},
		},
		{
			document:     "rulechanged.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"firewall_rules[" + markRule.key() + "]": markRule.json(),
				"policy_rules[monkeybrains|ipv4|fwmark]": tofuFwmarkRule("monkeybrains", "ipv4", "5", "300", "300"),
			},
			lines: []string{
				`~ "inet|mangle|prerouting|provider-mark|enmbrains0" = {`,
				`~ expression = "iifname \"enmbrains0\" ct state new meta mark set 3" -> "iifname \"enmbrains0\" ct state new meta mark set 5"`,
				`~ mark = 3 -> 5`,
			},
		},
		{
			document:     "rulereordered.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"firewall_chains[ip|nat|prerouting]": tofuChain("ip", "nat", "prerouting",
					"ip|nat|prerouting|pinned-source-mark|ipv4", secondDNAT, firstDNAT),
				"firewall_chains[ip|nat|postrouting]": tofuChain("ip", "nat", "postrouting",
					secondSNAT, firstSNAT,
					tofuMasqueradeRule("enwebpass0", "192.0.2.0/29").key(),
					tofuMasqueradeRule("enmbrains0", "192.0.2.0/29").key()),
			},
			lines: []string{
				`~ firewall_chains = {`,
				`~ "ip|nat|prerouting" = {`,
				`~ "ip|nat|postrouting" = {`,
				`~ rule_order = [`,
				`- "` + firstDNAT + `",`,
				`+ "` + firstDNAT + `",`,
				`# (6 unchanged attributes hidden)`,
			},
		},
		{
			document:     "setchanged.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"firewall_sets[inet|mangle|att_pinned_v4]": tofuPinnedSet(`"198.51.100.0/24","203.0.113.128/25"`),
			},
			lines: []string{
				`~ firewall_sets = {`,
				`~ "inet|mangle|att_pinned_v4" = {`,
				`+ "203.0.113.128/25",`,
			},
		},
	}
}

func tofuProviderDefault(connectionID string, family string, destination string) string {
	return `{"connection_id":"` + connectionID + `","family":"` + family + `","interface":"enatt0",` +
		`"internal_destination":"` + destination + `","internal_interface":"enmwanbr0","table_id":100}`
}

func tofuGatewayRoute(metric string) string {
	return `{"destination":"0.0.0.0/0","family":"ipv4","gateway":"192.0.2.1","interface":"enmgmt0",` +
		`"metric":` + metric + `,"source":"gateway","table_id":254}`
}

func tofuVariants() []tofuVariant {
	return []tofuVariant{
		{
			document:     "gateway.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{"routes[enmgmt0|ipv4|0.0.0.0/0]": tofuGatewayRoute("10")},
			lines: []string{
				`+ "enmgmt0|ipv4|0.0.0.0/0" = {`,
				`+ destination = "0.0.0.0/0"`,
				`+ gateway = "192.0.2.1"`,
				`+ metric = 10`,
				`+ source = "gateway"`,
				`+ table_id = 254`,
			},
		},
		{
			document:     "upper.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{},
			lines:        []string{`# terraform_data.file will be updated in-place`},
		},
		{
			document:     "defaults.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{},
			lines:        []string{`# terraform_data.file will be updated in-place`},
		},
		{
			document:     "added.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"routes[enwebpass0|ipv4|198.18.3.0/24]": `{"destination":"198.18.3.0/24","family":"ipv4",` +
					`"gateway":"203.0.113.1","interface":"enwebpass0","metric":0,"source":"route","table_id":254}`,
			},
			lines: []string{
				`# mwan_network_config.gateway will be updated in-place`,
				`~ resource "mwan_network_config" "gateway" {`,
				`~ routes = {`,
				`+ "enwebpass0|ipv4|198.18.3.0/24" = {`,
				`+ destination = "198.18.3.0/24"`,
				`+ family = "ipv4"`,
				`+ gateway = "203.0.113.1"`,
				`+ interface = "enwebpass0"`,
				`+ metric = 0`,
				`+ source = "route"`,
				`+ table_id = 254`,
				`# (2 unchanged attributes hidden)`,
			},
		},
		{
			document:     "removed.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{"routes[enwebpass0|ipv4|198.18.2.0/24]": ""},
			lines: []string{
				`~ routes = {`,
				`- "enwebpass0|ipv4|198.18.2.0/24" = {`,
				`- destination = "198.18.2.0/24"`,
				`- family = "ipv4"`,
				`- gateway = "203.0.113.1"`,
				`- interface = "enwebpass0"`,
				`- metric = 50`,
				`- source = "route"`,
				`- table_id = 254`,
				`} -> null,`,
			},
		},
		{
			document:     "changed.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"routes[enwebpass0|ipv4|198.18.0.0/24]": `{"destination":"198.18.0.0/24","family":"ipv4",` +
					`"gateway":"203.0.113.9","interface":"enwebpass0","metric":50,"source":"route","table_id":254}`,
			},
			lines: []string{
				`~ "enwebpass0|ipv4|198.18.0.0/24" = {`,
				`~ gateway = "203.0.113.1" -> "203.0.113.9"`,
				`~ metric = 0 -> 50`,
				`# (5 unchanged attributes hidden)`,
			},
		},
		{
			document:     "onlink.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"routes[enwebpass0|ipv6|2001:db8:a00::/48]": `{"destination":"2001:db8:a00::/48","family":"ipv6",` +
					`"gateway":null,"interface":"enwebpass0","metric":0,"source":"route","table_id":254}`,
			},
			lines: []string{
				`~ "enwebpass0|ipv6|2001:db8:a00::/48" = {`,
				`- gateway = "2001:db8:ff::1"`,
				`# (6 unchanged attributes hidden)`,
			},
		},
		{
			document:     "internalnet.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"provider_defaults[att|ipv4|100]":          tofuInternalDefault("att", "enatt0", "100"),
				"provider_defaults[webpass|ipv4|200]":      tofuInternalDefault("webpass", "enwebpass0", "200"),
				"provider_defaults[monkeybrains|ipv4|300]": tofuInternalDefault("monkeybrains", "enmbrains0", "300"),
				"firewall_rules[ip|nat|postrouting|masquerade|enwebpass0]": tofuMasqueradeRule(
					"enwebpass0", "192.0.2.0/28").json(),
				"firewall_rules[ip|nat|postrouting|masquerade|enmbrains0]": tofuMasqueradeRule(
					"enmbrains0", "192.0.2.0/28").json(),
			},
			lines: []string{
				`~ provider_defaults = {`,
				`~ "att|ipv4|100" = {`,
				`~ "webpass|ipv4|200" = {`,
				`~ "monkeybrains|ipv4|300" = {`,
				`~ internal_destination = "192.0.2.0/29" -> "192.0.2.0/28"`,
			},
		},
		{
			document:     "reformatted.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionNoOp,
			changed:      map[string]string{},
			lines:        []string{},
		},
		{
			document:     "reordered.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{},
			lines:        []string{`# terraform_data.file will be updated in-place`},
		},
		{
			document:     "lan.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"interfaces[lan0]": `{"connection_id":"lan0","enabled":null,"name":"lan0","owner":"external",` +
					`"provider_name":null,"roles":[],"type":"iana-if-type:other"}`,
			},
			lines: []string{
				`~ interfaces = {`,
				`+ "lan0" = {`,
				`+ name = "lan0"`,
				`+ roles = []`,
			},
		},
	}
}

func tofuInternalDefault(connectionID string, iface string, tableID string) string {
	return `{"connection_id":"` + connectionID + `","family":"ipv4","interface":"` + iface +
		`","internal_destination":"192.0.2.0/28","internal_interface":"enmwanbr0","table_id":` + tableID + `}`
}

func newTofuWorkspace(t *testing.T, moduleFile string) *tofuWorkspace {
	t.Helper()
	binary := os.Getenv("TOFU")
	if binary == "" {
		t.Fatal("TOFU must contain the path of the OpenTofu binary; run make test-tofu-plan")
	}
	// OpenTofu needs dev_overrides to load the local provider build.
	if os.Getenv("TF_CLI_CONFIG_FILE") == "" {
		t.Fatal("TF_CLI_CONFIG_FILE must contain the dev_overrides configuration; run make test-tofu-plan")
	}
	dir := t.TempDir()
	module, err := os.ReadFile(filepath.Join(tofuFixtureDir, moduleFile))
	if err != nil {
		t.Fatalf("read %s: %v", moduleFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), module, 0o600); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	return &tofuWorkspace{binary: binary, dir: dir}
}

func tofuDocument(t *testing.T, name string) string {
	t.Helper()
	if replacements, edited := tofuEdits()[name]; edited {
		document := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(document, []byte(readNetworkDocument(t, replacements...)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return document
	}
	document, err := filepath.Abs(filepath.Join(tofuFixtureDir, name))
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	return document
}

func (w *tofuWorkspace) command(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.CommandContext(t.Context(), w.binary, args...)
	command.Dir = w.dir
	command.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	return command
}

func (w *tofuWorkspace) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	output, err := w.command(t, args...).CombinedOutput()
	return string(output), err
}

func (w *tofuWorkspace) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	output, err := w.run(t, args...)
	if err != nil {
		t.Fatalf("tofu %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func (w *tofuWorkspace) apply(t *testing.T, document string) {
	t.Helper()
	w.mustRun(t, "apply", "-auto-approve", "-input=false", "-no-color",
		"-var", "network_file="+tofuDocument(t, document))
}

func (w *tofuWorkspace) checkVariants(t *testing.T, variants []tofuVariant) {
	t.Helper()
	for _, variant := range variants {
		t.Run(strings.TrimSuffix(variant.document, ".json"), func(t *testing.T) {
			plan, text := w.plan(t, variant.document)
			checkTofuPlan(t, plan, text, variant)
		})
	}
}

func (w *tofuWorkspace) plan(t *testing.T, document string) (tofuPlan, string) {
	t.Helper()
	planFile := filepath.Join(t.TempDir(), "tfplan")
	w.mustRun(t, "plan", "-input=false", "-no-color", "-out="+planFile,
		"-var", "network_file="+tofuDocument(t, document))

	command := w.command(t, "show", "-json", planFile)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	shown, err := command.Output()
	if err != nil {
		t.Fatalf("tofu show -json: %v\n%s", err, stderr.String())
	}
	var plan tofuPlan
	if err := json.Unmarshal(shown, &plan); err != nil {
		t.Fatalf("decode the JSON plan: %v", err)
	}
	return plan, w.mustRun(t, "show", "-no-color", planFile)
}

func checkTofuPlan(t *testing.T, plan tofuPlan, text string, variant tofuVariant) {
	t.Helper()
	for _, change := range plan.ResourceChanges {
		if slices.Contains(change.Change.Actions, "delete") {
			t.Errorf("%s has actions %v, want no delete or replacement", change.Address, change.Change.Actions)
		}
	}
	config := findTofuChange(t, plan, tofuConfigAddress)
	checkTofuAction(t, config, variant.configAction)
	checkTofuAction(t, findTofuChange(t, plan, tofuFileAddress), variant.fileAction)

	before := flatTofuEntries(t, config.Change.Before)
	if got := changedTofuEntries(before, flatTofuEntries(t, config.Change.After)); !maps.Equal(got, variant.changed) {
		t.Errorf("%s changed entries:\ngot  %v\nwant %v", tofuConfigAddress, got, variant.changed)
	}
	for entry, want := range tofuPriorEntries()[variant.document] {
		if got := before[entry]; got != want {
			t.Errorf("%s before %s:\ngot  %s\nwant %s", tofuConfigAddress, entry, got, want)
		}
	}
	if variant.configAction == tofuActionUpdate {
		if hasUnknownTofuValue(t, config.Change.AfterUnknown) {
			t.Errorf("%s after_unknown = %s, want every value known", tofuConfigAddress, config.Change.AfterUnknown)
		}
		block := tofuResourceBlock(t, text, tofuConfigAddress)
		if strings.Contains(block, "known after apply") {
			t.Errorf("%s renders an unknown value:\n%s", tofuConfigAddress, block)
		}
	}

	unchanged := variant.configAction == tofuActionNoOp && variant.fileAction == tofuActionNoOp
	if unchanged && !strings.HasPrefix(normalizeTofuText(text), "No changes.") {
		t.Errorf("the rendered plan does not start with No changes.:\n%s", text)
	}
	lines := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		lines[normalizeTofuText(line)] = true
	}
	for _, want := range variant.lines {
		if !lines[want] {
			t.Errorf("the rendered plan lacks the line %q:\n%s", want, text)
		}
	}
}

func findTofuChange(t *testing.T, plan tofuPlan, address string) tofuResourceChange {
	t.Helper()
	for _, change := range plan.ResourceChanges {
		if change.Address == address {
			return change
		}
	}
	t.Fatalf("the plan has no resource change for %s", address)
	return tofuResourceChange{Address: "", Change: tofuChange{Actions: nil, Before: nil, After: nil, AfterUnknown: nil}}
}

func checkTofuAction(t *testing.T, change tofuResourceChange, want string) {
	t.Helper()
	if !slices.Equal(change.Change.Actions, []string{want}) {
		t.Errorf("%s actions = %v, want [%s]", change.Address, change.Change.Actions, want)
	}
}

func flatTofuEntries(t *testing.T, value json.RawMessage) map[string]string {
	t.Helper()
	var decoded map[string]map[string]json.RawMessage
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatalf("decode the network maps %s: %v", value, err)
	}
	entries := map[string]string{}
	for _, attribute := range networkMapNames() {
		for key, entry := range decoded[attribute] {
			entries[attribute+"["+key+"]"] = compactTofuJSON(t, entry)
		}
	}
	return entries
}

// changedTofuEntries records an empty string for an entry absent from after.
func changedTofuEntries(before map[string]string, after map[string]string) map[string]string {
	changed := map[string]string{}
	for name := range before {
		if before[name] != after[name] {
			changed[name] = after[name]
		}
	}
	for name, entry := range after {
		if before[name] != entry {
			changed[name] = entry
		}
	}
	return changed
}

func compactTofuJSON(t *testing.T, value json.RawMessage) string {
	t.Helper()
	if value == nil {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		t.Fatalf("compact %s: %v", value, err)
	}
	return compact.String()
}

// OpenTofu represents unknown values as true in after_unknown.
func hasUnknownTofuValue(t *testing.T, afterUnknown json.RawMessage) bool {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(afterUnknown))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			t.Fatalf("decode after_unknown %s: %v", afterUnknown, err)
		}
		if token == true {
			return true
		}
	}
}

func tofuResourceBlock(t *testing.T, text string, address string) string {
	t.Helper()
	_, block, found := strings.Cut(text, "# "+address+" ")
	if !found {
		t.Fatalf("the rendered plan has no change for %s:\n%s", address, text)
	}
	block, _, _ = strings.Cut(block, "\n\n")
	return block
}

// OpenTofu's diagnostic borders and line wrapping require text normalization.
func normalizeTofuText(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "│", " ")), " ")
}
