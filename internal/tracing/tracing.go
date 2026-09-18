package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"maps"
)

const (
	TraceIDKey      = "trace_id"
	SpanIDKey       = "span_id"
	ParentSpanIDKey = "parent_span_id"
	RunIDKey        = "run_id"
	ComponentKey    = "component"
	OperationKey    = "operation"
	EventKey        = "event"
	AttemptKey      = "attempt"
)

type contextKey struct{}

type traceContext struct {
	attrs map[string]slog.Attr
}

type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
}

func NewID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

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

func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(TraceIDKey, traceID))
}

func WithRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(RunIDKey, runID))
}

func WithComponent(ctx context.Context, component string) context.Context {
	if component == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(ComponentKey, component))
}

func WithOperation(ctx context.Context, operation string) context.Context {
	if operation == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(OperationKey, operation))
}

func WithEvent(ctx context.Context, event string) context.Context {
	if event == "" {
		return ctx
	}
	return WithAttrs(ctx, slog.String(EventKey, event))
}

func WithAttempt(ctx context.Context, attempt int) context.Context {
	return WithAttrs(ctx, slog.Int(AttemptKey, attempt))
}

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

func TraceID(ctx context.Context) string {
	return stringValue(ctx, TraceIDKey)
}

func SpanID(ctx context.Context) string {
	return stringValue(ctx, SpanIDKey)
}

func RunID(ctx context.Context) string {
	return stringValue(ctx, RunIDKey)
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
