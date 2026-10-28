package networkjson_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

const vxlanSettings = `"vxlan":{"vni":0,"source-port":49152,"destination-port":4789,"tos":2},`

func vxlanDocument(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/vxlan-min.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDecodeVXLANIdentity(t *testing.T) {
	const internal = `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`
	for _, entry := range []struct {
		name      string
		protocol  string
		settings  string
		accepted  bool
		rejection string
	}{
		{name: "duplicate", protocol: "vxlan", settings: vxlanSettings, accepted: false, rejection: "configure the same tunnel"},
		{name: "different-VNI", protocol: "vxlan", settings: strings.Replace(vxlanSettings, `"vni":0`, `"vni":1`, 1), accepted: true, rejection: ""},
		{name: "different-source-port", protocol: "vxlan", settings: strings.Replace(vxlanSettings, `"source-port":49152`, `"source-port":49153`, 1), accepted: false, rejection: "share VXLAN receiver"},
		{name: "different-destination-port", protocol: "vxlan", settings: strings.Replace(vxlanSettings, `"destination-port":4789`, `"destination-port":4790`, 1), accepted: true, rejection: ""},
		{name: "different-protocol", protocol: "6in4", settings: "", accepted: true, rejection: ""},
	} {
		t.Run(entry.name, func(t *testing.T) {
			second := fmt.Sprintf(`{"name":"vxlan-other","type":"iana-if-type:tunnel","goodkind-mwan-steering:connection-id":"vxlan-other",
"goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"tunnel":{
"protocol":%q,%s"underlay":"enatt0","remote-address":"198.51.100.1","local-address":"192.0.2.130"}}}`, entry.protocol, entry.settings)
			document := strings.Replace(vxlanDocument(t), internal, second+","+internal, 1)
			_, err := networkjson.Decode([]byte(document))
			if (err == nil) != entry.accepted {
				t.Fatalf("tunnel identity acceptance=%v expected=%v error=%v", err == nil, entry.accepted, err)
			}
			if !entry.accepted && !strings.Contains(err.Error(), entry.rejection) {
				t.Fatalf("duplicate identity failed for another reason: %v", err)
			}
		})
	}
}

func TestDecodeVXLANTransport(t *testing.T) {
	loaded, err := networkjson.Decode([]byte(vxlanDocument(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, connection := range loaded.Connections {
		if connection.Name != "vxlan0" {
			continue
		}
		tunnel := connection.Link.Tunnel
		if tunnel.Protocol != interfaceintent.TunnelProtocolVXLAN || tunnel.VXLAN == nil ||
			*tunnel.VXLAN != (interfaceintent.VXLAN{VNI: 0, SourcePort: 49152, DestinationPort: 4789, TOS: 2}) {
			t.Fatalf("decoded VXLAN settings changed: %+v", tunnel)
		}
		return
	}
	t.Fatal("decoded configuration omitted the VXLAN connection")
}

func TestDecodeVXLANRequiresTypedSettings(t *testing.T) {
	for _, entry := range []struct{ name, before, after string }{
		{"missing-container", vxlanSettings, ""},
		{"missing-vni", `"vni":0,`, ""},
		{"missing-source-port", `"source-port":49152,`, ""},
		{"missing-destination-port", `"destination-port":4789,`, ""},
		{"missing-tos", `,"tos":2`, ""},
		{"vni-overflow", `"vni":0`, `"vni":16777216`},
		{"zero-source-port", `"source-port":49152`, `"source-port":0`},
		{"zero-destination-port", `"destination-port":4789`, `"destination-port":0`},
		{"port-overflow", `"source-port":49152`, `"source-port":65536`},
		{"tos-overflow", `"tos":2`, `"tos":256`},
		{"SIT-container", `"protocol":"vxlan"`, `"protocol":"6in4"`},
	} {
		t.Run(entry.name, func(t *testing.T) {
			document := strings.Replace(vxlanDocument(t), entry.before, entry.after, 1)
			if _, err := networkjson.Decode([]byte(document)); err == nil {
				t.Fatal("decoder accepted invalid VXLAN settings")
			}
		})
	}
}
