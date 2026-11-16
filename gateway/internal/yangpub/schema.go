package yangpub

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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

		if err := removeDated(dir, module.File); err != nil {
			return nil, changed, err
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

func removeDated(dir string, file string) error {
	if strings.Contains(file, "@") {
		return nil
	}

	extension := filepath.Ext(file)
	prefix := strings.TrimSuffix(file, extension) + "@"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return schemaFailed("read the schema directory", dir, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, extension) {
			continue
		}

		if err := removeModule(dir, name); err != nil {
			return err
		}
	}

	return nil
}

func removeModule(dir string, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		return schemaFailed("remove the dated schema module", name, err)
	}

	slog.Info("yangpub: removed dated schema module", "dir", dir, "name", name)

	return nil
}

func schemaFailed(operation string, name string, err error) error {
	slog.Warn("yangpub: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("%s %s: %w", operation, name, err)
}
