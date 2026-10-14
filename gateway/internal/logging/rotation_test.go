package logging_test

import (
	"log/slog"
	"os"
	"path/filepath"
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
)

// TestFileJSONKeepsAtMostMaxRotatedFiles polls the directory for up to 30
// seconds before it asserts the count. Lumberjack runs cleanup in the
// background.
func TestFileJSONKeepsAtMostMaxRotatedFiles(t *testing.T) {
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

	deadline := time.Now().Add(rotationTestDeadline)
	rotated := rotatedLogFiles(t, dir)
	for len(rotated) > wantMaxRotatedFiles && time.Now().Before(deadline) {
		time.Sleep(rotationTestPollPeriod)
		rotated = rotatedLogFiles(t, dir)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(rotated) > wantMaxRotatedFiles {
		t.Fatalf("rotated files = %d, want at most %d: %v",
			len(rotated), wantMaxRotatedFiles, rotated)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active log file: %v", err)
	}
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
