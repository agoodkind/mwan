//go:build cgo

package networkjson_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkload"
)

func TestLoadSchemaRejectsInvalidBGPSessions(t *testing.T) {
	schemaDir := schemaDirForTest(t)
	path, data := readInstance(t, "network-bgp-tunnel.json")
	if _, err := networkload.Load(path, schemaDir); err != nil {
		t.Fatalf("Load rejected the valid instance: %v", err)
	}
	base := string(data)
	cases := []struct {
		name string
		edit documentEdit
	}{
		{name: "hold of zero", edit: documentEdit{old: bgpTunnelHold, replacement: `"hold": 0,`}},
		{name: "hold below three seconds", edit: documentEdit{old: bgpTunnelHold, replacement: `"hold": 2,`}},
		{name: "keepalive equal to hold", edit: documentEdit{old: `"keepalive": 10,`, replacement: `"keepalive": 30,`}},
		{name: "keepalive above hold", edit: documentEdit{old: `"keepalive": 10,`, replacement: `"keepalive": 31,`}},
		{name: "unspecified router-id", edit: documentEdit{old: `"router-id": "192.0.2.130",`, replacement: `"router-id": "0.0.0.0",`}},
		{
			name: "backup-for with the mode always",
			edit: documentEdit{old: `"mode": "always",`, replacement: `"mode": "always", "backup-for": ["isp"],`},
		},
		{name: "the mode backup without backup-for", edit: documentEdit{old: `"mode": "always",`, replacement: `"mode": "backup",`}},
		{name: "route-metric of the kernel default", edit: documentEdit{old: bgpTunnelMetric, replacement: `"route-metric": 1024,`}},
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
