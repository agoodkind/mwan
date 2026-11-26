//go:build linux && protocol

package commandline

import (
	"bytes"
	"errors"
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
	"goodkind.io/mwan/gateway/internal/netif"
)

const (
	dhcpRenamedLinkChildEnv  = "MWAN_DHCP_RENAMED_LINK_CHILD"
	dhcpRenamedLinkBinaryEnv = "MWAN_DHCP_RENAMED_LINK_BINARY"
	dhcpRenamedLinkAddress   = "198.51.100.100/24"
	dhcpRenamedLinkForeign   = "203.0.113.9/24"
)

type dhcpRecoveryLinkChange int

const (
	dhcpRecoveryDeleteForeignAddress dhcpRecoveryLinkChange = iota
	dhcpRecoveryRenameFastRebind
	dhcpRecoveryRenameAfterBoundLongLease
	dhcpRecoveryFlapAfterBoundLongLease
)

func TestOwnedDHCPv4RecoveryPreservesForeignAddressOnRecreatedLink(t *testing.T) {
	testOwnedDHCPv4RecoveryLinkChange(t, dhcpRecoveryDeleteForeignAddress)
}

func TestOwnedDHCPv4RecoveryRenamedLinkFastRebind(t *testing.T) {
	testOwnedDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameFastRebind)
}

func TestOwnedDHCPv4BoundRenamedLinkLongLease(t *testing.T) {
	testOwnedDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameAfterBoundLongLease)
}

func TestOwnedDHCPv4BoundLinkFlapKeepsLease(t *testing.T) {
	testOwnedDHCPv4RecoveryLinkChange(t, dhcpRecoveryFlapAfterBoundLongLease)
}

