package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
			text:        "",
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
		t.Run(
			tc.name,
			func(t *testing.T) {
				configPath := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(configPath, []byte(tc.text), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}

				t.Setenv("MWAN_CONFIG", configPath)

				cfg, err := LoadPath("")

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
					t.Fatalf(
						"max_state_age = %s present %t, want %s present %t",
						gotAge,
						gotPresent,
						tc.wantAge,
						tc.wantPresent,
					)
				}
			},
		)
	}
}
