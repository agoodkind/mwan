package networkload_test

// Both network document entry points check where OpenFlow can use a DHCP lease.
// One case tries an address rewrite inside an indirect group's bucket and on
// the flow that calls the group. It also checks a lease inside a nested clone
// action and a lease on a select group's bucket.

import (
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/networkjson"
	"goodkind.io/mwan/gateway/networkload"
)

func TestOVSBucketLeaseReferences(t *testing.T) {
	const (
		fixedBucket   = `{"id": 1, "name": "output", "argument": "wan0"}`
		referringFlow = `{"id": 1, "name": "group", "argument": "1"}`
		leasedFlow    = `{"id": 1, "name": "set_field", "argument": "${dhcpv4:uplink-a}->ip_src"},` +
			`{"id": 2, "name": "group", "argument": "1"}`
		dhcpv4Action = `{"id": 1, "name": "set_field", "argument": "${dhcpv4:uplink-a}->ip_src"}`
		dhcpv6Action = `{"id": 1, "name": "set_field", "argument": "${dhcpv6-pd:uplink-a}->ipv6_src"}`
		cloneAction  = `{"id": 1, "name": "clone", ` +
			`"argument": "set_field:${dhcpv4:uplink-a}->ip_src,output:wan0"}`
		context  = "OpenFlow group 1 bucket 7: an indirect group bucket cannot reference DHCP leases"
		guidance = "Put lease-dependent actions on referring flows"
	)
	base := replaceOnce(
		t,
		readFile(t, "testdata/ovs-health.json"),
		`"id": 7, "condition": {"healthy-connection": "uplink-a"}`,
		`"id": 7`,
	)

	for _, test := range []struct {
		name          string
		groupType     string
		bucketActions string
		flowActions   string
		reject        bool
	}{
		{"indirect DHCPv4 bucket", "indirect", dhcpv4Action, referringFlow, true},
		{"indirect DHCPv6 PD bucket", "indirect", dhcpv6Action, referringFlow, true},
		{"indirect bucket with nested DHCP action", "indirect", cloneAction, referringFlow, true},
		{"indirect fixed bucket", "indirect", fixedBucket, referringFlow, false},
		{
			"indirect group with DHCP action on referring flow",
			"indirect",
			fixedBucket,
			leasedFlow,
			false,
		},
		{"select DHCP bucket", "select", dhcpv4Action, referringFlow, false},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				document := replaceOnce(t, base, `"type": "select"`, `"type": "`+test.groupType+`"`)
				document = replaceOnce(t, document, fixedBucket, test.bucketActions)
				document = replaceOnce(t, document, referringFlow, test.flowActions)

				_, loadErr := networkload.LoadOVS(writeDocument(t, document), "../yangpub/schema")
				_, decodeErr := networkjson.Decode(document)

				for _, result := range []struct {
					entryPoint string
					err        error
				}{
					{"LoadOVS", loadErr},
					{"Decode", decodeErr},
				} {
					if !test.reject {
						if result.err != nil {
							t.Errorf("%s rejected the document: %v", result.entryPoint, result.err)
						}

						continue
					}

					if result.err == nil || !strings.Contains(result.err.Error(), context) ||
						!strings.Contains(result.err.Error(), guidance) {
						t.Errorf(
							"%s error = %v, want %q and %q",
							result.entryPoint,
							result.err,
							context,
							guidance,
						)
					}
				}
			},
		)
	}
}
