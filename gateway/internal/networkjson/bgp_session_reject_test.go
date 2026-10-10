package networkjson_test

import (
	"testing"

	"goodkind.io/mwan/internal/networkjson"
)

const (
	bgpTunnelSession = "interface tun6in4: wan tunnel bgp-session vps: "
	bgpTunnelExport  = "interface tun6in4: wan tunnel bgp-session vps export 2001:db8:b01::/60: "
	bgpTunnelHold    = `"hold": 30,`
	bgpTunnelMetric  = `"route-metric": 300,`
	bgpDirectBackup  = `"backup-for": ["transit-a"],`
)

func bgpDecodeFailure(t *testing.T, document []byte) string {
	t.Helper()
	loaded, err := networkjson.Decode(document)
	if err != nil {
		return err.Error()
	}
	if len(loaded.Rejected) == 0 {
		t.Fatalf("Decode accepted the document: WAN=%+v", loaded.WAN)
	}
	return loaded.Rejected[0].Err.Error()
}

func TestDecodeRejectsInvalidBGPSessions(t *testing.T) {
	secondSession := `{ "name": "vps", "peer-address": "2001:db8:6::9", "local-as": 64512, "remote-as": 64513, ` +
		`"router-id": "192.0.2.130", "keepalive": 10, "hold": 30, "route-metric": 400 },` + "\n            {\n" +
		`              "name": "vps",`
	cases := []struct {
		name  string
		path  string
		edits []planEdit
		want  string
	}{
		{
			name:  "duplicate session name",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: "{\n" + `              "name": "vps",`, replacement: secondSession}},
			want:  `interface tun6in4: wan tunnel: bgp-session name "vps" is duplicated`,
		},
		{
			name:  "zero local ASN",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"local-as": 64512,`, replacement: `"local-as": 0,`}},
			want:  bgpTunnelSession + "local-as must not be 0",
		},
		{
			name:  "zero remote ASN",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"remote-as": 64512,`, replacement: `"remote-as": 0,`}},
			want:  bgpTunnelSession + "remote-as must not be 0",
		},
		{
			name:  "address families of the peer and local addresses differ",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"local-address": "2001:db8:6::2",`, replacement: `"local-address": "192.0.2.131",`}},
			want:  bgpTunnelSession + "local-address 192.0.2.131 and peer-address 2001:db8:6::1 use different address families",
		},
		{
			name:  "IPv4 import prefix",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"prefix": "2001:db8:50::/44", "min-length": 44`, replacement: `"prefix": "198.51.100.0/24", "min-length": 24`}},
			want:  bgpTunnelSession + "import prefix 198.51.100.0/24 is not an IPv6 prefix",
		},
		{
			name:  "IPv4 export prefix",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"prefix": "2001:db8:b01::/60",`, replacement: `"prefix": "198.51.100.0/24",`}},
			want:  bgpTunnelSession + "export prefix 198.51.100.0/24 is not an IPv6 prefix",
		},
		{
			name:  "hold below three seconds",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: bgpTunnelHold, replacement: `"hold": 2,`}, {old: `"keepalive": 10,`, replacement: `"keepalive": 1,`}},
			want:  bgpTunnelSession + "hold 2 must be 0 or between 3 and 65535",
		},
		{
			name:  "hold of zero",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: bgpTunnelHold, replacement: `"hold": 0,`}},
			want:  bgpTunnelSession + "hold 0 disables the hold timer, and the embedded speaker cannot disable it",
		},
		{
			name:  "keepalive equal to hold",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"keepalive": 10,`, replacement: `"keepalive": 30,`}},
			want:  bgpTunnelSession + "keepalive 30 must be below hold 30",
		},
		{
			name:  "route-metric of zero",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: bgpTunnelMetric, replacement: `"route-metric": 0,`}},
			want:  bgpTunnelSession + "route-metric 0 collides with the kernel default metric 1024",
		},
		{
			name:  "route-metric of the kernel default",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: bgpTunnelMetric, replacement: `"route-metric": 1024,`}},
			want:  bgpTunnelSession + "route-metric 1024 collides with the kernel default metric 1024",
		},
		{
			name:  "export without a next hop",
			path:  bgpTunnelFixture,
			edits: []planEdit{{old: `"next-hop": "2001:db8:6::2",`, replacement: ``}},
			want:  bgpTunnelExport + "next-hop is required",
		},
		{
			name:  "route-metric of another session",
			path:  bgpDirectFixture,
			edits: []planEdit{{old: `"route-metric": 310,`, replacement: `"route-metric": 300,`}},
			want:  "wan transit-b bgp-session upstream: route-metric 300 is already taken by wan transit-a bgp-session upstream",
		},
		{
			name: "route-metric of a configured route",
			path: bgpTunnelFixture,
			edits: []planEdit{{
				old: `"address": [{ "ip": "2001:db8:6::2", "prefix-length": 64 }],`,
				replacement: `"address": [{ "ip": "2001:db8:6::2", "prefix-length": 64 }], ` +
					`"goodkind-mwan-steering:gateway": "2001:db8:6::1", "goodkind-mwan-steering:route-metric": 300,`,
			}},
			want: "wan tunnel-6in4 bgp-session vps: route-metric 300 is already taken by a configured ipv6 route of interface tun6in4",
		},
		{
			name:  "backup mode without backup-for",
			path:  bgpDirectFixture,
			edits: []planEdit{{old: bgpDirectBackup, replacement: ``}},
			want:  "wan transit bgp-session upstream export 2001:db8:b01::/60: mode backup requires backup-for",
		},
		{
			name:  "backup-for without a matching connection",
			path:  bgpDirectFixture,
			edits: []planEdit{{old: bgpDirectBackup, replacement: `"backup-for": ["transit-c"],`}},
			want:  "wan transit-b bgp-session upstream: export 2001:db8:b01::/60 backup-for transit-c is not a connection ID",
		},
		{
			name:  "backup-for of the session's own connection",
			path:  bgpDirectFixture,
			edits: []planEdit{{old: bgpDirectBackup, replacement: `"backup-for": ["transit-b"],`}},
			want:  "wan transit-b bgp-session upstream: export 2001:db8:b01::/60 backup-for transit-b is the session's own connection",
		},
		{
			name: "provider without an IPv6 family",
			path: bgpDirectFixture,
			edits: []planEdit{{
				old: `"targets-v6": []` + "\n          }",
				replacement: `"targets-v6": []` + "\n          }," + ` "bgp-session": [{ "name": "upstream", "peer-address": "2001:db8:179:c::1", ` +
					`"local-as": 64512, "remote-as": 64513, "router-id": "192.0.2.132", "keepalive": 10, "hold": 30, "route-metric": 320 }]`,
			}},
			want: "wan isp: a bgp-session requires an ipv6 family",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := bgpDecodeFailure(t, bgpDocument(t, testCase.path, testCase.edits...))
			if got != testCase.want {
				t.Fatalf("Decode error = %q, want %q", got, testCase.want)
			}
		})
	}
}
