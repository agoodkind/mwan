//go:build linux && firewallnetns

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	startupPolicyChildEnv   = "MWAN_STARTUP_POLICY_TEST_CHILD"
	startupPolicyBinaryEnv  = "MWAN_STARTUP_POLICY_TEST_BINARY"
	startupPolicyWebpassMAC = "02:00:5e:00:53:01"
	// startupPolicyBound is the longest the full firewall policy and the policy
	// routes may take to appear after the daemon starts while provider probes
	// have no answering target.
	startupPolicyBound = 15 * time.Second
	// startupPolicyGiveUp bounds the wait so a slow run still reports its time.
	startupPolicyGiveUp = 60 * time.Second
)

func TestStartupPolicyIgnoresUnansweredProbes(t *testing.T) {
	if os.Getenv(startupPolicyChildEnv) == "1" {
		runStartupPolicyChild(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestStartupPolicyIgnoresUnansweredProbes$", "-test.v")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), startupPolicyChildEnv+"=1", startupPolicyBinaryEnv+"="+binary)
	output, err := child.CombinedOutput()
	t.Logf("isolated startup policy test output: %s", output)
	if err != nil {
		t.Fatalf("The isolated startup policy test failed: %v", err)
	}
}

func runStartupPolicyChild(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte(`"goodkind-mwan-steering:link-files": "rendered"`), []byte(`"goodkind-mwan-steering:link-files": "hand-authored"`))
	fixture = replaceStartupFixture(t, fixture,
		`"goodkind-mwan-steering:link-files": "hand-authored",
        "goodkind-mwan-steering:link": {
          "match": { "driver": "igc" }
        },`,
		`"goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "`+startupPolicyWebpassMAC+`" }
        },`)
	fixture = replaceStartupFixture(t, fixture, `"goodkind-mwan-steering:dhcp": true,
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:napt44",
            "static-mapping"`, `"goodkind-mwan-steering:dhcp": false,
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:napt44",
            "static-mapping"`)
	legacyWAN := `"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`
	typedWAN := `"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "` + runtimeWANMAC + `" }
        }`
	fixture = replaceStartupFixture(t, fixture, legacyWAN, typedWAN)
	attHealth := `"forced-dscp": 8, "health": {"enabled": true, "ping-count": 1, "success-threshold": 1, "failure-threshold": 1, "recovery-threshold": 1, "check-interval": 30, "targets-v4": ["198.51.100.2"], "targets-v6": ["2001:db8:2::2"]}`
	fixture = replaceStartupFixture(t, fixture, `"forced-dscp": 8`, attHealth)
	fixture = replaceStartupFixture(t, fixture, `["192.0.2.10", "192.0.2.11"]`, `["10.98.0.2", "10.98.0.3"]`)
	fixture = replaceStartupFixture(t, fixture, `["2001:db8:53::1", "2001:db8:53::2"]`, `["2001:db8:98::2", "2001:db8:98::3"]`)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n",
		filepath.Join(stateDir, "owned-links.json"), filepath.Join(stateDir, "owned-static.json"), filepath.Join(stateDir, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01::1/64", "2001:db8:b01:fe::1/64"}, []string{"192.0.2.2/29", "2001:db8:b01::2/64", "2001:db8:b01:fe::2/64"}, "")
	// The webpass peer has no addresses, so none of its probe targets answer
	// and every probe waits for its timeout. The att peer answers.
	webpass := newRuntimePeer(t, gateway, "enwebpass0", "webpass-host", []string{"10.98.0.1/24", "2001:db8:98::1/64"}, nil, startupPolicyWebpassMAC)
	wan := newRuntimePeer(t, gateway, "enatt0", "wan-host", []string{"198.51.100.1/24", "2001:db8:2::1/64"}, []string{"198.51.100.2/24", "2001:db8:2::2/64"}, runtimeWANMAC)
	addRuntimeDefault(t, "enatt0", "198.51.100.2")
	defer management.namespace.Close()
	defer lan.namespace.Close()
	defer webpass.namespace.Close()
	defer wan.namespace.Close()
	setRuntimeNamespace(t, gateway)

	started := time.Now()
	daemon := startRuntimeDaemon(t, os.Getenv(startupPolicyBinaryEnv), configPath, root, "startup-policy")
	defer stopRuntimeDaemon(t, daemon)
	var missing []string
	for time.Since(started) < startupPolicyGiveUp {
		missing = missingStartupPolicy(t)
		if len(missing) == 0 {
			break
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(100 * time.Millisecond)
	}
	elapsed := time.Since(started)
	t.Logf("full policy complete after %s; still missing: %v", elapsed, missing)
	if len(missing) != 0 || elapsed > startupPolicyBound {
		t.Fatalf("The full firewall policy and policy routes took %s with bound %s; missing %v",
			elapsed, startupPolicyBound, missing)
	}
}

func replaceStartupFixture(t *testing.T, fixture []byte, old, replacement string) []byte {
	t.Helper()
	if !bytes.Contains(fixture, []byte(old)) {
		t.Fatalf("test fixture lacks %q", old)
	}
	return bytes.ReplaceAll(fixture, []byte(old), []byte(replacement))
}

// missingStartupPolicy lists the parts of the full policy the kernel lacks:
// the static-mapping DNAT rule, the transit accept rule toward a provider,
// and the provider policy rule.
func missingStartupPolicy(t *testing.T) []string {
	t.Helper()
	var missing []string
	nat, _ := exec.Command("nft", "list", "table", "ip", "nat").CombinedOutput()
	if !strings.Contains(string(nat), "dnat to 192.0.2.2") {
		missing = append(missing, "dnat to 192.0.2.2")
	}
	filter, _ := exec.Command("nft", "list", "table", "inet", "filter").CombinedOutput()
	if !strings.Contains(string(filter), `oifname "enatt0"`) {
		missing = append(missing, `forward accept oifname "enatt0"`)
	}
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rule := range rules {
		if rule.Priority == 100 && rule.Table == 100 && rule.Mark == 1 {
			found = true
		}
	}
	if !found {
		missing = append(missing, "policy rule priority 100 table 100 mark 1")
	}
	return missing
}
