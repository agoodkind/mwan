package commandline

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type checkedUnitState struct {
	Mode     fs.FileMode
	Modified time.Time
	Content  string
}

func checkedUnitDirectory(t *testing.T, directory string) map[string]checkedUnitState {
	t.Helper()
	states := map[string]checkedUnitState{}
	if err := filepath.WalkDir(
		directory,
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}

			state := checkedUnitState{Mode: info.Mode(), Modified: info.ModTime(), Content: ""}
			if info.Mode().IsRegular() {
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}

				state.Content = string(content)
			}

			states[path] = state

			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	return states
}
