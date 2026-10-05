package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"goodkind.io/mwan/internal/config"
)

func loadWatchdogSection(t *testing.T, section string) (*config.Config, error) {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(section), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := config.LoadArguments([]string{"mwan", "--config", configPath})
	return cfg, err
}

func TestLoadRejectsBothStatusSources(t *testing.T) {
	t.Parallel()

	_, err := loadWatchdogSection(t, `
[watchdog]
status_listen_port = 50053
status_command = ["/usr/local/bin/mwan", "gateway-status"]
`)

	if err == nil {
		t.Fatal("Load accepted status_command together with status_listen_port")
	}
}

func TestLoadAcceptsEitherStatusSourceAlone(t *testing.T) {
	t.Parallel()

	command, err := loadWatchdogSection(t, `
[watchdog]
status_command = ["/usr/local/bin/mwan", "gateway-status"]
`)
	if err != nil {
		t.Fatalf("Load rejected status_command alone: %v", err)
	}
	wantCommand := []string{"/usr/local/bin/mwan", "gateway-status"}
	if !slices.Equal(command.Watchdog.StatusCommand, wantCommand) {
		t.Fatalf("status_command = %v, want %v", command.Watchdog.StatusCommand, wantCommand)
	}

	port, err := loadWatchdogSection(t, `
[watchdog]
status_listen_port = 50053
`)
	if err != nil {
		t.Fatalf("Load rejected status_listen_port alone: %v", err)
	}
	if port.Watchdog.StatusListenPort != 50053 {
		t.Fatalf("status_listen_port = %d, want 50053", port.Watchdog.StatusListenPort)
	}
	if len(port.Watchdog.StatusCommand) != 0 {
		t.Fatalf("status_command = %v, want empty", port.Watchdog.StatusCommand)
	}
}
