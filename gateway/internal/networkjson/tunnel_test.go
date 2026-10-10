//go:build cgo

package networkjson_test

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
)

const (
	tunnelInstance = "network-tunnel.json"
	tunnelMTU      = `"mtu": 1480,`
	tunnelOwner    = `"goodkind-mwan-steering:owner": "mwan",`
	tunnelUnderlay = `"underlay": "enatt0",`
	tunnelRemote   = `"remote-address": "198.51.100.1",`
	tunnelLocal    = `"local-address": "192.0.2.130",`
)

func secondTunnel(underlay string, remote string) documentEdit {
	return documentEdit{
		old: internalLink,
		replacement: internalLink + `,
      {
        "name": "tun6in4b",
        "type": "iana-if-type:tunnel",
        "goodkind-mwan-steering:connection-id": "tunnel-6in4-b",
        "goodkind-mwan-steering:owner": "mwan",
        "goodkind-mwan-steering:link": {
          "tunnel": {
            "protocol": "6in4",
            "underlay": "` + underlay + `",
            "remote-address": "` + remote + `",
            "local-address": "192.0.2.130"
          }
        }
      }`,
	}
}

func TestLoadTunnelInstance(t *testing.T) {
	path, _ := readInstance(t, tunnelInstance)

	loaded, err := networkload.Load(path, schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	connection := requireConnection(t, loaded.Connections, "tun6in4")
	if connection.Owner != interfaceintent.OwnerMWAN || connection.Link == nil {
		t.Fatalf("tunnel connection = %+v", connection)
	}
	if connection.Link.Kind != interfaceintent.KindTunnel || connection.Link.MTU == nil || *connection.Link.MTU != 1480 {
		t.Fatalf("tunnel link = %+v", connection.Link)
	}
	want := &interfaceintent.Tunnel{
		Protocol: interfaceintent.TunnelProtocol6in4, Underlay: "enatt0",
		Remote: netip.MustParseAddr("198.51.100.1"), Local: netip.MustParseAddr("192.0.2.130"),
		TTL: new(uint8(64)),
	}
	if !reflect.DeepEqual(connection.Link.Tunnel, want) {
		t.Fatalf("tunnel = %+v, want %+v", connection.Link.Tunnel, want)
	}
}

func TestDecodeRejectsInvalidTunnels(t *testing.T) {
	_, data := readInstance(t, tunnelInstance)
	base := string(data)
	if _, err := networkjson.Decode(data); err != nil {
		t.Fatalf("Decode rejected the valid tunnel instance: %v", err)
	}
	cases := []struct {
		name string
		edit documentEdit
		want string
	}{
		{
			name: "owner other than mwan",
			edit: documentEdit{old: tunnelOwner, replacement: `"goodkind-mwan-steering:owner": "external",`},
			want: "tunnel link requires owner mwan",
		},
		{
			name: "underlay absent from the configuration",
			edit: documentEdit{old: tunnelUnderlay, replacement: `"underlay": "enmissing0",`},
			want: "tunnel underlay enmissing0 is not declared",
		},
		{
			name: "underlay is the tunnel",
			edit: documentEdit{old: tunnelUnderlay, replacement: `"underlay": "tun6in4",`},
			want: "tunnel underlay must be another interface",
		},
		{
			name: "underlay is another tunnel",
			edit: secondTunnel("tun6in4", "198.51.100.2"),
			want: "tunnel underlay tun6in4 is a tunnel",
		},
		{
			name: "tunnel with vlan",
			edit: documentEdit{old: tunnelMTU, replacement: tunnelMTU + ` "vlan": { "parent": "enatt0", "id": 7 },`},
			want: "link cannot include both a tunnel and a vlan",
		},
		{
			name: "tunnel with bridge-master",
			edit: documentEdit{old: tunnelMTU, replacement: tunnelMTU + ` "bridge-master": "enmwanbr0",`},
			want: "link cannot include both a tunnel and a bridge-master",
		},
		{
			name: "IPv6 remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "2001:db8::1",`},
			want: "link/tunnel/remote-address 2001:db8::1 must be a unicast IPv4 address",
		},
		{
			name: "IPv6 local-address",
			edit: documentEdit{old: tunnelLocal, replacement: `"local-address": "2001:db8::2",`},
			want: "link/tunnel/local-address 2001:db8::2 must be a unicast IPv4 address",
		},
		{
			name: "two tunnels with one underlay, local, and remote",
			edit: secondTunnel("enatt0", "198.51.100.1"),
			want: "interfaces tun6in4 and tun6in4b configure the same tunnel",
		},
		{
			name: "hardware-address on a tunnel",
			edit: documentEdit{old: tunnelMTU, replacement: tunnelMTU + ` "hardware-address": "02:00:5e:00:53:09",`},
			want: "tunnel link cannot include a hardware-address",
		},
		{
			name: "match on a tunnel",
			edit: documentEdit{old: tunnelMTU, replacement: tunnelMTU + ` "match": { "driver": "igc" },`},
			want: "tunnel link cannot include a device match",
		},
		{
			name: "protocol outside the enumeration",
			edit: documentEdit{old: `"protocol": "6in4",`, replacement: `"protocol": "gre",`},
			want: `link/tunnel/protocol "gre" is not supported`,
		},
		{
			name: "mtu below the IPv6 minimum",
			edit: documentEdit{old: tunnelMTU, replacement: `"mtu": 1279,`},
			want: "6in4 tunnel mtu 1279 is below the IPv6 minimum 1280",
		},
	}
	cases = append(cases, struct {
		name string
		edit documentEdit
		want string
	}{
		name: "IPv4 address on a 6in4 tunnel",
		edit: documentEdit{
			old:         `"ietf-ip:ipv6": {` + "\n" + `          "address": [{ "ip": "2001:db8:6::2", "prefix-length": 64 }]`,
			replacement: `"ietf-ip:ipv4": {` + "\n" + `          "address": [{ "ip": "192.0.2.131", "prefix-length": 30 }]`,
		},
		want: "a 6in4 tunnel transports only IPv6",
	})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loaded, err := networkjson.Decode([]byte(editDocument(t, base, testCase.edit)))
			if rejection := rejectionText(loaded, err); !strings.Contains(rejection, testCase.want) {
				t.Fatalf("Decode rejection = %q, want it to contain %q", rejection, testCase.want)
			}
		})
	}
}

