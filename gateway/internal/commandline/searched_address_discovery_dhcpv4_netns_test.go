//go:build linux && protocol

package commandline

import (
	"context"
	"log/slog"
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
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/netif"
)

const (
	dhcpRecoveryDiscoverChildEnv  = "MWAN_DHCP_RECOVERY_DISCOVER_CHILD"
	dhcpRecoveryDiscoverBinaryEnv = "MWAN_DHCP_RECOVERY_DISCOVER_BINARY"
	dhcpRecoveryDaemonConfigEnv   = "MWAN_DHCP_RECOVERY_DAEMON_CONFIG"
	dhcpRecoverySavedAddress      = "198.51.100.100/24"
	dhcpRecoverySavedGateway      = "198.51.100.2"
	dhcpRecoveryChildTimeout      = 3 * time.Minute
	dhcpRecoveryPhaseTimeout      = 20 * time.Second
)

func TestOwnedDHCPv4RecoveryDiscoverNewAddress(t *testing.T) {
	testOwnedDHCPv4RecoveryDiscover(t, "198.51.100.77", "198.51.100.3", false)
}

func TestOwnedDHCPv4RecoveryDiscoverSameAddress(t *testing.T) {
	testOwnedDHCPv4RecoveryDiscover(t, "198.51.100.100", "198.51.100.3", false)
}

func TestOwnedDHCPv4RecoveryDiscoverInterfaceReplacement(t *testing.T) {
	testOwnedDHCPv4RecoveryDiscover(t, "", "", false)
}

func TestOwnedDHCPv4RecoveryDiscoverLinkReturnsLater(t *testing.T) {
	testOwnedDHCPv4RecoveryDiscover(t, "", "", true)
}

func testOwnedDHCPv4RecoveryDiscover(
	t *testing.T,
	offeredAddress string,
	offeredGateway string,
	linkReturnsLater bool,
) {
	if configPath := os.Getenv(dhcpRecoveryDaemonConfigEnv); configPath != "" {
		runDHCPRecoveryDaemon(t, configPath)
		return
	}
	if os.Getenv(dhcpRecoveryDiscoverChildEnv) == "1" {
		runOwnedDHCPv4RecoveryDiscover(
			t,
			net.ParseIP(offeredAddress),
			net.ParseIP(offeredGateway),
			linkReturnsLater,
		)
		return
	}
	t.Parallel()
	binary := protocolTestBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), dhcpRecoveryChildTimeout)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
		Setpgid:    true,
	}
	child.Cancel = func() error { return unix.Kill(-child.Process.Pid, unix.SIGKILL) }
	child.WaitDelay = 2 * time.Second
	child.Env = append(
		os.Environ(),
		dhcpRecoveryDiscoverChildEnv+"=1",
		dhcpRecoveryDiscoverBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runDHCPRecoveryDaemon(t *testing.T, configPath string) {
	t.Helper()
	settings, err := config.LoadPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	startClient := func(ctx context.Context, log *slog.Logger, clientConfig netif.DHCPConfig) *netif.DHCPClient {
		clientConfig.RestartWindow = 5 * time.Second
		clientConfig.RequestTimeout = 2 * time.Second
		clientConfig.DiscoverTimeout = 2 * time.Second
		clientConfig.InitialBackoff = time.Second
		clientConfig.MaxBackoff = 2 * time.Second
		return netif.StartDHCPClient(ctx, log, clientConfig)
	}
	if err := runIfMgr(
		t.Context(),
		settings,
		commandopts.Ifmgr{Role: ifMgrWANRole, Debug: true},
		os.Stderr,
		startClient,
	); err != nil {
		t.Fatal(err)
	}
}

func runOwnedDHCPv4RecoveryDiscover(
	t *testing.T,
	offeredAddress net.IP,
	offeredGateway net.IP,
	linkReturnsLater bool,
) {
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
	service := startDHCPv4RuntimeKea(
		t,
		root,
		false,
		600,
		dhcpRecoverySavedGateway,
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
	binary := os.Getenv(dhcpRecoveryDiscoverBinaryEnv)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "dhcpv4")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, true)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)
	waitDHCPRecoveryRecord(t, daemon, leaseDirectory)
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)

	daemon = startRuntimeDaemonProcess(
		t,
		os.Args[0],
		filepath.Join(root, "dhcpv4.log"),
		[]string{dhcpRecoveryDaemonConfigEnv + "=" + configPath},
		"-test.run=^"+t.Name()+"$",
	)
	waitDHCPRecoveryDiscoverFailure(t, daemon)
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, true)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, true)

	discoverSeen := make(chan struct{}, 1)
	setRuntimeNamespace(t, provider.namespace)
	server, err := server4.NewServer(
		"wan-vlan",
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			if offeredAddress == nil {
				if packet.MessageType() == dhcpv4.MessageTypeDiscover {
					select {
					case discoverSeen <- struct{}{}:
					default:
					}
				}
				return
			}
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
				dhcpv4.WithYourIP(offeredAddress),
				dhcpv4.WithOption(
					dhcpv4.OptServerIdentifier(net.ParseIP(dhcpRecoverySavedGateway)),
				),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptRouter(offeredGateway)),
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
	go func() { _ = server.Serve() }()
	defer func() { _ = server.Close() }()
	setRuntimeNamespace(t, gateway)

	if offeredAddress == nil {
		select {
		case <-discoverSeen:
		case <-time.After(dhcpRecoveryPhaseTimeout):
			t.Fatalf("discoverSeen deadline: %s", runtimeLogTail(t, daemon, 40))
		}
		oldLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		if linkReturnsLater {
			assertDHCPRecoveryAfterLinkReturns(t, daemon, gateway, oldLink)
			return
		}
		renameDHCPWANParent(t, "wan-under", "wan-under-old")
		if err := netlink.LinkDel(oldLink); err != nil {
			t.Fatal(err)
		}
		replacement := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
		defer replacement.namespace.Close()
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, false)
		waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
		return
	}

	waitDHCPv4RuntimeState(t, daemon, "state=BOUND", dhcpRecoveryPhaseTimeout)
	offeredPrefix := offeredAddress.String() + "/24"
	waitStaticRuntimeAddress(t, daemon, "enatt0", offeredPrefix, true)
	waitDHCPRecoveryGateway(t, daemon, offeredGateway)
	if offeredPrefix != dhcpRecoverySavedAddress {
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, false)
	}
}

