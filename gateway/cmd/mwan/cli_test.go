package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/observation"
)

func runCLIProcess(t *testing.T, binary string, arguments ...string) (string, string, int) {
	t.Helper()
	return runCLIProcessInput(t, binary, "", arguments...)
}

func runCLIProcessInput(t *testing.T, binary, input string, arguments ...string) (string, string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+filepath.Join(t.TempDir(), "absent.toml"))
	command.Stdin = strings.NewReader(input)
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	err := command.Run()
	if err == nil {
		return output.String(), diagnostics.String(), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatal(err)
	}
	return output.String(), diagnostics.String(), exitError.ExitCode()
}

func TestCLIHelpEveryCommand(t *testing.T) {
	binary := buildReportCommand(t)
	commands := [][]string{
		{},
		{"help"},
		{"--help"},
		{"agent"},
		{"watchdog"},
		{"watchdog", "failover"},
		{"ifmgr"},
		{"health"},
		{"notify"},
		{"pd"},
		{"trace-boot"},
		{"deploy"},
		{"deploy-gate"},
		{"wanconfig-selftest"},
		{"version"},
		{"install"},
		{"observe"},
		{"gateway-status"},
		{"config-state"},
		{"report"},
		{"report", "send"},
		{"report", "watch"},
		{"debug"},
	}
	for _, view := range []string{"connectivity", "curl4", "curl6", "lb4", "lb4-ifaces", "lb6", "lb6-ifaces", "npt", "ping4", "ping6", "policy", "prefixes", "routes", "sim4", "sim6", "stats", "status", "systemd", "trace-start", "trace-stop", "trace-tail"} {
		commands = append(commands, []string{"debug", view})
	}
	for _, parent := range []string{"deploy", "deploy-gate"} {
		for _, action := range []string{"arm", "watch", "status", "lease", "release", "verify", "recover", "check-egress", "check-owned-addresses", "check-network", "check-networkd", "check-release", "check-firewall", "inspect-firewall", "check-failover", "wait-reboot", "wait-egress", "wait-deploy"} {
			commands = append(commands, []string{parent, action})
		}
	}
	commands = append(commands, []string{"deploy-gate", "commit"})
	for _, arguments := range commands {
		t.Run(strings.Join(arguments, "/"), func(t *testing.T) {
			invocation := append(append([]string(nil), arguments...), "--help")
			output, diagnostics, code := runCLIProcess(t, binary, invocation...)
			if code != 0 || !strings.Contains(output, "Usage:") || !strings.Contains(output, "Flags:") {
				t.Fatalf("help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
			}
			if diagnostics != "" {
				t.Fatalf("help wrote diagnostics: %s", diagnostics)
			}
		})
	}
	output, diagnostics, code := runCLIProcess(t, binary)
	if code != 0 || !strings.Contains(output, "Available Commands:") {
		t.Fatalf("root help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
	for _, name := range []string{"agent", "watchdog", "ifmgr", "health", "notify", "pd", "debug", "trace-boot", "deploy", "deploy-gate", "wanconfig-selftest", "version", "install", "observe", "gateway-status", "config-state", "report"} {
		if !strings.Contains(output, name) {
			t.Errorf("root help omitted %s", name)
		}
	}
	output, diagnostics, code = runCLIProcess(t, binary, "install")
	if code != 0 || strings.Count(output, "Usage:") != 1 || diagnostics != "" {
		t.Fatalf("install help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
	output, diagnostics, code = runCLIProcess(t, binary, "deploy-gate", "verify", "--help")
	if code != 0 || !strings.Contains(output, "Aliases:") || !strings.Contains(output, "verify, commit") || diagnostics != "" {
		t.Fatalf("verify alias help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
}

func TestCLIHelpSkipsConfigurationAndFirewall(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(path, []byte("invalid = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"ifmgr", "--role", "wan", "--config", path, "--help"},
		{"ifmgr", "--role", "wan", "-config", path, "-help"},
		{"deploy", "arm", "--config", path, "--help"},
		{"report", "watch", "--config", path, "--help"},
		{"report", "--config", path, "send", "--help"},
		{"watchdog", "--config", path, "failover", "--help"},
		{"debug", "--config", path, "status", "--help"},
	} {
		output, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 0 || !strings.Contains(output, "Usage:") || diagnostics != "" {
			t.Fatalf("%v exit=%d output=%s diagnostics=%s", arguments, code, output, diagnostics)
		}
	}
}

func TestCLINestedConfigurationPlacement(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "selected-runtime.toml")
	for _, arguments := range [][]string{
		{"report", "--config", path, "send"},
		{"watchdog", "--config", path, "failover"},
		{"debug", "--config", path, "status"},
	} {
		_, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 1 || !strings.Contains(diagnostics, path) || strings.Contains(diagnostics, "Usage:") {
			t.Fatalf("%v exit=%d diagnostics=%s", arguments, code, diagnostics)
		}
	}
}

func TestCLIUsageAndRuntimeErrors(t *testing.T) {
	binary := buildReportCommand(t)
	for _, test := range []struct {
		arguments []string
		code      int
		usage     string
	}{
		{[]string{"unknown-command"}, 1, "mwan"},
		{[]string{"debug", "not-a-view"}, 1, "mwan debug"},
		{[]string{"debug"}, 1, "mwan debug"},
		{[]string{"ifmgr", "--unknown"}, 1, "mwan ifmgr"},
		{[]string{"ifmgr", "--role", "not-a-role"}, 1, "mwan ifmgr"},
		{[]string{"watchdog", "--red-team", "not-a-scenario"}, 1, "mwan watchdog"},
		{[]string{"agent", "--config", ""}, 1, "mwan agent"},
		{[]string{"pd", ""}, 1, "mwan pd"},
		{[]string{"deploy", "check-failover", ""}, 64, "mwan deploy check-failover"},
		{[]string{"observe", "--unknown"}, 2, "mwan observe"},
		{[]string{"install", "--apply"}, 2, "mwan install"},
		{[]string{"version", "extra"}, 2, "mwan version"},
		{[]string{"report", "send", "extra"}, 1, "mwan report send"},
		{[]string{"debug", "ping4", "one", "two"}, 1, "mwan debug ping4"},
		{[]string{"deploy", "wait-reboot", "invalid", "id", "1"}, 64, "mwan deploy wait-reboot"},
		{[]string{"deploy-gate", "check-network"}, 64, "mwan deploy-gate check-network"},
		{[]string{"deploy", "arm", "manifest.json"}, 64, "mwan deploy arm"},
	} {
		output, diagnostics, code := runCLIProcess(t, binary, test.arguments...)
		if code != test.code || strings.Count(diagnostics, "Usage:") != 1 || !strings.Contains(diagnostics, test.usage) || output != "" {
			t.Fatalf("%v exit=%d output=%s diagnostics=%s", test.arguments, code, output, diagnostics)
		}
		if strings.Contains(diagnostics, "read config") {
			t.Fatalf("usage attempted configuration: %s", diagnostics)
		}
	}
	_, diagnostics, code := runCLIProcess(t, binary, "agent")
	if code != 1 || !strings.Contains(diagnostics, "read config") || strings.Contains(diagnostics, "Usage:") {
		t.Fatalf("runtime exit=%d diagnostics=%s", code, diagnostics)
	}
}

func TestCLIDeploymentOperationExitCodes(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"deploy", "arm", "--config", path},
		{"deploy", "status", "operation-1", "--config", path},
		{"deploy", "lease", "operation-1", "generation-1", "phase-1", "invalid", "--config", path},
		{"deploy-gate", "release", "operation-1", "generation-1", "--config", path},
		{"deploy-gate", "commit", "--config", path},
	} {
		output, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 1 || strings.Count(diagnostics, "Usage:") != 1 || output != "" {
			t.Fatalf("operation arguments %v exit=%d output=%s diagnostics=%s", arguments, code, output, diagnostics)
		}
	}
	for _, arguments := range [][]string{
		{"deploy", "arm", "manifest.json"},
		{"deploy", "arm", "manifest.json", "--config", "relative.toml"},
		{"deploy", "arm", "manifest.json", "--config", ""},
	} {
		_, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 64 || strings.Count(diagnostics, "Usage:") != 1 {
			t.Fatalf("arm configuration %v exit=%d diagnostics=%s", arguments, code, diagnostics)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing-runtime.toml")
	_, diagnostics, code := runCLIProcess(t, binary, "deploy", "status", "operation-1", "generation-1", "--config", missing)
	if code != 64 || !strings.Contains(diagnostics, missing) || strings.Contains(diagnostics, "Usage:") {
		t.Fatalf("operation load exit=%d diagnostics=%s", code, diagnostics)
	}
	invalid := filepath.Join(t.TempDir(), "invalid-timeout.toml")
	if err := os.WriteFile(invalid, []byte("[watchdog]\nconnectivity_timeout_seconds = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, code = runCLIProcess(t, binary, "deploy", "status", "operation-1", "generation-1", "--config", invalid)
	if code != 64 || !strings.Contains(diagnostics, "positive watchdog timeouts") || strings.Contains(diagnostics, "Usage:") {
		t.Fatalf("operation timeout exit=%d diagnostics=%s", code, diagnostics)
	}
}

func TestCLIObservationJSONInput(t *testing.T) {
	binary := buildReportCommand(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("healthy"))
	}))
	defer server.Close()
	root := t.TempDir()
	machineID := "cli-observation-machine"
	machinePath := filepath.Join(root, "machine-id")
	if err := os.WriteFile(machinePath, []byte(machineID), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(observation.RuntimeConfig{MachineIDPath: machinePath, ProbeBinary: binary})
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, "runtime.json")
	if err := os.WriteFile(runtimePath, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	spec := observation.CheckSpec{
		ID: "cli-http", Dimension: observation.DimensionDownstreamApplication,
		Operation: observation.OperationHTTP, Observer: observation.Endpoint{Kind: observation.EndpointLocal, MachineID: machineID},
		Family: observation.FamilyIPv4, Source: netip.MustParseAddr("127.0.0.1"), Target: server.URL,
		TimeoutSeconds: 2, MaxAgeSeconds: 10, ExpectedHTTPStatus: []int{http.StatusOK}, ExpectedBody: "healthy",
	}
	request, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, standardInput := range []bool{false, true} {
		arguments := []string{"observe", "--runtime-settings", runtimePath}
		input := ""
		if standardInput {
			input = string(request)
		} else {
			arguments = append(arguments, "--check", string(request))
		}
		output, diagnostics, code := runCLIProcessInput(t, binary, input, arguments...)
		if code != 0 {
			t.Fatalf("observe exit=%d output=%s diagnostics=%s", code, output, diagnostics)
		}
		var result observation.Result
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("result JSON: %v: %s", err, output)
		}
		if result.Outcome != observation.OutcomePass || result.CheckID != spec.ID || result.HTTPStatus != http.StatusOK {
			t.Fatalf("HTTP observation did not pass: %+v", result)
		}
	}
}

func TestCLIConfigPrecedenceAndLegacyFlagValues(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"-example", "-config", "--help"} {
		output, diagnostics, code := runCLIProcess(t, binary, "notify", "-self-test", "-dry-run", "-config", path, "--config", "/absent.toml", "--message", value)
		if code != 0 || !strings.Contains(output, "message       = "+value) {
			t.Fatalf("message=%q exit=%d output=%s diagnostics=%s", value, code, output, diagnostics)
		}
	}
}

func TestCLIDebugPingUsesRequestedInterface(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	iface := "mwan-no-iface"
	output, diagnostics, code := runCLIProcess(t, binary, "debug", "ping4", iface, "--config", path)
	if code != 0 || !strings.Contains(output, "via "+iface) || strings.Count(output, "attempt ") != 6 || strings.Count(output, "3 transmitted, 0 received") != 2 {
		t.Fatalf("debug ping exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
}

func TestCLICheckReleaseConfigurationFailuresUseStdout(t *testing.T) {
	binary := buildReportCommand(t)
	missing := filepath.Join(t.TempDir(), "missing.toml")
	invalid := filepath.Join(t.TempDir(), "invalid-watchdog.toml")
	if err := os.WriteFile(invalid, []byte("[watchdog]\nconnectivity_timeout_seconds = 'invalid'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{missing, invalid} {
		output, diagnostics, code := runCLIProcess(t, binary, "deploy", "check-release", "provider-1", "mwan", "--config", path)
		if code != 1 || !strings.HasPrefix(output, "release configuration rejected: ") || strings.Contains(output, "Usage:") || strings.Contains(diagnostics, "Usage:") || strings.Contains(diagnostics, "mwan deploy check-release:") {
			t.Fatalf("release configuration %s exit=%d output=%s diagnostics=%s", path, code, output, diagnostics)
		}
	}
}
