package yangschema

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/yangpub/schema"
)

const (
	tempDirPattern             = "mwan-yangschema-*"
	tempFileMode   fs.FileMode = 0o600
)

// LoadEmbedded loads embedded modules from temporary files.
// [Schema.Close] removes the temporary directory.
func LoadEmbedded() (*Schema, error) {
	tempDir, err := os.MkdirTemp("", tempDirPattern)
	if err != nil {
		return nil, embeddedFailed("create the temporary schema directory", os.TempDir(), err)
	}
	loaded, err := loadEmbeddedFrom(tempDir)
	if err != nil {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			slog.Warn("yangschema: remove the temporary schema directory failed",
				"dir", tempDir, "err", removeErr)
		}
		return nil, err
	}
	return loaded, nil
}

func loadEmbeddedFrom(tempDir string) (*Schema, error) {
	for _, module := range schema.Modules() {
		content, err := schema.Read(module.File)
		if err != nil {
			return nil, embeddedFailed("read the embedded module", module.File, err)
		}
		path := filepath.Join(tempDir, module.File)
		if err := os.WriteFile(path, content, tempFileMode); err != nil {
			return nil, embeddedFailed("write the temporary schema module", path, err)
		}
	}
	ctx, err := newContext(tempDir)
	if err != nil {
		return nil, embeddedFailed("load the embedded schema from", tempDir, err)
	}
	return &Schema{ctx: ctx, tempDir: tempDir}, nil
}

func embeddedFailed(operation string, name string, err error) error {
	slog.Error("yangschema: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("yangschema: %s %s: %w", operation, name, err)
}
