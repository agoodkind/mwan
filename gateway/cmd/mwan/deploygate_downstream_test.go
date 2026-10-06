package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDownstreamProbeJSON = `{
  "opnsense_vmid": 201,
  "ipv4_source": "10.240.240.2",
  "ipv4_next_hop": "10.240.240.3",
  "ipv6_source": "3d06:bad:b01:201::2",
  "ipv6_next_hop": "3d06:bad:b01:201::3"
}`

func TestDeployGateRequiresDownstreamProbeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(path, []byte(testDownstreamProbeJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"check-egress", "ipv4,ipv6"},
		{"check-egress", "ipv4,ipv6", path + ".missing"},
		{
			"wait-deploy", "113", testOldBootID, "180", "180", "trace-123",
			filepath.Join(t.TempDir(), "verdict.json"), "ipv4,ipv6", "3",
		},
	} {
		if code := runDeployGate(args); code != exitDeployGateUsage {
			t.Fatalf("runDeployGate(%v) = %d, want usage error", args, code)
		}
	}
	inputs, ok := parseWaitDeployArgs([]string{
		"113", testOldBootID, "180", "180", "trace-123",
		filepath.Join(t.TempDir(), "verdict.json"), "ipv4,ipv6", "3", path,
	})
	if !ok || inputs.probe.opnsenseVMID != 201 || inputs.rounds != 3 {
		t.Fatalf("wait-deploy inputs = %+v, valid=%t", inputs, ok)
	}
}

func TestDeployGateAcceptsSingleFamilyProbeConfigs(t *testing.T) {
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
			if code := runDeployGate([]string{"check-egress", testCase.family, path}); code == exitDeployGateUsage {
				t.Fatalf("%s-only config was rejected as invalid", testCase.family)
			}
			if code := runDeployGate([]string{"check-egress", "ipv4,ipv6", path}); code != exitDeployGateUsage {
				t.Fatalf("both-family check accepted %s-only config: code=%d", testCase.family, code)
			}
			_, ok := parseWaitDeployArgs([]string{
				"113", testOldBootID, "180", "180", "trace-123",
				filepath.Join(t.TempDir(), "verdict.json"), testCase.family, "3", path,
			})
			if !ok {
				t.Fatalf("wait-deploy rejected %s-only config", testCase.family)
			}
		})
	}
}

func TestDeployGateCheckEgressJudgesTheGuestReply(t *testing.T) {
	binaryPath := buildMwanBinary(t)
	probePath := writeTestFile(t, "probe.json", testDownstreamProbeJSON)
	configPath := writeTestFile(t, "config.toml", "")
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
			wantExit: exitDeployGateOK, wantOutput: "ipv4=yes",
		},
		{
			name: "empty stdout without a truncation flag", route: routeOK,
			curl:     `{"exitcode":0,"exited":1}`,
			wantExit: exitDeployGateOK, wantOutput: "ipv4=yes",
		},
		{
			name: "https probe exits non-zero", route: routeOK,
			curl:     `{"exitcode":22,"exited":1}`,
			wantExit: exitDeployGateFailed, wantOutput: "guest command exited 22",
		},
		{
			name: "route probe exits non-zero", route: `{"exitcode":1,"exited":1}`, curl: curlOK,
			wantExit: exitDeployGateFailed, wantOutput: "guest command exited 1",
		},
		{
			name: "https probe has not exited", route: routeOK,
			curl:     `{"exited":0,"pid":4242}`,
			wantExit: exitDeployGateFailed, wantOutput: "did not exit within",
		},
		{
			name:     "route reply lacks the exited flag",
			route:    `{"exitcode":0,"out-data":"gateway: 10.240.240.3\n"}`,
			curl:     curlOK,
			wantExit: exitDeployGateFailed, wantOutput: "did not exit within",
		},
		{
			name: "https reply lacks an exit code", route: routeOK,
			curl:     `{"exited":1,"out-data":"200","out-truncated":0}`,
			wantExit: exitDeployGateFailed, wantOutput: "without an exit code",
		},
		{
			name: "https reply is truncated", route: routeOK,
			curl:     `{"exitcode":0,"exited":1,"out-truncated":1,"out-data":"200"}`,
			wantExit: exitDeployGateFailed, wantOutput: "truncated",
		},
		{
			name: "https reply is not JSON", route: routeOK, curl: `{"exited":`,
			wantExit: exitDeployGateFailed, wantOutput: "parse qm guest exec output",
		},
		{
			name:     "route output has no gateway line",
			route:    `{"exitcode":0,"exited":1,"out-truncated":0,"out-data":"route to: 1.1.1.1\n"}`,
			curl:     curlOK,
			wantExit: exitDeployGateFailed, wantOutput: "route output has no gateway",
		},
		{
			name:     "route output has a partial gateway",
			route:    `{"exitcode":0,"exited":1,"out-truncated":0,"out-data":"gateway: 10.240.240.\n"}`,
			curl:     curlOK,
			wantExit: exitDeployGateFailed, wantOutput: "route output gateway",
		},
		{
			name: "route output repeats the gateway line",
			route: `{"exitcode":0,"exited":1,"out-truncated":0,` +
				`"out-data":"gateway: 10.240.240.3\ngateway: 10.240.240.3\n"}`,
			curl:     curlOK,
			wantExit: exitDeployGateFailed, wantOutput: "invalid gateway line",
		},
		{
			name:     "route output names another next hop",
			route:    `{"exitcode":0,"exited":1,"out-truncated":0,"out-data":"gateway: 10.240.240.9\n"}`,
			curl:     curlOK,
			wantExit: exitDeployGateFailed, wantOutput: "expected 10.240.240.3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			routePath := writeTestFile(t, "route.reply", tc.route)
			curlPath := writeTestFile(t, "curl.reply", tc.curl)
			script := "#!/bin/sh\ncase \"$*\" in\n*/sbin/route*) cat '" + routePath +
				"';;\n*curl*) cat '" + curlPath + "';;\nesac\n"
			toolDir := installFakeHypervisorTool(t, "qm", script)

			output, exitCode := runMwanBinary(t, binaryPath, toolDir, configPath,
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
