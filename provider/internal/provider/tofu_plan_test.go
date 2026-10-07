//go:build tofu

package provider_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	tofuFixtureDir    = "testdata/networkplan"
	tofuBaseDocument  = "network-routes.json"
	tofuConfigAddress = "mwan_network_config.gateway"
	tofuFileAddress   = "terraform_data.file"
	tofuActionUpdate  = "update"
	tofuActionNoOp    = "no-op"
	tofuDecodeError   = "must be a canonical same-family network prefix"
)

type tofuPlan struct {
	ResourceChanges []tofuResourceChange `json:"resource_changes"`
}

type tofuResourceChange struct {
	Address string     `json:"address"`
	Change  tofuChange `json:"change"`
}

type tofuChange struct {
	Actions      []string        `json:"actions"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
	AfterUnknown json.RawMessage `json:"after_unknown"`
}

type tofuNetworkMaps struct {
	Interfaces       map[string]json.RawMessage `json:"interfaces"`
	Routes           map[string]json.RawMessage `json:"routes"`
	ProviderDefaults map[string]json.RawMessage `json:"provider_defaults"`
}

type tofuWorkspace struct {
	binary string
	dir    string
}

type tofuVariant struct {
	document     string
	configAction string
	fileAction   string
	changed      map[string]string
	lines        []string
}

type tofuRejection struct {
	document string
	want     []string
}

func TestTofuPlan(t *testing.T) {
	workspace := newTofuWorkspace(t)
	workspace.apply(t, tofuBaseDocument)
	workspace.checkVariants(t, tofuVariants())

	for _, rejection := range tofuRejections() {
		t.Run(strings.TrimSuffix(rejection.document, ".json"), func(t *testing.T) {
			output, err := workspace.run(t, "plan", "-input=false", "-no-color",
				"-var", "network_file="+tofuDocument(t, rejection.document))
			if err == nil {
				t.Fatalf("tofu plan succeeded for %s:\n%s", rejection.document, output)
			}
			normalized := normalizeTofuText(output)
			for _, want := range rejection.want {
				if !strings.Contains(normalized, want) {
					t.Errorf("tofu plan output lacks %q:\n%s", want, output)
				}
			}
		})
	}

	shorthand := newTofuWorkspace(t)
	shorthand.apply(t, "gateway.json")
	shorthand.checkVariants(t, []tofuVariant{{
		document:     "gatewaymetric.json",
		configAction: tofuActionUpdate,
		fileAction:   tofuActionUpdate,
		changed:      map[string]string{"routes[enmgmt0|ipv4|0.0.0.0/0]": tofuGatewayRoute("20")},
		lines: []string{
			`~ "enmgmt0|ipv4|0.0.0.0/0" = {`,
			`~ metric = 10 -> 20`,
			`# (6 unchanged attributes hidden)`,
		},
	}})
}

func tofuRejections() []tofuRejection {
	return []tofuRejection{
		{document: "invalid.json", want: []string{tofuDecodeError, `"198.18.1.1/24"`}},
		{document: "syntax.json", want: []string{"Invalid network document"}},
		{
			document: "duplicate.json",
			want:     []string{"Invalid network document", `object member "name" appears more than once`},
		},
		{
			document: "rejected.json",
			want:     []string{"Rejected provider entry", "enmbrains0", "from-prio is required"},
		},
	}
}

func tofuGatewayRoute(metric string) string {
	return `{"destination":"0.0.0.0/0","family":"ipv4","gateway":"192.0.2.1","interface":"enmgmt0",` +
		`"metric":` + metric + `,"source":"gateway","table_id":254}`
}

