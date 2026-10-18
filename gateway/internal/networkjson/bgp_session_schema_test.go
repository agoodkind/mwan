//go:build cgo

package networkjson_test

import (
	"errors"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkload"
)

const (
	bgpTunnelRemoteAS  = `"remote-as": 64512,`
	bgpTunnelKeepalive = `"keepalive": 10,`
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
		{name: "connect-retry below two seconds", edit: documentEdit{old: `"keepalive": 10,`, replacement: `"keepalive": 10, "connect-retry": 1,`}},
		{name: "graceful-restart-time of zero", edit: documentEdit{old: `"keepalive": 10,`, replacement: `"keepalive": 10, "graceful-restart-time": 0,`}},
		{name: "graceful-restart-time above the capability field", edit: documentEdit{old: `"keepalive": 10,`, replacement: `"keepalive": 10, "graceful-restart-time": 4096,`}},
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

func withTunnelSessionMembers(t *testing.T, base string, members string) string {
	t.Helper()
	start := strings.Index(base, bgpTunnelRemoteAS)
	end := strings.Index(base, bgpTunnelKeepalive)
	if start < 0 || end < start {
		t.Fatalf("the tunnel session lacks %s before %s", bgpTunnelRemoteAS, bgpTunnelKeepalive)
	}
	start += len(bgpTunnelRemoteAS)
	return base[:start] + " " + members + " " + base[end:]
}

func requireSchemaFailure(t *testing.T, document string, want string) {
	t.Helper()
	_, err := networkload.Load(writeDocument(t, document), schemaDirForTest(t))
	if err == nil || !strings.HasPrefix(err.Error(), "validate ") {
		t.Fatalf("Load error = %v, want a schema validation error", err)
	}
	if failure := errors.Unwrap(err).Error(); !strings.Contains(failure, want) {
		t.Fatalf("schema error = %s, want text %s", failure, want)
	}
}

func TestLoadRejectsBGPSessionRouterID(t *testing.T) {
	_, data := readInstance(t, "network-bgp-tunnel.json")
	document := withTunnelSessionMembers(t, string(data), `"router-id": "192.0.2.130",`)
	requireSchemaFailure(t, document, `Node "router-id" not found as a child of "bgp-session" node`)
}

func TestLoadRejectsBGPSessionWithoutValidFamily(t *testing.T) {
	_, data := readInstance(t, "network-bgp-tunnel.json")
	base := string(data)
	cases := []struct {
		name    string
		members string
		want    string
	}{
		{name: "unknown family", members: `"family": ["ipv4-unicast"],`, want: `Invalid enumeration value "ipv4-unicast"`},
		{name: "empty family", members: `"family": [],`, want: `Too few "family" instances`},
		{name: "absent family", members: ``, want: `Too few "family" instances`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			requireSchemaFailure(t, withTunnelSessionMembers(t, base, testCase.members), testCase.want)
		})
	}
}
