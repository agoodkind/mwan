package networkload_test

// The public loader converts the Linux VXLAN example into typed tunnel settings.
// These cases check that applying those settings makes an independent copy and
// that configuration publication restores named options such as vni with
// value 0. Legacy input and invalid option values exercise the same loader.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/wanconfig"
	"goodkind.io/mwan/gateway/networkload"
)

const vxlanInstancePath = "../yang/instances/network-vxlan.json"

func TestVXLANLoadApplyAndPublish(t *testing.T) {
	loaded, err := networkload.Load(vxlanInstancePath, writeSchemaDir(t))
	if err != nil {
		t.Fatal(err)
	}

	var cfg config.Config
	loaded.Apply(&cfg)
	var configured *interfaceintent.Tunnel

	for _, connection := range cfg.IfMgr.Connections {
		if connection.Name == "vxlan0" {
			configured = connection.Link.Tunnel
			if connection.IPv4 == nil || connection.IPv6 == nil {
				t.Fatal("VXLAN provider omitted an address family")
			}
		}
	}

	if configured == nil || configured.VXLAN == nil || configured.VXLAN.VNI != 0 ||
		*configured.TTL != 64 {
		t.Fatalf("applied VXLAN settings changed: %+v", configured)
	}

	for _, connection := range loaded.Connections {
		if connection.Name == "vxlan0" {
			connection.Link.Tunnel.VXLAN.VNI = 1
			connection.Link.Tunnel.VXLAN.SourcePortMin = 1
			connection.Link.Tunnel.VXLAN.SourcePortMax = 65535
			*connection.Link.Tunnel.TTL = 32
		}
	}

	if configured.VXLAN.VNI != 0 || configured.VXLAN.SourcePortMin != 49152 ||
		configured.VXLAN.SourcePortMax != 49153 ||
		*configured.TTL != 64 {
		t.Fatal("Apply shared mutable tunnel configuration")
	}

	var gateway wanconfig.Gateway
	gateway.InternalIface = loaded.InternalIface
	gateway.Connections = cfg.IfMgr.Connections
	items, err := wanconfig.ConfigItems(gateway)
	if err != nil {
		t.Fatal(err)
	}

	const base = "/ietf-interfaces:interfaces/interface[name='vxlan0']/goodkind-mwan-steering:link/tunnel/options[name='"
	want := map[string]string{
		"vni":              "0",
		"source-port-min":  "49152",
		"source-port-max":  "49153",
		"destination-port": "4789",
		"tos":              "2",
	}

	for _, item := range items {
		if !strings.HasPrefix(item.Path, base) {
			continue
		}

		leaf := strings.TrimSuffix(strings.TrimPrefix(item.Path, base), "']/value")
		if value, present := want[leaf]; !present || item.Value != value {
			t.Fatalf("unexpected published VXLAN leaf: %+v", item)
		}

		delete(want, leaf)
	}

	if len(want) != 0 {
		t.Fatalf("publication omitted VXLAN leaves: %+v", want)
	}
}

