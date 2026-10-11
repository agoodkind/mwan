package watchdog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/configstate"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/watchdog"
	"goodkind.io/mwan/internal/watchdogconfig"
)

const (
	configStateVerb     = "config-state"
	configStateHash     = "5f2c0d0b8f0d4c0e9a1b7e3d6c4a2f1e0d9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a"
	configStateFileHash = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	configStateEpoch    = 1700000000
	configStateFilePath = "/etc/mwan/config.toml"

	configDriftChildEnv      = "MWAN_WATCHDOG_CONFIG_DRIFT_TEST_CHILD"
	configDriftDirectoryEnv  = "MWAN_WATCHDOG_CONFIG_DRIFT_TEST_DIRECTORY"
	configDriftHash          = "a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a4c6d3e7b1a9e0c4d0f8b0d0c2f5"
	configDriftFileHash      = "9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0"
	configDriftAddedFileHash = "1122334455667788990011223344556677889900112233445566778899001122"
	configDriftAddedFilePath = "/etc/mwan/extra.toml"
	configDriftMarkerFile    = "first-state-printed"
	configDriftJSONLogFile   = "watchdog.json"
	configDriftMessage       = "config hash drift detected"
	configDriftWindowMinutes = 30
	vsockFallbackAlertKind   = "vsock-fallback"
)

type configDriftLogRecord struct {
	Message             string `json:"msg"`
	Level               string `json:"level"`
	AlertKind           string `json:"alert_kind"`
	OldHash             string `json:"old_hash"`
	NewHash             string `json:"new_hash"`
	ChangedFiles        string `json:"changed_files"`
	VMID                string `json:"vm_id"`
	ChangeWindowMinutes int    `json:"change_window_minutes"`
}

func wantConfigState() configstate.State {
	return configstate.State{
		ConfigHash:      configStateHash,
		ConfigManifest:  configStateFileHash + "  " + configStateFilePath + "\n",
		LastDeployEpoch: configStateEpoch,
	}
}

func driftedConfigState() configstate.State {
	return configstate.State{
		ConfigHash: configDriftHash,
		ConfigManifest: configDriftFileHash + "  " + configStateFilePath + "\n" +
			configDriftAddedFileHash + "  " + configDriftAddedFilePath + "\n",
		LastDeployEpoch: configStateEpoch,
	}
}

func newGuestConfigStateSource(t *testing.T) *watchdog.GuestConfigStateSource {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &watchdogconfig.Config{GuestType: watchdogconfig.GuestTypeLXC}
	return watchdog.NewGuestConfigStateSource(
		ops.NewRealOps(cfg, logger),
		statusVMID,
		[]string{statusBinary, configStateVerb},
		logger,
	)
}

func encodeConfigState(t *testing.T, state configstate.State) string {
	t.Helper()

	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode config state: %v", err)
	}
	return string(payload)
}

func installConfigDriftCommands(t *testing.T, directory, first, second string) {
	t.Helper()

	commands := t.TempDir()
	pct := fmt.Sprintf(
		"#!/bin/sh\ncase \"$1\" in\n"+
			"status) printf '%%s\\n' 'status: running' ;;\n"+
			"listsnapshot) ;;\n"+
			"exec)\n"+
			"[ \"$*\" = \"exec %[1]s -- %[2]s %[3]s\" ] || exit %[4]d\n"+
			"if [ -e '%[5]s' ]; then printf '%%s\\n' '%[7]s'\n"+
			"else : > '%[5]s'; printf '%%s\\n' '%[6]s'; fi ;;\n"+
			"*) exit 1 ;;\nesac\n",
		statusVMID, statusBinary, configStateVerb, argvMismatchExit,
		filepath.Join(directory, configDriftMarkerFile), first, second,
	)
	scripts := map[string]string{
		"pct":   pct,
		"ping":  "#!/bin/sh\nexit 0\n",
		"ping6": "#!/bin/sh\nexit 0\n",
	}
	for name, script := range scripts {
		path := filepath.Join(commands, name)
		if err := os.WriteFile(path, []byte(script), pruneExecutableMode); err != nil {
			t.Fatalf("os.WriteFile failed for %s: %v", name, err)
		}
	}
	t.Setenv("PATH", commands)
}

