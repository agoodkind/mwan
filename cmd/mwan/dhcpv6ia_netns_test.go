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

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/networkjson"
)

func TestOwnedDHCPv6IAAddressOnlyRuntime(t *testing.T) {
	testOwnedDHCPv6IARuntime(t, false, false, false)
}

func TestOwnedDHCPv6IACombinedRuntime(t *testing.T) {
	testOwnedDHCPv6IARuntime(t, true, false, false)
}

func TestOwnedDHCPv6IAUnequalLifetimesRuntime(t *testing.T) {
	testOwnedDHCPv6IARuntime(t, true, true, false)
}

func TestOwnedDHCPv6IADuplicateRuntime(t *testing.T) {
	testOwnedDHCPv6IARuntime(t, true, false, true)
}

func testOwnedDHCPv6IARuntime(t *testing.T, combined, unequal, duplicate bool) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6IARuntime(t, combined, unequal, duplicate)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 IA daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv6IARuntime(t *testing.T, combined, unequal, duplicate bool) {
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
	writeDHCPv6IARuntimeNetwork(t, networkDir, combined)
	if _, err := networkjson.Load(filepath.Join(networkDir, "network.json"), schemaDir); err != nil {
		t.Fatalf("load runtime network: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = true\n", filepath.Join(root, "owned-links.json"), filepath.Join(root, "owned-addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"}, "")
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	parent, err := netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: parent.Attrs().Index}, VlanId: 101}); err != nil {
		t.Fatal(err)
	}
	providerAddresses := []string{"2001:db8:2::2/64"}
	if duplicate {
		providerAddresses = append(providerAddresses, "2001:db8:2::100/128")
	}
	configureRuntimeLink(t, "wan-vlan", providerAddresses)
	waitAutoconfigurationLinkLocal(t, "wan-vlan")
	setRuntimeNamespace(t, downstream.namespace)
	addMappedRuntimeRoute(t, "2001:db8:2::/64", "2001:db8:b01:fe::3", "lan-host")
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	setRuntimeNamespace(t, provider.namespace)
	for _, directory := range []string{filepath.Join(root, "kea-run"), filepath.Join(root, "kea-state")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bindStartupDirectory(t, filepath.Join(root, "kea-run"), "/run/kea")
	bindStartupDirectory(t, filepath.Join(root, "kea-state"), "/var/lib/kea")
	service := startDHCPv6IARuntimeKea(t, root, combined)
	serviceRunning := true
	setRuntimeNamespace(t, gateway)
	defer func() {
		if serviceRunning {
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
			setRuntimeNamespace(t, gateway)
		}
	}()
	var addressUpdates chan netlink.AddrUpdate
	if duplicate {
		addressUpdates = make(chan netlink.AddrUpdate, 128)
		done := make(chan struct{})
		if err := netlink.AddrSubscribe(addressUpdates, done); err != nil {
			t.Fatal(err)
		}
		defer close(done)
	}
	daemon := startRuntimeDaemon(t, os.Getenv(dhcpv6RuntimeBinaryEnv), configPath, root, "dhcpv6ia")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	if duplicate {
		assertDHCPv6IADuplicate(t, daemon, root, addressUpdates)
		return
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:2::100/128", true)
	waitDHCPv6IALeaseRecord(t, daemon, filepath.Join(root, "kea-state", "leases6.csv"), "2001:db8:2::100", "0")
	if combined {
		waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
		waitDHCPv6IALeaseRecord(t, daemon, filepath.Join(root, "kea-state", "leases6.csv"), "2001:db8:30::", "2")
	}
	assertDHCPv6IALocalPacket(t, daemon, gateway, provider.namespace, "[2001:db8:2::100]:52228")
	if !unequal {
		return
	}
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	addMappedRuntimeRoute(t, "2001:db8:30::/60", "2001:db8:2::1", "wan-vlan")
	served, stopServer := startDHCPv6IAUnequalServer(t)
	defer stopServer()
	stopRouter := startDHCPv6RuntimeRouter(t, true)
	defer stopRouter()
	setRuntimeNamespace(t, gateway)
	daemon = startRuntimeDaemon(t, os.Getenv(dhcpv6RuntimeBinaryEnv), configPath, root, "dhcpv6ia-unequal")
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::100/128", true)
	waitDHCPv6RuntimeDefault(t, daemon)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve unequal DHCPv6 associations: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("DHCPv6 Request with unequal associations did not complete")
	}
	stopServer()
	assertDHCPv6IALocalPacket(t, daemon, gateway, provider.namespace, "[2001:db8:30::100]:52229")
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 10*time.Second)
	waitDHCPv6RuntimeSourceRule(t, daemon, true)
	assertMappedRuntimeReply(t, daemon, gateway, provider.namespace, downstream.namespace,
		"udp6", "[2001:db8:30::1]:52227", "[2001:db8:b01:fe::2]:52227")
	waitDHCPv6RuntimeAddressGone(t, daemon, "2001:db8:30::100/128")
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
}

