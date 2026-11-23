//go:build linux && protocol

package commandline

import (
	"context"
	"errors"
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
	"goodkind.io/mwan/internal/forwardingready"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	forwardingChildEnv     = "MWAN_FAILOVER_FORWARDING_CHILD"
	forwardingBinaryEnv    = "MWAN_FAILOVER_FORWARDING_BINARY"
	forwardingChildTimeout = 5 * time.Minute
	forwardingRPCTimeout   = 10 * time.Second
	forwardingStopTimeout  = 5 * time.Second
	forwardingASN          = 65000
	forwardingFailoverNH4  = "192.0.2.10"
	forwardingPrimaryNH4   = "192.0.2.18"
	forwardingFailoverNH6  = "2001:db8:10::2"
	forwardingPrimaryNH6   = "2001:db8:20::2"
	forwardingRouter4A     = "192.0.2.9"
	forwardingRouter4B     = "192.0.2.17"
	forwardingRouter6A     = "2001:db8:10::1"
	forwardingRouter6B     = "2001:db8:20::1"
	forwardingTarget4      = "203.0.113.2"
	forwardingTarget6      = "2001:db8:f::2"
	forwardingClient4      = "192.0.2.34"
	forwardingClient6      = "2001:db8:c::2"
	forwardingPort         = 30611
	forwardingAgentPort    = 50052
)

type forwardingNamespaces struct {
	gateway, client, failover, primary, upstream netns.NsHandle
}

