//go:build linux && (firewall || protocol || firewallnetns || systemd)

package commandline

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const runtimeWANMAC = "02:00:5e:00:53:aa"

type runtimePeer struct {
	namespace netns.NsHandle
}

func runtimeLogTail(t *testing.T, daemon *runtimeDaemon, count int) string {
	t.Helper()
	lines := strings.Split(runtimeDaemonLog(t, daemon), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

func waitRuntimeTCP(
	t *testing.T,
	daemon *runtimeDaemon,
	gateway netns.NsHandle,
	destinationNamespace netns.NsHandle,
	sourceNamespace netns.NsHandle,
	network string,
	destination string,
	source string,
	deadline time.Time,
) {
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
	t.Fatalf(
		"WAN daemon did not restore %s TCP within 15s: %s",
		network,
		runtimeLogTail(t, daemon, 30),
	)
}

type runtimeDaemon struct {
	command *exec.Cmd
	logPath string
	done    <-chan error
	stopped bool
}

func assertRuntimeDaemonRunning(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	select {
	case err := <-daemon.done:
		daemon.stopped = true
		t.Fatalf("WAN daemon exited during the test: %v: %s", err, runtimeDaemonLog(t, daemon))
	default:
	}
}

func startRuntimeDaemon(t *testing.T, binary, configPath, root, name string) *runtimeDaemon {
	t.Helper()
	return startRuntimeDaemonForRole(t, binary, configPath, filepath.Join(root, name+".log"), "wan")
}

func startRoleDHCPv4RuntimeDaemon(
	t *testing.T,
	binary string,
	configPath string,
	root string,
	role string,
) *runtimeDaemon {
	t.Helper()
	return startRuntimeDaemonForRole(t, binary, configPath, filepath.Join(root, role+".log"), role)
}

func startRuntimeDaemonForRole(
	t *testing.T,
	binary string,
	configPath string,
	logPath string,
	role string,
) *runtimeDaemon {
	t.Helper()
	return startRuntimeDaemonProcess(
		t,
		binary,
		logPath,
		[]string{"MWAN_CONFIG=" + configPath},
		"ifmgr",
		"--role",
		role,
		"--debug",
	)
}

func startRuntimeDaemonProcess(
	t *testing.T,
	binary string,
	logPath string,
	environment []string,
	arguments ...string,
) *runtimeDaemon {
	t.Helper()
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	// Cleanup can stop the daemon after the test context ends.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = append(os.Environ(), environment...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer cancel()
		done <- command.Wait()
	}()
	daemon := &runtimeDaemon{command: command, logPath: logPath, done: done}
	t.Cleanup(func() { killOwnedRuntimeDaemon(t, daemon) })
	return daemon
}

func stopRuntimeDaemon(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	if daemon.stopped {
		return
	}
	daemon.stopped = true
	if err := daemon.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("stop WAN daemon: %v", err)
	}
	select {
	case err := <-daemon.done:
		if err != nil {
			t.Fatalf("WAN daemon failed to stop: %v: %s", err, runtimeDaemonLog(t, daemon))
		}
	case <-time.After(5 * time.Second):
		if err := daemon.command.Process.Kill(); err != nil {
			t.Errorf("kill WAN daemon: %v", err)
		}
		select {
		case <-daemon.done:
		case <-time.After(2 * time.Second):
			t.Error("wait for WAN daemon after SIGKILL: deadline exceeded")
		}
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

func waitRuntimeTable(
	t *testing.T,
	daemon *runtimeDaemon,
	family string,
	table string,
	timeout time.Duration,
) {
	t.Helper()
	gateway, err := netns.GetFromPid(daemon.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	setRuntimeNamespace(t, gateway)
	var tableFamily nftables.TableFamily
	switch family {
	case "inet":
		tableFamily = nftables.TableFamilyINet
	case "ip":
		tableFamily = nftables.TableFamilyIPv4
	case "ip6":
		tableFamily = nftables.TableFamilyIPv6
	default:
		t.Fatalf("unsupported nftables family %q", family)
	}
	conn, err := nftables.New(nftables.WithNetNSFd(int(gateway)))
	if err != nil {
		t.Fatalf("open nftables connection: %v", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := conn.ListTableOfFamily(table, tableFamily); err == nil {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
	t.Fatalf(
		"WAN daemon did not install %s %s: ruleset=%s daemon=%s",
		family,
		table,
		rules,
		runtimeDaemonLog(t, daemon),
	)
}

func assertRuntimeTable(t *testing.T, family, table string) {
	t.Helper()
	if output, err := exec.Command(
		"nft",
		"list",
		"table",
		family,
		table,
	).CombinedOutput(); err != nil {
		t.Fatalf("%s %s is missing after the daemon stopped: %v: %s", family, table, err, output)
	}
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
		output, err := exec.Command(
			"nft",
			"list",
			"chain",
			"inet",
			"mwan_steer",
			"forward",
		).CombinedOutput()
		if err == nil &&
			!strings.Contains(string(output), `oifname "enatt0" meta nfproto ipv4 drop`) &&
			!strings.Contains(string(output), `oifname "enatt0" meta nfproto ipv6 drop`) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	lines := strings.Split(runtimeDaemonLog(t, daemon), "\n")
	relevant := make([]string, 0)
	for _, line := range lines {
		if strings.Contains(line, `"module":"wan.routes"`) ||
			strings.Contains(line, `"module":"steering"`) ||
			strings.Contains(line, `"module":"health"`) {
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

func newRuntimePeer(
	t *testing.T,
	gateway netns.NsHandle,
	gatewayName string,
	peerName string,
	gatewayAddresses []string,
	peerAddresses []string,
	gatewayMAC string,
) runtimePeer {
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
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: attributes,
		PeerName:  peerName,
	}); err != nil {
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
	if err := netlink.RouteAdd(&netlink.Route{
		Dst: network,
		Gw:  net.ParseIP(nextHop),
	}); err != nil {
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
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(nextHop),
	}); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeTCP(
	t *testing.T,
	destinationNamespace netns.NsHandle,
	sourceNamespace netns.NsHandle,
	network string,
	destination string,
	source string,
	accepted bool,
) {
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

func waitRuntimeOwnershipRead(
	t *testing.T,
	daemon *runtimeDaemon,
	read func() string,
	expected string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(read(), expected) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"operational read lacks %s: %s; daemon: %s",
		expected,
		read(),
		runtimeLogTail(t, daemon, 30),
	)
}

func bindStartupDirectory(t *testing.T, source string, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind %s to %s: %v", source, target, err)
	}
}

func waitAutoconfigurationLinkLocal(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			if address.IP.IsLinkLocalUnicast() && address.Flags&unix.IFA_F_TENTATIVE == 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s link-local DAD did not complete", name)
}
