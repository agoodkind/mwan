package networkjson_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkd"
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
          "forwarding": true,
          "address": [{ "ip": "203.0.113.2", "prefix-length": 29 }],
          "goodkind-mwan-steering:dhcp": false,
          "goodkind-mwan-steering:gateway": "203.0.113.1",
          "goodkind-mwan-steering:route-metric": 10
        },
        "ietf-ip:ipv6": {
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
          "npt-prefix": "2001:db8:beef:200::/60",
          "static-mapping": [
            { "external": "203.0.113.2", "internal": "192.0.2.2" },
            { "external": "203.0.113.3", "internal": "192.0.2.3" }
          ],
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
        "goodkind-mwan-steering:steering": { "tier": 1, "weight": 1 },
        "goodkind-mwan-steering:wan": {
          "name": "att",
          "table-id": 100,
          "fw-mark": 1,
          "fw-mark-prio": 100,
          "from-prio": 55,
          "npt-prefix": "2001:db8:beef:100::/60"
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
	for _, link := range loaded.Links {
		if link.Name == iface {
			t.Fatalf("rejected interface %s still has a link specification", iface)
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

func TestLoadAcceptsAProviderThatDelegatesNoPrefix(t *testing.T) {
	t.Parallel()

	// A provider on an IPv4-only link delegates nothing, so it carries no
	// npt-prefix. The model leaves the leaf optional and the routing module
	// installs no IPv6 source rule without it, so the loader must carry the
	// provider rather than refuse the whole file.
	body := strings.Replace(
		validDocument,
		`,
          "npt-prefix": "2001:db8:beef:100::/60"`,
		``,
		1,
	)
	if body == validDocument {
		t.Fatal("the document still carries att's npt prefix")
	}
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a provider that delegates no prefix: %v", err)
	}
	if got := len(loaded.WAN); got != 2 {
		t.Fatalf("provider count = %d, want 2", got)
	}
	att := loaded.WAN["att"]
	if att.NptPrefix != "" {
		t.Fatalf("att npt prefix = %q, want empty", att.NptPrefix)
	}
	if att.Iface != "enatt0" || att.TableID != 100 || att.Tier != 1 {
		t.Fatalf("att entry = %+v, want the rest of its configuration unchanged", att)
	}
	if got := loaded.WAN["webpass"].NptPrefix; got != "2001:db8:beef:200::/60" {
		t.Fatalf("webpass npt prefix = %q, want 2001:db8:beef:200::/60", got)
	}
}

func TestLoadAcceptsADisabledProbeWithNoSettings(t *testing.T) {
	t.Parallel()

	// A disabled probe is the second way a provider goes unprobed, and the
	// daemon reads none of its settings, so a container carrying only the flag
	// must load rather than stop the daemon over counts nobody consumes.
	body := strings.Replace(
		validDocument,
		`"npt-prefix": "2001:db8:beef:100::/60"`,
		`"npt-prefix": "2001:db8:beef:100::/60", "health": {"enabled": false}`,
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
		`"npt-prefix": "2001:db8:beef:100::/60"`,
		`"npt-prefix": "2001:db8:beef:100::/60", "health": {
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
	if got := loaded.WAN["webpass"].StaticMappings; !reflect.DeepEqual(got, want) {
		t.Fatalf("webpass static mappings = %v, want %v", got, want)
	}
	if got := loaded.WAN["att"].StaticMappings; len(got) != 0 {
		t.Fatalf("att static mappings = %v, want none", got)
	}
}

func TestLoadRejectsAnExternalAddressMappedByTwoProviders(t *testing.T) {
	t.Parallel()

	// The schema keys the list inside one provider and cannot see another
	// provider's list. Two providers translating one address would each own it
	// wherever it is on-link, so which link carries it would be undefined.
	body := strings.Replace(
		validDocument,
		`"npt-prefix": "2001:db8:beef:100::/60"`,
		`"npt-prefix": "2001:db8:beef:100::/60",
          "static-mapping": [{ "external": "203.0.113.3", "internal": "192.0.2.4" }]`,
		1,
	)
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
		"from priority": {from: `"from-prio": 55,`, to: `"from-prio": 56,`, leaf: "from-prio"},
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
		`"npt-prefix": "2001:db8:beef:100::/60"`,
		`"npt-prefix": "2001:db8:beef:100::/60", "forced-dscp": 8`,
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
		`"npt-prefix": "2001:db8:beef:100::/60"`,
		`"npt-prefix": "2001:db8:beef:100::/60", "forced-dscp": 0`,
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
		`"npt-prefix": "2001:db8:beef:100::/60"`,
		`"npt-prefix": "2001:db8:beef:100::/60", "forced-dscp": 8`,
		1,
	)
	body = strings.Replace(
		body,
		`"npt-prefix": "2001:db8:beef:200::/60",`,
		`"npt-prefix": "2001:db8:beef:200::/60", "forced-dscp": 8,`,
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
	if len(loaded.Links) != 1 {
		t.Fatalf("link count = %d, want 1 for the one rendered provider", len(loaded.Links))
	}
	want := []networkd.File{{
		Kind: networkd.FileNetwork,
		Sections: []networkd.Section{{
			Index:   0,
			Name:    "DHCPv6",
			Entries: []networkd.Entry{{Index: 0, Key: "UseDNS", Value: "no"}},
		}},
	}}
	if got := loaded.Links[0].Files; !reflect.DeepEqual(got, want) {
		t.Fatalf("free-form files = %+v, want %+v", got, want)
	}
}

// TestLoadCarriesTheLinkIdentity pins the specification the renderer reads:
// every typed leaf the document carries arrives on the rendered provider's
// entry in Config.Links, the hand-authored provider contributes none, and
// Apply carries the list onto the daemon's configuration.
func TestLoadCarriesTheLinkIdentity(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := []networkd.Spec{{
		Name:            "enwebpass0",
		TableID:         200,
		Match:           networkd.Match{Driver: "igc"},
		HardwareAddress: "02:00:5e:00:53:01",
		IPv4: &networkd.FamilyV4{
			Family: networkd.Family{
				Forwarding:  new(true),
				Addresses:   []networkd.Address{{IP: netip.MustParseAddr("203.0.113.2"), PrefixLength: 29}},
				DHCP:        new(false),
				Gateway:     netip.MustParseAddr("203.0.113.1"),
				RouteMetric: new(uint32(10)),
			},
		},
		IPv6: &networkd.FamilyV6{
			Family:   networkd.Family{Forwarding: new(true), DHCP: new(true)},
			AcceptRA: new(true),
			Delegation: &networkd.Delegation{
				Hint:     netip.MustParsePrefix("::/56"),
				DUIDType: "link-layer-time",
				DUID:     "00:01:2a:5b:3c:4d:02:00:5e:00:53:01",
			},
		},
	}}
	if !reflect.DeepEqual(loaded.Links, want) {
		t.Fatalf("links = %+v, want %+v", loaded.Links, want)
	}
	if got := loaded.WAN["webpass"].LinkFiles; got != "rendered" {
		t.Fatalf("webpass link-files = %q, want rendered", got)
	}
	if got := loaded.WAN["att"].LinkFiles; got != "hand-authored" {
		t.Fatalf("att link-files = %q, want hand-authored", got)
	}

	var cfg config.Config
	loaded.Apply(&cfg)
	if !reflect.DeepEqual(cfg.IfMgr.Links, want) {
		t.Fatalf("applied links = %+v, want the loaded list", cfg.IfMgr.Links)
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
		`"npt-prefix": "2001:db8:beef:200::/60",`,
		`"npt-prefix": "2001:db8:beef:200::/60", "v4-source": "203.0.113.2",`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's v4-source: %v", err)
	}
	requireOneRejection(t, loaded, "enwebpass0", "webpass", "v4-source")
}

func TestLoadRejectsAProviderThatStatesNoLinkFiles(t *testing.T) {
	t.Parallel()

	// An entry with no link block looks the same whether its files are
	// deliberately hand-authored or its link block was never written, so the
	// exemption must be stated rather than inferred from the absence.
	body := strings.Replace(validDocument, `"goodkind-mwan-steering:link-files": "hand-authored",`, ``, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load failed the whole document over one provider's link-files leaf: %v", err)
	}
	requireOneRejection(t, loaded, "enatt0", "att", "interface enatt0: link-files is required")
}

func TestLoadRejectsARenderedLinkWithNoIdentity(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		link string
		want string
	}{
		"no link container": {
			link: ``,
			want: "link container is required",
		},
		"neither match nor vlan": {
			link: `"goodkind-mwan-steering:link": { "hardware-address": "02:00:5e:00:53:01" },`,
			want: "exactly one of match/driver, match/hardware-address, or vlan, got 0",
		},
		"both match leaves": {
			link: `"goodkind-mwan-steering:link": { "match": { "driver": "igc", "hardware-address": "02:00:5e:00:53:01" } },`,
			want: "exactly one of match/driver, match/hardware-address, or vlan, got 2",
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

func TestLoadCarriesAVLANOnAnInterfaceTheDocumentDescribes(t *testing.T) {
	t.Parallel()

	// The parent carries no provider of its own, which is the shape a tagged
	// hand-off takes: the physical link is an entry, the VLAN is the member.
	body := strings.Replace(
		withWebpassVLAN("ensonic0"),
		`{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		`{ "name": "ensonic0", "type": "iana-if-type:other" },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		1,
	)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a VLAN whose parent the document describes: %v", err)
	}
	if len(loaded.Links) != 1 {
		t.Fatalf("link count = %d, want 1", len(loaded.Links))
	}
	want := &networkd.VLAN{Parent: "ensonic0", ID: 101}
	if got := loaded.Links[0].VLAN; !reflect.DeepEqual(got, want) {
		t.Fatalf("vlan = %+v, want %+v", got, want)
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
	requireOneRejection(t, loaded, "enatt0", "att", "interface enatt0: link-files is hand-authored")
}