func TestForwardingPeerLossSelectsBestPath(t *testing.T) {
	if os.Getenv(forwardingChildEnv) != "1" {
		binary := protocolTestBinary(t)
		childContext, cancel := context.WithTimeout(context.Background(), forwardingChildTimeout)
		defer cancel()
		child := exec.CommandContext(
			childContext,
			os.Args[0],
			"-test.run=^"+t.Name()+"$",
			"-test.v",
		)
		child.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
			Setpgid:    true,
		}
		child.Cancel = func() error {
			err := syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}

			return err
		}
		child.WaitDelay = forwardingStopTimeout
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
	root, networkDir, _ := prepareRuntimeDirectories(t)
	namespaces := newForwardingNamespaces(t)
	wireForwardingTopology(t, namespaces)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.failover,
		func() {
			addForwardingWANSupportLinks(t)
		},
	)
	startForwardingTargets(t, namespaces)
	router := startForwardingRouter(t)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range []string{
			"failover-agent",
			"failover-ifmgr",
			"primary-agent",
		} {
			content, err := os.ReadFile(filepath.Join(root, name+".log"))
			if err == nil {
				t.Logf("%s log: %s", name, content)
			}
		}
	})
	writeForwardingWANNetwork(
		t,
		networkDir,
		"wan",
		"toroute",
		"192.0.2.32/29",
		"2001:db8:c::/64",
		forwardingTarget4,
		forwardingTarget6,
		true,
	)
	wanConfig := writeForwardingConfig(t, root, "wan", true)
	primaryConfig := writeForwardingConfig(t, root, "primary", false)
	startForwardingProcess(
		t,
		namespaces,
		namespaces.failover,
		root,
		"failover-ifmgr",
		wanConfig,
		"ifmgr",
		"--role",
		"wan",
	)
	startForwardingProcess(
		t,
		namespaces,
		namespaces.failover,
		root,
		"failover-agent",
		wanConfig,
		"agent",
		"--tcp-addr",
		fmt.Sprintf("%s:%d", forwardingFailoverNH4, forwardingAgentPort),
	)
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingFailoverNH4, 60*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingFailoverNH6, 60*time.Second)
	readinessSocket := filepath.Join(root, "ready.sock")
	waitForwardingReadiness(t, readinessSocket, forwardingready.State{IPv4: true, IPv6: true})
	addForwardingWANClientRoutes(t, namespaces)
	assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
	assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
	withdrawConnection, err := grpc.NewClient(
		fmt.Sprintf("%s:%d", forwardingFailoverNH4, forwardingAgentPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer withdrawConnection.Close()
	wanClient := mwanv1.NewMWANAgentClient(withdrawConnection)
	withdrawResult, err := wanClient.WithdrawRoutes(
		forwardingRPCContext(t),
		&mwanv1.WithdrawRoutesRequest{},
	)
	if err != nil || !withdrawResult.GetSuccess() {
		t.Fatalf("withdraw WAN routes: response=%v error=%v", withdrawResult, err)
	}
	waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 0)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 0)
	assertForwardingPathCountStable(t, router, bgppkt.RF_IPv4_UC, 0, time.Second)
	assertForwardingPathCountStable(t, router, bgppkt.RF_IPv6_UC, 0, time.Second)
	waitForwardingNoDefault(t, netlink.FAMILY_V4)
	waitForwardingNoDefault(t, netlink.FAMILY_V6)
	startForwardingProcess(
		t,
		namespaces,
		namespaces.primary,
		root,
		"primary-agent",
		primaryConfig,
		"agent",
		"--tcp-addr",
		fmt.Sprintf("%s:%d", forwardingPrimaryNH4, forwardingAgentPort),
	)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 1)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 1)
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingPrimaryNH4, 10*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingPrimaryNH6, 10*time.Second)
	assertForwardingPathCountStable(t, router, bgppkt.RF_IPv4_UC, 1, time.Second)
	assertForwardingPathCountStable(t, router, bgppkt.RF_IPv6_UC, 1, time.Second)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			setForwardingUpstreamAddress(t, "198.51.100.2/30", false)
		},
	)
	waitForwardingReadiness(t, readinessSocket, forwardingready.State{IPv4: false, IPv6: true})
	resumeResult, err := wanClient.AnnounceRoutes(
		forwardingRPCContext(t),
		&mwanv1.AnnounceRoutesRequest{},
	)
	if err != nil || resumeResult.GetSuccess() {
		t.Fatalf("announce with IPv4 unready: response=%v error=%v", resumeResult, err)
	}
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			setForwardingUpstreamAddress(t, "198.51.100.2/30", true)
		},
	)
	waitForwardingReadiness(t, readinessSocket, forwardingready.State{IPv4: true, IPv6: true})
	assertForwardingPathCountStable(t, router, bgppkt.RF_IPv4_UC, 1, time.Second)
	assertForwardingPathCountStable(t, router, bgppkt.RF_IPv6_UC, 1, time.Second)
	resumeResult, err = wanClient.AnnounceRoutes(
		forwardingRPCContext(t),
		&mwanv1.AnnounceRoutesRequest{},
	)
	if err != nil || !resumeResult.GetSuccess() {
		t.Fatalf("announce after readiness recovery: response=%v error=%v", resumeResult, err)
	}
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
		{
			netlink.FAMILY_V4,
			"tcp4",
			forwardingTarget4,
			"198.51.100.2/30",
			"192.0.2.32/29",
			"198.51.100.1",
			"198.51.100.5",
			forwardingFailoverNH4,
			forwardingPrimaryNH4,
		},
		{
			netlink.FAMILY_V6,
			"tcp6",
			forwardingTarget6,
			"2001:db8:f1::2/64",
			"2001:db8:c::/64",
			"2001:db8:f1::1",
			"2001:db8:f2::1",
			forwardingFailoverNH6,
			forwardingPrimaryNH6,
		},
	}
	for _, testCase := range cases {
		forwardingInNamespace(
			t,
			namespaces,
			namespaces.upstream,
			func() {
				setForwardingUpstreamAddress(t, testCase.upstreamCIDR, false)
				forwardingRoute(t, testCase.clientNetwork, testCase.viaPrimary)
			},
		)
		waitForwardingDefault(t, testCase.family, testCase.primary, 20*time.Second)
		if testCase.family == netlink.FAMILY_V4 {
			waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 1)
			assertForwardingPathCountStable(t, router, bgppkt.RF_IPv6_UC, 2, time.Second)
		} else {
			waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 1)
			assertForwardingPathCountStable(t, router, bgppkt.RF_IPv4_UC, 2, time.Second)
		}
		assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
		assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
		forwardingInNamespace(
			t,
			namespaces,
			namespaces.upstream,
			func() {
				setForwardingUpstreamAddress(t, testCase.upstreamCIDR, true)
				forwardingRoute(t, testCase.clientNetwork, testCase.viaFailover)
			},
		)
		waitForwardingDefault(t, testCase.family, testCase.failover, 20*time.Second)
		if testCase.family == netlink.FAMILY_V4 {
			waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 2)
			assertForwardingPathCountStable(t, router, bgppkt.RF_IPv6_UC, 2, time.Second)
		} else {
			waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 2)
			assertForwardingPathCountStable(t, router, bgppkt.RF_IPv4_UC, 2, time.Second)
		}
		assertForwardingDial(t, namespaces, testCase.network, testCase.target)
	}
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			setForwardingUpstreamAddress(t, "198.51.100.2/30", false)
			setForwardingUpstreamAddress(t, "2001:db8:f1::2/64", false)
			forwardingRoute(t, "192.0.2.32/29", "198.51.100.5")
			forwardingRoute(t, "2001:db8:c::/64", "2001:db8:f2::1")
		},
	)
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingPrimaryNH4, 20*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingPrimaryNH6, 20*time.Second)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 1)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 1)
	waitForwardingReadiness(t, readinessSocket, forwardingready.State{IPv4: false, IPv6: false})
	assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
	assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			setForwardingUpstreamAddress(t, "198.51.100.2/30", true)
			setForwardingUpstreamAddress(t, "2001:db8:f1::2/64", true)
		},
	)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv4_UC, 2)
	waitForwardingPathCount(t, router, bgppkt.RF_IPv6_UC, 2)
	waitForwardingReadiness(t, readinessSocket, forwardingready.State{IPv4: true, IPv6: true})
	for _, address := range []string{forwardingFailoverNH4, forwardingFailoverNH6} {
		if err := router.DeletePeer(
			context.Background(),
			&apipb.DeletePeerRequest{Address: address},
		); err != nil {
			t.Fatal(err)
		}
	}
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingPrimaryNH4, 20*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingPrimaryNH6, 20*time.Second)
	assertForwardingDial(t, namespaces, "tcp4", forwardingTarget4)
	assertForwardingDial(t, namespaces, "tcp6", forwardingTarget6)
}

