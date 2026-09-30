//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestNetworkdNPTEdgeDaemonRuntime(t *testing.T) {
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
	if err := os.MkdirAll("/var/lib/mwan", 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/var/lib/mwan", "networkd-npt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	networkDir := filepath.Join(root, "network")
	unitDir := filepath.Join(root, "units")
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
				t.Errorf("unmount %s: %v", destination, err)
			}
		})
	}
	networkdResolverCommand(t, "systemctl", "start", "systemd-udevd")
	networkdResolverCommand(t, "systemctl", "set-environment", "SYSTEMD_LOG_LEVEL=debug")
	t.Cleanup(func() { networkdResolverCommand(t, "systemctl", "stop", "systemd-networkd") })
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "npt-mgmt", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "npt-lan", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.3/29", "2001:db8:b01:fe::2/64"}, "")
	defer lan.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enwebpass0", "wan-host", nil, []string{"198.51.100.2/24", "2001:db8:2::2/64"}, legacyRuntimeMAC)
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	waitAutoconfigurationLinkLocal(t, "wan-host")
	services := startProtocolServicesForInterface(t, root, "wan-host")
	defer stopProtocolServices(t, services)
	for _, destination := range []string{"/run/kea", "/var/lib/kea"} {
		t.Cleanup(func() {
			if err := unix.Unmount(destination, 0); err != nil {
				t.Errorf("unmount %s: %v", destination, err)
			}
		})
	}
	setRuntimeNamespace(t, gateway)
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
	writeMappedRuntimeProvider(t, networkDir, "10.39.7.3", "2001:db8:beef:300::/60")
	writeRuntimeLegacyNPT(t, networkDir, "2001:db8:30::/56", false)
	path := filepath.Join(networkDir, "network.json")
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input = []byte(strings.ReplaceAll(string(input), `"external-source":"configured","external-prefix":"2001:db8:30::/56"`, `"external-source":"delegated","expected-prefix":"2001:db8:30::/56"`))
	input = []byte(strings.ReplaceAll(string(input), `"ietf-ip:ipv6":{"goodkind-mwan-steering:translation"`, `"ietf-ip:ipv6":{"goodkind-mwan-steering:dhcp":true,"goodkind-mwan-steering:delegation":{"hint":"::/56","without-ra":"solicit","use-delegated-prefix":true},"goodkind-mwan-steering:translation"`))
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	unit := "[Match]\nName=enwebpass0\n[Network]\nDHCP=ipv6\nIPv6AcceptRA=yes\nKeepConfiguration=yes\n[IPv6AcceptRA]\nDHCPv6Client=always\n[DHCPv6]\nWithoutRA=solicit\nUseAddress=yes\nUseDelegatedPrefix=yes\nPrefixDelegationHint=::/56\n"
	if err := os.WriteFile(filepath.Join(unitDir, "10-legacy.network"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	networkdResolverCommand(t, "systemctl", "restart", "systemd-networkd")
	configuration := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"100ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.addresses]\nstate_file = %q\n", filepath.Join(root, "addresses.json"))
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := protocolTestBinary(t)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "networkd-npt")
	defer killOwnedRuntimeDaemon(t, daemon)
	defer func() {
		if t.Failed() {
			t.Logf("network JSON: %s", input)
			t.Logf("complete networkd NPT daemon log: %s", runtimeDaemonLog(t, daemon))
			t.Logf("networkd state: %s", networkdResolverCommand(t, "networkctl", "status", "enwebpass0", "--no-pager"))
			t.Logf("networkd journal: %s", networkdResolverCommand(t, "journalctl", "-u", "systemd-networkd", "--no-pager", "-n", "150"))
			for _, service := range services {
				data, err := os.ReadFile(service.logPath)
				t.Logf("%s log: %s (%v)", service.name, data, err)
			}
		}
	}()
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 10*time.Second)
	assertRuntimeNPTEdges(t, filepath.Join(root, "addresses.json"), "2001:db8:30::1/128")
	link, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	before, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	status := networkdResolverCommand(t, "networkctl", "status", "enwebpass0", "--no-pager")
	prefix, err := exec.Command(binary, "pd", "enwebpass0").Output()
	if err != nil || !strings.Contains(status, "10-legacy.network") || strings.TrimSpace(string(prefix)) != "2001:db8:30::/56" {
		t.Fatalf("networkd did not acquire the delegated prefix: prefix=%q error=%v status=%s", prefix, err, status)
	}
	killOwnedRuntimeDaemon(t, daemon)
	restarted := startRuntimeDaemon(t, binary, configPath, root, "networkd-npt-restarted")
	defer killOwnedRuntimeDaemon(t, restarted)
	waitMappedRuntimeRule(t, restarted, "ip6", "nat", "2001:db8:30::1", 10*time.Second)
	assertRuntimeNPTEdges(t, filepath.Join(root, "addresses.json"), "2001:db8:30::1/128")
	after, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range before {
		found := false
		for _, current := range after {
			if current.IPNet.String() == address.IPNet.String() {
				found = true
			}
		}
		if !found {
			t.Fatalf("restart removed networkd address %s", address.IPNet)
		}
	}
	networkdResolverCommand(t, "systemctl", "is-active", "systemd-networkd")
	assertRuntimeDaemonRunning(t, restarted)
	killOwnedRuntimeDaemon(t, restarted)
	checkNetworkdNPTEdgeService(t, gateway, lan.namespace, binary, configPath, networkDir, root)
}

