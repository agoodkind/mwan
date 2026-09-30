//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net"
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
	kernelPolicyChildEnv  = "MWAN_KERNEL_POLICY_TEST_CHILD"
	kernelPolicyBinaryEnv = "MWAN_KERNEL_POLICY_TEST_BINARY"
)

func TestKernelPolicyDaemonRuntime(t *testing.T) {
	if os.Getenv(kernelPolicyChildEnv) == "1" {
		runKernelPolicyDaemonRuntime(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestKernelPolicyDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), kernelPolicyChildEnv+"=1", kernelPolicyBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated kernel policy daemon: %v: %s", err, output)
	}
}

func runKernelPolicyDaemonRuntime(t *testing.T) {
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
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	source := newRuntimePeer(t, gateway, "ownphys0", "owned-peer", nil, nil, ownedRuntimeParentMAC)
	defer source.namespace.Close()
	setRuntimeNamespace(t, source.namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer521", ParentIndex: parent.Attrs().Index}, VlanId: 521}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "peer521", []string{"10.52.1.2/24"})
	addRuntimeRoute(t, gateway, source.namespace, "192.0.2.0/29", "10.52.1.1")
	setRuntimeNamespace(t, gateway)
	binary := os.Getenv(kernelPolicyBinaryEnv)
	writeKernelPolicyNetwork(t, networkDir, nil, false)
	assertKernelPolicyStartupValidation(t, binary, networkDir, networkdDir, root, config)
	baseline := startRuntimeDaemon(t, binary, configPath, root, "kernel-baseline")
	defer killOwnedRuntimeDaemon(t, baseline)
	waitStaticRuntimeAddress(t, baseline, "enatt0", "10.52.1.1/24", true)
	killOwnedRuntimeDaemon(t, baseline)
	setKernelPolicyValue(t, "ipv4", "enatt0", "forwarding", "0")
	setKernelPolicyValue(t, "ipv4", "enmgmt0", "forwarding", "1")
	metric := readKernelPolicyValue(t, "ipv6", "enatt0", "ra_defrtr_metric")
	originalIPv6 := make(map[string]string)
	for _, leaf := range []string{"forwarding", "autoconf", "accept_ra_defrtr", "accept_ra"} {
		originalIPv6[leaf] = readKernelPolicyValue(t, "ipv6", "enatt0", leaf)
	}
	writeKernelPolicyNetwork(t, networkDir, new(false), true)
	first := startRuntimeDaemon(t, binary, configPath, root, "kernel-disabled")
	defer killOwnedRuntimeDaemon(t, first)
	waitKernelPolicyValue(t, first, "ipv6", "enatt0", "ra_defrtr_metric", "777")
	waitStaticRuntimeAddress(t, first, "enatt0", "10.52.1.1/24", true)
	waitKernelPolicyFirewall(t, first)
	assertKernelPolicyPacket(t, gateway, source.namespace, lan.namespace, false)
	killOwnedRuntimeDaemon(t, first)
	writeKernelPolicyNetwork(t, networkDir, new(true), true)
	second := startRuntimeDaemon(t, binary, configPath, root, "kernel-enabled")
	defer killOwnedRuntimeDaemon(t, second)
	waitKernelPolicyValue(t, second, "ipv4", "enatt0", "forwarding", "1")
	waitStaticRuntimeAddress(t, second, "enatt0", "10.52.1.1/24", true)
	waitKernelPolicyFirewall(t, second)
	assertKernelPolicyPacket(t, gateway, source.namespace, lan.namespace, true)
	killOwnedRuntimeDaemon(t, second)
	if got := readKernelPolicyValue(t, "ipv4", "enatt0", "forwarding"); got != "1" {
		t.Fatalf("daemon shutdown changed forwarding to %s", got)
	}
	third := startRuntimeDaemon(t, binary, configPath, root, "kernel-restart")
	defer killOwnedRuntimeDaemon(t, third)
	waitKernelPolicyValue(t, third, "ipv4", "enatt0", "forwarding", "1")
	waitStaticRuntimeAddress(t, third, "enatt0", "10.52.1.1/24", true)
	waitKernelPolicyFirewall(t, third)
	assertKernelPolicyPacket(t, gateway, source.namespace, lan.namespace, true)
	killOwnedRuntimeDaemon(t, third)
	writeKernelPolicyNetwork(t, networkDir, new(false), true)
	disabled := startRuntimeDaemon(t, binary, configPath, root, "kernel-explicit-false")
	defer killOwnedRuntimeDaemon(t, disabled)
	waitKernelPolicyValue(t, disabled, "ipv4", "enatt0", "forwarding", "0")
	waitStaticRuntimeAddress(t, disabled, "enatt0", "10.52.1.1/24", true)
	waitKernelPolicyFirewall(t, disabled)
	assertKernelPolicyPacket(t, gateway, source.namespace, lan.namespace, false)
	killOwnedRuntimeDaemon(t, disabled)
	writeKernelPolicyNetwork(t, networkDir, new(true), true)
	reenabled := startRuntimeDaemon(t, binary, configPath, root, "kernel-reenabled")
	defer killOwnedRuntimeDaemon(t, reenabled)
	waitKernelPolicyValue(t, reenabled, "ipv4", "enatt0", "forwarding", "1")
	waitStaticRuntimeAddress(t, reenabled, "enatt0", "10.52.1.1/24", true)
	waitKernelPolicyFirewall(t, reenabled)
	assertKernelPolicyPacket(t, gateway, source.namespace, lan.namespace, true)
	killOwnedRuntimeDaemon(t, reenabled)
	setKernelPolicyValue(t, "ipv6", "enatt0", "ra_defrtr_metric", "888")
	writeKernelPolicyNetwork(t, networkDir, nil, false)
	fourth := startRuntimeDaemon(t, binary, configPath, root, "kernel-removed")
	defer killOwnedRuntimeDaemon(t, fourth)
	waitKernelPolicyValue(t, fourth, "ipv4", "enatt0", "forwarding", "0")
	waitStaticRuntimeAddress(t, fourth, "enatt0", "10.52.1.1/24", true)
	waitStaticRuntimeLog(t, fourth, "changed outside MWAN")
	waitKernelPolicyFirewall(t, fourth)
	assertKernelPolicyPacket(t, gateway, source.namespace, lan.namespace, false)
	for leaf, value := range originalIPv6 {
		waitKernelPolicyValue(t, fourth, "ipv6", "enatt0", leaf, value)
	}
	if got := readKernelPolicyValue(t, "ipv6", "enatt0", "ra_defrtr_metric"); got != "888" {
		t.Fatalf("removal overwrote external metric: got %s, original %s", got, metric)
	}
	if got := readKernelPolicyValue(t, "ipv4", "enmgmt0", "forwarding"); got != "1" {
		t.Fatalf("unrelated management forwarding changed to %s", got)
	}
	killOwnedRuntimeDaemon(t, fourth)
	assertKernelPolicyCleanupOnly(t, binary, networkDir, configPath, root)
}

func assertKernelPolicyCleanupOnly(t *testing.T, binary, networkDir, configPath, root string) {
	t.Helper()
	journalPath := filepath.Join(root, "kernel-policy.json")
	if kernelPolicyFieldCount(t, journalPath) == 0 {
		t.Fatal("cleanup control requires the journal retained after an external metric change")
	}
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	writeOwnedRuntimeNetwork(t, networkDir, false, false)
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"200ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n", journalPath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startRuntimeDaemon(t, binary, configPath, root, "kernel-cleanup-only")
	defer killOwnedRuntimeDaemon(t, daemon)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		assertRuntimeDaemonRunning(t, daemon)
		if kernelPolicyFieldCount(t, journalPath) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cleanup-only daemon retained kernel journal fields: %s", runtimeLogTail(t, daemon, 40))
}

func kernelPolicyFieldCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Fields []json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	return len(journal.Fields)
}

