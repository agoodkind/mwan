//go:build !systemd

package commandline

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/gateway/internal/config"
)

func TestIfMgrStdoutHonorsDebugSetting(t *testing.T) {
	const message = "test debug record"

	cases := []struct {
		name        string
		flagDebug   bool
		configDebug bool
		wantStdout  bool
	}{
		{name: "debug off", flagDebug: false, configDebug: false, wantStdout: false},
		{name: "debug flag", flagDebug: true, configDebug: false, wantStdout: true},
		{name: "debug config", flagDebug: false, configDebug: true, wantStdout: true},
	}
	for _, test := range cases {
		t.Run(
			test.name,
			func(t *testing.T) {
				var cfg config.Config
				cfg.IfMgr.Debug = test.configDebug
				cfg.IfMgr.JSONLogFile = filepath.Join(t.TempDir(), "ifmgr.json")
				var stdout bytes.Buffer
				logger, closer := buildIfMgrLogger(&cfg, test.flagDebug, &stdout)
				logger.Debug(message)
				if err := closer.Close(); err != nil {
					t.Fatalf("close logger: %v", err)
				}

				if got := bytes.Contains(stdout.Bytes(), []byte(message)); got != test.wantStdout {
					t.Errorf(
						"stdout contains the debug record = %t, want %t: %s",
						got,
						test.wantStdout,
						stdout.String(),
					)
				}

				records, err := os.ReadFile(cfg.IfMgr.JSONLogFile)
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Contains(records, []byte(message)) {
					t.Errorf("JSON log omitted the debug record: %s", records)
				}
			},
		)
	}
}