func checkNetworkdNPTEdgeService(t *testing.T, gateway, downstream netns.NsHandle, binary, configPath, networkDir, root string) {
	t.Helper()
	provider := newRuntimePeer(t, gateway, "service-parent", "service-peer", nil, nil, "")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	parent, err := netlink.LinkByName("service-peer")
	if err != nil {
		t.Fatal(err)
	}
	peerVLAN := &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "service-vlan", ParentIndex: parent.Attrs().Index}, VlanId: 53}
	if err := netlink.LinkAdd(peerVLAN); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "service-vlan", []string{"fd53::2/64"})
	addMappedRuntimeRoute(t, "2001:db8:53::/60", "fd53::1", "service-vlan")
	setRuntimeNamespace(t, downstream)
	addMappedRuntimeRoute(t, "fd53::/64", "2001:db8:b01:fe::3", "npt-lan")
	setRuntimeNamespace(t, gateway)
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	forwardingPath := "/proc/sys/net/ipv6/conf/all/forwarding"
	forwarding, err := os.ReadFile(forwardingPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forwardingPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		setRuntimeNamespace(t, gateway)
		if err := os.WriteFile(forwardingPath, forwarding, 0o600); err != nil {
			t.Error(err)
		}
	}()
	writeNPTServiceProvider(t, networkDir, false)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/usr/local/bin/mwan", data, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("getent", "group", "sysrepo").CombinedOutput(); err != nil {
		t.Logf("provision sysrepo group: %s", output)
		networkdResolverCommand(t, "groupadd", "--system", "sysrepo")
	}
	installStartupDaemonUnit(t, configPath)
	t.Cleanup(func() {
		networkdResolverCommand(t, "systemctl", "stop", "mwan-ifmgr@wan")
		for _, path := range []string{"/etc/systemd/system/mwan-ifmgr@.service", "/etc/systemd/system/mwan-ifmgr@wan.service.d", "/usr/local/bin/mwan"} {
			if err := os.RemoveAll(path); err != nil {
				t.Error(err)
			}
		}
		networkdResolverCommand(t, "systemctl", "daemon-reload")
	})
	defer func() {
		if t.Failed() {
			setRuntimeNamespace(t, gateway)
			t.Log(networkdResolverCommand(t, "journalctl", "-u", "mwan-ifmgr@wan", "--no-pager"))
			for _, arguments := range [][]string{{"-6", "route", "show", "table", "all"}, {"-6", "rule", "show"}, {"-6", "neigh", "show"}} {
				output, err := exec.Command("ip", arguments...).CombinedOutput()
				t.Logf("service ip %v: %s (%v)", arguments, output, err)
			}
			output, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
			t.Logf("service ruleset: %s (%v)", output, err)
			forwarding, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
			t.Logf("service IPv6 forwarding: %s (%v)", forwarding, err)
		}
	}()
	startOrderedDaemon(t, "start")
	t.Log(networkdResolverCommand(t, "systemctl", "show", "mwan-ifmgr@wan", "--property=AmbientCapabilities", "--property=CapabilityBoundingSet"))
	waitNPTServiceAddress(t, "enservice0", "2001:db8:53::1/128", true)
	assertRuntimeNPTEdges(t, filepath.Join(root, "addresses.json"), "2001:db8:30::1/128", "2001:db8:53::1/128")
	waitRuntimeAttachedEdge(t, "enmwanbr0", "2001:db8:30::1")
	waitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t)
	assertNPTServiceReply(t, gateway, provider.namespace, downstream)
	checkRenderedNPTStaticContinuity(t, gateway, provider.namespace, downstream, networkDir, root)
	// SIGKILL preserves the old daemon's attached programs and maps.
	networkdResolverCommand(t, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", "mwan-ifmgr@wan")
	networkdResolverCommand(t, "systemctl", "stop", "mwan-ifmgr@wan")
	waitRuntimeAttachedEdge(t, "enmwanbr0", "2001:db8:30::1")
	writeNPTServiceProvider(t, networkDir, true)
	startOrderedDaemon(t, "start")
	waitNPTServiceAddress(t, "enwebpass0", "2001:db8:30::1/128", false)
	assertRuntimeNPTEdges(t, filepath.Join(root, "addresses.json"), "2001:db8:53::1/128")
	waitNPTServiceAddress(t, "enservice0", "2001:db8:53::1/128", true)
	assertMappedRuntimeRuleAbsent(t, "ip6", "nat", "2001:db8:30::1")
	waitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t)
	assertNPTServiceReply(t, gateway, provider.namespace, downstream)
}

