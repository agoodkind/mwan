//go:build linux && (firewall || protocol || systemd)

package testenv

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

const RuntimeWANMAC = "02:00:5e:00:53:aa"

type RuntimePeer struct {
	Namespace netns.NsHandle
}

func RuntimeLogTail(t *testing.T, daemon *RuntimeDaemon, count int) string {
	t.Helper()
	lines := strings.Split(RuntimeDaemonLog(t, daemon), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

func WaitRuntimeTCP(
	t *testing.T,
	daemon *RuntimeDaemon,
	gateway netns.NsHandle,
	destinationNamespace netns.NsHandle,
	sourceNamespace netns.NsHandle,
	network string,
	destination string,
	source string,
	deadline time.Time,
) {
	t.Helper()
	SetRuntimeNamespace(t, destinationNamespace)
	listener, err := net.Listen(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	SetRuntimeNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		connection, dialErr := dialer.Dial(network, destination)
		if dialErr == nil {
			connection.Close()
			SetRuntimeNamespace(t, gateway)
			return
		}
		SetRuntimeNamespace(t, gateway)
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(100 * time.Millisecond)
		SetRuntimeNamespace(t, sourceNamespace)
	}
	SetRuntimeNamespace(t, gateway)
	t.Fatalf(
		"WAN daemon did not restore %s TCP within 15s: %s",
		network,
		RuntimeLogTail(t, daemon, 30),
	)
}

type RuntimeDaemon struct {
	Command *exec.Cmd
	LogPath string
	Done    <-chan error
	Stopped bool
}

func AssertRuntimeDaemonRunning(t *testing.T, daemon *RuntimeDaemon) {
	t.Helper()
	select {
	case err := <-daemon.Done:
		daemon.Stopped = true
		t.Fatalf("WAN daemon exited during the test: %v: %s", err, RuntimeDaemonLog(t, daemon))
	default:
	}
}

func StartRuntimeDaemon(t *testing.T, binary, configPath, root, name string) *RuntimeDaemon {
	t.Helper()
	return StartRuntimeDaemonForRole(t, binary, configPath, filepath.Join(root, name+".log"), "wan")
}

func StartRoleDHCPv4RuntimeDaemon(
	t *testing.T,
	binary string,
	configPath string,
	root string,
	role string,
) *RuntimeDaemon {
	t.Helper()
	return StartRuntimeDaemonForRole(t, binary, configPath, filepath.Join(root, role+".log"), role)
}

func StartRuntimeDaemonForRole(
	t *testing.T,
	binary string,
	configPath string,
	logPath string,
	role string,
) *RuntimeDaemon {
	t.Helper()
	return StartRuntimeDaemonProcess(
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

func StartRuntimeDaemonProcess(
	t *testing.T,
	binary string,
	logPath string,
	environment []string,
	arguments ...string,
) *RuntimeDaemon {
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
	daemon := &RuntimeDaemon{Command: command, LogPath: logPath, Done: done}
	t.Cleanup(func() { KillOwnedRuntimeDaemon(t, daemon) })
	return daemon
}

func StopRuntimeDaemon(t *testing.T, daemon *RuntimeDaemon) {
	t.Helper()
	if daemon.Stopped {
		return
	}
	daemon.Stopped = true
	if err := daemon.Command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("stop WAN daemon: %v", err)
	}
	select {
	case err := <-daemon.Done:
		if err != nil {
			t.Fatalf("WAN daemon failed to stop: %v: %s", err, RuntimeDaemonLog(t, daemon))
		}
	case <-time.After(5 * time.Second):
		if err := daemon.Command.Process.Kill(); err != nil {
			t.Errorf("kill WAN daemon: %v", err)
		}
		select {
		case <-daemon.Done:
		case <-time.After(2 * time.Second):
			t.Error("wait for WAN daemon after SIGKILL: deadline exceeded")
		}
		t.Fatalf("WAN daemon did not stop: %s", RuntimeDaemonLog(t, daemon))
	}
}

func RuntimeDaemonLog(t *testing.T, daemon *RuntimeDaemon) string {
	t.Helper()
	content, err := os.ReadFile(daemon.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func WaitRuntimeTable(
	t *testing.T,
	daemon *RuntimeDaemon,
	family string,
	table string,
	timeout time.Duration,
) {
	t.Helper()
	gateway, err := netns.GetFromPid(daemon.Command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	SetRuntimeNamespace(t, gateway)
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
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
	t.Fatalf(
		"WAN daemon did not install %s %s: ruleset=%s daemon=%s",
		family,
		table,
		rules,
		RuntimeDaemonLog(t, daemon),
	)
}

func AssertRuntimeTable(t *testing.T, family, table string) {
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

func WaitRuntimeForwarding(t *testing.T, daemon *RuntimeDaemon, timeout time.Duration) {
	t.Helper()
	gateway, err := netns.GetFromPid(daemon.Command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		SetRuntimeNamespace(t, gateway)
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
	lines := strings.Split(RuntimeDaemonLog(t, daemon), "\n")
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

func RunRuntimeNFT(t *testing.T, arguments ...string) {
	t.Helper()
	if output, err := exec.Command("nft", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("The nft command %v failed: %v: %s", arguments, err, output)
	}
}

func NewRuntimePeer(
	t *testing.T,
	gateway netns.NsHandle,
	gatewayName string,
	peerName string,
	gatewayAddresses []string,
	peerAddresses []string,
	gatewayMAC string,
) RuntimePeer {
	t.Helper()
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	SetRuntimeLoopback(t)
	SetRuntimeNamespace(t, gateway)
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
	ConfigureRuntimeLink(t, gatewayName, gatewayAddresses)
	SetRuntimeNamespace(t, peerNamespace)
	ConfigureRuntimeLink(t, peerName, peerAddresses)
	SetRuntimeNamespace(t, gateway)
	return RuntimePeer{Namespace: peerNamespace}
}

func SetRuntimeNamespace(t *testing.T, namespace netns.NsHandle) {
	t.Helper()
	if err := netns.Set(namespace); err != nil {
		t.Fatal(err)
	}
}

func SetRuntimeLoopback(t *testing.T) {
	t.Helper()
	link, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func ConfigureRuntimeLink(t *testing.T, name string, addresses []string) {
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

func AddRuntimeRoute(t *testing.T, gateway, namespace netns.NsHandle, destination, nextHop string) {
	t.Helper()
	SetRuntimeNamespace(t, namespace)
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
	SetRuntimeNamespace(t, gateway)
}

func AddRuntimeDefault(t *testing.T, interfaceName, nextHop string) {
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

func AssertRuntimeTCP(
	t *testing.T,
	destinationNamespace netns.NsHandle,
	sourceNamespace netns.NsHandle,
	network string,
	destination string,
	source string,
	accepted bool,
) {
	t.Helper()
	SetRuntimeNamespace(t, destinationNamespace)
	listener, err := net.Listen(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	SetRuntimeNamespace(t, sourceNamespace)
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
		SetRuntimeNamespace(t, destinationNamespace)
		rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
		t.Fatalf("A connection from %s to %s failed: %v\n%s", source, destination, err, rules)
	}
	if !accepted && err == nil {
		t.Fatalf("A connection from %s to %s succeeded", source, destination)
	}
	SetRuntimeNamespace(t, destinationNamespace)
}

func WaitRuntimeOwnershipRead(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	expected string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(read(), expected) {
			return
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"operational read lacks %s: %s; daemon: %s",
		expected,
		read(),
		RuntimeLogTail(t, daemon, 30),
	)
}

func BindStartupDirectory(t *testing.T, source string, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind %s to %s: %v", source, target, err)
	}
}

func WaitAutoconfigurationLinkLocal(t *testing.T, name string) {
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
