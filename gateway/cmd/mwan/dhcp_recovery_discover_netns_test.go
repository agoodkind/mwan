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
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	dhcpRecoveryDiscoverChildEnv  = "MWAN_DHCP_RECOVERY_DISCOVER_CHILD"
	dhcpRecoveryDiscoverBinaryEnv = "MWAN_DHCP_RECOVERY_DISCOVER_BINARY"
	dhcpRecoverySavedAddress      = "198.51.100.101/24"
	dhcpRecoverySavedGateway      = "198.51.100.2"
)

func TestOOBDHCPv4RecoveryDiscoverNewAddress(t *testing.T) {
	testOOBDHCPv4RecoveryDiscover(t, "198.51.100.77", "198.51.100.3")
}

func TestOOBDHCPv4RecoveryDiscoverSameAddress(t *testing.T) {
	testOOBDHCPv4RecoveryDiscover(t, "198.51.100.101", "198.51.100.3")
}

func testOOBDHCPv4RecoveryDiscover(t *testing.T, offeredAddress, offeredGateway string) {
	if os.Getenv(dhcpRecoveryDiscoverChildEnv) == "1" {
		runOOBDHCPv4RecoveryDiscover(t, net.ParseIP(offeredAddress), net.ParseIP(offeredGateway))
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpRecoveryDiscoverChildEnv+"=1", dhcpRecoveryDiscoverBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runOOBDHCPv4RecoveryDiscover(t *testing.T, offeredAddress, offeredGateway net.IP) {
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
	setRuntimeNamespace(t, provider.namespace)
	service := startDHCPv4RuntimeKea(t, root, false, 600, dhcpRecoverySavedGateway, "198.51.100.100", true)
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
	binary := os.Getenv(dhcpRecoveryDiscoverBinaryEnv)
	daemon := startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, true)
	waitRoleDHCPv4Route(t, daemon, 100, true)
	waitDHCPRecoveryRecord(t, daemon, filepath.Join(leaseDirectory, "roles"))
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)

	daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	waitDHCPRecoveryDiscoverFailure(t, daemon, 90*time.Second)
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, true)
	waitRoleDHCPv4Route(t, daemon, 100, true)

	setRuntimeNamespace(t, provider.namespace)
	server, err := server4.NewServer("wan-vlan", &net.UDPAddr{IP: net.IPv4zero, Port: 67},
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
				dhcpv4.WithYourIP(offeredAddress),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP(dhcpRecoverySavedGateway))),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptRouter(offeredGateway)),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(10*time.Minute)),
			)
			if replyErr == nil {
				_, _ = conn.WriteTo(reply.ToBytes(), peer)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	defer func() { _ = server.Close() }()
	setRuntimeNamespace(t, gateway)

	waitDHCPv4RuntimeState(t, daemon, "state=BOUND", 60*time.Second)
	offeredPrefix := offeredAddress.String() + "/24"
	waitStaticRuntimeAddress(t, daemon, "enatt0", offeredPrefix, true)
	waitDHCPRecoveryGateway(t, daemon, 100, offeredGateway)
	if offeredPrefix != dhcpRecoverySavedAddress {
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRecoverySavedAddress, false)
	}
}

func waitDHCPRecoveryDiscoverFailure(t *testing.T, daemon *runtimeDaemon, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
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
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: 100}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	routePresent := false
	for _, route := range routes {
		routePresent = routePresent || route.Gw.Equal(net.ParseIP(dhcpRecoverySavedGateway))
	}
	if !addressPresent || !routePresent {
		t.Fatalf("assertDHCPRecoverySavedAssignment: addresses=%v routes=%v: %s", addresses, routes, runtimeLogTail(t, daemon, 30))
	}
}

func waitDHCPRecoveryGateway(t *testing.T, daemon *runtimeDaemon, table int, gatewayAddress net.IP) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var observed []netlink.Route
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
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
	t.Fatalf("waitDHCPRecoveryGateway %s: routes=%v: %s", gatewayAddress, observed, runtimeLogTail(t, daemon, 30))
}