func waitNPTServiceForwarding(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "chain", "inet", "mwan_steer", "forward").CombinedOutput()
		if err == nil && mappedRuntimeHasLine(string(output), []string{`iifname "enmwanbr0" oifname "enservice0" meta nfproto ipv6`, `meta mark != 0x00000005 drop`}) {
			rules, err := netlink.RuleList(unix.AF_INET6)
			if err != nil {
				t.Fatal(err)
			}
			for _, rule := range rules {
				if rule.Priority == 530 && rule.Table == 530 && rule.Mark == 5 {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("service provider forwarding did not become ready")
}

func checkRenderedNPTStaticContinuity(t *testing.T, gateway, upstream, downstream netns.NsHandle, directory, root string) {
	t.Helper()
	link, err := netlink.LinkByName("enservice0")
	if err != nil {
		t.Fatal(err)
	}
	vlan, ok := link.(*netlink.Vlan)
	parent, parentErr := netlink.LinkByIndex(link.Attrs().ParentIndex)
	if !ok || vlan.VlanId != 53 || parentErr != nil || parent.Attrs().Name != "service-parent" {
		t.Fatalf("networkd provider is not VLAN 53 on service-parent: %+v, %v", link, parentErr)
	}
	before := runtimeScopedNPTReceipt(t, filepath.Join(root, "addresses.json"), "2001:db8:53::1/128")
	updates := make(chan netlink.AddrUpdate, 128)
	done := make(chan struct{})
	var mutex sync.Mutex
	var observationErrors []error
	if err := netlink.AddrSubscribeWithOptions(updates, done, netlink.AddrSubscribeOptions{
		Namespace: &gateway,
		ErrorCallback: func(err error) {
			select {
			case <-done:
				return
			default:
			}
			mutex.Lock()
			observationErrors = append(observationErrors, err)
			mutex.Unlock()
		},
	}); err != nil {
		t.Fatal(err)
	}
	collected := make(chan []netlink.AddrUpdate, 1)
	go func() {
		var events []netlink.AddrUpdate
		for event := range updates {
			events = append(events, event)
		}
		collected <- events
	}()
	defer func() {
		close(done)
		events := <-collected
		mutex.Lock()
		defer mutex.Unlock()
		if len(observationErrors) != 0 {
			t.Errorf("continuous address observer failed: %v", observationErrors)
		}
		for _, event := range events {
			if event.LinkIndex == link.Attrs().Index && event.LinkAddress.String() == "2001:db8:53::1/128" {
				t.Errorf("networkd reconfiguration changed the configured NPT edge: %+v", event)
			}
		}
	}()
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), `"goodkind-mwan-steering:route-metric":500`, `"goodkind-mwan-steering:route-metric":501`, 1)
	if updated == string(data) {
		t.Fatal("configured provider route metric was absent")
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	startOrderedDaemon(t, "restart")
	waitNPTServiceAddress(t, "enservice0", "2001:db8:53::1/128", true)
	waitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t)
	assertNPTServiceReply(t, gateway, upstream, downstream)
	after := runtimeScopedNPTReceipt(t, filepath.Join(root, "addresses.json"), "2001:db8:53::1/128")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("networkd reconfiguration changed the scoped receipt: before=%s after=%s", before, after)
	}
}