func assertKernelPolicyStartupValidation(t *testing.T, binary, networkDir, networkdDir, root, config string) {
	t.Helper()
	networkPath := filepath.Join(networkDir, "network.json")
	original, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(original, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	entries = append(entries, json.RawMessage(`{"name":"legacy-policy","type":"iana-if-type:ethernetCsmacd","goodkind-mwan-steering:owner":"networkd","goodkind-mwan-steering:link-files":"rendered","goodkind-mwan-steering:link":{"match":{"hardware-address":"02:00:5e:00:53:ab"}},"ietf-ip:ipv4":{"address":[{"ip":"203.0.113.9","prefix-length":24}]}}`))
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(networkPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(root, "malformed-kernel-policy.json")
	if err := os.WriteFile(malformed, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ path, reason string }{
		{path: "relative-policy.json", reason: "absolute journal path"},
		{path: malformed, reason: "decode kernel policy journal"},
	} {
		invalidPath := filepath.Join(root, "invalid-kernel-config.toml")
		invalid := strings.Replace(config, fmt.Sprintf("%q", filepath.Join(root, "kernel-policy.json")), fmt.Sprintf("%q", check.path), 1)
		if err := os.WriteFile(invalidPath, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "ifmgr", "--role", "wan")
		command.Env = append(os.Environ(), "MWAN_CONFIG="+invalidPath)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), check.reason) {
			t.Fatalf("invalid kernel journal %s: %v: %s", check.path, err, output)
		}
		files, err := os.ReadDir(networkdDir)
		if err != nil || len(files) != 0 {
			t.Fatalf("invalid kernel journal wrote networkd files: %v: %v", files, err)
		}
	}
	if err := os.WriteFile(networkPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeKernelPolicyNetwork(t *testing.T, directory string, forwarding *bool, ipv6 bool) {
	t.Helper()
	writeOwnedRuntimeNetwork(t, directory, false, false)
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	var retained []map[string]json.RawMessage
	for _, entry := range entries {
		if string(entry["name"]) == `"enatt0"` {
			entry["enabled"] = json.RawMessage("true")
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"mwan"`)
			entry["goodkind-mwan-steering:connection-id"] = json.RawMessage(`"att"`)
			entry["goodkind-mwan-steering:link"] = json.RawMessage(`{"vlan":{"parent":"ownphys0","id":521}}`)
			entry["ietf-ip:ipv4"] = json.RawMessage(`{"address":[{"ip":"10.52.1.1","prefix-length":24}],"goodkind-mwan-steering:gateway":"10.52.1.2","goodkind-mwan-steering:translation":{"mode":"native"}}`)
			entry["ietf-ip:ipv6"] = json.RawMessage(`{"goodkind-mwan-steering:translation":{"mode":"native"}}`)
			if forwarding != nil {
				entry["ietf-ip:ipv4"] = json.RawMessage(fmt.Sprintf(`{"forwarding":%t,"address":[{"ip":"10.52.1.1","prefix-length":24}],"goodkind-mwan-steering:gateway":"10.52.1.2","goodkind-mwan-steering:translation":{"mode":"native"}}`, *forwarding))
			}
			if ipv6 {
				entry["ietf-ip:ipv6"] = json.RawMessage(`{"forwarding":true,"goodkind-mwan-steering:accept-ra":true,"goodkind-mwan-steering:route-metric":777,"goodkind-mwan-steering:translation":{"mode":"native"}}`)
			}
			retained = append(retained, entry)
		} else if string(entry["name"]) == `"enmwanbr0"` || string(entry["name"]) == `"enmgmt0"` || string(entry["name"]) == `"ownphys0"` {
			retained = append(retained, entry)
		}
	}
	interfaces["interface"], err = json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	var group map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["goodkind-mwan-steering:steering-group"], &group); err != nil {
		t.Fatal(err)
	}
	interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readKernelPolicyValue(t *testing.T, family, name, leaf string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc/sys/net", family, "conf", name, leaf))
	if err != nil {
		t.Fatal(err)
	}
	return string(data[:len(data)-1])
}

func setKernelPolicyValue(t *testing.T, family, name, leaf, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join("/proc/sys/net", family, "conf", name, leaf), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitKernelPolicyValue(t *testing.T, daemon *runtimeDaemon, family, name, leaf, value string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if readKernelPolicyValue(t, family, name, leaf) == value {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	var relevant []string
	for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
		if strings.Contains(line, "autoconfiguration") || strings.Contains(line, "kernel policy") || strings.Contains(line, "owned-link") {
			relevant = append(relevant, line)
		}
	}
	t.Fatalf("%s/%s/%s did not become %s: %s", family, name, leaf, value, strings.Join(relevant, "\n"))
}

func waitKernelPolicyFirewall(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "chain", "inet", "filter", "forward").CombinedOutput()
		if err == nil && strings.Contains(string(output), `iifname "enatt0" oifname "enmwanbr0" meta nfproto ipv4 accept`) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon forwarding rule was not installed: %s", runtimeDaemonLog(t, daemon))
}

func assertKernelPolicyPacket(t *testing.T, gateway, source, destination netns.NsHandle, expected bool) {
	t.Helper()
	setRuntimeNamespace(t, destination)
	listener, err := net.ListenPacket("udp4", "192.0.2.2:52100")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, source)
	connection, err := net.Dial("udp4", "192.0.2.2:52100")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("kernel-policy")); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	setRuntimeNamespace(t, gateway)
	var packet [64]byte
	count, _, readErr := listener.ReadFrom(packet[:])
	if expected && (readErr != nil || string(packet[:count]) != "kernel-policy") {
		rules, rulesErr := exec.Command("nft", "list", "ruleset").CombinedOutput()
		routes, routeErr := netlink.RouteList(nil, netlink.FAMILY_V4)
		policy, policyErr := netlink.RuleList(netlink.FAMILY_V4)
		t.Fatalf("forwarded packet missing: %v; forwarding=%s; rules=%s (%v); routes=%+v (%v); policy=%+v (%v); rp_filter=%s", readErr, readKernelPolicyValue(t, "ipv4", "enatt0", "forwarding"), rules, rulesErr, routes, routeErr, policy, policyErr, readKernelPolicyValue(t, "ipv4", "enatt0", "rp_filter"))
	}
	if !expected && readErr == nil {
		t.Fatal("packet forwarded while per-link forwarding was disabled")
	}
	if !expected {
		if timeout, ok := readErr.(net.Error); !ok || !timeout.Timeout() {
			t.Fatalf("disabled forwarding returned a non-timeout error: %v", readErr)
		}
	}
}