func tofuVariants() []tofuVariant {
	return []tofuVariant{
		{
			document:     "gateway.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{"routes[enmgmt0|ipv4|0.0.0.0/0]": tofuGatewayRoute("10")},
			lines: []string{
				`+ "enmgmt0|ipv4|0.0.0.0/0" = {`,
				`+ destination = "0.0.0.0/0"`,
				`+ gateway = "192.0.2.1"`,
				`+ metric = 10`,
				`+ source = "gateway"`,
				`+ table_id = 254`,
			},
		},
		{
			document:     "upper.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{},
			lines:        []string{`# terraform_data.file will be updated in-place`},
		},
		{
			document:     "defaults.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{},
			lines:        []string{`# terraform_data.file will be updated in-place`},
		},
		{
			document:     "added.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"routes[enwebpass0|ipv4|198.18.3.0/24]": `{"destination":"198.18.3.0/24","family":"ipv4",` +
					`"gateway":"203.0.113.1","interface":"enwebpass0","metric":0,"source":"route","table_id":254}`,
			},
			lines: []string{
				`# mwan_network_config.gateway will be updated in-place`,
				`~ resource "mwan_network_config" "gateway" {`,
				`~ routes = {`,
				`+ "enwebpass0|ipv4|198.18.3.0/24" = {`,
				`+ destination = "198.18.3.0/24"`,
				`+ family = "ipv4"`,
				`+ gateway = "203.0.113.1"`,
				`+ interface = "enwebpass0"`,
				`+ metric = 0`,
				`+ source = "route"`,
				`+ table_id = 254`,
				`# (2 unchanged attributes hidden)`,
			},
		},
		{
			document:     "removed.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{"routes[enwebpass0|ipv4|198.18.2.0/24]": ""},
			lines: []string{
				`~ routes = {`,
				`- "enwebpass0|ipv4|198.18.2.0/24" = {`,
				`- destination = "198.18.2.0/24"`,
				`- family = "ipv4"`,
				`- gateway = "203.0.113.1"`,
				`- interface = "enwebpass0"`,
				`- metric = 50`,
				`- source = "route"`,
				`- table_id = 254`,
				`} -> null,`,
			},
		},
		{
			document:     "changed.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"routes[enwebpass0|ipv4|198.18.0.0/24]": `{"destination":"198.18.0.0/24","family":"ipv4",` +
					`"gateway":"203.0.113.9","interface":"enwebpass0","metric":50,"source":"route","table_id":254}`,
			},
			lines: []string{
				`~ "enwebpass0|ipv4|198.18.0.0/24" = {`,
				`~ gateway = "203.0.113.1" -> "203.0.113.9"`,
				`~ metric = 0 -> 50`,
				`# (5 unchanged attributes hidden)`,
			},
		},
		{
			document:     "onlink.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"routes[enwebpass0|ipv6|2001:db8:a00::/48]": `{"destination":"2001:db8:a00::/48","family":"ipv6",` +
					`"gateway":null,"interface":"enwebpass0","metric":0,"source":"route","table_id":254}`,
			},
			lines: []string{
				`~ "enwebpass0|ipv6|2001:db8:a00::/48" = {`,
				`- gateway = "2001:db8:ff::1"`,
				`# (6 unchanged attributes hidden)`,
			},
		},
		{
			document:     "internalnet.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"provider_defaults[att|ipv4|100]":          tofuInternalDefault("att", "enatt0", "100"),
				"provider_defaults[webpass|ipv4|200]":      tofuInternalDefault("webpass", "enwebpass0", "200"),
				"provider_defaults[monkeybrains|ipv4|300]": tofuInternalDefault("monkeybrains", "enmbrains0", "300"),
			},
			lines: []string{
				`~ provider_defaults = {`,
				`~ "att|ipv4|100" = {`,
				`~ "webpass|ipv4|200" = {`,
				`~ "monkeybrains|ipv4|300" = {`,
				`~ internal_destination = "192.0.2.0/29" -> "192.0.2.0/28"`,
			},
		},
		{
			document:     "reformatted.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionNoOp,
			changed:      map[string]string{},
			lines:        []string{},
		},
		{
			document:     "reordered.json",
			configAction: tofuActionNoOp,
			fileAction:   tofuActionUpdate,
			changed:      map[string]string{},
			lines:        []string{`# terraform_data.file will be updated in-place`},
		},
		{
			document:     "lan.json",
			configAction: tofuActionUpdate,
			fileAction:   tofuActionUpdate,
			changed: map[string]string{
				"interfaces[lan0]": `{"connection_id":"lan0","enabled":null,"name":"lan0","owner":"external",` +
					`"provider_name":null,"roles":[],"type":"iana-if-type:other"}`,
			},
			lines: []string{
				`~ interfaces = {`,
				`+ "lan0" = {`,
				`+ name = "lan0"`,
				`+ roles = []`,
			},
		},
	}
}

