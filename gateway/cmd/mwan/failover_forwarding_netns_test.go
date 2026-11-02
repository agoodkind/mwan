//go:build linux && firewallnetns

package main

import (
	"context"
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

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	forwardingChildEnv    = "MWAN_FAILOVER_FORWARDING_CHILD"
	forwardingBinaryEnv   = "MWAN_FAILOVER_FORWARDING_BINARY"
	forwardingASN         = 65000
	forwardingFailoverNH4 = "192.0.2.10"
	forwardingPrimaryNH4  = "192.0.2.18"
	forwardingFailoverNH6 = "2001:db8:10::2"
	forwardingPrimaryNH6  = "2001:db8:20::2"
	forwardingRouter4A    = "192.0.2.9"
	forwardingRouter4B    = "192.0.2.17"
	forwardingRouter6A    = "2001:db8:10::1"
	forwardingRouter6B    = "2001:db8:20::1"
	forwardingTarget4     = "203.0.113.2"
	forwardingTarget6     = "2001:db8:f::2"
	forwardingClient4     = "192.0.2.34"
	forwardingClient6     = "2001:db8:c::2"
	forwardingPort        = 30611
	forwardingAgentPort   = 50052
)

type forwardingNamespaces struct {
	gateway, client, failover, primary, upstream netns.NsHandle
}

func TestFailoverForwardingSelectsWorkingGateway(t *testing.T) {
	if os.Getenv(forwardingChildEnv) != "1" {
		binary := protocolTestBinary(t)
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
		child.Env = append(os.Environ(), forwardingChildEnv+"=1", forwardingBinaryEnv+"="+binary)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
		}
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := t.TempDir()
	stateDir := filepath.Join(root, "mwan-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
	namespaces := newForwardingNamespaces(t)
	wireForwardingTopology(t, namespaces)
	startForwardingTargets(t, namespaces)
	router := startForwardingRouter(t)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range []string{"failover-agent", "failover-ifmgr", "primary-agent", "watchdog-run"} {
			content, err := os.ReadFile(filepath.Join(root, name+".log"))
			if err == nil {
				t.Logf("%s log: %s", name, content)
			}
		}
	})
	failoverConfig := writeForwardingConfig(t, root, "failover", true)
	primaryConfig := writeForwardingConfig(t, root, "primary", false)
	startForwardingProcess(t, namespaces, namespaces.failover, root, "failover-ifmgr", failoverConfig, "ifmgr", "--role", "failover")
	startForwardingProcess(t, namespaces, namespaces.failover, root, "failover-agent", failoverConfig, "agent", "--tcp-addr", fmt.Sprintf("%s:%d", forwardingFailoverNH4, forwardingAgentPort), "--vsock-port", "0")
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingFailoverNH4, 60*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingFailoverNH6, 60*time.Second)
	watchdogConfig := writeForwardingWatchdogConfig(t, root)
	assertForwardingWatchdogUsesFailover(t, root, watchdogConfig)
	assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
	assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
	startForwardingProcess(t, namespaces, namespaces.primary, root, "primary-agent", primaryConfig, "agent", "--tcp-addr", fmt.Sprintf("%s:%d", forwardingPrimaryNH4, forwardingAgentPort), "--vsock-port", "0")
	waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 2)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 2)
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingFailoverNH4, 10*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingFailoverNH6, 10*time.Second)
	cases := []struct {
		family        int
		network       string
		target        string
		upstreamCIDR  string
		clientNetwork string
		viaFailover   string
		viaPrimary    string
		failover      string
		primary       string
	}{
		{netlink.FAMILY_V4, "tcp4", forwardingTarget4, "198.51.100.2/30", "192.0.2.32/29", "198.51.100.1", "198.51.100.5", forwardingFailoverNH4, forwardingPrimaryNH4},
		{netlink.FAMILY_V6, "tcp6", forwardingTarget6, "2001:db8:f1::2/64", "2001:db8:c::/64", "2001:db8:f1::1", "2001:db8:f2::1", forwardingFailoverNH6, forwardingPrimaryNH6},
	}
	for _, testCase := range cases {
		forwardingInNamespace(t, namespaces, namespaces.upstream, func() {
			setForwardingUpstreamAddress(t, testCase.upstreamCIDR, false)
			forwardingRoute(t, testCase.clientNetwork, testCase.viaPrimary)
		})
		waitForwardingDefault(t, testCase.family, testCase.primary, 20*time.Second)
		assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
		assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
		assertForwardingFailoverRejected(t, watchdogConfig, "does not announce every configured address family")
		assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
		assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
		forwardingInNamespace(t, namespaces, namespaces.upstream, func() {
			setForwardingUpstreamAddress(t, testCase.upstreamCIDR, true)
			forwardingRoute(t, testCase.clientNetwork, testCase.viaFailover)
		})
		waitForwardingDefault(t, testCase.family, testCase.failover, 20*time.Second)
		assertForwardingDial(t, namespaces, testCase.network, testCase.target)
	}
	forwardingInNamespace(t, namespaces, namespaces.upstream, func() {
		setForwardingUpstreamAddress(t, "198.51.100.2/30", false)
		setForwardingUpstreamAddress(t, "2001:db8:f1::2/64", false)
		forwardingRoute(t, "192.0.2.32/29", "198.51.100.5")
		forwardingRoute(t, "2001:db8:c::/64", "2001:db8:f2::1")
	})
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingPrimaryNH4, 20*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingPrimaryNH6, 20*time.Second)
	assertForwardingFailoverRejected(t, watchdogConfig, "no announced address families are ready")
	assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
	assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
	forwardingInNamespace(t, namespaces, namespaces.upstream, func() {
		setForwardingUpstreamAddress(t, "198.51.100.2/30", true)
		setForwardingUpstreamAddress(t, "2001:db8:f1::2/64", true)
	})
	waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 2)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 2)
	for _, address := range []string{forwardingFailoverNH4, forwardingFailoverNH6} {
		if err := router.DeletePeer(context.Background(), &apipb.DeletePeerRequest{Address: address}); err != nil {
			t.Fatal(err)
		}
	}
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingPrimaryNH4, 20*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingPrimaryNH6, 20*time.Second)
	assertForwardingFailoverRejected(t, watchdogConfig, "cannot confirm that every BGP session is established")
	assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
	assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
}

