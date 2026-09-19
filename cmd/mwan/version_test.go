package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// versionTestCommit is the commit stamped into the binary under test, a full
// SHA the way the release stamps one.
const versionTestCommit = "0123456789abcdef0123456789abcdef01234567"

// TestVersionReportsTheStampedBuild builds the real binary with a stamped
// commit, the way the release stamps it, and runs `mwan version` with the
// config path pointed at a file that does not exist. The deploy's release check
// parses exactly this line, so the test asserts the whole line.
func TestVersionReportsTheStampedBuild(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "mwan")
	ldflags := "-X goodkind.io/gklog/version.Commit=" + versionTestCommit +
		" -X goodkind.io/gklog/version.Dirty=false"
	build := exec.CommandContext(t.Context(), "go", "build", "-ldflags", ldflags, "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}

	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read built binary: %v", err)
	}
	digest := sha256.Sum256(binary)
	binhash := hex.EncodeToString(digest[:])[:12]

	run := exec.CommandContext(t.Context(), binaryPath, "version")
	run.Env = append(os.Environ(), "MWAN_CONFIG="+filepath.Join(t.TempDir(), "absent.toml"))
	var stderr bytes.Buffer
	run.Stderr = &stderr
	stdout, err := run.Output()
	if err != nil {
		t.Fatalf("mwan version: %v\nstderr: %s", err, stderr.String())
	}

	want := fmt.Sprintf("version=%s+%s commit=%s dirty=clean binhash=%s libsysrepo=%s",
		versionTestCommit, binhash, versionTestCommit, binhash, linkedSysrepoVersion())
	if got := strings.TrimSuffix(string(stdout), "\n"); got != want {
		t.Fatalf("mwan version printed\n%q\nwant\n%q", got, want)
	}
}
