// Package logging is the mwan-specific glue around goodkind.io/gklog.
// gklog provides the slog primitives (TeeHandler, TextHandler,
// rotating file writer, locked writer, email handler) and the
// handler-list factory. This package adds:
//
//   - re-exports of the bits mwan call sites use most so existing
//     imports of "goodkind.io/mwan/internal/logging" keep working.
//   - ContextHandler, the project-specific [slog.Handler] that pulls
//     trace attrs out of context (depends on internal/tracing).
//
// Email delivery is owned by goodkind.io/mwan/internal/notify; the
// slog email handler that used to live here is intentionally absent so
// alerts flow through one path with state-change semantics.
//
// New project code can also import goodkind.io/gklog directly; this
// package is intentionally a thin adapter, not a wrapper hierarchy.
package logging

import (
	"io"
	"log/slog"

	"goodkind.io/gklog"
)

// Config is the gklog Config re-exported so mwan call sites read
// naturally without dropping the gklog import on every line.
type Config = gklog.Config

// New constructs a logger via gklog and wraps the result in mwan's
// ContextHandler so trace attrs from the request/operation context
// flow into every record. Returns the logger plus a Closer that
// releases any file-backed handlers; daemons should
// `defer closer.Close()` at the end of their lifetime to flush rotation
// state cleanly. Long-running daemons that exit only on SIGTERM can
// safely discard the closer (the OS reclaims open file handles on
// exit) but doing so leaks any in-flight rotation buffers.
func New(cfg Config) (*slog.Logger, io.Closer) {
	logger, closer := gklog.New(cfg)
	wrapped := slog.New(NewContextHandler(logger.Handler()))
	if cfg.BuildVersion != "" {
		wrapped = wrapped.With("build", cfg.BuildVersion)
	}
	return wrapped, closer
}

// StdoutJSON returns a JSON-to-stdout handler at LevelDebug. Re-exports
// gklog.StdoutJSON for short imports.
func StdoutJSON() slog.Handler { return gklog.StdoutJSON(slog.LevelDebug) }

// FileText returns a rotating, multi-process-locked text handler at
// path with the given label. Re-exports gklog.FileText with default
// rotation (5MB, keep forever, compressed).
func FileText(path, label string) slog.Handler {
	return gklog.FileText(path, label, gklog.RotationConfig{})
}

// FileJSON returns a rotating, multi-process-locked JSON handler at
// path. Re-exports gklog.FileJSON at LevelDebug with default rotation.
func FileJSON(path string) slog.Handler {
	return gklog.FileJSON(path, slog.LevelDebug, gklog.RotationConfig{})
}
