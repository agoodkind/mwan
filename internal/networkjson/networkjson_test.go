package networkjson_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// schemaDirForTest materialises the model set the gateway installs, from the
// bytes the binary embeds, so the loader is checked against the schema that
// ships rather than against a second copy kept beside it.
func schemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := yangpub.WriteSchema(dir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	return dir
}

// validDocument is one gateway's network tree: two providers in different
// tiers, one on a rendered link with a static IPv4 address and a delegation
// client, and one whose unit files are hand-authored and which carries no
// probe at all, plus the internal link and the group-wide values. Addresses
// are documentation prefixes.
const validDocument = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "rendered",
        "goodkind-mwan-steering:link": {
          "match": { "driver": "igc" },
          "hardware-address": "02:00:5e:00:53:01"
        },
        "ietf-ip:ipv4": {
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:napt44",
            "static-mapping": [
              { "external": "203.0.113.2", "internal": "192.0.2.2" },
              { "external": "203.0.113.3", "internal": "192.0.2.3" }
            ]
          },
          "forwarding": true,
          "address": [{ "ip": "203.0.113.2", "prefix-length": 29 }],
          "goodkind-mwan-steering:dhcp": false,
          "goodkind-mwan-steering:gateway": "203.0.113.1",
          "goodkind-mwan-steering:route-metric": 10
        },
        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:nptv6",
            "nptv6": {
              "internal-prefix": "2001:db8:b01::/60",
              "external-source": "delegated",
              "expected-prefix": "2001:db8:beef:200::/60"
            }
          },
          "forwarding": true,
          "goodkind-mwan-steering:dhcp": true,
          "goodkind-mwan-steering:accept-ra": true,
          "goodkind-mwan-steering:delegation": {
            "hint": "::/56",
            "duid-type": "link-layer-time",
            "duid": "00:01:2a:5b:3c:4d:02:00:5e:00:53:01"
          }
        },
        "goodkind-mwan-steering:steering": { "tier": 0, "weight": 2 },
        "goodkind-mwan-steering:wan": {
          "name": "webpass",
          "table-id": 200,
          "fw-mark": 2,
          "fw-mark-prio": 200,
          "from-prio": 56,
          "health": {
            "enabled": true,
            "ping-count": 3,
            "success-threshold": 2,
            "failure-threshold": 2,
            "recovery-threshold": 2,
            "check-interval": 10,
            "targets-v4": ["192.0.2.10", "192.0.2.11"],
            "targets-v6": ["2001:db8:53::1", "2001:db8:53::2"],
            "http-urls": ["https://example.test/ip"]
          }
        }
      },
      {
        "name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored",
        "ietf-ip:ipv4": {
          "goodkind-mwan-steering:translation": { "mode": "ietf-nat:napt44" }
        },
        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:translation": { "mode": "native" }
        },
        "goodkind-mwan-steering:steering": { "tier": 1, "weight": 1 },
        "goodkind-mwan-steering:wan": {
          "name": "att",
          "table-id": 100,
          "fw-mark": 1,
          "fw-mark-prio": 100,
          "from-prio": 55
        }
      },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }
    ],
    "goodkind-mwan-steering:steering-group": {
      "hash-mode": "source",
      "reserved-tables": [400, 500],
      "translation": {
        "internal-prefix": "2001:db8:b01::/60",
        "opnsense-edge-v6": "2001:db8:b01:fe::2",
        "mwanbr-edge-v6": "2001:db8:b01:fe::3"
      },
      "routes": {
        "internal-iface": "enmwanbr0",
        "internal-net-v4": "192.0.2.0/29"
      },
      "health": { "probe-timeout": 2000 }
    }
  }
}`

// writeDocument puts body in a file the loader can read.
func writeDocument(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

// requireOneRejection asserts that Load refused exactly the provider entry on
// iface, with an error containing want, and loaded the other provider of
// validDocument in full. The daemon runs on that other provider.
func requireOneRejection(t *testing.T, loaded *networkjson.Config, iface string, provider string, want string) {
	t.Helper()
	if len(loaded.Rejected) != 1 {
		t.Fatalf("rejected = %+v, want exactly one entry", loaded.Rejected)
	}
	got := loaded.Rejected[0]
	if got.Interface != iface || got.Provider != provider {
		t.Fatalf("rejected %s/%s, want %s/%s", got.Interface, got.Provider, iface, provider)
	}
	if got.Err == nil || !strings.Contains(got.Err.Error(), want) {
		t.Fatalf("rejection error = %v, want it to contain %q", got.Err, want)
	}
	if _, present := loaded.WAN[provider]; present {
		t.Fatalf("rejected provider %s is still in WAN", provider)
	}
	if _, present := loaded.Health[provider]; present {
		t.Fatalf("rejected provider %s is still in Health", provider)
	}
	for _, connection := range loaded.Connections {
		if connection.Name == iface {
			t.Fatalf("rejected interface %s still has a connection", iface)
		}
	}
	other := "att"
	if provider == "att" {
		other = "webpass"
	}
	if _, present := loaded.WAN[other]; !present {
		t.Fatalf("provider %s is missing; a rejection must leave the other provider loaded", other)
	}
	if got := len(loaded.WAN); got != 1 {
		t.Fatalf("provider count = %d, want 1", got)
	}
}

func requireConnection(t *testing.T, connections []interfaceintent.Connection, name string) interfaceintent.Connection {
	t.Helper()
	for _, connection := range connections {
		if connection.Name == name {
			return connection
		}
	}
	t.Fatalf("connection %s missing from %+v", name, connections)
	return interfaceintent.Connection{}
}

func TestLoadValidFile(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.WAN); got != 2 {
		t.Fatalf("provider count = %d, want 2", got)
	}
	if got := loaded.WAN["webpass"].Iface; got != "enwebpass0" {
		t.Fatalf("webpass iface = %q, want enwebpass0", got)
	}
	if got := loaded.WAN["webpass"].TableID; got != 200 {
		t.Fatalf("webpass table id = %d, want 200", got)
	}
	if got := loaded.WAN["webpass"].V4Source; got != "203.0.113.2" {
		t.Fatalf("webpass v4 source = %q, want 203.0.113.2", got)
	}
	if got := loaded.WAN["att"].V4Source; got != "" {
		t.Fatalf("att v4 source = %q, want empty", got)
	}
	if _, probed := loaded.Health["att"]; probed {
		t.Fatal("att carries no health container and must hold no probe")
	}
	if got := loaded.Health["webpass"].CheckIntervalSeconds; got == nil || *got != 10 {
		t.Fatalf("webpass check interval = %v, want 10", got)
	}
	if got := loaded.Health["webpass"].SuccessThreshold; got == nil || *got != 2 {
		t.Fatalf("webpass success threshold = %v, want 2", got)
	}
	if got := loaded.ProbeTimeoutMillis; got != 2000 {
		t.Fatalf("probe timeout = %d, want 2000", got)
	}
	if got := loaded.InternalPrefix; got != "2001:db8:b01::/60" {
		t.Fatalf("internal prefix = %q, want 2001:db8:b01::/60", got)
	}
	if got := loaded.InternalIface; got != "enmwanbr0" {
		t.Fatalf("internal iface = %q, want enmwanbr0", got)
	}
	var staticBase, mappedAdditional bool
	for _, claim := range loaded.Claims {
		if claim.Kind == interfaceintent.ResourceStaticAddress && claim.Key == "enwebpass0/203.0.113.2/29" && claim.Writer == interfaceintent.WriterNetworkd {
			staticBase = true
		}
		if claim.Kind == interfaceintent.ResourceMappedAddress && claim.Key == "203.0.113.3" && claim.Writer == interfaceintent.WriterWANRoutes {
			mappedAdditional = true
		}
		if claim.Kind == interfaceintent.ResourceMappedAddress && claim.Key == "203.0.113.2" {
			t.Fatal("base static address has a second mapped-address writer")
		}
	}
	if !staticBase || !mappedAdditional {
		t.Fatalf("address claims omit base static or additional mapping: %+v", loaded.Claims)
	}
}

func TestLoadDoesNotClaimRoutedMappingAsAnAddress(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `"external": "203.0.113.3"`, `"external": "198.51.100.193"`, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, claim := range loaded.Claims {
		if claim.Kind == interfaceintent.ResourceMappedAddress && claim.Key == "198.51.100.193" {
			t.Fatal("routed translation mapping acquired an address writer")
		}
	}
}

func TestLoadRejectsUnsupportedRenderedSettings(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]struct{ old, replacement string }{
		"disabled interface": {`"name": "enwebpass0",`, `"name": "enwebpass0", "enabled": false,`},
		"disabled family":    {`"ietf-ip:ipv4": {`, `"ietf-ip:ipv4": { "enabled": false,`},
		"delegation iaid":    {`"hint": "::/56",`, `"hint": "::/56", "iaid": 42,`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := strings.Replace(validDocument, change.old, change.replacement, 1)
			loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("Load rejected all providers: %v", err)
			}
			if len(loaded.Rejected) != 1 || loaded.Rejected[0].Provider != "webpass" || len(loaded.WAN) != 1 {
				t.Fatalf("unsupported setting did not reject only webpass: rejected=%+v wan=%+v", loaded.Rejected, loaded.WAN)
			}
		})
	}
}

func TestLoadRejectsCaseVariantDeviceMatch(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `"match": { "driver": "igc" }`, `"match": { "hardware-address": "02:00:5E:00:53:01" }`, 1)
	body = strings.Replace(body, `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`, `{ "name": "enmwanbr0", "type": "iana-if-type:other", "goodkind-mwan-steering:link-files": "rendered", "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:01" } } }`, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil || !strings.Contains(err.Error(), "match the same device") {
		t.Fatalf("case-variant MAC match error = %v", err)
	}
}

func TestLoadUsesConnectionIdentityForSharedProviderLabel(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `"name": "enwebpass0",`, `"name": "enwebpass0", "goodkind-mwan-steering:connection-id": "sonic-a",`, 1)
	body = strings.Replace(body, `"name": "enatt0",`, `"name": "enatt0", "goodkind-mwan-steering:connection-id": "sonic-b",`, 1)
	body = strings.Replace(body, `"name": "att",`, `"name": "webpass",`, 1)
	body = strings.Replace(body, `"name": "enmwanbr0",`, `"name": "enmwanbr0", "goodkind-mwan-steering:connection-id": "internal",`, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.WAN) != 2 || loaded.WAN["sonic-a"].Iface != "enwebpass0" || loaded.WAN["sonic-b"].Iface != "enatt0" {
		t.Fatalf("connection-keyed providers = %+v", loaded.WAN)
	}
	if loaded.WAN["sonic-a"].ProviderName != "webpass" || loaded.WAN["sonic-b"].ProviderName != "webpass" {
		t.Fatalf("provider display labels changed: %+v", loaded.WAN)
	}
	if _, found := loaded.Health["sonic-a"]; !found {
		t.Fatalf("sonic-a health policy missing: %+v", loaded.Health)
	}
	if loaded.ConnectionIDs["enmwanbr0"] != "internal" {
		t.Fatalf("internal identity = %q", loaded.ConnectionIDs["enmwanbr0"])
	}
}

func TestLoadRejectsDuplicateNormalizedConnectionIdentity(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `"name": "enatt0",`, `"name": "enatt0", "goodkind-mwan-steering:connection-id": "webpass",`, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil || !strings.Contains(err.Error(), `connection-id "webpass" is shared`) {
		t.Fatalf("duplicate connection identity error = %v", err)
	}
}

func TestLoadRejectsConnectionIdentityThatCannotRoundTrip(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"health separator":    `sonic:a`,
		"leading whitespace":  ` sonic`,
		"trailing whitespace": `sonic `,
		"line break":          `sonic\nother`,
		"carriage return":     `sonic\rother`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := strings.Replace(validDocument, `"name": "enwebpass0",`,
				`"name": "enwebpass0", "goodkind-mwan-steering:connection-id": "`+value+`",`, 1)
			_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err == nil || !strings.Contains(err.Error(), "invalid connection-id") {
				t.Fatalf("connection-id %q: error = %v", value, err)
			}
		})
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := networkjson.Load(writeDocument(t, "{not json"), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a file that is not JSON")
	}
}

func TestLoadRejectsSchemaViolation(t *testing.T) {
	t.Parallel()

	// The schema bounds fw-mark at 1 or higher, which is the check the daemon
	// makes on every provider today.
	body := strings.Replace(validDocument, `"fw-mark": 2,`, `"fw-mark": 0,`, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a zero firewall mark")
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	t.Parallel()

	_, err := networkjson.Load(filepath.Join(t.TempDir(), "absent.json"), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a missing file")
	}
}

func TestLoadRejectsMissingRequiredLeaf(t *testing.T) {
	t.Parallel()

	// The schema leaves table-id optional, because a leaf's type cannot see
	// whether the daemon needs it. The loader is where that requirement lives,
	// so an absent table id must fail rather than default to zero.
	body := strings.Replace(validDocument, `"table-id": 100,`, ``, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's table id: %v", err)
	}
	requireOneRejection(t, loaded, "enatt0", "att", "wan att: table-id is required")
}

func TestLoadFailsWhenEveryProviderIsRejected(t *testing.T) {
	t.Parallel()

	// A document with no loadable provider gives the daemon nothing to steer,
	// and starting on it would silently route every LAN flow by the main table.
	body := strings.Replace(validDocument, `"table-id": 100,`, ``, 1)
	body = strings.Replace(body, `"table-id": 200,`, ``, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a document with no loadable provider")
	}
	if !strings.Contains(err.Error(), "every provider entry was rejected") {
		t.Fatalf("error does not state that every entry was rejected: %v", err)
	}
}

func TestLoadAcceptsAnIPv4OnlyProvider(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:translation": { "mode": "native" }
        },`, "", 1)
	if body == validDocument {
		t.Fatal("IPv6 policy was not removed")
	}
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	att := loaded.WAN["att"]
	if att.TranslationV6 != nil || att.TranslationV4 == nil {
		t.Fatalf("IPv4-only policy = %+v", att)
	}
	if got := loaded.WAN["webpass"].TranslationV6.NPT.ExpectedPrefix; got != netip.MustParsePrefix("2001:db8:beef:200::/60") {
		t.Fatalf("expected delegation = %s", got)
	}
}