func runtimeScopedNPTReceipt(t *testing.T, path, prefix string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Objects []json.RawMessage `json:"objects"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	for _, object := range journal.Objects {
		var record struct {
			Scope  string `json:"scope"`
			Prefix string `json:"prefix"`
		}
		if err := json.Unmarshal(object, &record); err != nil {
			t.Fatal(err)
		}
		if record.Scope == "npt-edge" && record.Prefix == prefix {
			return object
		}
	}
	t.Fatalf("scoped NPT receipt %s was absent: %s", prefix, data)
	return nil
}

func writeNPTServiceProvider(t *testing.T, directory string, withdraw bool) {
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
	if withdraw {
		for _, entry := range entries {
			if string(entry["name"]) == `"enwebpass0"` {
				entry["ietf-ip:ipv6"] = json.RawMessage(`{"goodkind-mwan-steering:dhcp":true,"goodkind-mwan-steering:delegation":{"hint":"::/56","without-ra":"solicit","use-delegated-prefix":true}}`)
			}
		}
	} else {
		var provider map[string]json.RawMessage
		if err := json.Unmarshal([]byte(`{"name":"enservice0","type":"iana-if-type:l2vlan","enabled":true,"goodkind-mwan-steering:connection-id":"service-provider","goodkind-mwan-steering:owner":"networkd","goodkind-mwan-steering:link-files":"rendered","goodkind-mwan-steering:link":{"vlan":{"parent":"service-parent","id":53}},"ietf-ip:ipv6":{"address":[{"ip":"fd53::1","prefix-length":64}],"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:accept-ra":false,"goodkind-mwan-steering:gateway":"fd53::2","goodkind-mwan-steering:route-metric":500,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":"2001:db8:53::/60"}}},"goodkind-mwan-steering:wan":{"name":"service","table-id":530,"fw-mark":5,"fw-mark-prio":530,"from-prio":60,"health":{"enabled":false}},"goodkind-mwan-steering:steering":{"tier":1,"weight":1}}`), &provider); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, provider)
		parentLink, err := netlink.LinkByName("service-parent")
		if err != nil {
			t.Fatal(err)
		}
		var parent map[string]json.RawMessage
		parentJSON := fmt.Sprintf(`{"name":"service-parent","type":"iana-if-type:ethernetCsmacd","goodkind-mwan-steering:owner":"networkd","goodkind-mwan-steering:link-files":"rendered","goodkind-mwan-steering:link":{"match":{"hardware-address":%q}}}`, parentLink.Attrs().HardwareAddr.String())
		if err := json.Unmarshal([]byte(parentJSON), &parent); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, parent)
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

func waitNPTServiceAddress(t *testing.T, name, prefix string, present bool) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		addresses, err := netlink.AddrList(link, unix.AF_INET6)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, address := range addresses {
			found = found || address.IPNet.String() == prefix
		}
		if found == present {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("service address %s on %s: expected presence %t", prefix, name, present)
}

func assertNPTServiceReply(t *testing.T, gateway, upstream, downstream netns.NsHandle) {
	t.Helper()
	defer setRuntimeNamespace(t, gateway)
	setRuntimeNamespace(t, downstream)
	listener, err := net.ListenPacket("udp6", "[2001:db8:b01:fe::2]:53206")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, upstream)
	connection, err := net.DialTimeout("udp6", "[2001:db8:53::1]:53206", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("service-request")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, downstream)
	if err := listener.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, sender, err := listener.ReadFrom(buffer)
	if err != nil || string(buffer[:count]) != "service-request" {
		t.Fatalf("service provider request: count=%d data=%q error=%v", count, buffer[:count], err)
	}
	if _, err := listener.WriteTo([]byte("service-reply"), sender); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, upstream)
	count, err = connection.Read(buffer)
	setRuntimeNamespace(t, gateway)
	if err != nil || string(buffer[:count]) != "service-reply" {
		t.Fatalf("service provider reply: count=%d data=%q error=%v", count, buffer[:count], err)
	}
}
