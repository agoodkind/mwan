//go:build linux && protocol

package commandline

import (
	"errors"
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
	"goodkind.io/mwan/gateway/internal/netif"
)

const (
	dhcpRestartChildEnv  = "MWAN_DHCP_RESTART_TEST_CHILD"
	dhcpRestartBinaryEnv = "MWAN_DHCP_RESTART_TEST_BINARY"
)

func TestOwnedDHCPv4DaemonRestartRecovery(t *testing.T) {
	testOwnedDHCPv4DaemonRestartRecovery(t, false, false)
}

func TestOwnedDHCPv4DaemonLateInterfaceRecovery(t *testing.T) {
	testOwnedDHCPv4DaemonRestartRecovery(t, true, false)
}

func TestOwnedDHCPv4DaemonRejectedRecovery(t *testing.T) {
	testOwnedDHCPv4DaemonRestartRecovery(t, false, true)
}

func testOwnedDHCPv4DaemonRestartRecovery(t *testing.T, lateInterface, rejected bool) {
	if os.Getenv(dhcpRestartChildEnv) == "1" {
		runOwnedDHCPv4DaemonRestartRecovery(t, lateInterface, rejected)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), dhcpRestartChildEnv+"=1", dhcpRestartBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCP restart test: %v: %s", err, output)
	}
}

func runOwnedDHCPv4DaemonRestartRecovery(t *testing.T, lateInterface, rejected bool) {
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
	setRuntimeLoopback(t)
	downstream := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.namespace.Close()
	provider := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	leaseSeconds := 40
	if rejected {
		leaseSeconds = 8
	}
	service := startDHCPv4RuntimeKea(
		t,
		root,
		false,
		leaseSeconds,
		"198.51.100.2",
		"198.51.100.100",
		true,
	)
	serviceRunning := true
	defer func() {
		if serviceRunning {
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
		}
	}()
	setRuntimeNamespace(t, gateway)
	configPath := prepareDHCPv4WAN(t, root, true, false)
	leaseDirectory := filepath.Join(root, "leases", "wan")
	binary := os.Getenv(dhcpRestartBinaryEnv)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	waitDHCPRecoveryRecord(t, daemon, leaseDirectory)
	var savedLease *netif.LeaseInfo
	if rejected {
		store, err := netif.NewLeaseRecoveryStore(leaseDirectory)
		if err != nil {
			t.Fatal(err)
		}
		link, err := net.InterfaceByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		savedLease, err = store.LoadDHCPv4("att", link, []byte{1, 0xaa, 0xbb})
		if err != nil {
			t.Fatal(err)
		}
	}
	stopRuntimeDaemon(t, daemon)
	if lateInterface {
		renameDHCPWANParent(t, "wan-under", "wan-under-old")
	}
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	if rejected {
		setRuntimeNamespace(t, gateway)
		if untilExpiry := time.Until(savedLease.ExpiresAt); untilExpiry > 0 {
			time.Sleep(untilExpiry + 100*time.Millisecond)
		}
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
		waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
		daemon = startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", false)
		waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
		logContent := runtimeDaemonLog(t, daemon)
		if !strings.Contains(logContent, "addresses: DHCPv4 saved lease rejected") ||
			strings.Contains(logContent, "state=BOUND") {
			t.Fatalf("saved lease rejection not observed: %s", runtimeLogTail(t, daemon, 30))
		}
		entries, err := os.ReadDir(leaseDirectory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected WAN lease remains saved: entries=%v err=%v", entries, err)
		}
		return
	}
	observer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 67})
	if err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	if lateInterface {
		waitDHCPRecoveryMainLoop(t, daemon)
		if err := observer.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}

		if count, _, err := observer.ReadFromUDP(make([]byte, 1500)); !errors.Is(
			err,
			os.ErrDeadlineExceeded,
		) {
			t.Fatalf("DHCP received %d bytes before the VLAN parent returned: %v", count, err)
		}

		renameDHCPWANParent(t, "wan-under-old", "wan-under")
	}
	packet := readDHCPRecoveryPacket(t, observer)
	if packet.MessageType() != dhcpv4.MessageTypeRequest ||
		!packet.RequestedIPAddress().Equal(net.IPv4(198, 51, 100, 100)) ||
		!packet.ClientIPAddr.Equal(net.IPv4zero) ||
		packet.Options.Has(dhcpv4.OptionServerIdentifier) {
		t.Fatalf("first restart packet = %s", packet.Summary())
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}

	if lateInterface {
		return
	}

	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	setRuntimeNamespace(t, provider.namespace)
	service = startDHCPv4RuntimeKea(t, root, false, 40, "198.51.100.2", "198.51.100.100", false)
	serviceRunning = true
	setRuntimeNamespace(t, gateway)
	waitDHCPv4RuntimeState(t, daemon, "state=BOUND", 25*time.Second)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	waitRecoveryExpiry(t, daemon, 50*time.Second)
}

func waitDHCPRecoveryMainLoop(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		logContent := runtimeDaemonLog(t, daemon)
		if strings.Contains(logContent, `"phase":"initial-reconcile"`) &&
			strings.Contains(logContent, "ifmgr: entering main loop") {
			assertRuntimeDaemonRunning(t, daemon)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"daemon did not finish startup reconciliation before interface creation: %s",
		runtimeLogTail(t, daemon, 30),
	)
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
			if address.IPNet.String() == "198.51.100.100/24" {
				assigned = true
			}
		}
		if !assigned {
			waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("saved DHCPv4 assignment did not expire: %s", runtimeLogTail(t, daemon, 40))
}
