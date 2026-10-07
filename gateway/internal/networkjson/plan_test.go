package networkjson_test

import (
	"maps"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	planBaseDocument = "testdata/plan-base.json"

	planManagementLink = `{ "name": "enmgmt0", "type": "iana-if-type:other" }`
	planManagementIPv4 = `"ietf-ip:ipv4": { "goodkind-mwan-steering:gateway": "192.0.2.1", ` +
		`"goodkind-mwan-steering:route-metric": 10, "goodkind-mwan-steering:route": [` +
		`{ "destination": "198.18.2.0/24", "gateway": "192.0.2.3", "metric": 50 }] }`
)

type planEdit struct {
	old         string
	replacement string
}

var planWebpassEdits = []planEdit{
	{
		old: `"goodkind-mwan-steering:translation": {` + "\n" + `            "mode": "ietf-nat:napt44",`,
		replacement: `"goodkind-mwan-steering:route-metric": 20, "goodkind-mwan-steering:route": [` +
			`{ "destination": "198.18.0.0/24", "gateway": "203.0.113.1" }, { "destination": "198.18.1.0/24" }], ` +
			`"goodkind-mwan-steering:translation": {` + "\n" + `            "mode": "ietf-nat:napt44",`,
	},
	{
		old: `"goodkind-mwan-steering:dhcp": false,`,
		replacement: `"goodkind-mwan-steering:dhcp": false, "goodkind-mwan-steering:gateway": "2001:db8:beef:200::1", ` +
			`"goodkind-mwan-steering:route": [` +
			`{ "destination": "2001:DB8:A00::/48", "gateway": "2001:db8:beef:200::1", "metric": 2048 }],`,
	},
}

