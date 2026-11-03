//go:build linux && firewallnetns

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	dhcpRenamedLinkChildEnv  = "MWAN_DHCP_RENAMED_LINK_CHILD"
	dhcpRenamedLinkBinaryEnv = "MWAN_DHCP_RENAMED_LINK_BINARY"
	dhcpRenamedLinkAddress   = "198.51.100.101/24"
	dhcpRenamedLinkForeign   = "203.0.113.9/24"
)

type dhcpRecoveryLinkChange int

const (
	dhcpRecoveryRename dhcpRecoveryLinkChange = iota
	dhcpRecoveryRenameLateReplacement
	dhcpRecoveryDeleteForeignAddress
	dhcpRecoveryRenameFastRebind
	dhcpRecoveryRenameBeforeStart
	dhcpRecoveryRenameAfterBound
	dhcpRecoveryRenameAfterBoundLongLease
	dhcpRecoveryFlapAfterBoundLongLease
	dhcpRecoveryFlapDuringRenewalRetry
)

func TestOOBDHCPv4RecoveryWithdrawsRenamedLinkAddress(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRename)
}

func TestOOBDHCPv4RecoveryWithdrawsRenamedLinkAddressAfterLateReplacement(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameLateReplacement)
}

func TestOOBDHCPv4RecoveryPreservesForeignAddressOnRecreatedLink(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryDeleteForeignAddress)
}

func TestOOBDHCPv4RecoveryRenamedLinkFastRebind(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameFastRebind)
}

func TestOOBDHCPv4RecoveryRenamedLinkBeforeStart(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameBeforeStart)
}

func TestOOBDHCPv4BoundRenamedLink(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameAfterBound)
}

func TestOOBDHCPv4BoundRenamedLinkLongLease(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameAfterBoundLongLease)
}

func TestOOBDHCPv4BoundLinkFlapKeepsLease(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryFlapAfterBoundLongLease)
}

func TestOOBDHCPv4LinkFlapsKeepRenewalRetryDeadline(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryFlapDuringRenewalRetry)
}

func renameDHCPRecoveryLink(t *testing.T, oldLink netlink.Link) {
	t.Helper()
	if err := netlink.LinkSetDown(oldLink); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetName(oldLink, "enatt0-old"); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(oldLink); err != nil {
		t.Fatal(err)
	}
	foreignAddress, err := netlink.ParseAddr(dhcpRenamedLinkForeign)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(oldLink, foreignAddress); err != nil {
		t.Fatal(err)
	}
}