func waitForwardingNoDefault(t *testing.T, family int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(
			family,
			&netlink.Route{Table: unix.RT_TABLE_MAIN},
			netlink.RT_FILTER_TABLE,
		)
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
	text := fmt.Sprintf(
		`[bgp]
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
`,
		forwardingASN,
		routerID,
		nextHop6,
		neighbor4,
		neighbor6,
	)
	if failover {
		text += fmt.Sprintf(
			`
[bgp.forwarding_readiness]
socket_path = %q
poll_interval_milliseconds = 200
read_timeout_milliseconds = 200

[ifmgr]
role = "wan"
reconcile_interval = "1s"
[ifmgr.iface.enmwanbr0]
[ifmgr.modules.links]
state_file = %q
[ifmgr.modules.addresses]
state_file = %q
[ifmgr.modules.autoconfiguration]
state_file = %q
[ifmgr.modules.bgp_sessions]
state_file = %q
[wanconfig]
publish = true
`,
			filepath.Join(root, "ready.sock"),
			filepath.Join(root, "wan-links.json"),
			filepath.Join(root, "wan-addresses.json"),
			filepath.Join(root, "wan-kernel-policy.json"),
			filepath.Join(root, "wan-bgp-sessions.json"),
		)
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
	for _, handle := range []*netns.NsHandle{
		&namespaces.client,
		&namespaces.failover,
		&namespaces.primary,
		&namespaces.upstream,
	} {
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
	forwardingLink(
		t,
		namespaces,
		namespaces.gateway,
		"lan",
		[]string{"192.0.2.33/29", "2001:db8:c::1/64"},
		namespaces.client,
		"eth0",
		[]string{forwardingClient4 + "/29", forwardingClient6 + "/64"},
	)
	forwardingLink(
		t,
		namespaces,
		namespaces.gateway,
		"tofailover",
		[]string{forwardingRouter4A + "/30", forwardingRouter6A + "/64"},
		namespaces.failover,
		"toroute",
		[]string{forwardingFailoverNH4 + "/30", forwardingFailoverNH6 + "/64"},
	)
	forwardingLink(
		t,
		namespaces,
		namespaces.gateway,
		"toprimary",
		[]string{forwardingRouter4B + "/30", forwardingRouter6B + "/64"},
		namespaces.primary,
		"toroute",
		[]string{forwardingPrimaryNH4 + "/30", forwardingPrimaryNH6 + "/64"},
	)
	forwardingLink(
		t,
		namespaces,
		namespaces.failover,
		"wan",
		[]string{"198.51.100.1/30", "2001:db8:f1::1/64"},
		namespaces.upstream,
		"fromfailover",
		[]string{"198.51.100.2/30", "2001:db8:f1::2/64"},
	)
	forwardingLink(
		t,
		namespaces,
		namespaces.primary,
		"wan",
		[]string{"198.51.100.5/30", "2001:db8:f2::1/64"},
		namespaces.upstream,
		"fromprimary",
		[]string{"198.51.100.6/30", "2001:db8:f2::2/64"},
	)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
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
		},
	)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.client,
		func() {
			forwardingRoute(t, "0.0.0.0/0", "192.0.2.33")
			forwardingRoute(t, "::/0", "2001:db8:c::1")
		},
	)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.failover,
		func() {
			forwardingRoute(t, "0.0.0.0/0", "198.51.100.2")
			forwardingRoute(t, "::/0", "2001:db8:f1::2")
			forwardingRoute(t, "192.0.2.32/29", forwardingRouter4A)
			forwardingRoute(t, "2001:db8:c::/64", forwardingRouter6A)
		},
	)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.primary,
		func() {
			forwardingRoute(t, "0.0.0.0/0", "198.51.100.6")
			forwardingRoute(t, "::/0", "2001:db8:f2::2")
			forwardingRoute(t, "192.0.2.32/29", forwardingRouter4B)
			forwardingRoute(t, "2001:db8:c::/64", forwardingRouter6B)
		},
	)
	for _, handle := range []netns.NsHandle{
		namespaces.gateway,
		namespaces.failover,
		namespaces.primary,
		namespaces.upstream,
	} {
		forwardingInNamespace(
			t,
			namespaces,
			handle,
			func() {
				loopback, err := netlink.LinkByName("lo")
				if err != nil {
					t.Fatal(err)
				}
				if err := netlink.LinkSetUp(loopback); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{
					"/proc/sys/net/ipv4/ip_forward",
					"/proc/sys/net/ipv6/conf/all/forwarding",
				} {
					if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			},
		)
	}
}

