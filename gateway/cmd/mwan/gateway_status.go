package main

import (
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

func runGatewayStatusOptions(flags gatewayStatusFlags, cfg *config.Config, output, diagnostics io.Writer) int {
	logger, closer := operatorLogger(diagnostics, flags.verbose)
	defer closer.Close()
	status, err := readGatewayStatus(cfg, flags, clock.Real{}, logger)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan gateway-status: %v\n", err)
		return 1
	}
	if flags.outputFormat == "text" {
		err = WriteGatewayStatusText(output, status)
	} else {
		err = WriteJSON(output, status)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan gateway-status: write status: %v\n", err)
		return 1
	}
	return 0
}

func gatewayStatusError(logger *slog.Logger, operation string, err error) error {
	logger.Warn("gateway-status: operation failed", "operation", operation, "err", err)
	return fmt.Errorf("%s: %w", operation, err)
}

func requireMaxStateAge(logger *slog.Logger, section *config.IfMgrHealthSection) (time.Duration, error) {
	if section == nil {
		return 0, errors.New("ifmgr.modules.health.max_state_age is not set")
	}
	maxStateAge, present, err := section.ParseMaxStateAge()
	if err != nil {
		logger.Warn("gateway-status: max_state_age rejected", "err", err)
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
	logger *slog.Logger,
) (statuspush.Status, error) {
	none := statuspush.Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil}
	maxStateAge, err := requireMaxStateAge(logger, cfg.IfMgr.Modules.Health)
	if err != nil {
		return none, gatewayStatusError(logger, "read health state age bound", err)
	}
	if err := networkload.ApplyFrom(cfg, flags.networkPath, flags.schemaDir); err != nil {
		return none, gatewayStatusError(logger, "load network configuration", err)
	}
	healthConfig, err := buildHealthConfig(buildWANRefs(cfg.IfMgr), cfg.IfMgr.Modules.Health)
	if err != nil {
		return none, gatewayStatusError(logger, "build health configuration", err)
	}
	statePath := healthConfig.StateFilePath()
	info, err := os.Stat(statePath)
	if err != nil {
		return none, gatewayStatusError(logger, "stat health state file", err)
	}
	stateAge := wallClock.Now().Sub(info.ModTime())
	if stateAge > maxStateAge {
		return none, gatewayStatusError(logger, "check health state file age", fmt.Errorf(
			"%s was written %s ago, older than ifmgr.modules.health.max_state_age %s",
			statePath, stateAge.Round(time.Second), maxStateAge,
		))
	}
	states, err := netif.ReadHealthState(statePath)
	if err != nil {
		return none, gatewayStatusError(logger, "read health state file", err)
	}
	// SentAt uses the health state file's modification time.
	sentAt := info.ModTime().UTC()
	return statuspush.NewStatus(sentAt, healthConfig.TierMembers(), states), nil
}