func assertDHCPRenamedLinkForeignAddress(t *testing.T, oldLink netlink.Link) {
	t.Helper()
	addresses, err := netlink.AddrList(oldLink, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	foreignPresent := false
	for _, address := range addresses {
		foreignPresent = foreignPresent || address.IPNet.String() == dhcpRenamedLinkForeign
	}
	if !foreignPresent {
		t.Fatalf("foreignPresent = false: addresses=%v", addresses)
	}
}

func testOOBDHCPv4RecoveryLinkChange(t *testing.T, change dhcpRecoveryLinkChange) {
	if os.Getenv(dhcpRenamedLinkChildEnv) == "1" {
		runOOBDHCPv4RecoveryLinkChange(t, change)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpRenamedLinkChildEnv+"=1", dhcpRenamedLinkBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runOOBDHCPv4RecoveryLinkChange(t *testing.T, change dhcpRecoveryLinkChange) {
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
	provider := newRuntimePeer(t, gateway, "enatt0", "wan-vlan", nil, []string{"198.51.100.2/24"}, "")
	defer provider.namespace.Close()
	leaseSeconds := 600
	if change == dhcpRecoveryRenameLateReplacement || change == dhcpRecoveryRenameAfterBound {
		leaseSeconds = 40
	}
	if change == dhcpRecoveryFlapDuringRenewalRetry {
		leaseSeconds = 80
	}
	setRuntimeNamespace(t, provider.namespace)
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
	binary := os.Getenv(dhcpRenamedLinkBinaryEnv)
	daemon := startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	waitRoleDHCPv4Route(t, daemon, 100, true)
	waitDHCPRecoveryRecord(t, daemon, filepath.Join(leaseDirectory, "roles"))
	if change == dhcpRecoveryFlapDuringRenewalRetry || change == dhcpRecoveryFlapAfterBoundLongLease {
		setRuntimeNamespace(t, provider.namespace)
		stopProtocolServices(t, []protocolService{service})
		serviceRunning = false
		setRuntimeNamespace(t, gateway)
		if change == dhcpRecoveryFlapDuringRenewalRetry {
			assertDHCPRenewalRetriesDuringLinkFlaps(t, daemon, provider.namespace, gateway, filepath.Join(leaseDirectory, "roles"))
		} else {
			assertDHCPBoundLinkFlapKeepsLease(t, daemon, provider.namespace, gateway, filepath.Join(leaseDirectory, "roles"))
		}
		return
	}
	if change == dhcpRecoveryRenameAfterBound || change == dhcpRecoveryRenameAfterBoundLongLease {
		boundLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		renamedAt := time.Now()
		renameDHCPRecoveryLink(t, boundLink)
		replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", nil, []string{"198.51.100.2/24"}, "")
		defer replacement.namespace.Close()
		if change == dhcpRecoveryRenameAfterBoundLongLease {
			defer startDHCPRenamedLinkServer(t, replacement.namespace, gateway)()
			waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
			waitDHCPv4RuntimeState(t, daemon, "state=BOUND ", 30*time.Second)
			if elapsed := time.Since(renamedAt); elapsed > 30*time.Second {
				t.Fatalf("198.51.100.77/24 elapsed = %s", elapsed)
			}
		}
		waitDHCPRenamedLinkAddressRemoved(t, daemon, boundLink.Attrs().Index)
		assertDHCPRenamedLinkForeignAddress(t, boundLink)
		return
	}
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)

	if change == dhcpRecoveryRenameBeforeStart {
		absentLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		renameDHCPRecoveryLink(t, absentLink)
		daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
		time.Sleep(3 * time.Second)
		assertRuntimeDaemonRunning(t, daemon)
		replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", nil, []string{"198.51.100.2/24"}, "")
		defer replacement.namespace.Close()
		waitDHCPRenamedLinkAddressRemoved(t, daemon, absentLink.Attrs().Index)
		assertDHCPRenamedLinkForeignAddress(t, absentLink)
		return
	}

	daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	if change == dhcpRecoveryRenameLateReplacement {
		waitDHCPv4RuntimeState(t, daemon, "dhcp: restart request failed", 20*time.Second)
	}
	oldLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if change == dhcpRecoveryDeleteForeignAddress {
		if err := netlink.LinkDel(oldLink); err != nil {
			t.Fatal(err)
		}
		replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", []string{dhcpRenamedLinkAddress}, nil, "")
		defer replacement.namespace.Close()
		waitDHCPv4RuntimeState(t, daemon, "state=EXPIRED", 40*time.Second)
		time.Sleep(2 * time.Second)
		assertRuntimeDaemonRunning(t, daemon)
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
		return
	}
	renameDHCPRecoveryLink(t, oldLink)
	if change == dhcpRecoveryRenameLateReplacement {
		waitDHCPv4RuntimeState(t, daemon, "state=EXPIRED", 60*time.Second)
	}
	replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", nil, []string{"198.51.100.2/24"}, "")
	defer replacement.namespace.Close()
	if change == dhcpRecoveryRenameFastRebind {
		defer startDHCPRenamedLinkServer(t, replacement.namespace, gateway)()
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
	}

	waitDHCPRenamedLinkAddressRemoved(t, daemon, oldLink.Attrs().Index)
	assertRuntimeDaemonRunning(t, daemon)
	assertDHCPRenamedLinkForeignAddress(t, oldLink)
}

func assertDHCPRenewalRetriesDuringLinkFlaps(t *testing.T, daemon *runtimeDaemon, serverNamespace, gateway netns.NsHandle, leaseDirectory string) {
	t.Helper()
	flappedLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int64
	defer startDHCPRequestCounter(t, &requestCount, flappedLink.Attrs().HardwareAddr, serverNamespace, gateway)()
	store, err := netif.NewLeaseRecoveryStore(leaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	iface, err := net.InterfaceByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.LoadDHCPv4("oob/enatt0", iface, nil)
	if err != nil {
		t.Fatal(err)
	}
	flapDeadline := lease.RebindAt.Add(5 * time.Second)
	if !flapDeadline.Before(lease.ExpiresAt) {
		t.Fatal("The rebind check must finish before the lease expires.")
	}
	waitDHCPv4RuntimeState(t, daemon, "dhcp: renewal attempt failed", 60*time.Second)
	requestsBefore := requestCount.Load()
	if requestsBefore == 0 {
		t.Fatalf("DHCP client packet count = 0: %s", runtimeLogTail(t, daemon, 30))
	}
	for requestCount.Load() <= requestsBefore {
		if !time.Now().Before(flapDeadline) {
			t.Fatalf("DHCP client packet count = %d, want > %d: %s", requestCount.Load(), requestsBefore, runtimeLogTail(t, daemon, 30))
		}
		if err := netlink.LinkSetDown(flappedLink); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(flappedLink); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	assertRuntimeDaemonRunning(t, daemon)
}

func assertDHCPBoundLinkFlapKeepsLease(t *testing.T, daemon *runtimeDaemon, serverNamespace, gateway netns.NsHandle, leaseDirectory string) {
	t.Helper()
	flappedLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	var packetCount atomic.Int64
	defer startDHCPRequestCounter(t, &packetCount, flappedLink.Attrs().HardwareAddr, serverNamespace, gateway)()
	store, err := netif.NewLeaseRecoveryStore(leaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	flappedInterface, err := net.InterfaceByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	savedBefore, err := store.LoadDHCPv4("oob/enatt0", flappedInterface, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(flappedLink); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(flappedLink); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	assertRuntimeDaemonRunning(t, daemon)
	if count := packetCount.Load(); count != 0 {
		t.Fatalf("DHCP client packet count = %d, want 0: %s", count, runtimeLogTail(t, daemon, 30))
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	savedAfter, err := store.LoadDHCPv4("oob/enatt0", flappedInterface, nil)
	if err != nil {
		t.Fatal(err)
	}
	if savedAfter.ExpiresAt.Sub(savedBefore.ExpiresAt).Abs() > time.Second {
		t.Fatalf("saved lease ExpiresAt before=%s after=%s", savedBefore.ExpiresAt, savedAfter.ExpiresAt)
	}
}

func startDHCPRequestCounter(t *testing.T, packetCount *atomic.Int64, hardwareAddress net.HardwareAddr, serverNamespace, gateway netns.NsHandle) func() {
	t.Helper()
	return startDHCPRuntimeServer(t, "wan-vlan", serverNamespace, gateway,
		func(_ net.PacketConn, _ net.Addr, packet *dhcpv4.DHCPv4) {
			if bytes.Equal(packet.ClientHWAddr, hardwareAddress) {
				packetCount.Add(1)
			}
		})
}

func startDHCPRenamedLinkServer(t *testing.T, serverNamespace, gateway netns.NsHandle) func() {
	t.Helper()
	return startDHCPRuntimeServer(t, "wan-new", serverNamespace, gateway,
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			responseType := dhcpv4.MessageTypeOffer
			if packet.MessageType() == dhcpv4.MessageTypeRequest {
				if !packet.Options.Has(dhcpv4.OptionServerIdentifier) {
					return
				}
				responseType = dhcpv4.MessageTypeAck
			}
			reply, replyErr := dhcpv4.NewReplyFromRequest(packet,
				dhcpv4.WithMessageType(responseType),
				dhcpv4.WithYourIP(net.ParseIP("198.51.100.77")),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP("198.51.100.2"))),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptRouter(net.ParseIP("198.51.100.3"))),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(10*time.Minute)),
			)
			if replyErr == nil {
				_, _ = conn.WriteTo(reply.ToBytes(), peer)
			}
		})
}

func startDHCPRuntimeServer(t *testing.T, ifaceName string, serverNamespace, gateway netns.NsHandle, handler server4.Handler) func() {
	t.Helper()
	setRuntimeNamespace(t, serverNamespace)
	server, err := server4.NewServer(ifaceName, &net.UDPAddr{IP: net.IPv4zero, Port: 67}, handler)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	setRuntimeNamespace(t, gateway)
	return func() {
		t.Helper()
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Fatalf("server4.Server.Serve: %v", err)
		}
	}
}

func waitDHCPRenamedLinkAddressRemoved(t *testing.T, daemon *runtimeDaemon, linkIndex int) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	var addresses []netlink.Addr
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByIndex(linkIndex)
		if err != nil {
			t.Fatal(err)
		}
		addresses, err = netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			t.Fatal(err)
		}
		present := false
		for _, address := range addresses {
			present = present || address.IPNet.String() == dhcpRenamedLinkAddress
		}
		if !present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waitDHCPRenamedLinkAddressRemoved: addresses=%v: %s", addresses, runtimeLogTail(t, daemon, 30))
}
