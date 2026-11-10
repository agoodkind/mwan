package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/configstate"
	"goodkind.io/mwan/internal/netif"
)

func TestCLIConfigStateFormats(t *testing.T) {
	binary := buildReportCommand(t)
	inputs := writeConfigStateInputs(t, configStateDeployText)
	defaultOutput, diagnostics, code := runCLIProcess(t, binary, inputs.args...)
	if code != 0 {
		t.Fatalf("default output exit=%d diagnostics=%s", code, diagnostics)
	}
	jsonArgs := append(append([]string{}, inputs.args...), "--output-format", "json")
	jsonOutput, diagnostics, code := runCLIProcess(t, binary, jsonArgs...)
	if code != 0 || jsonOutput != defaultOutput {
		t.Fatalf("JSON exit=%d output=%q default=%q diagnostics=%s", code, jsonOutput, defaultOutput, diagnostics)
	}
	textArgs := append(append([]string{}, inputs.args...), "--output-format", "text")
	textOutput, diagnostics, code := runCLIProcess(t, binary, textArgs...)
	state := configstate.Read(inputs.root, inputs.deployFile)
	want := fmt.Sprintf("config hash: %s\nconfig manifest:\n%slast deploy epoch: %d\n", state.ConfigHash, state.ConfigManifest, state.LastDeployEpoch)
	if code != 0 || textOutput != want {
		t.Fatalf("text exit=%d output=%q want=%q diagnostics=%s", code, textOutput, want, diagnostics)
	}
}

func TestCLIConfigStateVerboseDiagnostics(t *testing.T) {
	binary := buildReportCommand(t)
	inputs := writeConfigStateInputs(t, configStateDeployText)
	output, diagnostics, code := runCLIProcess(t, binary, inputs.args...)
	if code != 0 || diagnostics != "" {
		t.Fatalf("default exit=%d diagnostics=%q", code, diagnostics)
	}
	args := append(append([]string{}, inputs.args...), "--verbose")
	verboseOutput, diagnostics, code := runCLIProcess(t, binary, args...)
	if code != 0 || verboseOutput != output || !strings.Contains(diagnostics, "mwan boundary") {
		t.Fatalf("verbose exit=%d output=%q diagnostics=%q", code, verboseOutput, diagnostics)
	}
}

func TestCLIDebugPingJSON(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output, diagnostics, code := runCLIProcess(t, binary, "debug", "ping4", "mwan-no-iface", "--config", path, "--output-format", "json")
	if code != 0 {
		t.Fatalf("ping exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
	var attempts []netif.PingAttempt
	if err := json.Unmarshal([]byte(output), &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 6 {
		t.Fatalf("attempts=%+v", attempts)
	}
	for index, attempt := range attempts {
		if attempt.Interface != "mwan-no-iface" || attempt.Family != "inet" || attempt.Attempt != index%3+1 || attempt.Timeout.Seconds() != 5 || attempt.Target == "" || attempt.Error == "" {
			t.Fatalf("attempt=%+v", attempt)
		}
	}
}

func TestCLIOutputFormatRejectedBeforeConfiguration(t *testing.T) {
	binary := buildReportCommand(t)
	missing := filepath.Join(t.TempDir(), "missing.toml")
	for _, command := range []string{"gateway-status", "config-state"} {
		output, diagnostics, code := runCLIProcess(t, binary, command, "--config", missing, "--output-format", "invalid")
		if code == 0 || output != "" || !strings.Contains(diagnostics, "invalid output format") || strings.Contains(diagnostics, missing) {
			t.Fatalf("%s exit=%d output=%q diagnostics=%q", command, code, output, diagnostics)
		}
	}
	for _, view := range []string{"connectivity", "curl4", "curl6", "lb4", "lb6", "lb4-ifaces", "lb6-ifaces", "ping4", "ping6", "npt", "prefixes", "routes", "policy", "status", "stats", "sim4", "sim6", "systemd"} {
		output, diagnostics, code := runCLIProcess(t, binary, "debug", view, "--config", missing, "--output-format", "invalid")
		if code == 0 || output != "" || !strings.Contains(diagnostics, "invalid output format") || strings.Contains(diagnostics, missing) {
			t.Fatalf("debug %s exit=%d output=%q diagnostics=%q", view, code, output, diagnostics)
		}
	}
}

func TestCLIGatewayStatusFormats(t *testing.T) {
	binary := buildReportCommand(t)
	statePath := filepath.Join(t.TempDir(), "health.state")
	if err := os.WriteFile(statePath, []byte("webpass:garbled\nmonkeybrains:healthy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := writeGatewayStatusInputs(t, statePath, freshStateBound)
	defaultOutput, diagnostics, code := runCLIProcess(t, binary, args...)
	if code != 0 {
		t.Fatalf("default exit=%d diagnostics=%s", code, diagnostics)
	}
	jsonArgs := append(append([]string{}, args...), "--output-format", "json")
	jsonOutput, diagnostics, code := runCLIProcess(t, binary, jsonArgs...)
	if code != 0 || jsonOutput != defaultOutput || !strings.Contains(jsonOutput, `"webpass":"unknown"`) {
		t.Fatalf("JSON exit=%d output=%q default=%q diagnostics=%s", code, jsonOutput, defaultOutput, diagnostics)
	}
	textArgs := append(append([]string{}, args...), "--output-format", "text")
	textOutput, diagnostics, code := runCLIProcess(t, binary, textArgs...)
	if code != 0 || !strings.Contains(textOutput, "webpass") || !strings.Contains(textOutput, "unknown") || !strings.Contains(textOutput, "monkeybrains") || !strings.Contains(textOutput, "healthy") {
		t.Fatalf("text exit=%d output=%q diagnostics=%s", code, textOutput, diagnostics)
	}
}

func TestCLIGatewayStatusVerboseDiagnostics(t *testing.T) {
	binary := buildReportCommand(t)
	args := writeGatewayStatusInputs(t, filepath.Join(t.TempDir(), "health.state"), "")
	output, diagnostics, code := runCLIProcess(t, binary, args...)
	if code != 1 || output != "" || strings.Contains(diagnostics, "gateway-status: operation failed") || !strings.Contains(diagnostics, "ifmgr.modules.health.max_state_age is not set") {
		t.Fatalf("default exit=%d output=%q diagnostics=%q", code, output, diagnostics)
	}
	args = append(args, "--verbose")
	output, diagnostics, code = runCLIProcess(t, binary, args...)
	if code != 1 || output != "" || !strings.Contains(diagnostics, "gateway-status: operation failed") || !strings.Contains(diagnostics, "ifmgr.modules.health.max_state_age is not set") {
		t.Fatalf("verbose exit=%d output=%q diagnostics=%q", code, output, diagnostics)
	}
}