func TestLoadAcceptsADisabledProbeWithNoSettings(t *testing.T) {
	t.Parallel()

	// A disabled probe is the second way a provider goes unprobed, and the
	// daemon reads none of its settings, so a container carrying only the flag
	// must load rather than stop the daemon over counts nobody consumes.
	body := strings.Replace(
		validDocument,
		`"from-prio": 55`,
		`"from-prio": 55, "health": {"enabled": false}`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a disabled probe with no settings: %v", err)
	}
	probe, present := loaded.Health["att"]
	if !present {
		t.Fatal("att carries a disabled health container and must hold a probe entry")
	}
	if probe.Enabled {
		t.Fatal("att probe loaded as enabled")
	}
	if probe.PingCount != nil || len(probe.TargetsV6) != 0 {
		t.Fatalf("disabled probe carries settings the file left out: %+v", probe)
	}
}

func TestLoadKeepsEverySettingOfADisabledProbe(t *testing.T) {
	t.Parallel()

	// The inventory renders all nine settings for a disabled probe, and the
	// management surface serves the probe from the loaded section. A setting
	// dropped here would be missing from the served tree while the file still
	// carries it.
	body := strings.Replace(
		validDocument,
		`"from-prio": 55`,
		`"from-prio": 55, "health": {
            "enabled": false,
            "ping-count": 4,
            "success-threshold": 1,
            "failure-threshold": 5,
            "recovery-threshold": 6,
            "check-interval": 0,
            "targets-v4": ["192.0.2.20"],
            "targets-v6": ["2001:db8:53::20"],
            "http-urls": ["https://example.test/att"]
          }`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a disabled probe carrying its settings: %v", err)
	}
	probe, present := loaded.Health["att"]
	if !present || probe.Enabled {
		t.Fatalf("att probe = %+v, present = %v, want a disabled probe", probe, present)
	}
	counts := map[string]*int{
		"ping-count":         probe.PingCount,
		"success-threshold":  probe.SuccessThreshold,
		"failure-threshold":  probe.FailureThreshold,
		"recovery-threshold": probe.RecoveryThreshold,
		"check-interval":     probe.CheckIntervalSeconds,
	}
	want := map[string]int{
		"ping-count":         4,
		"success-threshold":  1,
		"failure-threshold":  5,
		"recovery-threshold": 6,
		// Zero is a value the model accepts, so it must survive as a value
		// rather than read as a leaf the file left out.
		"check-interval": 0,
	}
	for leaf, value := range want {
		got := counts[leaf]
		if got == nil || *got != value {
			t.Fatalf("health/%s = %v, want %d", leaf, got, value)
		}
	}
	if !reflect.DeepEqual(probe.TargetsV4, []string{"192.0.2.20"}) ||
		!reflect.DeepEqual(probe.TargetsV6, []string{"2001:db8:53::20"}) ||
		!reflect.DeepEqual(probe.HTTPURLs, []string{"https://example.test/att"}) {
		t.Fatalf("disabled probe lists = %+v, want the file's lists", probe)
	}
}

