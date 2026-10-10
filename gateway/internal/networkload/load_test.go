package networkload_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/yangpub/schema"
	"goodkind.io/mwan/internal/yangschema"
)

const (
	instanceGlob   = "../../yang/instances/*.json"
	networkMinPath = "../../yang/instances/network-min.json"

	managementLink = `{ "name": "enmgmt0", "type": "iana-if-type:other" }`
	unknownMember  = `{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }`
	duplicateName  = `{ "name": "enmgmt0", "name": "enmgmt0", "type": "iana-if-type:other" }`

	uniqueTable           = `"table-id": 300,`
	duplicateTable        = `"table-id": 200,`
	duplicateTableMessage = "table-id 200 is already taken"
)

type rejectionSummary struct {
	Interface string
	Provider  string
	Err       string
}

func writeSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, module := range schema.Modules() {
		content, err := schema.Read(module.File)
		if err != nil {
			t.Fatalf("read the embedded module: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, module.File), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", module.File, err)
		}
	}
	return dir
}

func loadEmbedded(t *testing.T) *yangschema.Schema {
	t.Helper()
	loaded, err := yangschema.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	t.Cleanup(loaded.Close)
	return loaded
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func writeDocument(t *testing.T, document []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func replaceOnce(t *testing.T, base []byte, old string, replacement string) []byte {
	t.Helper()
	if count := strings.Count(string(base), old); count != 1 {
		t.Fatalf("edit target %q occurs %d times, want 1", old, count)
	}
	return []byte(strings.Replace(string(base), old, replacement, 1))
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

func TestValidateAndDecodeMatchesLoadForValidDocuments(t *testing.T) {
	documents, err := filepath.Glob(instanceGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", instanceGlob, err)
	}
	if len(documents) == 0 {
		t.Fatalf("no network document matches %s", instanceGlob)
	}
	schemaDir := writeSchemaDir(t)
	for _, path := range documents {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data := readFile(t, path)
			loaded, err := networkload.Load(path, schemaDir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			decoded, err := networkload.ValidateAndDecode(data, loadEmbedded(t))
			if err != nil {
				t.Fatalf("ValidateAndDecode: %v", err)
			}
			requireSameConfig(t, loaded, decoded)
			canonical, err := networkjson.Canonicalize(data)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			decodedCanonical, err := networkload.ValidateAndDecode(canonical, loadEmbedded(t))
			if err != nil {
				t.Fatalf("ValidateAndDecode canonical: %v", err)
			}
			requireSameConfig(t, loaded, decodedCanonical)
		})
	}
}

func TestValidateAndDecodeClassifiesRejections(t *testing.T) {
	base := readFile(t, networkMinPath)

	t.Run("duplicate member", func(t *testing.T) {
		document := replaceOnce(t, base, managementLink, duplicateName)
		_, err := networkload.ValidateAndDecode(document, loadEmbedded(t))
		var malformed *networkload.JSONError
		if !errors.As(err, &malformed) {
			t.Fatalf("error = %v, want a JSONError", err)
		}
		if !strings.Contains(err.Error(), `"name" appears more than once`) {
			t.Fatalf("error = %q, want the duplicate member", err)
		}
	})

	t.Run("trailing data", func(t *testing.T) {
		document := append(append([]byte{}, base...), []byte("{}")...)
		_, err := networkload.ValidateAndDecode(document, loadEmbedded(t))
		var malformed *networkload.JSONError
		if !errors.As(err, &malformed) {
			t.Fatalf("error = %v, want a JSONError", err)
		}
	})

	t.Run("unknown member", func(t *testing.T) {
		document := replaceOnce(t, base, managementLink, unknownMember)
		_, err := networkload.ValidateAndDecode(document, loadEmbedded(t))
		var rejected *networkload.SchemaError
		if !errors.As(err, &rejected) {
			t.Fatalf("error = %v, want a SchemaError", err)
		}
		if !strings.Contains(err.Error(), "unknown-member") {
			t.Fatalf("error = %q, want the unknown member", err)
		}
	})

	t.Run("semantic defect", func(t *testing.T) {
		document := replaceOnce(t, base, uniqueTable, duplicateTable)
		_, err := networkload.ValidateAndDecode(document, loadEmbedded(t))
		if err == nil || !strings.Contains(err.Error(), duplicateTableMessage) {
			t.Fatalf("error = %v, want the duplicate table", err)
		}
		var malformed *networkload.JSONError
		var rejected *networkload.SchemaError
		if errors.As(err, &malformed) || errors.As(err, &rejected) {
			t.Fatalf("error = %v, want an unclassified semantic error", err)
		}
	})

	t.Run("closed schema", func(t *testing.T) {
		closed := loadEmbedded(t)
		closed.Close()
		loaded, err := networkload.ValidateAndDecode(base, closed)
		if !errors.Is(err, yangschema.ErrSchemaClosed) {
			t.Fatalf("error = %v, want ErrSchemaClosed", err)
		}
		if loaded != nil {
			t.Fatal("ValidateAndDecode decoded a document without schema validation")
		}
	})

	t.Run("absent schema", func(t *testing.T) {
		loaded, err := networkload.ValidateAndDecode(base, nil)
		if !errors.Is(err, yangschema.ErrSchemaClosed) {
			t.Fatalf("error = %v, want ErrSchemaClosed", err)
		}
		if loaded != nil {
			t.Fatal("ValidateAndDecode decoded a document without schema validation")
		}
	})
}

func TestLoadReturnsPathQualifiedErrors(t *testing.T) {
	base := readFile(t, networkMinPath)
	schemaDir := writeSchemaDir(t)
	absentPath := filepath.Join(t.TempDir(), "absent.json")
	absentSchemaDir := filepath.Join(t.TempDir(), "absent")
	unknownPath := writeDocument(t, replaceOnce(t, base, managementLink, unknownMember))
	duplicatePath := writeDocument(t, replaceOnce(t, base, managementLink, duplicateName))
	truncatedPath := writeDocument(t, base[:len(base)/2])
	semanticPath := writeDocument(t, replaceOnce(t, base, uniqueTable, duplicateTable))
	cases := []struct {
		name      string
		path      string
		schemaDir string
		prefix    string
		message   string
	}{
		{
			name: "unreadable document", path: absentPath, schemaDir: schemaDir,
			prefix: "read " + absentPath + ": ", message: "no such file",
		},
		{
			name: "unloadable schema", path: networkMinPath, schemaDir: absentSchemaDir,
			prefix: "load schema from " + absentSchemaDir + ": ", message: "ly_ctx_new",
		},
		{
			name: "schema violation", path: unknownPath, schemaDir: schemaDir,
			prefix: "validate " + unknownPath + ": ", message: "unknown-member",
		},
		{
			name: "duplicate member", path: duplicatePath, schemaDir: schemaDir,
			prefix: "decode " + duplicatePath + ": ", message: "appears more than once",
		},
		{
			name: "truncated document", path: truncatedPath, schemaDir: schemaDir,
			prefix: "decode " + truncatedPath + ": ", message: "unexpected EOF",
		},
		{
			name: "semantic defect", path: semanticPath, schemaDir: schemaDir,
			prefix: semanticPath + ": ", message: duplicateTableMessage,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loaded, err := networkload.Load(testCase.path, testCase.schemaDir)
			if err == nil {
				t.Fatal("Load succeeded")
			}
			if loaded != nil {
				t.Fatalf("Load returned a configuration with error %v", err)
			}
			if !strings.HasPrefix(err.Error(), testCase.prefix) {
				t.Fatalf("error = %q, want prefix %q", err, testCase.prefix)
			}
			if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("error = %q, want it to contain %q", err, testCase.message)
			}
		})
	}
}
