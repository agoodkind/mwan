package yangschema_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goodkind.io/mwan/internal/yangpub/schema"
	"goodkind.io/mwan/internal/yangschema"
)

const (
	// Acceptance and rejection tests use this network document.
	networkMinPath = "testdata/network-min.json"

	managementLink = `{ "name": "enmgmt0", "type": "iana-if-type:other" }`

	rejectionChildEnv = "MWAN_YANGSCHEMA_REJECTION_CHILD"

	rejectedDocument = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:no-such-leaf": true
      }
    ]
  }
}`
)

type schemaLoader struct {
	name string
	load func(t *testing.T) *yangschema.Schema
}

func schemaLoaders() []schemaLoader {
	return []schemaLoader{
		{name: "embedded", load: loadEmbedded},
		{name: "path", load: loadFromPath},
	}
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

func loadFromPath(t *testing.T) *yangschema.Schema {
	t.Helper()
	loaded, err := yangschema.LoadSchema(writeSchemaDir(t))
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	t.Cleanup(loaded.Close)
	return loaded
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

func readNetworkMin(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(networkMinPath)
	if err != nil {
		t.Fatalf("read %s: %v", networkMinPath, err)
	}
	return string(data)
}

func replaceOnce(t *testing.T, base string, old string, replacement string) string {
	t.Helper()
	if count := strings.Count(base, old); count != 1 {
		t.Fatalf("edit target %q occurs %d times, want 1", old, count)
	}
	return strings.Replace(base, old, replacement, 1)
}

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestSchemasAcceptNetworkMin(t *testing.T) {
	document := readNetworkMin(t)
	for _, loader := range schemaLoaders() {
		t.Run(loader.name, func(t *testing.T) {
			loaded := loader.load(t)
			if err := loaded.ValidateConfigJSON([]byte(document)); err != nil {
				t.Fatalf("ValidateConfigJSON rejected %s: %v", networkMinPath, err)
			}
		})
	}
}

func TestSchemasAcceptADocumentWithoutOptionalContainers(t *testing.T) {
	for _, loader := range schemaLoaders() {
		t.Run(loader.name, func(t *testing.T) {
			loaded := loader.load(t)
			if err := loaded.ValidateConfigJSON([]byte(`{}`)); err != nil {
				t.Fatalf("ValidateConfigJSON rejected an empty document: %v", err)
			}
		})
	}
}

func TestSchemasRejectSchemaViolations(t *testing.T) {
	base := readNetworkMin(t)
	cases := []struct {
		name        string
		old         string
		replacement string
		message     string
	}{
		{
			name:        "unknown member",
			old:         managementLink,
			replacement: `{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }`,
			message:     "unknown-member",
		},
		{
			name:        "invalid enum",
			old:         `"hash-mode": "source"`,
			replacement: `"hash-mode": "bogus"`,
			message:     "bogus",
		},
		{
			name:        "out-of-range value",
			old:         `"ping-count": 3,` + "\n" + `            "success-threshold": 2,`,
			replacement: `"ping-count": 256,` + "\n" + `            "success-threshold": 2,`,
			message:     "256",
		},
		{
			name:        "missing mandatory node",
			old:         managementLink,
			replacement: `{ "name": "enmgmt0" }`,
			message:     "type",
		},
		{
			name:        "explicit null leaf",
			old:         `"forced-dscp": 8`,
			replacement: `"forced-dscp": null`,
			message:     "uint8 value",
		},
		{
			name:        "state node",
			old:         managementLink,
			replacement: `{ "name": "enmgmt0", "type": "iana-if-type:other", "oper-status": "up" }`,
			message:     "oper-status",
		},
	}
	for _, loader := range schemaLoaders() {
		for _, testCase := range cases {
			t.Run(loader.name+"/"+testCase.name, func(t *testing.T) {
				loaded := loader.load(t)
				document := replaceOnce(t, base, testCase.old, testCase.replacement)
				err := loaded.ValidateConfigJSON([]byte(document))
				if err == nil {
					t.Fatal("ValidateConfigJSON accepted the document")
				}
				if !strings.Contains(err.Error(), testCase.message) {
					t.Fatalf("error = %q, want it to contain %q", err, testCase.message)
				}
			})
		}
	}
}

func TestClosedSchemaRejectsValidation(t *testing.T) {
	document := []byte(readNetworkMin(t))
	for _, loader := range schemaLoaders() {
		t.Run(loader.name, func(t *testing.T) {
			loaded := loader.load(t)
			if err := loaded.ValidateConfigJSON(document); err != nil {
				t.Fatalf("ValidateConfigJSON before Close: %v", err)
			}
			loaded.Close()
			loaded.Close()
			if err := loaded.ValidateConfigJSON(document); !errors.Is(err, yangschema.ErrSchemaClosed) {
				t.Fatalf("error after Close = %v, want ErrSchemaClosed", err)
			}
		})
	}
	var absent *yangschema.Schema
	absent.Close()
	if err := absent.ValidateConfigJSON(document); !errors.Is(err, yangschema.ErrSchemaClosed) {
		t.Fatalf("error from a nil schema = %v, want ErrSchemaClosed", err)
	}
}

func TestLoadEmbeddedRemovesItsTemporaryDirectoryOnClose(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	loaded, err := yangschema.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	defer loaded.Close()
	if names := entryNames(t, tempRoot); len(names) != 1 {
		t.Fatalf("temporary root entries while open = %v, want one schema directory", names)
	}
	if err := loaded.ValidateConfigJSON([]byte(readNetworkMin(t))); err != nil {
		t.Fatalf("ValidateConfigJSON: %v", err)
	}
	loaded.Close()
	if names := entryNames(t, tempRoot); len(names) != 0 {
		t.Fatalf("temporary root entries after Close = %v, want none", names)
	}
}

func TestLoadEmbeddedReportsAnUnusableTemporaryRoot(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "absent")
	t.Setenv("TMPDIR", tempRoot)
	loaded, err := yangschema.LoadEmbedded()
	if err == nil {
		loaded.Close()
		t.Fatal("LoadEmbedded succeeded without a temporary root")
	}
	if loaded != nil {
		t.Fatalf("LoadEmbedded returned a schema with error %v", err)
	}
	if !strings.Contains(err.Error(), tempRoot) {
		t.Fatalf("error = %q, want it to contain %q", err, tempRoot)
	}
}

func TestLoadSchemaFailureReturnsNoSchema(t *testing.T) {
	emptyDir := t.TempDir()
	corrupt := writeSchemaDir(t)
	corruptModule := filepath.Join(corrupt, schema.SteeringFile)
	if err := os.WriteFile(corruptModule, []byte("module goodkind-mwan-steering {"), 0o600); err != nil {
		t.Fatalf("write %s: %v", corruptModule, err)
	}
	cases := []struct {
		name    string
		dir     string
		message string
	}{
		{name: "absent directory", dir: filepath.Join(t.TempDir(), "absent"), message: "absent"},
		{name: "directory without modules", dir: emptyDir, message: "iana-if-type"},
		{name: "corrupt module", dir: corrupt, message: "goodkind-mwan-steering"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loaded, err := yangschema.LoadSchema(testCase.dir)
			if err == nil {
				loaded.Close()
				t.Fatal("LoadSchema succeeded")
			}
			if loaded != nil {
				t.Fatalf("LoadSchema returned a schema with error %v", err)
			}
			if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("error = %q, want it to contain %q", err, testCase.message)
			}
			recovered := loadFromPath(t)
			if err := recovered.ValidateConfigJSON([]byte(readNetworkMin(t))); err != nil {
				t.Fatalf("ValidateConfigJSON after a failed load: %v", err)
			}
		})
	}
}

func TestRejectionsReturnTheMessageWithoutPrintingToStderr(t *testing.T) {
	if os.Getenv(rejectionChildEnv) != "" {
		_, loadErr := yangschema.LoadSchema(filepath.Join(t.TempDir(), "absent"))
		loaded := loadEmbedded(t)
		validateErr := loaded.ValidateConfigJSON([]byte(rejectedDocument))
		fmt.Fprintf(os.Stdout, "load: %v\nvalidate: %v\n", loadErr, validateErr)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), rejectionChildEnv+"=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run the test binary: %v\nstderr: %s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want it empty", stderr.String())
	}
	for _, message := range []string{"load: yangschema: ly_ctx_new", `Node "no-such-leaf" not found`} {
		if !strings.Contains(stdout.String(), message) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout.String(), message)
		}
	}
}

// TestValidateConfigJSONAlwaysReturnsTheMessage checks that concurrent validators
// report the undefined leaf in each rejection.
func TestValidateConfigJSONAlwaysReturnsTheMessage(t *testing.T) {
	t.Parallel()
	dir := writeSchemaDir(t)

	const (
		validators = 32
		rounds     = 200
	)
	var wait sync.WaitGroup
	failures := make(chan string, validators)
	for i := 0; i < validators; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			loaded, err := yangschema.LoadSchema(dir)
			if err != nil {
				failures <- "load schema: " + err.Error()
				return
			}
			defer loaded.Close()
			for round := 0; round < rounds; round++ {
				err := loaded.ValidateConfigJSON([]byte(rejectedDocument))
				if err == nil {
					failures <- "a document with an undefined leaf was accepted"
					return
				}
				if !strings.Contains(err.Error(), "no-such-leaf") {
					failures <- "rejection lost its message: " + err.Error()
					return
				}
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}
