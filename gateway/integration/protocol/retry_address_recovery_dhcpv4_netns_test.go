//go:build linux && protocol

package protocol_test

import (
	"context"
	"encoding/json"
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

	"github.com/fsnotify/fsnotify"
	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/integration/internal/testenv"
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
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
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
	testenv.MountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	testenv.SetRuntimeLoopback(t)
	downstream := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.Namespace.Close()
	provider := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
	defer provider.Namespace.Close()
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	leaseSeconds := 40
	if rejected {
		leaseSeconds = 8
	}
	service := testenv.StartDHCPv4RuntimeKea(
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
			testenv.SetRuntimeNamespace(t, provider.Namespace)
			testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
		}
	}()
	testenv.SetRuntimeNamespace(t, gateway)
	configPath := prepareDHCPv4WAN(t, root, true, false)
	leaseDirectory := filepath.Join(root, "leases", "wan")
	binary := os.Getenv(dhcpRestartBinaryEnv)
	daemon := testenv.StartRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	defer func() { testenv.StopRuntimeDaemon(t, daemon) }()
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
	testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	waitDHCPRecoveryRecord(t, daemon, leaseDirectory, leaseSeconds)
	testenv.StopRuntimeDaemon(t, daemon)
	if lateInterface {
		renameDHCPWANParent(t, "wan-under", "wan-under-old")
	}
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
	serviceRunning = false
	if rejected {
		testenv.SetRuntimeNamespace(t, gateway)
		expireDHCPRecoveryRecord(t, leaseDirectory, time.Duration(leaseSeconds)*time.Second)
		testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
		testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
		daemon = testenv.StartRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
		testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", false)
		testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
		logContent := testenv.RuntimeDaemonLog(t, daemon)
		if !strings.Contains(logContent, "addresses: DHCPv4 saved lease rejected") ||
			!strings.Contains(logContent, "saved DHCPv4 assignment is invalid or expired") ||
			strings.Contains(logContent, "state=BOUND") {
			t.Fatalf(
				"saved lease rejection not observed: %s",
				testenv.RuntimeLogTail(t, daemon, 30),
			)
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
	testenv.SetRuntimeNamespace(t, gateway)
	daemon = testenv.StartRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
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

	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
	testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	const replacementLeaseSeconds = 8
	service = testenv.StartDHCPv4RuntimeKea(
		t,
		root,
		false,
		replacementLeaseSeconds,
		"198.51.100.2",
		"198.51.100.100",
		false,
	)
	serviceRunning = true
	testenv.SetRuntimeNamespace(t, gateway)
	waitDHCPRecoveryRecord(t, daemon, leaseDirectory, replacementLeaseSeconds)
	testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	testenv.StopRuntimeDaemon(t, daemon)
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
	serviceRunning = false
	testenv.SetRuntimeNamespace(t, gateway)
	daemon = testenv.StartRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", false)
	testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
}

func expireDHCPRecoveryRecord(t *testing.T, directory string, lifetime time.Duration) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("saved lease directory contains %d records", len(entries))
	}
	store := netif.NewLeaseStore(filepath.Join(directory, entries[0].Name()))
	record, err := store.Load("att", netif.LeaseProtocolDHCPv4)
	if err != nil {
		t.Fatal(err)
	}

	if record.Clock.Uptime >= lifetime {
		record.Clock.Uptime -= lifetime
	} else {
		// A lease older than the current boot uses wall time for recovery.
		record.Clock.BootID = "previous-boot"
		record.Clock.WallTime = record.Clock.WallTime.Add(-lifetime)
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"acquired_at", "renew_at", "rebind_at", "expires_at"} {
			var timestamp time.Time
			if err := json.Unmarshal(payload[key], &timestamp); err != nil {
				t.Fatal(err)
			}
			payload[key], err = timestamp.Add(-lifetime).MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
		}
		record.Payload, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
}

func waitDHCPRecoveryMainLoop(t *testing.T, daemon *testenv.RuntimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		logContent := testenv.RuntimeDaemonLog(t, daemon)
		if strings.Contains(logContent, `"phase":"initial-reconcile"`) &&
			strings.Contains(logContent, "ifmgr: entering main loop") {
			testenv.AssertRuntimeDaemonRunning(t, daemon)
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"daemon did not finish startup reconciliation before interface creation: %s",
		testenv.RuntimeLogTail(t, daemon, 30),
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

func waitDHCPRecoveryRecord(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	directory string,
	leaseSeconds int,
) {
	t.Helper()
	store, err := netif.NewLeaseRecoveryStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	link, err := net.InterfaceByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(directory)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	for {
		if err := watcher.Add(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		lease, err := store.LoadDHCPv4("att", link, []byte{0x01, 0xaa, 0xbb})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if lease != nil && lease.LeaseTime == time.Duration(leaseSeconds)*time.Second {
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		select {
		case <-watcher.Events:
		case err := <-watcher.Errors:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatalf(
				"waiting for committed %ds lease: last lease=%+v: %s",
				leaseSeconds,
				lease,
				testenv.RuntimeLogTail(t, daemon, 30),
			)
		}
	}
}
