package networkload_test

import (
	"os"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/networkjson"
	"goodkind.io/mwan/gateway/networkload"
)

func TestOVSConnectionTranslation(t *testing.T) {
	const schema = "../yangpub/schema"
	document := readFile(t, "testdata/ovs-health.json")
	document = []byte(
		strings.ReplaceAll(
			string(document),
			`"condition": {"healthy-connection": "uplink-a"}`,
			`"connection": "uplink-a"`,
		),
	)
	document = replaceOnce(t, document, `"enabled": true`, `"enabled": false`)
	settings, err := networkload.LoadOVS(writeDocument(t, document), schema)
	if err != nil {
		t.Fatal(err)
	}

	if settings.OpenFlow.Flows[0].Connection != "uplink-a" ||
		settings.OpenFlow.Groups[0].Buckets[0].Connection != "uplink-a" || len(
		settings.OpenFlow.Mappings,
	) != 0 {
		t.Fatal("WAN without NPTv6 did not retain the configured entries")
	}

	const policy = `"ietf-ip:ipv6":{"goodkind-mwan-steering:translation":{
		"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"fd01:203:405::/48",
		"external-source":"configured","external-prefix":"2001:db8:1::/48"}}},`
	configured := replaceOnce(
		t,
		document,
		`"goodkind-mwan-steering:wan":`,
		policy+`"goodkind-mwan-steering:wan":`,
	)
	settings, err = networkload.LoadOVS(writeDocument(t, configured), schema)
	if err != nil {
		t.Fatal(err)
	}

	mapping := settings.OpenFlow.Mappings["uplink-a"]
	if mapping.InternalPrefix.String() != "fd01:203:405::/48" ||
		mapping.ExternalPrefix.String() != "2001:db8:1::/48" {
		t.Fatalf("unexpected NPTv6 mapping: %+v", mapping)
	}

	implicit := replaceOnce(t, configured, `"external-source":"configured",`, "")
	settings, err = networkload.LoadOVS(writeDocument(t, implicit), schema)
	if err != nil {
		t.Fatal(err)
	}

	if got := settings.OpenFlow.Mappings["uplink-a"]; got != mapping {
		t.Fatalf("implicit prefix source produced %+v, want %+v", got, mapping)
	}

	unknown := replaceOnce(
		t,
		document,
		`"id": 7, "connection": "uplink-a"`,
		`"id": 7, "connection": "wan0"`,
	)
	if _, err := networkload.LoadOVS(writeDocument(t, unknown), schema); err == nil ||
		!strings.Contains(err.Error(), `unknown WAN connection "wan0"`) {
		t.Fatalf("unexpected result for interface name in connection: %v", err)
	}

	delegated := replaceOnce(
		t,
		configured,
		`"ietf-ip:ipv6":{`,
		`"ietf-ip:ipv6":{
		"goodkind-mwan-steering:dhcp":true, "goodkind-mwan-steering:delegation":{
		"hint":"::/48","duid-type":"link-layer-time","duid":"00:01:2a:5b:3c:4d:02:00:5e:00:53:01"},`,
	)
	delegated = replaceOnce(
		t,
		delegated,
		`"configured","external-prefix"`,
		`"delegated","expected-prefix"`,
	)
	settings, err = networkload.LoadOVS(writeDocument(t, delegated), schema)
	if err != nil {
		t.Fatal(err)
	}

	mapping = settings.OpenFlow.Mappings["uplink-a"]
	if mapping.ExternalPrefix.IsValid() || mapping.ExpectedPrefix.String() != "2001:db8:1::/48" {
		t.Fatalf("unexpected delegated NPTv6 mapping: %+v", mapping)
	}

	conflict := replaceOnce(t, delegated, `"expected-prefix"`, `"external-prefix"`)
	_, err = networkload.LoadOVS(writeDocument(t, conflict), schema)
	if err == nil ||
		!strings.Contains(err.Error(), "delegated source cannot configure external-prefix") {
		t.Fatalf("unexpected result for conflicting delegated and configured sources: %v", err)
	}
}

func TestLoadOVS(t *testing.T) {
	settings, err := networkload.LoadOVS("testdata/ovs.json", "../yangpub/schema")
	if err != nil {
		t.Fatal(err)
	}

	if settings.Bridge != "br-mwan" || settings.DatapathType != "doca" ||
		settings.Offload != "doca" || len(settings.Ports) != 4 || settings.Ports[0].Type != "doca" {
		t.Fatalf("unexpected OVS settings: %+v", settings)
	}

	if _, err := networkload.LoadOVS(
		"../yang/instances/network-min.json",
		"../yangpub/schema",
	); err == nil {
		t.Fatal("LoadOVS accepted a kernel network document")
	}
}

