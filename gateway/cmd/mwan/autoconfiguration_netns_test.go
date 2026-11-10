//go:build linux && firewallnetns

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	raRuntimeChildEnv  = "MWAN_AUTOCONFIGURATION_TEST_CHILD"
	raRuntimeBinaryEnv = "MWAN_AUTOCONFIGURATION_TEST_BINARY"
)

func TestAutoconfigurationDaemonRuntime(t *testing.T) {
	runAutoconfigurationTest(t, func(t *testing.T) { runAutoconfigurationDaemonRuntime(t, false) })
}

func TestRadvdAutoconfigurationDaemonRuntime(t *testing.T) {
	runAutoconfigurationTest(t, func(t *testing.T) { runAutoconfigurationDaemonRuntime(t, true) })
}

func TestAutoconfigurationSolicitsRouterOnFreshStart(t *testing.T) {
	runAutoconfigurationTest(t, runAutoconfigurationSolicitationRuntime)
}

func runAutoconfigurationTest(t *testing.T, run func(*testing.T)) {
	t.Helper()
	if os.Getenv(raRuntimeChildEnv) == "1" {
		run(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), raRuntimeChildEnv+"=1", raRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated autoconfiguration daemon: %v: %s", err, output)
	}
}

// autoconfigurationRuntime is the gateway namespace and the VLAN 397 router
// peer that the autoconfiguration daemon tests share.
type autoconfigurationRuntime struct {
	gateway    netns.NsHandle
	peer       runtimePeer
	root       string
	configPath string
}

// buildAutoconfigurationRuntime requires the caller to lock the OS thread,
// because every namespace switch applies to the current thread.
func buildAutoconfigurationRuntime(t *testing.T) autoconfigurationRuntime {
	t.Helper()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	t.Cleanup(func() { _ = management.namespace.Close() })
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	t.Cleanup(func() { _ = lan.namespace.Close() })
	peer := newRuntimePeer(t, gateway, "ownphys0", "owned-peer", nil, nil, ownedRuntimeParentMAC)
	t.Cleanup(func() { _ = peer.namespace.Close() })
	setRuntimeNamespace(t, peer.namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index}, VlanId: 397}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "peer397", []string{"10.39.7.2/24", "2001:db8:517::1/64", "2001:db8:518::1/128"})
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOwnedRuntimeNetwork(t, networkDir, false, true)
	writeAutoconfigurationRuntimeNetwork(t, networkDir)
	return autoconfigurationRuntime{gateway: gateway, peer: peer, root: root, configPath: configPath}
}

