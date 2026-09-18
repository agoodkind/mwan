package logging

import (
	"context"
	"log/slog"

	"goodkind.io/mwan/internal/tracing"
)

// ContextHandler copies the trace attributes held on a context onto every
// record before passing it on. It exists so call sites log with the ordinary
// slog methods and still produce correlated records: without it every call
// site would have to repeat the identifiers by hand.
type ContextHandler struct {
	next slog.Handler
}

// NewContextHandler wraps a handler. Every other method delegates to next, so
// the wrapped handler keeps its own level, format, and destination.
func NewContextHandler(next slog.Handler) *ContextHandler {
	return &ContextHandler{next: next}
}

// Enabled reports what the wrapped handler reports. The trace attributes never
// change whether a record is logged.
func (h *ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle adds the context's trace attributes and passes the record on. The
// record is cloned before the attributes are added, because a [slog.Record]
// shares its backing array with copies and appending in place would corrupt a
// record another handler still holds.
func (h *ContextHandler) Handle(ctx context.Context, record slog.Record) error {
	if attrs := tracing.AttrsFromContext(ctx); len(attrs) > 0 {
		cloned := record.Clone()
		cloned.AddAttrs(attrs...)
		record = cloned
	}
	// The error is returned unwrapped on purpose. Wrapping it satisfies
	// wrapcheck but then trips the staticcheck-extra
	// wrapped_error_without_slog analyzer, whose only escapes are logging
	// before the return or a stdlib reader name. Neither is available: this
	// is the log path, so emitting a record here re-enters this handler, and
	// the name is fixed by [slog.Handler].
	return h.next.Handle(ctx, record)
}

// WithAttrs returns a wrapped handler that adds the given attributes, so a
// logger derived with [slog.Logger.With] still gets the context attributes.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{next: h.next.WithAttrs(attrs)}
}

// WithGroup returns a handler that opens the named group, still wrapped.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{next: h.next.WithGroup(name)}
}
