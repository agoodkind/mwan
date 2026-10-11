package main

import (
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/watchdogconfig"
)

func sharedWatchdogSection(cfg *config.Config) (watchdogconfig.WatchdogSection, error) {
	var none watchdogconfig.WatchdogSection
	if cfg == nil {
		return none, errors.New("shared watchdog configuration is required")
	}
	section, err := watchdogconfig.LoadWatchdogSection(cfg.Path)
	if err != nil {
		slog.Error("load shared watchdog section failed", "path", cfg.Path, "err", err)
		return none, fmt.Errorf("load shared watchdog section: %w", err)
	}
	return section, nil
}

func runIfMgrCommand(cfg *config.Config, flags ifmgrFlags) error {
	watchdogSection, err := sharedWatchdogSection(cfg)
	if err != nil {
		return err
	}
	return runIfMgr(cfg, watchdogSection, flags)
}
