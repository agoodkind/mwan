package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

const downstreamOldBootID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func runDownstreamCommand(t *testing.T, binary, toolDir, configPath string, arguments ...string) (string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, arguments...)
	command.Env = append(os.Environ(), "PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"), "MWAN_CONFIG="+configPath)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run downstream command: %v: %s", err, output)
	}
	return string(output), exitError.ExitCode()
}

func writeDownstreamFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func installFakeHypervisorTool(t *testing.T, name, script string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return directory
}

const testDownstreamProbeJSON = `{
  "opnsense_vmid": 201,
  "ipv4_source": "10.240.240.2",
  "ipv4_next_hop": "10.240.240.3",
  "ipv6_source": "3d06:bad:b01:201::2",
  "ipv6_next_hop": "3d06:bad:b01:201::3"
}`

func TestDeployGateRequiresDownstreamProbeConfig(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(path, []byte(testDownstreamProbeJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"check-egress", "ipv4,ipv6"},
		{"check-egress", "ipv4,ipv6", path + ".missing"},
		{
			"wait-deploy", "113", downstreamOldBootID, "180", "180", "trace-123",
			filepath.Join(t.TempDir(), "verdict.json"), "ipv4,ipv6", "3",
		},
	} {
		_, code := runDownstreamCommand(t, binary, t.TempDir(), "", append([]string{"deploy-gate"}, args...)...)
		if code != 64 {
			t.Fatalf("runDeployGate(%v) = %d, want usage error", args, code)
		}
	}
	missingConfig := filepath.Join(t.TempDir(), "absent.toml")
	output, code := runDownstreamCommand(t, binary, t.TempDir(), missingConfig,
		"deploy-gate", "wait-deploy", "113", downstreamOldBootID, "180", "180", "trace-123",
		filepath.Join(t.TempDir(), "verdict.json"), "ipv4,ipv6", "3", path,
		"--config", missingConfig)
	if code != 1 || !strings.Contains(output, missingConfig) || strings.Contains(output, "Usage:") {
		t.Fatalf("valid wait-deploy arguments failed before configuration loading: code=%d output=%s", code, output)
	}
}

