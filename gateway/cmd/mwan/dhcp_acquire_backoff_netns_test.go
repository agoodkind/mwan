//go:build linux && firewallnetns

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	dhcpAcquireBackoffChildEnv  = "MWAN_DHCP_ACQUIRE_BACKOFF_CHILD"
	dhcpAcquireBackoffBinaryEnv = "MWAN_DHCP_ACQUIRE_BACKOFF_BINARY"
)

func TestOOBDHCPv4LinkFlapDuringAcquireWakesRetry(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
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
	provider := newRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-vlan",
		nil,
		[]string{"198.51.100.2/24"},
		"",
	)
	defer provider.namespace.Close()
	var discoverCount atomic.Int64
	var firstTransaction atomic.Uint32
	setRuntimeNamespace(t, provider.namespace)
	server, err := server4.NewServer(
		"wan-vlan",
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			discoverCount.Add(1)
			transaction := binary.BigEndian.Uint32(packet.TransactionID[:])
			firstTransaction.CompareAndSwap(0, transaction)
			if transaction == firstTransaction.Load() {
				return
			}
			responseType := dhcpv4.MessageTypeOffer
			if packet.MessageType() == dhcpv4.MessageTypeRequest {
				responseType = dhcpv4.MessageTypeAck
			}
			reply, replyErr := dhcpv4.NewReplyFromRequest(
				packet,
				dhcpv4.WithMessageType(responseType),
				dhcpv4.WithYourIP(net.ParseIP("198.51.100.101")),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP("198.51.100.2"))),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptRouter(net.ParseIP("198.51.100.2"))),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(10*time.Minute)),
			)
			if replyErr == nil {
				_, _ = conn.WriteTo(reply.ToBytes(), peer)
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	defer func() {
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("server4.Server.Serve: %v", err)
		}
	}()
	setRuntimeNamespace(t, gateway)
	configPath := writeDHCPAcquireBackoffConfig(t, root)
	daemon := startRoleDHCPv4RuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"oob",
	)
	defer func() { stopRuntimeDaemon(t, daemon) }()
	discoverDeadline := time.Now().Add(20 * time.Second)
	for discoverCount.Load() == 0 {
		if !time.Now().Before(discoverDeadline) {
			t.Fatalf("DHCP packet count = 0: %s", runtimeLogTail(t, daemon, 30))
		}
		time.Sleep(10 * time.Millisecond)
	}
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	waitDHCPv4RuntimeState(t, daemon, "dhcp: acquire failed; will retry", 20*time.Second)
	failedAt := time.Now()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", true)
	if elapsed := time.Since(failedAt); elapsed > 15*time.Second {
		t.Fatalf("198.51.100.101/24 elapsed = %s, want at most 15s", elapsed)
	}
}

func dhcpAcquireBackoffIsolated(t *testing.T) bool {
	t.Helper()
	if os.Getenv(dhcpAcquireBackoffChildEnv) == "1" {
		return true
	}
	daemonBinary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		dhcpAcquireBackoffChildEnv+"=1",
		dhcpAcquireBackoffBinaryEnv+"="+daemonBinary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
	return false
}

