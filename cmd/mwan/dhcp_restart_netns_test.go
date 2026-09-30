//go:build linux && firewallnetns

package main

import (
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

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	dhcpRestartChildEnv  = "MWAN_DHCP_RESTART_TEST_CHILD"
	dhcpRestartBinaryEnv = "MWAN_DHCP_RESTART_TEST_BINARY"
)

func TestOOBDHCPv4DaemonRestartRecovery(t *testing.T) {
	testOOBDHCPv4DaemonRestartRecovery(t, false, false)
}

func TestOOBDHCPv4DaemonLateInterfaceRecovery(t *testing.T) {
	testOOBDHCPv4DaemonRestartRecovery(t, true, false)
}

func TestOOBDHCPv4DaemonRejectedRecovery(t *testing.T) {
	testOOBDHCPv4DaemonRestartRecovery(t, false, true)
}

func testOOBDHCPv4DaemonRestartRecovery(t *testing.T, lateInterface, rejected bool) {
	if os.Getenv(dhcpRestartChildEnv) == "1" {
		runOOBDHCPv4DaemonRestartRecovery(t, lateInterface, rejected)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpRestartChildEnv+"=1", dhcpRestartBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCP restart test: %v: %s", err, output)
	}
}

func runOOBDHCPv4DaemonRestartRecovery(t *testing.T, lateInterface, rejected bool) {
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
	stateDir := filepath.Join(root, "mwan-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
	setRuntimeLoopback(t)
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	defer provider.namespace.Close()
	parent, err := netlink.LinkByName("wan-under")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "enatt0", ParentIndex: parent.Attrs().Index}, VlanId: 101}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "enatt0", nil)
	setRuntimeNamespace(t, provider.namespace)
	parent, err = netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: parent.Attrs().Index}, VlanId: 101}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "wan-vlan", []string{"198.51.100.2/24"})
	leaseSeconds := 40
	if rejected {
		leaseSeconds = 8
	}
	service := startDHCPv4RuntimeKea(t, root, false, leaseSeconds, "198.51.100.2", "198.51.100.100", true)
	serviceRunning := true
	defer func() {
		if serviceRunning {
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
		}
	}()
	setRuntimeNamespace(t, gateway)
	leaseDirectory := filepath.Join(root, "leases")
	if err := os.Mkdir(leaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"oob\"\nreconcile_interval = \"1h\"\nlease_directory = %q\n[ifmgr.iface.enatt0]\ndhcp_v4 = true\n[ifmgr.modules.oobv6]\niface = \"enatt0\"\noob_addr = \"2001:db8::100/128\"\noob_table_id = 100\nmanage_slaac_source_rule = false\n[ifmgr.modules.oobv4]\niface = \"enatt0\"\noob_table_id = 100\n[ifmgr.modules.ra_lost]\niface = \"enatt0\"\n[[ifmgr.modules.policy_rules.rule]]\nfamily = \"ipv4\"\npriority = 100\nfrom = \"192.0.2.2/32\"\ntable_id = 100\n", leaseDirectory)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv(dhcpRestartBinaryEnv)
	daemon := startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", true)
	waitRoleDHCPv4Route(t, daemon, 100, true)
	waitDHCPRecoveryRecord(t, daemon, filepath.Join(leaseDirectory, "roles"))
	var savedLease *netif.LeaseInfo
	if rejected {
		store, err := netif.NewLeaseRecoveryStore(filepath.Join(leaseDirectory, "roles"))
		if err != nil {
			t.Fatal(err)
		}
		link, err := net.InterfaceByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		savedLease, err = store.LoadDHCPv4("oob/enatt0", link, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	stopRuntimeDaemon(t, daemon)
	var savedHardwareAddr net.HardwareAddr
	if lateInterface {
		link, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		savedHardwareAddr = append(net.HardwareAddr(nil), link.Attrs().HardwareAddr...)
		if err := netlink.LinkDel(link); err != nil {
			t.Fatal(err)
		}
	}
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	if rejected {
		setRuntimeNamespace(t, gateway)
		if untilExpiry := time.Until(savedLease.ExpiresAt); untilExpiry > 0 {
			time.Sleep(untilExpiry + 100*time.Millisecond)
		}
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", true)
		waitRoleDHCPv4Route(t, daemon, 100, true)
		daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", false)
		waitRoleDHCPv4Route(t, daemon, 100, false)
		logContent := runtimeDaemonLog(t, daemon)
		if !strings.Contains(logContent, "dhcp: saved lease unavailable") || !strings.Contains(logContent, "state=EXPIRED") || strings.Contains(logContent, "state=BOUND") {
			t.Fatalf("saved lease rejection not observed: %s", runtimeLogTail(t, daemon, 30))
		}
		entries, err := os.ReadDir(filepath.Join(leaseDirectory, "roles"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected role lease remains saved: entries=%v err=%v", entries, err)
		}
		return
	}
	observer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 67})
	if err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	if lateInterface {
		waitDHCPRecoveryMainLoop(t, daemon)
		if _, err := netlink.LinkByName("enatt0"); !netif.IsLinkNotFound(err) {
			t.Fatalf("DHCP interface existed before late creation: %v", err)
		}
		parent, err := netlink.LinkByName("wan-under")
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "enatt0", ParentIndex: parent.Attrs().Index}, VlanId: 101}); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetHardwareAddr(link, savedHardwareAddr); err != nil {
			t.Fatal(err)
		}
		configureRuntimeLink(t, "enatt0", nil)
	}
	packet := readDHCPRecoveryPacket(t, observer)
	if packet.MessageType() != dhcpv4.MessageTypeRequest || !packet.RequestedIPAddress().Equal(net.IPv4(198, 51, 100, 101)) || !packet.ClientIPAddr.Equal(net.IPv4zero) || packet.Options.Has(dhcpv4.OptionServerIdentifier) {
		t.Fatalf("first restart packet = %s", packet.Summary())
	}
	if lateInterface {
		if err := observer.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", true)
	waitRoleDHCPv4Route(t, daemon, 100, true)
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, provider.namespace)
	service = startDHCPv4RuntimeKea(t, root, false, 40, "198.51.100.2", "198.51.100.100", false)
	serviceRunning = true
	setRuntimeNamespace(t, gateway)
	waitDHCPv4RuntimeState(t, daemon, "state=BOUND", 25*time.Second)
	waitRoleDHCPv4Route(t, daemon, 100, true)
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)
	daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	waitRecoveryExpiry(t, daemon, 50*time.Second)
}

