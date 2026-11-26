package commandline

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"text/tabwriter"

	"goodkind.io/mwan/gateway/internal/healthcheck"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/gateway/internal/logging"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/statuspush"
	"goodkind.io/mwan/gateway/version"
)

const (
	statusTextMinWidth = 0
	statusTextTabWidth = 4
	statusTextPadding  = 2
)

func operatorLogger(diagnostics io.Writer, verbose bool) (*slog.Logger, io.Closer) {
	destination := io.Discard
	if verbose {
		destination = diagnostics
	}

	return logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers: []slog.Handler{
			slog.NewTextHandler(destination, &slog.HandlerOptions{Level: slog.LevelDebug}),
		},
	})
}

type outputValue interface {
	statuspush.Status | []netif.PingAttempt | []debugConnectivityWANResult |
		[]debugHTTPObservation | npt.RenderedTable | debugPrefixesResult | debugRoutesResult |
		debugPolicyResult | debugStatusResult | debugStatsResult | debugSimulationResult |
		debugSystemdResult | healthcheck.Record | []healthcheck.Record
}

// WriteJSON encodes a supported value as JSON.
//
// It logs encoder failures and returns them to the caller.
//
// Example:
//
//	err := WriteJSON(logger, output, status)
func WriteJSON[T outputValue](logger *slog.Logger, output io.Writer, value T) error {
	if err := json.NewEncoder(output).Encode(value); err != nil {
		logger.Warn("encode JSON output failed", "err", err)

		return fmt.Errorf("encode JSON: %w", err)
	}

	return nil
}

// WriteGatewayStatusText writes a gateway status snapshot as text.
//
// It sorts provider names before writing rows.
//
// Example:
//
//	err := WriteGatewayStatusText(logger, output, status)
func WriteGatewayStatusText(logger *slog.Logger, output io.Writer, status statuspush.Status) error {
	if _, err := fmt.Fprintf(
		output,
		"sent at: %s\nactive tier: %d\nproviders:\n",
		status.SentAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		status.ActiveTier,
	); err != nil {
		logger.Warn("write gateway status failed", "err", err)

		return fmt.Errorf("write gateway status: %w", err)
	}

	names := make([]string, 0, len(status.Providers))
	for name := range status.Providers {
		names = append(names, name)
	}

	sort.Strings(names)
	writer := tabwriter.NewWriter(
		output,
		statusTextMinWidth,
		statusTextTabWidth,
		statusTextPadding,
		' ',
		0,
	)

	for _, name := range names {
		if _, err := fmt.Fprintf(writer, "  %s\t%s\n", name, status.Providers[name]); err != nil {
			logger.Warn("write provider failed", "err", err)

			return fmt.Errorf("write provider: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		logger.Warn("flush providers failed", "err", err)

		return fmt.Errorf("flush providers: %w", err)
	}

	return nil
}
