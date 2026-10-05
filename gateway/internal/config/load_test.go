package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestLoadHealthMaxStateAge(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		wantAge     time.Duration
		wantPresent bool
		wantFail    bool
	}{
		{
			name:        "a duration",
			text:        "[ifmgr.modules.health]\nmax_state_age = \"90s\"\n",
			wantAge:     90 * time.Second,
			wantPresent: true,
			wantFail:    false,
		},
		{
			name:        "the setting is absent",
			text:        "[ifmgr.modules.health]\nstate_file = \"/run/mwan-health.state\"\n",
			wantAge:     0,
			wantPresent: false,
			wantFail:    false,
		},
		{
			name:        "no health section",
			text:        `mwan_vmid = "4100"`,
			wantAge:     0,
			wantPresent: false,
			wantFail:    false,
		},
		{
			name:        "a malformed duration",
			text:        "[ifmgr.modules.health]\nmax_state_age = \"soon\"\n",
			wantAge:     0,
			wantPresent: false,
			wantFail:    true,
		},
		{
			name:        "a zero duration",
			text:        "[ifmgr.modules.health]\nmax_state_age = \"0s\"\n",
			wantAge:     0,
			wantPresent: false,
			wantFail:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(configPath, []byte(tc.text), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("MWAN_CONFIG", configPath)

			cfg, err := Load()

			if tc.wantFail {
				if err == nil {
					t.Fatalf("Load accepted %q", tc.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.IfMgr.Modules.Health == nil {
				if tc.wantPresent {
					t.Fatal("health section did not load")
				}
				return
			}
			gotAge, gotPresent, parseErr := cfg.IfMgr.Modules.Health.ParseMaxStateAge()
			if parseErr != nil {
				t.Fatalf("ParseMaxStateAge: %v", parseErr)
			}
			if gotAge != tc.wantAge || gotPresent != tc.wantPresent {
				t.Fatalf("max_state_age = %s present %t, want %s present %t",
					gotAge, gotPresent, tc.wantAge, tc.wantPresent)
			}
		})
	}
}

func TestLoadedGuestTypeFilesystemFreeze(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "missing key is a QEMU guest", text: `mwan_vmid = "4100"`, want: true},
		{name: "qemu", text: `guest_type = "qemu"`, want: true},
		{name: "lxc", text: `guest_type = "lxc"`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(configPath, []byte(tc.text), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("MWAN_CONFIG", configPath)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			if got := cfg.GuestType.HasFilesystemFreeze(); got != tc.want {
				t.Fatalf("HasFilesystemFreeze = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestLoadGuestType(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		want     GuestType
		wantFail bool
	}{
		{name: "missing key is a QEMU guest", text: `mwan_vmid = "4100"`, want: GuestTypeQEMU, wantFail: false},
		{name: "qemu", text: `guest_type = "qemu"`, want: GuestTypeQEMU, wantFail: false},
		{name: "lxc", text: `guest_type = "lxc"`, want: GuestTypeLXC, wantFail: false},
		{name: "unknown value", text: `guest_type = "vm"`, want: "", wantFail: true},
		{name: "empty value", text: `guest_type = ""`, want: "", wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(configPath, []byte(tc.text), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("MWAN_CONFIG", configPath)

			cfg, err := Load()

			if tc.wantFail {
				if err == nil {
					t.Fatalf("Load accepted %q", tc.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.GuestType != tc.want {
				t.Errorf("guest type = %q, want %q", cfg.GuestType, tc.want)
			}
		})
	}
}