func TestLoadCarriesStaticMappings(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := []config.StaticMapping{
		{External: netip.MustParseAddr("203.0.113.2"), Internal: netip.MustParseAddr("192.0.2.2")},
		{External: netip.MustParseAddr("203.0.113.3"), Internal: netip.MustParseAddr("192.0.2.3")},
	}
	if got := loaded.WAN["webpass"].TranslationV4.StaticMappings; !reflect.DeepEqual(got, want) {
		t.Fatalf("webpass static mappings = %v, want %v", got, want)
	}
	if got := loaded.WAN["att"].TranslationV4.StaticMappings; len(got) != 0 {
		t.Fatalf("att static mappings = %v, want none", got)
	}
}

func TestLoadRejectsAnExternalAddressMappedByTwoProviders(t *testing.T) {
	t.Parallel()

	// The schema keys the list inside one provider and cannot see another
	// provider's list. Two providers translating one address would each own it
	// wherever it is on-link, so which link carries it would be undefined.
	body := strings.Replace(validDocument,
		`"goodkind-mwan-steering:translation": { "mode": "ietf-nat:napt44" }`,
		`"goodkind-mwan-steering:translation": { "mode": "ietf-nat:napt44", "static-mapping": [{ "external": "203.0.113.3", "internal": "192.0.2.4" }] }`, 1)

	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted one external address mapped by two providers")
	}
	if !strings.Contains(err.Error(), "static-mapping") || !strings.Contains(err.Error(), "203.0.113.3") {
		t.Fatalf("error does not name the list and the address: %v", err)
	}
}

func TestLoadCarriesSteeringAndTheGroupSettings(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := loaded.WAN["webpass"].Tier; got != 0 {
		t.Fatalf("webpass tier = %d, want 0", got)
	}
	if got := loaded.WAN["webpass"].Weight; got != 2 {
		t.Fatalf("webpass weight = %d, want 2", got)
	}
	if got := loaded.WAN["att"].Tier; got != 1 {
		t.Fatalf("att tier = %d, want 1", got)
	}
	if got := loaded.WAN["att"].Weight; got != 1 {
		t.Fatalf("att weight = %d, want 1", got)
	}
	if got := loaded.HashMode; got != "source" {
		t.Fatalf("hash mode = %q, want source", got)
	}
	if !reflect.DeepEqual(loaded.ReservedTables, []int{400, 500}) {
		t.Fatalf("reserved tables = %v, want [400 500]", loaded.ReservedTables)
	}
}

func TestLoadRejectsAProviderWithNoSteeringContainer(t *testing.T) {
	t.Parallel()

	// Tier decides which providers carry traffic, so a provider that does not
	// say where it sits cannot be steered at all. The schema cannot require the
	// container, because an interface that carries no provider must be free of
	// it, so the requirement lives here.
	body := strings.Replace(
		validDocument,
		`"goodkind-mwan-steering:steering": { "tier": 1, "weight": 1 },`,
		``,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's steering container: %v", err)
	}
	requireOneRejection(t, loaded, "enatt0", "att", "wan att: steering is required")
}

func TestLoadRejectsAMissingWeight(t *testing.T) {
	t.Parallel()

	// The schema defaults weight to 1 for the served tree. That default never
	// reaches the file the daemon decodes, so a document with no weight would
	// silently balance a provider at zero share. The loader refuses it instead.
	body := strings.Replace(
		validDocument,
		`"goodkind-mwan-steering:steering": { "tier": 0, "weight": 2 },`,
		`"goodkind-mwan-steering:steering": { "tier": 0 },`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's weight: %v", err)
	}
	requireOneRejection(t, loaded, "enwebpass0", "webpass", "wan webpass: steering/weight is required")
}

func TestLoadRejectsAMissingHashMode(t *testing.T) {
	t.Parallel()

	// The renderer always emits the hash mode, and the steering module switches
	// on it, so an absent value is a rendering fault rather than a request for
	// the schema default.
	body := strings.Replace(validDocument, `"hash-mode": "source",`, ``, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a steering group with no hash mode")
	}
	if !strings.Contains(err.Error(), "hash-mode") {
		t.Fatalf("error does not name the missing leaf: %v", err)
	}
}