func TestDeployGateAcceptsSingleFamilyProbeConfigs(t *testing.T) {
	binary := buildReportCommand(t)
	for _, testCase := range []struct {
		name     string
		family   string
		contents string
	}{
		{
			name:     "ipv4",
			family:   "ipv4",
			contents: `{"opnsense_vmid":201,"ipv4_source":"10.240.240.2","ipv4_next_hop":"10.240.240.3"}`,
		},
		{
			name:     "ipv6",
			family:   "ipv6",
			contents: `{"opnsense_vmid":201,"ipv6_source":"3d06:bad:b01:201::2","ipv6_next_hop":"3d06:bad:b01:201::3"}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probe.json")
			if err := os.WriteFile(path, []byte(testCase.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, code := runDownstreamCommand(t, binary, t.TempDir(), "", "deploy-gate", "check-egress", testCase.family, path)
			if code == 64 {
				t.Fatalf("%s-only config was rejected as invalid", testCase.family)
			}
			_, code = runDownstreamCommand(t, binary, t.TempDir(), "", "deploy-gate", "check-egress", "ipv4,ipv6", path)
			if code != 64 {
				t.Fatalf("both-family check accepted %s-only config: code=%d", testCase.family, code)
			}
			missingConfig := filepath.Join(t.TempDir(), "absent.toml")
			output, code := runDownstreamCommand(t, binary, t.TempDir(), missingConfig,
				"deploy-gate", "wait-deploy", "113", downstreamOldBootID, "180", "180", "trace-123",
				filepath.Join(t.TempDir(), "verdict.json"), testCase.family, "3", path,
				"--config", missingConfig)
			if code != 1 || !strings.Contains(output, missingConfig) || strings.Contains(output, "Usage:") {
				t.Fatalf("wait-deploy rejected %s-only config before configuration loading: code=%d output=%s", testCase.family, code, output)
			}
		})
	}
}

func TestDeployGateCheckEgressJudgesTheGuestReply(t *testing.T) {
	binaryPath := buildReportCommand(t)
	probePath := writeDownstreamFile(t, "probe.json", testDownstreamProbeJSON)
	configPath := writeDownstreamFile(t, "config.toml", "")
	const routeOK = `{"exitcode":0,"exited":1,"out-truncated":0,` +
		`"out-data":"   route to: 1.1.1.1\n    gateway: 10.240.240.3\n"}`
	const curlOK = `{"exitcode":0,"exited":1,"out-data":"200","out-truncated":0}`
	cases := []struct {
		name       string
		route      string
		curl       string
		wantExit   int
		wantOutput string
	}{
		{
			name: "both replies complete", route: routeOK, curl: curlOK,
			wantExit: 0, wantOutput: "ipv4=yes",
		},
		{
			name: "empty stdout without a truncation flag", route: routeOK,
			curl:     `{"exitcode":0,"exited":1}`,
			wantExit: 0, wantOutput: "ipv4=yes",
		},
		{
			name: "https probe exits non-zero", route: routeOK,
			curl:     `{"exitcode":22,"exited":1}`,
			wantExit: 1, wantOutput: "guest command exited 22",
		},
		{
			name: "route probe exits non-zero", route: `{"exitcode":1,"exited":1}`, curl: curlOK,
			wantExit: 1, wantOutput: "guest command exited 1",
		},
		{
			name: "https probe has not exited", route: routeOK,
			curl:     `{"exited":0,"pid":4242}`,
			wantExit: 1, wantOutput: "did not exit within",
		},
		{
			name:     "route reply lacks the exited flag",
			route:    `{"exitcode":0,"out-data":"gateway: 10.240.240.3\n"}`,
			curl:     curlOK,
			wantExit: 1, wantOutput: "did not exit within",
		},
		{
			name: "https reply lacks an exit code", route: routeOK,
			curl:     `{"exited":1,"out-data":"200","out-truncated":0}`,
			wantExit: 1, wantOutput: "without an exit code",
		},
		{
			name: "https reply is truncated", route: routeOK,
			curl:     `{"exitcode":0,"exited":1,"out-truncated":1,"out-data":"200"}`,
			wantExit: 1, wantOutput: "truncated",
		},
		{
			name: "https reply is not JSON", route: routeOK, curl: `{"exited":`,
			wantExit: 1, wantOutput: "parse qm guest exec output",
		},
		{
			name:     "route output has no gateway line",
			route:    `{"exitcode":0,"exited":1,"out-truncated":0,"out-data":"route to: 1.1.1.1\n"}`,
			curl:     curlOK,
			wantExit: 1, wantOutput: "route output has no gateway",
		},
		{
			name:     "route output has a partial gateway",
			route:    `{"exitcode":0,"exited":1,"out-truncated":0,"out-data":"gateway: 10.240.240.\n"}`,
			curl:     curlOK,
			wantExit: 1, wantOutput: "route output gateway",
		},
		{
			name: "route output repeats the gateway line",
			route: `{"exitcode":0,"exited":1,"out-truncated":0,` +
				`"out-data":"gateway: 10.240.240.3\ngateway: 10.240.240.3\n"}`,
			curl:     curlOK,
			wantExit: 1, wantOutput: "invalid gateway line",
		},
		{
			name:     "route output names another next hop",
			route:    `{"exitcode":0,"exited":1,"out-truncated":0,"out-data":"gateway: 10.240.240.9\n"}`,
			curl:     curlOK,
			wantExit: 1, wantOutput: "expected 10.240.240.3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			routePath := writeDownstreamFile(t, "route.reply", tc.route)
			curlPath := writeDownstreamFile(t, "curl.reply", tc.curl)
			fixture, err := template.ParseFiles(filepath.Join("testdata", "deploygate-guest-reply.sh"))
			if err != nil {
				t.Fatal(err)
			}
			paths := struct{ RoutePath, CurlPath string }{
				RoutePath: strings.ReplaceAll(routePath, "'", "'\\''"),
				CurlPath:  strings.ReplaceAll(curlPath, "'", "'\\''"),
			}
			var script strings.Builder
			if err := fixture.Execute(&script, paths); err != nil {
				t.Fatal(err)
			}
			toolDir := installFakeHypervisorTool(t, "qm", script.String())

			output, exitCode := runDownstreamCommand(t, binaryPath, toolDir, configPath,
				"deploy-gate", "check-egress", "ipv4", probePath)

			if exitCode != tc.wantExit {
				t.Fatalf("exit code = %d, want %d\noutput: %s", exitCode, tc.wantExit, output)
			}
			if !strings.Contains(output, tc.wantOutput) {
				t.Fatalf("output does not contain %q: %s", tc.wantOutput, output)
			}
		})
	}
}
