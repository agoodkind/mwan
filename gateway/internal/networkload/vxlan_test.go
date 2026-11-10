package networkload_test

import (
	"fmt"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/wanconfig"
)

const vxlanInstancePath = "../../yang/instances/network-vxlan.json"

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
	if configured == nil || configured.VXLAN == nil || configured.VXLAN.VNI != 0 || *configured.TTL != 64 {
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
	if configured.VXLAN.VNI != 0 || configured.VXLAN.SourcePortMin != 49152 || configured.VXLAN.SourcePortMax != 49153 || *configured.TTL != 64 {
		t.Fatal("Apply shared mutable tunnel configuration")
	}
	var gateway wanconfig.Gateway
	gateway.GuestType = cfg.IfMgr.GuestType
	gateway.InternalIface = loaded.InternalIface
	gateway.Connections = cfg.IfMgr.Connections
	items, err := wanconfig.ConfigItems(gateway)
	if err != nil {
		t.Fatal(err)
	}
	const base = "/ietf-interfaces:interfaces/interface[name='vxlan0']/goodkind-mwan-steering:link/tunnel/vxlan/"
	want := map[string]string{"vni": "0", "source-port-min": "49152", "source-port-max": "49153", "destination-port": "4789", "tos": "2"}
	for _, item := range items {
		if !strings.HasPrefix(item.Path, base) {
			continue
		}
		leaf := strings.TrimPrefix(item.Path, base)
		if value, present := want[leaf]; !present || item.Value != value {
			t.Fatalf("unexpected published VXLAN leaf: %+v", item)
		}
		delete(want, leaf)
	}
	if len(want) != 0 {
		t.Fatalf("publication omitted VXLAN leaves: %+v", want)
	}
}

func TestVXLANLoadRequiresContainerFieldsAndRanges(t *testing.T) {
	base := readFile(t, vxlanInstancePath)
	schema := writeSchemaDir(t)
	for _, entry := range []struct{ name, before, after string }{
		{"missing-vni", `"vni": 0, `, ""},
		{"missing-source-min", `"source-port-min": 49152, `, ""},
		{"missing-source-max", `"source-port-max": 49153, `, ""},
		{"unsupported-fixed-source", `"source-port-min": 49152, "source-port-max": 49153, `, `"source-port": 49152, `},
		{"missing-destination", `"destination-port": 4789, `, ""},
		{"missing-tos", `, "tos": 2`, ""},
		{"vni-overflow", `"vni": 0`, `"vni": 16777216`},
		{"source-min-zero", `"source-port-min": 49152`, `"source-port-min": 0`},
		{"source-max-zero", `"source-port-max": 49153`, `"source-port-max": 0`},
		{"equal-source-limits", `"source-port-max": 49153`, `"source-port-max": 49152`},
		{"reversed-source-limits", `"source-port-min": 49152`, `"source-port-min": 49154`},
		{"source-min-overflow", `"source-port-min": 49152`, `"source-port-min": 65536`},
		{"source-max-overflow", `"source-port-max": 49153`, `"source-port-max": 65536`},
		{"destination-zero", `"destination-port": 4789`, `"destination-port": 0`},
		{"destination-overflow", `"destination-port": 4789`, `"destination-port": 65536`},
		{"tos-overflow", `"tos": 2`, `"tos": 256`},
		{"wrong-protocol", `"protocol": "vxlan"`, `"protocol": "6in4"`},
	} {
		t.Run(entry.name, func(t *testing.T) {
			document := replaceOnce(t, base, entry.before, entry.after)
			if _, err := networkload.Load(writeDocument(t, document), schema); err == nil {
				t.Fatal("public loader accepted invalid VXLAN settings")
			}
		})
	}
	t.Run("maximum-values", func(t *testing.T) {
		document := replaceOnce(t, base, `"vni": 0`, `"vni": 16777215`)
		document = replaceOnce(t, document, `"source-port-min": 49152`, `"source-port-min": 65534`)
		document = replaceOnce(t, document, `"source-port-max": 49153`, `"source-port-max": 65535`)
		document = replaceOnce(t, document, `"destination-port": 4789`, `"destination-port": 65535`)
		document = replaceOnce(t, document, `"tos": 2`, `"tos": 255`)
		if _, err := networkload.Load(writeDocument(t, document), schema); err != nil {
			t.Fatalf("public loader rejected maximum VXLAN values: %v", err)
		}
	})
	for _, entry := range []struct {
		name             string
		minimum, maximum uint16
	}{
		{name: "minimum-range", minimum: 1, maximum: 2},
		{name: "full-range", minimum: 1, maximum: 65535},
	} {
		t.Run(entry.name, func(t *testing.T) {
			document := replaceOnce(t, base, `"source-port-min": 49152`, fmt.Sprintf(`"source-port-min": %d`, entry.minimum))
			document = replaceOnce(t, document, `"source-port-max": 49153`, fmt.Sprintf(`"source-port-max": %d`, entry.maximum))
			if _, err := networkload.Load(writeDocument(t, document), schema); err != nil {
				t.Fatalf("public loader rejected valid VXLAN source-port range %d..%d: %v", entry.minimum, entry.maximum, err)
			}
		})
	}
}
