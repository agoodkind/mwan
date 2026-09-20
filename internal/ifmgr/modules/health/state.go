package health

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// initStatuses keeps every restart probing each link from scratch, so a verdict
// rests on this run's probes alone. It sets every configured WAN to the unknown
// warmup state and restores no verdict a previous run recorded.
func (m *Module) initStatuses() {
	statuses := make(map[string]wanStatus, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		statuses[wan.Name] = wanStatus{
			State:     StateUnknown,
			OKCount:   0,
			FailCount: 0,
		}
	}
	m.Lock()
	m.statuses = statuses
	m.Unlock()
}

// Valid reports whether a state is one the hysteresis machine recognizes.
func (s State) Valid() bool {
	return s == StateUnknown || s == StateHealthy || s == StateUnhealthy
}

func (m *Module) writeStateFile(
	ctx context.Context,
	log *slog.Logger,
	statuses map[string]wanStatus,
) error {
	contents := m.serializeState(statuses)
	statePath := m.cfg.StateFile
	if err := writeFileAtomic(ctx, log, statePath, contents); err != nil {
		return stateFileError(ctx, log, "write runtime state", statePath, err)
	}
	log.DebugContext(
		ctx,
		"health: state file written",
		"state_file", statePath,
		"bytes", len(contents),
	)
	return nil
}

func (m *Module) serializeState(statuses map[string]wanStatus) []byte {
	var buffer bytes.Buffer
	for _, wan := range m.cfg.WANs {
		state := StateUnknown
		if status, ok := statuses[wan.Name]; ok && status.State.Valid() {
			state = status.State
		}
		_, _ = fmt.Fprintf(&buffer, "%s:%s\n", wan.Name, state)
	}
	return buffer.Bytes()
}

func writeFileAtomic(
	ctx context.Context,
	log *slog.Logger,
	path string,
	contents []byte,
) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return stateFileError(ctx, log, "create parent directory", parent, err)
	}
	tempFile, err := os.CreateTemp(parent, ".mwan-health-*")
	if err != nil {
		return stateFileError(ctx, log, "create temporary file", parent, err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err := tempFile.Chmod(0o644); err != nil {
		_ = tempFile.Close()
		return stateFileError(ctx, log, "chmod temporary file", tempPath, err)
	}
	if _, err := tempFile.Write(contents); err != nil {
		_ = tempFile.Close()
		return stateFileError(ctx, log, "write temporary file", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		return stateFileError(ctx, log, "close temporary file", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return stateFileError(ctx, log, "replace destination", path, err)
	}
	log.DebugContext(ctx, "health: state file replaced", "path", path, "bytes", len(contents))
	return nil
}

func stateFileError(
	ctx context.Context,
	log *slog.Logger,
	operation string,
	path string,
	err error,
) error {
	log.WarnContext(
		ctx,
		"health: state file operation failed",
		"operation", operation,
		"path", path,
		"err", err,
	)
	return fmt.Errorf("%s %q: %w", operation, path, err)
}