func TestVXLANLoadRequiresOptionsAndRanges(t *testing.T) {
	base := readFile(t, vxlanInstancePath)
	schema := writeSchemaDir(t)

	for _, entry := range []struct{ name, before, after string }{
		{"missing-vni", `{"name": "vni", "value": "0"}, `, ""},
		{"missing-source-min", `{"name": "source-port-min", "value": "49152"}, `, ""},
		{"missing-source-max", `{"name": "source-port-max", "value": "49153"}, `, ""},
		{
			"unsupported-fixed-source",
			`{"name": "source-port-min", "value": "49152"}, {"name": "source-port-max", "value": "49153"}, `,
			`{"name": "source-port", "value": "49152"}, `,
		},
		{"missing-destination", `{"name": "destination-port", "value": "4789"}, `, ""},
		{"missing-tos", `, {"name": "tos", "value": "2"}`, ""},
		{"vni-overflow", `{"name": "vni", "value": "0"}`, `{"name": "vni", "value": "16777216"}`},
		{
			"source-min-zero",
			`{"name": "source-port-min", "value": "49152"}`,
			`{"name": "source-port-min", "value": "0"}`,
		},
		{
			"source-max-zero",
			`{"name": "source-port-max", "value": "49153"}`,
			`{"name": "source-port-max", "value": "0"}`,
		},
		{
			"equal-source-limits",
			`{"name": "source-port-max", "value": "49153"}`,
			`{"name": "source-port-max", "value": "49152"}`,
		},
		{
			"reversed-source-limits",
			`{"name": "source-port-min", "value": "49152"}`,
			`{"name": "source-port-min", "value": "49154"}`,
		},
		{
			"source-min-overflow",
			`{"name": "source-port-min", "value": "49152"}`,
			`{"name": "source-port-min", "value": "65536"}`,
		},
		{
			"source-max-overflow",
			`{"name": "source-port-max", "value": "49153"}`,
			`{"name": "source-port-max", "value": "65536"}`,
		},
		{
			"destination-zero",
			`{"name": "destination-port", "value": "4789"}`,
			`{"name": "destination-port", "value": "0"}`,
		},
		{
			"destination-overflow",
			`{"name": "destination-port", "value": "4789"}`,
			`{"name": "destination-port", "value": "65536"}`,
		},
		{"tos-overflow", `{"name": "tos", "value": "2"}`, `{"name": "tos", "value": "256"}`},
		{"wrong-protocol", `"protocol": "vxlan"`, `"protocol": "6in4"`},
	} {
		t.Run(
			entry.name,
			func(t *testing.T) {
				document := replaceOnce(t, base, entry.before, entry.after)
				if _, err := networkload.Load(writeDocument(t, document), schema); err == nil {
					t.Fatal("public loader accepted invalid VXLAN settings")
				}
			},
		)
	}

	t.Run(
		"maximum-values",
		func(t *testing.T) {
			document := replaceOnce(
				t,
				base,
				`{"name": "vni", "value": "0"}`,
				`{"name": "vni", "value": "16777215"}`,
			)
			document = replaceOnce(
				t,
				document,
				`{"name": "source-port-min", "value": "49152"}`,
				`{"name": "source-port-min", "value": "65534"}`,
			)
			document = replaceOnce(
				t,
				document,
				`{"name": "source-port-max", "value": "49153"}`,
				`{"name": "source-port-max", "value": "65535"}`,
			)
			document = replaceOnce(
				t,
				document,
				`{"name": "destination-port", "value": "4789"}`,
				`{"name": "destination-port", "value": "65535"}`,
			)
			document = replaceOnce(
				t,
				document,
				`{"name": "tos", "value": "2"}`,
				`{"name": "tos", "value": "255"}`,
			)
			if _, err := networkload.Load(writeDocument(t, document), schema); err != nil {
				t.Fatalf("public loader rejected maximum VXLAN values: %v", err)
			}
		},
	)

	for _, entry := range []struct {
		name             string
		minimum, maximum uint16
	}{
		{name: "minimum-range", minimum: 1, maximum: 2},
		{name: "full-range", minimum: 1, maximum: 65535},
	} {
		t.Run(
			entry.name,
			func(t *testing.T) {
				document := replaceOnce(
					t,
					base,
					`{"name": "source-port-min", "value": "49152"}`,
					fmt.Sprintf(`{"name": "source-port-min", "value": "%d"}`, entry.minimum),
				)
				document = replaceOnce(
					t,
					document,
					`{"name": "source-port-max", "value": "49153"}`,
					fmt.Sprintf(`{"name": "source-port-max", "value": "%d"}`, entry.maximum),
				)
				if _, err := networkload.Load(writeDocument(t, document), schema); err != nil {
					t.Fatalf(
						"public loader rejected valid VXLAN source-port range %d..%d: %v",
						entry.minimum,
						entry.maximum,
						err,
					)
				}
			},
		)
	}
}

func TestLoadLegacyVXLANDocument(t *testing.T) {
	base := readFile(t, vxlanInstancePath)
	const options = `"options": [{"name": "vni", "value": "0"}, {"name": "source-port-min", "value": "49152"}, {"name": "source-port-max", "value": "49153"}, {"name": "destination-port", "value": "4789"}, {"name": "tos", "value": "2"}]`
	const old = `"vxlan": { "vni": 0, "source-port-min": 49152, "source-port-max": 49153, "destination-port": 4789, "tos": 2 }`
	legacy := replaceOnce(t, base, options, old)
	schema := writeSchemaDir(t)
	loaded, err := networkload.Load(writeDocument(t, legacy), schema)
	if err != nil {
		t.Fatal(err)
	}

	current, err := networkload.Load(writeDocument(t, base), schema)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(loaded.Connections, current.Connections) {
		t.Fatal("loading the legacy VXLAN block changed the configured Linux connections")
	}

	mixed := replaceOnce(t, base, options, options+", "+old)
	if _, err := networkload.Load(writeDocument(t, mixed), schema); err == nil ||
		!strings.Contains(err.Error(), "remove the legacy vxlan block") {
		t.Fatalf("loading both tunnel formats returned %v", err)
	}
}
