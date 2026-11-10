//go:build !nativeoffload

package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeOffloadUnsupportedBuild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binary := filepath.Join(root, "mwan")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ordinary mwan: %v: %s", err, output)
	}
	path := filepath.Join(root, "config.toml")
	const settings = `[ifmgr]
role = "wan"
[ifmgr.modules.nativeoffload]
enabled = true
cpu = 0
memory_mib = 256
hugepage_directory = "/dev/hugepages"
file_prefix = "mwan-native-test"
`
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "ifmgr", "--config", path)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "nativeoffload requires a Linux cgo build") {
		t.Fatalf("ordinary command did not reject enabled native mode before startup: %v: %s", err, output)
	}
}
