package networkjson_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
)

// schemaDirForTest assembles the model set the gateway installs into a
// temporary directory: the vendored IETF modules at the revisions the deploy
// copies, plus the repository's steering module at whatever revision it
// currently carries, so a revision bump does not touch this test.
func schemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sources := []string{
		"../../third_party/yang/standard/ietf/RFC/ietf-yang-types@2025-12-22.yang",
		"../../third_party/yang/standard/ietf/RFC/ietf-inet-types@2025-12-22.yang",
		"../../third_party/yang/standard/ietf/RFC/iana-if-type@2014-05-08.yang",
		"../../third_party/yang/standard/ietf/RFC/ietf-interfaces@2018-02-20.yang",
		"../../third_party/yang/standard/ietf/RFC/ietf-ip@2018-02-22.yang",
		"../../third_party/yang/standard/ietf/RFC/ietf-nat@2019-01-10.yang",
	}
	matches, err := filepath.Glob("../../yang/goodkind-mwan-steering@*.yang")
	if err != nil {
		t.Fatalf("glob steering model: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one steering model, found %d", len(matches))
	}
	for _, source := range append(sources, matches[0]) {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		target := filepath.Join(dir, filepath.Base(source))
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	return dir
}

// validDocument is one gateway's network tree: two providers in different
// tiers, one of them with an IPv4 source pin and one with no probe at all,
// plus the internal link and the group-wide values. Addresses are
// documentation prefixes.
const validDocument = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:steering": { "tier": 0, "weight": 2 },
        "goodkind-mwan-steering:wan": {
          "name": "webpass",
          "table-id": 200,
          "fw-mark": 2,
          "fw-mark-prio": 200,
          "from-prio": 56,
          "npt-prefix": "2001:db8:beef:200::/60",
          "v4-source": "203.0.113.2",
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
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider with no table id")
	}
	if !strings.Contains(err.Error(), "table-id") {
		t.Fatalf("error does not name the missing leaf: %v", err)
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
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider with no steering container")
	}
	if !strings.Contains(err.Error(), "steering is required") {
		t.Fatalf("error does not name the missing container: %v", err)
	}
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
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider with no weight")
	}
	if !strings.Contains(err.Error(), "steering/weight is required") {
		t.Fatalf("error does not name the missing leaf: %v", err)
	}
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
		`"v4-source": "203.0.113.2",`,
		`"v4-source": "203.0.113.2", "forced-dscp": 8,`,
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
