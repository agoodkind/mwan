package networkload_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/networkload"
)

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
