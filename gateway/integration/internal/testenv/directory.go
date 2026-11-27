//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type CheckedUnitState struct {
	Mode     fs.FileMode
	Modified time.Time
	Content  string
}

func CheckedUnitDirectory(t *testing.T, directory string) map[string]CheckedUnitState {
	t.Helper()
	states := map[string]CheckedUnitState{}
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

			state := CheckedUnitState{Mode: info.Mode(), Modified: info.ModTime(), Content: ""}
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
