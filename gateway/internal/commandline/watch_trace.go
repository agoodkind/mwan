package commandline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
)

const (
	traceRuntimeIDPath           = "/run/mwan-trace-id"
	tracePersistIDPath           = "/var/lib/mwan/trace-id"
	traceTailBacklogLines        = 500
	traceTailPollInterval        = 500 * time.Millisecond
	traceScannerInitialCapacity  = 64 * 1024
	traceScannerMaximumTokenSize = 1024 * 1024
	traceIDTimeLayout            = "20060102-150405"
)

func traceLogPath(cfg *config.Config) string {
	if cfg != nil && cfg.IfMgr.JSONLogFile != "" {
		return cfg.IfMgr.JSONLogFile
	}

	return "/var/log/mwan-ifmgr.jsonl"
}

func showDebugTraceStart(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	args []string,
) error {
	traceID := ""
	if len(args) > 0 {
		traceID = args[0]
	}

	if traceID == "" {
		traceID = (clock.Real{}).Now().Format(traceIDTimeLayout) + "-debug"
	}

	traceID = sanitizeTraceID(traceID)
	if err := writeTraceID(traceID); err != nil {
		return debugWrappedError(logger, "write trace id files", err)
	}

	logger.InfoContext(ctx, "debug trace started", "trace_id", traceID)
	fmt.Fprintf(output, "traceId=%s\n", traceID)
	fmt.Fprintf(output, "Daemon JSON events: %s\n\n", traceLogPath(cfg))

	fmt.Fprintln(output, "== routes ==")
	if err := showDebugRoutes(ctx, output, logger); err != nil {
		return err
	}

	fmt.Fprintln(output, "\n== policy ==")
	if err := showDebugPolicy(ctx, output, logger, cfg); err != nil {
		return err
	}

	fmt.Fprintln(output, "\n== npt ==")
	if err := showDebugNPT(ctx, output, logger); err != nil {
		return err
	}

	fmt.Fprintln(output, "\n== hint ==")
	fmt.Fprintf(output, "mwan debug trace-tail %s\n", traceID)

	return nil
}

func sanitizeTraceID(raw string) string {
	var builder strings.Builder
	builder.Grow(len(raw))

	for _, char := range raw {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.':
			builder.WriteRune(char)
		default:
			builder.WriteRune('-')
		}
	}

	return builder.String()
}

func writeTraceID(traceID string) error {
	traceID = sanitizeTraceID(traceID)
	if err := os.MkdirAll("/var/lib/mwan", 0o755); err != nil {
		slog.Warn("create mwan state dir failed", "err", err)

		return fmt.Errorf("create /var/lib/mwan: %w", err)
	}

	content := []byte(traceID + "\n")
	if err := os.WriteFile(traceRuntimeIDPath, content, 0o600); err != nil {
		slog.Warn("write runtime trace id failed", "path", traceRuntimeIDPath, "err", err)

		return fmt.Errorf("write %s: %w", traceRuntimeIDPath, err)
	}

	if err := os.Chmod(traceRuntimeIDPath, 0o644); err != nil {
		slog.Warn("chmod runtime trace id failed", "err", err)

		return fmt.Errorf("chmod %s: %w", traceRuntimeIDPath, err)
	}

	if err := os.WriteFile(tracePersistIDPath, content, 0o600); err != nil {
		slog.Warn("write persistent trace id failed", "path", tracePersistIDPath, "err", err)

		return fmt.Errorf("write %s: %w", tracePersistIDPath, err)
	}

	if err := os.Chmod(tracePersistIDPath, 0o644); err != nil {
		slog.Warn("chmod persistent trace id failed", "err", err)

		return fmt.Errorf("chmod %s: %w", tracePersistIDPath, err)
	}

	return nil
}

func showDebugTraceTail(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	args []string,
) error {
	filterID := ""
	if len(args) > 0 {
		filterID = args[0]
	}

	path := traceLogPath(cfg)
	file, err := os.Open(path)
	if err != nil {
		return debugWrappedError(logger, "open daemon json log", err)
	}

	defer func() { _ = file.Close() }()

	if err := renderTraceBacklog(output, file, filterID); err != nil {
		return err
	}

	followTraceLog(ctx, output, file, filterID)

	return nil
}

func renderTraceBacklog(output io.Writer, file *os.File, filterID string) error {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, traceScannerInitialCapacity), traceScannerMaximumTokenSize)
	backlog := make([]string, 0, traceTailBacklogLines)

	for scanner.Scan() {
		rendered, ok := renderTraceLine(scanner.Text(), filterID)
		if !ok {
			continue
		}

		backlog = append(backlog, rendered)
		if len(backlog) > traceTailBacklogLines {
			backlog = backlog[1:]
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("scan daemon json log failed", "err", err)

		return fmt.Errorf("scan daemon json log: %w", err)
	}

	for _, line := range backlog {
		fmt.Fprintln(output, line)
	}

	return nil
}

func followTraceLog(
	ctx context.Context,
	output io.Writer,
	file *os.File,
	filterID string,
) {
	reader := bufio.NewReader(file)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if !waitForTraceTailPoll(ctx) {
				return
			}

			continue
		}

		if rendered, ok := renderTraceLine(line, filterID); ok {
			fmt.Fprintln(output, rendered)
		}
	}
}

func waitForTraceTailPoll(ctx context.Context) bool {
	timer := time.NewTimer(traceTailPollInterval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var traceCoreFields = []string{"time", "trace_id", "component", "module", "msg"}

func renderTraceLine(raw string, filterID string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "", false
	}

	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		return "", false
	}

	traceID := traceStringField(record, "trace_id")
	if filterID != "" && traceID != filterID {
		return "", false
	}

	parts := []string{
		orDash(traceStringField(record, "time")),
		orDash(traceID),
		orDash(traceStringField(record, "component")),
		orDash(traceStringField(record, "module")),
		orDash(traceStringField(record, "msg")),
	}
	rest := make([]string, 0, len(record))

	core := make(map[string]bool, len(traceCoreFields))
	for _, field := range traceCoreFields {
		core[field] = true
	}

	for key, value := range record {
		if core[key] || key == "level" || key == "build" {
			continue
		}

		rest = append(rest, key+"="+strings.TrimSpace(string(value)))
	}

	sort.Strings(rest)
	line := strings.Join(parts, " ")
	if len(rest) > 0 {
		line += " " + strings.Join(rest, " ")
	}

	return line, true
}

func traceStringField(record map[string]json.RawMessage, key string) string {
	raw, ok := record[key]
	if !ok {
		return ""
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return strings.TrimSpace(string(raw))
	}

	return value
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}

func showDebugTraceStop(output io.Writer) error {
	fmt.Fprintln(output, "No background collectors to stop.")

	return nil
}
