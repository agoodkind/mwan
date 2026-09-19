package logging

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/tracing"
)

// ContextHandler copies the trace attributes held on a context onto every
// record before passing it on. It exists so call sites log with the ordinary
// slog methods and still produce correlated records: without it every call
// site would have to repeat the identifiers by hand.
type ContextHandler struct {
	next slog.Handler
	// fallbackLog reports a record the wrapped handler failed to write. It
	// writes through its own handler rather than this one, so reporting a
	// failure never re-enters the handler that just failed.
	fallbackLog *slog.Logger
}

// NewContextHandler wraps a handler. Every other method delegates to next, so
// the wrapped handler keeps its own level, format, and destination. A record
// next fails to write is reported as JSON on stdout.
func NewContextHandler(next slog.Handler) *ContextHandler {
	return newContextHandler(next, StdoutJSON())
}

// newContextHandler wraps next and reports its write failures through
// fallback, which must not lead back to next.
func newContextHandler(next, fallback slog.Handler) *ContextHandler {
	return &ContextHandler{next: next, fallbackLog: slog.New(fallback)}
}

// Enabled reports what the wrapped handler reports. The trace attributes never
// change whether a record is logged.
func (h *ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle adds the context's trace attributes and passes the record on. The
// record is cloned before the attributes are added, because a [slog.Record]
// shares its backing array with copies and appending in place would corrupt a
// record another handler still holds. When the wrapped handler fails, the
// failure is reported through the fallback logger and returned wrapped, so a
// caller can still match the wrapped handler's error.
func (h *ContextHandler) Handle(ctx context.Context, record slog.Record) error {
	if attrs := tracing.AttrsFromContext(ctx); len(attrs) > 0 {
		cloned := record.Clone()
		cloned.AddAttrs(attrs...)
		record = cloned
	}
	if err := h.next.Handle(ctx, record); err != nil {
		h.fallbackLog.WarnContext(ctx, "log record not written",
			"message", record.Message, "level", record.Level.String(), "err", err)
		return fmt.Errorf("write log record: %w", err)
	}
	return nil
}

// WithAttrs returns a wrapped handler that adds the given attributes, so a
// logger derived with [slog.Logger.With] still gets the context attributes.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{next: h.next.WithAttrs(attrs), fallbackLog: h.fallbackLog}
}

// WithGroup returns a handler that opens the named group, still wrapped.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{next: h.next.WithGroup(name), fallbackLog: h.fallbackLog}
}