func waitDHCPRecoveryMainLoop(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		logContent := runtimeDaemonLog(t, daemon)
		if strings.Contains(logContent, `"phase":"initial-reconcile"`) && strings.Contains(logContent, "ifmgr: entering main loop") {
			assertRuntimeDaemonRunning(t, daemon)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon did not finish startup reconciliation before interface creation: %s", runtimeLogTail(t, daemon, 30))
}

func readDHCPRecoveryPacket(t *testing.T, observer *net.UDPConn) *dhcpv4.DHCPv4 {
	t.Helper()
	if err := observer.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 1500)
	for {
		count, _, err := observer.ReadFromUDP(packet)
		if err != nil {
			t.Fatalf("read first DHCP restart packet: %v", err)
		}
		message, err := dhcpv4.FromBytes(packet[:count])
		if err == nil {
			return message
		}
	}
}

func waitDHCPRecoveryRecord(t *testing.T, daemon *runtimeDaemon, directory string) {
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
	t.Fatalf("DHCP recovery record absent: %s", runtimeLogTail(t, daemon, 30))
}

func waitRecoveryExpiry(t *testing.T, daemon *runtimeDaemon, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			t.Fatal(err)
		}
		assigned := false
		for _, address := range addresses {
			if address.IPNet.String() == "198.51.100.101/24" {
				assigned = true
			}
		}
		if !assigned {
			waitRoleDHCPv4Route(t, daemon, 100, false)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("saved DHCPv4 assignment did not expire: %s", runtimeLogTail(t, daemon, 40))
}
