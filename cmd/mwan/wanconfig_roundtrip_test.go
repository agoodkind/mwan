package main

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
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/yangpub"
)

// networkInstanceGlob finds every network document the YANG instance gate
// validates, so each document the schema accepts is also round-tripped here.
const networkInstanceGlob = "../../yang/instances/*.json"

// networkListKeys names the key leaf of every YANG list a network document
// carries, by the list's JSON member name. A list missing here fails the
// flattener, so a model change that adds a configuration list must name its key
// rather than be flattened into ambiguous paths.
var networkListKeys = map[string]string{
	"interface":      "name",
	"static-mapping": "external",
}

// servedOnlyPaths match the leaves the tree publishes that a network document
// does not carry: each interface's enabled flag and its two address-family
// enabled flags, the probe policy name, the translation instances, and the
// daemon settings. Every other published leaf must appear in the file.
var servedOnlyPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/enabled$`),
	regexp.MustCompile(`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/ietf-ip:ipv[46]/enabled$`),
	regexp.MustCompile(`^/ietf-interfaces:interfaces/interface\[name='[^']+'\]/goodkind-mwan-steering:steering/probe-policy$`),
	regexp.MustCompile(`^/ietf-nat:nat/`),
	regexp.MustCompile(`^/goodkind-mwan-steering:daemon/`),
}

// leafPair is one leaf instance: its data path and its value as text.
type leafPair struct {
	path  string
	value string
}

// TestPublishedTreeCarriesEveryNetworkLeaf proves the served tree carries every
// configuration value network.json carries, and nothing the file does not,
// apart from the leaves only the tree publishes. Each document goes through the
// daemon's own chain, from the loader to the published items, so a value the
// loader drops, a module config does not hold, or the projection does not emit
// shows up as a leaf in the file that is not served.
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
		t.Run(filepath.Base(document), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(document)
			if err != nil {
				t.Fatalf("read %s: %v", document, err)
			}
			inFile := flattenNetworkJSON(t, raw)
			served := withoutServedOnlyPairs(itemPairs(servedConfigItems(t, document, schemaDir)))
			compareLeafSets(t, inFile, served)
		})
	}
}

// networkSchemaDirForTest materialises the model set the gateway installs,
// from the bytes the binary embeds, which is the same set the loader's own
// tests use.
func networkSchemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := yangpub.WriteSchema(dir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	return dir
}

// servedConfigItems runs one network document through the chain the daemon
// runs at startup: load and validate it, apply it onto an empty configuration,
// build the wan role's module configs, project the gateway, and build the
// published items.
func servedConfigItems(t *testing.T, document string, schemaDir string) []wanconfig.Item {
	t.Helper()
	loaded, err := networkjson.Load(document, schemaDir)
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

// itemPairs renders published items as leaf pairs.
func itemPairs(items []wanconfig.Item) []leafPair {
	pairs := make([]leafPair, 0, len(items))
	for _, item := range items {
		pairs = append(pairs, leafPair{path: item.Path, value: item.Value})
	}
	return pairs
}

// withoutServedOnlyPairs drops the leaves only the tree publishes.
func withoutServedOnlyPairs(pairs []leafPair) []leafPair {
	kept := make([]leafPair, 0, len(pairs))
	for _, pair := range pairs {
		servedOnly := false
		for _, pattern := range servedOnlyPaths {
			if pattern.MatchString(pair.path) {
				servedOnly = true
				break
			}
		}
		if !servedOnly {
			kept = append(kept, pair)
		}
	}
	return kept
}

// flattenNetworkJSON turns a document in the model's JSON encoding into one pair
// per leaf instance, with paths in the form the published items use: member
// names joined by slashes, a list entry addressed by its key, and one pair per
// leaf-list value. Numbers keep their decimal text.
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

// flattenArray flattens a JSON array: an array of objects is a list, whose
// entries are addressed by their key, and an array of scalars is a leaf-list.
func flattenArray(t *testing.T, path string, name string, elements []any, pairs *[]leafPair) {
	t.Helper()
	for _, element := range elements {
		entry, isEntry := element.(map[string]any)
		if !isEntry {
			*pairs = append(*pairs, leafPair{path: path, value: scalarText(t, path, element)})
			continue
		}
		keyLeaf, known := networkListKeys[name]
		if !known {
			t.Fatalf("list %s has no key in networkListKeys; add its key", path)
		}
		keyValue, present := entry[keyLeaf]
		if !present {
			t.Fatalf("list %s has an entry with no %s", path, keyLeaf)
		}
		entryPath := path + "[" + keyLeaf + "='" + scalarText(t, path, keyValue) + "']"
		for _, leaf := range slices.Sorted(maps.Keys(entry)) {
			if leaf == keyLeaf {
				continue
			}
			flattenMember(t, entryPath+"/"+leaf, leaf, entry[leaf], pairs)
		}
	}
}

// scalarText renders one leaf value the way a published item carries it.
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

// compareLeafSets fails when the two pair sets differ as multisets, naming each
// side's extra leaves. Leaf-list order is not compared, because the datastore
// orders a system-ordered leaf-list itself.
func compareLeafSets(t *testing.T, inFile []leafPair, served []leafPair) {
	t.Helper()
	fileLines := renderPairs(inFile)
	servedLines := renderPairs(served)
	if slices.Equal(fileLines, servedLines) {
		return
	}
	t.Fatalf("served tree and file differ\nin file, not served:\n  %s\nserved, not in file:\n  %s",
		strings.Join(multisetDifference(fileLines, servedLines), "\n  "),
		strings.Join(multisetDifference(servedLines, fileLines), "\n  "))
}

func renderPairs(pairs []leafPair) []string {
	lines := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		lines = append(lines, pair.path+" = "+pair.value)
	}
	slices.Sort(lines)
	return lines
}

// multisetDifference returns the lines in from that remain after removing one
// occurrence per line in subtract.
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