func TestIndirectGroupHealthCondition(t *testing.T) {
	const schema = "../yangpub/schema"
	base, err := os.ReadFile("testdata/ovs-health.json")
	if err != nil {
		t.Fatal(err)
	}

	indirect := replaceOnce(t, base, `"type": "select"`, `"type": "indirect"`)
	const want = "Apply health conditions to referring flows"
	if _, err := networkload.LoadOVS(writeDocument(t, indirect), schema); err == nil ||
		!strings.Contains(err.Error(), want) {
		t.Fatalf("LoadOVS returned an unexpected result for a conditional indirect bucket: %v", err)
	}

	if _, err := networkjson.Decode(indirect); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Decode returned an unexpected result for a conditional indirect bucket: %v", err)
	}

	unconditional := replaceOnce(
		t,
		indirect,
		`"id": 7, "condition": {"healthy-connection": "uplink-a"}`,
		`"id": 7`,
	)
	if _, err := networkload.LoadOVS(writeDocument(t, unconditional), schema); err != nil {
		t.Fatalf("LoadOVS rejected a conditional flow that refers to an indirect group: %v", err)
	}
}

func TestLoadOVSHealthConditions(t *testing.T) {
	const fixture = "testdata/ovs-health.json"
	const schema = "../yangpub/schema"
	base, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	settings, err := networkload.LoadOVS(fixture, schema)
	if err != nil {
		t.Fatal(err)
	}

	flow := settings.OpenFlow.Flows[0]
	bucket := settings.OpenFlow.Groups[0].Buckets[0]
	if flow.Condition == nil || bucket.Condition == nil ||
		flow.Condition.HealthyConnection != "uplink-a" ||
		bucket.Condition.HealthyConnection != "uplink-a" {
		t.Fatalf("loaded conditions: flow %+v, bucket %+v", flow.Condition, bucket.Condition)
	}

	// A WAN without an explicit connection ID uses its WAN name instead.
	implicit := replaceOnce(t, base, `"goodkind-mwan-steering:connection-id": "uplink-a",`, "")
	implicit = replaceOnce(t, implicit, `"name": "provider-a"`, `"name": "uplink-a"`)
	if _, err := networkload.LoadOVS(writeDocument(t, implicit), schema); err != nil {
		t.Fatalf("WAN name condition: %v", err)
	}

	tests := []struct{ name, before, after, want string }{
		{
			name:   "unknown bucket connection",
			before: `"id": 7, "condition": {"healthy-connection": "uplink-a"}`,
			after:  `"id": 7, "condition": {"healthy-connection": "unknown"}`,
			want:   `OpenFlow group 1 bucket 7: health condition requires an enabled WAN health probe: "unknown"`,
		},
		{
			name:   "disabled flow probe",
			before: `"enabled": true`,
			after:  `"enabled": false`,
			want:   `OpenFlow flow "send-to-uplink": health condition requires an enabled WAN health probe: "uplink-a"`,
		},
		{
			name:   "empty condition",
			before: `"id": 7, "condition": {"healthy-connection": "uplink-a"}`,
			after:  `"id": 7, "condition": {}`,
			want:   "connection-verdict",
		},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				document := replaceOnce(t, base, test.before, test.after)
				_, err := networkload.LoadOVS(writeDocument(t, document), schema)
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("LoadOVS returned %v, want %q", err, test.want)
				}
			},
		)
	}
}

func TestLoadOVSUnhealthyConnection(t *testing.T) {
	base := string(readFile(t, "testdata/ovs-health.json"))
	document := strings.ReplaceAll(base, `"healthy-connection"`, `"unhealthy-connection"`)
	settings, err := networkload.LoadOVS(writeDocument(t, []byte(document)), "../yangpub/schema")
	if err != nil {
		t.Fatal(err)
	}

	if settings.OpenFlow.Flows[0].Condition.UnhealthyConnection != "uplink-a" ||
		settings.OpenFlow.Groups[0].Buckets[0].Condition.UnhealthyConnection != "uplink-a" {
		t.Fatal("missing unhealthy connection on the configured flow or bucket")
	}

	both := strings.Replace(
		document,
		`"unhealthy-connection":`,
		`"healthy-connection": "uplink-a", "unhealthy-connection":`,
		1,
	)
	if _, err := networkload.LoadOVS(
		writeDocument(t, []byte(both)),
		"../yangpub/schema",
	); err == nil {
		t.Fatal("LoadOVS accepted two health conditions on one entry")
	}

	if _, err := networkjson.Decode([]byte(both)); err == nil {
		t.Fatal("Decode accepted two health conditions on one entry")
	}
}

func TestLoadOVSGroupBucketCount(t *testing.T) {
	const wantError = "An indirect OpenFlow group requires exactly one bucket."

	base := readFile(t, "testdata/ovs.json")

	tests := []struct {
		name  string
		group string
		valid bool
	}{
		{name: "empty indirect", group: `{"id":1,"type":"indirect"}`},
		{
			name:  "one indirect bucket",
			group: `{"id":1,"type":"indirect","bucket":[{"id":1}]}`,
			valid: true,
		},
		{
			name:  "two indirect buckets",
			group: `{"id":1,"type":"indirect","bucket":[{"id":1},{"id":2}]}`,
		},
		{name: "empty select", group: `{"id":1,"type":"select"}`, valid: true},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				document := replaceOnce(
					t,
					base,
					`"port":`,
					`"openflow":{"socket":"/run/openvswitch/br-mwan.mgmt","group":[`+test.group+`]},"port":`,
				)
				_, err := networkload.LoadOVS(writeDocument(t, document), "../yangpub/schema")
				if test.valid {
					if err != nil {
						t.Fatal(err)
					}

					return
				}

				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("LoadOVS error = %v, want %q", err, wantError)
				}
			},
		)
	}
}
