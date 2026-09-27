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

func TestDownstreamProbeRejectsIncompleteGuestResults(t *testing.T) {
	for name, raw := range map[string]string{
		"missing exit":            `{"exitcode":0,"out-data":"gateway: 10.240.240.3"}`,
		"not exited":              `{"exited":0,"out-data":"gateway: 10.240.240.3"}`,
		"nonzero exit":            `{"exited":1,"exitcode":1}`,
		"missing truncation flag": `{"exited":1,"exitcode":0,"out-data":"gateway: 10.240.240.3"}`,
		"truncated":               `{"exited":1,"exitcode":0,"out-truncated":1,"out-data":"gateway: 10.240.240.3"}`,
		"invalid JSON":            `{"exited":`,
	} {
		if _, err := readGuestProbeResponse([]byte(raw)); err == nil {
			t.Fatalf("%s response passed", name)
		}
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
	response := `{"exited":1,"exitcode":0,"out-truncated":0,"out-data":"gateway: 10.240.240.3\n"}`
	output, err := readGuestProbeResponse([]byte(response))
	if err != nil {
		t.Fatal(err)
	}
	hop, err := readRouteGateway(output)
	if err != nil || hop.String() != "10.240.240.3" {
		t.Fatalf("gateway = %s, error = %v", hop, err)
	}
	if pingReplyPattern.MatchString(strings.ReplaceAll(output, "gateway", "ping")) {
		t.Fatal("a route result passed as a ping reply")
	}
	if !pingReplyPattern.MatchString("1 packets transmitted, 1 packets received, 0.0% packet loss") {
		t.Fatal("the FreeBSD ping summary did not pass")
	}
}
