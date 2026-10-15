package logging_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/logging"
)

const (
	// The expected limit must detect removal of the retention cap.
	wantMaxRotatedFiles      = 20
	rotationTestPayloadBytes = 1 << 20
	// The test needs more rotations than the limit allows to exercise deletion.
	rotationTestRecordCount = 120
	rotationTestDeadline    = 30 * time.Second
	rotationTestPollPeriod  = 50 * time.Millisecond
	rotationTestLabel       = "[rotation-test]"
	// Lumberjack removes the uncompressed source only after compression finishes.
	compressedLogSuffix = ".gz"
)

// TestFileRotationKeepsAtMostMaxRotatedFiles polls the directory for up to 30
// seconds before it asserts the count. Lumberjack runs cleanup in the
// background.
func TestFileRotationKeepsAtMostMaxRotatedFiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		newHandler func(path string) slog.Handler
	}{
		{
			name: "FileText",
			newHandler: func(path string) slog.Handler {
				return logging.FileText(path, rotationTestLabel)
			},
		},
		{
			name:       "FileJSON",
			newHandler: logging.FileJSON,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertRotatedFilesCapped(t, tc.newHandler)
		})
	}
}

func assertRotatedFilesCapped(t *testing.T, newHandler func(path string) slog.Handler) {
	t.Helper()
	dir := t.TempDir()
	activePath := filepath.Join(dir, "agent.log")
	log, closer := logging.New(logging.Config{
		Handlers: []slog.Handler{newHandler(activePath)},
	})
	payload := strings.Repeat("x", rotationTestPayloadBytes)
	for range rotationTestRecordCount {
		log.Info("rotation probe", "payload", payload)
	}

	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active log file: %v", err)
	}
	waitForSettledRotation(t, dir)
}

// Close does not stop lumberjack's background compression and deletion.
// TempDir cleanup must wait for both operations to finish.
func waitForSettledRotation(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(rotationTestDeadline)
	var previous []string
	for {
		rotated := rotatedLogFiles(t, dir)
		if rotationSettled(rotated) && slices.Equal(rotated, previous) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rotated files did not settle at most %d compressed files within %s: %d files %v",
				wantMaxRotatedFiles, rotationTestDeadline, len(rotated), rotated)
		}
		previous = rotated
		time.Sleep(rotationTestPollPeriod)
	}
}

// Compression can still create files after the count meets the retention cap.
func rotationSettled(rotated []string) bool {
	if len(rotated) > wantMaxRotatedFiles {
		return false
	}
	for _, name := range rotated {
		if !strings.HasSuffix(name, compressedLogSuffix) {
			return false
		}
	}
	return true
}

// rotatedLogFiles excludes the active agent.log file from the retention
// count. Rotated files use agent-<timestamp>.log or agent-<timestamp>.log.gz.
func rotatedLogFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var rotated []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "agent-") {
			rotated = append(rotated, entry.Name())
		}
	}
	return rotated
}
