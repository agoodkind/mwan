package commandconfig

import (
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/config"
)

// WithConfig loads the selected TOML file and calls execute with its
// settings.
//
// It logs configuration and operation failures before returning an error.
//
// Example:
//
//	err := commandconfig.WithConfig(commandPath, configPath, execute)
func WithConfig(commandPath, path string, execute func(*config.Config) error) error {
	cfg, err := config.LoadPath(path)
	if err != nil {
		slog.Error("load configuration failed", "err", err)

		return fmt.Errorf("load configuration: %w", err)
	}

	if err := execute(cfg); err != nil {
		slog.Error("command failed", "command", commandPath, "err", err)

		return err
	}

	return nil
}