func addForwardingWANClientRoutes(t *testing.T, namespaces forwardingNamespaces) {
	t.Helper()
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.failover,
		func() {
			link, err := netlink.LinkByName("toroute")
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range []struct {
				prefix  string
				target  string
				gateway string
			}{
				{
					prefix:  "192.0.2.34/32",
					target:  forwardingClient4,
					gateway: forwardingRouter4A,
				},
				{
					prefix:  "2001:db8:c::/64",
					target:  forwardingClient6,
					gateway: forwardingRouter6A,
				},
			} {
				destination, err := netlink.ParseIPNet(route.prefix)
				if err != nil {
					t.Fatal(err)
				}
				if err := netlink.RouteReplace(&netlink.Route{
					LinkIndex: link.Attrs().Index,
					Dst:       destination,
					Gw:        net.ParseIP(route.gateway),
					Table:     100,
				}); err != nil {
					t.Fatal(err)
				}
				waitForwardingWANPolicyRoute(t, route.target, route.gateway, 10*time.Second)
			}
		},
	)
}

func forwardingRPCContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), forwardingRPCTimeout)
	t.Cleanup(cancel)
	return ctx
}

func waitForwardingWANPolicyRoute(t *testing.T, target, gateway string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var observed []netlink.Route
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteGetWithOptions(
			net.ParseIP(target),
			&netlink.RouteGetOptions{Mark: 1},
		)
		if err != nil {
			t.Fatal(err)
		}
		observed = routes
		for _, route := range routes {
			if route.Table == 100 && route.Gw.Equal(net.ParseIP(gateway)) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"marked route to %s = %+v after %s, want table 100 via %s",
		target,
		observed,
		timeout,
		gateway,
	)
}