func TestLoadRejectsDuplicateRoutingNumbers(t *testing.T) {
	t.Parallel()

	// Each of the four numbers addresses a distinct kernel slot, and two
	// providers sharing one means the second silently takes the first's
	// traffic. Nothing derives them, so this is the only check that catches a
	// typo in inventory. fw-mark-prio and from-prio also collide with each
	// other, not just with their own kind, because both select an ip rule by
	// the same numeric priority and the routing module treats them as one
	// slot space.
	cases := map[string]struct {
		from  string
		to    string
		leaf  string
		leaf2 string
	}{
		"table":         {from: `"table-id": 100,`, to: `"table-id": 200,`, leaf: "table-id"},
		"mark":          {from: `"fw-mark": 1,`, to: `"fw-mark": 2,`, leaf: "fw-mark"},
		"mark priority": {from: `"fw-mark-prio": 100,`, to: `"fw-mark-prio": 200,`, leaf: "fw-mark-prio"},
		"from priority": {from: `"from-prio": 55`, to: `"from-prio": 56`, leaf: "from-prio"},
		"mark priority takes from priority": {
			from:  `"fw-mark-prio": 100,`,
			to:    `"fw-mark-prio": 56,`,
			leaf:  "fw-mark-prio",
			leaf2: "from-prio",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := strings.Replace(validDocument, tc.from, tc.to, 1)
			_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err == nil {
				t.Fatalf("Load accepted a duplicate %s", tc.leaf)
			}
			if !strings.Contains(err.Error(), tc.leaf) {
				t.Fatalf("error does not name %s: %v", tc.leaf, err)
			}
			if tc.leaf2 != "" && !strings.Contains(err.Error(), tc.leaf2) {
				t.Fatalf("error does not name %s: %v", tc.leaf2, err)
			}
		})
	}
}

func TestLoadAcceptsAForcedDSCP(t *testing.T) {
	t.Parallel()

	// One provider carrying a forced DSCP value is the shape the gateway
	// inventory renders, so the schema and the loader must both accept it.
	body := strings.Replace(
		validDocument,
		`"from-prio": 55`,
		`"from-prio": 55, "forced-dscp": 8`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a provider with a forced DSCP value: %v", err)
	}
	// The management surface serves the value from the loaded entry, so the
	// loader must hold it rather than only check it.
	if got := loaded.WAN["att"].ForcedDSCP; got != 8 {
		t.Fatalf("att forced DSCP = %d, want 8", got)
	}
	if got := loaded.WAN["webpass"].ForcedDSCP; got != 0 {
		t.Fatalf("webpass forced DSCP = %d, want 0 for a provider carrying none", got)
	}
}

func TestLoadRejectsAZeroForcedDSCP(t *testing.T) {
	t.Parallel()

	// Every unmarked packet carries DSCP zero, so a provider forcing it would
	// take all internal traffic. The schema's range is what refuses it.
	body := strings.Replace(
		validDocument,
		`"from-prio": 55`,
		`"from-prio": 55, "forced-dscp": 0`,
		1,
	)
	if _, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t)); err == nil {
		t.Fatal("Load accepted a forced DSCP value of zero")
	}
}

func TestLoadRejectsADuplicateForcedDSCP(t *testing.T) {
	t.Parallel()

	// A later firewall rule overwrites an earlier rule's mark, so two providers
	// sharing a value would silently send every tagged flow to the last one.
	body := strings.Replace(
		validDocument,
		`"from-prio": 55`,
		`"from-prio": 55, "forced-dscp": 8`,
		1,
	)
	body = strings.Replace(
		body,
		`"from-prio": 56,`,
		`"from-prio": 56, "forced-dscp": 8,`,
		1,
	)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted two providers sharing a forced DSCP value")
	}
	if !strings.Contains(err.Error(), "forced-dscp 8") {
		t.Fatalf("error does not name the shared value: %v", err)
	}
}

func TestLoadRejectsAProviderOnAReservedTable(t *testing.T) {
	t.Parallel()

	// The tunnel holds table 400 and the out-of-band path holds 500. A provider
	// on either would install a default route the tunnel's own rules then
	// select, which is an outage nobody would attribute to an inventory edit.
	body := strings.Replace(validDocument, `"table-id": 100,`, `"table-id": 400,`, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider on a reserved table")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error does not say the table is reserved: %v", err)
	}
}

func TestLoadRejectsAProviderOnAKernelTable(t *testing.T) {
	t.Parallel()

	// The kernel's own tables are reserved whether or not inventory says so,
	// because they are not inventory values. Table 254 is main: a provider
	// there would replace the host's own default route.
	body := strings.Replace(validDocument, `"table-id": 100,`, `"table-id": 254,`, 1)
	body = strings.Replace(body, `"reserved-tables": [400, 500],`, ``, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider on a kernel table")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error does not say the table is reserved: %v", err)
	}
}

func TestLoadAcceptsAGroupWithNoReservedTables(t *testing.T) {
	t.Parallel()

	// An empty leaf-list renders as an absent key, so a gateway that reserves
	// nothing beyond the kernel's own tables must load.
	body := strings.Replace(validDocument, `"reserved-tables": [400, 500],`, ``, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a group with no reserved tables: %v", err)
	}
	if len(loaded.ReservedTables) != 0 {
		t.Fatalf("reserved tables = %v, want none", loaded.ReservedTables)
	}
}

// TestLoadCarriesADeclaredEmptyIPv6TargetList is the loader half of the
// IPv4-only provider. The health module reads nil and empty differently: a
// family the provider named no targets for inherits the module-wide public
// resolvers, and a family it declared empty stays empty. That distinction only
// holds if an empty targets-v6 leaf-list survives the schema and the decode as
// an empty list rather than as an absent leaf, so this pins the schema's answer
// to an empty JSON array and the shape the daemon receives. Losing either would
// make a provider on a link with no IPv6 ping public resolvers out that link.
func TestLoadCarriesADeclaredEmptyIPv6TargetList(t *testing.T) {
	t.Parallel()

	body := strings.Replace(
		validDocument,
		`"targets-v6": ["2001:db8:53::1", "2001:db8:53::2"],`,
		`"targets-v6": [],`,
		1,
	)
	if body == validDocument {
		t.Fatal("the document still carries webpass's IPv6 targets")
	}
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a provider declaring no IPv6 targets: %v", err)
	}
	probe, present := loaded.Health["webpass"]
	if !present {
		t.Fatal("webpass carries a health container and must hold a probe entry")
	}
	if probe.TargetsV6 == nil {
		t.Fatal("a declared-empty targets-v6 must load as an empty list, not as nil")
	}
	if len(probe.TargetsV6) != 0 {
		t.Fatalf("targets-v6 = %v, want empty", probe.TargetsV6)
	}
	if !reflect.DeepEqual(probe.TargetsV4, []string{"192.0.2.10", "192.0.2.11"}) {
		t.Fatalf("targets-v4 = %v, want the file's list", probe.TargetsV4)
	}

	// Apply is the hop the daemon takes next, and it is where a copy could drop
	// the distinction before any module config is built.
	var cfg config.Config
	loaded.Apply(&cfg)
	applied := cfg.IfMgr.Modules.Health.WAN["webpass"]
	if applied.TargetsV6 == nil {
		t.Fatal("Apply turned a declared-empty targets-v6 into nil")
	}
	if len(applied.TargetsV6) != 0 {
		t.Fatalf("applied targets-v6 = %v, want empty", applied.TargetsV6)
	}
}

// withWebpassFreeForm gives webpass one free-form .network section carrying
// the given line, keyed at position zero.
func withWebpassFreeForm(key string, value string) string {
	return strings.Replace(validDocument,
		`"goodkind-mwan-steering:wan": {`,
		`"goodkind-mwan-steering:networkd": {"file": [{"kind": "network", "section": [`+
			`{"index": 0, "name": "DHCPv6", "entry": [`+
			`{"index": 0, "key": "`+key+`", "value": "`+value+`"}]}]}]},`+"\n"+
			`      "goodkind-mwan-steering:wan": {`, 1)
}

