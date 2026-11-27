//go:build linux && protocol

package protocol_test

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
	"goodkind.io/mwan/gateway/integration/internal/testenv"
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
	binary := testenv.ProtocolTestBinary(t)
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
	service := testenv.StartDHCPv4RuntimeKea(
		t,
		root,
		false,
		600,
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
	binary := os.Getenv(dhcpRenamedLinkBinaryEnv)
	daemon := testenv.StartRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	defer func() { testenv.StopRuntimeDaemon(t, daemon) }()
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	testenv.WaitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	waitDHCPRecoveryRecord(t, daemon, leaseDirectory, 600)
	if change == dhcpRecoveryFlapAfterBoundLongLease {
		testenv.SetRuntimeNamespace(t, provider.Namespace)
		testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
		serviceRunning = false
		testenv.SetRuntimeNamespace(t, gateway)
		assertDHCPBoundLinkFlapKeepsLease(t, daemon, provider.Namespace, gateway, leaseDirectory)
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
		defer replacement.Namespace.Close()
		defer startDHCPRenamedLinkServer(t, replacement.Namespace, gateway)()
		testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
		if elapsed := time.Since(renamedAt); elapsed > 30*time.Second {
			t.Fatalf("New DHCP address appeared after %s, want at most 30s", elapsed)
		}
		assertDHCPRenamedLinkForeignAddress(t, boundLink)
		return
	}
	testenv.StopRuntimeDaemon(t, daemon)
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
	serviceRunning = false
	testenv.SetRuntimeNamespace(t, gateway)

	daemon = testenv.StartRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	oldLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if change == dhcpRecoveryDeleteForeignAddress {
		renameDHCPWANParent(t, "wan-under", "wan-under-old")
		if err := netlink.LinkDel(oldLink); err != nil {
			t.Fatal(err)
		}
		replacement := testenv.NewRuntimePeer(
			t,
			gateway,
			"enatt0",
			"wan-new",
			[]string{dhcpRenamedLinkAddress},
			nil,
			"",
		)
		defer replacement.Namespace.Close()
		renameDHCPWANParent(t, "wan-under-old", "wan-under")
		time.Sleep(2 * time.Second)
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
		return
	}
	renameDHCPRecoveryLink(t, oldLink)
	replacement := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
	defer replacement.Namespace.Close()
	defer startDHCPRenamedLinkServer(t, replacement.Namespace, gateway)()
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, false)
	testenv.AssertRuntimeDaemonRunning(t, daemon)
	assertDHCPRenamedLinkForeignAddress(t, oldLink)
}

func assertDHCPBoundLinkFlapKeepsLease(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
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
			testenv.RuntimeLogTail(t, daemon, 30),
		)
	}
	testenv.AssertRuntimeDaemonRunning(t, daemon)
	if count := otherPacketCount.Load(); count != 0 {
		t.Fatalf(
			"DHCP client packet count = %d, want 0: %s",
			count,
			testenv.RuntimeLogTail(t, daemon, 30),
		)
	}
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
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
	testenv.SetRuntimeNamespace(t, serverNamespace)
	server, err := server4.NewServer(ifaceName, &net.UDPAddr{IP: net.IPv4zero, Port: 67}, handler)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	testenv.SetRuntimeNamespace(t, gateway)
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
