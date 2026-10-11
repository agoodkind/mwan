package main

import (
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/watchdogconfig"
)

func sharedWatchdogSection(cfg *config.Config) (watchdogconfig.WatchdogSection, error) {
	var none watchdogconfig.WatchdogSection
	if cfg == nil || cfg.Path == "" {
		return none, nil
	}
	section, err := watchdogconfig.LoadWatchdogSection(cfg.Path)
	if err != nil {
		slog.Error("load shared watchdog section failed", "path", cfg.Path, "err", err)
		return none, fmt.Errorf("load shared watchdog section: %w", err)
	}
	return section, nil
}

func networkdTimeout(cfg *config.Config) (time.Duration, error) {
	section, err := sharedWatchdogSection(cfg)
	if err != nil {
		return 0, err
	}
	return time.Duration(section.ConnectivityTimeoutSeconds) * time.Second, nil
}