func runAutoconfigurationDaemonRuntime(t *testing.T, useRadvd bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	topology := buildAutoconfigurationRuntime(t)
	gateway, peer, root := topology.gateway, topology.peer, topology.root
	daemon := startRuntimeDaemon(t, os.Getenv(raRuntimeBinaryEnv), topology.configPath, root, "ra-runtime")
	defer killOwnedRuntimeDaemon(t, daemon)
	link := waitOwnedRuntimeLink(t, daemon, "owned397", 10*time.Second)
	waitAutoconfigurationSysctls(t, daemon, "owned397")
	waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
	waitAutoconfigurationLinkLocal(t, "owned397")
	assertStaticRuntimePacket(t, gateway, peer.namespace, "udp4", "10.39.7.2:51701")
	var observation *ndp.Conn
	if useRadvd {
		device, err := net.InterfaceByName("owned397")
		if err != nil {
			t.Fatal(err)
		}
		observation, _, err = ndp.Listen(device, ndp.LinkLocal)
		if err != nil {
			t.Fatal(err)
		}
		defer observation.Close()
	}
	setRuntimeNamespace(t, peer.namespace)
	waitAutoconfigurationLinkLocal(t, "peer397")
	var advertiser *protocolService
	if useRadvd {
		advertiser = startAutoconfigurationRadvd(t, root)
		defer func() {
			if t.Failed() {
				content, err := os.ReadFile(advertiser.logPath)
				t.Logf("radvd log read=%v:\n%s", err, content)
			}
			if err := advertiser.command.Process.Signal(syscall.SIGCONT); err != nil {
				t.Errorf("resume radvd for cleanup: %v", err)
			}
			stopProtocolServices(t, []protocolService{*advertiser})
		}()
	} else {
		device, err := net.InterfaceByName("peer397")
		if err != nil {
			t.Fatal(err)
		}
		conn, _, err := ndp.Listen(device, ndp.LinkLocal)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ra := &ndp.RouterAdvertisement{
			RouterLifetime: 4 * time.Second,
			Options: []ndp.Option{&ndp.PrefixInformation{
				PrefixLength: 64, Prefix: netip.MustParseAddr("2001:db8:517::"),
				OnLink: true, AutonomousAddressConfiguration: true,
				PreferredLifetime: 2 * time.Second, ValidLifetime: 8 * time.Second,
			}},
		}
		if err := conn.WriteTo(ra, nil, netip.MustParseAddr("ff02::1")); err != nil {
			t.Fatal(err)
		}
	}
	setRuntimeNamespace(t, gateway)
	waitAutoconfigurationState(t, daemon, link, true, true, false)
	assertStaticRuntimePacket(t, gateway, peer.namespace, "udp6", "[2001:db8:517::1]:51702")
	assertStaticRuntimePacket(t, gateway, peer.namespace, "udp6", "[2001:db8:518::1]:51703")
	defaultGateway, err := netif.IfaceDefaultGateway("inet6", "owned397")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index, Gw: net.ParseIP(defaultGateway),
		Family: netlink.FAMILY_V6, Table: 778, Priority: 123, Protocol: unix.RTPROT_STATIC,
	}); err != nil {
		t.Fatal(err)
	}
	if advertiser != nil {
		childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", advertiser.command.Process.Pid, advertiser.command.Process.Pid)
		children, err := os.ReadFile(childrenPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("radvd pid=%d children=%s", advertiser.command.Process.Pid, children)
		if err := observation.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			message, _, source, err := observation.ReadFrom()
			if err != nil {
				t.Fatal(err)
			}
			advertisement, ok := message.(*ndp.RouterAdvertisement)
			if !ok {
				continue
			}
			t.Logf("radvd advertisement from %s: %#v", source, advertisement)
			for _, option := range advertisement.Options {
				if prefix, ok := option.(*ndp.PrefixInformation); ok {
					t.Logf("radvd prefix: %#v", prefix)
				}
			}
			break
		}
		// Freeze the router to test natural expiry without a final withdrawal RA.
		if err := advertiser.command.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
	}
	waitAutoconfigurationState(t, daemon, link, true, false, true)
	if routes, err := netlink.RouteGet(net.ParseIP("2001:db8:518::1")); !errors.Is(err, unix.ENETUNREACH) {
		t.Fatalf("expired default still selects external destination: routes=%#v err=%v", routes, err)
	}
	assertAutoconfigurationRouteObservations(t, defaultGateway)
	waitAutoconfigurationState(t, daemon, link, false, false, false)
	waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
}