func TestLoadRejectsAKeySetByBothLayers(t *testing.T) {
	t.Parallel()

	// webpass types its delegation hint, so a free-form line naming the key
	// that leaf renders to would either be read as a list or silently win.
	body := withWebpassFreeForm("PrefixDelegationHint", "::/60")
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's free-form section: %v", err)
	}
	const want = "interface enwebpass0: networkd section DHCPv6 key PrefixDelegationHint is set by the delegation hint leaf; remove one"
	requireOneRejection(t, loaded, "enwebpass0", "webpass", want)
}

func TestLoadCarriesAFreeFormSectionNoTypedLeafNames(t *testing.T) {
	t.Parallel()

	body := withWebpassFreeForm("UseDNS", "no")
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a free-form key no typed leaf names: %v", err)
	}
	want := []interfaceintent.UnitFile{{
		Kind: "network",
		Sections: []interfaceintent.UnitSection{{
			Index:   0,
			Name:    "DHCPv6",
			Entries: []interfaceintent.UnitEntry{{Index: 0, Key: "UseDNS", Value: "no"}},
		}},
	}}
	if got := requireConnection(t, loaded.Connections, "enwebpass0").Networkd; !reflect.DeepEqual(got, want) {
		t.Fatalf("free-form files = %+v, want %+v", got, want)
	}
}

// TestLoadCarriesTheLinkIdentity checks the loaded provider connection,
// the hand-authored connection, the internal connection, and Apply output.
func TestLoadCarriesTheLinkIdentity(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(loaded.Connections) != 3 {
		t.Fatalf("connection count = %d, want 3", len(loaded.Connections))
	}
	webpass := requireConnection(t, loaded.Connections, "enwebpass0")
	if webpass.ID != "webpass" || webpass.Type != "iana-if-type:other" ||
		webpass.Owner != interfaceintent.OwnerNetworkd || webpass.Roles&interfaceintent.RoleProvider == 0 {
		t.Fatalf("webpass identity, type, owner, or role = %+v", webpass)
	}
	if webpass.Link == nil || webpass.Link.Match.Driver != "igc" ||
		webpass.Link.HardwareAddress != "02:00:5e:00:53:01" {
		t.Fatalf("webpass link = %+v", webpass.Link)
	}
	if webpass.IPv4 == nil || webpass.IPv4.Forwarding == nil || !*webpass.IPv4.Forwarding ||
		webpass.IPv4.DHCP == nil || *webpass.IPv4.DHCP ||
		webpass.IPv4.Gateway != netip.MustParseAddr("203.0.113.1") ||
		webpass.IPv4.RouteMetric == nil || *webpass.IPv4.RouteMetric != 10 ||
		!reflect.DeepEqual(webpass.IPv4.Addresses, []interfaceintent.Address{{
			Prefix: netip.MustParsePrefix("203.0.113.2/29"), Purpose: interfaceintent.PurposeLocal,
		}}) {
		t.Fatalf("webpass IPv4 = %+v", webpass.IPv4)
	}
	wantDelegation := &interfaceintent.Delegation{
		Hint: netip.MustParsePrefix("::/56"), DUIDType: "link-layer-time",
		DUID: "00:01:2a:5b:3c:4d:02:00:5e:00:53:01",
	}
	if webpass.IPv6 == nil || webpass.IPv6.Forwarding == nil || !*webpass.IPv6.Forwarding ||
		webpass.IPv6.DHCP == nil || !*webpass.IPv6.DHCP ||
		webpass.IPv6.AcceptRA == nil || !*webpass.IPv6.AcceptRA ||
		!reflect.DeepEqual(webpass.IPv6.Delegation, wantDelegation) {
		t.Fatalf("webpass IPv6 = %+v, want delegation %+v", webpass.IPv6, wantDelegation)
	}
	att := requireConnection(t, loaded.Connections, "enatt0")
	if att.Owner != interfaceintent.OwnerNetworkd || att.Link != nil ||
		att.Roles&interfaceintent.RoleProvider == 0 {
		t.Fatalf("hand-authored provider = %+v", att)
	}
	internal := requireConnection(t, loaded.Connections, "enmwanbr0")
	if internal.Owner != interfaceintent.OwnerExternal || internal.Roles&interfaceintent.RoleInternal == 0 ||
		internal.Roles&interfaceintent.RoleProvider != 0 {
		t.Fatalf("internal connection = %+v", internal)
	}

	var cfg config.Config
	loaded.Apply(&cfg)
	if !reflect.DeepEqual(cfg.IfMgr.Connections, loaded.Connections) {
		t.Fatalf("applied connections = %+v, want %+v", cfg.IfMgr.Connections, loaded.Connections)
	}
}

// TestLoadDerivesTheIPv4SourcePinFromTheStaticAddress pins that the routing
// module's source pin is the link's own static address rather than a value
// inventory types, and that a link which leases its address gets none.
func TestLoadDerivesTheIPv4SourcePinFromTheStaticAddress(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := loaded.WAN["webpass"].V4Source; got != "203.0.113.2" {
		t.Fatalf("static-link v4 source = %q, want the address leaf 203.0.113.2", got)
	}

	leased := strings.Replace(
		validDocument,
		`"address": [{ "ip": "203.0.113.2", "prefix-length": 29 }],
          "goodkind-mwan-steering:dhcp": false,
          "goodkind-mwan-steering:gateway": "203.0.113.1",`,
		`"goodkind-mwan-steering:dhcp": true,`,
		1,
	)
	if leased == validDocument {
		t.Fatal("the document still carries webpass's static address")
	}
	loaded, err = networkjson.Load(writeDocument(t, leased), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a leased link: %v", err)
	}
	if got := loaded.WAN["webpass"].V4Source; got != "" {
		t.Fatalf("leased-link v4 source = %q, want empty", got)
	}
}

func TestLoadRejectsATypedV4Source(t *testing.T) {
	t.Parallel()

	// The daemon derives the pin from the address leaf, so a document still
	// typing it could disagree with the address the link holds.
	body := strings.Replace(
		validDocument,
		`"from-prio": 56,`,
		`"from-prio": 56, "v4-source": "203.0.113.2",`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's v4-source: %v", err)
	}
	requireOneRejection(t, loaded, "enwebpass0", "webpass", "v4-source")
}

func TestLoadInfersExternalOwnerWithoutLinkFiles(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `"goodkind-mwan-steering:link-files": "hand-authored",`, ``, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Rejected) != 0 || loaded.WAN["att"].Iface != "enatt0" {
		t.Fatalf("external provider was rejected: %+v", loaded.Rejected)
	}
	att := requireConnection(t, loaded.Connections, "enatt0")
	if att.Owner != interfaceintent.OwnerExternal || att.Link != nil {
		t.Fatalf("inferred external provider = %+v", att)
	}
}

func TestLoadRejectsNetworkdOwnerWithoutLinkFiles(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument, `"goodkind-mwan-steering:link-files": "hand-authored",`,
		`"goodkind-mwan-steering:owner": "networkd",`, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected the whole document: %v", err)
	}
	requireOneRejection(t, loaded, "enatt0", "att", "networkd owner requires link-files")
}

