//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/networkload"
)

const (
	dhcpv6RuntimeChildEnv  = "MWAN_DHCPV6_RUNTIME_TEST_CHILD"
	dhcpv6RuntimeBinaryEnv = "MWAN_DHCPV6_RUNTIME_TEST_BINARY"
)

func TestOwnedDHCPv6PDDaemonRuntime(t *testing.T) {
	testOwnedDHCPv6PDDaemonRuntime(t, false)
}

func TestOwnedDHCPv6PDDaemonWaitsForRA(t *testing.T) {
	testOwnedDHCPv6PDDaemonRuntime(t, true)
}

func TestOwnedDHCPv6PDRejectsInformationRequest(t *testing.T) {
	directory := t.TempDir()
	writeDHCPv6PDRuntimeNetwork(t, directory, "information-request")
	_, err := networkload.Load(filepath.Join(directory, "network.json"), filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err == nil || !strings.Contains(err.Error(), "without-ra information-request cannot acquire an IA_NA address or IA_PD prefix") {
		t.Fatalf("information-request rejection = %v", err)
	}
}

func testOwnedDHCPv6PDDaemonRuntime(t *testing.T, waitForRA bool) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6PDDaemonRuntime(t, waitForRA)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv6PDDaemonRuntime(t *testing.T, waitForRA bool) {
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
	withoutRA := "solicit"
	if waitForRA {
		withoutRA = "no"
	}
	writeDHCPv6PDRuntimeNetwork(t, networkDir, withoutRA)
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
	configureRuntimeLink(t, "wan-vlan", []string{"2001:db8:2::2/64"})
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
	service := startDHCPv6PDRuntimeKea(t, root, "2001:db8:30::", 56)
	t.Cleanup(func() {
		if t.Failed() {
			log, _ := os.ReadFile(service.logPath)
			for _, line := range strings.Split(string(log), "\n") {
				if strings.Contains(line, "PREFIX") || strings.Contains(line, "ALLOC") || strings.Contains(line, "PACKET_RECEIVED") {
					t.Logf("Kea DHCPv6: %s", line)
				}
			}
		}
	})
	setRuntimeNamespace(t, gateway)
	serviceRunning := true
	defer func() {
		if serviceRunning {
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
			setRuntimeNamespace(t, gateway)
		}
	}()
	daemon := startRuntimeDaemon(t, os.Getenv(dhcpv6RuntimeBinaryEnv), configPath, root, "dhcpv6pd")
	defer stopRuntimeDaemon(t, daemon)
	if waitForRA {
		waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:2::1/64", true)
		time.Sleep(2 * time.Second)
		content, err := os.ReadFile(service.logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "SOLICIT (type 1)") {
			t.Fatalf("Kea received Solicit before RA: %s", content)
		}
		waitDHCPv6RuntimeRASysctl(t, daemon)
		setRuntimeNamespace(t, provider.namespace)
		addMappedRuntimeRoute(t, "2001:db8:30::/60", "2001:db8:2::1", "wan-vlan")
		stopRouter := startDHCPv6RuntimeRouter(t, true)
		defer stopRouter()
		setRuntimeNamespace(t, gateway)
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:2::1/64", true)
	waitDHCPv6RuntimeRASysctl(t, daemon)
	if !waitForRA {
		setRuntimeNamespace(t, provider.namespace)
		addMappedRuntimeRoute(t, "2001:db8:30::/60", "2001:db8:2::1", "wan-vlan")
		stopRouter := startDHCPv6RuntimeRouter(t, false)
		defer stopRouter()
	}
	setRuntimeNamespace(t, gateway)
	waitDHCPv6RuntimeDefault(t, daemon)
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 10*time.Second)
	waitDHCPv6RuntimeSteering(t, daemon)
	waitDHCPv6RuntimeSourceRule(t, daemon, true)
	assertMappedRuntimeReply(t, daemon, gateway, provider.namespace, downstream.namespace, "udp6", "[2001:db8:30::1]:52227", "[2001:db8:b01:fe::2]:52227")
	if waitForRA {
		return
	}
	waitDHCPv6RuntimeLog(t, service.logPath, "RENEW (type 5)")
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	listenDHCPv6RuntimeRebind(t)
	setRuntimeNamespace(t, gateway)
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", false)
	waitDHCPv6RuntimeSourceRule(t, daemon, false)
	setRuntimeNamespace(t, provider.namespace)
	service = startDHCPv6PDRuntimeKea(t, root, "2001:db8:40::", 60)
	serviceRunning = true
	setRuntimeNamespace(t, gateway)
	waitDHCPv6RuntimeAddress(t, daemon, "2001:db8:40::1/128")
	waitDHCPv6RuntimeNPTRule(t, daemon, false)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	served, stopServing := startDHCPv6RuntimeUnequalServer(t)
	defer stopServing()
	setRuntimeNamespace(t, gateway)
	waitDHCPv6RuntimeAddress(t, daemon, "2001:db8:30::1/128")
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve unequal DHCPv6 prefixes: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("DHCPv6 Request for unequal prefixes did not complete")
	}
	waitDHCPv6RuntimeDefault(t, daemon)
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 5*time.Second)
	waitDHCPv6RuntimeSourceRule(t, daemon, true)
	waitDHCPv6RuntimePreferredDeadline(t, daemon)
	waitDHCPv6RuntimeAddressGone(t, daemon, "2001:db8:30::1/128")
	waitDHCPv6RuntimeSourceRule(t, daemon, false)
	waitDHCPv6RuntimeNPTRule(t, daemon, false)
	waitDHCPv6RuntimeAddress(t, daemon, "2001:db8:50::1/128")
}