func assertDHCPv6IADuplicate(t *testing.T, daemon *runtimeDaemon, root string, updates <-chan netlink.AddrUpdate) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	logPath := filepath.Join(root, "kea-dhcp6.log")
	var observed []netlink.Addr
	var observedUpdates []string
	dadFailed := false
	for time.Now().Before(deadline) {
		for {
			select {
			case update := <-updates:
				if update.LinkAddress.String() != "2001:db8:2::100/128" {
					continue
				}
				observedUpdates = append(observedUpdates, fmt.Sprintf("new=%t flags=%d", update.NewAddr, update.Flags))
				if update.Flags&unix.IFA_F_DADFAILED != 0 {
					dadFailed = true
				}
				if update.NewAddr && update.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
					t.Fatalf("duplicate IA_NA address became ready in netlink event: %+v", update)
				}
			default:
				goto observeKernel
			}
		}
	observeKernel:
		link, err := netlink.LinkByName("enatt0")
		if err == nil {
			addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
			if err != nil {
				t.Fatal(err)
			}
			observed = addresses
			for _, address := range addresses {
				if address.IPNet.String() == "2001:db8:2::100/128" && address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
					t.Fatalf("duplicate IA_NA address became ready: %+v", address)
				}
			}
		} else if !strings.Contains(err.Error(), "not found") {
			t.Fatal(err)
		}
		content, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "DECLINE (type 9) received") {
			if !dadFailed {
				t.Fatal("Kea received Decline without an observed DAD failure")
			}
			waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(25 * time.Millisecond)
	}
	content, _ := os.ReadFile(logPath)
	var keaPackets []string
	for _, line := range strings.Split(string(content), "\n") {
		if strings.Contains(line, "DHCP6_PACKET_") || strings.Contains(line, "DHCP6_DECLINE") {
			keaPackets = append(keaPackets, line)
		}
	}
	var daemonEvents []string
	for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
		if strings.Contains(line, `"module":"addresses"`) &&
			(strings.Contains(line, `"level":"WARN"`) || strings.Contains(line, `"level":"ERROR"`)) {
			daemonEvents = append(daemonEvents, line)
		}
	}
	if len(daemonEvents) > 5 {
		daemonEvents = daemonEvents[len(daemonEvents)-5:]
	}
	var addressStates []string
	for _, address := range observed {
		addressStates = append(addressStates, fmt.Sprintf("%s flags=%d", address.IPNet, address.Flags))
	}
	t.Fatalf("Kea did not receive DHCPv6 Decline: dad_failed=%t addresses=%v updates=%v Kea packets=%v daemon events=%v", dadFailed, addressStates, observedUpdates, keaPackets, daemonEvents)
}

func writeDHCPv6IARuntimeNetwork(t *testing.T, directory string, combined bool) {
	t.Helper()
	writeDHCPv6PDRuntimeNetwork(t, directory, "solicit")
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
	var ipv6 map[string]json.RawMessage
	if err := json.Unmarshal(entries[0]["ietf-ip:ipv6"], &ipv6); err != nil {
		t.Fatal(err)
	}
	var client map[string]json.RawMessage
	if err := json.Unmarshal(ipv6["goodkind-mwan-steering:dhcpv6-client"], &client); err != nil {
		t.Fatal(err)
	}
	client["address-iaid"] = json.RawMessage(`227`)
	client["request-address"] = json.RawMessage(`true`)
	if !combined {
		delete(ipv6, "goodkind-mwan-steering:delegation")
		ipv6["goodkind-mwan-steering:translation"] = json.RawMessage(`{"mode":"native"}`)
		client["duid"] = json.RawMessage(`"00:01:00:01:2a:5b:3c:4d:02:00:5e:00:53:01"`)
		client["without-ra"] = json.RawMessage(`"solicit"`)
		client["request-prefix"] = json.RawMessage(`false`)
	}
	ipv6["goodkind-mwan-steering:dhcpv6-client"], err = json.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	entries[0]["ietf-ip:ipv6"], err = json.Marshal(ipv6)
	if err != nil {
		t.Fatal(err)
	}
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, output, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startDHCPv6IARuntimeKea(t *testing.T, root string, combined bool) protocolService {
	t.Helper()
	prefixPool := ""
	if combined {
		prefixPool = `,"pd-pools":[{"prefix":"2001:db8:30::","prefix-len":56,"delegated-len":56}]`
	}
	config := `{"Dhcp6":{"interfaces-config":{"interfaces":["wan-vlan"]},"lease-database":{"type":"memfile","persist":true,"name":"/var/lib/kea/leases6.csv"},"valid-lifetime":16,"preferred-lifetime":8,"subnet6":[{"id":1,"subnet":"2001:db8:2::/64","interface":"wan-vlan","pools":[{"pool":"2001:db8:2::100-2001:db8:2::100"}]` + prefixPool + `}]}}`
	configPath := filepath.Join(root, "kea-dhcp6.conf")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "kea-dhcp6.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("kea-dhcp6", "-c", configPath, "-d")
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	logFile.Close()
	service := protocolService{name: "kea-dhcp6", command: command, logPath: logPath}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(logPath); err == nil && strings.Contains(string(content), "DHCP6_STARTED") {
			return service
		}
		time.Sleep(25 * time.Millisecond)
	}
	content, _ := os.ReadFile(logPath)
	t.Fatalf("Kea DHCPv6 did not start: %s", content)
	return service
}

