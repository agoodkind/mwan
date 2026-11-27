//go:build !systemd

package commandline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/mwan/gateway/internal/yangpub"
	"goodkind.io/mwan/gateway/networkjson"
	"goodkind.io/mwan/gateway/stackspec"
	"goodkind.io/mwan/gateway/yangpub/schema"
)

func runChild(t *testing.T, env []string, args ...string) string {
	t.Helper()
	command := exec.Command(os.Args[0], args...)
	command.Env = append(os.Environ(), env...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf(
			"child %v: %v\nstdout:\n%s\nstderr:\n%s",
			args,
			err,
			stdout.String(),
			stderr.String(),
		)
	}

	return stdout.String()
}

const stackFixtureDir = "testdata/rousette"

func seedStackModules(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, stackspec.RousetteYangDir)
	if _, err := os.Stat(dir); err == nil {
		return
	}

	if err := os.CopyFS(dir, os.DirFS(stackFixtureDir)); err != nil {
		t.Fatalf("copy %s to %s: %v", stackFixtureDir, dir, err)
	}
}

func runInstallChild(t *testing.T, root string) string {
	t.Helper()
	seedStackModules(t, root)
	env := []string{childMainEnv + "=1"}

	return runChild(t, env, "install", "--apply", "--role", "wan", "--root", root)
}

// Assign a distinct sysrepo prefix to each parallel child.
var sysrepoChildRuns atomic.Uint64

// Separate the sequence with an underscore to prevent matches against other children.
func sysrepoChildEnv(t *testing.T, root string, step string, prefix string) []string {
	t.Helper()
	shmPrefix := fmt.Sprintf(
		"mwaninstalltest%d_%d_%s",
		os.Getpid(),
		sysrepoChildRuns.Add(1),
		prefix,
	)
	t.Cleanup(func() { removeSelftestSHM(slog.New(slog.DiscardHandler), shmPrefix) })

	return []string{
		childSysrepoEnv + "=" + step,
		"SYSREPO_REPOSITORY_PATH=" + filepath.Join(root, "/etc/sysrepo"),
		"SYSREPO_SHM_PREFIX=" + shmPrefix,
		"SR_ENV_RUN_TESTS=1",
	}
}

type yangLibrary struct {
	Library struct {
		ModuleSets []struct {
			Modules []struct {
				Name     string   `json:"name"`
				Revision string   `json:"revision"`
				Features []string `json:"feature"`
			} `json:"module"`
		} `json:"module-set"`
	} `json:"ietf-yang-library:yang-library"`
}

type implementedModule struct {
	revision string
	features []string
}

func implementedModules(t *testing.T, root string, prefix string) map[string]implementedModule {
	t.Helper()
	tree := runChild(t, sysrepoChildEnv(t, root, "library", prefix))
	var library yangLibrary
	if err := json.Unmarshal([]byte(tree), &library); err != nil {
		t.Fatalf("decode the yang library: %v\n%s", err, tree)
	}

	modules := make(map[string]implementedModule)

	for _, set := range library.Library.ModuleSets {
		for _, module := range set.Modules {
			modules[module.Name] = implementedModule{
				revision: module.Revision,
				features: module.Features,
			}
		}
	}

	return modules
}

// Sysrepo and libyang supply their own revisions of the base type modules.
var gatewayModules = []string{
	"iana-if-type",
	"ietf-interfaces",
	"ietf-ip",
	"ietf-routing",
	"ietf-nat",
	"goodkind-mwan-steering",
}

func TestInstallApplyInstallsTheSchemaIntoSysrepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	first := runInstallChild(t, root)

	embeddedDir := t.TempDir()
	if _, err := yangpub.WriteSchema(embeddedDir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}

	modules := schema.Modules()
	for _, module := range modules {
		onDisk, err := os.ReadFile(filepath.Join(root, networkjson.DefaultSchemaDir, module.File))
		if err != nil {
			t.Errorf("read the installed %s: %v", module.File, err)

			continue
		}

		embedded, err := os.ReadFile(filepath.Join(embeddedDir, module.File))
		if err != nil {
			t.Fatalf("read the embedded %s: %v", module.File, err)
		}

		if !bytes.Equal(onDisk, embedded) {
			t.Errorf("the installed %s differs from the embedded copy", module.File)
		}
	}

	implemented := implementedModules(t, root, "a")

	for _, module := range modules {
		name := moduleFileName(module.File)
		if !slices.Contains(gatewayModules, name) {
			continue
		}

		revision := embeddedRevision(t, module.File)

		got, found := implemented[name]
		if !found {
			t.Errorf("%s is not implemented in the repository", name)

			continue
		}

		if got.revision != revision {
			t.Errorf("%s revision = %q, want %q", name, got.revision, revision)
		}

		for _, feature := range module.Features {
			if !slices.Contains(got.features, feature) {
				t.Errorf(
					"%s does not have feature %s enabled; enabled: %v",
					name,
					feature,
					got.features,
				)
			}
		}
	}

	wantStackModules := []struct {
		Name     string
		Revision string
	}{
		{Name: "ietf-restconf", Revision: "2017-01-26"},
		{Name: "ietf-restconf-monitoring", Revision: "2017-01-26"},
		{Name: "ietf-yang-patch", Revision: "2017-02-22"},
		{Name: "ietf-subscribed-notifications", Revision: "2019-09-09"},
		{Name: "ietf-restconf-subscribed-notifications", Revision: "2019-11-17"},
	}
	for _, module := range wantStackModules {
		got, found := implemented[module.Name]
		if !found {
			t.Errorf("%s is not implemented in the repository", module.Name)

			continue
		}

		if got.revision != module.Revision {
			t.Errorf("%s revision = %q, want %q", module.Name, got.revision, module.Revision)
		}

		wantLine := "installed module " + module.Name + "@" + module.Revision
		if !strings.Contains(first, wantLine) {
			t.Errorf("run output:\n%s\nwant a line %q", first, wantLine)
		}
	}

	second := runInstallChild(t, root)
	if !strings.Contains(second, "no change") || strings.Contains(second, " module ") {
		t.Fatalf("second run output:\n%s\nwant no change and no module lines", second)
	}
}

func moduleFileName(file string) string {
	name, _, _ := strings.Cut(strings.TrimSuffix(file, filepath.Ext(file)), "@")

	return name
}

func embeddedRevision(t *testing.T, file string) string {
	t.Helper()
	text, err := schema.Read(file)
	if err != nil {
		t.Fatal(err)
	}

	statement := regexp.MustCompile(`(?m)^\s*revision\s+"?(\d{4}-\d{2}-\d{2})`)
	revision := ""

	for _, match := range statement.FindAllSubmatch(text, -1) {
		revision = max(revision, string(match[1]))
	}

	return revision
}
