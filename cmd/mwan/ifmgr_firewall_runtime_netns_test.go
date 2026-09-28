//go:build linux && firewallnetns

package main

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

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	runtimeChildEnv  = "MWAN_FIREWALL_RUNTIME_TEST_CHILD"
	runtimeBinaryEnv = "MWAN_FIREWALL_RUNTIME_TEST_BINARY"
	runtimeWANMAC    = "02:00:5e:00:53:aa"
)

type runtimePeer struct {
	namespace netns.NsHandle
}

func TestWANFirewallRuntimePackets(t *testing.T) {
	if os.Getenv(runtimeChildEnv) == "1" {
		runWANFirewallRuntimeChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("The mwan build failed: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWANFirewallRuntimePackets$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), runtimeChildEnv+"=1", runtimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("The isolated WAN runtime test failed: %v: %s", err, output)
	}
}

func runWANFirewallRuntimeChild(t *testing.T) {
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
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte(`"goodkind-mwan-steering:link-files": "rendered"`), []byte(`"goodkind-mwan-steering:link-files": "hand-authored"`))
	legacyWAN := []byte(`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`)
	typedWAN := []byte(`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "` + runtimeWANMAC + `" }
        }`)
	if !bytes.Contains(fixture, legacyWAN) {
		t.Fatal("test fixture has no hand-authored enatt0 entry")
	}
	fixture = bytes.Replace(fixture, legacyWAN, typedWAN, 1)
	healthTarget := []byte(`"forced-dscp": 8`)
	healthConfig := []byte(`"forced-dscp": 8, "health": {"enabled": true, "ping-count": 1, "success-threshold": 1, "failure-threshold": 1, "recovery-threshold": 1, "check-interval": 1, "targets-v4": ["198.51.100.2"], "targets-v6": ["2001:db8:2::2"]}`)
	if !bytes.Contains(fixture, healthTarget) {
		t.Fatal("native provider has no forced-dscp field")
	}
	fixture = bytes.Replace(fixture, healthTarget, healthConfig, 1)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := "[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = false\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01::1/64", "2001:db8:b01:fe::1/64"}, []string{"192.0.2.2/29", "2001:db8:b01::2/64", "2001:db8:b01:fe::2/64"}, "")
	defer management.namespace.Close()
	defer lan.namespace.Close()
	setRuntimeNamespace(t, gateway)

	binary := os.Getenv(runtimeBinaryEnv)
	first := startRuntimeDaemon(t, binary, configPath, root, "first")
	defer stopRuntimeDaemon(t, first)
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	assertRuntimeTCP(t, gateway, management.namespace, "tcp4", "203.0.113.1:22", "203.0.113.2:0", true)
	assertRuntimeTCP(t, gateway, management.namespace, "tcp4", "203.0.113.1:2222", "203.0.113.2:0", false)
	waitRuntimeTable(t, first, "ip", "nat", 10*time.Second)
	waitRuntimeTable(t, first, "inet", "mangle", 10*time.Second)
	assertRuntimeNoReconcileLoop(t, first)

	wan := newRuntimePeer(t, gateway, "enatt0", "wan-host", []string{"198.51.100.1/24", "2001:db8:2::1/64"}, []string{"198.51.100.2/24", "2001:db8:2::2/64"}, runtimeWANMAC)
	defer wan.namespace.Close()
	addRuntimeDefault(t, "enatt0", "198.51.100.2")
	addRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	addRuntimeRoute(t, gateway, lan.namespace, "198.51.100.0/24", "192.0.2.1")
	addRuntimeRoute(t, gateway, lan.namespace, "2001:db8:2::/64", "2001:db8:b01::1")
	addRuntimeRoute(t, gateway, wan.namespace, "192.0.2.0/29", "198.51.100.1")
	addRuntimeRoute(t, gateway, wan.namespace, "2001:db8:b01:fe::/64", "2001:db8:2::1")
	waitRuntimeForwarding(t, first, 10*time.Second)
	assertRuntimeTCP(t, wan.namespace, lan.namespace, "tcp4", "198.51.100.2:3001", "192.0.2.2:0", true)
	setRuntimeNamespace(t, gateway)
	assertRuntimeTCP(t, wan.namespace, lan.namespace, "tcp6", "[2001:db8:2::2]:3002", "[2001:db8:b01:fe::2]:0", true)
	setRuntimeNamespace(t, gateway)
	assertRuntimeOwnedReturnRouteRepair(t, first, gateway, wan.namespace, lan.namespace)
	setRuntimeNamespace(t, gateway)
	assertRuntimeOwnedPolicyRuleRepair(t, first, gateway, wan.namespace, lan.namespace)

	setRuntimeNamespace(t, gateway)
	wanLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	oldWANIndex := wanLink.Attrs().Index
	waitRuntimeOwnedDefaults(t, first, oldWANIndex, time.Now().Add(10*time.Second))
	daemonPID := first.command.Process.Pid
	setRuntimeNamespace(t, wan.namespace)
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
	setRuntimeNamespace(t, gateway)
	deletionLogStart := len(runtimeDaemonLog(t, first))
	if err := netlink.LinkDel(wanLink); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDialFails(t, gateway, lan.namespace, "tcp4", "198.51.100.2:3012", "192.0.2.2:0")
	assertRuntimeDialFails(t, gateway, lan.namespace, "tcp6", "[2001:db8:2::2]:3013", "[2001:db8:b01:fe::2]:0")
	deletionDeadline := time.Now().Add(15 * time.Second)
	waitRuntimeMonitorResync(t, first, deletionLogStart, deletionDeadline)
	waitRuntimeOwnedDefaultsAbsent(t, first, oldWANIndex, deletionDeadline)
	logStart := len(runtimeDaemonLog(t, first))
	wan = newRuntimePeer(t, gateway, "enatt0", "wan-new", []string{"198.51.100.1/24", "2001:db8:2::1/64"}, []string{"198.51.100.2/24", "2001:db8:2::2/64"}, runtimeWANMAC)
	defer wan.namespace.Close()
	replacementLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if replacementLink.Attrs().Index == oldWANIndex {
		t.Fatalf("recreated enatt0 reused index %d", oldWANIndex)
	}
	addRuntimeDefault(t, "enatt0", "198.51.100.2")
	addRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	addRuntimeRoute(t, gateway, wan.namespace, "192.0.2.0/29", "198.51.100.1")
	addRuntimeRoute(t, gateway, wan.namespace, "2001:db8:b01:fe::/64", "2001:db8:2::1")
	recoveryDeadline := time.Now().Add(15 * time.Second)
	waitRuntimeMonitorResync(t, first, logStart, recoveryDeadline)
	waitRuntimeOwnedDefaults(t, first, replacementLink.Attrs().Index, recoveryDeadline)
	waitRuntimeTCP(t, first, gateway, wan.namespace, lan.namespace, "tcp4", "198.51.100.2:3014", "192.0.2.2:0", recoveryDeadline)
	waitRuntimeTCP(t, first, gateway, wan.namespace, lan.namespace, "tcp6", "[2001:db8:2::2]:3015", "[2001:db8:b01:fe::2]:0", recoveryDeadline)
	assertRuntimeDaemonRunning(t, first)
	if first.command.Process.Pid != daemonPID {
		t.Fatalf("WAN daemon PID changed from %d to %d", daemonPID, first.command.Process.Pid)
	}

	runRuntimeNFT(t, "flush", "ruleset")
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	waitRuntimeTable(t, first, "ip", "nat", 10*time.Second)
	waitRuntimeTable(t, first, "inet", "mangle", 10*time.Second)
	assertRuntimeTCP(t, gateway, management.namespace, "tcp4", "203.0.113.1:2222", "203.0.113.2:0", false)
	assertRuntimeTCP(t, wan.namespace, lan.namespace, "tcp4", "198.51.100.2:3003", "192.0.2.2:0", true)
	assertRuntimeTCP(t, wan.namespace, lan.namespace, "tcp6", "[2001:db8:2::2]:3004", "[2001:db8:b01:fe::2]:0", true)
	setRuntimeNamespace(t, gateway)
	stopRuntimeDaemon(t, first)
	assertRuntimeTable(t, "inet", "filter")
	assertRuntimeTable(t, "ip", "nat")
	assertRuntimeTable(t, "inet", "mangle")
	runRuntimeNFT(t, "flush", "ruleset")

	second := startRuntimeDaemon(t, binary, configPath, root, "second")
	defer stopRuntimeDaemon(t, second)
	waitRuntimeTable(t, second, "inet", "filter", 10*time.Second)
	waitRuntimeTable(t, second, "ip", "nat", 10*time.Second)
	waitRuntimeTable(t, second, "inet", "mangle", 10*time.Second)
	assertRuntimeTCP(t, gateway, management.namespace, "tcp4", "203.0.113.1:2222", "203.0.113.2:0", false)
	assertRuntimeTCP(t, wan.namespace, lan.namespace, "tcp4", "198.51.100.2:3005", "192.0.2.2:0", true)
	assertRuntimeTCP(t, wan.namespace, lan.namespace, "tcp6", "[2001:db8:2::2]:3006", "[2001:db8:b01:fe::2]:0", true)
	assertRuntimeNoReconcileLoop(t, second)
	assertRuntimeDaemonRunning(t, second)
}

