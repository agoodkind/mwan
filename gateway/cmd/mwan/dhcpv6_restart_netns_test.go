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
	"github.com/insomniacslk/dhcp/iana"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	dhcpv6RestartChildEnv  = "MWAN_DHCPV6_RESTART_TEST_CHILD"
	dhcpv6RestartBinaryEnv = "MWAN_DHCPV6_RESTART_TEST_BINARY"
	dhcpv6RestartCaseEnv   = "MWAN_DHCPV6_RESTART_TEST_CASE"
)

func TestOwnedDHCPv6DaemonRestartRecovery(t *testing.T) {
	if os.Getenv(dhcpv6RestartChildEnv) == "1" {
		runOwnedDHCPv6DaemonRestartRecovery(t)
		return
	}
	binary := protocolTestBinary(t)
	for _, scenario := range []string{"validation-and-expiry", "rejection", "withdrawal", "prefix-expiry"} {
		t.Run(scenario, func(t *testing.T) {
			runDHCPv6RestartChild(t, binary, scenario)
		})
	}
}

func runDHCPv6RestartChild(t *testing.T, binary, scenario string) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.v", "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpv6RestartChildEnv+"=1", dhcpv6RestartBinaryEnv+"="+binary, dhcpv6RestartCaseEnv+"="+scenario)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated DHCPv6 restart test: %v: %s", err, output)
	}
	t.Logf("isolated DHCPv6 restart result: %s", output)
}

