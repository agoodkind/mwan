// Package tracing carries log attributes on a context so that every record a
// call path emits shares one set of identifiers. The daemon has no tracing
// backend; the attributes reach the operator only through the log, so they are
// [slog.Attr] values from the moment they are set.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"maps"
)

const (
	// TraceIDKey names the identifier shared by every record of one traced
	// call path. StartTrace reuses the one already in the context, so a
	// nested span keeps the outermost caller's value.
	TraceIDKey = "trace_id"
	// SpanIDKey names the identifier of a single StartTrace call. Each call
	// mints a fresh one.
	SpanIDKey = "span_id"
	// ParentSpanIDKey names the span that was current when this one started.
	// It is absent on the outermost span.
	ParentSpanIDKey = "parent_span_id"
	// RunIDKey names the identifier of one daemon run or one operator
	// command, set once at the top and inherited by every span below it.
	RunIDKey = "run_id"
	// ComponentKey names the subsystem that owns the span.
	ComponentKey = "component"
	// OperationKey names the work the span performs.
	OperationKey = "operation"
	// EventKey names a point of interest inside an operation, for call paths
	// that mark progress without starting a span.
	EventKey = "event"
	// AttemptKey counts retries of one operation.
	AttemptKey = "attempt"
)

type contextKey struct{}

type traceContext struct {
	attrs map[string]slog.Attr
}

// Span reports the identifiers StartTrace assigned, for a caller that must
// record or forward them rather than read them back out of the context.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
}

// NewID returns a random 64-bit identifier in hex. A failed read from the
// random source is ignored, which yields a zero identifier rather than
// failing the operation the span describes.
func NewID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// WithAttrs returns a context carrying the given attributes in addition to
// those already set. Attributes are keyed, so a repeated key replaces the
// earlier value; an attribute with an empty key is dropped.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}

	current := fromContext(ctx)
	merged := make(map[string]slog.Attr, len(current.attrs)+len(attrs))
	maps.Copy(merged, current.attrs)
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		merged[attr.Key] = attr
	}
	return context.WithValue(ctx, contextKey{}, traceContext{attrs: merged})
}

// WithTraceID adopts a trace identifier that was minted elsewhere, so a span
// started from an incoming request joins the caller's trace. An empty value
// leaves the context unchanged.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(TraceIDKey, traceID))
}

// WithRunID stamps the run identifier once at the top of a run, so every span
// below it reports which run it belongs to. An empty value leaves the context
// unchanged.
func WithRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(RunIDKey, runID))
}

// WithOperation names the work in progress for call paths that annotate an
// existing span rather than start one. An empty value leaves the context
// unchanged.
func WithOperation(ctx context.Context, operation string) context.Context {
	if operation == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(OperationKey, operation))
}

// WithEvent marks a point of interest inside an operation. An empty value
// leaves the context unchanged.
func WithEvent(ctx context.Context, event string) context.Context {
	if event == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(EventKey, event))
}

// WithAttempt records which retry of an operation is running. Unlike the
// string helpers it has no empty case, so attempt zero is recorded.
func WithAttempt(ctx context.Context, attempt int) context.Context {
	return WithAttrs(ctx, slog.Int(AttemptKey, attempt))
}

// StartTrace begins a span and returns both the context to pass down and the
// identifiers it assigned. The trace identifier is inherited when the context
// already has one, so only the outermost call mints a trace; the span already
// in the context becomes this span's parent. Empty component or operation
// names are omitted rather than logged blank.
func StartTrace(ctx context.Context, component string, operation string) (
	context.Context, Span,
) {
	traceID := TraceID(ctx)
	if traceID == "" {
		traceID = NewID()
	}

	parentSpanID := SpanID(ctx)
	spanID := NewID()
	attrs := []slog.Attr{
		slog.String(TraceIDKey, traceID),
		slog.String(SpanIDKey, spanID),
	}
	if parentSpanID != "" {
		attrs = append(attrs, slog.String(ParentSpanIDKey, parentSpanID))
	}
	if component != "" {
		attrs = append(attrs, slog.String(ComponentKey, component))
	}
	if operation != "" {
		attrs = append(attrs, slog.String(OperationKey, operation))
	}

	ctx = WithAttrs(ctx, attrs...)
	return ctx, Span{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
	}
}

// AttrsFromContext returns the context's attributes with the well-known keys
// first, in a fixed order, followed by any others. The fixed order keeps the
// identifying fields in the same place on every line, which matters because
// the log is read directly rather than through a query tool.
func AttrsFromContext(ctx context.Context) []slog.Attr {
	current := fromContext(ctx)
	if len(current.attrs) == 0 {
		return nil
	}

	keys := []string{
		RunIDKey,
		TraceIDKey,
		SpanIDKey,
		ParentSpanIDKey,
		ComponentKey,
		OperationKey,
		EventKey,
		AttemptKey,
	}
	out := make([]slog.Attr, 0, len(current.attrs))
	seen := make(map[string]struct{}, len(current.attrs))
	for _, key := range keys {
		attr, ok := current.attrs[key]
		if !ok {
			continue
		}
		out = append(out, attr)
		seen[key] = struct{}{}
	}
	for key, attr := range current.attrs {
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, attr)
	}
	return out
}

// Logger returns a logger that stamps the context's attributes onto every
// record. A nil base returns nil, and a context with no attributes returns the
// base unchanged, so callers can use it unconditionally.
func Logger(ctx context.Context, base *slog.Logger) *slog.Logger {
	if base == nil {
		return nil
	}

	attrs := AttrsFromContext(ctx)
	if len(attrs) == 0 {
		return base
	}

	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	return base.With(args...)
}

// TraceID returns the context's trace identifier, or the empty string when no
// span has started.
func TraceID(ctx context.Context) string {
	return stringValue(ctx, TraceIDKey)
}

// SpanID returns the innermost span's identifier, or the empty string when no
// span has started. StartTrace reads it to find the parent of the span it is
// about to create.
func SpanID(ctx context.Context) string {
	return stringValue(ctx, SpanIDKey)
}

func fromContext(ctx context.Context) traceContext {
	if ctx == nil {
		return traceContext{attrs: map[string]slog.Attr{}}
	}

	current, ok := ctx.Value(contextKey{}).(traceContext)
	if !ok {
		return traceContext{attrs: map[string]slog.Attr{}}
	}
	if current.attrs == nil {
		current.attrs = map[string]slog.Attr{}
	}
	return current
}

func stringValue(ctx context.Context, key string) string {
	current := fromContext(ctx)
	attr, ok := current.attrs[key]
	if !ok {
		return ""
	}
	return attr.Value.String()
}
