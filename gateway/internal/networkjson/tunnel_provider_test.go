package networkjson_test

import (
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	tunnelProviderFixture = "network-tunnel-provider"
	tunnelProviderGateway = `"goodkind-mwan-steering:gateway": "2001:db8:6::1",`
	tunnelProviderNative  = `"goodkind-mwan-steering:translation": { "mode": "native" }` + "\n" + `        },` + "\n" +
		`        "goodkind-mwan-steering:wan": {` + "\n" + `          "name": "tunnel",`
	tunnelProviderTargets  = `"targets-v6": ["2001:db8:53::1"]`
	tunnelProviderUnderlay = `"underlay": "enisp0",`
	tunnelProviderPinSets  = `"pinned-set-v4-name": "isp_pinned_v4",`
	tunnelProviderPin      = `"pinned-source-v4": "192.0.2.2", "pinned-source-port": 51820, ` +
		`"pinned-destination-port": 51821, ` + tunnelProviderPinSets
)

func tunnelProviderDocument(t *testing.T, edits ...planEdit) []byte {
	t.Helper()
	path := planFixtures()[tunnelProviderFixture]
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

func decodeTunnelProvider(t *testing.T, edits ...planEdit) *networkjson.Config {
	t.Helper()
	loaded, err := networkjson.Decode(tunnelProviderDocument(t, edits...))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(loaded.Rejected) != 0 {
		t.Fatalf("Decode rejected %+v", loaded.Rejected)
	}
	return loaded
}

func TestDecodeAcceptsTunnelProviders(t *testing.T) {
	nptv6 := planEdit{
		old: tunnelProviderNative,
		replacement: `"goodkind-mwan-steering:translation": { "mode": "ietf-nat:nptv6", "nptv6": { ` +
			`"internal-prefix": "2001:db8:b01::/60", "external-source": "configured", ` +
			`"external-prefix": "2001:db8:beef:200::/60" } }` + "\n" + `        },` + "\n" +
			`        "goodkind-mwan-steering:wan": {` + "\n" + `          "name": "tunnel",`,
	}
	configuredDefault := planEdit{
		old: tunnelProviderGateway,
		replacement: `"goodkind-mwan-steering:route": [` +
			`{ "destination": "::/0", "gateway": "2001:db8:6::1", "metric": 20 }],`,
	}
	cases := []struct {
		name            string
		edits           []planEdit
		wantMode        config.TranslationMode
		wantDestination string
	}{
		{name: "native translation with the gateway shorthand", wantMode: config.TranslationNative, wantDestination: "::/0"},
		{name: "NPTv6 translation", edits: []planEdit{nptv6}, wantMode: config.TranslationNPTv6, wantDestination: "::/0"},
		{
			name: "configured default route with a gateway", edits: []planEdit{configuredDefault},
			wantMode: config.TranslationNative, wantDestination: "::/0",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loaded := decodeTunnelProvider(t, testCase.edits...)
			provider, accepted := loaded.WAN["tunnel-6in4"]
			if !accepted {
				t.Fatalf("WAN = %+v, want the tunnel provider", loaded.WAN)
			}
			if provider.TranslationV4 != nil || provider.TranslationV6 == nil || provider.TranslationV6.Mode != testCase.wantMode {
				t.Fatalf("tunnel translation = %+v / %+v, want no IPv4 family and IPv6 mode %s",
					provider.TranslationV4, provider.TranslationV6, testCase.wantMode)
			}
			if provider.Iface != "tun6in4" || provider.TableID != 200 || provider.FwMark != 2 {
				t.Fatalf("tunnel provider = %+v", provider)
			}
			key := routeKey("tun6in4", networkjson.FamilyIPv6, testCase.wantDestination)
			route, configured := loaded.ConfiguredRoutes()[key]
			if !configured || route.Gateway != netip.MustParseAddr("2001:db8:6::1") {
				t.Fatalf("tunnel default route = %+v (configured=%t), want gateway 2001:db8:6::1", route, configured)
			}
			defaults, err := loaded.ProviderDefaults()
			if err != nil {
				t.Fatalf("ProviderDefaults: %v", err)
			}
			families := make(map[string]bool)
			for defaultKey := range defaults {
				families[defaultKey.String()] = true
			}
			want := map[string]bool{"isp|ipv4|100": true, "tunnel-6in4|ipv6|200": true}
			if !reflect.DeepEqual(families, want) {
				t.Fatalf("provider table families = %v, want %v", families, want)
			}
		})
	}
}
