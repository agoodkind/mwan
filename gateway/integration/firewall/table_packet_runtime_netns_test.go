//go:build linux && firewall

package firewall_test

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	runtimeChildEnv  = "MWAN_FIREWALL_RUNTIME_TEST_CHILD"
	runtimeBinaryEnv = "MWAN_FIREWALL_RUNTIME_TEST_BINARY"
)

func TestWANFirewallRuntimePackets(t *testing.T) {
	if os.Getenv(runtimeChildEnv) == "1" {
		runWANFirewallRuntimeChild(t)
		return
	}
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestWANFirewallRuntimePackets$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), runtimeChildEnv+"=1", runtimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("The isolated WAN runtime test failed: %v: %s", err, output)
	}
}

func runWANFirewallRuntimeChild(t *testing.T) {
	testenv.MountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(
		fixture,
		[]byte(`"goodkind-mwan-steering:link-files": "rendered"`),
		[]byte(`"goodkind-mwan-steering:link-files": "hand-authored"`),
	)
	legacyWAN := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`,
	)
	typedWAN := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "` + testenv.RuntimeWANMAC + `" }
        }`,
	)
	if !bytes.Contains(fixture, legacyWAN) {
		t.Fatal("test fixture has no hand-authored enatt0 entry")
	}
	fixture = bytes.Replace(fixture, legacyWAN, typedWAN, 1)
	healthTarget := []byte(`"forced-dscp": 8`)
	healthConfig := []byte(
		`"forced-dscp": 8, "health": {"enabled": true, "ping-count": 1, "success-threshold": 1, "failure-threshold": 1, "recovery-threshold": 1, "check-interval": 1, "targets-v4": ["198.51.100.2"], "targets-v6": ["2001:db8:2::2"]}`,
	)
	if !bytes.Contains(fixture, healthTarget) {
		t.Fatal("native provider has no forced-dscp field")
	}
	fixture = bytes.Replace(fixture, healthTarget, healthConfig, 1)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := "[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = false\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		"/proc/sys/net/ipv6/conf/all/forwarding",
		[]byte("1"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	lan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29", "2001:db8:b01::1/64", "2001:db8:b01:fe::1/64"},
		[]string{"192.0.2.2/29", "2001:db8:b01::2/64", "2001:db8:b01:fe::2/64"},
		"",
	)
	defer management.Namespace.Close()
	defer lan.Namespace.Close()
	testenv.SetRuntimeNamespace(t, gateway)

	binary := os.Getenv(runtimeBinaryEnv)
	first := testenv.StartRuntimeDaemon(t, binary, configPath, root, "first")
	defer testenv.StopRuntimeDaemon(t, first)
	testenv.WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	testenv.AssertRuntimeTCP(
		t,
		gateway,
		management.Namespace,
		"tcp4",
		"203.0.113.1:22",
		"203.0.113.2:0",
		true,
	)
	testenv.AssertRuntimeTCP(
		t,
		gateway,
		management.Namespace,
		"tcp4",
		"203.0.113.1:2222",
		"203.0.113.2:0",
		false,
	)
	testenv.WaitRuntimeTable(t, first, "ip", "nat", 10*time.Second)
	testenv.WaitRuntimeTable(t, first, "inet", "mangle", 10*time.Second)

	wan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-host",
		[]string{"198.51.100.1/24", "2001:db8:2::1/64"},
		[]string{"198.51.100.2/24", "2001:db8:2::2/64"},
		testenv.RuntimeWANMAC,
	)
	defer wan.Namespace.Close()
	testenv.AddRuntimeDefault(t, "enatt0", "198.51.100.2")
	testenv.AddRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	testenv.AddRuntimeRoute(t, gateway, lan.Namespace, "198.51.100.0/24", "192.0.2.1")
	testenv.AddRuntimeRoute(t, gateway, lan.Namespace, "2001:db8:2::/64", "2001:db8:b01::1")
	testenv.AddRuntimeRoute(t, gateway, wan.Namespace, "192.0.2.0/29", "198.51.100.1")
	testenv.AddRuntimeRoute(t, gateway, wan.Namespace, "2001:db8:b01:fe::/64", "2001:db8:2::1")
	testenv.WaitRuntimeForwarding(t, first, 10*time.Second)
	testenv.AssertRuntimeTCP(
		t,
		wan.Namespace,
		lan.Namespace,
		"tcp4",
		"198.51.100.2:3001",
		"192.0.2.2:0",
		true,
	)
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.AssertRuntimeTCP(
		t,
		wan.Namespace,
		lan.Namespace,
		"tcp6",
		"[2001:db8:2::2]:3002",
		"[2001:db8:b01:fe::2]:0",
		true,
	)
	testenv.SetRuntimeNamespace(t, gateway)
	assertRuntimeOwnedReturnRouteRepair(t, first, gateway, wan.Namespace, lan.Namespace)
	testenv.SetRuntimeNamespace(t, gateway)
	assertRuntimeOwnedPolicyRuleRepair(t, first, gateway, wan.Namespace, lan.Namespace)

	testenv.SetRuntimeNamespace(t, gateway)
	wanLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	oldWANIndex := wanLink.Attrs().Index
	waitRuntimeOwnedDefaults(t, first, oldWANIndex, time.Now().Add(10*time.Second))
	daemonPID := first.Command.Process.Pid
	testenv.SetRuntimeNamespace(t, wan.Namespace)
	failedV4Listener, err := net.Listen("tcp4", "198.51.100.2:3012")
	if err != nil {
		t.Fatal(err)
	}
	defer failedV4Listener.Close()
	failedV6Listener, err := net.Listen("tcp6", "[2001:db8:2::2]:3013")
	if err != nil {
		t.Fatal(err)
	}
	defer failedV6Listener.Close()
	testenv.SetRuntimeNamespace(t, gateway)
	deletionLogStart := len(testenv.RuntimeDaemonLog(t, first))
	if err := netlink.LinkDel(wanLink); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDialFails(t, gateway, lan.Namespace, "tcp4", "198.51.100.2:3012", "192.0.2.2:0")
	assertRuntimeDialFails(
		t,
		gateway,
		lan.Namespace,
		"tcp6",
		"[2001:db8:2::2]:3013",
		"[2001:db8:b01:fe::2]:0",
	)
	deletionDeadline := time.Now().Add(15 * time.Second)
	waitRuntimeMonitorResync(t, first, deletionLogStart, deletionDeadline)
	waitRuntimeOwnedDefaultsAbsent(t, first, oldWANIndex, deletionDeadline)
	logStart := len(testenv.RuntimeDaemonLog(t, first))
	wan = testenv.NewRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-new",
		[]string{"198.51.100.1/24", "2001:db8:2::1/64"},
		[]string{"198.51.100.2/24", "2001:db8:2::2/64"},
		testenv.RuntimeWANMAC,
	)
	defer wan.Namespace.Close()
	replacementLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if replacementLink.Attrs().Index == oldWANIndex {
		t.Fatalf("recreated enatt0 reused index %d", oldWANIndex)
	}
	testenv.AddRuntimeDefault(t, "enatt0", "198.51.100.2")
	testenv.AddRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	testenv.AddRuntimeRoute(t, gateway, wan.Namespace, "192.0.2.0/29", "198.51.100.1")
	testenv.AddRuntimeRoute(t, gateway, wan.Namespace, "2001:db8:b01:fe::/64", "2001:db8:2::1")
	recoveryDeadline := time.Now().Add(15 * time.Second)
	waitRuntimeMonitorResync(t, first, logStart, recoveryDeadline)
	waitRuntimeOwnedDefaults(t, first, replacementLink.Attrs().Index, recoveryDeadline)
	testenv.WaitRuntimeTCP(
		t,
		first,
		gateway,
		wan.Namespace,
		lan.Namespace,
		"tcp4",
		"198.51.100.2:3014",
		"192.0.2.2:0",
		recoveryDeadline,
	)
	testenv.WaitRuntimeTCP(
		t,
		first,
		gateway,
		wan.Namespace,
		lan.Namespace,
		"tcp6",
		"[2001:db8:2::2]:3015",
		"[2001:db8:b01:fe::2]:0",
		recoveryDeadline,
	)
	testenv.AssertRuntimeDaemonRunning(t, first)
	if first.Command.Process.Pid != daemonPID {
		t.Fatalf("WAN daemon PID changed from %d to %d", daemonPID, first.Command.Process.Pid)
	}

	testenv.RunRuntimeNFT(t, "flush", "ruleset")
	testenv.WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	testenv.WaitRuntimeTable(t, first, "ip", "nat", 10*time.Second)
	testenv.WaitRuntimeTable(t, first, "inet", "mangle", 10*time.Second)
	testenv.AssertRuntimeTCP(
		t,
		gateway,
		management.Namespace,
		"tcp4",
		"203.0.113.1:2222",
		"203.0.113.2:0",
		false,
	)
	testenv.AssertRuntimeTCP(
		t,
		wan.Namespace,
		lan.Namespace,
		"tcp4",
		"198.51.100.2:3003",
		"192.0.2.2:0",
		true,
	)
	testenv.AssertRuntimeTCP(
		t,
		wan.Namespace,
		lan.Namespace,
		"tcp6",
		"[2001:db8:2::2]:3004",
		"[2001:db8:b01:fe::2]:0",
		true,
	)
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.StopRuntimeDaemon(t, first)
	testenv.AssertRuntimeTable(t, "inet", "filter")
	testenv.AssertRuntimeTable(t, "ip", "nat")
	testenv.AssertRuntimeTable(t, "inet", "mangle")
	testenv.RunRuntimeNFT(t, "flush", "ruleset")

	second := testenv.StartRuntimeDaemon(t, binary, configPath, root, "second")
	defer testenv.StopRuntimeDaemon(t, second)
	testenv.WaitRuntimeTable(t, second, "inet", "filter", 10*time.Second)
	testenv.WaitRuntimeTable(t, second, "ip", "nat", 10*time.Second)
	testenv.WaitRuntimeTable(t, second, "inet", "mangle", 10*time.Second)
	testenv.AssertRuntimeTCP(
		t,
		gateway,
		management.Namespace,
		"tcp4",
		"203.0.113.1:2222",
		"203.0.113.2:0",
		false,
	)
	testenv.AssertRuntimeTCP(
		t,
		wan.Namespace,
		lan.Namespace,
		"tcp4",
		"198.51.100.2:3005",
		"192.0.2.2:0",
		true,
	)
	testenv.AssertRuntimeTCP(
		t,
		wan.Namespace,
		lan.Namespace,
		"tcp6",
		"[2001:db8:2::2]:3006",
		"[2001:db8:b01:fe::2]:0",
		true,
	)
	testenv.AssertRuntimeDaemonRunning(t, second)
}

