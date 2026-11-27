//go:build cli

package commandline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const versionTestCommit = "0123456789abcdef0123456789abcdef01234567"

func TestVersionReportsTheStampedBuild(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "mwan")
	ldflags := "-X goodkind.io/gklog/version.Commit=" + versionTestCommit +
		" -X goodkind.io/gklog/version.Dirty=false"
	ctx, cancel := context.WithTimeout(t.Context(), testBinaryBuildTimeout)
	defer cancel()
	build := exec.CommandContext(
		ctx,
		"go",
		"build",
		"-ldflags",
		ldflags,
		"-o",
		binaryPath,
		"../../cmd/mwan",
	)
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

	want := fmt.Sprintf(
		"version=%s+%s commit=%s dirty=clean binhash=%s libsysrepo=%s",
		versionTestCommit,
		binhash,
		versionTestCommit,
		binhash,
		linkedSysrepoVersion(),
	)
	if got := strings.TrimSuffix(string(stdout), "\n"); got != want {
		t.Fatalf("mwan version printed\n%q\nwant\n%q", got, want)
	}
}