func writeForwardingWatchdogConfig(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "watchdog.toml")
	config := fmt.Sprintf("mwan_vmid = \"301\"\n[network]\nping_target_ipv4 = %q\nping_target_ipv6 = %q\n[watchdog]\nmwan_agent_tcp_addr = \"%s:%d\"\nlog_file = %q\njson_log_file = %q\nrollback_lock_file = %q\nrollback_state_file = %q\n[failover]\nlxc_id = \"203\"\nagent_tcp_addr = \"%s:%d\"\n", forwardingTarget4, forwardingTarget6, forwardingPrimaryNH4, forwardingAgentPort, filepath.Join(root, "watchdog.log"), filepath.Join(root, "watchdog.json"), filepath.Join(root, "rollback.lock"), filepath.Join(root, "rollback.state"), forwardingFailoverNH4, forwardingAgentPort)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertForwardingWatchdogUsesFailover(t *testing.T, root, configPath string) {
	t.Helper()
	address := fmt.Sprintf("%s:%d", forwardingFailoverNH4, forwardingAgentPort)
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	response, err := mwanv1.NewMWANAgentClient(connection).WithdrawRoutes(context.Background(), &mwanv1.WithdrawRoutesRequest{})
	if err != nil || !response.GetSuccess() {
		t.Fatalf("withdraw failover routes: response=%v error=%v", response, err)
	}
	waitForwardingNoDefault(t, netlink.FAMILY_V4)
	waitForwardingNoDefault(t, netlink.FAMILY_V6)
	qm := filepath.Join(root, "qm")
	build := exec.Command("go", "build", "-o", qm, "../../internal/watchdog/testdata/qm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build guest status helper: %v: %s", err, output)
	}
	logFile, err := os.Create(filepath.Join(root, "watchdog-run.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	command := exec.Command(os.Getenv(forwardingBinaryEnv), "watchdog", "--red-team", "proxmox-routing", "--red-team-live", "--red-team-iterations", "8", "--config", configPath)
	command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"))
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingFailoverNH4, 40*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingFailoverNH6, 40*time.Second)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("watchdog exit: %v", err)
	}
}

func waitForwardingNoDefault(t *testing.T, family int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, route := range routes {
			if route.Dst == nil || route.Dst.IP.IsUnspecified() {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("family %d default route remained after withdrawal", family)
}

func assertForwardingFailoverRejected(t *testing.T, configPath, expected string) {
	t.Helper()
	command := exec.Command(os.Getenv(forwardingBinaryEnv), "watchdog", "failover", "--config", configPath)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), expected) {
		t.Fatalf("watchdog failover error = %v, want %q: %s", err, expected, output)
	}
}

