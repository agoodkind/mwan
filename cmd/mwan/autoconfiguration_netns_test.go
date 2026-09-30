//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	raRuntimeChildEnv  = "MWAN_AUTOCONFIGURATION_TEST_CHILD"
	raRuntimeBinaryEnv = "MWAN_AUTOCONFIGURATION_TEST_BINARY"
)

func TestAutoconfigurationDaemonRuntime(t *testing.T) {
	if os.Getenv(raRuntimeChildEnv) == "1" {
		runAutoconfigurationDaemonRuntime(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestAutoconfigurationDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), raRuntimeChildEnv+"=1", raRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated autoconfiguration daemon: %v: %s", err, output)
	}
}

func runAutoconfigurationDaemonRuntime(t *testing.T) {
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
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	peer := newRuntimePeer(t, gateway, "ownphys0", "owned-peer", nil, nil, ownedRuntimeParentMAC)
	defer peer.namespace.Close()
	setRuntimeNamespace(t, peer.namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index}, VlanId: 397}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "peer397", []string{"10.39.7.2/24", "2001:db8:517::1/64"})
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOwnedRuntimeNetwork(t, networkDir, false, true)
	writeAutoconfigurationRuntimeNetwork(t, networkDir)
	daemon := startRuntimeDaemon(t, os.Getenv(raRuntimeBinaryEnv), configPath, root, "ra-runtime")
	defer killOwnedRuntimeDaemon(t, daemon)
	link := waitOwnedRuntimeLink(t, daemon, "owned397", 10*time.Second)
	waitAutoconfigurationSysctls(t, daemon, "owned397")
	waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
	waitAutoconfigurationLinkLocal(t, "owned397")
	assertStaticRuntimePacket(t, gateway, peer.namespace, "udp4", "10.39.7.2:51701")
	setRuntimeNamespace(t, peer.namespace)
	waitAutoconfigurationLinkLocal(t, "peer397")
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
	setRuntimeNamespace(t, gateway)
	waitAutoconfigurationState(t, daemon, link, true, true, false)
	assertStaticRuntimePacket(t, gateway, peer.namespace, "udp6", "[2001:db8:517::1]:51702")
	waitAutoconfigurationState(t, daemon, link, true, false, true)
	waitAutoconfigurationState(t, daemon, link, false, false, false)
	waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
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
		foundRoute := false
		for _, current := range routes {
			if current.LinkIndex == link.Attrs().Index && current.Dst != nil && current.Dst.String() == "::/0" && current.Priority == 4700 && current.Protocol == unix.RTPROT_RA {
				foundRoute = true
			}
		}
		if foundAddress == address && foundRoute == route && (!address || foundDeprecated == deprecated) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("kernel RA state mismatch: address=%t route=%t deprecated=%t; addresses=%v routes=%v", address, route, deprecated, lastAddresses, lastRoutes)
}
