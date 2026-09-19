package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/yangpub"
)

// selftestModelsDir writes the gateway's model files into a directory the
// private repository installs from. They come from the binary's embedded
// schema, so the selftest drives real sysrepo with the modules a gateway
// installs, at the revisions it installs them.
func selftestModelsDir(t *testing.T) string {
	t.Helper()
	modelsDir := t.TempDir()
	if _, err := yangpub.WriteSchema(modelsDir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	return modelsDir
}

// TestWanconfigSelftest_PrivateRepository runs the private selftest the
// way an operator would, against a repository and model directory the
// test assembles. It is the end-to-end proof of the serving contract:
// with real libyang and sysrepo, one operational read over a second
// connection carries the published configuration and the provided live
// state together, and Close releases both. It is not parallel because
// sysrepo reads its repository location from the process environment.
func TestWanconfigSelftest_PrivateRepository(t *testing.T) {
	modelsDir := selftestModelsDir(t)
	repository := filepath.Join(t.TempDir(), "repository")

	// The run must hand the process environment back the way it found it,
	// or a later connection in this binary reaches the private repository.
	// Two variables start set and one starts absent, so both restore paths
	// are exercised.
	const priorRepositoryPath = "/prior/repository"
	const priorSHMPrefix = "priorprefix"
	t.Setenv("SYSREPO_REPOSITORY_PATH", priorRepositoryPath)
	t.Setenv("SYSREPO_SHM_PREFIX", priorSHMPrefix)
	t.Setenv("SR_ENV_RUN_TESTS", "")
	if err := os.Unsetenv("SR_ENV_RUN_TESTS"); err != nil {
		t.Fatalf("unset SR_ENV_RUN_TESTS: %v", err)
	}

	code := runWanconfigSelftest([]string{"--repository", repository, "--models-dir", modelsDir})
	if code != 0 {
		t.Fatalf("private selftest exit code = %d, want 0", code)
	}

	if got := os.Getenv("SYSREPO_REPOSITORY_PATH"); got != priorRepositoryPath {
		t.Fatalf("SYSREPO_REPOSITORY_PATH after the run = %q, want %q", got, priorRepositoryPath)
	}
	if got := os.Getenv("SYSREPO_SHM_PREFIX"); got != priorSHMPrefix {
		t.Fatalf("SYSREPO_SHM_PREFIX after the run = %q, want %q", got, priorSHMPrefix)
	}
	if value, present := os.LookupEnv("SR_ENV_RUN_TESTS"); present {
		t.Fatalf("SR_ENV_RUN_TESTS after the run = %q, want it unset", value)
	}

	// The run's shared-memory segments carry this process's prefix and
	// must be gone, or every run leaves a set behind on the host.
	leftovers, err := filepath.Glob(filepath.Join(sysrepoSHMDir, fmt.Sprintf("mwanselftest%d*", os.Getpid())))
	if err != nil {
		t.Fatalf("list shared memory: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("shared memory left behind: %v", leftovers)
	}
}

// TestPublishedTreeRoundTripsThroughSysrepo publishes each network document's
// projection into a private repository with real libyang and sysrepo, then
// reads the running datastore back and compares it with the document. The
// item-level round trip cannot see a value the datastore refuses, and one
// refused item fails the whole replace, so this proves every published leaf is
// accepted and served unchanged. It is not parallel because sysrepo reads its
// repository location from the process environment.
func TestPublishedTreeRoundTripsThroughSysrepo(t *testing.T) {
	documents, err := filepath.Glob(networkInstanceGlob)
	if err != nil {
		t.Fatalf("glob network documents: %v", err)
	}
	if len(documents) == 0 {
		t.Fatalf("no network document matches %s", networkInstanceGlob)
	}
	schemaDir := networkSchemaDirForTest(t)
	flags := selftestFlags{
		repository: filepath.Join(t.TempDir(), "repository"),
		modelsDir:  selftestModelsDir(t),
	}
	log := slog.Default()
	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()

	reader, closeRepository, err := openPrivateRepository(ctx, log, flags)
	if err != nil {
		t.Fatalf("open private repository: %v", err)
	}
	defer closeRepository()
	daemon, err := yangpub.New(log)
	if err != nil {
		t.Fatalf("daemon connection: %v", err)
	}
	defer func() { _ = daemon.Close() }()

	for _, document := range documents {
		t.Run(filepath.Base(document), func(t *testing.T) {
			items := servedConfigItems(t, document, schemaDir)
			replacer := runningReplacer{pub: daemon}
			if err := replacer.ReplaceConfig(ctx, wanconfig.OwnedPaths, items); err != nil {
				t.Fatalf("publish %s: %v", document, err)
			}
			exported, found, err := reader.ExportJSON(ctx, yangpub.DatastoreRunning, "/ietf-interfaces:*")
			if err != nil {
				t.Fatalf("read running interfaces: %v", err)
			}
			if !found {
				t.Fatal("read running interfaces: nothing served")
			}
			raw, err := os.ReadFile(document)
			if err != nil {
				t.Fatalf("read %s: %v", document, err)
			}
			served := withoutServedOnlyPairs(flattenNetworkJSON(t, []byte(exported)))
			compareLeafSets(t, flattenNetworkJSON(t, raw), served)
		})
	}
}
