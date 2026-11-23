package commandline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/installspec"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

const sysrepoRepositoryDir = "/etc/sysrepo"

const privateRepositoryMode = 0o750

var errRepositoryBindingMismatch = errors.New("sysrepo repository binding mismatch")

type schemaOutcome struct {
	changed      []string
	modules      []yangpub.ModuleChange
	nacmImported []yangpub.Datastore
}

// Check both datastores after a repository reset.
// The policy file can outlast the datastore entries.
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
	imports := wanSpec.SysrepoImports(policy)
	batches := []moduleBatch{{models: models, searchDir: schemaDir}}

	for _, module := range wanSpec.StackYangModules() {
		model := yangpub.Model{
			Path:     filepath.Join(root, module.Path()),
			Features: module.Features,
			Update:   false,
		}
		batches = append(
			batches,
			moduleBatch{
				models:    []yangpub.Model{model},
				searchDir: filepath.Join(root, module.Dir),
			},
		)
	}

	policyPath := filepath.Join(root, installspec.NACMPolicyPath)
	if root == "" {
		if err := applyDatastore(ctx, log, batches, imports, &outcome); err != nil {
			return outcome, err
		}

		return outcome, writePolicy(policyPath, policy, &outcome)
	}

	repository := filepath.Join(root, sysrepoRepositoryDir)
	if err := os.MkdirAll(repository, privateRepositoryMode); err != nil {
		return outcome, installFailed("create the private repository", repository, err)
	}

	// Set the repository path and shared memory prefix before opening sysrepo.
	// The underscore separates the process ID from the rest of the prefix.
	shmPrefix := fmt.Sprintf("mwaninstall%d_", os.Getpid())
	restoreEnv := setSelftestEnv([]envSetting{
		{name: "SYSREPO_REPOSITORY_PATH", value: repository},
		{name: "SYSREPO_SHM_PREFIX", value: shmPrefix},
		{name: "SR_ENV_RUN_TESTS", value: "1"},
	})
	defer restoreEnv()
	defer removeSelftestSHM(log, shmPrefix)
	if bound := yangpub.RepositoryPath(); bound != repository {
		return outcome, repositoryBindingMismatchError{bound: bound, private: repository}
	}

	if err := applyDatastore(ctx, log, batches, imports, &outcome); err != nil {
		return outcome, err
	}

	return outcome, writePolicy(policyPath, policy, &outcome)
}

type repositoryBindingMismatchError struct {
	bound   string
	private string
}

func (err repositoryBindingMismatchError) Error() string {
	return fmt.Sprintf(
		"sysrepo in this process is bound to repository %s, not the private repository %s",
		err.bound,
		err.private,
	)
}

func (err repositoryBindingMismatchError) Unwrap() error {
	return errRepositoryBindingMismatch
}

type moduleBatch struct {
	models    []yangpub.Model
	searchDir string
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
	batches []moduleBatch,
	imports []installspec.SysrepoImport,
	outcome *schemaOutcome,
) error {
	datastore, err := yangpub.New(log)
	if err != nil {
		return installFailed("connect to sysrepo for", "the schema modules", err)
	}

	defer func() { _ = datastore.Close() }()

	for _, batch := range batches {
		modules, err := datastore.InstallModules(ctx, batch.models, batch.searchDir)
		outcome.modules = append(outcome.modules, modules...)
		if err != nil {
			return installFailed("install into sysrepo", "the schema modules", err)
		}
	}

	for _, entry := range imports {
		matches, err := datastore.ConfigMatches(ctx, entry.Datastore, entry.Module, entry.Content)
		if err != nil {
			return installFailed("read the NACM policy in", string(entry.Datastore), err)
		}

		if matches {
			continue
		}

		if err := datastore.ImportConfig(
			ctx,
			entry.Datastore,
			entry.Module,
			entry.Content,
		); err != nil {
			return installFailed("import the NACM policy into", string(entry.Datastore), err)
		}

		outcome.nacmImported = append(outcome.nacmImported, entry.Datastore)
	}

	return nil
}
