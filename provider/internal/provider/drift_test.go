//go:build drift

package provider_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/installspec"
)

const (
	binaryEnv    = "MWAN_BINARY"
	privateRepo  = "etc/sysrepo"
	enableMarker = "would enable "
	octalBase    = 8
	fileModeBits = 32
)

type installedFile struct {
	content []byte
	mode    fs.FileMode
}

// walkInstalled lists the regular files below root by absolute host path. The
// private sysrepo repository of a wan run is not an installed file.
func walkInstalled(t *testing.T, root string) map[string]installedFile {
	t.Helper()
	installed := map[string]installedFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == privateRepo {
			return fs.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		installed["/"+relative] = installedFile{content: content, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return installed
}

// TestProviderRoleMatchesMwanInstall runs the mwan binary's install verb under a
// private root for every role and compares every file it wrote, and the units
// it names, with the mwan_role data source.
func TestProviderRoleMatchesMwanInstall(t *testing.T) {
	binary := os.Getenv(binaryEnv)
	if binary == "" {
		t.Fatalf("%s must name the mwan binary built from the same commit", binaryEnv)
	}
	provider := newServer(t, buildCommit, "")

	for _, role := range installspec.Roles() {
		t.Run(string(role), func(t *testing.T) {
			root := t.TempDir()
			command := exec.Command(binary, "install", "--apply", "--role", string(role), "--root", root)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("mwan install --role %s: %v\n%s", role, err, output)
			}
			installed := walkInstalled(t, root)

			data, diagnostics := provider.readRole(t, string(role))
			if len(diagnostics) > 0 {
				t.Fatalf("read mwan_role %s: %v", role, diagnostics[0].Detail)
			}

			want := map[string]installedFile{}
			for _, file := range data.Files {
				want[file.Path] = installedFile{content: []byte(file.Content), mode: parseMode(t, file.Mode)}
			}
			for _, module := range data.Modules {
				want[module.Path] = installedFile{content: []byte(module.Content), mode: parseMode(t, module.Mode)}
			}

			for path, wantFile := range want {
				gotFile, found := installed[path]
				if !found {
					t.Errorf("mwan install did not write %s", path)
					continue
				}
				if !bytes.Equal(gotFile.content, wantFile.content) {
					t.Errorf("%s differs between mwan install and the provider", path)
				}
				if gotFile.mode != wantFile.mode {
					t.Errorf("%s mode = %v from mwan install, %v from the provider", path, gotFile.mode, wantFile.mode)
				}
			}
			for path := range installed {
				if _, listed := want[path]; !listed {
					t.Errorf("mwan install wrote %s, which the provider does not list", path)
				}
			}

			wantEnable := enableMarker + strings.Join(data.EnableUnits, " ")
			if !strings.Contains(string(output), wantEnable) {
				t.Errorf("mwan install output:\n%s\nwant the line %q", output, wantEnable)
			}
		})
	}
}

func parseMode(t *testing.T, text string) fs.FileMode {
	t.Helper()
	mode, err := strconv.ParseUint(text, octalBase, fileModeBits)
	if err != nil {
		t.Fatalf("file mode %q is not octal: %v", text, err)
	}
	return fs.FileMode(mode)
}