func forwardingLink(
	t *testing.T,
	namespaces forwardingNamespaces,
	leftNamespace netns.NsHandle,
	left string,
	leftAddresses []string,
	rightNamespace netns.NsHandle,
	right string,
	rightAddresses []string,
) {
	t.Helper()
	const temporary = "forwardpeer"
	forwardingInNamespace(
		t,
		namespaces,
		leftNamespace,
		func() {
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
		},
	)
	forwardingInNamespace(
		t,
		namespaces,
		rightNamespace,
		func() {
			peer, err := netlink.LinkByName(temporary)
			if err != nil {
				t.Fatal(err)
			}
			if err := netlink.LinkSetName(peer, right); err != nil {
				t.Fatal(err)
			}
			forwardingConfigureLink(t, right, rightAddresses)
		},
	)
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
	if err := netlink.RouteReplace(&netlink.Route{
		Dst: network,
		Gw:  net.ParseIP(gateway),
	}); err != nil {
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

func forwardingInNamespace(
	t *testing.T,
	namespaces forwardingNamespaces,
	handle netns.NsHandle,
	action func(),
) {
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
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			for network, target := range map[string]string{
				"tcp4": forwardingTarget4,
				"tcp6": forwardingTarget6,
			} {
				listener, err := net.Listen(
					network,
					net.JoinHostPort(target, fmt.Sprint(forwardingPort)),
				)
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
		},
	)
}

func startForwardingRouter(t *testing.T) *server.BgpServer {
	t.Helper()
	router := server.NewBgpServer()
	go router.Serve()
	t.Cleanup(router.Stop)
	if err := router.StartBgp(
		context.Background(),
		&apipb.StartBgpRequest{
			Global: &apipb.Global{
				Asn:        forwardingASN,
				RouterId:   forwardingRouter4A,
				ListenPort: 179,
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := router.WatchEvent(
		context.Background(),
		server.WatchEventMessageCallbacks{
			OnBestPath: func(paths []*apiutil.Path, _ time.Time) {
				for _, path := range paths {
					applyForwardingBestPath(path)
				}
			},
		},
		server.WatchBestPath(true),
	); err != nil {
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
		if err := router.AddPeer(
			context.Background(),
			&apipb.AddPeerRequest{
				Peer: &apipb.Peer{
					Conf: &apipb.PeerConf{
						NeighborAddress: peer.address,
						PeerAsn:         forwardingASN,
					},
					Transport: &apipb.Transport{PassiveMode: true},
					Timers: &apipb.Timers{
						Config: &apipb.TimersConfig{KeepaliveInterval: 1, HoldTime: 3},
					},
					AfiSafis: []*apipb.AfiSafi{
						{
							Config: &apipb.AfiSafiConfig{
								Family: &apipb.Family{
									Afi:  peer.afi,
									Safi: apipb.Family_SAFI_UNICAST,
								},
								Enabled: true,
							},
						},
					},
				},
			},
		); err != nil {
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
			_ = netlink.RouteReplace(&netlink.Route{
				Dst: network,
				Gw:  net.IP(value.Value.AsSlice()),
			})
		case *bgppkt.PathAttributeMpReachNLRI:
			_ = netlink.RouteReplace(&netlink.Route{
				Dst: network,
				Gw:  net.IP(value.Nexthop.AsSlice()),
			})
		}
	}
}

func startForwardingProcess(
	t *testing.T,
	namespaces forwardingNamespaces,
	handle netns.NsHandle,
	root string,
	name string,
	configPath string,
	arguments ...string,
) {
	t.Helper()
	command := exec.Command(os.Getenv(forwardingBinaryEnv), arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	logFile, err := os.Create(filepath.Join(root, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	command.Stdout = logFile
	command.Stderr = logFile
	forwardingInNamespace(
		t,
		namespaces,
		handle,
		func() {
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
		},
	)
	t.Cleanup(func() {
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			t.Errorf("send SIGTERM to %s: %v", name, err)
		}
		wait := make(chan error, 1)
		go func() { wait <- command.Wait() }()
		timer := time.NewTimer(forwardingStopTimeout)
		defer timer.Stop()
		select {
		case <-wait:
		case <-timer.C:
			if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil &&
				!errors.Is(err, syscall.ESRCH) {
				t.Errorf("send SIGKILL to %s: %v", name, err)
			}
			<-wait
		}
	})
}

func waitForwardingDefault(t *testing.T, family int, gateway string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	want := net.ParseIP(gateway)
	var observed []netlink.Route
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(
			family,
			&netlink.Route{Table: unix.RT_TABLE_MAIN},
			netlink.RT_FILTER_TABLE,
		)
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
	t.Fatalf(
		"family %d default via %s absent after %s: routes=%v",
		family,
		gateway,
		timeout,
		observed,
	)
}

func waitForwardingPathCount(
	t *testing.T,
	router *server.BgpServer,
	family bgppkt.Family,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	count := 0
	for time.Now().Before(deadline) {
		count = 0
		err := router.ListPath(
			apiutil.ListPathRequest{TableType: apipb.TableType_TABLE_TYPE_GLOBAL, Family: family},
			func(_ bgppkt.NLRI, paths []*apiutil.Path) { count += len(paths) },
		)
		if err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("family %v path count = %d, want %d", family, count, want)
}

func assertForwardingPathCountStable(
	t *testing.T,
	router *server.BgpServer,
	family bgppkt.Family,
	want int,
	duration time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		count := 0
		if err := router.ListPath(
			apiutil.ListPathRequest{TableType: apipb.TableType_TABLE_TYPE_GLOBAL, Family: family},
			func(_ bgppkt.NLRI, paths []*apiutil.Path) { count += len(paths) },
		); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf(
				"family %v path count = %d during stability check, want %d",
				family,
				count,
				want,
			)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForwardingReadiness(t *testing.T, socket string, expected forwardingready.State) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var observed forwardingready.State
	var readErr error
	for time.Now().Before(deadline) {
		observed, readErr = forwardingready.Read(context.Background(), socket, 500*time.Millisecond)
		if readErr == nil && observed == expected {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("forwarding readiness=%+v expected=%+v error=%v", observed, expected, readErr)
}

func assertForwardingDial(t *testing.T, namespaces forwardingNamespaces, network, target string) {
	t.Helper()
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.client,
		func() {
			deadline := time.Now().Add(15 * time.Second)
			var dialErr error
			for time.Now().Before(deadline) {
				conn, err := net.DialTimeout(
					network,
					net.JoinHostPort(target, fmt.Sprint(forwardingPort)),
					time.Second,
				)
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
			logForwardingDialState(t, namespaces, target)
			t.Fatalf("client %s to %s: %v", network, target, dialErr)
		},
	)
}

func logForwardingDialState(t *testing.T, namespaces forwardingNamespaces, target string) {
	t.Helper()
	for _, namespace := range []struct {
		name   string
		handle netns.NsHandle
	}{
		{name: "client", handle: namespaces.client},
		{name: "gateway", handle: namespaces.gateway},
		{name: "wan", handle: namespaces.failover},
		{name: "upstream", handle: namespaces.upstream},
	} {
		forwardingInNamespace(
			t,
			namespaces,
			namespace.handle,
			func() {
				routes, err := netlink.RouteGet(net.ParseIP(target))
				t.Logf("%s route get %s: %+v (error: %v)", namespace.name, target, routes, err)
				clientTarget := forwardingClient4
				if strings.Contains(target, ":") {
					clientTarget = forwardingClient6
				}
				routes, err = netlink.RouteGet(net.ParseIP(clientTarget))
				t.Logf(
					"%s route get client %s: %+v (error: %v)",
					namespace.name,
					clientTarget,
					routes,
					err,
				)
				routes, err = netlink.RouteList(nil, netlink.FAMILY_ALL)
				t.Logf("%s routes: %+v (error: %v)", namespace.name, routes, err)
				for _, table := range []int{100, 200, 300} {
					routes, err = netlink.RouteListFiltered(
						netlink.FAMILY_ALL,
						&netlink.Route{Table: table},
						netlink.RT_FILTER_TABLE,
					)
					t.Logf(
						"%s table %d routes: %+v (error: %v)",
						namespace.name,
						table,
						routes,
						err,
					)
				}
				rules, err := netlink.RuleList(netlink.FAMILY_ALL)
				t.Logf("%s rules: %+v (error: %v)", namespace.name, rules, err)
				neighbors, err := netlink.NeighList(0, netlink.FAMILY_ALL)
				t.Logf("%s neighbors: %+v (error: %v)", namespace.name, neighbors, err)
				conntrack, err := os.ReadFile("/proc/net/nf_conntrack")
				t.Logf("%s conntrack: %s (error: %v)", namespace.name, conntrack, err)
				ipv4Forwarding, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
				t.Logf("%s IPv4 forwarding: %s (error: %v)", namespace.name, ipv4Forwarding, err)
				ipv6Forwarding, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
				t.Logf("%s IPv6 forwarding: %s (error: %v)", namespace.name, ipv6Forwarding, err)
				links, err := netlink.LinkList()
				var names []string
				for _, link := range links {
					names = append(names, link.Attrs().Name)
				}
				t.Logf("%s links: %v (error: %v)", namespace.name, names, err)
				ctx, cancel := context.WithTimeout(context.Background(), forwardingStopTimeout)
				defer cancel()
				output, err := exec.CommandContext(ctx, "nft", "list", "ruleset").CombinedOutput()
				t.Logf("%s nft ruleset: %s (error: %v)", namespace.name, output, err)
			},
		)
	}
}
