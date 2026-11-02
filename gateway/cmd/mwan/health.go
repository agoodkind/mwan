package main

import (
	"context"
	"fmt"
	"io"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/healthcheck"
	"goodkind.io/mwan/internal/networkload"
)

func runHealthOptions(ctx context.Context, path string, flags healthFlags, output, diagnostics io.Writer) error {
	logger, closer := operatorLogger(diagnostics, flags.verbose)
	defer closer.Close()
	cfg, err := config.LoadPath(path)
	if err != nil {
		logger.Warn("health configuration rejected", "err", err)
		return fmt.Errorf("load health configuration: %w", err)
	}
	if err := networkload.ApplyFrom(cfg, flags.networkPath, flags.schemaDir); err != nil {
		logger.Warn("health network configuration rejected", "err", err)
		return fmt.Errorf("load health network configuration: %w", err)
	}
	policy, err := buildHealthProviderConfig(buildWANRefs(cfg.IfMgr), cfg.IfMgr.Modules.Health)
	if err != nil {
		logger.Warn("health provider policy rejected", "err", err)
		return fmt.Errorf("build health provider policy: %w", err)
	}
	records := make([]healthcheck.Record, 0)
	emit := func(record healthcheck.Record) error {
		if !flags.watch {
			records = append(records, record)
			return nil
		}
		if flags.outputFormat == "json" {
			return WriteJSON(output, record)
		}
		return writeHealthRecordText(output, logger, record)
	}
	if err := healthcheck.Run(ctx, healthcheck.Options{WANs: policy.WANs, Timeout: policy.Timeout, Watch: flags.watch, Clock: clock.Real{}}, logger, emit); err != nil {
		logger.Warn("health probes failed", "err", err)
		return fmt.Errorf("run health probes: %w", err)
	}
	if flags.watch && len(policy.WANs) != 0 {
		return nil
	}
	if flags.outputFormat == "json" {
		return WriteJSON(output, records)
	}
	return writeHealthText(output, logger, records)
}
