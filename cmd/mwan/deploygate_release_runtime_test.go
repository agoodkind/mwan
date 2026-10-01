//go:build linux && firewallnetns

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestConnectionReleaseDaemonRuntime(t *testing.T) {
	if os.Getenv("MWAN_NETWORKD_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires a dedicated container with real systemd-networkd")
	}
	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(initName)) != "systemd" || os.Geteuid() != 0 {
		t.Fatalf("requires root and systemd PID 1: %q, %v", initName, err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer func() {
		setRuntimeNamespace(t, gateway)
		for _, name := range []string{"enmgmt0", "enmwanbr0", "ownphys0", "service-parent"} {
			link, err := netlink.LinkByName(name)
			if err != nil {
				continue
			}
			if err := netlink.LinkDel(link); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := os.MkdirAll("/var/lib/mwan", 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/var/lib/mwan", "connection-release-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	networkDir, unitDir := filepath.Join(root, "network"), filepath.Join(root, "units")
	for _, directory := range []string{networkDir, unitDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	for destination, source := range map[string]string{"/etc/mwan": networkDir, "/etc/systemd/network": unitDir, "/usr/local/share/wanconfig/yang": schemaDir} {
		bindStartupDirectory(t, source, destination)
		t.Cleanup(func() {
			if err := unix.Unmount(destination, 0); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(func() { networkdResolverCommand(t, "systemctl", "stop", "systemd-networkd") })
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "release-mgmt", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "release-lan", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"}, "")
	defer lan.namespace.Close()
	owned := newRuntimePeer(t, gateway, "ownphys0", "release-owned", nil, nil, ownedRuntimeParentMAC)
	defer owned.namespace.Close()
	setRuntimeNamespace(t, owned.namespace)
	addReleasePeerVLAN(t, "release-owned", "peer397", 397, []string{"10.39.7.2/24", "fd39:7::2/64"})
	setRuntimeNamespace(t, gateway)
	provider := newRuntimePeer(t, gateway, "service-parent", "service-peer", nil, nil, "")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	addReleasePeerVLAN(t, "service-peer", "service-vlan", 53, []string{"fd53::2/64"})
	addMappedRuntimeRoute(t, "2001:db8:53::/60", "fd53::1", "service-vlan")
	setRuntimeNamespace(t, lan.namespace)
	addMappedRuntimeRoute(t, "fd53::/64", "2001:db8:b01:fe::3", "release-lan")
	setRuntimeNamespace(t, gateway)
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	forwarding, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		setRuntimeNamespace(t, gateway)
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", forwarding, 0o600); err != nil {
			t.Error(err)
		}
	}()
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
	writeNPTServiceProvider(t, networkDir, false)
	setReleaseConnectionField(t, networkDir, "service-provider", "goodkind-mwan-steering:steering", json.RawMessage(`{"tier":0,"weight":1}`))
	for name, unit := range map[string]string{
		"10-service.network": "[Match]\nName=enservice0\n[Network]\nAddress=fd53::1/64\nIPv6AcceptRA=no\nKeepConfiguration=yes\n[Route]\nGateway=fd53::2\nMetric=500\n",
		"10-service.netdev":  "[NetDev]\nName=enservice0\nKind=vlan\n[VLAN]\nId=53\n",
		"10-parent.network":  "[Match]\nName=service-parent\n[Network]\nVLAN=enservice0\nIPv6AcceptRA=no\n",
	} {
		if err := os.WriteFile(filepath.Join(unitDir, name), []byte(unit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	networkdResolverCommand(t, "systemctl", "start", "systemd-udevd")
	networkdResolverCommand(t, "systemctl", "restart", "systemd-networkd")
	configPath := filepath.Join(root, "config.toml")
	configuration := fmt.Sprintf("[watchdog]\nconnectivity_timeout_seconds = 1\n[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"100ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel.json"))
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := protocolTestBinary(t)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "release-owned")
	defer killOwnedRuntimeDaemon(t, daemon)
	defer func() {
		if t.Failed() {
			t.Log(runtimeDaemonLog(t, daemon))
			setRuntimeNamespace(t, gateway)
			for _, arguments := range [][]string{{"-6", "route", "show", "table", "all"}, {"-6", "rule", "show"}, {"-6", "neigh", "show"}} {
				output, err := exec.Command("ip", arguments...).CombinedOutput()
				t.Logf("release ip %v: %s (%v)", arguments, output, err)
			}
			output, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
			t.Logf("release ruleset: %s (%v)", output, err)
		}
	}()
	waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
	waitConfiguredRuntimeRoute(t, daemon, "10.39.9.0/24")
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:53::1", 10*time.Second)
	waitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t, 500, nil, "")
	assertNPTServiceReply(t, gateway, provider.namespace, lan.namespace)
	receipt := runtimeScopedNPTReceipt(t, filepath.Join(root, "addresses.json"), "2001:db8:53::1/128")
	setReleaseOwner(t, networkDir, "service-provider", "external")
	assertReleaseCommand(t, binary, configPath, "service-provider", "networkd", false)
	assertReleaseCommand(t, binary, configPath, "service-provider", "mwan", true)
	serviceUnit, err := os.ReadFile(filepath.Join(unitDir, "10-service.network"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "10-service.network"), []byte("[Match]\nName=enservice0\n[Link]\nUnmanaged=yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	networkdResolverCommand(t, "networkctl", "reload")
	networkdResolverCommand(t, "networkctl", "reconfigure", "enservice0")
	waitReleaseCommand(t, binary, configPath, "service-provider", "networkd", daemon)
	networkdResolverCommand(t, "systemctl", "kill", "--signal=STOP", "systemd-networkd")
	defer networkdResolverCommand(t, "systemctl", "kill", "--signal=CONT", "systemd-networkd")
	deadline, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	output, deadlineError := exec.CommandContext(deadline, binary, "deploy-gate", "check-release", "service-provider", "networkd", "--config", configPath).CombinedOutput()
	deadlineExpired := deadline.Err()
	cancel()
	networkdResolverCommand(t, "systemctl", "kill", "--signal=CONT", "systemd-networkd")
	if deadlineError == nil || deadlineExpired != nil || !strings.Contains(string(output), "context deadline exceeded") {
		t.Fatalf("stopped networkd did not fail within its configured observation deadline: %s (%v, outer deadline %v)", output, deadlineError, deadlineExpired)
	}
	t.Logf("configured networkd observation deadline: %s", output)
	setReleaseOwner(t, networkDir, "service-provider", "networkd")
	setReleaseConnectionField(t, networkDir, "service-provider", "goodkind-mwan-steering:link-files", json.RawMessage(`"rendered"`))
	if err := os.WriteFile(filepath.Join(unitDir, "10-service.network"), serviceUnit, 0o644); err != nil {
		t.Fatal(err)
	}
	networkdResolverCommand(t, "networkctl", "reload")
	networkdResolverCommand(t, "networkctl", "reconfigure", "enservice0")
	waitNPTServiceForwarding(t, 500, nil, "")
	setReleaseOwner(t, networkDir, "owned397", "external")
	assertReleaseCommand(t, binary, configPath, "owned397", "mwan", false)
	killOwnedRuntimeDaemon(t, daemon)
	restarted := startRuntimeDaemon(t, binary, configPath, root, "release-external")
	defer killOwnedRuntimeDaemon(t, restarted)
	waitReleaseCommand(t, binary, configPath, "owned397", "mwan", restarted)
	waitMappedRuntimeRule(t, restarted, "ip6", "nat", "2001:db8:53::1", 10*time.Second)
	waitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t, 500, nil, "")
	assertNPTServiceReply(t, gateway, provider.namespace, lan.namespace)
	after := runtimeScopedNPTReceipt(t, filepath.Join(root, "addresses.json"), "2001:db8:53::1/128")
	if !reflect.DeepEqual(receipt, after) {
		t.Fatalf("ordinary release changed NPT receipt: before=%s after=%s", receipt, after)
	}
	killOwnedRuntimeDaemon(t, restarted)
	linksPath := filepath.Join(root, "links.json")
	links, err := os.ReadFile(linksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linksPath); err != nil {
		t.Fatal(err)
	}
	outputText := assertReleaseCommand(t, binary, configPath, "owned397", "mwan", true)
	if !strings.Contains(outputText, `"links_present":false`) {
		t.Fatalf("missing journal presence was not reported: %s", outputText)
	}
	if err := os.WriteFile(linksPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertReleaseCommand(t, binary, configPath, "owned397", "mwan", false)
	if err := os.WriteFile(linksPath, links, 0o600); err != nil {
		t.Fatal(err)
	}
	setReleaseOwner(t, networkDir, "service-provider", "external")
	addressesPath := filepath.Join(root, "addresses.json")
	addresses, err := os.ReadFile(addressesPath)
	if err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	previousBoot := strings.ReplaceAll(string(addresses), strings.TrimSpace(string(boot)), "00000000-0000-0000-0000-000000000000")
	if previousBoot == string(addresses) {
		t.Fatal("production address journal has no current boot ID")
	}
	if err := os.WriteFile(addressesPath, []byte(previousBoot), 0o600); err != nil {
		t.Fatal(err)
	}
	outputText = assertReleaseCommand(t, binary, configPath, "service-provider", "mwan", false)
	if !strings.Contains(outputText, `"previous_boot":true`) {
		t.Fatalf("previous-boot receipt was not reported: %s", outputText)
	}
	unchanged, err := os.ReadFile(addressesPath)
	if err != nil || string(unchanged) != previousBoot {
		t.Fatalf("read-only verifier changed the previous-boot journal: %v", err)
	}
	if err := os.WriteFile(addressesPath, addresses, 0o600); err != nil {
		t.Fatal(err)
	}
	assertReleaseCommand(t, binary, configPath, "service-provider", "mwan", true)
}

func addReleasePeerVLAN(t *testing.T, parentName, name string, id int, addresses []string) {
	t.Helper()
	parent, err := netlink.LinkByName(parentName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: name, ParentIndex: parent.Attrs().Index}, VlanId: id}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, name, addresses)
}

func setReleaseOwner(t *testing.T, directory, id, owner string) {
	t.Helper()
	setReleaseConnectionField(t, directory, id, "goodkind-mwan-steering:owner", json.RawMessage(fmt.Sprintf("%q", owner)))
}

func setReleaseConnectionField(t *testing.T, directory, id, field string, value json.RawMessage) {
	t.Helper()
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
	found := false
	for _, entry := range entries {
		if string(entry["goodkind-mwan-steering:connection-id"]) == fmt.Sprintf("%q", id) {
			entry[field] = value
			if field == "goodkind-mwan-steering:owner" && string(value) == `"external"` {
				delete(entry, "goodkind-mwan-steering:link-files")
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("connection %s absent", id)
	}
	interfaces["interface"], err = json.Marshal(entries)
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

func assertReleaseCommand(t *testing.T, binary, configPath, id, previous string, success bool) string {
	t.Helper()
	output, err := exec.Command(binary, "deploy-gate", "check-release", id, previous, "--config", configPath).CombinedOutput()
	t.Logf("release %s previous=%s output=%s error=%v", id, previous, output, err)
	if (err == nil) != success {
		t.Fatalf("release %s expected success=%v: %s (%v)", id, success, output, err)
	}
	return string(output)
}

func waitReleaseCommand(t *testing.T, binary, configPath, id, previous string, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var output []byte
	var err error
	for time.Now().Before(deadline) {
		output, err = exec.Command(binary, "deploy-gate", "check-release", id, previous, "--config", configPath).CombinedOutput()
		if err == nil {
			t.Logf("release %s previous=%s: %s", id, previous, output)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("release %s previous=%s did not complete: %s (%v); daemon=%s", id, previous, output, err, runtimeDaemonLog(t, daemon))
}