func writeForwardingConfig(t *testing.T, root, name string, failover bool) string {
	t.Helper()
	routerID := forwardingPrimaryNH4
	nextHop6 := forwardingPrimaryNH6
	neighbor4 := forwardingRouter4B
	neighbor6 := forwardingRouter6B
	if failover {
		routerID = forwardingFailoverNH4
		nextHop6 = forwardingFailoverNH6
		neighbor4 = forwardingRouter4A
		neighbor6 = forwardingRouter6A
	}
	text := fmt.Sprintf(`[bgp]
enabled = true
asn = %d
router_id = %q
next_hop_v6 = %q
keepalive_seconds = 1
hold_seconds = 3
listen_port = -1

[[bgp.neighbors]]
address = %q

[[bgp.neighbors_v6]]
address = %q

[bgp.announce]
ipv4 = ["0.0.0.0/0"]
ipv6 = ["::/0"]
`, forwardingASN, routerID, nextHop6, neighbor4, neighbor6)
	if failover {
		text += fmt.Sprintf(`
[bgp.forwarding_readiness]
socket_path = %q
poll_interval_milliseconds = 200
read_timeout_milliseconds = 200

[ifmgr]
role = "failover"
reconcile_interval = "1s"

[ifmgr.iface.wan]
dhcp_v4 = false

[ifmgr.modules.slaac_health]
iface = "wan"
degraded_after = "1h"
escalate_after = "1h"
alert_after = "1h"
max_toggles_per_hour = 0

[ifmgr.modules.bridge_probe]
iface = "wan"

[ifmgr.modules.connectivity_probe]
iface = "wan"
targets_v4 = [%q]
targets_v6 = [%q]
timeout = "500ms"
unhealthy_after = "1s"

[ifmgr.modules.ra_lost]
iface = "wan"
`, filepath.Join(root, "ready.sock"), forwardingTarget4, forwardingTarget6)
	}
	path := filepath.Join(root, name+".toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newForwardingNamespaces(t *testing.T) forwardingNamespaces {
	t.Helper()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	namespaces := forwardingNamespaces{gateway: gateway}
	for _, handle := range []*netns.NsHandle{&namespaces.client, &namespaces.failover, &namespaces.primary, &namespaces.upstream} {
		created, err := netns.New()
		if err != nil {
			t.Fatal(err)
		}
		*handle = created
		if err := netns.Set(gateway); err != nil {
			t.Fatal(err)
		}
	}
	return namespaces
}

func wireForwardingTopology(t *testing.T, namespaces forwardingNamespaces) {
	t.Helper()
	forwardingLink(t, namespaces, namespaces.gateway, "lan", []string{"192.0.2.33/29", "2001:db8:c::1/64"}, namespaces.client, "eth0", []string{forwardingClient4 + "/29", forwardingClient6 + "/64"})
	forwardingLink(t, namespaces, namespaces.gateway, "tofailover", []string{forwardingRouter4A + "/30", forwardingRouter6A + "/64"}, namespaces.failover, "toroute", []string{forwardingFailoverNH4 + "/30", forwardingFailoverNH6 + "/64"})
	forwardingLink(t, namespaces, namespaces.gateway, "toprimary", []string{forwardingRouter4B + "/30", forwardingRouter6B + "/64"}, namespaces.primary, "toroute", []string{forwardingPrimaryNH4 + "/30", forwardingPrimaryNH6 + "/64"})
	forwardingLink(t, namespaces, namespaces.failover, "wan", []string{"198.51.100.1/30", "2001:db8:f1::1/64"}, namespaces.upstream, "fromfailover", []string{"198.51.100.2/30", "2001:db8:f1::2/64"})
	forwardingLink(t, namespaces, namespaces.primary, "wan", []string{"198.51.100.5/30", "2001:db8:f2::1/64"}, namespaces.upstream, "fromprimary", []string{"198.51.100.6/30", "2001:db8:f2::2/64"})
	forwardingInNamespace(t, namespaces, namespaces.upstream, func() {
		loopback, err := netlink.LinkByName("lo")
		if err != nil {
			t.Fatal(err)
		}
		for _, cidr := range []string{forwardingTarget4 + "/32", forwardingTarget6 + "/128"} {
			address, err := netlink.ParseAddr(cidr)
			if err != nil {
				t.Fatal(err)
			}
			if err := netlink.AddrAdd(loopback, address); err != nil {
				t.Fatal(err)
			}
		}
		forwardingRoute(t, "192.0.2.32/29", "198.51.100.1")
		forwardingRoute(t, "2001:db8:c::/64", "2001:db8:f1::1")
	})
	forwardingInNamespace(t, namespaces, namespaces.client, func() {
		forwardingRoute(t, "0.0.0.0/0", "192.0.2.33")
		forwardingRoute(t, "::/0", "2001:db8:c::1")
	})
	forwardingInNamespace(t, namespaces, namespaces.failover, func() {
		forwardingRoute(t, "0.0.0.0/0", "198.51.100.2")
		forwardingRoute(t, "::/0", "2001:db8:f1::2")
		forwardingRoute(t, "192.0.2.32/29", forwardingRouter4A)
		forwardingRoute(t, "2001:db8:c::/64", forwardingRouter6A)
	})
	forwardingInNamespace(t, namespaces, namespaces.primary, func() {
		forwardingRoute(t, "0.0.0.0/0", "198.51.100.6")
		forwardingRoute(t, "::/0", "2001:db8:f2::2")
		forwardingRoute(t, "192.0.2.32/29", forwardingRouter4B)
		forwardingRoute(t, "2001:db8:c::/64", forwardingRouter6B)
	})
	for _, handle := range []netns.NsHandle{namespaces.gateway, namespaces.failover, namespaces.primary, namespaces.upstream} {
		forwardingInNamespace(t, namespaces, handle, func() {
			loopback, err := netlink.LinkByName("lo")
			if err != nil {
				t.Fatal(err)
			}
			if err := netlink.LinkSetUp(loopback); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/proc/sys/net/ipv4/ip_forward", "/proc/sys/net/ipv6/conf/all/forwarding"} {
				if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func forwardingLink(t *testing.T, namespaces forwardingNamespaces, leftNamespace netns.NsHandle, left string, leftAddresses []string, rightNamespace netns.NsHandle, right string, rightAddresses []string) {
	t.Helper()
	const temporary = "forwardpeer"
	forwardingInNamespace(t, namespaces, leftNamespace, func() {
		veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: left}, PeerName: temporary}
		if err := netlink.LinkAdd(veth); err != nil {
			t.Fatal(err)
		}
		peer, err := netlink.LinkByName(temporary)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetNsFd(peer, int(rightNamespace)); err != nil {
			t.Fatal(err)
		}
		forwardingConfigureLink(t, left, leftAddresses)
	})
	forwardingInNamespace(t, namespaces, rightNamespace, func() {
		peer, err := netlink.LinkByName(temporary)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetName(peer, right); err != nil {
			t.Fatal(err)
		}
		forwardingConfigureLink(t, right, rightAddresses)
	})
}

func forwardingConfigureLink(t *testing.T, name string, addresses []string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, cidr := range addresses {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(link, address); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func forwardingRoute(t *testing.T, destination, gateway string) {
	t.Helper()
	_, network, err := net.ParseCIDR(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteReplace(&netlink.Route{Dst: network, Gw: net.ParseIP(gateway)}); err != nil {
		t.Fatalf("RouteReplace %s via %s: %v", destination, gateway, err)
	}
}

func setForwardingUpstreamAddress(t *testing.T, cidr string, present bool) {
	t.Helper()
	link, err := netlink.LinkByName("fromfailover")
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	if present {
		err = netlink.AddrAdd(link, address)
	} else {
		err = netlink.AddrDel(link, address)
	}
	if err != nil {
		t.Fatalf("upstream address %s present=%t: %v", cidr, present, err)
	}
}

func forwardingInNamespace(t *testing.T, namespaces forwardingNamespaces, handle netns.NsHandle, action func()) {
	t.Helper()
	if err := netns.Set(handle); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := netns.Set(namespaces.gateway); err != nil {
			t.Fatal(err)
		}
	}()
	action()
}

func startForwardingTargets(t *testing.T, namespaces forwardingNamespaces) {
	t.Helper()
	forwardingInNamespace(t, namespaces, namespaces.upstream, func() {
		for network, target := range map[string]string{"tcp4": forwardingTarget4, "tcp6": forwardingTarget6} {
			listener, err := net.Listen(network, net.JoinHostPort(target, fmt.Sprint(forwardingPort)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					_, _ = conn.Write([]byte("ok"))
					_ = conn.Close()
				}
			}()
		}
	})
}

func startForwardingRouter(t *testing.T) *server.BgpServer {
	t.Helper()
	router := server.NewBgpServer()
	go router.Serve()
	t.Cleanup(router.Stop)
	if err := router.StartBgp(context.Background(), &apipb.StartBgpRequest{Global: &apipb.Global{
		Asn: forwardingASN, RouterId: forwardingRouter4A, ListenPort: 179,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := router.WatchEvent(context.Background(), server.WatchEventMessageCallbacks{
		OnBestPath: func(paths []*apiutil.Path, _ time.Time) {
			for _, path := range paths {
				applyForwardingBestPath(path)
			}
		},
	}, server.WatchBestPath(true)); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []struct {
		address string
		afi     apipb.Family_Afi
	}{
		{forwardingFailoverNH4, apipb.Family_AFI_IP},
		{forwardingFailoverNH6, apipb.Family_AFI_IP6},
		{forwardingPrimaryNH4, apipb.Family_AFI_IP},
		{forwardingPrimaryNH6, apipb.Family_AFI_IP6},
	} {
		if err := router.AddPeer(context.Background(), &apipb.AddPeerRequest{Peer: &apipb.Peer{
			Conf:      &apipb.PeerConf{NeighborAddress: peer.address, PeerAsn: forwardingASN},
			Transport: &apipb.Transport{PassiveMode: true},
			Timers:    &apipb.Timers{Config: &apipb.TimersConfig{KeepaliveInterval: 1, HoldTime: 3}},
			AfiSafis: []*apipb.AfiSafi{{Config: &apipb.AfiSafiConfig{
				Family: &apipb.Family{Afi: peer.afi, Safi: apipb.Family_SAFI_UNICAST}, Enabled: true,
			}}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	return router
}

func applyForwardingBestPath(path *apiutil.Path) {
	if path == nil || path.Nlri == nil {
		return
	}
	_, network, err := net.ParseCIDR(path.Nlri.String())
	if err != nil {
		return
	}
	if path.Withdrawal {
		_ = netlink.RouteDel(&netlink.Route{Dst: network})
		return
	}
	for _, attribute := range path.Attrs {
		switch value := attribute.(type) {
		case *bgppkt.PathAttributeNextHop:
			_ = netlink.RouteReplace(&netlink.Route{Dst: network, Gw: net.IP(value.Value.AsSlice())})
		case *bgppkt.PathAttributeMpReachNLRI:
			_ = netlink.RouteReplace(&netlink.Route{Dst: network, Gw: net.IP(value.Nexthop.AsSlice())})
		}
	}
}

func startForwardingProcess(t *testing.T, namespaces forwardingNamespaces, handle netns.NsHandle, root, name, configPath string, arguments ...string) {
	t.Helper()
	command := exec.Command(os.Getenv(forwardingBinaryEnv), arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	logFile, err := os.Create(filepath.Join(root, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	command.Stdout = logFile
	command.Stderr = logFile
	forwardingInNamespace(t, namespaces, handle, func() {
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		_ = command.Wait()
	})
}

func waitForwardingDefault(t *testing.T, family int, gateway string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	want := net.ParseIP(gateway)
	var observed []netlink.Route
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
		if err != nil {
			t.Fatal(err)
		}
		observed = routes
		for _, route := range routes {
			defaultRoute := route.Dst == nil || route.Dst.IP.IsUnspecified()
			if defaultRoute && route.Gw.Equal(want) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("family %d default via %s absent after %s: routes=%v", family, gateway, timeout, observed)
}

func waitForwardingPathCount(t *testing.T, router *server.BgpServer, family bgppkt.Family, want int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	count := 0
	for time.Now().Before(deadline) {
		count = 0
		err := router.ListPath(apiutil.ListPathRequest{TableType: apipb.TableType_TABLE_TYPE_GLOBAL, Family: family},
			func(_ bgppkt.NLRI, paths []*apiutil.Path) { count += len(paths) })
		if err != nil {
			t.Fatal(err)
		}
		if count >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("family %v path count = %d, want %d", family, count, want)
}

func assertForwardingDial(t *testing.T, namespaces forwardingNamespaces, network, target string) {
	t.Helper()
	forwardingInNamespace(t, namespaces, namespaces.client, func() {
		deadline := time.Now().Add(15 * time.Second)
		var dialErr error
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout(network, net.JoinHostPort(target, fmt.Sprint(forwardingPort)), time.Second)
			if err == nil {
				buffer := make([]byte, 2)
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				_, err = conn.Read(buffer)
				_ = conn.Close()
			}
			if err == nil {
				return
			}
			dialErr = err
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("client %s to %s: %v", network, target, dialErr)
	})
}
