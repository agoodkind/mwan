package commandline_test

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

	"goodkind.io/mwan/gateway/internal/commandline"
)

func testMwanBinary(t *testing.T) string {
	t.Helper()

	return commandline.BuildTestBinary(t)
}

func runCLIProcess(t *testing.T, binary string, arguments ...string) (string, string, int) {
	t.Helper()

	return runCLIProcessInput(t, binary, "", arguments...)
}

func runCLIProcessInput(
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