func runOwnedDHCPv6DaemonRestartRecovery(t *testing.T) {
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
	writeDHCPv6PDRuntimeNetwork(t, networkDir, "solicit")
	leaseDirectory := filepath.Join(root, "leases")
	journalDirectory := filepath.Join(leaseDirectory, "wan")
	if err := os.MkdirAll(journalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\nlease_directory = %q\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = true\n", leaseDirectory, filepath.Join(root, "owned-links.json"), filepath.Join(root, "owned-addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
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
	setRuntimeNamespace(t, provider.namespace)
	for _, directory := range []string{filepath.Join(root, "kea-run"), filepath.Join(root, "kea-state")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bindStartupDirectory(t, filepath.Join(root, "kea-run"), "/run/kea")
	bindStartupDirectory(t, filepath.Join(root, "kea-state"), "/var/lib/kea")
	service := startDHCPv6RestartKea(t, root)
	addMappedRuntimeRoute(t, "2001:db8:30::/60", "2001:db8:2::1", "wan-vlan")
	stopRouter := startDHCPv6RuntimeRouter(t, false)
	defer stopRouter()
	serviceRunning := true
	defer func() {
		if serviceRunning {
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
		}
	}()
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startRuntimeDaemon(t, os.Getenv(dhcpv6RestartBinaryEnv), configPath, root, "dhcpv6-first")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
	waitDHCPv6RestartRecord(t, daemon, journalDirectory)
	waitDHCPv6RuntimeDefault(t, daemon)
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 5*time.Second)
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	observer := listenDHCPv6Restart(t)
	defer observer.Close()
	setRuntimeNamespace(t, gateway)
	for attempt := 0; attempt < 3; attempt++ {
		stopWatching := watchDHCPv6RestartAddress(t)
		daemon = startRuntimeDaemon(t, os.Getenv(dhcpv6RestartBinaryEnv), configPath, root, fmt.Sprintf("dhcpv6-validated-%d", attempt))
		request, peer := readDHCPv6RestartPacket(t, observer)
		if request.MessageType != dhcpv6.MessageTypeRebind || len(request.Options.IAPD()) != 1 {
			t.Fatalf("validated restart did not send Rebind: %s", request)
		}
		time.Sleep(750 * time.Millisecond)
		assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"), "2001:db8:30::1/128")
		waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
		waitDHCPv6RuntimeNPTRule(t, daemon, false)
		switch os.Getenv(dhcpv6RestartCaseEnv) {
		case "rejection":
			rejectDHCPv6Restart(t, observer, request, peer)
			waitDHCPv6RuntimeLog(t, daemon.logPath, "dhcpv6 recovery completed")
			waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", false)
			assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
			waitDHCPv6RestartRecordGone(t, daemon, journalDirectory)
			return
		case "withdrawal":
			stopRuntimeDaemon(t, daemon)
			withdrawDHCPv6RestartNPT(t, networkDir)
			daemon = startRuntimeDaemon(t, os.Getenv(dhcpv6RestartBinaryEnv), configPath, root, "dhcpv6-withdrawn-npt")
			waitDHCPv6RestartEdgeReleased(t, daemon, filepath.Join(root, "owned-addresses.json"))
			waitDHCPv6RuntimeNPTRule(t, daemon, false)
			return
		}
		replyDHCPv6Restart(t, observer, request, peer)
		waitDHCPv6RuntimeLog(t, daemon.logPath, "dhcpv6 recovery completed")
		waitDHCPv6RuntimeLog(t, daemon.logPath, "dhcpv6 assignment changed")
		waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
		waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 5*time.Second)
		stopWatching()
		stopRuntimeDaemon(t, daemon)
		if os.Getenv(dhcpv6RestartCaseEnv) == "prefix-expiry" {
			daemon = startRuntimeDaemon(t, os.Getenv(dhcpv6RestartBinaryEnv), configPath, root, "dhcpv6-own-prefix-expiry")
			request, _ := readDHCPv6RestartPacket(t, observer)
			if request.MessageType != dhcpv6.MessageTypeRebind || len(request.Options.IAPD()) != 1 || len(request.Options.IAPD()[0].Options.Prefixes()) != 2 {
				t.Fatalf("cached multi-prefix restart did not send both prefixes: %s", request)
			}
			waitDHCPv6RestartEdgeReleased(t, daemon, filepath.Join(root, "owned-addresses.json"))
			waitDHCPv6RuntimeNPTRule(t, daemon, false)
			assertDHCPv6OtherPrefixValid(t, journalDirectory)
			return
		}
	}
	validUntil := savedDHCPv6RestartDeadline(t, journalDirectory)
	daemon = startRuntimeDaemon(t, os.Getenv(dhcpv6RestartBinaryEnv), configPath, root, "dhcpv6-restart")
	first, _ := readDHCPv6RestartPacket(t, observer)
	if first.MessageType != dhcpv6.MessageTypeRebind || len(first.Options.IAPD()) != 1 || len(first.Options.IAPD()[0].Options.Prefixes()) != 1 {
		t.Fatalf("first restart packet did not validate saved prefix: %s", first)
	}
	if entries, err := os.ReadDir(journalDirectory); err != nil || len(entries) != 1 {
		t.Fatalf("saved DHCPv6 assignment removed during validation: entries=%v err=%v", entries, err)
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
	waitDHCPv6RuntimeNPTRule(t, daemon, false)
	second, _ := readDHCPv6RestartPacket(t, observer)
	for second.MessageType == dhcpv6.MessageTypeRebind && second.TransactionID == first.TransactionID {
		second, _ = readDHCPv6RestartPacket(t, observer)
	}
	if second.MessageType != dhcpv6.MessageTypeRebind || second.TransactionID == first.TransactionID {
		t.Fatalf("unanswered first exchange did not retry Rebind: %s", second)
	}
	if entries, err := os.ReadDir(journalDirectory); err != nil || len(entries) != 1 {
		t.Fatalf("saved DHCPv6 assignment removed after one unanswered exchange: entries=%v err=%v", entries, err)
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
	for {
		message, _ := readDHCPv6RestartPacket(t, observer)
		if message.MessageType == dhcpv6.MessageTypeSolicit {
			observedAt := time.Now()
			if observedAt.Before(validUntil) {
				lines := strings.Split(runtimeDaemonLog(t, daemon), "\n")
				if len(lines) > 20 {
					lines = lines[len(lines)-20:]
				}
				t.Fatalf("fresh Solicit at %s preceded original valid deadline %s by %s; daemon log tail:\n%s", observedAt, validUntil, validUntil.Sub(observedAt), strings.Join(lines, "\n"))
			}
			break
		}
		if message.MessageType != dhcpv6.MessageTypeRebind {
			t.Fatalf("unexpected packet before restart timeout: %s", message)
		}
	}
	waitDHCPv6RestartRecordGone(t, daemon, journalDirectory)
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", false)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
}

func waitDHCPv6RestartEdgeReleased(t *testing.T, daemon *runtimeDaemon, path string) {
	t.Helper()
	// Kernel deletion precedes receipt persistence in the address reconciler.
	deadline := time.Now().Add(10 * time.Second)
	var data []byte
	var addresses []netlink.Addr
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName("enatt0")
		if netif.IsLinkNotFound(err) {
			assertRuntimeDaemonRunning(t, daemon)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		addresses, err = netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		addressPresent := false
		for _, address := range addresses {
			addressPresent = addressPresent || address.IPNet.String() == "2001:db8:30::1/128"
		}
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var journal struct {
			Objects []struct {
				Scope string `json:"scope"`
			} `json:"objects"`
		}
		if err := json.Unmarshal(data, &journal); err != nil {
			t.Fatal(err)
		}
		present := false
		for _, record := range journal.Objects {
			present = present || record.Scope == "npt-edge"
		}
		if !addressPresent && !present {
			assertRuntimeNPTEdges(t, path)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("NPT edge release incomplete: addresses=%v journal=%s: %s", addresses, data, runtimeLogTail(t, daemon, 50))
}

func withdrawDHCPv6RestartNPT(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "network.json")
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := `"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"delegated","expected-prefix":"2001:db8:30::/60"}`
	if !strings.Contains(string(document), policy) {
		t.Fatal("delegated NPT configuration is absent")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(document), policy, `"mode":"native"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rejectDHCPv6Restart(t *testing.T, listener *net.UDPConn, request *dhcpv6.Message, peer *net.UDPAddr) {
	t.Helper()
	serverID, err := dhcpv6.DUIDFromBytes([]byte{0, 3, 0, 1, 2, 0, 94, 0, 83, 2})
	if err != nil {
		t.Fatal(err)
	}
	response, err := dhcpv6.NewMessage(dhcpv6.WithClientID(request.Options.ClientID()), dhcpv6.WithServerID(serverID))
	if err != nil {
		t.Fatal(err)
	}
	response.MessageType = dhcpv6.MessageTypeReply
	response.TransactionID = request.TransactionID
	response.AddOption(&dhcpv6.OptStatusCode{StatusCode: iana.StatusNoBinding})
	if _, err := listener.WriteToUDP(response.ToBytes(), peer); err != nil {
		t.Fatal(err)
	}
}

func savedDHCPv6RestartDeadline(t *testing.T, directory string) time.Time {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read saved DHCPv6 assignment: entries=%v err=%v", entries, err)
	}
	record, err := netif.NewLeaseStore(filepath.Join(directory, entries[0].Name())).Load("att", netif.LeaseProtocolDHCPv6)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Prefixes []struct {
			ValidUntil time.Time `json:"valid_until"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Prefixes) != 1 || payload.Prefixes[0].ValidUntil.IsZero() {
		t.Fatalf("saved DHCPv6 prefix deadline missing: %+v", payload)
	}
	t.Logf("saved DHCPv6 recovery clock: %+v; prefix valid deadline: %s", record.Clock, payload.Prefixes[0].ValidUntil)
	return payload.Prefixes[0].ValidUntil
}

func assertDHCPv6OtherPrefixValid(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("multi-prefix recovery record is absent: entries=%v err=%v", entries, err)
	}
	record, err := netif.NewLeaseStore(filepath.Join(directory, entries[0].Name())).Load("att", netif.LeaseProtocolDHCPv6)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Prefixes []struct {
			Prefix     string    `json:"prefix"`
			ValidUntil time.Time `json:"valid_until"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range payload.Prefixes {
		if prefix.Prefix == "2001:db8:40::/56" && time.Now().Before(prefix.ValidUntil) {
			t.Logf("matching edge expired while other cached prefix remains valid until %s", prefix.ValidUntil)
			return
		}
	}
	t.Fatalf("other cached prefix was not valid after matching edge removal: %+v", payload)
}

func waitDHCPv6RestartRecord(t *testing.T, daemon *runtimeDaemon, directory string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err == nil && len(entries) == 1 {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	entries, err := os.ReadDir(directory)
	var relevant []string
	for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
		if strings.Contains(line, "DHCPv6") || strings.Contains(line, "lease recovery") || strings.Contains(line, "lease_directory") {
			relevant = append(relevant, line)
		}
	}
	t.Fatalf("saved DHCPv6 assignment absent: entries=%v err=%v logs=%s", entries, err, strings.Join(relevant, "\n"))
}

func startDHCPv6RestartKea(t *testing.T, root string) protocolService {
	t.Helper()
	config := `{"Dhcp6":{"interfaces-config":{"interfaces":["wan-vlan"]},"lease-database":{"type":"memfile","persist":true,"name":"/var/lib/kea/leases6.csv"},"valid-lifetime":30,"preferred-lifetime":20,"subnet6":[{"id":1,"subnet":"2001:db8:2::/64","interface":"wan-vlan","pd-pools":[{"prefix":"2001:db8:30::","prefix-len":56,"delegated-len":56}]}]}}`
	configPath := filepath.Join(root, "kea-dhcp6-restart.conf")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "kea-dhcp6-restart.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("kea-dhcp6", "-c", configPath, "-d")
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		logFile.Close()
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

func listenDHCPv6Restart(t *testing.T) *net.UDPConn {
	t.Helper()
	listener, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6unspecified, Port: dhcpv6.DefaultServerPort})
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
	if err := listener.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	return listener
}

func readDHCPv6RestartPacket(t *testing.T, listener *net.UDPConn) (*dhcpv6.Message, *net.UDPAddr) {
	t.Helper()
	packet := make([]byte, 1500)
	for {
		count, peer, err := listener.ReadFromUDP(packet)
		if err != nil {
			t.Fatalf("read DHCPv6 restart packet: %v", err)
		}
		message, err := dhcpv6.MessageFromBytes(packet[:count])
		if err == nil {
			return message, peer
		}
	}
}

func replyDHCPv6Restart(t *testing.T, listener *net.UDPConn, request *dhcpv6.Message, peer *net.UDPAddr) {
	t.Helper()
	if request.Options.ClientID() == nil {
		t.Fatal("Rebind omitted client ID")
	}
	serverID, err := dhcpv6.DUIDFromBytes([]byte{0, 3, 0, 1, 2, 0, 94, 0, 83, 2})
	if err != nil {
		t.Fatal(err)
	}
	response, err := dhcpv6.NewMessage(dhcpv6.WithClientID(request.Options.ClientID()), dhcpv6.WithServerID(serverID))
	if err != nil {
		t.Fatal(err)
	}
	response.MessageType = dhcpv6.MessageTypeReply
	response.TransactionID = request.TransactionID
	_, prefix, err := net.ParseCIDR("2001:db8:30::/56")
	if err != nil {
		t.Fatal(err)
	}
	association := &dhcpv6.OptIAPD{IaId: request.Options.IAPD()[0].IaId, T1: 8 * time.Second, T2: 16 * time.Second}
	association.Options.Add(&dhcpv6.OptIAPrefix{Prefix: prefix, PreferredLifetime: 16 * time.Second, ValidLifetime: 24 * time.Second})
	if os.Getenv(dhcpv6RestartCaseEnv) == "prefix-expiry" {
		association.Options = dhcpv6.PDOptions{}
		association.T1, association.T2 = time.Second, 2*time.Second
		association.Options.Add(&dhcpv6.OptIAPrefix{Prefix: prefix, PreferredLifetime: 3 * time.Second, ValidLifetime: 4 * time.Second})
		_, other, err := net.ParseCIDR("2001:db8:40::/56")
		if err != nil {
			t.Fatal(err)
		}
		association.Options.Add(&dhcpv6.OptIAPrefix{Prefix: other, PreferredLifetime: 16 * time.Second, ValidLifetime: 20 * time.Second})
	}
	response.AddOption(association)
	if _, err := listener.WriteToUDP(response.ToBytes(), peer); err != nil {
		t.Fatal(err)
	}
}

func watchDHCPv6RestartAddress(t *testing.T) func() {
	t.Helper()
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	const address = "2001:db8:30::1/128"
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, current := range addresses {
		if current.IPNet.String() == address {
			found = true
		}
	}
	if !found {
		t.Fatal("saved NPT address was absent before daemon restart")
	}
	updates := make(chan netlink.AddrUpdate, 128)
	errors := make(chan error, 1)
	done := make(chan struct{})
	if err := netlink.AddrSubscribeWithOptions(updates, done, netlink.AddrSubscribeOptions{
		ErrorCallback: func(subscriptionErr error) {
			select {
			case errors <- subscriptionErr:
			default:
			}
		},
	}); err != nil {
		close(done)
		t.Fatal(err)
	}
	return func() {
		time.Sleep(300 * time.Millisecond)
		close(done)
		for {
			select {
			case update := <-updates:
				if update.LinkIndex == link.Attrs().Index && update.LinkAddress.String() == address && !update.NewAddr {
					t.Fatal("validated Rebind removed the saved NPT address")
				}
			case err := <-errors:
				t.Fatalf("IPv6 address observation failed: %v", err)
			default:
				return
			}
		}
	}
}

func waitDHCPv6RestartRecordGone(t *testing.T, daemon *runtimeDaemon, directory string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err == nil && len(entries) == 0 {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("saved DHCPv6 assignment remained after original expiry: %s", runtimeLogTail(t, daemon, 40))
}