func runConfigDriftChild(t *testing.T) {
	t.Helper()

	directory := os.Getenv(configDriftDirectoryEnv)
	cfg := &watchdogconfig.Config{}
	cfg.MwanVMID = statusVMID
	cfg.GuestType = watchdogconfig.GuestTypeLXC
	cfg.Network.PingTargetIPv4 = "192.0.2.1"
	cfg.Network.PingTargetIPv6 = "2001:db8::1"
	cfg.Watchdog.LogFile = filepath.Join(directory, "watchdog.log")
	cfg.Watchdog.JSONLogFile = filepath.Join(directory, configDriftJSONLogFile)
	cfg.Watchdog.RollbackStateFile = filepath.Join(directory, "rollback.state")
	cfg.Watchdog.RollbackLockFile = filepath.Join(directory, "rollback.lock")
	cfg.Watchdog.CheckIntervalDegraded = 1
	cfg.Watchdog.MaxIterations = 1
	cfg.Watchdog.HashCheckEveryNHealthy = 1
	cfg.Watchdog.DeployWindowMinutes = configDriftWindowMinutes
	cfg.Watchdog.ConfigStateCommand = []string{statusBinary, configStateVerb}

	if err := watchdog.Run(cfg); err != nil {
		t.Fatalf("watchdog.Run failed: %v", err)
	}
}

func readConfigDriftLog(t *testing.T, directory string) []configDriftLogRecord {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(directory, configDriftJSONLogFile))
	if err != nil {
		t.Fatal(err)
	}
	var records []configDriftLogRecord
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record configDriftLogRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("json.Unmarshal failed for %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestRunReportsConfigDriftFromTheConfigStateCommand(t *testing.T) {
	if os.Getenv(configDriftChildEnv) == "1" {
		runConfigDriftChild(t)
		return
	}
	directory := t.TempDir()
	drifted := driftedConfigState()
	installConfigDriftCommands(
		t,
		directory,
		encodeConfigState(t, wantConfigState()),
		encodeConfigState(t, drifted),
	)

	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), configDriftChildEnv+"=1", configDriftDirectoryEnv+"="+directory)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("The watchdog test subprocess failed: %v: %s", err, output)
	}

	var drifts []configDriftLogRecord
	for _, record := range readConfigDriftLog(t, directory) {
		if record.AlertKind == vsockFallbackAlertKind {
			t.Fatalf("The log contains unexpected alert_kind %q", record.AlertKind)
		}
		if record.Message == configDriftMessage {
			drifts = append(drifts, record)
		}
	}
	if len(drifts) != 1 {
		t.Fatalf("The log contains %d drift records; the test requires 1", len(drifts))
	}
	want := configDriftLogRecord{
		Message:   configDriftMessage,
		Level:     slog.LevelWarn.String(),
		AlertKind: "",
		OldHash:   configStateHash,
		NewHash:   configDriftHash,
		ChangedFiles: "  modified: " + configStateFilePath + "\n" +
			"  added:    " + configDriftAddedFilePath + "\n",
		VMID:                statusVMID,
		ChangeWindowMinutes: configDriftWindowMinutes,
	}
	if drifts[0] != want {
		t.Fatalf("The drift record is %+v; the test requires %+v", drifts[0], want)
	}
}

func TestGuestConfigStateSourceDecodesTheCommandOutput(t *testing.T) {
	want := wantConfigState()
	installGuestVerb(t, configStateVerb, encodeConfigState(t, want), 0)
	source := newGuestConfigStateSource(t)

	state, _, err := source.Latest(context.Background())
	if err != nil {
		t.Fatal("config state read failed")
	}
	if state != want {
		t.Fatalf("state = %+v, want %+v", state, want)
	}
}

func TestGuestConfigStateSourceReportsNothingOnFailure(t *testing.T) {
	payload := encodeConfigState(t, wantConfigState())
	cases := []struct {
		name     string
		stdout   string
		exitCode int
		missing  bool
	}{
		{name: "the exec fails", stdout: payload, exitCode: 0, missing: true},
		{name: "the command exits non-zero", stdout: payload, exitCode: 3, missing: false},
		{name: "the output is not JSON", stdout: "gateway is up", exitCode: 0, missing: false},
		{name: "the output is empty", stdout: "", exitCode: 0, missing: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installGuestVerb(t, configStateVerb, tc.stdout, tc.exitCode)
			if tc.missing {
				t.Setenv("PATH", t.TempDir())
			}
			source := newGuestConfigStateSource(t)

			state, _, err := source.Latest(context.Background())

			ok := err == nil
			none := configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}
			if ok || state != none {
				t.Fatalf("state = %+v, ok = %t, want %+v, false", state, ok, none)
			}
		})
	}
}