func planDocument(t *testing.T, edits ...planEdit) []byte {
	t.Helper()
	data, err := os.ReadFile(planBaseDocument)
	if err != nil {
		t.Fatalf("read %s: %v", planBaseDocument, err)
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

func decodePlanDocument(t *testing.T, edits ...planEdit) *networkjson.Config {
	t.Helper()
	loaded, err := networkjson.Decode(planDocument(t, edits...))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(loaded.Rejected) != 0 {
		t.Fatalf("Decode rejected %+v", loaded.Rejected)
	}
	return loaded
}

func configuredRoute(destination string, gateway string, metric uint32, source networkjson.RouteSource) networkjson.ConfiguredRoute {
	address := netip.Addr{}
	if gateway != "" {
		address = netip.MustParseAddr(gateway)
	}
	return networkjson.ConfiguredRoute{
		RouteIntent: interfaceintent.RouteIntent{
			Destination: netip.MustParsePrefix(destination),
			Gateway:     address,
			TableID:     254,
			Metric:      metric,
		},
		Source: source,
	}
}

func routeKey(iface string, family networkjson.Family, destination string) networkjson.RouteKey {
	return networkjson.RouteKey{Interface: iface, Family: family, Destination: netip.MustParsePrefix(destination)}
}

func TestConfiguredRoutesProjectEveryOwner(t *testing.T) {
	owners := []struct {
		name string
		link string
	}{
		{name: "external", link: `{ "name": "enmgmt0", "type": "iana-if-type:other", `},
		{
			name: "networkd",
			link: `{ "name": "enmgmt0", "type": "iana-if-type:other", ` +
				`"goodkind-mwan-steering:link-files": "rendered", ` +
				`"goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:78" } }, `,
		},
		{
			name: "mwan",
			link: `{ "name": "enmgmt0", "type": "iana-if-type:other", ` +
				`"goodkind-mwan-steering:connection-id": "management", "goodkind-mwan-steering:owner": "mwan", ` +
				`"goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:78" } }, `,
		},
	}
	want := map[networkjson.RouteKey]networkjson.ConfiguredRoute{
		routeKey("enmgmt0", networkjson.FamilyIPv4, "0.0.0.0/0"): configuredRoute(
			"0.0.0.0/0", "192.0.2.1", 10, networkjson.RouteSourceGateway),
		routeKey("enmgmt0", networkjson.FamilyIPv4, "198.18.2.0/24"): configuredRoute(
			"198.18.2.0/24", "192.0.2.3", 50, networkjson.RouteSourceRoute),
		routeKey("enwebpass0", networkjson.FamilyIPv4, "198.18.0.0/24"): configuredRoute(
			"198.18.0.0/24", "203.0.113.1", 0, networkjson.RouteSourceRoute),
		routeKey("enwebpass0", networkjson.FamilyIPv4, "198.18.1.0/24"): configuredRoute(
			"198.18.1.0/24", "", 0, networkjson.RouteSourceRoute),
		routeKey("enwebpass0", networkjson.FamilyIPv6, "::/0"): configuredRoute(
			"::/0", "2001:db8:beef:200::1", 0, networkjson.RouteSourceGateway),
		routeKey("enwebpass0", networkjson.FamilyIPv6, "2001:db8:a00::/48"): configuredRoute(
			"2001:db8:a00::/48", "2001:db8:beef:200::1", 2048, networkjson.RouteSourceRoute),
	}
	wantKeys := []string{
		"enmgmt0|ipv4|0.0.0.0/0",
		"enmgmt0|ipv4|198.18.2.0/24",
		"enwebpass0|ipv4|198.18.0.0/24",
		"enwebpass0|ipv4|198.18.1.0/24",
		"enwebpass0|ipv6|2001:db8:a00::/48",
		"enwebpass0|ipv6|::/0",
	}
	for _, owner := range owners {
		t.Run(owner.name, func(t *testing.T) {
			management := planEdit{old: planManagementLink, replacement: owner.link + planManagementIPv4 + " }"}
			loaded := decodePlanDocument(t, append(slices.Clone(planWebpassEdits), management)...)
			routes := loaded.ConfiguredRoutes()
			if !maps.Equal(routes, want) {
				t.Fatalf("ConfiguredRoutes:\ngot  %+v\nwant %+v", routes, want)
			}
			keys := make([]string, 0, len(routes))
			for key := range routes {
				keys = append(keys, key.String())
			}
			slices.Sort(keys)
			if !slices.Equal(keys, wantKeys) {
				t.Fatalf("route keys = %q, want %q", keys, wantKeys)
			}
		})
	}
}

func TestConfiguredRoutesRejectGatewayWithDefaultRouteEntry(t *testing.T) {
	management := planEdit{
		old: planManagementLink,
		replacement: `{ "name": "enmgmt0", "type": "iana-if-type:other", "ietf-ip:ipv4": { ` +
			`"goodkind-mwan-steering:gateway": "192.0.2.1", ` +
			`"goodkind-mwan-steering:route": [{ "destination": "0.0.0.0/0", "gateway": "192.0.2.3" }] } }`,
	}
	_, err := networkjson.Decode(planDocument(t, management))
	want := "interface enmgmt0: ipv4 route destination 0.0.0.0/0 is configured twice"
	if err == nil || err.Error() != want {
		t.Fatalf("Decode error = %v, want %q", err, want)
	}
}

func providerDefault(iface string, family networkjson.Family, tableID uint32, destination string) networkjson.ProviderDefault {
	return networkjson.ProviderDefault{
		Interface:           iface,
		Family:              family,
		TableID:             tableID,
		InternalDestination: netip.MustParsePrefix(destination),
		InternalInterface:   "enmwanbr0",
	}
}

func requireProviderDefaults(t *testing.T, loaded *networkjson.Config, want map[string]networkjson.ProviderDefault) {
	t.Helper()
	defaults, err := loaded.ProviderDefaults()
	if err != nil {
		t.Fatalf("ProviderDefaults: %v", err)
	}
	got := make(map[string]networkjson.ProviderDefault, len(defaults))
	for key, value := range defaults {
		if key.Family != value.Family || key.TableID != value.TableID {
			t.Fatalf("key %s disagrees with value %+v", key, value)
		}
		got[key.String()] = value
	}
	if !maps.Equal(got, want) {
		t.Fatalf("ProviderDefaults:\ngot  %+v\nwant %+v", got, want)
	}
}

func baseProviderDefaults() map[string]networkjson.ProviderDefault {
	return map[string]networkjson.ProviderDefault{
		"webpass|ipv4|200":      providerDefault("enwebpass0", networkjson.FamilyIPv4, 200, "192.0.2.0/29"),
		"webpass|ipv6|200":      providerDefault("enwebpass0", networkjson.FamilyIPv6, 200, "2001:db8:b01:fe::2/128"),
		"att|ipv4|100":          providerDefault("enatt0", networkjson.FamilyIPv4, 100, "192.0.2.0/29"),
		"att|ipv6|100":          providerDefault("enatt0", networkjson.FamilyIPv6, 100, "2001:db8:b01:fe::2/128"),
		"monkeybrains|ipv4|300": providerDefault("enmbrains0", networkjson.FamilyIPv4, 300, "192.0.2.0/29"),
	}
}

func TestProviderDefaultsProjectTranslatedFamilies(t *testing.T) {
	requireProviderDefaults(t, decodePlanDocument(t), baseProviderDefaults())
}

func TestProviderDefaultsIgnoreMainTableGatewayAndMetric(t *testing.T) {
	requireProviderDefaults(t, decodePlanDocument(t, planWebpassEdits...), baseProviderDefaults())
}

func TestProviderDefaultsOmitRejectedProviders(t *testing.T) {
	rejectMonkeybrains := planEdit{old: `"from-prio": 57,`, replacement: ``}
	loaded, err := networkjson.Decode(planDocument(t, rejectMonkeybrains))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(loaded.Rejected) != 1 || loaded.Rejected[0].Interface != "enmbrains0" {
		t.Fatalf("Rejected = %+v, want enmbrains0", loaded.Rejected)
	}
	want := baseProviderDefaults()
	delete(want, "monkeybrains|ipv4|300")
	requireProviderDefaults(t, loaded, want)
}

func TestProviderDefaultsRejectTableIDOutsideUint32(t *testing.T) {
	for _, tableID := range []string{"-1", "4294967296"} {
		loaded := decodePlanDocument(t, planEdit{old: `"table-id": 200,`, replacement: `"table-id": ` + tableID + `,`})
		defaults, err := loaded.ProviderDefaults()
		want := "wan webpass: table-id " + tableID + " is outside 0 to 4294967295"
		if err == nil || err.Error() != want {
			t.Fatalf("ProviderDefaults = %+v, %v; want error %q", defaults, err, want)
		}
	}
}
