package main

import (
	"os"
	"path/filepath"
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

func TestDownstreamProbeRejectsIncompleteGuestResults(t *testing.T) {
	if _, err := readGuestProbeResponse(guestResult(1, "")); err == nil {
		t.Fatal("nonzero exit passed")
	}
	for name, output := range map[string]string{
		"missing gateway":   "route to: 1.1.1.1\n",
		"partial gateway":   "gateway: 10.240.240.\n",
		"duplicate gateway": "gateway: 10.240.240.3\ngateway: 10.240.240.3\n",
	} {
		if _, err := readRouteGateway(output); err == nil {
			t.Fatalf("%s route passed", name)
		}
	}
	hop, err := readRouteGateway("gateway: 10.240.240.3\n")
	if err != nil || hop.String() != "10.240.240.3" {
		t.Fatalf("gateway = %s, error = %v", hop, err)
	}
}