func assertDHCPRecoveryAfterLinkReturns(
	t *testing.T,
	daemon *runtimeDaemon,
	gateway netns.NsHandle,
	oldLink netlink.Link,
) {
	t.Helper()
	previousLogSize := len(runtimeDaemonLog(t, daemon))
	renameDHCPRecoveryLink(t, oldLink)
	missingLinkDeadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(
		runtimeDaemonLog(t, daemon)[previousLogSize:],
		"connection att link is not ready",
	) {
		if !time.Now().Before(missingLinkDeadline) {
			t.Fatalf("WAN did not report the missing link: %s", runtimeLogTail(t, daemon, 40))
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)

	replacement := newDHCPv4WANProvider(t, gateway, "198.51.100.2/24")
	defer replacement.namespace.Close()
	replacementDiscover := make(chan string, 1)
	defer startDHCPRuntimeServer(
		t,
		"wan-vlan",
		replacement.namespace,
		gateway,
		func(_ net.PacketConn, _ net.Addr, packet *dhcpv4.DHCPv4) {
			if packet.MessageType() == dhcpv4.MessageTypeDiscover {
				select {
				case replacementDiscover <- packet.ClientHWAddr.String():
				default:
				}
			}
		},
	)()
	select {
	case clientMAC := <-replacementDiscover:
		replacementLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		if want := replacementLink.Attrs().HardwareAddr.String(); clientMAC != want {
			t.Fatalf("Replacement DHCP DISCOVER client MAC = %s, want %s", clientMAC, want)
		}
	case <-time.After(dhcpRecoveryPhaseTimeout):
		t.Fatalf(
			"Replacement provider did not receive DHCP DISCOVER: %s",
			runtimeLogTail(t, daemon, 40),
		)
	}
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, false)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
	assertDHCPRenamedLinkForeignAddress(t, oldLink)
}

func waitDHCPRecoveryDiscoverFailure(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(dhcpRecoveryPhaseTimeout)
	for time.Now().Before(deadline) {
		if strings.Contains(runtimeDaemonLog(t, daemon), "dhcp: recovery DISCOVER failed") {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		assertDHCPRecoverySavedAssignment(t, daemon)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waitDHCPRecoveryDiscoverFailure: %s", runtimeLogTail(t, daemon, 40))
}

func assertDHCPRecoverySavedAssignment(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	addressPresent := false
	for _, address := range addresses {
		addressPresent = addressPresent || address.IPNet.String() == dhcpRecoverySavedAddress
	}
	routes, err := netlink.RouteListFiltered(
		unix.AF_INET,
		&netlink.Route{Table: unix.RT_TABLE_MAIN},
		netlink.RT_FILTER_TABLE,
	)
	if err != nil {
		t.Fatal(err)
	}
	routePresent := false
	for _, route := range routes {
		routePresent = routePresent || route.Gw.Equal(net.ParseIP(dhcpRecoverySavedGateway))
	}
	if !addressPresent || !routePresent {
		t.Fatalf(
			"assertDHCPRecoverySavedAssignment: addresses=%v routes=%v: %s",
			addresses,
			routes,
			runtimeLogTail(t, daemon, 30),
		)
	}
}

func waitDHCPRecoveryGateway(
	t *testing.T,
	daemon *runtimeDaemon,
	gatewayAddress net.IP,
) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var observed []netlink.Route
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(
			unix.AF_INET,
			&netlink.Route{Table: unix.RT_TABLE_MAIN},
			netlink.RT_FILTER_TABLE,
		)
		if err != nil {
			t.Fatal(err)
		}
		observed = routes
		defaults := 0
		matched := false
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() != "0.0.0.0/0" {
				continue
			}
			defaults++
			matched = matched || route.Gw.Equal(gatewayAddress)
		}
		if defaults == 1 && matched {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"waitDHCPRecoveryGateway %s: routes=%v: %s",
		gatewayAddress,
		observed,
		runtimeLogTail(t, daemon, 30),
	)
}