func startDHCPv6IAUnequalServer(t *testing.T) (<-chan error, func()) {
	t.Helper()
	listener, err := net.ListenPacket("udp6", "[::]:547")
	if err != nil {
		t.Fatal(err)
	}
	link, err := net.InterfaceByName("wan-vlan")
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if err := ipv6.NewPacketConn(listener).JoinGroup(link, &net.UDPAddr{IP: net.ParseIP("ff02::1:2")}); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		defer listener.Close()
		serverID, err := dhcpv6.DUIDFromBytes([]byte{0, 3, 0, 1, 2, 0, 94, 0, 83, 3})
		if err != nil {
			served <- err
			return
		}
		_, prefix, err := net.ParseCIDR("2001:db8:30::/60")
		if err != nil {
			served <- err
			return
		}
		buffer := make([]byte, 1500)
		for {
			count, peer, err := listener.ReadFrom(buffer)
			if err != nil {
				served <- err
				return
			}
			request, err := dhcpv6.MessageFromBytes(buffer[:count])
			if err != nil || request.MessageType != dhcpv6.MessageTypeSolicit && request.MessageType != dhcpv6.MessageTypeRequest {
				continue
			}
			prefixes := request.Options.IAPD()
			addresses := request.Options.IANA()
			if len(prefixes) != 1 || len(addresses) != 1 || request.Options.ClientID() == nil {
				served <- fmt.Errorf("%s requested %d IA_PD and %d IA_NA associations", request.MessageType, len(prefixes), len(addresses))
				return
			}
			responseType := dhcpv6.MessageTypeAdvertise
			if request.MessageType == dhcpv6.MessageTypeRequest {
				responseType = dhcpv6.MessageTypeReply
			}
			response := &dhcpv6.Message{MessageType: responseType, TransactionID: request.TransactionID}
			dhcpv6.WithClientID(request.Options.ClientID())(response)
			dhcpv6.WithServerID(serverID)(response)
			response.AddOption(&dhcpv6.OptionGeneric{OptionCode: dhcpv6.OptionSolMaxRT, OptionData: []byte{0, 0, 0, 60}})
			prefixOptions := dhcpv6.PDOptions{}
			prefixOptions.Add(&dhcpv6.OptIAPrefix{Prefix: prefix, PreferredLifetime: 20 * time.Second, ValidLifetime: 30 * time.Second})
			response.AddOption(&dhcpv6.OptIAPD{IaId: prefixes[0].IaId, T1: 10 * time.Second, T2: 20 * time.Second, Options: prefixOptions})
			addressOptions := dhcpv6.IdentityOptions{}
			addressOptions.Add(&dhcpv6.OptIAAddress{IPv6Addr: net.ParseIP("2001:db8:30::100"), PreferredLifetime: 2 * time.Second, ValidLifetime: 6 * time.Second})
			response.AddOption(&dhcpv6.OptIANA{IaId: addresses[0].IaId, T1: 2 * time.Second, T2: 4 * time.Second, Options: addressOptions})
			if _, err := listener.WriteTo(response.ToBytes(), peer); err != nil {
				served <- err
				return
			}
			if responseType == dhcpv6.MessageTypeReply {
				served <- nil
				return
			}
		}
	}()
	return served, func() { listener.Close() }
}

func waitDHCPv6IALeaseRecord(t *testing.T, daemon *runtimeDaemon, path, address, leaseType string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(string(content), "\n") {
				fields := strings.Split(line, ",")
				if len(fields) > 6 && fields[0] == address && fields[6] == leaseType {
					return
				}
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	content, _ := os.ReadFile(path)
	t.Fatalf("Kea did not record %s lease %s: %s; daemon: %s", leaseType, address, content, runtimeLogTail(t, daemon, 30))
}

func assertDHCPv6IALocalPacket(t *testing.T, daemon *runtimeDaemon, gateway, upstream netns.NsHandle, address string) {
	t.Helper()
	setRuntimeNamespace(t, gateway)
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	permit := exec.Command("nft", "insert", "rule", "inet", "filter", "input",
		"iifname", "enatt0", "ip6", "saddr", "2001:db8:2::2", "ip6", "daddr", host,
		"udp", "dport", port, "accept")
	if output, err := permit.CombinedOutput(); err != nil {
		t.Fatalf("permit test IA_NA listener: %v: %s", err, output)
	}
	listener, err := net.ListenPacket("udp6", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, upstream)
	connection, err := net.DialTimeout("udp6", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("local-ia-na")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	if err := listener.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, _, err := listener.ReadFrom(buffer)
	if err != nil || string(buffer[:count]) != "local-ia-na" {
		t.Fatalf("local IA_NA packet: count=%d data=%q error=%v: %s", count, buffer[:count], err, runtimeLogTail(t, daemon, 30))
	}
}
