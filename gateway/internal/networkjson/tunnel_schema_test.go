//go:build cgo

package networkjson_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkload"
)

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
			name: "limited broadcast remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "255.255.255.255",`},
		},
		{
			name: "reserved remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "240.0.0.1",`},
		},
		{
			name: "current network remote-address",
			edit: documentEdit{old: tunnelRemote, replacement: `"remote-address": "0.1.2.3",`},
		},
		{
			name: "loopback local-address",
			edit: documentEdit{old: tunnelLocal, replacement: `"local-address": "127.0.0.1",`},
		},
		{
			name: "multicast local-address",
			edit: documentEdit{old: tunnelLocal, replacement: `"local-address": "224.0.0.5",`},
		},
		{
			name: "limited broadcast local-address",
			edit: documentEdit{old: tunnelLocal, replacement: `"local-address": "255.255.255.255",`},
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
