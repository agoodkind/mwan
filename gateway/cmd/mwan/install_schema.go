package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/installspec"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// A rooted install uses this repository path below its selected root.
const sysrepoRepositoryDir = "/etc/sysrepo"

type schemaOutcome struct {
	changed      []string
	modules      []yangpub.ModuleChange
	nacmImported []yangpub.Datastore
}

// A policy file does not prove that either datastore contains the policy.
// Check both datastores even when that file exists after a repository reset.
// A rooted install uses its own repository and shared-memory prefix.
func installSchema(ctx context.Context, log *slog.Logger, root string) (schemaOutcome, error) {
	outcome := schemaOutcome{changed: nil, modules: nil, nacmImported: nil}
	schemaDir := filepath.Join(root, networkjson.DefaultSchemaDir)
	models, changed, err := yangpub.WriteSchemaChanges(schemaDir)
	outcome.changed = changed
	if err != nil {
		return outcome, installFailed("write the schema into", schemaDir, err)
	}
	policy, err := installspec.NACMPolicy()
	if err != nil {
		return outcome, installFailed("read the embedded file", installspec.NACMPolicyPath, err)
	}
	wanSpec, _ := installspec.For(installspec.RoleWAN)
	imports, err := wanSpec.SysrepoImports()
	if err != nil {
		return outcome, installFailed("read the embedded file", installspec.NACMPolicyPath, err)
	}
	policyPath := filepath.Join(root, installspec.NACMPolicyPath)
	if root == "" {
		if err := applyDatastore(ctx, log, models, schemaDir, imports, &outcome); err != nil {
			return outcome, err
		}
		return outcome, writePolicy(policyPath, policy, &outcome)
	}
	repository := filepath.Join(root, sysrepoRepositoryDir)
	if err := os.MkdirAll(repository, 0o750); err != nil {
		return outcome, installFailed("create the private repository", repository, err)
	}
	// sysrepo selects its repository path and shared-memory prefix at the first connection.
	// Configure both before opening the private repository.
	// The underscore delimits the process ID in the cleanup pattern.
	shmPrefix := fmt.Sprintf("mwaninstall%d_", os.Getpid())
	restoreEnv := setSelftestEnv([]envSetting{
		{name: "SYSREPO_REPOSITORY_PATH", value: repository},
		{name: "SYSREPO_SHM_PREFIX", value: shmPrefix},
		// The private repository does not require the host sysrepo group.
		{name: "SR_ENV_RUN_TESTS", value: "1"},
	})
	defer restoreEnv()
	defer removeSelftestSHM(log, shmPrefix)
	if bound := yangpub.RepositoryPath(); bound != repository {
		return outcome, fmt.Errorf(
			"sysrepo in this process is bound to repository %s, not the private repository %s",
			bound, repository)
	}
	if err := applyDatastore(ctx, log, models, schemaDir, imports, &outcome); err != nil {
		return outcome, err
	}
	return outcome, writePolicy(policyPath, policy, &outcome)
}

func writePolicy(path string, policy []byte, outcome *schemaOutcome) error {
	changed, err := installfile.Write(path, policy, installspec.FileMode)
	if err != nil {
		return installFailed("write the NACM policy", path, err)
	}
	if changed {
		outcome.changed = append(outcome.changed, path)
	}
	return nil
}

func applyDatastore(
	ctx context.Context,
	log *slog.Logger,
	models []yangpub.Model,
	searchDir string,
	imports []installspec.SysrepoImport,
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
	for _, entry := range imports {
		ds := yangpub.Datastore(entry.Datastore)
		matches, err := datastore.ConfigMatches(ctx, ds, entry.Module, entry.Content)
		if err != nil {
			return installFailed("read the NACM policy in", string(ds), err)
		}
		if matches {
			continue
		}
		if err := datastore.ImportConfig(ctx, ds, entry.Module, entry.Content); err != nil {
			return installFailed("import the NACM policy into", string(ds), err)
		}
		outcome.nacmImported = append(outcome.nacmImported, ds)
	}
	return nil
}