func tofuInternalDefault(connectionID string, iface string, tableID string) string {
	return `{"connection_id":"` + connectionID + `","family":"ipv4","interface":"` + iface +
		`","internal_destination":"192.0.2.0/28","internal_interface":"enmwanbr0","table_id":` + tableID + `}`
}

func newTofuWorkspace(t *testing.T) *tofuWorkspace {
	t.Helper()
	binary := os.Getenv("TOFU")
	if binary == "" {
		t.Fatal("TOFU must contain the path of the OpenTofu binary; run make test-tofu-plan")
	}
	// OpenTofu needs dev_overrides to load the local provider build.
	if os.Getenv("TF_CLI_CONFIG_FILE") == "" {
		t.Fatal("TF_CLI_CONFIG_FILE must contain the dev_overrides configuration; run make test-tofu-plan")
	}
	dir := t.TempDir()
	module, err := os.ReadFile(filepath.Join(tofuFixtureDir, "main.tf"))
	if err != nil {
		t.Fatalf("read main.tf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), module, 0o600); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	return &tofuWorkspace{binary: binary, dir: dir}
}

func tofuDocument(t *testing.T, name string) string {
	t.Helper()
	document, err := filepath.Abs(filepath.Join(tofuFixtureDir, name))
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	return document
}

func (w *tofuWorkspace) command(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.CommandContext(t.Context(), w.binary, args...)
	command.Dir = w.dir
	command.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	return command
}

func (w *tofuWorkspace) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	output, err := w.command(t, args...).CombinedOutput()
	return string(output), err
}

func (w *tofuWorkspace) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	output, err := w.run(t, args...)
	if err != nil {
		t.Fatalf("tofu %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func (w *tofuWorkspace) apply(t *testing.T, document string) {
	t.Helper()
	w.mustRun(t, "apply", "-auto-approve", "-input=false", "-no-color",
		"-var", "network_file="+tofuDocument(t, document))
}

func (w *tofuWorkspace) checkVariants(t *testing.T, variants []tofuVariant) {
	t.Helper()
	for _, variant := range variants {
		t.Run(strings.TrimSuffix(variant.document, ".json"), func(t *testing.T) {
			plan, text := w.plan(t, variant.document)
			checkTofuPlan(t, plan, text, variant)
		})
	}
}

func (w *tofuWorkspace) plan(t *testing.T, document string) (tofuPlan, string) {
	t.Helper()
	planFile := filepath.Join(t.TempDir(), "tfplan")
	w.mustRun(t, "plan", "-input=false", "-no-color", "-out="+planFile,
		"-var", "network_file="+tofuDocument(t, document))

	command := w.command(t, "show", "-json", planFile)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	shown, err := command.Output()
	if err != nil {
		t.Fatalf("tofu show -json: %v\n%s", err, stderr.String())
	}
	var plan tofuPlan
	if err := json.Unmarshal(shown, &plan); err != nil {
		t.Fatalf("decode the JSON plan: %v", err)
	}
	return plan, w.mustRun(t, "show", "-no-color", planFile)
}

func checkTofuPlan(t *testing.T, plan tofuPlan, text string, variant tofuVariant) {
	t.Helper()
	for _, change := range plan.ResourceChanges {
		if slices.Contains(change.Change.Actions, "delete") {
			t.Errorf("%s has actions %v, want no delete or replacement", change.Address, change.Change.Actions)
		}
	}
	config := findTofuChange(t, plan, tofuConfigAddress)
	checkTofuAction(t, config, variant.configAction)
	checkTofuAction(t, findTofuChange(t, plan, tofuFileAddress), variant.fileAction)

	if got := changedTofuEntries(t, config.Change); !maps.Equal(got, variant.changed) {
		t.Errorf("%s changed entries:\ngot  %v\nwant %v", tofuConfigAddress, got, variant.changed)
	}
	if variant.configAction == tofuActionUpdate {
		if hasUnknownTofuValue(t, config.Change.AfterUnknown) {
			t.Errorf("%s after_unknown = %s, want every value known", tofuConfigAddress, config.Change.AfterUnknown)
		}
		block := tofuResourceBlock(t, text, tofuConfigAddress)
		if strings.Contains(block, "known after apply") {
			t.Errorf("%s renders an unknown value:\n%s", tofuConfigAddress, block)
		}
	}

	unchanged := variant.configAction == tofuActionNoOp && variant.fileAction == tofuActionNoOp
	if unchanged && !strings.HasPrefix(normalizeTofuText(text), "No changes.") {
		t.Errorf("the rendered plan does not start with No changes.:\n%s", text)
	}
	lines := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		lines[normalizeTofuText(line)] = true
	}
	for _, want := range variant.lines {
		if !lines[want] {
			t.Errorf("the rendered plan lacks the line %q:\n%s", want, text)
		}
	}
}

func findTofuChange(t *testing.T, plan tofuPlan, address string) tofuResourceChange {
	t.Helper()
	for _, change := range plan.ResourceChanges {
		if change.Address == address {
			return change
		}
	}
	t.Fatalf("the plan has no resource change for %s", address)
	return tofuResourceChange{Address: "", Change: tofuChange{Actions: nil, Before: nil, After: nil, AfterUnknown: nil}}
}

func checkTofuAction(t *testing.T, change tofuResourceChange, want string) {
	t.Helper()
	if !slices.Equal(change.Change.Actions, []string{want}) {
		t.Errorf("%s actions = %v, want [%s]", change.Address, change.Change.Actions, want)
	}
}

func changedTofuEntries(t *testing.T, change tofuChange) map[string]string {
	t.Helper()
	before := decodeTofuNetworkMaps(t, change.Before)
	after := decodeTofuNetworkMaps(t, change.After)
	changed := map[string]string{}
	compareTofuEntries(t, changed, "interfaces", before.Interfaces, after.Interfaces)
	compareTofuEntries(t, changed, "routes", before.Routes, after.Routes)
	compareTofuEntries(t, changed, "provider_defaults", before.ProviderDefaults, after.ProviderDefaults)
	return changed
}

func decodeTofuNetworkMaps(t *testing.T, value json.RawMessage) tofuNetworkMaps {
	t.Helper()
	var decoded tofuNetworkMaps
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatalf("decode the network maps %s: %v", value, err)
	}
	return decoded
}

