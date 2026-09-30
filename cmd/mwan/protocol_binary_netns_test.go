//go:build linux && firewallnetns

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func protocolTestBinary(t *testing.T) string {
	t.Helper()
	binary, supplied := os.LookupEnv("MWAN_PROTOCOL_TEST_BINARY")
	if supplied {
		if !filepath.IsAbs(binary) {
			t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be an absolute path: %q", binary)
		}
		stat, err := os.Stat(binary)
		if err != nil {
			t.Fatalf("inspect MWAN_PROTOCOL_TEST_BINARY: %v", err)
		}
		if !stat.Mode().IsRegular() || stat.Mode().Perm()&0o111 == 0 {
			t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be a regular executable file: %q", binary)
		}
	}
	if os.Geteuid() != 0 {
		if supplied {
			t.Fatal("explicit daemon validation requires root for network and mount namespaces")
		}
		t.Skip("network and mount namespaces require root")
	}
	if supplied {
		t.Logf("protocol daemon executable: %s", binary)
		return binary
	}
	binary = filepath.Join(t.TempDir(), "mwan")
	buildStarted := time.Now()
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v: %s", err, output)
	}
	stat, err := os.Stat(binary)
	if err != nil || stat.ModTime().Before(buildStarted) {
		t.Fatalf("mwan binary was not freshly built: stat=%v err=%v", stat, err)
	}
	t.Logf("protocol daemon executable: %s", binary)
	return binary
}
