//go:build linux && firewallnetns

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The command must apply the actual nftables policy in a private namespace.
// The caller's namespace must have exactly the same rules afterward.
func TestCheckFirewallIsolatedKernel(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("network namespace test requires root")
	}
	before, err := exec.Command("nft", "-j", "list", "ruleset").Output()
	if err != nil {
		t.Fatalf("read caller rules: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public command: %v: %s", err, output)
	}
	fixture := filepath.Join("..", "..", "yang", "instances", "network-min.json")
	schema := filepath.Join("..", "..", "internal", "yangpub", "schema")
	check := exec.Command(binary, "deploy-gate", "check-firewall", fixture, schema)
	output, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("public firewall check: %v: %s", err, output)
	}
	for _, expected := range []string{"inet filter input", "ip nat postrouting", "inet mangle prerouting", "att_pinned_v4"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("readback omits %q: %s", expected, output)
		}
	}
	after, err := exec.Command("nft", "-j", "list", "ruleset").Output()
	if err != nil {
		t.Fatalf("read caller rules after check: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("public firewall check changed caller nftables rules")
	}
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"ietf-interfaces:interfaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	badCheck := exec.Command(binary, "deploy-gate", "check-firewall", invalid, schema)
	if output, err := badCheck.CombinedOutput(); err == nil {
		t.Fatalf("invalid document was accepted: %s", output)
	}
	for _, mode := range []string{"check-firewall", "inspect-firewall"} {
		command := exec.Command(binary, "deploy-gate", mode)
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("%s accepted missing arguments: %s", mode, output)
		}
		failure, ok := err.(*exec.ExitError)
		if !ok || failure.ExitCode() != exitDeployGateUsage {
			t.Fatalf("%s returned %v for missing arguments: %s", mode, err, output)
		}
	}
}