func assertRuntimeOwnedReturnRouteRepair(t *testing.T, daemon *runtimeDaemon, gateway, wan, lan netns.NsHandle) {
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
		LinkIndex: internal.Attrs().Index, Dst: foreignDestination,
		Table: 150, Protocol: unix.RTPROT_STATIC, Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(foreign); err != nil {
		t.Fatal(err)
	}
	var owned *netlink.Route
	var routes []netlink.Route
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && owned == nil {
		var err error
		routes, err = netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: 100}, netlink.RT_FILTER_TABLE)
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
	assertRuntimeTCP(t, wan, lan, "tcp4", "198.51.100.2:3009", "192.0.2.2:0", false)
	setRuntimeNamespace(t, gateway)
	deletedAt := time.Now()
	deadline = deletedAt.Add(5 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: 100}, netlink.RT_FILTER_TABLE)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() == "192.0.2.0/29" {
				foreignRoutes, err := netlink.RouteListFiltered(unix.AF_INET,
					&netlink.Route{Table: 150}, netlink.RT_FILTER_TABLE)
				if err != nil || len(foreignRoutes) != 1 {
					t.Fatalf("unrelated route changed during repair: %v, %v", foreignRoutes, err)
				}
				t.Logf("owned route restored after %s", time.Since(deletedAt))
				assertRuntimeTCP(t, wan, lan, "tcp4", "198.51.100.2:3010", "192.0.2.2:0", true)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("owned return route was not restored before the 1h periodic reconcile; daemon log: %s", runtimeLogTail(t, daemon, 30))
}

