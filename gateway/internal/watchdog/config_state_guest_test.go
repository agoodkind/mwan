package watchdog_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
)

func wantConfigState() configstate.State {
	return configstate.State{
		ConfigHash:      configStateHash,
		ConfigManifest:  configStateFileHash + "  /etc/mwan/config.toml\n",
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

func TestGuestConfigStateSourceDecodesTheCommandOutput(t *testing.T) {
	want := wantConfigState()
	installGuestVerb(t, configStateVerb, encodeConfigState(t, want), 0)
	source := newGuestConfigStateSource(t)

	state, ok := source.Latest(context.Background())

	if !ok {
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

			state, ok := source.Latest(context.Background())

			none := configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}
			if ok || state != none {
				t.Fatalf("state = %+v, ok = %t, want %+v, false", state, ok, none)
			}
		})
	}
}
