package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadBGPForwardingReadiness(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		wantError  string
		wantConfig bool
	}{
		{
			name: "primary",
			config: `[bgp]
enabled = true
use_wanconfig = true

[bgp.forwarding_readiness]
socket_path = "/run/mwan-forwarding-ready.sock"
poll_interval_milliseconds = 1000
read_timeout_milliseconds = 500
`,
			wantConfig: true,
		},
		{
			name: "primary missing table",
			config: `[bgp]
enabled = true
use_wanconfig = true
`,
			wantError: "socket_path",
		},
		{
			name: "primary relative socket",
			config: `[bgp]
enabled = true
use_wanconfig = true

[bgp.forwarding_readiness]
socket_path = "mwan.sock"
poll_interval_milliseconds = 1000
read_timeout_milliseconds = 500
`,
			wantError: "socket_path",
		},
		{
			name: "primary zero poll interval",
			config: `[bgp]
enabled = true
use_wanconfig = true

[bgp.forwarding_readiness]
socket_path = "/run/mwan-forwarding-ready.sock"
poll_interval_milliseconds = 0
read_timeout_milliseconds = 500
`,
			wantError: "poll_interval_milliseconds",
		},
		{
			name: "primary negative read timeout",
			config: `[bgp]
enabled = true
use_wanconfig = true

[bgp.forwarding_readiness]
socket_path = "/run/mwan-forwarding-ready.sock"
poll_interval_milliseconds = 1000
read_timeout_milliseconds = -1
`,
			wantError: "read_timeout_milliseconds",
		},
		{
			name: "backup without readiness",
			config: `[bgp]
enabled = true
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(configPath, []byte(test.config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("MWAN_CONFIG", configPath)
			cfg, err := Load()
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Load() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if test.wantConfig {
				readiness := cfg.BGP.ForwardingReadiness
				if readiness.SocketPath != "/run/mwan-forwarding-ready.sock" {
					t.Errorf("socket_path = %q", readiness.SocketPath)
				}
				if readiness.PollInterval() != time.Second {
					t.Errorf("poll interval = %s", readiness.PollInterval())
				}
				if readiness.ReadTimeout() != 500*time.Millisecond {
					t.Errorf("read timeout = %s", readiness.ReadTimeout())
				}
			}
		})
	}
}