func writeDHCPAcquireBackoffConfig(t *testing.T, root string) string {
	t.Helper()
	leaseDirectory := filepath.Join(root, "leases")
	if err := os.Mkdir(leaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"oob\"\nreconcile_interval = \"1h\"\nlease_directory = %q\n[ifmgr.iface.enatt0]\ndhcp_v4 = true\ndhcp_initial_backoff = \"30s\"\ndhcp_max_backoff = \"5m\"\n[ifmgr.modules.oobv6]\niface = \"enatt0\"\noob_addr = \"2001:db8::100/128\"\noob_table_id = 100\nmanage_slaac_source_rule = false\n[ifmgr.modules.oobv4]\niface = \"enatt0\"\noob_table_id = 100\n[ifmgr.modules.ra_lost]\niface = \"enatt0\"\n[[ifmgr.modules.policy_rules.rule]]\nfamily = \"ipv4\"\npriority = 100\nfrom = \"192.0.2.2/32\"\ntable_id = 100\n",
		leaseDirectory,
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestOOBDHCPv4UnrelatedLinkFlapKeepsAcquisitionBackoff(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
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
	provider := newRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-vlan",
		nil,
		[]string{"198.51.100.2/24"},
		"",
	)
	defer provider.namespace.Close()
	unrelatedName := "unrelated0"
	if err := netlink.LinkAdd(&netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{Name: unrelatedName},
	}); err != nil {
		t.Fatal(err)
	}
	var transactionsMu sync.Mutex
	transactions := map[uint32]struct{}{}
	transactionCount := func() int {
		transactionsMu.Lock()
		defer transactionsMu.Unlock()
		return len(transactions)
	}
	setRuntimeNamespace(t, provider.namespace)
	server, err := server4.NewServer(
		"wan-vlan",
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(_ net.PacketConn, _ net.Addr, packet *dhcpv4.DHCPv4) {
			transactionsMu.Lock()
			defer transactionsMu.Unlock()
			transactions[binary.BigEndian.Uint32(packet.TransactionID[:])] = struct{}{}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	defer func() {
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("server4.Server.Serve: %v", err)
		}
	}()
	setRuntimeNamespace(t, gateway)
	configPath := writeDHCPAcquireBackoffConfig(t, root)
	daemon := startRoleDHCPv4RuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"oob",
	)
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitDHCPv4RuntimeState(t, daemon, "dhcp: acquire failed; will retry", 40*time.Second)
	for _, name := range []string{unrelatedName, "enmwanbr0", unrelatedName, "enmwanbr0"} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetDown(link); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
	}
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMTU(link, 1400); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	assertRuntimeDaemonRunning(t, daemon)
	if count := transactionCount(); count != 1 {
		t.Fatalf("DHCP transaction count = %d, want 1: %s", count, runtimeLogTail(t, daemon, 30))
	}
	if count := strings.Count(
		runtimeDaemonLog(t, daemon),
		"dhcp: acquire failed; will retry",
	); count != 1 {
		t.Fatalf("acquire failure count = %d, want 1: %s", count, runtimeLogTail(t, daemon, 30))
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	flappedAt := time.Now()
	retryDeadline := flappedAt.Add(15 * time.Second)
	for transactionCount() < 2 {
		if !time.Now().Before(retryDeadline) {
			t.Fatalf(
				"DHCP transaction count = %d, want 2: %s",
				transactionCount(),
				runtimeLogTail(t, daemon, 30),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("enatt0 retry elapsed = %s", time.Since(flappedAt))
}

func TestOOBDHCPv4LinkUpWakesAcquisitionBackoff(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
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
	provider := newRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-vlan",
		nil,
		[]string{"198.51.100.2/24"},
		"",
	)
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	service := startDHCPv4RuntimeKea(t, root, false, 600, "198.51.100.2", "198.51.100.100", true)
	defer func() {
		setRuntimeNamespace(t, provider.namespace)
		stopProtocolServices(t, []protocolService{service})
	}()
	setRuntimeNamespace(t, gateway)
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	configPath := writeDHCPAcquireBackoffConfig(t, root)
	daemon := startRoleDHCPv4RuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"oob",
	)
	defer func() { stopRuntimeDaemon(t, daemon) }()
	backoffDeadline := time.Now().Add(40 * time.Second)
	for !strings.Contains(runtimeDaemonLog(t, daemon), "dhcp: acquire failed; will retry") {
		if !time.Now().Before(backoffDeadline) {
			t.Fatalf("acquire failure absent: %s", runtimeLogTail(t, daemon, 30))
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	linkUpAt := time.Now()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", true)
	if elapsed := time.Since(linkUpAt); elapsed > 15*time.Second {
		t.Fatalf("198.51.100.101/24 elapsed = %s, want at most 15s", elapsed)
	}
	t.Logf("198.51.100.101/24 elapsed = %s", time.Since(linkUpAt))
}
