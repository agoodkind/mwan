//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	staticRuntimeChildEnv  = "MWAN_STATIC_RUNTIME_TEST_CHILD"
	staticRuntimeBinaryEnv = "MWAN_STATIC_RUNTIME_TEST_BINARY"
)

func TestOwnedStaticDaemonRuntime(t *testing.T) {
	if os.Getenv(staticRuntimeChildEnv) == "1" {
		runOwnedStaticDaemonRuntime(t)
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
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedStaticDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), staticRuntimeChildEnv+"=1", staticRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated static daemon test: %v: %s", err, output)
	}
}

func runOwnedStaticDaemonRuntime(t *testing.T) {
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
	staticStateDir := filepath.Join(root, "static-state")
	if err := os.MkdirAll(staticStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[wanconfig]\npublish = true\n", filepath.Join(root, "owned-links.json"), filepath.Join(staticStateDir, "owned-static.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	parentPeer := newRuntimePeer(t, gateway, "ownphys0", "owned-peer", nil, nil, ownedRuntimeParentMAC)
	defer parentPeer.namespace.Close()
	setRuntimeNamespace(t, parentPeer.namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index}, VlanId: 397}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "peer397", []string{"10.39.7.2/24", "10.39.7.3/24", "fd39:7::2/64", "fd39:7::3/64"})
	setRuntimeNamespace(t, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	reader, closeRepository, err := openPrivateRepository(ctx, slog.Default(), selftestFlags{repository: filepath.Join(root, "repository"), modelsDir: selftestModelsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	read := func() string {
		value, found, readErr := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if readErr != nil || !found {
			return ""
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(value)); err != nil {
			t.Fatal(err)
		}
		return compact.String()
	}
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
	binary := os.Getenv(staticRuntimeBinaryEnv)
	first := startRuntimeDaemon(t, binary, configPath, root, "static-first")
	defer killOwnedRuntimeDaemon(t, first)
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	waitStaticRuntimeAddress(t, first, "owned397", "10.39.7.1/24", true)
	promotionPath := "/proc/sys/net/ipv4/conf/owned397/promote_secondaries"
	promotion, err := os.ReadFile(promotionPath)
	if err != nil || string(promotion) != "1\n" {
		t.Fatalf("owned IPv4 promotion: value=%q err=%v", promotion, err)
	}
	waitStaticRuntimeAddress(t, first, "owned397", "fd39:7::1/64", true)
	waitStaticRuntimeRoute(t, first, "ipv4", "10.39.7.2", 398, true)
	waitStaticRuntimeRoute(t, first, "ipv6", "fd39:7::2", 398, true)
	waitRuntimeOwnershipRead(t, first, read, `"kind":"static-route"`)
	waitRuntimeOwnershipRead(t, first, read, `"gateway":"10.39.7.2"`)
	waitOwnedRuntimeAbsent(t, first, "late397", 10*time.Second)
	assertStaticRuntimePacket(t, gateway, parentPeer.namespace, "udp4", "10.39.7.2:39701")
	assertStaticRuntimePacket(t, gateway, parentPeer.namespace, "udp6", "[fd39:7::2]:39702")
	link, err := netlink.LinkByName("owned397")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := netlink.ParseAddr("10.39.7.99/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, foreign); err != nil {
		t.Fatal(err)
	}
	waitStaticRuntimeAddress(t, first, "owned397", "10.39.7.99/24", true)
	if files, err := os.ReadDir(networkdDir); err != nil || len(files) != 0 {
		t.Fatalf("networkd files changed: %v, %v", files, err)
	}
	killOwnedRuntimeDaemon(t, first)
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.4", "fd39:7::4", "10.39.7.3", "fd39:7::3", 400, 24, true)
	second := startRuntimeDaemon(t, binary, configPath, root, "static-second")
	defer killOwnedRuntimeDaemon(t, second)
	waitRuntimeTable(t, second, "inet", "filter", 10*time.Second)
	waitStaticRuntimeAddress(t, second, "owned397", "10.39.7.4/24", true)
	waitStaticRuntimeAddress(t, second, "owned397", "fd39:7::4/64", true)
	waitStaticRuntimeAddress(t, second, "owned397", "10.39.7.1/24", false)
	waitStaticRuntimeAddress(t, second, "owned397", "fd39:7::1/64", false)
	waitStaticRuntimeAddress(t, second, "owned397", "10.39.7.99/24", true)
	waitStaticRuntimeRoute(t, second, "ipv4", "10.39.7.3", 400, true)
	waitStaticRuntimeRoute(t, second, "ipv6", "fd39:7::3", 400, true)
	waitStaticRuntimeRoute(t, second, "ipv4", "10.39.7.2", 398, false)
	latePeer := newRuntimePeer(t, gateway, "latephys0", "late-peer", nil, nil, ownedRuntimeLateMAC)
	defer latePeer.namespace.Close()
	setRuntimeNamespace(t, gateway)
	waitStaticRuntimeAddress(t, second, "late397", "10.39.8.1/24", true)
	lateLink, err := netlink.LinkByName("late397")
	if err != nil {
		t.Fatal(err)
	}
	lateParent, err := netlink.LinkByName("latephys0")
	if err != nil || lateLink.Attrs().ParentIndex != lateParent.Attrs().Index {
		t.Fatalf("late static link parent: link=%v parent=%v err=%v", lateLink, lateParent, err)
	}
	killOwnedRuntimeDaemon(t, second)
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.4", "fd39:7::4", "10.39.7.3", "fd39:7::3", 402, 24, true)
	metricOnly := startRuntimeDaemon(t, binary, configPath, root, "static-metric-only")
	defer killOwnedRuntimeDaemon(t, metricOnly)
	waitStaticRuntimeRoute(t, metricOnly, "ipv4", "10.39.7.3", 402, true)
	waitStaticRuntimeRoute(t, metricOnly, "ipv4", "10.39.7.3", 400, false)
	waitStaticRuntimeAddress(t, metricOnly, "owned397", "10.39.7.4/24", true)
	killOwnedRuntimeDaemon(t, metricOnly)
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.4", "fd39:7::4", "10.39.7.3", "fd39:7::3", 402, 25, true)
	prefixChange := startRuntimeDaemon(t, binary, configPath, root, "static-prefix-change")
	defer killOwnedRuntimeDaemon(t, prefixChange)
	waitStaticRuntimeAddress(t, prefixChange, "owned397", "10.39.7.4/25", true)
	waitStaticRuntimeAddress(t, prefixChange, "owned397", "10.39.7.4/24", false)
	waitStaticRuntimeAddress(t, prefixChange, "owned397", "10.39.7.99/24", true)
	waitStaticRuntimeRoute(t, prefixChange, "ipv4", "10.39.7.3", 402, true)
	killOwnedRuntimeDaemon(t, prefixChange)
	foreignRoute := &netlink.Route{LinkIndex: link.Attrs().Index, Table: unix.RT_TABLE_MAIN, Family: unix.AF_INET, Gw: net.ParseIP("10.39.7.2"), Priority: 401, Protocol: unix.RTPROT_STATIC}
	if err := netlink.RouteAdd(foreignRoute); err != nil {
		t.Fatal(err)
	}
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.4", "fd39:7::4", "10.39.7.3", "fd39:7::3", 401, 25, true)
	conflict := startRuntimeDaemon(t, binary, configPath, root, "static-conflict")
	defer killOwnedRuntimeDaemon(t, conflict)
	waitStaticRuntimeLog(t, conflict, "default route metric 401 conflicts with an unowned route")
	waitStaticRuntimeRoute(t, conflict, "ipv4", "10.39.7.3", 402, true)
	waitStaticRuntimeRoute(t, conflict, "ipv4", "10.39.7.3", 401, false)
	waitStaticRuntimeFamilyApply(t, conflict, read, "owned397", "ipv4", "failed", "default route metric 401 conflicts with an unowned route")
	if routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE); err != nil {
		t.Fatal(err)
	} else {
		found := false
		for _, route := range routes {
			found = found || route.Priority == 401 && route.Protocol == unix.RTPROT_STATIC && route.Gw.Equal(net.ParseIP("10.39.7.2"))
		}
		if !found {
			t.Fatal("foreign default route was changed")
		}
	}
	killOwnedRuntimeDaemon(t, conflict)
	if err := netlink.LinkDel(lateParent); err != nil {
		t.Fatal(err)
	}
	writeStaticRuntimeNetwork(t, networkDir, "", "fd39:7::4", "", "fd39:7::3", 400, 24, false)
	third := startRuntimeDaemon(t, binary, configPath, root, "static-third")
	defer killOwnedRuntimeDaemon(t, third)
	waitRuntimeTable(t, third, "inet", "filter", 10*time.Second)
	waitStaticRuntimeFamilyApply(t, third, read, "late397", "ipv4", "failed", "connection late397 link is not ready")
	waitStaticRuntimeAddress(t, third, "owned397", "10.39.7.4/25", false)
	waitStaticRuntimeAddress(t, third, "owned397", "fd39:7::4/64", true)
	waitStaticRuntimeAddress(t, third, "owned397", "10.39.7.99/24", true)
	waitStaticRuntimeRoute(t, third, "ipv4", "10.39.7.3", 402, false)
	waitStaticRuntimeRoute(t, third, "ipv6", "fd39:7::3", 400, true)
	promotion, err = os.ReadFile(promotionPath)
	if err != nil || string(promotion) != "0\n" {
		t.Fatalf("restored IPv4 promotion: value=%q err=%v", promotion, err)
	}
	latePeer = newRuntimePeer(t, gateway, "latephys0", "late-peer-new", nil, nil, ownedRuntimeLateMAC)
	defer latePeer.namespace.Close()
	setRuntimeNamespace(t, gateway)
	waitStaticRuntimeAddress(t, third, "late397", "10.39.8.1/24", true)
	waitStaticRuntimeFamilyApply(t, third, read, "late397", "ipv4", "ready", "")
	for _, name := range []string{"ownphys0", "enmgmt0", "enmwanbr0"} {
		if _, err := netlink.LinkByName(name); err != nil {
			t.Fatalf("static family removal changed %s: %v", name, err)
		}
	}
	killOwnedRuntimeDaemon(t, third)
	removeStaticRuntimeConnection(t, networkDir, "owned397")
	if err := unix.Mount(staticStateDir, staticStateDir, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	mounted := true
	t.Cleanup(func() {
		if mounted {
			_ = unix.Unmount(staticStateDir, 0)
		}
	})
	if err := unix.Mount("", staticStateDir, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		t.Fatal(err)
	}
	failedRemoval := startRuntimeDaemon(t, binary, configPath, root, "static-remove-failed")
	defer killOwnedRuntimeDaemon(t, failedRemoval)
	waitStaticPendingRemoval(t, failedRemoval, read, "owned397", "ipv6", true)
	if err := unix.Unmount(staticStateDir, 0); err != nil {
		t.Fatal(err)
	}
	mounted = false
	lateParent, err = netlink.LinkByName("latephys0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(lateParent); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lateParent); err != nil {
		t.Fatal(err)
	}
	waitStaticPendingRemoval(t, failedRemoval, read, "owned397", "ipv6", false)
	waitOwnedRuntimeAbsent(t, failedRemoval, "owned397", 10*time.Second)
}

func removeStaticRuntimeConnection(t *testing.T, directory, name string) {
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
	retained := entries[:0]
	for _, entry := range entries {
		var entryName string
		if err := json.Unmarshal(entry["name"], &entryName); err != nil {
			t.Fatal(err)
		}
		if entryName != name {
			retained = append(retained, entry)
		}
	}
	interfaces["interface"], err = json.Marshal(retained)
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

func writeStaticRuntimeNetwork(t *testing.T, directory, ipv4, ipv6, gateway4, gateway6 string, metric, prefixLength int, includeV4 bool) {
	t.Helper()
	writeOwnedRuntimeNetwork(t, directory, false, true)
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
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name != "owned397" {
			if name == "late397" {
				entry["ietf-ip:ipv4"] = json.RawMessage(`{"address":[{"ip":"10.39.8.1","prefix-length":24}]}`)
			}
			continue
		}
		entry["enabled"] = json.RawMessage("true")
		if includeV4 {
			entry["ietf-ip:ipv4"] = json.RawMessage(fmt.Sprintf(`{"address":[{"ip":%q,"prefix-length":%d}],"goodkind-mwan-steering:gateway":%q,"goodkind-mwan-steering:route-metric":%d}`, ipv4, prefixLength, gateway4, metric))
		}
		entry["ietf-ip:ipv6"] = json.RawMessage(fmt.Sprintf(`{"address":[{"ip":%q,"prefix-length":64}],"goodkind-mwan-steering:gateway":%q,"goodkind-mwan-steering:route-metric":%d}`, ipv6, gateway6, metric))
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

func waitStaticRuntimeLog(t *testing.T, daemon *runtimeDaemon, expected string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(runtimeDaemonLog(t, daemon), expected) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon log lacks %q: %s", expected, runtimeLogTail(t, daemon, 40))
}

func waitStaticRuntimeFamilyApply(t *testing.T, daemon *runtimeDaemon, read func() string, name, family, result, reason string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var document struct {
			Interfaces struct {
				Entries []map[string]json.RawMessage `json:"interface"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if err := json.Unmarshal([]byte(read()), &document); err == nil {
			for _, entry := range document.Interfaces.Entries {
				var entryName string
				if json.Unmarshal(entry["name"], &entryName) != nil || entryName != name {
					continue
				}
				var familyState struct {
					Ownership struct {
						LastApply struct {
							Result string `json:"result"`
							Reason string `json:"reason"`
						} `json:"last-apply"`
					} `json:"goodkind-mwan-steering:ownership-family-state"`
				}
				if json.Unmarshal(entry["ietf-ip:"+family], &familyState) == nil &&
					familyState.Ownership.LastApply.Result == result && familyState.Ownership.LastApply.Reason == reason {
					return
				}
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s %s apply result=%s reason=%q absent from operational state: %s", name, family, result, reason, read())
}

func waitStaticPendingRemoval(t *testing.T, daemon *runtimeDaemon, read func() string, id, family string, present bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		value := read()
		var document struct {
			Interfaces struct {
				Group struct {
					State struct {
						Pending []struct {
							ConnectionID string `json:"connection-id"`
							Family       string `json:"family"`
							Result       string `json:"result"`
							Reason       string `json:"reason"`
						} `json:"pending-removal"`
					} `json:"state"`
				} `json:"goodkind-mwan-steering:steering-group"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if value != "" && json.Unmarshal([]byte(value), &document) == nil {
			found := false
			for _, pending := range document.Interfaces.Group.State.Pending {
				if pending.ConnectionID == id && pending.Family == family {
					found = pending.Result == "failed" && pending.Reason != ""
				}
			}
			if found == present {
				return
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pending removal %s/%s present=%t absent from operational state: %s; daemon: %s", id, family, present, read(), runtimeLogTail(t, daemon, 50))
}

func waitStaticRuntimeAddress(t *testing.T, daemon *runtimeDaemon, name, prefix string, present bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var observed []netlink.Addr
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(name)
		if netif.IsLinkNotFound(err) {
			assertRuntimeDaemonRunning(t, daemon)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			t.Fatal(err)
		}
		observed = addresses
		found := false
		for _, address := range addresses {
			if address.IPNet.String() == prefix {
				found = !present || address.Flags&unix.IFA_F_TENTATIVE == 0
			}
		}
		if found == present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	var relevant []string
	for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
		if strings.Contains(line, `"module":"addresses"`) || strings.Contains(line, `"module":"links"`) {
			relevant = append(relevant, line)
		}
	}
	if len(relevant) > 20 {
		relevant = relevant[len(relevant)-20:]
	}
	t.Fatalf("address %s on %s present=%t not observed; current=%v: %s", prefix, name, present, observed, strings.Join(relevant, "\n"))
}

func waitStaticRuntimeRoute(t *testing.T, daemon *runtimeDaemon, family, gateway string, metric int, present bool) {
	t.Helper()
	addressFamily := unix.AF_INET
	if family == "ipv6" {
		addressFamily = unix.AF_INET6
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(addressFamily, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, route := range routes {
			if route.Priority == metric && route.Gw.Equal(net.ParseIP(gateway)) && route.Protocol == netif.OwnedStaticRouteProtocol {
				found = true
			}
		}
		if found == present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("default route via %s metric %d present=%t not observed: %s", gateway, metric, present, runtimeLogTail(t, daemon, 40))
}

func assertStaticRuntimePacket(t *testing.T, gateway, peer netns.NsHandle, network, destination string) {
	t.Helper()
	setRuntimeNamespace(t, peer)
	listener, err := net.ListenPacket(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, gateway)
	connection, err := net.DialTimeout(network, destination, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("static-owned")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, peer)
	if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, _, err := listener.ReadFrom(buffer)
	setRuntimeNamespace(t, gateway)
	if err != nil || !bytes.Equal(buffer[:count], []byte("static-owned")) {
		t.Fatalf("%s packet: count=%d data=%q error=%v", network, count, buffer[:count], err)
	}
}
