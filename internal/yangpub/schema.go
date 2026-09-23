package yangpub

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/installfile"
)

// schemaFS carries the gateway's data model inside the binary. A gateway
// therefore serves the model its own release was built from, and a deploy
// cannot pair a binary with a schema from somewhere else. The directory's
// README records where each file came from.
//
//go:embed schema/*.yang
var schemaFS embed.FS

// schemaSubdir is the directory the embed directive above captured.
const schemaSubdir = "schema"

// schemaFileMode is the mode WriteSchema gives each written module. The
// deploy installs these world readable, because sysrepo and rousette read
// them as their own users.
const schemaFileMode fs.FileMode = 0o644

// schemaDirMode is the mode WriteSchema gives the directory it creates.
const schemaDirMode fs.FileMode = 0o755

// SchemaModule is one module of the gateway's model, named by its file and
// carrying the features that must be enabled when it is installed.
type SchemaModule struct {
	// File is the module's file name inside the embedded schema directory,
	// including the revision date the YANG convention puts there.
	File string
	// Features are the module's feature names to enable at install time. A
	// module whose leaves are not feature gated has none.
	Features []string
	// Update lets an install replace this module when the repository holds
	// it at another revision.
	Update bool
}

// SchemaModules lists the modules to install, in the order they install.
// Imports resolve from the directory rather than from this order, so the
// order only has to put a module after anything it augments.
//
// ietf-nat guards every enum value behind its nat-type features, so a module
// installed with no features enabled leaves those leaves with no valid value
// and libyang rejects it. The four named here are the translation types the
// steering model uses.
//
// Update is set on the five modules the deploy installed and updated with
// sysrepoctl. The deploy never updated the two base type modules or the
// interface-type registry: libyang and sysrepo load their own revisions of
// the base types, and the deploy installed the registry from rousette's model
// directory without an update step.
var SchemaModules = []SchemaModule{
	{File: "ietf-yang-types@2025-12-22.yang", Features: nil, Update: false},
	{File: "ietf-inet-types@2025-12-22.yang", Features: nil, Update: false},
	{File: "iana-if-type@2014-05-08.yang", Features: nil, Update: false},
	{File: "ietf-interfaces@2018-02-20.yang", Features: nil, Update: true},
	{File: "ietf-ip@2018-02-22.yang", Features: nil, Update: true},
	{File: "ietf-routing@2018-03-13.yang", Features: nil, Update: true},
	{
		File:     "ietf-nat@2019-01-10.yang",
		Features: []string{"basic-nat44", "napt44", "dst-nat", "nptv6"},
		Update:   true,
	},
	{File: "goodkind-mwan-steering@2026-09-22.yang", Features: nil, Update: true},
}

// WriteSchema writes every embedded module into dir, creating dir when it is
// absent, and returns the models in install order with the paths it wrote.
// A file already holding the right bytes is left alone, so the caller can run
// this against a directory it wrote before without changing any timestamp.
//
// The result is what InstallModules takes, and dir is the search directory
// that resolves the imports between them.
func WriteSchema(dir string) ([]Model, error) {
	models, _, err := WriteSchemaChanges(dir)
	return models, err
}

// WriteSchemaChanges is WriteSchema that also returns the path of every file
// whose content it replaced, in the order it wrote them, so an install verb
// can report what changed on the host.
func WriteSchemaChanges(dir string) ([]Model, []string, error) {
	if err := os.MkdirAll(dir, schemaDirMode); err != nil {
		return nil, nil, schemaFailed("create the schema directory", dir, err)
	}
	models := make([]Model, 0, len(SchemaModules))
	var changed []string
	for _, module := range SchemaModules {
		content, err := schemaFS.ReadFile(filepath.Join(schemaSubdir, module.File))
		if err != nil {
			return nil, changed, schemaFailed("read the embedded module", module.File, err)
		}
		path := filepath.Join(dir, module.File)
		wrote, err := installfile.Write(path, content, schemaFileMode)
		if err != nil {
			return nil, changed, schemaFailed("write the schema module", module.File, err)
		}
		if wrote {
			changed = append(changed, path)
		}
		models = append(models, Model{Path: path, Features: module.Features, Update: module.Update})
	}
	return models, changed, nil
}

// schemaFailed logs one failure where it happened and returns it wrapped
// under the same words, so the cause reads the same in the journal and in the
// message the caller prints.
func schemaFailed(operation string, name string, err error) error {
	slog.Warn("yangpub: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("%s %s: %w", operation, name, err)
}
