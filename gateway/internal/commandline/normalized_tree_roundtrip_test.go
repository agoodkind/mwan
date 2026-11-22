package commandline

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/yangpub"
)

const networkInstanceGlob = "../../yang/instances/*.json"

// Use each YANG list's key in the flattened path. Reject lists without a known key.
var networkListKeys = map[string][]string{
	"interface":          {"name"},
	"static-mapping":     {"external"},
	"address":            {"ip"},
	"forwarding-address": {"address"},
	"goodkind-mwan-steering:forwarding-address": {"address"},
	"goodkind-mwan-steering:route":              {"destination"},
	"file":                                      {"kind"},
	"section":                                   {"index"},
	"entry":                                     {"index"},
	"management-service":                        {"protocol", "port"},
	"bgp-session":                               {"name"},
	"import":                                    {"prefix"},
	"export":                                    {"prefix"},
}

// Allow these fields outside the input document. The published tree derives them.
var servedOnlyPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/enabled$`),
	regexp.MustCompile(
		`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/ietf-ip:ipv[46]/enabled$`,
	),
	regexp.MustCompile(
		`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/goodkind-mwan-steering:owner$`,
	),
	regexp.MustCompile(
		`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/goodkind-mwan-steering:steering/probe-policy$`,
	),
	regexp.MustCompile(
		`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/goodkind-mwan-steering:wan/v4-source$`,
	),
	regexp.MustCompile(`^/ietf-nat:nat/`),
	regexp.MustCompile(`^/goodkind-mwan-steering:daemon/`),
	regexp.MustCompile(`/firewall/management-service\[protocol='[^']+'\]\[port='[0-9]+'\]$`),
}

type leafPair struct {
	path  string
	value string
}

func TestPublishedTreeCarriesEveryNetworkLeaf(t *testing.T) {
	t.Parallel()
	documents, err := filepath.Glob(networkInstanceGlob)
	if err != nil {
		t.Fatalf("glob network documents: %v", err)
	}

	if len(documents) == 0 {
		t.Fatalf("no network document matches %s", networkInstanceGlob)
	}

	schemaDir := networkSchemaDirForTest(t)

	for _, document := range documents {
		t.Run(
			filepath.Base(document),
			func(t *testing.T) {
				t.Parallel()
				raw, err := os.ReadFile(document)
				if err != nil {
					t.Fatalf("read %s: %v", document, err)
				}

				inFile := flattenNetworkJSON(t, raw)
				served := withoutServedOnlyPairs(
					itemPairs(servedConfigItems(t, document, schemaDir)),
					inFile,
				)
				compareLeafSets(t, inFile, served)
			},
		)
	}
}

// Use the schema modules embedded in the gateway binary.
func networkSchemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := yangpub.WriteSchema(dir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}

	return dir
}

func servedConfigItems(t *testing.T, document string, schemaDir string) []wanconfig.Item {
	t.Helper()
	loaded, err := networkload.Load(document, schemaDir)
	if err != nil {
		t.Fatalf("load %s: %v", document, err)
	}

	var cfg config.Config
	loaded.Apply(&cfg)
	moduleConfigs, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("build wan module configs: %v", err)
	}

	gateway, ok, err := gatewayFromModuleConfigs(&cfg, moduleConfigs)
	if err != nil {
		t.Fatalf("project gateway: %v", err)
	}

	if !ok {
		t.Fatal("projection found no wan role configuration")
	}

	items, err := wanconfig.ConfigItems(gateway)
	if err != nil {
		t.Fatalf("build items: %v", err)
	}

	return items
}

func itemPairs(items []wanconfig.Item) []leafPair {
	pairs := make([]leafPair, 0, len(items))
	for _, item := range items {
		pairs = append(pairs, leafPair{path: item.Path, value: item.Value})
	}

	return pairs
}

// Compare a derived leaf when the document also supplies it.
func withoutServedOnlyPairs(pairs []leafPair, inFile []leafPair) []leafPair {
	kept := make([]leafPair, 0, len(pairs))
	for _, pair := range pairs {
		servedOnly := false

		for _, pattern := range servedOnlyPaths {
			if pattern.MatchString(pair.path) {
				servedOnly = !slices.Contains(inFile, pair)

				break
			}
		}

		if !servedOnly {
			kept = append(kept, pair)
		}
	}

	return kept
}

func flattenNetworkJSON(t *testing.T, raw []byte) []leafPair {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		t.Fatalf("decode document: %v", err)
	}

	var pairs []leafPair
	flattenObject(t, "", root, &pairs)

	return pairs
}

func flattenObject(t *testing.T, path string, object map[string]any, pairs *[]leafPair) {
	t.Helper()

	for _, name := range slices.Sorted(maps.Keys(object)) {
		flattenMember(t, path+"/"+name, name, object[name], pairs)
	}
}

func flattenMember(t *testing.T, path string, name string, value any, pairs *[]leafPair) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		flattenObject(t, path, typed, pairs)
	case []any:
		flattenArray(t, path, name, typed, pairs)
	default:
		*pairs = append(*pairs, leafPair{path: path, value: scalarText(t, path, typed)})
	}
}

func flattenArray(t *testing.T, path string, name string, elements []any, pairs *[]leafPair) {
	t.Helper()

	for _, element := range elements {
		entry, isEntry := element.(map[string]any)
		if !isEntry {
			*pairs = append(*pairs, leafPair{path: path, value: scalarText(t, path, element)})

			continue
		}

		keyLeaves, known := networkListKeys[name]
		if !known {
			t.Fatalf("list %s has no key in networkListKeys; add its key", path)
		}

		entryPath := path

		for _, keyLeaf := range keyLeaves {
			keyValue, present := entry[keyLeaf]
			if !present {
				t.Fatalf("list %s has an entry with no %s", path, keyLeaf)
			}

			entryPath += "[" + keyLeaf + "='" + scalarText(t, path, keyValue) + "']"
		}

		for _, leaf := range slices.Sorted(maps.Keys(entry)) {
			if slices.Contains(keyLeaves, leaf) {
				continue
			}

			flattenMember(t, entryPath+"/"+leaf, leaf, entry[leaf], pairs)
		}
	}
}

func scalarText(t *testing.T, path string, value any) string {
	t.Helper()
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}

		return "false"
	default:
		t.Fatalf("%s holds %T, which no leaf of this model encodes", path, value)

		return ""
	}
}

// Compare leaf values without requiring the datastore to preserve leaf-list order.
func compareLeafSets(t *testing.T, inFile []leafPair, served []leafPair) {
	t.Helper()
	fileLines := renderPairs(inFile)
	servedLines := renderPairs(served)
	if slices.Equal(fileLines, servedLines) {
		return
	}

	t.Fatalf(
		"served tree and file differ\nin file, not served:\n  %s\nserved, not in file:\n  %s",
		strings.Join(multisetDifference(fileLines, servedLines), "\n  "),
		strings.Join(multisetDifference(servedLines, fileLines), "\n  "),
	)
}

func renderPairs(pairs []leafPair) []string {
	lines := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		lines = append(lines, pair.path+" = "+pair.value)
	}

	slices.Sort(lines)

	return lines
}

func multisetDifference(from []string, subtract []string) []string {
	remaining := make(map[string]int, len(subtract))
	for _, line := range subtract {
		remaining[line]++
	}

	var difference []string

	for _, line := range from {
		if remaining[line] > 0 {
			remaining[line]--

			continue
		}

		difference = append(difference, line)
	}

	return difference
}
