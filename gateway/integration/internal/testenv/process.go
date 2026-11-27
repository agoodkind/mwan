//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func RunCLIProcess(t *testing.T, binary string, arguments ...string) (string, string, int) {
	t.Helper()

	return RunCLIProcessInput(t, binary, "", arguments...)
}

func RunCLIProcessInput(
	t *testing.T,
	binary string,
	input string,
	arguments ...string,
) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+filepath.Join(t.TempDir(), "absent.toml"))
	command.Stdin = strings.NewReader(input)
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	err := command.Run()
	if err == nil {
		return output.String(), diagnostics.String(), 0
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatal(err)
	}

	return output.String(), diagnostics.String(), exitError.ExitCode()
}