func renameDHCPRecoveryLink(t *testing.T, oldLink netlink.Link) {
	t.Helper()
	renameDHCPWANParent(t, "wan-under", "wan-under-old")
	if err := netlink.LinkSetDown(oldLink); err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetName(oldLink, "enatt0-old"); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(oldLink, ""); err != nil {
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

func testOwnedDHCPv4RecoveryLinkChange(t *testing.T, change dhcpRecoveryLinkChange) {
	if os.Getenv(dhcpRenamedLinkChildEnv) == "1" {
		runOwnedDHCPv4RecoveryLinkChange(t, change)
		return
	}
	t.Parallel()
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		dhcpRenamedLinkChildEnv+"=1",
		dhcpRenamedLinkBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runOwnedDHCPv4RecoveryLinkChange(t *testing.T, change dhcpRecoveryLinkChange) {
	mountPrivateRuntimeDirectories(t)
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
	service := startDHCPv4RuntimeKea(t, root, false, 600, "198.51.100.2", "198.51.100.100", true)
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
	binary := os.Getenv(dhcpRenamedLinkBinaryEnv)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	waitDHCPRecoveryRecord(t, daemon, leaseDirectory)
	if change == dhcpRecoveryFlapAfterBoundLongLease {
		setRuntimeNamespace(t, provider.namespace)
		stopProtocolServices(t, []protocolService{service})
		serviceRunning = false
		setRuntimeNamespace(t, gateway)
		assertDHCPBoundLinkFlapKeepsLease(t, daemon, provider.namespace, gateway, leaseDirectory)
		return
	}
	if change == dhcpRecoveryRenameAfterBoundLongLease {
		boundLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		renamedAt := time.Now()
		renameDHCPRecoveryLink(t, boundLink)
		replacement := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
		defer replacement.namespace.Close()
		defer startDHCPRenamedLinkServer(t, replacement.namespace, gateway)()
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
		if elapsed := time.Since(renamedAt); elapsed > 30*time.Second {
			t.Fatalf("New DHCP address appeared after %s, want at most 30s", elapsed)
		}
		assertDHCPRenamedLinkForeignAddress(t, boundLink)
		return
	}
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)

	daemon = startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	oldLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if change == dhcpRecoveryDeleteForeignAddress {
		renameDHCPWANParent(t, "wan-under", "wan-under-old")
		if err := netlink.LinkDel(oldLink); err != nil {
			t.Fatal(err)
		}
		replacement := newRuntimePeer(
			t,
			gateway,
			"enatt0",
			"wan-new",
			[]string{dhcpRenamedLinkAddress},
			nil,
			"",
		)
		defer replacement.namespace.Close()
		renameDHCPWANParent(t, "wan-under-old", "wan-under")
		time.Sleep(2 * time.Second)
		assertRuntimeDaemonRunning(t, daemon)
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
		return
	}
	renameDHCPRecoveryLink(t, oldLink)
	replacement := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
	defer replacement.namespace.Close()
	defer startDHCPRenamedLinkServer(t, replacement.namespace, gateway)()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, false)
	assertRuntimeDaemonRunning(t, daemon)
	assertDHCPRenamedLinkForeignAddress(t, oldLink)
}

func assertDHCPBoundLinkFlapKeepsLease(
	t *testing.T,
	daemon *runtimeDaemon,
	serverNamespace netns.NsHandle,
	gateway netns.NsHandle,
	leaseDirectory string,
) {
	t.Helper()
	flappedLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	verified := make(chan struct{}, 1)
	var otherPacketCount atomic.Int64
	hardwareAddress := flappedLink.Attrs().HardwareAddr
	defer startDHCPRuntimeServer(
		t,
		"wan-vlan",
		serverNamespace,
		gateway,
		func(_ net.PacketConn, _ net.Addr, packet *dhcpv4.DHCPv4) {
			if !bytes.Equal(packet.ClientHWAddr, hardwareAddress) {
				return
			}
			if packet.MessageType() == dhcpv4.MessageTypeRequest &&
				packet.ClientIPAddr.IsUnspecified() &&
				!packet.Options.Has(dhcpv4.OptionServerIdentifier) {
				select {
				case verified <- struct{}{}:
				default:
				}
				return
			}
			otherPacketCount.Add(1)
		},
	)()
	store, err := netif.NewLeaseRecoveryStore(leaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	flappedInterface, err := net.InterfaceByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	savedBefore, err := store.LoadDHCPv4("att", flappedInterface, []byte{1, 0xaa, 0xbb})
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(flappedLink); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(flappedLink); err != nil {
		t.Fatal(err)
	}
	select {
	case <-verified:
	case <-time.After(5 * time.Second):
		t.Fatalf(
			"The server did not receive a verification request after the interface returned. Daemon log: %s",
			runtimeLogTail(t, daemon, 30),
		)
	}
	assertRuntimeDaemonRunning(t, daemon)
	if count := otherPacketCount.Load(); count != 0 {
		t.Fatalf("DHCP client packet count = %d, want 0: %s", count, runtimeLogTail(t, daemon, 30))
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	savedAfter, err := store.LoadDHCPv4("att", flappedInterface, []byte{1, 0xaa, 0xbb})
	if err != nil {
		t.Fatal(err)
	}
	if savedAfter.ExpiresAt.Sub(savedBefore.ExpiresAt).Abs() > time.Second {
		t.Fatalf(
			"saved lease ExpiresAt before=%s after=%s",
			savedBefore.ExpiresAt,
			savedAfter.ExpiresAt,
		)
	}
}

func startDHCPRenamedLinkServer(t *testing.T, serverNamespace, gateway netns.NsHandle) func() {
	t.Helper()
	return startDHCPRuntimeServer(
		t,
		"wan-vlan",
		serverNamespace,
		gateway,
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			responseType := dhcpv4.MessageTypeOffer
			if packet.MessageType() == dhcpv4.MessageTypeRequest {
				if !packet.Options.Has(dhcpv4.OptionServerIdentifier) {
					return
				}
				responseType = dhcpv4.MessageTypeAck
			}
			reply, replyErr := dhcpv4.NewReplyFromRequest(
				packet,
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
		},
	)
}

func startDHCPRuntimeServer(
	t *testing.T,
	ifaceName string,
	serverNamespace netns.NsHandle,
	gateway netns.NsHandle,
	handler server4.Handler,
) func() {
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

func renameDHCPWANParent(t *testing.T, currentName, newName string) {
	t.Helper()
	link, err := netlink.LinkByName(currentName)
	if err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetName(link, newName); err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}
