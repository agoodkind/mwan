package commandline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const testBinaryBuildTimeout = 5 * time.Minute

var testBinary struct {
	build sync.Once
	dir   string
	err   error
}

func BuildTestBinary(t *testing.T) string {
	t.Helper()
	testBinary.build.Do(func() {
		testBinary.dir, testBinary.err = os.MkdirTemp("", "mwan-test-binary-")
		if testBinary.err != nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), testBinaryBuildTimeout)
		defer cancel()
		path := filepath.Join(testBinary.dir, "mwan")
		build := exec.CommandContext(ctx, "go", "build", "-o", path, "../../cmd/mwan")
		if output, err := build.CombinedOutput(); err != nil {
			testBinary.err = fmt.Errorf("go build: %w\n%s", err, output)
		}
	})
	if testBinary.err != nil {
		t.Fatal(testBinary.err)
	}

	return filepath.Join(testBinary.dir, "mwan")
}

func removeTestBinary() error {
	if testBinary.dir == "" {
		return nil
	}

	return os.RemoveAll(testBinary.dir)
}

func runMwanBinary(
	t *testing.T,
	binaryPath string,
	toolDir string,
	configPath string,
	args ...string,
) (string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binaryPath, args...)
	command.Env = append(
		os.Environ(),
		"PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MWAN_CONFIG="+configPath,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run %v: %v", args, err)
	}

	return string(output), exitError.ExitCode()
}
