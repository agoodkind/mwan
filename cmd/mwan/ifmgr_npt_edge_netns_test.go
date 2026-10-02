//go:build linux && firewallnetns

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func assertRuntimeTranslationSettled(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	const reason = "NPT translation readiness changed"
	before := strings.Count(runtimeDaemonLog(t, daemon), reason)
	time.Sleep(500 * time.Millisecond)
	assertRuntimeDaemonRunning(t, daemon)
	if after := strings.Count(runtimeDaemonLog(t, daemon), reason); after != before {
		t.Fatalf("stable NPT state requested another reconcile: before=%d after=%d", before, after)
	}
}

func TestLegacyNPTPreparationUpgrade(t *testing.T) {
	if os.Getenv("MWAN_NPT_LEGACY_BINARY") == "" || os.Getenv("MWAN_NPT_LEGACY_SHA256") == "" {
		t.Fatal("original-release upgrade requires MWAN_NPT_LEGACY_BINARY and MWAN_NPT_LEGACY_SHA256")
	}
	if os.Getenv("MWAN_NPT_UPGRADE_CHILD") == "1" {
		runLegacyNPTPreparationUpgrade(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestLegacyNPTPreparationUpgrade$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), "MWAN_NPT_UPGRADE_CHILD=1", mappedRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated original-release upgrade: %v: %s", err, output)
	}
}

func runLegacyNPTPreparationUpgrade(t *testing.T) {
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
	networkdDir := filepath.Join(root, "networkd")
	for _, directory := range []string{networkDir, networkdDir} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64", "2001:db8:b01:1::3/64"}, []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64", "2001:db8:b01:1::2/64"}, "")
	defer lan.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enwebpass0", "legacy-peer", []string{"10.20.0.2/24", "fd20::1/64"}, []string{"10.20.0.1/24", "fd20::2/64"}, legacyRuntimeMAC)
	defer provider.namespace.Close()
	setRuntimeNamespace(t, lan.namespace)
	addMappedRuntimeRoute(t, "10.20.0.0/24", "192.0.2.1", "lan-host")
	addMappedRuntimeRoute(t, "fd20::/64", "2001:db8:b01:fe::3", "lan-host")
	setRuntimeNamespace(t, gateway)
	addRuntimeDefault(t, "enwebpass0", "fd20::2")
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	internalLink, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	internalRoute, err := netlink.ParseIPNet("2001:db8:b01::/60")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: internalLink.Attrs().Index, Dst: internalRoute, Table: 200}); err != nil {
		t.Fatal(err)
	}
	internalV4Route, err := netlink.ParseIPNet("192.0.2.0/29")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: internalLink.Attrs().Index, Dst: internalV4Route, Table: 200}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	network := `{"ietf-interfaces:interfaces":{"interface":[{"name":"enwebpass0","type":"iana-if-type:ethernetCsmacd","goodkind-mwan-steering:connection-id":"webpass","goodkind-mwan-steering:owner":"networkd","goodkind-mwan-steering:link-files":"hand-authored","goodkind-mwan-steering:wan":{"name":"webpass","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,"health":{"enabled":false}},"goodkind-mwan-steering:steering":{"tier":1,"weight":1},"ietf-ip:ipv6":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":"2001:db8:beef:600::/60"}}}}]}}`
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
	base, err := os.ReadFile(filepath.Join(networkDir, "network.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document, providerDocument map[string]json.RawMessage
	if err := json.Unmarshal(base, &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(network), &providerDocument); err != nil {
		t.Fatal(err)
	}
	var interfaces, providerInterfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(providerDocument["ietf-interfaces:interfaces"], &providerInterfaces); err != nil {
		t.Fatal(err)
	}
	var entries, providers []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(providerInterfaces["interface"], &providers); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if string(entry["name"]) == `"enmgmt0"` || string(entry["name"]) == `"enmwanbr0"` {
			providers = append(providers, entry)
		}
	}
	interfaces["interface"], err = json.Marshal(providers)
	if err != nil {
		t.Fatal(err)
	}
	var steering, firewall map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["goodkind-mwan-steering:steering-group"], &steering); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(steering["firewall"], &firewall); err != nil {
		t.Fatal(err)
	}
	for key := range firewall {
		if strings.HasPrefix(key, "pinned-") && key != "pinned-set-v4-name" && key != "pinned-set-v6-name" {
			delete(firewall, key)
		}
	}
	steering["firewall"], err = json.Marshal(firewall)
	if err != nil {
		t.Fatal(err)
	}
	interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(steering)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	base, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), base, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.addresses]\nstate_file = %q\n", filepath.Join(root, "owned-addresses.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owned-addresses.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkRuntimeLegacyFirstStart(t, configPath, root, gateway, provider.namespace, lan.namespace)
}

