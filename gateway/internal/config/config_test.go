package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/internal/config"
)

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	return configPath
}

func TestLoadPathReadsAgentSocketAndTCPSettings(t *testing.T) {
	configPath := writeConfig(
		t,
		"[agent]\nsocket_path = \"/run/mwan/agent.sock\"\ntcp_addr = \"127.0.0.1:50052\"\n",
	)
	cfg, err := config.LoadPath(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Agent.SocketPath != "/run/mwan/agent.sock" ||
		cfg.Agent.TCPAddr != "127.0.0.1:50052" {
		t.Fatalf("agent settings = %+v", cfg.Agent)
	}
}

func TestLoadPathLeavesAgentSocketUnconfiguredByDefault(t *testing.T) {
	cfg, err := config.LoadPath(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Agent.SocketPath != "" {
		t.Fatalf("agent socket path = %q", cfg.Agent.SocketPath)
	}
}

func TestLoadPathUsesEmailDefaults(t *testing.T) {
	t.Setenv("SMTP2GO_API_KEY", "")
	cfg, err := config.LoadPath(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}

	want := config.EmailConfig{MinLevel: "ERROR", Cooldown: "5m"}
	if cfg.Email != want {
		t.Fatalf("email settings = %+v, want %+v", cfg.Email, want)
	}
}

func TestLoadPathOverridesEmailAPIKeyFromEnvironment(t *testing.T) {
	t.Setenv("SMTP2GO_API_KEY", " key-from-environment ")
	cfg, err := config.LoadPath(writeConfig(t, "[email]\nsmtp2go_api_key = \"file-key\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Email.SMTP2GOAPIKey != "key-from-environment" {
		t.Fatalf("email API key = %q", cfg.Email.SMTP2GOAPIKey)
	}
}

func TestLoadPathNetworkdTimeout(t *testing.T) {
	for _, test := range []struct {
		name      string
		text      string
		want      time.Duration
		wantError string
	}{
		{"default", "", 60 * time.Second, ""},
		{"configured", "[ifmgr]\nnetworkd_timeout_seconds = 17\n", 17 * time.Second, ""},
		{"zero", "[ifmgr]\nnetworkd_timeout_seconds = 0\n", 0, "must be greater than zero"},
		{"negative", "[ifmgr]\nnetworkd_timeout_seconds = -1\n", 0, "must be greater than zero"},
		{
			"maximum duration",
			"[ifmgr]\nnetworkd_timeout_seconds = 9223372036\n",
			9223372036 * time.Second,
			"",
		},
		{
			"duration overflow",
			"[ifmgr]\nnetworkd_timeout_seconds = 9223372037\n",
			0,
			"must not exceed 9223372036",
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				cfg, err := config.LoadPath(writeConfig(t, test.text))
				if test.wantError != "" {
					if err == nil ||
						!strings.Contains(
							err.Error(),
							"[ifmgr] networkd_timeout_seconds "+test.wantError,
						) {
						t.Fatalf("LoadPath error = %v", err)
					}

					return
				}

				if err != nil {
					t.Fatal(err)
				}

				if actual := cfg.IfMgr.NetworkdTimeout(); actual != test.want {
					t.Fatalf("networkd timeout = %s, want %s", actual, test.want)
				}
			},
		)
	}
}