// runAutoconfigurationSolicitationRuntime uses a peer router that never
// advertises unprompted. The gateway learns its IPv6 default route only after
// the kernel sends a Router Solicitation on the freshly created owned link.
func runAutoconfigurationSolicitationRuntime(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	topology := buildAutoconfigurationRuntime(t)
	binary := os.Getenv(raRuntimeBinaryEnv)
	setRuntimeNamespace(t, topology.peer.namespace)
	// A Neighbor Advertisement without the router flag makes the gateway kernel
	// delete every route through that neighbor, including the RA default route.
	// The kernel sets the router flag only on a forwarding link.
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/peer397/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitAutoconfigurationLinkLocal(t, "peer397")
	solicitations, stopResponder := answerAutoconfigurationSolicitations(t, "peer397")
	defer stopResponder()
	setRuntimeNamespace(t, topology.gateway)
	// The kernel decides whether to solicit when duplicate address detection
	// for the link-local address finishes. Without detection, the address is
	// ready when the link comes up and before any later kernel policy pass,
	// which reproduces a link that finishes detection before the policy write.
	for _, scope := range []string{"all", "default"} {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+scope+"/accept_dad", []byte("0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := startRuntimeDaemon(t, binary, topology.configPath, topology.root, "ra-solicit-first")
	defer killOwnedRuntimeDaemon(t, first)
	select {
	case source := <-solicitations:
		t.Logf("Router Solicitation from %s", source)
	case <-time.After(10 * time.Second):
		t.Fatalf("peer397 received no Router Solicitation within 10 s of daemon start: %s", runtimeLogTail(t, first, 40))
	}
	link := waitOwnedRuntimeLink(t, first, "owned397", 10*time.Second)
	if sent := routerSolicitationsSent(t, "owned397"); sent == 0 {
		t.Fatal("Icmp6OutRouterSolicits on owned397 is 0")
	}
	waitAutoconfigurationState(t, first, link, true, true, false)
	killOwnedRuntimeDaemon(t, first)
	second := startRuntimeDaemon(t, binary, topology.configPath, topology.root, "ra-solicit-restart")
	defer killOwnedRuntimeDaemon(t, second)
	restarted := waitOwnedRuntimeLink(t, second, "owned397", 10*time.Second)
	if restarted.Attrs().Index != link.Attrs().Index {
		t.Fatalf("restart recreated owned397: index %d, want %d", restarted.Attrs().Index, link.Attrs().Index)
	}
	if restarted.Attrs().Flags&net.FlagUp == 0 {
		t.Fatal("restart left owned397 down")
	}
	waitAutoconfigurationState(t, second, restarted, true, true, false)
}

// answerAutoconfigurationSolicitations sends a Router Advertisement on name
// only in reply to a Router Solicitation. The returned channel receives the
// source of each solicitation, and the returned function stops the responder.
func answerAutoconfigurationSolicitations(t *testing.T, name string) (<-chan netip.Addr, func()) {
	t.Helper()
	device, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := ndp.Listen(device, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	// The kernel drops multicast for a group that the link has not joined.
	// Router Solicitations use the all-routers group.
	if err := conn.JoinGroup(netip.MustParseAddr("ff02::2")); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	advertisement := &ndp.RouterAdvertisement{
		RouterLifetime: 30 * time.Minute,
		Options: []ndp.Option{&ndp.PrefixInformation{
			PrefixLength: 64, Prefix: netip.MustParseAddr("2001:db8:517::"),
			OnLink: true, AutonomousAddressConfiguration: true,
			PreferredLifetime: 30 * time.Minute, ValidLifetime: time.Hour,
		}},
	}
	solicitations := make(chan netip.Addr, 1)
	done := make(chan error, 1)
	go func() {
		for {
			message, _, source, err := conn.ReadFrom()
			if err != nil {
				done <- nil
				return
			}
			if _, ok := message.(*ndp.RouterSolicitation); !ok {
				continue
			}
			if err := conn.WriteTo(advertisement, nil, netip.MustParseAddr("ff02::1")); err != nil {
				done <- fmt.Errorf("answer Router Solicitation from %s: %w", source, err)
				return
			}
			select {
			case solicitations <- source:
			default:
			}
		}
	}()
	return solicitations, func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close Router Solicitation responder: %v", err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// routerSolicitationsSent reads the counter from the namespace of the current
// thread. The test thread switches namespaces, and /proc/net follows the main
// thread instead.
func routerSolicitationsSent(t *testing.T, name string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc/thread-self/net/dev_snmp6", name))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "Icmp6OutRouterSolicits" {
			continue
		}
		count, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	t.Fatalf("%s statistics lack Icmp6OutRouterSolicits", name)
	return 0
}

func assertAutoconfigurationRouteObservations(t *testing.T, gateway string) {
	t.Helper()
	mainRoutes, err := netif.ListTableRoutes(t.Context(), slog.Default(), "inet6", unix.RT_TABLE_MAIN)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range mainRoutes {
		if route.Protocol == unix.RTPROT_RA && route.Dest == "default" {
			t.Fatalf("inspection includes expired RA default: %#v", route)
		}
	}
	routes, err := netif.ListTableRoutes(t.Context(), slog.Default(), "inet6", 778)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Via != gateway || routes[0].Metric != 123 || routes[0].TableID != 778 {
		t.Fatalf("permanent provider-table default changed: %#v", routes)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	monitor := netif.NewMonitor(ctx, slog.Default(), netif.MonitorConfig{Iface: "owned397"})
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-monitor.Events:
			if !ok {
				t.Fatal("route monitor ended before the initial snapshot")
			}
			if event.Kind != netif.EvResync || event.Snapshot == nil {
				continue
			}
			foundPermanent := false
			for _, route := range event.Snapshot.Routes {
				if route.TableID == unix.RT_TABLE_MAIN && route.Protocol == unix.RTPROT_RA && route.Dest == "default" {
					t.Fatalf("snapshot includes expired RA default: %#v", route)
				}
				if route.TableID == 778 && route.Via == gateway && route.Metric == 123 {
					foundPermanent = true
				}
			}
			if !foundPermanent {
				t.Fatalf("snapshot omitted permanent provider-table default: %#v", event.Snapshot.Routes)
			}
			return
		case <-deadline.C:
			t.Fatal("route monitor did not publish its initial snapshot")
		}
	}
}

func startAutoconfigurationRadvd(t *testing.T, root string) *protocolService {
	t.Helper()
	configPath := filepath.Join(root, "radvd.conf")
	config := "interface peer397 { AdvSendAdvert on; MinRtrAdvInterval 3; MaxRtrAdvInterval 4; AdvDefaultLifetime 4; prefix 2001:db8:517::/64 { AdvOnLink on; AdvAutonomous on; AdvPreferredLifetime 2; AdvValidLifetime 8; }; };\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "radvd.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("radvd", "-n", "-m", "stderr", "-d", "5", "-C", configPath, "-p", filepath.Join(root, "radvd.pid"))
	command.Stdout, command.Stderr = logFile, logFile
	startError := command.Start()
	closeError := logFile.Close()
	if startError != nil {
		t.Fatalf("start radvd: %v", startError)
	}
	if closeError != nil {
		t.Errorf("close radvd log: %v", closeError)
	}
	return &protocolService{name: "radvd", command: command, logPath: logPath}
}

func writeAutoconfigurationRuntimeNetwork(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if string(entry["name"]) != `"owned397"` {
			continue
		}
		entry["enabled"] = json.RawMessage("true")
		entry["ietf-ip:ipv4"] = json.RawMessage(`{"address":[{"ip":"10.39.7.1","prefix-length":24}]}`)
		entry["ietf-ip:ipv6"] = json.RawMessage(`{"forwarding":true,"goodkind-mwan-steering:accept-ra":true,"goodkind-mwan-steering:autoconf":true,"goodkind-mwan-steering:accept-ra-default-route":true,"goodkind-mwan-steering:use-ra-dns":false,"goodkind-mwan-steering:route-metric":4700}`)
	}
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitAutoconfigurationSysctls(t *testing.T, daemon *runtimeDaemon, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	want := map[string]string{"accept_ra": "2", "autoconf": "1", "accept_ra_defrtr": "1", "ra_defrtr_metric": "4700", "forwarding": "1"}
	for time.Now().Before(deadline) {
		matches := true
		for key, expected := range want {
			value, err := os.ReadFile("/proc/sys/net/ipv6/conf/" + name + "/" + key)
			if err != nil || strings.TrimSpace(string(value)) != expected {
				matches = false
				break
			}
		}
		if matches {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("kernel RA policy missing: %s", runtimeLogTail(t, daemon, 40))
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

func waitAutoconfigurationState(t *testing.T, daemon *runtimeDaemon, link netlink.Link, address, route, deprecated bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var lastAddresses []netlink.Addr
	var lastRoutes []netlink.Route
	for time.Now().Before(deadline) {
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		lastAddresses = addresses
		foundAddress, foundDeprecated := false, false
		for _, current := range addresses {
			if strings.HasPrefix(current.IP.String(), "2001:db8:517:") {
				foundAddress = current.Flags&unix.IFA_F_TENTATIVE == 0
				foundDeprecated = current.Flags&unix.IFA_F_DEPRECATED != 0
			}
		}
		routes, err := netlink.RouteListFiltered(unix.AF_INET6, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
		if err != nil {
			t.Fatal(err)
		}
		lastRoutes = routes
		currentDefault, err := netif.FindMainRADefault(t.Context(), link.Attrs().Name)
		if err != nil {
			t.Fatal(err)
		}
		foundRoute := currentDefault != nil && currentDefault.Metric == 4700
		defaultGateway, err := netif.IfaceDefaultGateway("inet6", link.Attrs().Name)
		if err != nil {
			t.Fatal(err)
		}
		if foundAddress == address && foundRoute == route && (defaultGateway != "") == route && (!address || foundDeprecated == deprecated) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	output, err := exec.Command("ip", "-6", "-json", "route", "show", "table", "main").CombinedOutput()
	t.Logf("actual route cache: err=%v %s", err, output)
	defaultGateway, gatewayError := netif.IfaceDefaultGateway("ipv6", link.Attrs().Name)
	t.Logf("production default gateway: err=%v gateway=%q", gatewayError, defaultGateway)
	selectedRoutes, selectionError := netlink.RouteGet(net.ParseIP("2001:db8:518::1"))
	t.Logf("kernel external destination lookup: err=%v routes=%#v", selectionError, selectedRoutes)
	t.Fatalf("kernel RA state mismatch: address=%t route=%t deprecated=%t; addresses=%#v routes=%#v", address, route, deprecated, lastAddresses, lastRoutes)
}
