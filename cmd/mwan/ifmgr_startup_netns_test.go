//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	startupChildEnv  = "MWAN_STARTUP_TEST_CHILD"
	startupBinaryEnv = "MWAN_STARTUP_TEST_BINARY"
)

func TestWANStartupProtectsBeforeConfigValidation(t *testing.T) {
	if os.Getenv(startupChildEnv) == "1" {
		runWANStartupChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWANStartupProtectsBeforeConfigValidation$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), startupChildEnv+"=1", startupBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated WAN startup test: %v: %s", err, output)
	}
}

func runWANStartupChild(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatalf("make mount namespace private: %v", err)
	}
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	networkPath := filepath.Join(networkDir, "network.json")
	if err := os.WriteFile(networkPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "invalid.toml")
	if err := os.WriteFile(configPath, []byte("[bgp]\ndynamic_neighbors = [\"not-a-prefix\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MWAN_CONFIG", configPath)

	binary := os.Getenv(startupBinaryEnv)
	output, err := exec.Command(binary, "ifmgr", "--role", "wan").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "dynamic_neighbors") {
		t.Fatalf("invalid TOML did not fail after baseline installation: %v: %s", err, output)
	}
	input, err := exec.Command("nft", "list", "chain", "inet", "filter", "input").CombinedOutput()
	if err != nil {
		t.Fatalf("read protective input chain: %v: %s", err, input)
	}
	if !bytes.Contains(input, []byte("policy drop")) || !bytes.Contains(input, []byte("enmgmt0")) || !bytes.Contains(input, []byte("dport 22")) {
		t.Fatalf("protective management policy missing: %s", input)
	}
	entries, err := os.ReadDir(networkdDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("network files were written before full validation: %v", entries)
	}
	before := startupRuleset(t)
	invalid := bytes.Replace(fixture, []byte(`"port": 22`), []byte(`"port": 0`), 1)
	if bytes.Equal(invalid, fixture) {
		t.Fatal("invalid baseline mutation did not match the fixture")
	}
	if err := os.WriteFile(networkPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(binary, "ifmgr", "--role", "wan").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid management service") {
		t.Fatalf("invalid baseline did not stop startup: %v: %s", err, output)
	}
	after := startupRuleset(t)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid baseline replaced the prior kernel rules")
	}
	missing := fixtureWithoutFirewall(t, fixture)
	if err := os.WriteFile(networkPath, missing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[ifmgr]\nrole = \"wan\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before = startupRuleset(t)
	commandContext, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	output, err = exec.CommandContext(commandContext, binary, "ifmgr", "--role", "wan").CombinedOutput()
	if commandContext.Err() != nil {
		t.Fatalf("The WAN startup command timed out without a firewall policy: %s", output)
	}
	if err == nil || !strings.Contains(string(output), "WAN firewall policy is absent") {
		t.Fatalf("The WAN daemon did not reject the absent firewall policy: %v: %s", err, output)
	}
	after = startupRuleset(t)
	if !bytes.Equal(before, after) {
		t.Fatal("WAN startup changed kernel rules despite the absent firewall policy")
	}
	entries, err = os.ReadDir(networkdDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("WAN startup wrote network files despite the absent firewall policy: %v", entries)
	}
}

func fixtureWithoutFirewall(t *testing.T, fixture []byte) []byte {
	t.Helper()
	decode := func(raw []byte) map[string]json.RawMessage {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	document := decode(fixture)
	interfaces := decode(document["ietf-interfaces:interfaces"])
	group := decode(interfaces["goodkind-mwan-steering:steering-group"])
	if _, exists := group["firewall"]; !exists {
		t.Fatal("the fixture has no firewall section")
	}
	delete(group, "firewall")
	groupJSON, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	interfaces["goodkind-mwan-steering:steering-group"] = groupJSON
	interfacesJSON, err := json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"] = interfacesJSON
	result, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func bindStartupDirectory(t *testing.T, source string, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind %s to %s: %v", source, target, err)
	}
}

func startupRuleset(t *testing.T) []byte {
	t.Helper()
	output, err := exec.Command("nft", "-j", "list", "ruleset").CombinedOutput()
	if err != nil {
		t.Fatalf("read nftables ruleset: %v: %s", err, output)
	}
	return output
}
