//go:build linux && firewallnetns

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/networkload"
)

const (
	forcedDSCPReadbackChildEnv  = "MWAN_FORCED_DSCP_READBACK_TEST_CHILD"
	forcedDSCPReadbackBinaryEnv = "MWAN_FORCED_DSCP_READBACK_TEST_BINARY"
	forcedDSCPFixtureLeaf       = `"forced-dscp": 8`
	// Named and unnamed DSCP values need coverage because nft prints them differently.
	forcedDSCPUnnamed = 35
	forcedDSCPNamed   = 8
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

func TestCheckFirewallForcedDSCPReadback(t *testing.T) {
	if os.Getenv(forcedDSCPReadbackChildEnv) == "1" {
		runForcedDSCPMismatchChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace test requires root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public command: %v: %s", err, output)
	}
	schema := filepath.Join("..", "..", "internal", "yangpub", "schema")
	for _, dscp := range []int{forcedDSCPUnnamed, forcedDSCPNamed} {
		fixture := forcedDSCPFixture(t, dscp)
		output, err := exec.Command(binary, "deploy-gate", "check-firewall", fixture, schema).CombinedOutput()
		if err != nil {
			t.Errorf("public firewall check rejected forced-dscp %d: %v: %s", dscp, err, output)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCheckFirewallForcedDSCPReadback$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), forcedDSCPReadbackChildEnv+"=1", forcedDSCPReadbackBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the isolated forced DSCP mismatch test failed: %v: %s", err, output)
	}
}

// The private network namespace prevents this check from changing host rules.
func runForcedDSCPMismatchChild(t *testing.T) {
	schema := filepath.Join("..", "..", "internal", "yangpub", "schema")
	loaded, err := networkload.Load(forcedDSCPFixture(t, forcedDSCPUnnamed), schema)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := firewall.Compile(loaded.Firewall)
	if err != nil {
		t.Fatal(err)
	}
	if err := firewall.Apply(t.Context(), applied); err != nil {
		t.Fatal(err)
	}
	expected := forcedDSCPFixture(t, forcedDSCPNamed)
	binary := os.Getenv(forcedDSCPReadbackBinaryEnv)
	output, err := exec.Command(binary, "deploy-gate", "inspect-firewall", expected, schema).CombinedOutput()
	if err == nil {
		t.Fatalf("firewall readback accepted applied forced-dscp %d against expected %d: %s", forcedDSCPUnnamed, forcedDSCPNamed, output)
	}
	if !strings.Contains(string(output), "differs") {
		t.Fatalf("firewall readback failed for a reason other than the DSCP rule: %v: %s", err, output)
	}
}

func forcedDSCPFixture(t *testing.T, dscp int) string {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fixture, []byte(forcedDSCPFixtureLeaf)) {
		t.Fatal("test fixture has no forced-dscp leaf")
	}
	leaf := `"forced-dscp": ` + strconv.Itoa(dscp)
	fixture = bytes.Replace(fixture, []byte(forcedDSCPFixtureLeaf), []byte(leaf), 1)
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckFirewallIsolatedKernelLXC(t *testing.T) {
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
	fixture := filepath.Join("..", "..", "yang", "instances", "network-lxc.json")
	schema := filepath.Join("..", "..", "internal", "yangpub", "schema")
	output, err := exec.Command(binary, "deploy-gate", "check-firewall", fixture, schema).CombinedOutput()
	if err != nil {
		t.Fatalf("public firewall check: %v: %s", err, output)
	}
	readback := string(output) + " "
	if !strings.Contains(readback, "inet filter input") {
		t.Errorf("readback omits the input chain: %s", output)
	}
	for _, port := range []string{"dport 22 ", "dport 50052 "} {
		if strings.Contains(readback, port) {
			t.Errorf("readback accepts a management port %q: %s", port, output)
		}
	}
	for _, port := range []string{"dport 179 ", "dport 3784 ", "dport 3785 "} {
		if !strings.Contains(readback, port) {
			t.Errorf("readback omits the routing session port %q: %s", port, output)
		}
	}
	after, err := exec.Command("nft", "-j", "list", "ruleset").Output()
	if err != nil {
		t.Fatalf("read caller rules after check: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("public firewall check changed caller nftables rules")
	}
}
