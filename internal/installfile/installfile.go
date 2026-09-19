// Package installfile writes a file the binary owns onto the host, and
// reports whether that changed anything. Every install path in the program
// uses it, so "changed" means the same thing for a unit file, a schema
// module, and anything added later.
package installfile

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
)

// dirMode is the mode Write gives a parent directory it has to create.
const dirMode fs.FileMode = 0o755

// Write puts content at path and reports whether the file changed. A file
// already holding exactly these bytes is left untouched, which is what makes
// a second run of an install verb report nothing and leave every timestamp
// alone. The write itself replaces the file by rename, so a reader never
// sees a partial file and a failed write leaves the old one in place.
//
// Write does not compare the mode: an operator who tightened a mode by hand
// keeps it until the content changes.
func Write(path string, content []byte, mode fs.FileMode) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, failed("read the file in place", path, err)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, dirMode); err != nil {
		return false, failed("create the parent directory", parent, err)
	}
	if err := renameio.WriteFile(path, content, mode); err != nil {
		return false, failed("write the file", path, err)
	}
	return true, nil
}

// failed logs one failure where it happened and returns it wrapped under the
// same words, so an operator reads the same cause in the journal and in the
// message the command prints.
func failed(operation string, path string, err error) error {
	slog.Warn("installfile: "+operation+" failed", "path", path, "err", err)
	return fmt.Errorf("%s %s: %w", operation, path, err)
}
