package health

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func (m *Module) loadStatuses(ctx context.Context, log *slog.Logger) error {
	statuses := make(map[string]wanStatus, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		statuses[wan.Name] = wanStatus{
			State:     StateUnknown,
			OKCount:   0,
			FailCount: 0,
		}
	}

	_, persistPath := m.stateFilePaths()
	file, err := os.Open(persistPath)
	if errors.Is(err, os.ErrNotExist) {
		m.Lock()
		m.statuses = statuses
		m.Unlock()
		log.DebugContext(
			ctx,
			"health: persistent state missing; starting unknown",
			"path", persistPath,
		)
		return nil
	}
	if err != nil {
		return stateFileError(ctx, log, "open persistent state", persistPath, err)
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, rawState, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		if _, configured := statuses[name]; !configured {
			continue
		}
		state := State(rawState)
		if !state.Valid() {
			continue
		}
		statuses[name] = wanStatus{
			State:     state,
			OKCount:   0,
			FailCount: 0,
		}
	}
	if err := scanner.Err(); err != nil {
		return stateFileError(ctx, log, "scan persistent state", persistPath, err)
	}

	m.Lock()
	m.statuses = statuses
	m.Unlock()
	log.DebugContext(
		ctx,
		"health: persistent state loaded",
		"path", persistPath,
		"wan_count", len(statuses),
	)
	return nil
}

// Valid rejects malformed persisted values so a damaged mirror cannot bypass
// the unknown warmup state on daemon restart.
func (s State) Valid() bool {
	return s == StateUnknown || s == StateHealthy || s == StateUnhealthy
}

func (m *Module) writeStateFiles(
	ctx context.Context,
	log *slog.Logger,
	statuses map[string]wanStatus,
) error {
	contents := m.serializeState(statuses)
	statePath, persistPath := m.stateFilePaths()
	// The runtime file is the load-bearing output, so a failure to write it is an
	// error the caller must see. Write it before the persist mirror so a runtime
	// failure leaves persist at the last committed state, matching runCycle's
	// in-memory rollback.
	if err := writeFileAtomic(ctx, log, statePath, contents); err != nil {
		return stateFileError(ctx, log, "write runtime state", statePath, err)
	}
	// The persist mirror is best-effort restart-recovery state. Log and continue
	// rather than failing the cycle or killing the module: losing the mirror
	// only costs restart recovery, and the module re-converges within a couple
	// of cycles.
	if err := writeFileAtomic(ctx, log, persistPath, contents); err != nil {
		log.WarnContext(
			ctx,
			"health: persist mirror write failed (best-effort); continuing",
			"path", persistPath,
			"err", err,
		)
	}
	log.DebugContext(
		ctx,
		"health: state files written",
		"state_file", statePath,
		"persist_state_file", persistPath,
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

func (m *Module) stateFilePaths() (string, string) {
	return m.cfg.StateFile, m.cfg.PersistStateFile
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
