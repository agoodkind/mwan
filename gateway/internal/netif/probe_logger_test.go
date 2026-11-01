package netif_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/netif"
)

func TestHTTPGetWithLoggerPreservesResponseAndDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte("  original body\n"))
	}))
	defer server.Close()
	var logs bytes.Buffer
	logger, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewTextHandler(&logs, nil)}})
	defer closer.Close()
	result, err := netif.HTTPGetWithLogger(context.Background(), logger, "", "inet", server.URL, time.Second)
	if err != nil || result.StatusCode != http.StatusServiceUnavailable || result.Body != "original body" {
		t.Fatalf("HTTP result=%+v error=%v", result, err)
	}
	_, err = netif.HTTPGetWithLogger(context.Background(), logger, "", "invalid", server.URL, time.Second)
	if err == nil || !strings.Contains(err.Error(), "unsupported address family") || !strings.Contains(logs.String(), "unsupported address family") {
		t.Fatalf("HTTP error=%v logs=%q", err, logs.String())
	}
}

func TestPing4WithLoggerUsesProvidedLogger(t *testing.T) {
	var logs bytes.Buffer
	logger, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewTextHandler(&logs, nil)}})
	defer closer.Close()
	_, err := netif.Ping4WithLogger(context.Background(), logger, "", netip.IPv6Loopback(), time.Second)
	if err == nil || !strings.Contains(logs.String(), "target is not IPv4") {
		t.Fatalf("ping error=%v logs=%q", err, logs.String())
	}
	quiet, quietCloser := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewTextHandler(io.Discard, nil)}})
	defer quietCloser.Close()
	_, quietErr := netif.Ping4WithLogger(context.Background(), quiet, "", netip.IPv6Loopback(), time.Second)
	if quietErr == nil || quietErr.Error() != err.Error() {
		t.Fatalf("quiet ping error=%v, want %v", quietErr, err)
	}
}
