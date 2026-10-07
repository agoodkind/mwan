package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkload"
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
	status, err := readGatewayStatus(cfg, flags, clock.Real{})
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

func requireMaxStateAge(section *config.IfMgrHealthSection) (time.Duration, error) {
	if section == nil {
		return 0, errors.New("ifmgr.modules.health.max_state_age is not set")
	}
	maxStateAge, present, err := section.ParseMaxStateAge()
	if err != nil {
		slog.Warn("gateway-status: max_state_age rejected", "err", err)
		return 0, fmt.Errorf("ifmgr.modules.health: %w", err)
	}
	if !present {
		return 0, errors.New("ifmgr.modules.health.max_state_age is not set")
	}
	return maxStateAge, nil
}

func readGatewayStatus(
	cfg *config.Config,
	flags gatewayStatusFlags,
	wallClock clock.Clock,
) (statuspush.Status, error) {
	none := statuspush.Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil}
	maxStateAge, err := requireMaxStateAge(cfg.IfMgr.Modules.Health)
	if err != nil {
		return none, gatewayStatusError("read health state age bound", err)
	}
	if err := networkload.ApplyFrom(cfg, flags.networkPath, flags.schemaDir); err != nil {
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
	stateAge := wallClock.Now().Sub(info.ModTime())
	if stateAge > maxStateAge {
		return none, gatewayStatusError("check health state file age", fmt.Errorf(
			"%s was written %s ago, older than ifmgr.modules.health.max_state_age %s",
			statePath, stateAge.Round(time.Second), maxStateAge,
		))
	}
	states, err := netif.ReadHealthState(statePath)
	if err != nil {
		return none, gatewayStatusError("read health state file", err)
	}
	// SentAt uses the health state file's modification time.
	sentAt := info.ModTime().UTC()
	return statuspush.NewStatus(sentAt, healthConfig.TierMembers(), states), nil
}