func TestLoadMWANOwnedLink(t *testing.T) {
	t.Parallel()
	owned := `{ "name": "enowned0", "type": "iana-if-type:ethernetCsmacd",
        "goodkind-mwan-steering:connection-id": "owned-link",
        "goodkind-mwan-steering:owner": "mwan",
        "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:77" } } },`
	body := strings.Replace(validDocument, `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		owned+` { "name": "enmwanbr0", "type": "iana-if-type:other" }`, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	connection := requireConnection(t, loaded.Connections, "enowned0")
	if connection.ID != "owned-link" || connection.Owner != interfaceintent.OwnerMWAN ||
		connection.Link == nil || connection.Link.Kind != interfaceintent.KindPhysical {
		t.Fatalf("MWAN connection = %+v", connection)
	}
	var linkClaims int
	for _, claim := range loaded.Claims {
		if claim.ConnectionID != connection.ID {
			continue
		}
		linkClaims++
		if claim.Kind != interfaceintent.ResourceLink || claim.Key != "enowned0" ||
			claim.Writer != interfaceintent.WriterMWANLink {
			t.Fatalf("MWAN claim = %+v", claim)
		}
	}
	if linkClaims != 1 {
		t.Fatalf("MWAN claims = %d, want one link claim", linkClaims)
	}

	for name, testCase := range map[string]struct {
		field string
		want  string
	}{
		"missing connection ID": {`"goodkind-mwan-steering:connection-id": "owned-link",`, "requires connection-id"},
		"missing link": {`,
        "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:77" } }`, "requires a link"},
		"link files":  {`"goodkind-mwan-steering:link-files": "rendered",`, "cannot use networkd files"},
		"networkd":    {`"goodkind-mwan-steering:networkd": {},`, "cannot use networkd files"},
		"IPv4":        {`"ietf-ip:ipv4": { "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:resolver": {} },`, "supports local addresses, DHCPv4"},
		"IPv6":        {`"ietf-ip:ipv6": { "goodkind-mwan-steering:dhcp": true },`, "supports static local addresses and an optional gateway with route metric only"},
		"lease store": {`"goodkind-mwan-steering:lease-store": "/tmp/leases",`, "cannot use networkd files"},
		"steering":    {`"goodkind-mwan-steering:steering": { "tier": 0 },`, "steering without a provider"},
		"WAN":         {`"goodkind-mwan-steering:wan": { "name": "owned" },`, "requires an address family"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invalid := body
			if name == "missing connection ID" || name == "missing link" {
				invalid = strings.Replace(invalid, testCase.field, "", 1)
			} else {
				invalid = strings.Replace(invalid,
					`"goodkind-mwan-steering:owner": "mwan",`,
					`"goodkind-mwan-steering:owner": "mwan", `+testCase.field, 1)
			}
			if _, err := networkjson.Load(writeDocument(t, invalid), schemaDirForTest(t)); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("rejection error = %v, want %q", err, testCase.want)
			}
		})
	}
	t.Run("internal interface", func(t *testing.T) {
		t.Parallel()
		invalid := strings.Replace(body, `"internal-iface": "enmwanbr0"`, `"internal-iface": "enowned0"`, 1)
		if _, err := networkjson.Load(writeDocument(t, invalid), schemaDirForTest(t)); err == nil ||
			!strings.Contains(err.Error(), "cannot manage internal") {
			t.Fatalf("internal interface error = %v", err)
		}
	})

	t.Run("duplicate physical match across owners", func(t *testing.T) {
		t.Parallel()
		duplicate := strings.Replace(body, `"match": { "driver": "igc" }`,
			`"match": { "hardware-address": "02:00:5e:00:53:77" }`, 1)
		if _, err := networkjson.Load(writeDocument(t, duplicate), schemaDirForTest(t)); err == nil ||
			!strings.Contains(err.Error(), "match the same device") {
			t.Fatalf("duplicate match error = %v", err)
		}
	})
	t.Run("driver-only physical match", func(t *testing.T) {
		t.Parallel()
		invalid := strings.Replace(body, `"hardware-address": "02:00:5e:00:53:77"`,
			`"driver": "igc"`, 1)
		if _, err := networkjson.Load(writeDocument(t, invalid), schemaDirForTest(t)); err == nil ||
			!strings.Contains(err.Error(), "requires a hardware-address match") {
			t.Fatalf("driver-only match error = %v", err)
		}
	})
	t.Run("invalid Linux link name", func(t *testing.T) {
		t.Parallel()
		invalid := strings.Replace(body, `"name": "enowned0"`, `"name": "enowned0\n[Link]"`, 1)
		if _, err := networkjson.Load(writeDocument(t, invalid), schemaDirForTest(t)); err == nil ||
			!strings.Contains(err.Error(), "invalid character") {
			t.Fatalf("unsafe link name error = %v", err)
		}
	})
	for name, testCase := range map[string]struct {
		link string
		kind interfaceintent.Kind
	}{
		"VLAN":   {`"vlan": { "parent": "enwebpass0", "id": 101 }`, interfaceintent.KindVLAN},
		"bridge": {``, interfaceintent.KindBridge},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			variant := strings.Replace(body,
				`"match": { "hardware-address": "02:00:5e:00:53:77" }`, testCase.link, 1)
			if testCase.kind == interfaceintent.KindBridge {
				variant = strings.Replace(variant, `"name": "enowned0", "type": "iana-if-type:ethernetCsmacd"`,
					`"name": "enowned0", "type": "iana-if-type:bridge"`, 1)
			}
			loaded, err := networkjson.Load(writeDocument(t, variant), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			connection := requireConnection(t, loaded.Connections, "enowned0")
			if connection.Link == nil || connection.Link.Kind != testCase.kind {
				t.Fatalf("MWAN link = %+v, want kind %s", connection.Link, testCase.kind)
			}
		})
	}
	t.Run("physical link without match", func(t *testing.T) {
		t.Parallel()
		invalid := strings.Replace(body, `"match": { "hardware-address": "02:00:5e:00:53:77" }`, "", 1)
		if _, err := networkjson.Load(writeDocument(t, invalid), schemaDirForTest(t)); err == nil ||
			!strings.Contains(err.Error(), "requires exactly one identity") {
			t.Fatalf("physical identity error = %v", err)
		}
	})
}

func TestLoadRejectsARenderedLinkWithNoIdentity(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		link string
		want string
	}{
		"no link container": {
			link: ``,
			want: "rendered link requires a link container",
		},
		"neither match nor vlan": {
			link: `"goodkind-mwan-steering:link": { "hardware-address": "02:00:5e:00:53:01" },`,
			want: "link requires exactly one identity (driver, hardware-address, or vlan), got 0",
		},
		"both match leaves": {
			link: `"goodkind-mwan-steering:link": { "match": { "driver": "igc", "hardware-address": "02:00:5e:00:53:01" } },`,
			want: "link requires exactly one identity (driver, hardware-address, or vlan), got 2",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := strings.Replace(
				validDocument,
				`"goodkind-mwan-steering:link": {
          "match": { "driver": "igc" },
          "hardware-address": "02:00:5e:00:53:01"
        },`,
				tc.link,
				1,
			)
			if body == validDocument {
				t.Fatal("the document still carries webpass's link container")
			}
			loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("Load failed the whole document over one provider's link identity: %v", err)
			}
			requireOneRejection(t, loaded, "enwebpass0", "webpass", tc.want)
		})
	}
}

// withWebpassVLAN replaces webpass's device match with a VLAN on parent.
func withWebpassVLAN(parent string) string {
	return strings.Replace(
		validDocument,
		`"match": { "driver": "igc" },`,
		`"vlan": { "parent": "`+parent+`", "id": 101 },`,
		1,
	)
}

// TestLoadCarriesAVLANOnAnInterfaceTheDocumentDescribes checks that the loader
// returns a VLAN and its non-provider networkd parent.
func TestLoadCarriesAVLANOnAnInterfaceTheDocumentDescribes(t *testing.T) {
	t.Parallel()

	// The parent carries no provider of its own, which is the shape a tagged
	// hand-off takes: the physical link is an entry, the VLAN is the member.
	body := strings.Replace(
		withWebpassVLAN("ensonic0"),
		`{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		`{ "name": "ensonic0", "type": "iana-if-type:ethernetCsmacd",
        "goodkind-mwan-steering:owner": "networkd",
        "goodkind-mwan-steering:link-files": "rendered",
        "goodkind-mwan-steering:link": { "match": { "driver": "ixgbe" } } },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a VLAN whose parent the document describes: %v", err)
	}
	webpass := requireConnection(t, loaded.Connections, "enwebpass0")
	want := &interfaceintent.VLAN{Parent: "ensonic0", ID: 101}
	if webpass.Link == nil {
		t.Fatal("VLAN provider has no link")
	}
	if got := webpass.Link.VLAN; !reflect.DeepEqual(got, want) {
		t.Fatalf("vlan = %+v, want %+v", got, want)
	}
	parent := requireConnection(t, loaded.Connections, "ensonic0")
	if parent.Roles&interfaceintent.RoleParent == 0 || parent.Roles&interfaceintent.RoleProvider != 0 ||
		parent.Owner != interfaceintent.OwnerNetworkd || parent.Link == nil {
		t.Fatalf("non-provider parent = %+v", parent)
	}
}

func TestLoadRejectsAVLANParentWithoutARenderedLink(t *testing.T) {
	t.Parallel()
	body := strings.Replace(
		withWebpassVLAN("ensonic0"),
		`{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		`{ "name": "ensonic0", "type": "iana-if-type:other" },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		1,
	)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil || !strings.Contains(err.Error(), "vlan parent ensonic0 needs a rendered networkd link") {
		t.Fatalf("invalid shared parent error = %v", err)
	}
}

func TestLoadRejectsAVLANParentTheDocumentDoesNotDescribe(t *testing.T) {
	t.Parallel()

	// A VLAN exists only because its parent's file names it, so a parent no
	// entry describes is created by nothing and the provider would be silently
	// absent with no error from any layer. The parent leaf is an interface
	// reference, so the schema validation Load runs first is what refuses it.
	body := withWebpassVLAN("ensonic0")
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a VLAN whose parent no interface in the document describes")
	}
	if !strings.Contains(err.Error(), "ensonic0") {
		t.Fatalf("error does not name the parent: %v", err)
	}
}

func TestLoadRejectsOnlyTheEntryWithAFamilyThatOmitsDHCP(t *testing.T) {
	t.Parallel()

	// A family container that does not say whether a client runs cannot be
	// rendered either way, and the schema cannot require the leaf because an
	// interface with no provider may leave it out. The 2026-09-20 testbed
	// outage was an ipv6 container with accept-ra and no dhcp: the loader
	// refused the whole file and the daemon exited, which removed steering from
	// three correct providers. Each family is checked, and only the one entry
	// is rejected.
	cases := map[string]struct {
		leaf string
		want string
	}{
		"ipv4": {leaf: `"goodkind-mwan-steering:dhcp": false,`, want: "interface enwebpass0: ipv4/dhcp is required"},
		"ipv6": {leaf: `"goodkind-mwan-steering:dhcp": true,`, want: "interface enwebpass0: ipv6/dhcp is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := strings.Replace(validDocument, tc.leaf, ``, 1)
			if body == validDocument {
				t.Fatalf("webpass's %s dhcp leaf is still in the document", name)
			}
			loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("Load failed the whole document over one provider's %s container: %v", name, err)
			}
			requireOneRejection(t, loaded, "enwebpass0", "webpass", tc.want)
		})
	}
}

func TestLoadRejectsAHandAuthoredEntryThatDescribesItsLink(t *testing.T) {
	t.Parallel()

	// hand-authored means the model says nothing about how the link comes up,
	// so a link container beside it would be a description the daemon
	// silently ignored.
	body := strings.Replace(
		validDocument,
		`"goodkind-mwan-steering:link-files": "hand-authored",`,
		`"goodkind-mwan-steering:link-files": "hand-authored",
        "goodkind-mwan-steering:link": { "match": { "driver": "i40e" } },`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's contradiction: %v", err)
	}
	requireOneRejection(t, loaded, "enatt0", "att", "hand-authored files cannot include a renderable link")
}

func TestLoadRejectsInvalidTranslation(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		old         string
		replacement string
		family      string
		reason      string
	}{
		"IPv6 mode on IPv4": {
			old: `"mode": "ietf-nat:napt44"`, replacement: `"mode": "ietf-nat:nptv6"`,
			family: "ipv4", reason: "translation mode",
		},
		"native mode with mappings": {
			old: `"mode": "ietf-nat:napt44"`, replacement: `"mode": "native"`,
			family: "ipv4", reason: "static mappings require NAPT44",
		},
		"missing internal prefix": {
			old: `"internal-prefix": "2001:db8:b01::/60",`, replacement: "",
			family: "ipv6", reason: "internal-prefix requires an IPv6 prefix",
		},
		"unsupported prefix length": {
			old: `"internal-prefix": "2001:db8:b01::/60"`, replacement: `"internal-prefix": "2001:db8:b01::/65"`,
			family: "ipv6", reason: "must be /64 or shorter",
		},
		"configured source without prefix": {
			old: `"external-source": "delegated"`, replacement: `"external-source": "configured"`,
			family: "ipv6", reason: "external-prefix requires an IPv6 prefix",
		},
		"missing source": {
			old: `"external-source": "delegated",`, replacement: "",
			family: "ipv6", reason: "external-source must be configured or delegated",
		},
		"unusable outgoing interface": {
			old: `"name": "enwebpass0"`, replacement: `"name": "invalid/interface"`,
			family: "ipv4", reason: "requires a usable outgoing interface",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := strings.Replace(validDocument, testCase.old, testCase.replacement, 1)
			if body == validDocument {
				t.Fatal("document mutation did not match")
			}
			loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("provider-local error rejected document: %v", err)
			}
			iface := "enwebpass0"
			if name == "unusable outgoing interface" {
				iface = "invalid/interface"
			}
			requireOneRejection(t, loaded, iface, "webpass", "wan webpass "+testCase.family+": ")
			if !strings.Contains(loaded.Rejected[0].Err.Error(), testCase.reason) {
				t.Fatalf("rejection = %v, want %s", loaded.Rejected[0].Err, testCase.reason)
			}
		})
	}
}

func TestLoadAcceptsHandAuthoredDelegationMetadata(t *testing.T) {
	t.Parallel()
	body := strings.Replace(validDocument,
		`"goodkind-mwan-steering:translation": { "mode": "native" }`,
		`"goodkind-mwan-steering:dhcp": true,
          "goodkind-mwan-steering:delegation": { "hint": "::/60" },
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:nptv6",
            "nptv6": {
              "internal-prefix": "2001:db8:b01::/60",
              "external-source": "delegated",
              "expected-prefix": "2001:db8:beef:100::/60"
            }
          }`, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Rejected) != 0 {
		t.Fatalf("rejected: %+v", loaded.Rejected)
	}
	policy := loaded.WAN["att"].TranslationV6
	if policy == nil || policy.NPT == nil || policy.NPT.ExternalSource != config.PrefixDelegated {
		t.Fatalf("hand-authored delegation policy = %+v", policy)
	}
	if att := requireConnection(t, loaded.Connections, "enatt0"); att.Link != nil {
		t.Fatalf("hand-authored connection has rendered link: %+v", att)
	}
}

func TestLoadBaselineKeepsProtectiveInputsIndependentOfProviderValidation(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../yang/instances/network-min.json")
	if err != nil {
		t.Fatalf("read network instance: %v", err)
	}
	body := strings.Replace(string(raw), `"table-id": 200`, `"table-id": "invalid"`, 1)
	body = strings.Replace(body, `"pinned-source-port": 51820`, `"pinned-source-port": "invalid"`, 1)
	if body == string(raw) {
		t.Fatal("network instance mutations did not match")
	}
	baseline, err := networkjson.LoadBaseline(writeDocument(t, body))
	if err != nil {
		t.Fatalf("LoadBaseline rejected unrelated fields: %v", err)
	}
	if baseline == nil || baseline.ManagementInterface != "enmgmt0" || baseline.InternalInterface != "enmwanbr0" {
		t.Fatalf("baseline = %+v", baseline)
	}
	if len(baseline.ManagementServices) != 2 || len(baseline.ProviderInterfaces) != 3 {
		t.Fatalf("baseline services or providers = %+v", baseline)
	}

	invalidManagement := strings.Replace(body, `"port": 22`, `"port": 0`, 1)
	if _, err := networkjson.LoadBaseline(writeDocument(t, invalidManagement)); err == nil {
		t.Fatal("LoadBaseline accepted an invalid management port")
	}
}

func mwanDHCPv4Document(family string) string {
	owned := `{ "name": "enowned0", "type": "iana-if-type:ethernetCsmacd",
        "goodkind-mwan-steering:connection-id": "owned-link",
        "goodkind-mwan-steering:owner": "mwan",
        "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:77" } },
        "ietf-ip:ipv4": ` + family + ` },`
	return strings.Replace(validDocument, `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		owned+` { "name": "enmwanbr0", "type": "iana-if-type:other" }`, 1)
}