func compareTofuEntries(
	t *testing.T,
	changed map[string]string,
	attribute string,
	before map[string]json.RawMessage,
	after map[string]json.RawMessage,
) {
	t.Helper()
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		beforeEntry, afterEntry := compactTofuJSON(t, before[key]), compactTofuJSON(t, after[key])
		if beforeEntry != afterEntry {
			changed[attribute+"["+key+"]"] = afterEntry
		}
	}
}

func compactTofuJSON(t *testing.T, value json.RawMessage) string {
	t.Helper()
	if value == nil {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		t.Fatalf("compact %s: %v", value, err)
	}
	return compact.String()
}

// OpenTofu represents unknown values as true in after_unknown.
func hasUnknownTofuValue(t *testing.T, afterUnknown json.RawMessage) bool {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(afterUnknown))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			t.Fatalf("decode after_unknown %s: %v", afterUnknown, err)
		}
		if token == true {
			return true
		}
	}
}

func tofuResourceBlock(t *testing.T, text string, address string) string {
	t.Helper()
	_, block, found := strings.Cut(text, "# "+address+" ")
	if !found {
		t.Fatalf("the rendered plan has no change for %s:\n%s", address, text)
	}
	block, _, _ = strings.Cut(block, "\n\n")
	return block
}

// OpenTofu's diagnostic borders and line wrapping require text normalization.
func normalizeTofuText(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "│", " ")), " ")
}
