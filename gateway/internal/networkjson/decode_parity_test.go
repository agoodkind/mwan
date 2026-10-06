//go:build cgo

package networkjson_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkjson"
)

const (
	layerSchema = "schema"
	layerDecode = "decode"

	instanceDir = "../../yang/instances"

	// The test edits the unpinned provider because rejecting the pinned provider
	// would fail the firewall pin check first.
	monkeybrainsSteering = `"tier": 2,` + "\n" + `          "weight": 1`
)

func monkeybrainsWeight(value string) documentEdit {
	return documentEdit{old: monkeybrainsSteering, replacement: `"tier": 2,` + "\n" + `          "weight": ` + value}
}

type documentEdit struct {
	old         string
	replacement string
}

type rejectionSummary struct {
	Interface string
	Provider  string
	Err       string
}

func readInstance(t *testing.T, name string) (string, []byte) {
	t.Helper()
	path := filepath.Join(instanceDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return path, data
}

func editDocument(t *testing.T, base string, edit documentEdit) string {
	t.Helper()
	if count := strings.Count(base, edit.old); count != 1 {
		t.Fatalf("edit target %q occurs %d times, want 1", edit.old, count)
	}
	return strings.Replace(base, edit.old, edit.replacement, 1)
}

func summarizeRejections(rejected []networkjson.Rejection) []rejectionSummary {
	summaries := make([]rejectionSummary, 0, len(rejected))
	for _, rejection := range rejected {
		summaries = append(summaries, rejectionSummary{
			Interface: rejection.Interface,
			Provider:  rejection.Provider,
			Err:       rejection.Err.Error(),
		})
	}
	return summaries
}

// Rejected errors require comparison by text because each decode creates
// new error values.
func requireSameConfig(t *testing.T, want *networkjson.Config, got *networkjson.Config) {
	t.Helper()
	wantRejected := summarizeRejections(want.Rejected)
	gotRejected := summarizeRejections(got.Rejected)
	if !reflect.DeepEqual(wantRejected, gotRejected) {
		t.Fatalf("Rejected differs:\nwant %+v\ngot  %+v", wantRejected, gotRejected)
	}
	wantRest := *want
	gotRest := *got
	wantRest.Rejected = nil
	gotRest.Rejected = nil
	if !reflect.DeepEqual(wantRest, gotRest) {
		t.Fatalf("Config differs:\nwant %+v\ngot  %+v", wantRest, gotRest)
	}
}

func rejectionText(loaded *networkjson.Config, err error) string {
	if err != nil {
		return err.Error()
	}
	if len(loaded.Rejected) > 0 {
		return loaded.Rejected[0].Err.Error()
	}
	return ""
}

func TestDecodeMatchesLoadForValidDocuments(t *testing.T) {
	schemaDir := schemaDirForTest(t)
	for _, name := range []string{"network-min.json", "network-freeform.json"} {
		t.Run(name, func(t *testing.T) {
			path, data := readInstance(t, name)
			loaded, err := networkjson.Load(path, schemaDir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			decoded, err := networkjson.Decode(data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			requireSameConfig(t, loaded, decoded)
		})
	}
}

func TestDecodeRejectsOnlySemanticDefects(t *testing.T) {
	schemaDir := schemaDirForTest(t)
	_, data := readInstance(t, "network-min.json")
	base := string(data)
	internalLink := `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`
	managementLink := `{ "name": "enmgmt0", "type": "iana-if-type:other" }`
	internalRoute := func(destination string, gateway string) documentEdit {
		return documentEdit{
			old: internalLink,
			replacement: `{ "name": "enmwanbr0", "type": "iana-if-type:other", "ietf-ip:ipv4": ` +
				`{ "goodkind-mwan-steering:route": [{ "destination": "` + destination +
				`", "gateway": "` + gateway + `" }] } }`,
		}
	}
	cases := []struct {
		name        string
		layer       string
		edit        documentEdit
		decodeError string
	}{
		{
			name:  "unknown member in an interface entry",
			layer: layerSchema,
			edit: documentEdit{
				old:         managementLink,
				replacement: `{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }`,
			},
		},
		{
			name:  "hash-mode outside the enumeration",
			layer: layerSchema,
			edit:  documentEdit{old: `"hash-mode": "source"`, replacement: `"hash-mode": "bogus"`},
		},
		{
			name:  "health ping-count above uint8",
			layer: layerSchema,
			edit: documentEdit{
				old:         `"ping-count": 3,` + "\n" + `            "success-threshold": 2,`,
				replacement: `"ping-count": 256,` + "\n" + `            "success-threshold": 2,`,
			},
		},
		{
			name:        "wan fw-mark below its range",
			layer:       layerDecode,
			edit:        documentEdit{old: `"fw-mark": 3,`, replacement: `"fw-mark": 0,`},
			decodeError: "has a zero or duplicated mark",
		},
		{
			name:  "interface without the mandatory type",
			layer: layerSchema,
			edit:  documentEdit{old: managementLink, replacement: `{ "name": "enmgmt0" }`},
		},
		{
			name:        "wan without the mandatory name",
			layer:       layerDecode,
			edit:        documentEdit{old: `"name": "monkeybrains",`, replacement: ``},
			decodeError: "a provider with no name",
		},
		{
			name:        "route destination with host bits",
			layer:       layerDecode,
			edit:        internalRoute("198.51.100.1/24", "192.0.2.1"),
			decodeError: "must be a canonical same-family network prefix",
		},
		{
			name:        "route gateway unspecified",
			layer:       layerDecode,
			edit:        internalRoute("198.51.100.0/24", "0.0.0.0"),
			decodeError: "has invalid same-family gateway",
		},
		{
			name:        "route gateway multicast",
			layer:       layerDecode,
			edit:        internalRoute("198.51.100.0/24", "224.0.0.1"),
			decodeError: "has invalid same-family gateway",
		},
		{
			name:        "steering weight null",
			layer:       layerDecode,
			edit:        monkeybrainsWeight(`null`),
			decodeError: "steering/weight is required",
		},
		{
			name:  "forced-dscp null",
			layer: layerSchema,
			edit:  documentEdit{old: `"forced-dscp": 8`, replacement: `"forced-dscp": null`},
		},
		{
			name:        "probe-timeout null",
			layer:       layerDecode,
			edit:        documentEdit{old: `"probe-timeout": 2000`, replacement: `"probe-timeout": null`},
			decodeError: "steering-group/health/probe-timeout is required",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := editDocument(t, base, testCase.edit)
			loaded, loadErr := networkjson.Load(writeDocument(t, document), schemaDir)
			if rejectionText(loaded, loadErr) == "" {
				t.Fatalf("Load accepted the %s-layer defect", testCase.layer)
			}
			decoded, decodeErr := networkjson.Decode([]byte(document))
			decodeRejection := rejectionText(decoded, decodeErr)
			if testCase.layer == layerSchema {
				if decodeRejection != "" {
					t.Fatalf("Decode rejected a schema-only defect: %s", decodeRejection)
				}
				return
			}
			if !strings.Contains(decodeRejection, testCase.decodeError) {
				t.Fatalf("Decode rejection = %q, want it to contain %q", decodeRejection, testCase.decodeError)
			}
		})
	}
}

func TestDecodeTreatsNullAsAbsent(t *testing.T) {
	_, data := readInstance(t, "network-min.json")
	base := string(data)
	cases := []struct {
		name   string
		null   documentEdit
		absent documentEdit
	}{
		{
			name:   "steering weight",
			null:   monkeybrainsWeight(`null`),
			absent: documentEdit{old: monkeybrainsSteering, replacement: `"tier": 2`},
		},
		{
			name:   "forced-dscp",
			null:   documentEdit{old: `"forced-dscp": 8`, replacement: `"forced-dscp": null`},
			absent: documentEdit{old: `"from-prio": 55,` + "\n" + `          "forced-dscp": 8`, replacement: `"from-prio": 55`},
		},
		{
			name:   "probe-timeout",
			null:   documentEdit{old: `"probe-timeout": 2000`, replacement: `"probe-timeout": null`},
			absent: documentEdit{old: `{ "probe-timeout": 2000 }`, replacement: `{}`},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			withNull, nullErr := networkjson.Decode([]byte(editDocument(t, base, testCase.null)))
			withoutMember, absentErr := networkjson.Decode([]byte(editDocument(t, base, testCase.absent)))
			if nullErr != nil || absentErr != nil {
				if nullErr == nil || absentErr == nil || nullErr.Error() != absentErr.Error() {
					t.Fatalf("Decode errors differ: null %v, absent %v", nullErr, absentErr)
				}
				return
			}
			requireSameConfig(t, withoutMember, withNull)
		})
	}
}
