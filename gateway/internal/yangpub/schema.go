package yangpub

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/yangpub/schema"
)

// WriteSchema writes every embedded module into dir, creating dir when it is
// absent, and returns the models in install order with the paths it wrote.
// A file already holding the right bytes is left alone, and its timestamp does
// not change.
//
// The result is what InstallModules takes, and dir is the search directory
// that resolves the imports between them.
func WriteSchema(dir string) ([]Model, error) {
	models, _, err := WriteSchemaChanges(dir)
	return models, err
}

// WriteSchemaChanges is WriteSchema that also returns the path of every file
// it replaced, in the order it wrote them. An install verb reports these paths.
func WriteSchemaChanges(dir string) ([]Model, []string, error) {
	if err := os.MkdirAll(dir, schema.DirMode); err != nil {
		return nil, nil, schemaFailed("create the schema directory", dir, err)
	}
	modules := schema.Modules()
	models := make([]Model, 0, len(modules))
	var changed []string
	for _, module := range modules {
		content, err := schema.Read(module.File)
		if err != nil {
			return nil, changed, schemaFailed("read the embedded module", module.File, err)
		}
		path := filepath.Join(dir, module.File)
		wrote, err := installfile.Write(path, content, schema.FileMode)
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
// under the same words. The journal and the printed message give the same cause.
func schemaFailed(operation string, name string, err error) error {
	slog.Warn("yangpub: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("%s %s: %w", operation, name, err)
}
