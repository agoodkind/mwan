package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadAcceptsHostConfigCarryingOpnsenseTables loads the hypervisor config
// shape, which still renders the [opnsense.host], [opnsense.upgrade], and
// [opnsense.drain] tables for binaries that read them from this file. The
// gateway loader no longer models those tables and must still load the file.
func TestLoadAcceptsHostConfigCarryingOpnsenseTables(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configText := `
hostname = "hypervisor-test"
mwan_vmid = "4100"

[watchdog]
service_name = "mwan-watchdog-test"

[opnsense.host]
upstream = "unix:///var/run/mwan-opnsense-drain.sock"
listen = "/var/run/mwan-opnsense.sock"
reconnect = "2s"
heartbeat_interval = "30s"
heartbeat_timeout = "10s"

[opnsense.upgrade]
vmid = 4242

[opnsense.drain]
chardev = "unix:///var/run/qemu-server/4242.mwanrpc"
listen = "/var/run/mwan-opnsense-drain.sock"
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MWAN_CONFIG", configPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load rejected a host config carrying [opnsense.*] tables: %v", err)
	}
	if cfg.Hostname != "hypervisor-test" || cfg.MwanVMID != "4100" {
		t.Errorf("hostname = %q mwan_vmid = %q, want hypervisor-test and 4100", cfg.Hostname, cfg.MwanVMID)
	}
	if cfg.Watchdog.ServiceName != "mwan-watchdog-test" {
		t.Errorf("watchdog service_name = %q, want mwan-watchdog-test", cfg.Watchdog.ServiceName)
	}
}