func TestLoadMWANOwnedDHCPv4(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		clientID     string
		routeSetting string
		gateway      string
		wantID       []byte
	}{
		{name: "hardware identity and default routes"},
		{name: "configured identity and routes", clientID: `"client-id": "hex:01aabb",`, routeSetting: `"use-routes": true,`, wantID: []byte{0x01, 0xaa, 0xbb}},
		{name: "routes disabled with static gateway", routeSetting: `"use-routes": false,`, gateway: `"goodkind-mwan-steering:gateway": "192.0.2.1",`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			family := `{ "goodkind-mwan-steering:dhcp": true,
                "goodkind-mwan-steering:route-metric": 17,
                ` + testCase.gateway + `
                "goodkind-mwan-steering:dhcpv4": { ` + testCase.clientID + testCase.routeSetting + `
                  "use-dns": false } }`
			loaded, err := networkjson.Load(writeDocument(t, mwanDHCPv4Document(family)), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			connection := requireConnection(t, loaded.Connections, "enowned0")
			if connection.IPv4 == nil || connection.IPv4.DHCP == nil || !*connection.IPv4.DHCP ||
				connection.IPv4.DHCPv4 == nil || connection.IPv4.RouteMetric == nil || *connection.IPv4.RouteMetric != 17 {
				t.Fatalf("DHCPv4 intent = %+v", connection.IPv4)
			}
			decoded, err := networkjson.DecodeDHCPv4ClientID(connection.IPv4.DHCPv4.ClientID)
			if err != nil || !reflect.DeepEqual(decoded, testCase.wantID) {
				t.Fatalf("decoded client ID = %x, error = %v; want %x", decoded, err, testCase.wantID)
			}
			for _, expected := range []struct {
				kind   interfaceintent.ResourceKind
				writer interfaceintent.Writer
			}{
				{interfaceintent.ResourceDHCPv4, interfaceintent.WriterMWANProtocol},
				{interfaceintent.ResourceAcquiredAddress, interfaceintent.WriterMWANAddress},
			} {
				found := false
				for _, claim := range loaded.Claims {
					if claim.ConnectionID == connection.ID && claim.Kind == expected.kind && claim.Writer == expected.writer {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing %s claim by %s: %+v", expected.kind, expected.writer, loaded.Claims)
				}
			}
			foundRoute := false
			expectedRoute := "main/default/17"
			for _, claim := range loaded.Claims {
				if claim.ConnectionID == connection.ID && claim.Kind == interfaceintent.ResourceMainRoute &&
					claim.Key == expectedRoute &&
					claim.Writer == interfaceintent.WriterMWANRoute {
					foundRoute = true
				}
			}
			if !foundRoute {
				t.Fatalf("missing MWAN default route claim: %+v", loaded.Claims)
			}
		})
	}
}

