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
	mappedRuntimeChildEnv  = "MWAN_MAPPED_RUNTIME_TEST_CHILD"
	mappedRuntimeBinaryEnv = "MWAN_MAPPED_RUNTIME_TEST_BINARY"
	legacyRuntimeMAC       = "02:00:5e:00:53:20"
)

func TestOwnedMappedDaemonRuntime(t *testing.T) {
	if os.Getenv(mappedRuntimeChildEnv) == "1" {
		runOwnedMappedDaemonRuntime(t)
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
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedMappedDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), mappedRuntimeChildEnv+"=1", mappedRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated mapped daemon test: %v: %s", err, output)
	}
}

func runOwnedMappedDaemonRuntime(t *testing.T) {
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
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n", filepath.Join(root, "owned-links.json"), filepath.Join(root, "owned-addresses.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.3/29", "192.0.2.4/29", "2001:db8:b01:fe::2/64"}, "")
	defer lan.namespace.Close()
	legacy := newRuntimePeer(t, gateway, "enwebpass0", "legacy-peer", []string{"fd20::1/64"}, []string{"fd20::2/64"}, legacyRuntimeMAC)
	defer legacy.namespace.Close()
	parentPeer := newRuntimePeer(t, gateway, "ownphys0", "owned-peer", nil, nil, ownedRuntimeParentMAC)
	defer parentPeer.namespace.Close()
	setRuntimeNamespace(t, parentPeer.namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index}, VlanId: 397}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "peer397", []string{"10.39.7.2/24", "fd39:7::2/64"})
	addMappedRuntimeRoute(t, "10.39.7.4/32", "10.39.7.1", "peer397")
	addMappedRuntimeRoute(t, "2001:db8:beef:300::/60", "fd39:7::1", "peer397")
	addMappedRuntimeRoute(t, "2001:db8:beef:400::/60", "fd39:7::1", "peer397")
	setRuntimeNamespace(t, lan.namespace)
	addMappedRuntimeRoute(t, "10.39.7.0/24", "192.0.2.1", "lan-host")
	addMappedRuntimeRoute(t, "fd39:7::/64", "2001:db8:b01:fe::3", "lan-host")
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
	writeMappedRuntimeProvider(t, networkDir, "10.39.7.3", "2001:db8:beef:300::/60")
	first := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-first")
	defer killOwnedRuntimeDaemon(t, first)
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	waitStaticRuntimeAddress(t, first, "owned397", "10.39.7.3/32", true)
	waitStaticRuntimeAddress(t, first, "owned397", "10.39.7.4/32", false)
	waitStaticRuntimeAddress(t, first, "owned397", "2001:db8:beef:300::1/128", true)
	waitStaticRuntimeAddress(t, first, "enwebpass0", "2001:db8:beef:200::1/128", true)
	waitMappedRuntimeRule(t, first, "ip", "nat", "10.39.7.3", 40*time.Second)
	waitMappedRuntimeRule(t, first, "ip6", "nat", "2001:db8:beef:300::1", 40*time.Second)
	assertMappedRuntimeReply(t, first, gateway, parentPeer.namespace, lan.namespace, "udp4", "10.39.7.3:39803", "192.0.2.3:39803")
	assertMappedRuntimeReply(t, first, gateway, parentPeer.namespace, lan.namespace, "udp4", "10.39.7.4:39804", "192.0.2.4:39804")
	assertMappedRuntimeReply(t, first, gateway, parentPeer.namespace, lan.namespace, "udp6", "[2001:db8:beef:300::1]:39806", "[2001:db8:b01:fe::2]:39806")
	killOwnedRuntimeDaemon(t, first)
	link, err := netlink.LinkByName("owned397")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := netlink.ParseAddr("10.39.7.99/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, foreign); err != nil {
		t.Fatal(err)
	}
	writeMappedRuntimeProvider(t, networkDir, "10.39.7.5", "2001:db8:beef:400::/60")
	second := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-second")
	defer killOwnedRuntimeDaemon(t, second)
	waitStaticRuntimeAddress(t, second, "owned397", "10.39.7.5/32", true)
	waitStaticRuntimeAddress(t, second, "owned397", "10.39.7.3/32", false)
	waitStaticRuntimeAddress(t, second, "owned397", "2001:db8:beef:400::1/128", true)
	waitStaticRuntimeAddress(t, second, "owned397", "2001:db8:beef:300::1/128", false)
	waitStaticRuntimeAddress(t, second, "owned397", "10.39.7.99/24", true)
	killOwnedRuntimeDaemon(t, second)
	link, err = netlink.LinkByName("owned397")
	if err != nil {
		t.Fatal(err)
	}
	foreignNPT, err := netlink.ParseAddr("2001:db8:beef:500::1/64")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, foreignNPT); err != nil {
		t.Fatal(err)
	}
	writeMappedRuntimeProvider(t, networkDir, "10.39.7.5", "2001:db8:beef:500::/60")
	conflict := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-conflict")
	defer killOwnedRuntimeDaemon(t, conflict)
	waitStaticRuntimeLog(t, conflict, "foreign address uses 2001:db8:beef:500::1 with another prefix")
	waitStaticRuntimeLog(t, conflict, "MWAN-owned external address 2001:db8:beef:500::1/128 is not verified")
	assertMappedRuntimeRuleAbsent(t, "ip6", "nat", "2001:db8:beef:500::1")
	waitStaticRuntimeAddress(t, conflict, "owned397", "2001:db8:beef:500::1/64", true)
}

