package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// sysrepo resolves its repository and shared-memory prefix once per process
// and keeps them, so one process can reach only one repository. These tests
// therefore run every sysrepo step in a child process: the test binary re-runs
// itself, and TestMain turns the child into the step the variables name.
const (
	// childMainEnv makes the child run the mwan command line with its
	// arguments, the same entry point the installed binary has.
	childMainEnv = "MWAN_INSTALL_TEST_MAIN"
	// childSysrepoEnv makes the child run one sysrepo step against the
	// repository SYSREPO_REPOSITORY_PATH names: "seed" installs the model
	// files its arguments name, with the first argument as the search
	// directory, "library" prints the ietf-yang-library tree, and "export"
	// prints the subtree at its second argument in the datastore its first
	// argument names, or nothing when the subtree is empty.
	childSysrepoEnv = "MWAN_INSTALL_TEST_SYSREPO"
)

func TestMain(m *testing.M) {
	if os.Getenv(childMainEnv) != "" {
		main()
		os.Exit(0)
	}
	if step := os.Getenv(childSysrepoEnv); step != "" {
		os.Exit(runSysrepoStep(step, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runSysrepoStep is the child side of childSysrepoEnv.
func runSysrepoStep(step string, args []string) int {
	// The parent removes this child's shared memory, because it chose the
	// prefix.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	datastore, err := yangpub.New(log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		return 1
	}
	defer func() { _ = datastore.Close() }()
	switch step {
	case "seed":
		models := make([]yangpub.Model, 0, len(args)-1)
		for _, path := range args[1:] {
			models = append(models, yangpub.Model{Path: path, Features: seedFeatures(path), Update: false})
		}
		if _, err := datastore.InstallModules(context.Background(), models, args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "seed: %v\n", err)
			return 1
		}
		return 0
	case "library":
		tree, found, err := datastore.ExportJSON(
			context.Background(), yangpub.DatastoreOperational, "/ietf-yang-library:yang-library")
		if err != nil || !found {
			fmt.Fprintf(os.Stderr, "read the yang library: found=%v err=%v\n", found, err)
			return 1
		}
		fmt.Fprint(os.Stdout, tree)
		return 0
	case "export":
		tree, _, err := datastore.ExportJSON(context.Background(), yangpub.Datastore(args[0]), args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "export %s from %s: %v\n", args[1], args[0], err)
			return 1
		}
		fmt.Fprint(os.Stdout, tree)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown step %q\n", step)
	return 1
}

// seedFeatures gives a seeded module the features the embedded list names
// for it, so the seeded repository is the one a deploy would have left.
func seedFeatures(path string) []string {
	name, _ := moduleFileNameRevision(filepath.Base(path))
	for _, module := range yangpub.SchemaModules {
		if moduleName, _ := moduleFileNameRevision(module.File); moduleName == name {
			return module.Features
		}
	}
	return nil
}

// runChild runs the test binary as a child with env added, fails the test on
// a non-zero exit, and returns what the child printed on stdout.
func runChild(t *testing.T, env []string, args ...string) string {
	t.Helper()
	command := exec.Command(os.Args[0], args...)
	command.Env = append(os.Environ(), env...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("child %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// runInstallChild runs `mwan install --apply --role wan --root root` in a
// child process and returns its stdout.
func runInstallChild(t *testing.T, root string) string {
	t.Helper()
	return runChild(t, []string{childMainEnv + "=1"},
		"install", "--apply", "--role", "wan", "--root", root)
}

// sysrepoChildRuns numbers the sysrepo child steps, so parallel tests never
// share a shared-memory prefix.
var sysrepoChildRuns atomic.Uint64

// sysrepoChildEnv points a sysrepo step at the repository a rooted install
// keeps below root, with its own shared-memory prefix, and removes that
// prefix's shared memory when the test ends. The number in the prefix is
// unique to the call and ends in a separator, so the removal's glob matches
// no other call's segments.
func sysrepoChildEnv(t *testing.T, root string, step string, prefix string) []string {
	t.Helper()
	shmPrefix := fmt.Sprintf("mwaninstalltest%d_%d_%s", os.Getpid(), sysrepoChildRuns.Add(1), prefix)
	t.Cleanup(func() { removeSelftestSHM(slog.New(slog.DiscardHandler), shmPrefix) })
	return []string{
		childSysrepoEnv + "=" + step,
		"SYSREPO_REPOSITORY_PATH=" + filepath.Join(root, "/etc/sysrepo"),
		"SYSREPO_SHM_PREFIX=" + shmPrefix,
		"SR_ENV_RUN_TESTS=1",
	}
}

// yangLibrary is the part of the ietf-yang-library operational tree these
// tests read: each implemented module with its revision and enabled features.
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

// implementedModule is one row of yangLibrary.
type implementedModule struct {
	revision string
	features []string
}

// implementedModules reads which modules the rooted repository implements.
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
			modules[module.Name] = implementedModule{revision: module.Revision, features: module.Features}
		}
	}
	return modules
}

// gatewayModules are the modules the deploy installed into the gateway's
// repository with sysrepoctl: the five it installed and updated, and the
// interface-type registry. The two base type modules are left out, because
// libyang and sysrepo load their own revisions of those.
var gatewayModules = []string{
	"iana-if-type", "ietf-interfaces", "ietf-ip", "ietf-routing", "ietf-nat", "goodkind-mwan-steering",
}

// olderSteeringFixture is the steering model one revision before the one the
// binary embeds, byte for byte as configs deployed it, so the update path runs
// against a revision a gateway really carried.
const olderSteeringFixture = "testdata/goodkind-mwan-steering@2026-09-13.yang"

// TestInstallApplyInstallsTheSchemaIntoSysrepo runs `mwan install --apply
// --role wan` under a root, then reads the rooted repository with real
// sysrepo. Every gateway module must be implemented at its embedded revision,
// ietf-nat must carry the four translation features the deploy enables, the
// schema directory the daemon validates against must hold the embedded bytes,
// and a second run must change nothing.
func TestInstallApplyInstallsTheSchemaIntoSysrepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	runInstallChild(t, root)

	embeddedDir := t.TempDir()
	if _, err := yangpub.WriteSchema(embeddedDir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	for _, module := range yangpub.SchemaModules {
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

	modules := implementedModules(t, root, "a")
	for _, module := range yangpub.SchemaModules {
		name, revision := moduleFileNameRevision(module.File)
		if !slices.Contains(gatewayModules, name) {
			continue
		}
		got, found := modules[name]
		if !found {
			t.Errorf("%s is not implemented in the repository", name)
			continue
		}
		if got.revision != revision {
			t.Errorf("%s revision = %q, want %q", name, got.revision, revision)
		}
		for _, feature := range module.Features {
			if !slices.Contains(got.features, feature) {
				t.Errorf("%s does not have feature %s enabled; enabled: %v", name, feature, got.features)
			}
		}
	}

	second := runInstallChild(t, root)
	if !strings.Contains(second, "no change") || strings.Contains(second, " module ") {
		t.Fatalf("second run output:\n%s\nwant no change and no module lines", second)
	}
}

// TestInstallApplyUpdatesAnOlderSteeringRevision covers a gateway whose
// repository already carries the steering model at the revision before the
// binary's, which is every gateway the first time a release bumps the model.
// Installing alone matches the module by name and leaves the old revision in
// place, so the run must update it.
func TestInstallApplyUpdatesAnOlderSteeringRevision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	olderDir := t.TempDir()
	models, err := yangpub.WriteSchema(olderDir)
	if err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	older, err := os.ReadFile(olderSteeringFixture)
	if err != nil {
		t.Fatalf("read the older steering model: %v", err)
	}
	olderPath := filepath.Join(olderDir, filepath.Base(olderSteeringFixture))
	if err := os.WriteFile(olderPath, older, 0o644); err != nil {
		t.Fatalf("write the older steering model: %v", err)
	}
	// The older revision replaces the embedded one, both as the model to
	// seed and in the directory imports resolve from.
	seedArgs := []string{olderDir}
	for _, model := range models {
		if strings.HasPrefix(filepath.Base(model.Path), "goodkind-mwan-steering@") {
			if err := os.Remove(model.Path); err != nil {
				t.Fatalf("remove the embedded steering model: %v", err)
			}
			seedArgs = append(seedArgs, olderPath)
			continue
		}
		seedArgs = append(seedArgs, model.Path)
	}
	runChild(t, sysrepoChildEnv(t, root, "seed", "seed"), seedArgs...)
	if got := implementedModules(t, root, "before")["goodkind-mwan-steering"].revision; got != "2026-09-13" {
		t.Fatalf("seeded steering revision = %q, want 2026-09-13", got)
	}

	output := runInstallChild(t, root)

	_, wantRevision := moduleFileNameRevision(
		yangpub.SchemaModules[len(yangpub.SchemaModules)-1].File)
	got := implementedModules(t, root, "after")["goodkind-mwan-steering"]
	if got.revision != wantRevision {
		t.Fatalf("steering revision after the run = %q, want %q", got.revision, wantRevision)
	}
	wantLine := "updated module goodkind-mwan-steering from 2026-09-13 to " + wantRevision
	if !strings.Contains(output, wantLine) {
		t.Fatalf("run output:\n%s\nwant a line %q", output, wantLine)
	}
}

// moduleFileNameRevision splits a module file name, name@revision.yang.
func moduleFileNameRevision(file string) (name string, revision string) {
	name, revision, _ = strings.Cut(strings.TrimSuffix(file, filepath.Ext(file)), "@")
	return name, revision
}