func TestLoadMWANDHCPv4WithoutRoutesNeedsNoMetric(t *testing.T) {
	t.Parallel()
	family := `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "use-routes": false } }`
	if _, err := networkjson.Load(writeDocument(t, mwanDHCPv4Document(family)), schemaDirForTest(t)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsDuplicateMWANDHCPv4DefaultMetric(t *testing.T) {
	t.Parallel()
	family := `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:route-metric": 17 }`
	document := mwanDHCPv4Document(family)
	second := `{ "name": "enowned1", "type": "iana-if-type:ethernetCsmacd",
        "goodkind-mwan-steering:connection-id": "owned-link-1",
        "goodkind-mwan-steering:owner": "mwan",
        "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:78" } },
        "ietf-ip:ipv4": ` + family + ` },`
	document = strings.Replace(document, `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		second+` { "name": "enmwanbr0", "type": "iana-if-type:other" }`, 1)
	_, err := networkjson.Load(writeDocument(t, document), schemaDirForTest(t))
	if err == nil || !strings.Contains(err.Error(), "resource main-route/ipv4/main/default/17 has writers") {
		t.Fatalf("Load error = %v, want duplicate default route metric", err)
	}
}

func TestLoadRejectsUnsupportedMWANDHCPv4Options(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		family string
		want   string
	}{
		{"client ID encoding", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "client-id": "ethernet:aa" } }`, "client-id"},
		{"client ID one byte", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "client-id": "hex:01" } }`, "client-id"},
		{"client ID over 255 bytes", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "client-id": "hex:` + strings.Repeat("01", 256) + `" } }`, "client-id"},
		{"client ID invalid hex", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "client-id": "hex:01xz" } }`, "client-id"},
		{"DNS", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "use-dns": true } }`, "use-dns requires resolver ownership"},
		{"missing route metric", `{ "goodkind-mwan-steering:dhcp": true }`, "DHCP routes require route-metric"},
		{"explicit routes without route metric", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv4": { "use-routes": true } }`, "DHCP routes require route-metric"},
		{"static gateway with default DHCP routes", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:gateway": "192.0.2.1" }`, "cannot combine a static gateway with DHCP routes"},
		{"static gateway with requested DHCP routes", `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:gateway": "192.0.2.1", "goodkind-mwan-steering:dhcpv4": { "use-routes": true } }`, "cannot combine a static gateway with DHCP routes"},
		{"client without DHCP", `{ "goodkind-mwan-steering:dhcp": false, "goodkind-mwan-steering:dhcpv4": {} }`, "requires ipv4/dhcp true"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := networkjson.Load(writeDocument(t, mwanDHCPv4Document(testCase.family)), schemaDirForTest(t))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Load error = %v, want %q", err, testCase.want)
			}
		})
	}
}
