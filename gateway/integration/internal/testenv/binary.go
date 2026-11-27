//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const BinaryBuildTimeout = 5 * time.Minute

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

		ctx, cancel := context.WithTimeout(context.Background(), BinaryBuildTimeout)
		defer cancel()
		path := filepath.Join(testBinary.dir, "mwan")
		build := exec.CommandContext(ctx, "go", "build", "-o", path, "goodkind.io/mwan/gateway/cmd/mwan")
		if output, err := build.CombinedOutput(); err != nil {
			testBinary.err = fmt.Errorf("go build: %w\n%s", err, output)
		}
	})
	if testBinary.err != nil {
		t.Fatal(testBinary.err)
	}

	return filepath.Join(testBinary.dir, "mwan")
}

func RemoveTestBinary() error {
	if testBinary.dir == "" {
		return nil
	}

	return os.RemoveAll(testBinary.dir)
}
