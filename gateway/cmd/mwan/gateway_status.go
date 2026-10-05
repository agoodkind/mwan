package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/statuspush"
)

func runGatewayStatus(
	args []string,
	cfg *config.Config,
	output io.Writer,
	diagnostics io.Writer,
) int {
	flags, err := parseGatewayStatusFlags(args, diagnostics)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan gateway-status: %v\n", err)
		return 1
	}
	status, err := readGatewayStatus(cfg, flags)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan gateway-status: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(status); err != nil {
		fmt.Fprintf(diagnostics, "mwan gateway-status: write status: %v\n", err)
		return 1
	}
	return 0
}

func gatewayStatusError(operation string, err error) error {
	slog.Warn("gateway-status: operation failed", "operation", operation, "err", err)
	return fmt.Errorf("%s: %w", operation, err)
}

func readGatewayStatus(
	cfg *config.Config,
	flags gatewayStatusFlags,
) (statuspush.Status, error) {
	none := statuspush.Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil}
	if err := networkjson.ApplyFrom(cfg, flags.networkPath, flags.schemaDir); err != nil {
		return none, gatewayStatusError("load network configuration", err)
	}
	healthConfig, err := buildHealthConfig(buildWANRefs(cfg.IfMgr), cfg.IfMgr.Modules.Health)
	if err != nil {
		return none, gatewayStatusError("build health configuration", err)
	}
	statePath := healthConfig.StateFilePath()
	info, err := os.Stat(statePath)
	if err != nil {
		return none, gatewayStatusError("stat health state file", err)
	}
	states, err := netif.ReadHealthState(statePath)
	if err != nil {
		return none, gatewayStatusError("read health state file", err)
	}
	// SentAt is the state file modification time. The health module rewrites the
	// file every probe cycle.
	sentAt := info.ModTime().UTC()
	return statuspush.NewStatus(sentAt, healthConfig.TierMembers(), states), nil
}
