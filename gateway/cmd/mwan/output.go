package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/configstate"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/statuspush"
)

func outputFormatFlag(command *cobra.Command, format *string, defaultFormat string) {
	command.Flags().StringVar(format, "output-format", defaultFormat, "Choose text or json output.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if *format != "text" && *format != "json" {
			return fmt.Errorf("invalid output format %q: choose text or json", *format)
		}
		return nil
	}
}

type outputValue interface {
	configstate.State | statuspush.Status | []netif.PingAttempt
}

// WriteJSON encodes a finite result as one JSON record.
func WriteJSON[T outputValue](output io.Writer, value T) error {
	if err := json.NewEncoder(output).Encode(value); err != nil {
		slog.Warn("encode JSON output failed", "err", err)
		return fmt.Errorf("encode JSON: %w", err)
	}
	return nil
}

// WriteConfigStateText presents the shared configuration state for operators.
func WriteConfigStateText(output io.Writer, state configstate.State) error {
	_, err := fmt.Fprintf(output, "config hash: %s\nconfig manifest:\n%slast deploy epoch: %d\n", state.ConfigHash, state.ConfigManifest, state.LastDeployEpoch)
	if err != nil {
		slog.Warn("write configuration state failed", "err", err)
		return fmt.Errorf("write config state: %w", err)
	}
	return nil
}

// WriteGatewayStatusText presents the shared gateway status for operators.
func WriteGatewayStatusText(output io.Writer, status statuspush.Status) error {
	if _, err := fmt.Fprintf(output, "sent at: %s\nactive tier: %d\nproviders:\n", status.SentAt.Format("2006-01-02T15:04:05.999999999Z07:00"), status.ActiveTier); err != nil {
		slog.Warn("write gateway status failed", "err", err)
		return fmt.Errorf("write gateway status: %w", err)
	}
	names := make([]string, 0, len(status.Providers))
	for name := range status.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, name := range names {
		if _, err := fmt.Fprintf(writer, "  %s\t%s\n", name, status.Providers[name]); err != nil {
			slog.Warn("write provider failed", "err", err)
			return fmt.Errorf("write provider: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		slog.Warn("flush providers failed", "err", err)
		return fmt.Errorf("flush providers: %w", err)
	}
	return nil
}