func assertMappedRuntimeRuleAbsent(t *testing.T, family, table, match string) {
	t.Helper()
	output, err := exec.Command("nft", "list", "table", family, table).CombinedOutput()
	if err != nil {
		t.Fatalf("read %s %s rules: %v: %s", family, table, err, output)
	}
	if strings.Contains(string(output), match) {
		t.Fatalf("%s %s unexpectedly contains %s: %s", family, table, match, output)
	}
}

func waitMappedRuntimeRule(t *testing.T, daemon *runtimeDaemon, family, table, match string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "table", family, table).CombinedOutput()
		if err == nil && strings.Contains(string(output), match) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
	t.Fatalf("%s %s did not install %s: rules=%s daemon=%s", family, table, match, rules, runtimeLogTail(t, daemon, 100))
}

func addMappedRuntimeRoute(t *testing.T, destination, gateway, device string) {
	t.Helper()
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	_, prefix, err := net.ParseCIDR(destination)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: prefix}
	if gateway != "" {
		route.Gw = net.ParseIP(gateway)
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}
}

func writeMappedRuntimeProvider(t *testing.T, directory, externalV4, externalV6 string) {
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
	for _, entry := range entries {
		if string(entry["name"]) == `"enwebpass0"` {
			delete(entry, "goodkind-mwan-steering:link-files")
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"external"`)
			entry["goodkind-mwan-steering:link"] = json.RawMessage(`{"match":{"hardware-address":"` + legacyRuntimeMAC + `"}}`)
			entry["goodkind-mwan-steering:wan"] = json.RawMessage(`{"name":"webpass","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,"health":{"enabled":false}}`)
			entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":1,"weight":1}`)
			continue
		}
		if string(entry["name"]) != `"owned397"` {
			continue
		}
		entry["ietf-ip:ipv4"] = json.RawMessage(fmt.Sprintf(`{"address":[{"ip":"10.39.7.1","prefix-length":24}],"goodkind-mwan-steering:gateway":"10.39.7.2","goodkind-mwan-steering:route-metric":398,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":%q,"internal":"192.0.2.3","delivery":"local"},{"external":"10.39.7.4","internal":"192.0.2.4","delivery":"routed"}]}}`, externalV4))
		entry["ietf-ip:ipv6"] = json.RawMessage(fmt.Sprintf(`{"address":[{"ip":"fd39:7::1","prefix-length":64}],"goodkind-mwan-steering:gateway":"fd39:7::2","goodkind-mwan-steering:route-metric":398,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":%q}}}`, externalV6))
		entry["goodkind-mwan-steering:wan"] = json.RawMessage(`{"name":"owned","table-id":398,"fw-mark":4,"fw-mark-prio":398,"from-prio":58}`)
		entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":0,"weight":1}`)
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

func assertMappedRuntimeReply(t *testing.T, daemon *runtimeDaemon, gateway, upstream, downstream netns.NsHandle, network, external, internal string) {
	t.Helper()
	setRuntimeNamespace(t, downstream)
	listener, err := net.ListenPacket(network, internal)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, upstream)
	connection, err := net.DialTimeout(network, external, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("mapped-request")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, downstream)
	if err := listener.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, sender, err := listener.ReadFrom(buffer)
	if err != nil || string(buffer[:count]) != "mapped-request" {
		t.Fatalf("mapped request: count=%d data=%q error=%v: %s", count, buffer[:count], err, runtimeLogTail(t, daemon, 50))
	}
	if _, err := listener.WriteTo([]byte("mapped-reply"), sender); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, upstream)
	count, err = connection.Read(buffer)
	setRuntimeNamespace(t, gateway)
	if err != nil || string(buffer[:count]) != "mapped-reply" {
		t.Fatalf("mapped reply: count=%d data=%q error=%v", count, buffer[:count], err)
	}
}
