package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestNewWritesToFile checks the project's adapter wires the gklog
// FileJSON constructor through to disk and that the closer flushes the
// underlying lumberjack writer.
func TestNewWritesToFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "agent.log")
	log, closer := New(Config{
		BuildVersion: "test-version",
		Handlers:     []slog.Handler{FileJSON(p)},
	})
	if log == nil {
		t.Fatal("nil logger")
	}
	log.Info("probe", "k", "v")
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("log file: %v", err)
	}
}

// TestNewEmptyHandlers exercises the no-handlers degenerate case the
// adapter inherits from gklog.
func TestNewEmptyHandlers(t *testing.T) {
	t.Parallel()
	log, closer := New(Config{BuildVersion: "test"})
	if log == nil {
		t.Fatal("nil logger")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
