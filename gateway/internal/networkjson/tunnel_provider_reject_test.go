package networkjson_test

import (
	"testing"

	"goodkind.io/mwan/internal/networkjson"
)

func TestDecodeRejectsInvalidTunnelProviders(t *testing.T) {
	plainLink := `{ "name": "enmgmt0", "type": "iana-if-type:other" }`
	cases := []struct {
		name  string
		edits []planEdit
		want  string
	}{
		{
			name:  "no IPv6 gateway",
			edits: []planEdit{{old: tunnelProviderGateway, replacement: ``}},
			want:  "interface tun6in4: a 6in4 tunnel provider requires an ipv6 default route with an explicit gateway",
		},
		{
			name: "configured default route without a gateway",
			edits: []planEdit{{
				old:         tunnelProviderGateway,
				replacement: `"goodkind-mwan-steering:route": [{ "destination": "::/0" }],`,
			}},
			want: "interface tun6in4: a 6in4 tunnel provider requires an ipv6 default route with an explicit gateway",
		},
		{
			name:  "underlay that is not a provider",
			edits: []planEdit{{old: tunnelProviderUnderlay, replacement: `"underlay": "enmgmt0",`}},
			want:  "interface tun6in4: tunnel provider underlay enmgmt0 is not a provider",
		},
		{
			name: "underlay provider without an IPv4 family",
			edits: []planEdit{
				{old: tunnelProviderUnderlay, replacement: `"underlay": "enonlyv6",`},
				{
					old: plainLink,
					replacement: plainLink + `,
      {
        "name": "enonlyv6",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored",
        "ietf-ip:ipv6": { "goodkind-mwan-steering:translation": { "mode": "native" } },
        "goodkind-mwan-steering:wan": {
          "name": "onlyv6", "table-id": 300, "fw-mark": 3, "fw-mark-prio": 300, "from-prio": 57
        },
        "goodkind-mwan-steering:steering": { "tier": 1, "weight": 1 }
      }`,
				},
			},
			want: "interface tun6in4: tunnel provider underlay enonlyv6 has no ipv4 family",
		},
		{
			name: "IPv4 family on the tunnel provider",
			edits: []planEdit{{
				old: `"ietf-ip:ipv6": {` + "\n" + `          "address": [{ "ip": "2001:db8:6::2", "prefix-length": 64 }],`,
				replacement: `"ietf-ip:ipv4": { "goodkind-mwan-steering:translation": { "mode": "native" } },` + "\n" +
					`        "ietf-ip:ipv6": {` + "\n" + `          "address": [{ "ip": "2001:db8:6::2", "prefix-length": 64 }],`,
			}},
			want: "interface tun6in4: a 6in4 tunnel provider cannot include an ipv4 family",
		},
		{
			name:  "no IPv6 probe target",
			edits: []planEdit{{old: tunnelProviderTargets, replacement: `"targets-v6": []`}},
			want:  "interface tun6in4: a 6in4 tunnel provider requires an enabled health probe with targets-v6",
		},
		{
			name: "IPv4 probe target",
			edits: []planEdit{{
				old: tunnelProviderTargets, replacement: tunnelProviderTargets + `, "targets-v4": ["192.0.2.11"]`,
			}},
			want: "interface tun6in4: a 6in4 tunnel provider cannot probe targets-v4",
		},
		{
			name:  "table-id of the underlay provider",
			edits: []planEdit{{old: `"table-id": 200,`, replacement: `"table-id": 100,`}},
			want:  "wan tunnel-6in4: table-id 100 is already taken by wan isp",
		},
		{
			name:  "fw-mark of the underlay provider",
			edits: []planEdit{{old: `"fw-mark": 2,`, replacement: `"fw-mark": 1,`}},
			want:  "wan tunnel-6in4: fw-mark 1 is already taken by wan isp",
		},
		{
			name:  "rule priority of the underlay provider",
			edits: []planEdit{{old: `"from-prio": 56,`, replacement: `"from-prio": 100,`}},
			want:  "wan tunnel-6in4: from-prio 100 is already taken by wan isp's fw-mark-prio",
		},
		{
			name:  "tunnel provider as the pinned provider",
			edits: []planEdit{{old: tunnelProviderPinSets, replacement: `"pinned-provider": "tunnel", ` + tunnelProviderPin}},
			want:  "firewall pin target tunnel has no ipv4 family",
		},
		{
			name: "tunnel provider as the pinned connection",
			edits: []planEdit{{
				old: tunnelProviderPinSets, replacement: `"pinned-connection-id": "tunnel-6in4", ` + tunnelProviderPin,
			}},
			want: "firewall pin target tunnel has no ipv4 family",
		},
		{
			name:  "forced DSCP on the tunnel provider",
			edits: []planEdit{{old: `"fw-mark-prio": 200,`, replacement: `"fw-mark-prio": 200, "forced-dscp": 8,`}},
			want:  "firewall provider tunnel: forced-dscp requires an ipv4 family",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loaded, err := networkjson.Decode(tunnelProviderDocument(t, testCase.edits...))
			if err == nil {
				t.Fatalf("Decode accepted the document: WAN=%+v rejected=%+v", loaded.WAN, loaded.Rejected)
			}
			if err.Error() != testCase.want {
				t.Fatalf("Decode error = %q, want %q", err, testCase.want)
			}
		})
	}
}