func waitDHCPv6RuntimePreferredDeadline(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	const reason = `"reason":"dhcpv6 assignment changed"`
	initial := strings.Count(runtimeDaemonLog(t, daemon), reason)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(runtimeDaemonLog(t, daemon), reason) > initial {
			waitDHCPv6RuntimeAddress(t, daemon, "2001:db8:30::1/128")
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("preferred lifetime did not request reconciliation before valid expiry: %s", runtimeLogTail(t, daemon, 20))
}

func startDHCPv6RuntimeUnequalServer(t *testing.T) (<-chan error, func()) {
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
		serverID, err := dhcpv6.DUIDFromBytes([]byte{0, 3, 0, 1, 2, 0, 94, 0, 83, 2})
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
			if !request.Options.RequestedOptions().Contains(dhcpv6.OptionSolMaxRT) {
				served <- fmt.Errorf("%s did not request SOL_MAX_RT", request.MessageType)
				return
			}
			associations := request.Options.IAPD()
			if len(associations) != 1 || request.Options.ClientID() == nil {
				continue
			}
			response := &dhcpv6.Message{MessageType: dhcpv6.MessageTypeAdvertise, TransactionID: request.TransactionID}
			if request.MessageType == dhcpv6.MessageTypeRequest {
				response.MessageType = dhcpv6.MessageTypeReply
			}
			dhcpv6.WithClientID(request.Options.ClientID())(response)
			dhcpv6.WithServerID(serverID)(response)
			response.AddOption(&dhcpv6.OptionGeneric{OptionCode: dhcpv6.OptionSolMaxRT, OptionData: []byte{0, 0, 0, 60}})
			prefixes := dhcpv6.Options{}
			for _, value := range []struct {
				prefix    string
				preferred time.Duration
				valid     time.Duration
			}{
				{prefix: "2001:db8:30::/60", preferred: 6 * time.Second, valid: 15 * time.Second},
				{prefix: "2001:db8:50::/60", preferred: 25 * time.Second, valid: 35 * time.Second},
			} {
				_, network, err := net.ParseCIDR(value.prefix)
				if err != nil {
					served <- err
					return
				}
				prefixes.Add(&dhcpv6.OptIAPrefix{Prefix: network, PreferredLifetime: value.preferred, ValidLifetime: value.valid})
			}
			response.AddOption(&dhcpv6.OptIAPD{IaId: associations[0].IaId, T1: 3 * time.Second, T2: 5 * time.Second, Options: dhcpv6.PDOptions{Options: prefixes}})
			if _, err := listener.WriteTo(response.ToBytes(), peer); err != nil {
				served <- err
				return
			}
			if request.MessageType == dhcpv6.MessageTypeRequest {
				served <- nil
				return
			}
		}
	}()
	return served, func() { listener.Close() }
}

func waitDHCPv6RuntimeNPTRule(t *testing.T, daemon *runtimeDaemon, present bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "table", "ip6", "nat").CombinedOutput()
		if err == nil && strings.Contains(string(output), "2001:db8:30::1") == present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("short-lived NPT rule present=%t not observed: %s", present, runtimeLogTail(t, daemon, 40))
}

func waitDHCPv6RuntimeSteering(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "chain", "inet", "mwan_steer", "forward").CombinedOutput()
		if err == nil && strings.Contains(string(output), `iifname "enmwanbr0" oifname "enatt0" meta nfproto ipv6 meta mark != 0x00000001 drop`) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("IPv6 steering did not become ready: %s", runtimeLogTail(t, daemon, 60))
}