func assertRuntimeOwnedReturnRouteRepair(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	gateway netns.NsHandle,
	wan netns.NsHandle,
	lan netns.NsHandle,
) {
	t.Helper()
	_, foreignDestination, err := net.ParseCIDR("192.0.2.0/29")
	if err != nil {
		t.Fatal(err)
	}
	internal, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	foreign := &netlink.Route{
		LinkIndex: internal.Attrs().Index,
		Dst:       foreignDestination,
		Table:     150,
		Protocol:  unix.RTPROT_STATIC,
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(foreign); err != nil {
		t.Fatal(err)
	}
	var owned *netlink.Route
	var routes []netlink.Route
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && owned == nil {
		var err error
		routes, err = netlink.RouteListFiltered(
			unix.AF_INET,
			&netlink.Route{Table: 100},
			netlink.RT_FILTER_TABLE,
		)
		if err != nil {
			t.Fatal(err)
		}
		for i := range routes {
			if routes[i].Dst != nil && routes[i].Dst.String() == "192.0.2.0/29" {
				owned = &routes[i]
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if owned == nil {
		t.Fatalf("owned internal return route missing before deletion: %v", routes)
	}
	if err := netlink.RouteDel(owned); err != nil {
		t.Fatal(err)
	}
	testenv.AssertRuntimeTCP(t, wan, lan, "tcp4", "198.51.100.2:3009", "192.0.2.2:0", false)
	testenv.SetRuntimeNamespace(t, gateway)
	deletedAt := time.Now()
	deadline = deletedAt.Add(5 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(
			unix.AF_INET,
			&netlink.Route{Table: 100},
			netlink.RT_FILTER_TABLE,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() == "192.0.2.0/29" {
				foreignRoutes, err := netlink.RouteListFiltered(
					unix.AF_INET,
					&netlink.Route{Table: 150},
					netlink.RT_FILTER_TABLE,
				)
				if err != nil || len(foreignRoutes) != 1 {
					t.Fatalf("unrelated route changed during repair: %v, %v", foreignRoutes, err)
				}
				t.Logf("owned route restored after %s", time.Since(deletedAt))
				testenv.AssertRuntimeTCP(
					t,
					wan,
					lan,
					"tcp4",
					"198.51.100.2:3010",
					"192.0.2.2:0",
					true,
				)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"owned return route was not restored before the 1h periodic reconcile; daemon log: %s",
		testenv.RuntimeLogTail(t, daemon, 30),
	)
}

func assertRuntimeOwnedPolicyRuleRepair(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	gateway netns.NsHandle,
	wan netns.NsHandle,
	lan netns.NsHandle,
) {
	t.Helper()
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	var owned *netlink.Rule
	for i := range rules {
		if rules[i].Priority == 100 && rules[i].Table == 100 && rules[i].Mark == 1 {
			owned = &rules[i]
			break
		}
	}
	if owned == nil {
		t.Fatalf("owned policy rule missing before deletion: %v", rules)
	}
	if err := netlink.RuleDel(owned); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Now()
	deadline := deletedAt.Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rules, err = netlink.RuleList(unix.AF_INET)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range rules {
			if rule.Priority == 100 && rule.Table == 100 && rule.Mark == 1 {
				t.Logf("owned rule restored after %s", time.Since(deletedAt))
				testenv.AssertRuntimeTCP(
					t,
					wan,
					lan,
					"tcp4",
					"198.51.100.2:3011",
					"192.0.2.2:0",
					true,
				)
				testenv.SetRuntimeNamespace(t, gateway)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"owned policy rule was not restored before the 1h periodic reconcile; daemon log: %s",
		testenv.RuntimeLogTail(t, daemon, 30),
	)
}

func assertRuntimeDialFails(
	t *testing.T,
	gateway netns.NsHandle,
	sourceNamespace netns.NsHandle,
	network string,
	destination string,
	source string,
) {
	t.Helper()
	testenv.SetRuntimeNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	connection, err := dialer.Dial(network, destination)
	if connection != nil {
		connection.Close()
	}
	testenv.SetRuntimeNamespace(t, gateway)
	if err == nil {
		t.Fatalf("outbound %s TCP connection succeeded after enatt0 deletion", network)
	}
}

func waitRuntimeMonitorResync(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	logStart int,
	deadline time.Time,
) {
	t.Helper()
	for time.Now().Before(deadline) {
		log := testenv.RuntimeDaemonLog(t, daemon)
		if len(log) >= logStart {
			for _, line := range strings.Split(log[logStart:], "\n") {
				if strings.Contains(line, `"msg":"ifmgr: reconcile requested"`) &&
					strings.Contains(line, `"reason":"interface monitor resubscribed"`) {
					return
				}
			}
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"WAN daemon did not request reconcile after interface resync: %s",
		testenv.RuntimeLogTail(t, daemon, 30),
	)
}

func waitRuntimeOwnedDefaults(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	linkIndex int,
	deadline time.Time,
) {
	t.Helper()
	for time.Now().Before(deadline) {
		ready := true
		for _, family := range []struct {
			value   int
			gateway net.IP
		}{
			{value: unix.AF_INET, gateway: net.ParseIP("198.51.100.2")},
			{value: unix.AF_INET6, gateway: net.ParseIP("2001:db8:2::2")},
		} {
			routes, err := netlink.RouteListFiltered(
				family.value,
				&netlink.Route{Table: 100},
				netlink.RT_FILTER_TABLE,
			)
			if err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, route := range routes {
				if route.Dst != nil {
					prefixLength, _ := route.Dst.Mask.Size()
					if prefixLength != 0 {
						continue
					}
				}
				if route.LinkIndex == linkIndex && route.Gw.Equal(family.gateway) {
					matched = true
					break
				}
			}
			ready = ready && matched
		}
		if ready {
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"WAN daemon did not restore table 100 defaults on index %d: %s",
		linkIndex,
		testenv.RuntimeLogTail(t, daemon, 30),
	)
}

func waitRuntimeOwnedDefaultsAbsent(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	linkIndex int,
	deadline time.Time,
) {
	t.Helper()
	for time.Now().Before(deadline) {
		found := false
		for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
			routes, err := netlink.RouteListFiltered(
				family,
				&netlink.Route{Table: 100},
				netlink.RT_FILTER_TABLE,
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range routes {
				if route.LinkIndex != linkIndex {
					continue
				}
				if route.Dst != nil {
					prefixLength, _ := route.Dst.Mask.Size()
					if prefixLength != 0 {
						continue
					}
				}
				found = true
			}
		}
		if !found {
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"table 100 defaults still reference deleted enatt0 index %d: %s",
		linkIndex,
		testenv.RuntimeLogTail(t, daemon, 30),
	)
}
