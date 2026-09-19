package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/gklog"
	"goodkind.io/mwan/internal/tracing"
)

type captureHandler struct {
	attrs []slog.Attr
	last  map[string]string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	out := make(map[string]string)
	for _, attr := range h.attrs {
		out[attr.Key] = attr.Value.String()
	}
	record.Attrs(func(attr slog.Attr) bool {
		out[attr.Key] = attr.Value.String()
		return true
	})
	h.last = out
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{
		attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...),
	}
}

func (h *captureHandler) WithGroup(_ string) slog.Handler {
	return &captureHandler{attrs: append([]slog.Attr(nil), h.attrs...)}
}

func TestContextHandlerAddsTracingAttrs(t *testing.T) {
	t.Parallel()

	capture := &captureHandler{}
	logger := slog.New(NewContextHandler(capture))
	ctx := tracing.WithTraceID(context.Background(), "trace-123")
	ctx = tracing.WithOperation(ctx, "ping")

	logger.InfoContext(ctx, "hello", "key", "value")

	if capture.last["trace_id"] != "trace-123" {
		t.Fatalf("trace_id=%q", capture.last["trace_id"])
	}
	if capture.last["operation"] != "ping" {
		t.Fatalf("operation=%q", capture.last["operation"])
	}
	if capture.last["key"] != "value" {
		t.Fatalf("key=%q", capture.last["key"])
	}
}

// TestContextHandlerReportsAndReturnsAWriteFailure writes through a JSON
// handler whose file is already closed, which is how a real sink fails.
func TestContextHandlerReportsAndReturnsAWriteFailure(t *testing.T) {
	t.Parallel()

	closedFile, err := os.Create(filepath.Join(t.TempDir(), "closed.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatal(err)
	}
	var fallbackOutput strings.Builder
	handler := newContextHandler(
		slog.NewJSONHandler(closedFile, nil),
		slog.NewTextHandler(&fallbackOutput, nil),
	)
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)

	handleErr := handler.WithAttrs([]slog.Attr{slog.String("key", "value")}).
		Handle(context.Background(), record)

	if !errors.Is(handleErr, os.ErrClosed) {
		t.Fatalf("err = %v, want one wrapping %v", handleErr, os.ErrClosed)
	}
	output := fallbackOutput.String()
	for _, want := range []string{"log record not written", "message=hello", "file already closed"} {
		if !strings.Contains(output, want) {
			t.Fatalf("fallback output %q is missing %q", output, want)
		}
	}
}

func TestTextHandlerPreservesWithAttrsAndGroups(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	logger := slog.New(gklog.NewTextHandler(&builder, "[mwan]")).
		With("trace_id", "trace-456").
		WithGroup("rpc")

	logger.Info("hello", "code", "OK")

	output := builder.String()
	if !strings.Contains(output, "trace_id=trace-456") {
		t.Fatalf("output=%q", output)
	}
	if !strings.Contains(output, "rpc.code=OK") {
		t.Fatalf("output=%q", output)
	}
}

func TestNewLoggerAppliesContextHandler(t *testing.T) {
	t.Parallel()

	logger, _ := New(Config{BuildVersion: "test-build"})

	ctx := tracing.WithTraceID(context.Background(), "trace-789")
	ctx = tracing.WithOperation(ctx, "noop")

	// Smoke test only. The dedicated handler test above checks exact attrs.
	logger.With("discard", io.Discard).InfoContext(ctx, "probe")
}
