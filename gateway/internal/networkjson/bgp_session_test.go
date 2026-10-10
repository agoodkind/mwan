package networkjson_test

import (
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	bgpDirectFixture = "../../yang/instances/network-bgp-direct.json"
	bgpTunnelFixture = "../../yang/instances/network-bgp-tunnel.json"
	bgpHomePrefix    = "2001:db8:b01::/60"
)

func bgpDocument(t *testing.T, path string, edits ...planEdit) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	document := string(data)
	for _, edit := range edits {
		if count := strings.Count(document, edit.old); count != 1 {
			t.Fatalf("edit target %q occurs %d times, want 1", edit.old, count)
		}
		document = strings.Replace(document, edit.old, edit.replacement, 1)
	}
	return []byte(document)
}

func decodeBGPDocument(t *testing.T, path string, edits ...planEdit) *networkjson.Config {
	t.Helper()
	loaded, err := networkjson.Decode(bgpDocument(t, path, edits...))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(loaded.Rejected) != 0 {
		t.Fatalf("Decode rejected %+v", loaded.Rejected)
	}
	return loaded
}

func onlyBGPSession(t *testing.T, loaded *networkjson.Config, connection string) config.BGPSession {
	t.Helper()
	sessions := loaded.WAN[connection].BGPSessions
	if len(sessions) != 1 {
		t.Fatalf("connection %s has %d sessions, want 1", connection, len(sessions))
	}
	return sessions[0]
}

func TestDecodeBuildsDirectBGPSessions(t *testing.T) {
	loaded := decodeBGPDocument(t, bgpDirectFixture)
	home := netip.MustParsePrefix(bgpHomePrefix)
	med := uint32(10)
	want := bgpsession.Config{
		Name: "upstream", RouterID: netip.MustParseAddr("192.0.2.130"), LocalASN: 64512, RemoteASN: 64513,
		PeerAddress: netip.MustParseAddr("2001:db8:179:a::1"), PeerPort: 179,
		LocalAddress: netip.MustParseAddr("2001:db8:179:a::2"), KeepaliveSeconds: 10, HoldSeconds: 30,
		Import:      []bgpsession.ImportRule{{Prefix: netip.MustParsePrefix("::/0"), MinLength: 0, MaxLength: 0}},
		RouteMetric: 300,
		Export: []bgpsession.ExportRule{{
			Prefix: home, Mode: bgpsession.ExportAlways, MED: &med, NextHop: netip.MustParseAddr("2001:db8:179:a::2"),
			Communities:      []bgpsession.Community{{ASN: 64513, Value: 100}},
			LargeCommunities: []bgpsession.LargeCommunity{{GlobalAdmin: 64512, LocalData1: 1, LocalData2: 2}},
		}},
	}
	first := onlyBGPSession(t, loaded, "transit-a")
	if !reflect.DeepEqual(first.Session, want) {
		t.Fatalf("transit-a session = %+v, want %+v", first.Session, want)
	}
	if len(first.BackupFor) != 0 {
		t.Fatalf("transit-a backup-for = %v, want none", first.BackupFor)
	}

	second := onlyBGPSession(t, loaded, "transit-b")
	if second.Session.LocalASN != first.Session.LocalASN || second.Session.RemoteASN != first.Session.RemoteASN {
		t.Fatalf("transit-b ASNs = %d and %d, want the ASNs of transit-a", second.Session.LocalASN, second.Session.RemoteASN)
	}
	if second.Session.LocalAddress.IsValid() || second.Session.MultihopTTL != 2 || second.Session.RouteMetric != 310 {
		t.Fatalf("transit-b session = %+v", second.Session)
	}
	rule := second.Session.Export[0]
	if rule.Mode != bgpsession.ExportBackup || rule.PrependCount != 2 {
		t.Fatalf("transit-b export = %+v", rule)
	}
	wantBackup := map[netip.Prefix][]connectionid.ID{home: {"transit-a"}}
	if !reflect.DeepEqual(second.BackupFor, wantBackup) {
		t.Fatalf("transit-b backup-for = %v, want %v", second.BackupFor, wantBackup)
	}
}

func TestDecodeAcceptsBGPSessionVariants(t *testing.T) {
	nptv6 := `"goodkind-mwan-steering:translation": { "mode": "ietf-nat:nptv6", "nptv6": { ` +
		`"internal-prefix": "2001:db8:b01::/60", "external-source": "configured", "external-prefix": "2001:db8:beef:200::/60" } }`
	cases := []struct {
		name  string
		path  string
		edits []planEdit
		check func(t *testing.T, loaded *networkjson.Config)
	}{
		{
			name: "tunnel provider without an IPv6 gateway and with an iBGP session",
			path: bgpTunnelFixture,
			check: func(t *testing.T, loaded *networkjson.Config) {
				session := onlyBGPSession(t, loaded, "tunnel-6in4").Session
				if !session.Internal() || *session.Export[0].LocalPreference != 200 {
					t.Fatalf("tunnel session = %+v", session)
				}
			},
		},
		{
			name: "absent peer-port",
			path: bgpTunnelFixture,
			edits: []planEdit{{
				old: `"peer-port": 179,`, replacement: ``,
			}},
			check: func(t *testing.T, loaded *networkjson.Config) {
				if port := onlyBGPSession(t, loaded, "tunnel-6in4").Session.PeerPort; port != 179 {
					t.Fatalf("peer port = %d, want 179", port)
				}
			},
		},
		{
			name: "NPTv6 translation",
			path: bgpTunnelFixture,
			edits: []planEdit{{
				old: `"goodkind-mwan-steering:translation": { "mode": "native" }`, replacement: nptv6,
			}},
			check: func(t *testing.T, loaded *networkjson.Config) {
				if mode := loaded.WAN["tunnel-6in4"].TranslationV6.Mode; mode != config.TranslationNPTv6 {
					t.Fatalf("translation mode = %s", mode)
				}
				onlyBGPSession(t, loaded, "tunnel-6in4")
			},
		},
		{
			name: "IPv4 peer and local addresses",
			path: bgpTunnelFixture,
			edits: []planEdit{
				{old: `"peer-address": "2001:db8:6::1",`, replacement: `"peer-address": "198.51.100.9",`},
				{old: `"local-address": "2001:db8:6::2",`, replacement: `"local-address": "192.0.2.131",`},
			},
			check: func(t *testing.T, loaded *networkjson.Config) {
				if peer := onlyBGPSession(t, loaded, "tunnel-6in4").Session.PeerAddress; !peer.Is4() {
					t.Fatalf("peer address = %s", peer)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.check(t, decodeBGPDocument(t, testCase.path, testCase.edits...))
		})
	}
}