func checkRuntimeLegacyFirstStart(t *testing.T, configPath, root string, gateway, upstream, downstream netns.NsHandle) {
	t.Helper()
	legacyBinary := os.Getenv("MWAN_NPT_LEGACY_BINARY")
	legacySHA256 := os.Getenv("MWAN_NPT_LEGACY_SHA256")
	legacyBytes, err := os.ReadFile(legacyBinary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(legacyBytes)
	if hex.EncodeToString(digest[:]) != legacySHA256 {
		t.Fatal("original executable differs from required release hash")
	}
	setRuntimeNamespace(t, upstream)
	addMappedRuntimeRoute(t, "2001:db8:beef:600::/60", "fd20::1", "legacy-peer")
	setRuntimeNamespace(t, gateway)
	configureLegacyUpgradeLink(t, filepath.Join(root, "mwan", "network.json"))
	setReleaseConnectionField(t, filepath.Join(root, "mwan"), "webpass", "ietf-ip:ipv4", json.RawMessage(`{"address":[{"ip":"10.20.0.2","prefix-length":24}],"goodkind-mwan-steering:gateway":"10.20.0.1","goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":"10.20.0.3","internal":"192.0.2.2"}]}}`))
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyConfig := strings.Split(string(config), "[ifmgr.modules.addresses]")[0]
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	old := startRuntimeDaemon(t, legacyBinary, configPath, root, "mapped-old-unjournaled")
	defer killOwnedRuntimeDaemon(t, old)
	waitMappedRuntimeRule(t, old, "ip6", "nat", "2001:db8:beef:600::1", 10*time.Second)
	waitRuntimeNPTEdgeReady(t, old, "enwebpass0", "2001:db8:beef:600::1/128")
	waitStaticRuntimeAddress(t, old, "enwebpass0", "10.20.0.3/32", true)
	unrelatedLink, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	addEgressAddress(t, unrelatedLink, "10.20.0.99/32")
	assertMappedRuntimeReply(t, old, gateway, upstream, downstream, "udp4", "10.20.0.3:17608", "192.0.2.2:17608")
	assertRuntimeDaemonRunning(t, old)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	assertMappedRuntimeReply(t, old, gateway, upstream, downstream, "udp6", "[2001:db8:beef:601:4611::2]:17600", "[2001:db8:b01:1::2]:17600")
	assertMappedRuntimeReply(t, old, gateway, upstream, downstream, "udp6", "[2001:db8:beef:600::1]:17604", "[2001:db8:b01:fe::2]:17604")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "legacy-npt-transition.json")
	capture := exec.Command(os.Getenv(mappedRuntimeBinaryEnv), "ifmgr", "--role", "wan", "--capture-legacy-npt", manifest, "--producer-pid", fmt.Sprint(old.command.Process.Pid), "--producer-sha256", legacySHA256)
	capture.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	removeExtra := addExtraLegacyUpgradeRule(t)
	if output, err := capture.CombinedOutput(); err == nil || !strings.Contains(string(output), "rules differ from configured producer intent") {
		t.Fatalf("capture must reject an extra recognized rule: %v: %s", err, output)
	}
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	removeExtra()
	assertMappedRuntimeReply(t, old, gateway, upstream, downstream, "udp6", "[2001:db8:beef:601:4611::2]:17607", "[2001:db8:b01:1::2]:17607")
	capture = exec.Command(os.Getenv(mappedRuntimeBinaryEnv), "ifmgr", "--role", "wan", "--capture-legacy-npt", manifest, "--producer-pid", fmt.Sprint(old.command.Process.Pid), "--producer-sha256", legacySHA256)
	capture.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	if output, err := capture.CombinedOutput(); err != nil {
		t.Fatalf("capture original producer: %v: %s", err, output)
	}
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	assertMappedRuntimeReply(t, old, gateway, upstream, downstream, "udp6", "[2001:db8:beef:601:4611::2]:17601", "[2001:db8:b01:1::2]:17601")
	assertLegacyAdoptionCommand(t, configPath, manifest, "is still present or cannot be verified absent")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	stopRuntimeDaemon(t, old)
	manifestBytes, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var captured map[string]json.RawMessage
	if err := json.Unmarshal(manifestBytes, &captured); err != nil {
		t.Fatal(err)
	}
	var mapped []struct {
		Record struct {
			Prefix string `json:"prefix"`
		} `json:"record"`
	}
	if err := json.Unmarshal(captured["mapped_addresses"], &mapped); err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 1 || mapped[0].Record.Prefix != "10.20.0.3/32" {
		t.Fatalf("capture selected primary or unrelated addresses: %s", manifestBytes)
	}
	captured["mapped_addresses"] = json.RawMessage(strings.ReplaceAll(string(captured["mapped_addresses"]), "10.20.0.3/32", "10.20.0.99/32"))
	unrelated, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, unrelated, 0o600); err != nil {
		t.Fatal(err)
	}
	assertLegacyAdoptionCommand(t, configPath, manifest, "legacy mapped address intent, link or kernel address changed")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	if err := json.Unmarshal(manifestBytes, &captured); err != nil {
		t.Fatal(err)
	}
	captured["boot_id"] = json.RawMessage(`"a-different-boot"`)
	stale, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	assertLegacyAdoptionCommand(t, configPath, manifest, "manifest identity is stale or invalid")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	if err := os.WriteFile(manifest, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	staleConfig := strings.ReplaceAll(string(config), "owned-addresses.json", "another-journal.json")
	if err := os.WriteFile(configPath, []byte(staleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	assertLegacyAdoptionCommand(t, configPath, manifest, "manifest identity is stale or invalid")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	providerLink, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	originalMAC := slices.Clone(providerLink.Attrs().HardwareAddr)
	changedMAC, err := net.ParseMAC("02:00:5e:00:53:21")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetHardwareAddr(providerLink, changedMAC); err != nil {
		t.Fatal(err)
	}
	assertLegacyAdoptionCommand(t, configPath, manifest, "configured MAC does not match")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	if err := netlink.LinkSetHardwareAddr(providerLink, originalMAC); err != nil {
		t.Fatal(err)
	}
	assertLegacyAdoptionCommand(t, configPath, manifest, "")
	assertLegacyAdoptionCommand(t, configPath, manifest, "")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"), "2001:db8:beef:600::1/128")
	current := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-first-journal-start")
	defer killOwnedRuntimeDaemon(t, current)
	waitMappedRuntimeRule(t, current, "ip6", "nat", "2001:db8:beef:600::1", 10*time.Second)
	waitStaticRuntimeAddress(t, current, "enwebpass0", "2001:db8:beef:600::1/128", true)
	waitStaticRuntimeAddress(t, current, "enwebpass0", "10.20.0.3/32", true)
	assertMappedRuntimeReply(t, current, gateway, upstream, downstream, "udp4", "10.20.0.3:17609", "192.0.2.2:17609")
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"), "2001:db8:beef:600::1/128")
	assertMappedRuntimeReply(t, current, gateway, upstream, downstream, "udp6", "[2001:db8:beef:601:4611::2]:17602", "[2001:db8:b01:1::2]:17602")
	assertMappedRuntimeReply(t, current, gateway, upstream, downstream, "udp6", "[2001:db8:beef:600::1]:17605", "[2001:db8:b01:fe::2]:17605")
	killOwnedRuntimeDaemon(t, current)
	restarted := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-upgrade-restart")
	defer killOwnedRuntimeDaemon(t, restarted)
	waitMappedRuntimeRule(t, restarted, "ip6", "nat", "2001:db8:beef:600::1", 10*time.Second)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"), "2001:db8:beef:600::1/128")
	assertMappedRuntimeReply(t, restarted, gateway, upstream, downstream, "udp6", "[2001:db8:beef:601:4611::2]:17603", "[2001:db8:b01:1::2]:17603")
	assertMappedRuntimeReply(t, restarted, gateway, upstream, downstream, "udp6", "[2001:db8:beef:600::1]:17606", "[2001:db8:b01:fe::2]:17606")
	assertMappedRuntimeReply(t, restarted, gateway, upstream, downstream, "udp4", "10.20.0.3:17610", "192.0.2.2:17610")
	waitStaticRuntimeAddress(t, restarted, "enmgmt0", "203.0.113.1/24", true)
}

func addExtraLegacyUpgradeRule(t *testing.T) func() {
	t.Helper()
	connection, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: "nat"}
	chain := &nftables.Chain{Table: table, Name: "postrouting"}
	rules, err := connection.GetRules(table, chain)
	if err != nil || len(rules) == 0 {
		t.Fatalf("read original NPT rules: %v", err)
	}
	marker := []byte("legacy-upgrade-extra-rule")
	connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: rules[0].Exprs, UserData: marker})
	if err := connection.Flush(); err != nil {
		t.Fatal(err)
	}
	return func() {
		rules, err := connection.GetRules(table, chain)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range rules {
			if bytes.Equal(rule.UserData, marker) {
				if err := connection.DelRule(rule); err != nil {
					t.Fatal(err)
				}
				if err := connection.Flush(); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		t.Fatal("extra NPT rule disappeared before exact cleanup")
	}
}

func assertLegacyAdoptionCommand(t *testing.T, configPath, manifest, expectedError string) {
	t.Helper()
	command := exec.Command(os.Getenv(mappedRuntimeBinaryEnv), "ifmgr", "--role", "wan", "--adopt-legacy-npt", manifest)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	output, err := command.CombinedOutput()
	if expectedError == "" {
		if err != nil {
			t.Fatalf("adopt original producer receipts: %v: %s", err, output)
		}
		return
	}
	if err == nil || !strings.Contains(string(output), expectedError) {
		t.Fatalf("adoption rejection expected %q: %v: %s", expectedError, err, output)
	}
	if count := strings.Count(string(output), "legacy NPT transition rejected"); count != 1 {
		t.Fatalf("adoption rejection requires one command error log, got %d: %s", count, output)
	}
	if strings.Contains(string(output), "legacy NPT manifest inspection failed") || strings.Contains(string(output), "legacy NPT edge inspection failed") || strings.Contains(string(output), "legacy link identity inspection failed") {
		t.Fatalf("adoption rejection logged a helper failure: %s", output)
	}
}

func configureLegacyUpgradeLink(t *testing.T, path string) {
	t.Helper()
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
	for _, entry := range entries {
		if string(entry["name"]) != `"enwebpass0"` {
			continue
		}
		entry["goodkind-mwan-steering:link-files"] = json.RawMessage(`"rendered"`)
		entry["goodkind-mwan-steering:link"] = json.RawMessage(fmt.Sprintf(`{"match":{"driver":"veth"},"hardware-address":%q}`, legacyRuntimeMAC))
		var ipv6 map[string]json.RawMessage
		if err := json.Unmarshal(entry["ietf-ip:ipv6"], &ipv6); err != nil {
			t.Fatal(err)
		}
		ipv6["goodkind-mwan-steering:dhcp"] = json.RawMessage("false")
		entry["ietf-ip:ipv6"], err = json.Marshal(ipv6)
		if err != nil {
			t.Fatal(err)
		}
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

func waitRuntimeNPTEdgeReady(t *testing.T, daemon *runtimeDaemon, iface, cidr string) {
	t.Helper()
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(iface)
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, unix.AF_INET6)
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			if address.IP.String() == prefix.Addr().String() && address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
				return
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("NPT edge %s did not become ready: %s", cidr, runtimeLogTail(t, daemon, 50))
}

func writeRuntimeLegacyNPT(t *testing.T, directory, prefix string, removeWANs bool) {
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
	var retained []map[string]json.RawMessage
	for _, entry := range entries {
		if string(entry["goodkind-mwan-steering:owner"]) == `"mwan"` {
			continue
		}
		delete(entry, "goodkind-mwan-steering:link")
		delete(entry, "ietf-ip:ipv4")
		delete(entry, "ietf-ip:ipv6")
		if string(entry["name"]) == `"enwebpass0"` {
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"networkd"`)
			entry["goodkind-mwan-steering:link-files"] = json.RawMessage(`"hand-authored"`)
			delete(entry, "goodkind-mwan-steering:link")
			entry["ietf-ip:ipv6"] = json.RawMessage(fmt.Sprintf(`{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":%q}}}`, prefix))
		}
		if removeWANs {
			delete(entry, "ietf-ip:ipv6")
		}
		retained = append(retained, entry)
	}
	interfaces["interface"], err = json.Marshal(retained)
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

func assertRuntimeNPTEdges(t *testing.T, path string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Objects []struct {
			Scope  string `json:"scope"`
			Prefix string `json:"prefix"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, record := range journal.Objects {
		if slices.Contains(want, record.Prefix) && record.Scope != "npt-edge" {
			t.Fatalf("edge %s was journaled as ordinary acquisition: %s", record.Prefix, data)
		}
		if record.Scope == "npt-edge" {
			actual = append(actual, record.Prefix)
		}
	}
	slices.Sort(actual)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("journaled NPT edges = %v; expected %v: %s", actual, want, data)
	}
}

func setMappedRuntimeIncompatibleChain(t *testing.T) {
	t.Helper()
	for _, arguments := range [][]string{
		{"delete", "chain", "ip6", "nat", "postrouting"},
		{"add", "chain", "ip6", "nat", "postrouting", "{", "type", "filter", "hook", "postrouting", "priority", "100", ";", "policy", "accept", ";", "}"},
	} {
		if output, err := exec.Command("nft", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("replace NPT postrouting chain: %v: %s", err, output)
		}
	}
}
