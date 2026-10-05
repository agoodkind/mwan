package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// sysrepoRepositoryDir is the repository the gateway's sysrepo is compiled
// to use. A run under --root keeps its own repository at this path below the
// root instead of touching the host's.
const sysrepoRepositoryDir = "/etc/sysrepo"

// nacmPolicy is the read-only RESTCONF access policy: NACM denies every write
// and grants the anonymous user read access, the contract rousette serves
// anonymous clients under.
//
//go:embed nacm-anonymous.xml
var nacmPolicy []byte

const (
	// nacmPolicyPath is where the policy lands on the host, the path the
	// deploy has always written it to.
	nacmPolicyPath = "/etc/sysrepo-nacm-anonymous.xml"
	// nacmModule is the module the policy configures.
	nacmModule = "ietf-netconf-acm"
)

// nacmDatastores are the datastores the policy is imported into, in the order
// the deploy imported it: startup first, so a restarted sysrepo loads the
// policy, then running, so it applies now.
var nacmDatastores = []yangpub.Datastore{yangpub.DatastoreStartup, yangpub.DatastoreRunning}

// schemaOutcome is what the datastore half of an install did.
type schemaOutcome struct {
	// changed names the files it rewrote: schema modules and the policy.
	changed []string
	// modules names the schema modules it installed or updated.
	modules []yangpub.ModuleChange
	// nacmImported names the datastores it imported the policy into.
	nacmImported []yangpub.Datastore
}

// installSchema writes the embedded modules into the schema directory the
// daemon validates its network file against and installs or updates them in
// sysrepo from that directory. It then imports the NACM policy into each of
// startup and running whose ietf-netconf-acm configuration differs from the
// embedded policy, and writes the policy file. It returns what it did, even
// when it fails partway.
//
// The datastore decides the import, not the file: a repository reset under a
// file left from an earlier deploy still needs the policy.
//
// A run under root writes below the root and uses a private repository below
// the root, so it never touches the host's datastore.
func installSchema(ctx context.Context, log *slog.Logger, root string) (schemaOutcome, error) {
	outcome := schemaOutcome{changed: nil, modules: nil, nacmImported: nil}
	schemaDir := filepath.Join(root, networkjson.DefaultSchemaDir)
	models, changed, err := yangpub.WriteSchemaChanges(schemaDir)
	outcome.changed = changed
	if err != nil {
		return outcome, installFailed("write the schema into", schemaDir, err)
	}
	policyPath := filepath.Join(root, nacmPolicyPath)
	if root == "" {
		if err := applyDatastore(ctx, log, models, schemaDir, &outcome); err != nil {
			return outcome, err
		}
		return outcome, writePolicy(policyPath, &outcome)
	}
	repository := filepath.Join(root, sysrepoRepositoryDir)
	if err := os.MkdirAll(repository, 0o750); err != nil {
		return outcome, installFailed("create the private repository", repository, err)
	}
	// sysrepo reads its repository path and shared-memory prefix from the
	// environment at the process's first connection and keeps both for the
	// life of the process. The command runs once per process, so setting them
	// here reaches sysrepo; the check below refuses to go on when something
	// earlier in the process already bound sysrepo to another repository.
	// The separator after the process id keeps the shared-memory removal's
	// glob from matching another run whose process id starts with this one.
	shmPrefix := fmt.Sprintf("mwaninstall%d_", os.Getpid())
	restoreEnv := setSelftestEnv([]envSetting{
		{name: "SYSREPO_REPOSITORY_PATH", value: repository},
		{name: "SYSREPO_SHM_PREFIX", value: shmPrefix},
		// The same reason as the private selftest: the rooted repository is
		// this run's own, so sysrepo's group policy has nothing to protect,
		// and without this the run needs a sysrepo group on every machine.
		{name: "SR_ENV_RUN_TESTS", value: "1"},
	})
	defer restoreEnv()
	defer removeSelftestSHM(log, shmPrefix)
	if bound := yangpub.RepositoryPath(); bound != repository {
		return outcome, fmt.Errorf(
			"sysrepo in this process is bound to repository %s, not the private repository %s",
			bound, repository)
	}
	if err := applyDatastore(ctx, log, models, schemaDir, &outcome); err != nil {
		return outcome, err
	}
	return outcome, writePolicy(policyPath, &outcome)
}

// writePolicy writes the embedded policy to path and records it in outcome
// when the content changed.
func writePolicy(path string, outcome *schemaOutcome) error {
	changed, err := installfile.Write(path, nacmPolicy, systemdUnitMode)
	if err != nil {
		return installFailed("write the NACM policy", path, err)
	}
	if changed {
		outcome.changed = append(outcome.changed, path)
	}
	return nil
}

// applyDatastore connects to the datastore the environment names, brings the
// models in, imports the policy into each datastore that does not already
// hold it, and disconnects. It records what it did in outcome.
func applyDatastore(
	ctx context.Context,
	log *slog.Logger,
	models []yangpub.Model,
	searchDir string,
	outcome *schemaOutcome,
) error {
	datastore, err := yangpub.New(log)
	if err != nil {
		return installFailed("connect to sysrepo for", "the schema modules", err)
	}
	defer func() { _ = datastore.Close() }()
	modules, err := datastore.InstallModules(ctx, models, searchDir)
	outcome.modules = modules
	if err != nil {
		return installFailed("install into sysrepo", "the schema modules", err)
	}
	for _, ds := range nacmDatastores {
		matches, err := datastore.ConfigMatches(ctx, ds, nacmModule, nacmPolicy)
		if err != nil {
			return installFailed("read the NACM policy in", string(ds), err)
		}
		if matches {
			continue
		}
		if err := datastore.ImportConfig(ctx, ds, nacmModule, nacmPolicy); err != nil {
			return installFailed("import the NACM policy into", string(ds), err)
		}
		outcome.nacmImported = append(outcome.nacmImported, ds)
	}
	return nil
}
