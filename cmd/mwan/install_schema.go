package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// sysrepoRepositoryDir is the repository the gateway's sysrepo is compiled
// to use. A run under --root keeps its own repository at this path below the
// root instead of touching the host's.
const sysrepoRepositoryDir = "/etc/sysrepo"

// installSchema writes the embedded modules into the schema directory the
// daemon validates its network file against, then installs or updates them in
// sysrepo from that directory. It returns the schema files it rewrote and the
// modules it changed, even when it fails partway.
//
// A run under root writes the directory below the root and installs into a
// private repository below the root, so it never touches the host's datastore.
func installSchema(
	ctx context.Context, log *slog.Logger, root string,
) ([]string, []yangpub.ModuleChange, error) {
	schemaDir := filepath.Join(root, networkjson.DefaultSchemaDir)
	models, changed, err := yangpub.WriteSchemaChanges(schemaDir)
	if err != nil {
		return changed, nil, installFailed("write the schema into", schemaDir, err)
	}
	if root == "" {
		modules, err := installModules(ctx, log, models, schemaDir)
		return changed, modules, err
	}
	repository := filepath.Join(root, sysrepoRepositoryDir)
	if err := os.MkdirAll(repository, 0o750); err != nil {
		return changed, nil, installFailed("create the private repository", repository, err)
	}
	// sysrepo reads its repository path and shared-memory prefix from the
	// environment at the process's first connection and keeps both for the
	// life of the process. The command runs once per process, so setting them
	// here reaches sysrepo; the check below refuses to go on when something
	// earlier in the process already bound sysrepo to another repository.
	shmPrefix := fmt.Sprintf("mwaninstall%d", os.Getpid())
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
		return changed, nil, fmt.Errorf(
			"sysrepo in this process is bound to repository %s, not the private repository %s",
			bound, repository)
	}
	modules, err := installModules(ctx, log, models, schemaDir)
	return changed, modules, err
}

// installModules connects to the datastore the environment names, brings the
// models in, and disconnects.
func installModules(
	ctx context.Context, log *slog.Logger, models []yangpub.Model, searchDir string,
) ([]yangpub.ModuleChange, error) {
	datastore, err := yangpub.New(log)
	if err != nil {
		return nil, installFailed("connect to sysrepo for", "the schema modules", err)
	}
	defer func() { _ = datastore.Close() }()
	modules, err := datastore.InstallModules(ctx, models, searchDir)
	if err != nil {
		return modules, installFailed("install into sysrepo", "the schema modules", err)
	}
	return modules, nil
}