func TestDecodeRejectsTunnelMTUAboveConfiguredUnderlay(t *testing.T) {
	_, data := readInstance(t, tunnelInstance)
	ownedUnderlay := documentEdit{
		old: internalLink,
		replacement: internalLink + `,
      {
        "name": "brunder0",
        "type": "iana-if-type:bridge",
        "goodkind-mwan-steering:connection-id": "bridge-underlay",
        "goodkind-mwan-steering:owner": "mwan",
        "goodkind-mwan-steering:link": { "mtu": 1500 }
      }`,
	}
	document := editDocument(t, string(data), ownedUnderlay)
	document = editDocument(t, document, documentEdit{old: tunnelUnderlay, replacement: `"underlay": "brunder0",`})
	if _, err := networkjson.Decode([]byte(document)); err != nil {
		t.Fatalf("Decode rejected tunnel mtu 1480 under underlay mtu 1500: %v", err)
	}

	document = editDocument(t, document, documentEdit{old: tunnelMTU, replacement: `"mtu": 1481,`})

	loaded, err := networkjson.Decode([]byte(document))
	want := "tunnel mtu 1481 exceeds underlay brunder0 mtu 1500 less the 20-byte outer header"
	if rejection := rejectionText(loaded, err); !strings.Contains(rejection, want) {
		t.Fatalf("Decode rejection = %q, want it to contain %q", rejection, want)
	}
}

func TestLoadSchemaRejectsInvalidTunnels(t *testing.T) {
	schemaDir := schemaDirForTest(t)
	_, data := readInstance(t, tunnelInstance)
	base := string(data)
	cases := []struct {
		name string
		edit documentEdit
	}{
		{
			name: "IPv6 remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "2001:db8::1",`},
		},
		{
			name: "unspecified remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "0.0.0.0",`},
		},
		{
			name: "multicast remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "239.1.1.1",`},
		},
		{
			name: "multicast local-address",
			edit: documentEdit{old: tunnelLocal, replacement: `"local-address": "224.0.0.5",`},
		},
		{
			name: "mtu below the IPv6 minimum",
			edit: documentEdit{old: tunnelMTU, replacement: `"mtu": 1279,`},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := editDocument(t, base, testCase.edit)
			_, err := networkload.Load(writeDocument(t, document), schemaDir)
			if err == nil || !strings.HasPrefix(err.Error(), "validate ") {
				t.Fatalf("Load error = %v, want a schema validation error", err)
			}
		})
	}
}
