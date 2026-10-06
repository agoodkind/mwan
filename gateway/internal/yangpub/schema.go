package yangpub

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/yangpub/schema"
)

// WriteSchema preserves timestamps when installed module bytes already match.
// The returned models use installation order and resolve imports from dir.
func WriteSchema(dir string) ([]Model, error) {
	models, _, err := WriteSchemaChanges(dir)
	return models, err
}

// WriteSchemaChanges also returns changed file paths for installer reporting.
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

func schemaFailed(operation string, name string, err error) error {
	slog.Warn("yangpub: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("%s %s: %w", operation, name, err)
}