func assertRuntimeOwnedPolicyRuleRepair(t *testing.T, daemon *runtimeDaemon, gateway, wan, lan netns.NsHandle) {
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
				assertRuntimeTCP(t, wan, lan, "tcp4", "198.51.100.2:3011", "192.0.2.2:0", true)
				setRuntimeNamespace(t, gateway)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("owned policy rule was not restored before the 1h periodic reconcile; daemon log: %s", runtimeLogTail(t, daemon, 30))
}

func runtimeLogTail(t *testing.T, daemon *runtimeDaemon, count int) string {
	t.Helper()
	lines := strings.Split(runtimeDaemonLog(t, daemon), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

func assertRuntimeDialFails(t *testing.T, gateway, sourceNamespace netns.NsHandle, network, destination, source string) {
	t.Helper()
	setRuntimeNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	connection, err := dialer.Dial(network, destination)
	if connection != nil {
		connection.Close()
	}
	setRuntimeNamespace(t, gateway)
	if err == nil {
		t.Fatalf("outbound %s TCP connection succeeded after enatt0 deletion", network)
	}
}

func waitRuntimeMonitorResync(t *testing.T, daemon *runtimeDaemon, logStart int, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		log := runtimeDaemonLog(t, daemon)
		if len(log) >= logStart {
			for _, line := range strings.Split(log[logStart:], "\n") {
				if strings.Contains(line, `"msg":"ifmgr: reconcile requested"`) &&
					strings.Contains(line, `"reason":"interface monitor resubscribed"`) {
					return
				}
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("WAN daemon did not request reconcile after interface resync: %s", runtimeLogTail(t, daemon, 30))
}

func waitRuntimeOwnedDefaults(t *testing.T, daemon *runtimeDaemon, linkIndex int, deadline time.Time) {
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
			routes, err := netlink.RouteListFiltered(family.value,
				&netlink.Route{Table: 100}, netlink.RT_FILTER_TABLE)
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
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("WAN daemon did not restore table 100 defaults on index %d: %s",
		linkIndex, runtimeLogTail(t, daemon, 30))
}

func waitRuntimeOwnedDefaultsAbsent(t *testing.T, daemon *runtimeDaemon, linkIndex int, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		found := false
		for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
			routes, err := netlink.RouteListFiltered(family,
				&netlink.Route{Table: 100}, netlink.RT_FILTER_TABLE)
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
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("table 100 defaults still reference deleted enatt0 index %d: %s",
		linkIndex, runtimeLogTail(t, daemon, 30))
}

func waitRuntimeTCP(t *testing.T, daemon *runtimeDaemon, gateway, destinationNamespace, sourceNamespace netns.NsHandle, network, destination, source string, deadline time.Time) {
	t.Helper()
	setRuntimeNamespace(t, destinationNamespace)
	listener, err := net.Listen(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		connection, dialErr := dialer.Dial(network, destination)
		if dialErr == nil {
			connection.Close()
			setRuntimeNamespace(t, gateway)
			return
		}
		setRuntimeNamespace(t, gateway)
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(100 * time.Millisecond)
		setRuntimeNamespace(t, sourceNamespace)
	}
	setRuntimeNamespace(t, gateway)
	t.Fatalf("WAN daemon did not restore %s TCP within 15s: %s", network, runtimeLogTail(t, daemon, 30))
}

type runtimeDaemon struct {
	command *exec.Cmd
	logPath string
	stopped bool
}

func assertRuntimeDaemonRunning(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(daemon.command.Process.Pid, &status, syscall.WNOHANG, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 0 {
		daemon.stopped = true
		t.Fatalf("WAN daemon exited before the packet test ended: %s", runtimeDaemonLog(t, daemon))
	}
}

func startRuntimeDaemon(t *testing.T, binary, configPath, root, name string) *runtimeDaemon {
	t.Helper()
	logPath := filepath.Join(root, name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "ifmgr", "--role", "wan", "--debug")
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	logFile.Close()
	return &runtimeDaemon{command: command, logPath: logPath}
}

func stopRuntimeDaemon(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	if daemon.stopped {
		return
	}
	daemon.stopped = true
	if err := daemon.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("The WAN daemon failed to stop: %v: %s", err, runtimeDaemonLog(t, daemon))
		}
	case <-time.After(5 * time.Second):
		daemon.command.Process.Kill()
		<-done
		t.Fatalf("WAN daemon did not stop: %s", runtimeDaemonLog(t, daemon))
	}
}

func runtimeDaemonLog(t *testing.T, daemon *runtimeDaemon) string {
	t.Helper()
	content, err := os.ReadFile(daemon.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func waitRuntimeTable(t *testing.T, daemon *runtimeDaemon, family, table string, timeout time.Duration) {
	t.Helper()
	gateway, err := netns.GetFromPid(daemon.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		setRuntimeNamespace(t, gateway)
		if output, err := exec.Command("nft", "list", "table", family, table).CombinedOutput(); err == nil && len(output) > 0 {
			return
		}
		if daemon.command.ProcessState != nil {
			t.Fatalf("WAN daemon exited before %s %s appeared: %s", family, table, runtimeDaemonLog(t, daemon))
		}
		time.Sleep(50 * time.Millisecond)
	}
	rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
	t.Fatalf("WAN daemon did not install %s %s: ruleset=%s daemon=%s", family, table, rules, runtimeDaemonLog(t, daemon))
}

func assertRuntimeTable(t *testing.T, family, table string) {
	t.Helper()
	if output, err := exec.Command("nft", "list", "table", family, table).CombinedOutput(); err != nil {
		t.Fatalf("%s %s is missing after the daemon stopped: %v: %s", family, table, err, output)
	}
}

func assertRuntimeNoReconcileLoop(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	first := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		first = countRuntimeFirewallReconciles(runtimeDaemonLog(t, daemon))
		if first > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if first == 0 {
		t.Fatalf("WAN daemon never reconciled the firewall: %s", runtimeDaemonLog(t, daemon))
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		second := countRuntimeFirewallReconciles(runtimeDaemonLog(t, daemon))
		if second != first {
			t.Fatalf("WAN daemon reconciled without a timer or external change: %s", runtimeDaemonLog(t, daemon))
		}
	}
}

func countRuntimeFirewallReconciles(log string) int {
	count := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"msg":"ifmgr: Reconcile"`) && strings.Contains(line, `"module":"firewall"`) {
			count++
		}
	}
	return count
}

func waitRuntimeForwarding(t *testing.T, daemon *runtimeDaemon, timeout time.Duration) {
	t.Helper()
	gateway, err := netns.GetFromPid(daemon.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		setRuntimeNamespace(t, gateway)
		output, err := exec.Command("nft", "list", "chain", "inet", "mwan_steer", "forward").CombinedOutput()
		if err == nil && !strings.Contains(string(output), `oifname "enatt0" meta nfproto ipv4 drop`) && !strings.Contains(string(output), `oifname "enatt0" meta nfproto ipv6 drop`) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	lines := strings.Split(runtimeDaemonLog(t, daemon), "\n")
	relevant := make([]string, 0)
	for _, line := range lines {
		if strings.Contains(line, `"module":"wan.routes"`) || strings.Contains(line, `"module":"steering"`) || strings.Contains(line, `"module":"health"`) {
			relevant = append(relevant, line)
		}
	}
	if len(relevant) > 25 {
		relevant = relevant[len(relevant)-25:]
	}
	t.Fatalf("WAN daemon did not permit native forwarding: %s", strings.Join(relevant, "\n"))
}

func runRuntimeNFT(t *testing.T, arguments ...string) {
	t.Helper()
	if output, err := exec.Command("nft", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("The nft command %v failed: %v: %s", arguments, err, output)
	}
}

func newRuntimePeer(t *testing.T, gateway netns.NsHandle, gatewayName, peerName string, gatewayAddresses, peerAddresses []string, gatewayMAC string) runtimePeer {
	t.Helper()
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	setRuntimeNamespace(t, gateway)
	attributes := netlink.NewLinkAttrs()
	attributes.Name = gatewayName
	if gatewayMAC != "" {
		hardware, err := net.ParseMAC(gatewayMAC)
		if err != nil {
			t.Fatal(err)
		}
		links, err := netlink.LinkList()
		if err != nil {
			t.Fatal(err)
		}
		for _, link := range links {
			if bytes.Equal(link.Attrs().HardwareAddr, hardware) {
				t.Fatalf("gateway MAC %s already belongs to %s", gatewayMAC, link.Attrs().Name)
			}
		}
		attributes.HardwareAddr = hardware
	}
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: attributes, PeerName: peerName}); err != nil {
		t.Fatal(err)
	}
	peerLink, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(peerLink, int(peerNamespace)); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, gatewayName, gatewayAddresses)
	setRuntimeNamespace(t, peerNamespace)
	configureRuntimeLink(t, peerName, peerAddresses)
	setRuntimeNamespace(t, gateway)
	return runtimePeer{namespace: peerNamespace}
}

func setRuntimeNamespace(t *testing.T, namespace netns.NsHandle) {
	t.Helper()
	if err := netns.Set(namespace); err != nil {
		t.Fatal(err)
	}
}

func setRuntimeLoopback(t *testing.T) {
	t.Helper()
	link, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func configureRuntimeLink(t *testing.T, name string, addresses []string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		parsed, err := netlink.ParseAddr(address)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.IP.To4() == nil {
			parsed.Flags = unix.IFA_F_NODAD
		}
		if err := netlink.AddrAdd(link, parsed); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func addRuntimeRoute(t *testing.T, gateway, namespace netns.NsHandle, destination, nextHop string) {
	t.Helper()
	setRuntimeNamespace(t, namespace)
	_, network, err := net.ParseCIDR(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{Dst: network, Gw: net.ParseIP(nextHop)}); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
}

func addRuntimeDefault(t *testing.T, interfaceName, nextHop string) {
	t.Helper()
	link, err := netlink.LinkByName(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(nextHop)}); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeTCP(t *testing.T, destinationNamespace, sourceNamespace netns.NsHandle, network, destination, source string, accepted bool) {
	t.Helper()
	setRuntimeNamespace(t, destinationNamespace)
	listener, err := net.Listen(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	connection, err := dialer.Dial(network, destination)
	if connection != nil {
		connection.Close()
	}
	if accepted && err != nil {
		setRuntimeNamespace(t, destinationNamespace)
		rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
		t.Fatalf("A connection from %s to %s failed: %v\n%s", source, destination, err, rules)
	}
	if !accepted && err == nil {
		t.Fatalf("A connection from %s to %s succeeded", source, destination)
	}
	setRuntimeNamespace(t, destinationNamespace)
}
