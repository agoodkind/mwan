//go:build drift

package provider_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
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

// The private sysrepo repository is test state, not an installed file.
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

func TestProviderRoleMatchesMwanInstall(t *testing.T) {
	binary := os.Getenv(binaryEnv)
	if binary == "" {
		t.Fatalf("%s must name the mwan binary built from the same commit", binaryEnv)
	}
	provider := newServer(t, buildCommit, "")

	for _, role := range installspec.Roles() {
		for _, guest := range config.GuestTypes() {
			t.Run(string(role)+"/"+string(guest), func(t *testing.T) {
				checkRoleMatchesInstall(t, provider, binary, role, guest)
			})
		}
	}
}

func checkRoleMatchesInstall(
	t *testing.T,
	provider *protocolServer,
	binary string,
	role installspec.Role,
	guest config.GuestType,
) {
	root := t.TempDir()
	command := exec.Command(
		binary, "install", "--apply", "--role", string(role), "--guest-type", string(guest), "--root", root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("mwan install --role %s --guest-type %s: %v\n%s", role, guest, err, output)
	}
	installed := walkInstalled(t, root)

	data, diagnostics := provider.readRoleForGuest(t, string(role), string(guest))
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
		// The policy is compared with sysrepo_data below.
		if path == installspec.NACMPolicyPath {
			continue
		}
		if _, listed := want[path]; !listed {
			t.Errorf("mwan install wrote %s, which the provider does not list", path)
		}
	}

	spec, _ := installspec.For(role)
	unitNames := make([]string, 0, len(data.Units))
	for _, unit := range data.Units {
		if !slices.Contains(spec.Enable, unit.Name) {
			if slices.Contains(strings.Fields(enableLine(string(output))), unit.Name) {
				t.Errorf("mwan install output enables the not-owned unit %s:\n%s", unit.Name, output)
			}
		} else {
			unitNames = append(unitNames, unit.Name)
		}
		if !unit.Enabled {
			t.Errorf("unit %s is listed but not enabled", unit.Name)
		}
		for _, path := range unit.Files {
			if _, written := installed[path]; !written {
				t.Errorf("unit %s reads %s, which mwan install did not write", unit.Name, path)
			}
		}
	}
	wantEnable := enableMarker + strings.Join(unitNames, " ")
	if !strings.Contains(string(output), wantEnable) {
		t.Errorf("mwan install output:\n%s\nwant the line %q", output, wantEnable)
	}

	for path, file := range installed {
		if !strings.HasPrefix(path, installspec.SystemdUnitDir+"/") || !strings.HasSuffix(path, ".service") {
			continue
		}
		for line := range strings.SplitSeq(string(file.content), "\n") {
			command, found := strings.CutPrefix(line, "ExecStart=")
			if !found {
				continue
			}
			program := strings.Fields(command)[0]
			if filepath.Base(program) == "mwan" && program != data.BinaryPath {
				t.Errorf("%s starts %s, the provider binary_path is %s", path, program, data.BinaryPath)
			}
		}
	}

	_, policyWritten := installed[installspec.NACMPolicyPath]
	if policyWritten != (len(data.SysrepoData) > 0) {
		t.Errorf("mwan install wrote the policy file = %t, the provider lists %d sysrepo_data entries",
			policyWritten, len(data.SysrepoData))
	}

	wantImport := ""
	if len(data.SysrepoData) > 0 {
		datastores := make([]string, 0, len(data.SysrepoData))
		policy := installed[installspec.NACMPolicyPath]
		for _, entry := range data.SysrepoData {
			datastores = append(datastores, entry.Datastore)
			if !bytes.Equal([]byte(entry.Content), policy.content) {
				t.Errorf("sysrepo_data content for %s differs from the policy mwan install wrote", entry.Datastore)
			}
			if entry.Module != data.SysrepoData[0].Module {
				t.Errorf("sysrepo_data modules differ: %s and %s", entry.Module, data.SysrepoData[0].Module)
			}
		}
		wantImport = "imported the " + data.SysrepoData[0].Module + " policy into " + strings.Join(datastores, " and ")
	}
	if wantImport == "" && strings.Contains(string(output), "imported the") {
		t.Errorf("mwan install imported a policy, and the provider lists no sysrepo_data:\n%s", output)
	}
	if wantImport != "" && !strings.Contains(string(output), wantImport) {
		t.Errorf("mwan install output:\n%s\nwant the line %q", output, wantImport)
	}
}

func enableLine(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, enableMarker) {
			return line
		}
	}
	return ""
}

func parseMode(t *testing.T, text string) fs.FileMode {
	t.Helper()
	mode, err := strconv.ParseUint(text, octalBase, fileModeBits)
	if err != nil {
		t.Fatalf("file mode %q is not octal: %v", text, err)
	}
	return fs.FileMode(mode)
}