func waitDHCPv6RuntimeSourceRule(t *testing.T, daemon *runtimeDaemon, present bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rules, err := netlink.RuleList(netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, rule := range rules {
			if rule.Priority == 55 && rule.Src != nil && rule.Src.String() == "2001:db8:30::/60" {
				found = true
			}
		}
		if found == present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	rules, _ := netlink.RuleList(netlink.FAMILY_V6)
	routes, _ := netlink.RouteList(nil, netlink.FAMILY_V6)
	t.Fatalf("delegated IPv6 source rule present=%t not observed: rules=%v routes=%v daemon=%s", present, rules, routes, runtimeLogTail(t, daemon, 15))
}

func waitDHCPv6RuntimeRASysctl(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value, err := os.ReadFile("/proc/sys/net/ipv6/conf/enatt0/accept_ra")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(value)) == "2" {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("WAN does not accept RA: %s", runtimeLogTail(t, daemon, 40))
}

func startDHCPv6RuntimeRouter(t *testing.T, managed bool) func() {
	t.Helper()
	link, err := net.InterfaceByName("wan-vlan")
	if err != nil {
		t.Fatal(err)
	}
	connection, _, err := ndp.Listen(link, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	advertisement := &ndp.RouterAdvertisement{RouterLifetime: 60 * time.Second, ManagedConfiguration: managed}
	if err := connection.WriteTo(advertisement, nil, netip.MustParseAddr("ff02::1")); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				connection.WriteTo(advertisement, nil, netip.MustParseAddr("ff02::1"))
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		connection.Close()
	}
}

func waitDHCPv6RuntimeDefault(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteList(nil, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if (route.Dst == nil || route.Dst.IP.IsUnspecified() && len(route.Dst.Mask) == 16 && route.Dst.Mask[0] == 0) && route.Gw != nil && route.Gw.IsLinkLocalUnicast() {
				return
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	routes, _ := netlink.RouteList(nil, netlink.FAMILY_V6)
	t.Fatalf("RA default route missing, routes=%v: %s", routes, runtimeLogTail(t, daemon, 40))
}

func waitDHCPv6RuntimeAddress(t *testing.T, daemon *runtimeDaemon, prefix string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			if address.IPNet.String() == prefix && address.Flags&unix.IFA_F_TENTATIVE == 0 {
				return
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("delegated address %s was not ready: %s", prefix, runtimeLogTail(t, daemon, 40))
}

func waitDHCPv6RuntimeAddressGone(t *testing.T, daemon *runtimeDaemon, prefix string) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		present := false
		for _, address := range addresses {
			if address.IPNet.String() == prefix {
				present = true
			}
		}
		if !present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expired delegated address %s remains installed: %s", prefix, runtimeLogTail(t, daemon, 30))
}

func waitDHCPv6RuntimeLog(t *testing.T, path, phrase string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), phrase) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Kea did not receive %s", phrase)
}

func listenDHCPv6RuntimeRebind(t *testing.T) {
	t.Helper()
	listener, err := net.ListenPacket("udp6", "[::]:547")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	link, err := net.InterfaceByName("wan-vlan")
	if err != nil {
		t.Fatal(err)
	}
	if err := ipv6.NewPacketConn(listener).JoinGroup(link, &net.UDPAddr{IP: net.ParseIP("ff02::1:2")}); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1500)
	for {
		count, _, err := listener.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("DHCPv6 Rebind not received: %v", err)
		}
		message, err := dhcpv6.MessageFromBytes(buffer[:count])
		if err == nil && message.MessageType == dhcpv6.MessageTypeRebind {
			if !message.Options.RequestedOptions().Contains(dhcpv6.OptionSolMaxRT) {
				t.Fatal("Rebind did not request SOL_MAX_RT")
			}
			return
		}
	}
}

func writeDHCPv6PDRuntimeNetwork(t *testing.T, directory, withoutRA string) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	owned := `{"name":"enatt0","type":"iana-if-type:l2vlan","enabled":true,"goodkind-mwan-steering:connection-id":"att","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"wan-under","id":101}},"ietf-ip:ipv6":{"address":[{"ip":"2001:db8:2::1","prefix-length":64}],"goodkind-mwan-steering:dhcp":true,"goodkind-mwan-steering:accept-ra":true,"goodkind-mwan-steering:accept-ra-default-route":true,"goodkind-mwan-steering:delegation":{"hint":"::/56","duid-type":"link-layer-time","duid":"00:01:00:01:2a:5b:3c:4d:02:00:5e:00:53:01","without-ra":"solicit","iaid":227,"use-delegated-prefix":true,"router-lifetime-seconds":1800},"goodkind-mwan-steering:dhcpv6-client":{"prefix-iaid":227,"request-prefix":true,"use-dns":false},"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"delegated","expected-prefix":"2001:db8:30::/60"}}},"goodkind-mwan-steering:wan":{"name":"att","table-id":100,"fw-mark":1,"fw-mark-prio":100,"from-prio":55},"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`
	owned = strings.Replace(owned, `"without-ra":"solicit"`, `"without-ra":"`+withoutRA+`"`, 1)
	interfaces["interface"] = json.RawMessage(`[` + owned + `,{"name":"wan-under","type":"iana-if-type:ethernetCsmacd"},{"name":"enmwanbr0","type":"iana-if-type:other"},{"name":"enmgmt0","type":"iana-if-type:other"}]`)
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "network.json"), output, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startDHCPv6PDRuntimeKea(t *testing.T, root, prefix string, bits int) protocolService {
	t.Helper()
	config := fmt.Sprintf(`{"Dhcp6":{"interfaces-config":{"interfaces":["wan-vlan"]},"lease-database":{"type":"memfile","persist":true,"name":"/var/lib/kea/leases6.csv"},"valid-lifetime":8,"preferred-lifetime":4,"subnet6":[{"id":1,"subnet":"2001:db8:2::/64","interface":"wan-vlan","pd-pools":[{"prefix":%q,"prefix-len":%d,"delegated-len":%d}]}]}}`, prefix, bits, bits)
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
