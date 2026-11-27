package networkload_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/networkload"
)

func TestLoadOVSRejectsUnknownRuleFields(t *testing.T) {
	base := readFile(t, "testdata/ovs-health.json")

	for _, test := range []struct {
		name, entry, field string
	}{
		{"flow raw match", `"name": "send-to-uplink",`, `"raw-match": ["gAAKAggA"],`},
		{
			"flow raw instruction",
			`"name": "send-to-uplink",`,
			`"raw-instruction": ["AAEACAEAAAA="],`,
		},
		{"flow input", `"name": "send-to-uplink",`, `"in-port": "wan0",`},
		{"flow protocol", `"name": "send-to-uplink",`, `"ether-type": 2048,`},
		{"flow output", `"name": "send-to-uplink",`, `"output": ["wan0"],`},
		{"flow group", `"name": "send-to-uplink",`, `"group-id": 1,`},
		{"group property", `"type": "select",`, `"raw-property": ["AAAAAAA="],`},
		{"bucket action", `"id": 7,`, `"raw-action": ["AAAAAAA="],`},
		{"bucket property", `"id": 7,`, `"raw-property": ["AAAAAAA="],`},
		{"bucket output", `"id": 7,`, `"output": "wan0",`},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				document := replaceOnce(t, base, test.entry, test.entry+test.field)
				_, err := networkload.LoadOVS(writeDocument(t, document), "../yangpub/schema")
				if err == nil || !strings.Contains(err.Error(), "not found") {
					t.Fatalf("LoadOVS did not reject an unsupported rule field: %v", err)
				}
			},
		)
	}
}
