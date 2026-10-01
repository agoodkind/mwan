//go:build linux && firewallnetns

package main

import (
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
	"goodkind.io/mwan/internal/yangpub"
)

const selectionRuntimeChildEnv = "MWAN_SELECTION_RUNTIME_CHILD"

func TestSelectionExclusionDaemonRuntime(t *testing.T) {
	if os.Getenv(selectionRuntimeChildEnv) == "1" {
		runSelectionExclusionDaemonRuntime(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestSelectionExclusionDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), selectionRuntimeChildEnv+"=1", mappedRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated selection test: %v: %s", err, output)
	}
}

func runSelectionExclusionDaemonRuntime(t *testing.T) {
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
	networkDir := filepath.Join(root, "network")
	networkdDir := filepath.Join(root, "networkd")
	for _, directory := range []string{networkDir, networkdDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"100ms\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "management", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "downstream", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"}, "")
	defer lan.namespace.Close()
	primary := newRuntimePeer(t, gateway, "enwebpass0", "primary", []string{"10.50.1.1/24", "fd50:1::1/64"}, []string{"10.50.1.2/24", "fd50:1::2/64", "10.50.9.2/32", "fd50:9::2/128"}, "")
	defer primary.namespace.Close()
	backup := newRuntimePeer(t, gateway, "enatt0", "backup", []string{"10.50.2.1/24", "fd50:2::1/64"}, []string{"10.50.2.2/24", "fd50:2::2/64", "10.50.9.2/32", "fd50:9::2/128"}, "")
	defer backup.namespace.Close()
	for index, peer := range []runtimePeer{primary, backup} {
		setRuntimeNamespace(t, peer.namespace)
		name := []string{"primary", "backup"}[index]
		addMappedRuntimeRoute(t, "192.0.2.0/29", fmt.Sprintf("10.50.%d.1", index+1), name)
		addMappedRuntimeRoute(t, "2001:db8:b01::/60", fmt.Sprintf("fd50:%d::1", index+1), name)
		waitAutoconfigurationLinkLocal(t, name)
	}
	setRuntimeNamespace(t, lan.namespace)
	addRuntimeDefault(t, "downstream", "192.0.2.1")
	addRuntimeDefault(t, "downstream", "2001:db8:b01:fe::3")
	waitAutoconfigurationLinkLocal(t, "downstream")
	setRuntimeNamespace(t, gateway)
	for _, name := range []string{"enmwanbr0", "enwebpass0", "enatt0"} {
		waitAutoconfigurationLinkLocal(t, name)
	}
	defer func() {
		if !t.Failed() {
			return
		}
		setRuntimeNamespace(t, gateway)
		routes, routeErr := netlink.RouteList(nil, netlink.FAMILY_V6)
		rules, ruleErr := netlink.RuleList(netlink.FAMILY_V6)
		t.Logf("gateway IPv6 main routes: %v: %v; rules: %v: %v", routes, routeErr, rules, ruleErr)
		for _, table := range []int{100, 200} {
			routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
			t.Logf("gateway IPv6 table %d: %v: %v", table, routes, err)
		}
		setRuntimeNamespace(t, lan.namespace)
		routes, routeErr = netlink.RouteGet(net.ParseIP("fd50:9::2"))
		t.Logf("downstream IPv6 destination lookup: %v: %v", routes, routeErr)
		setRuntimeNamespace(t, gateway)
		for _, arguments := range [][]string{{"nft", "list", "ruleset"}, {"ip", "-6", "route", "show", "table", "all"}, {"ip", "-6", "rule"}} {
			output, err := exec.Command(arguments[0], arguments[1:]...).CombinedOutput()
			t.Logf("%v: %v: %s", arguments, err, output)
		}
	}()
	for index, name := range []string{"enwebpass0", "enatt0"} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, gatewayAddress := range []string{fmt.Sprintf("10.50.%d.2", index+1), fmt.Sprintf("fd50:%d::2", index+1)} {
			if err := netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gatewayAddress), Priority: 100 + index}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, path := range []string{"/proc/sys/net/ipv4/ip_forward", "/proc/sys/net/ipv6/conf/all/forwarding"} {
		if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	reader, closeRepository, err := openPrivateRepository(ctx, slog.Default(), selftestFlags{repository: filepath.Join(root, "repository"), modelsDir: selftestModelsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	read := func() string {
		data, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if err != nil || !found {
			return ""
		}
		return data
	}
	writeSelectionRuntimeNetwork(t, networkDir, true)
	first := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "selection-default")
	defer killOwnedRuntimeDaemon(t, first)
	defer func() {
		if t.Failed() {
			t.Logf("initial daemon: %s", runtimeLogTail(t, first, 150))
		}
	}()
	waitSelectionRuntimeMark(t, first, 2)
	waitSelectionRuntimeState(t, first, read, "enwebpass0", true, true)
	assertStaticRuntimePacket(t, lan.namespace, primary.namespace, "udp4", "10.50.9.2:50101")
	assertStaticRuntimePacket(t, lan.namespace, primary.namespace, "udp6", "[fd50:9::2]:50102")
	setRuntimeNamespace(t, gateway)
	killOwnedRuntimeDaemon(t, first)
	writeSelectionRuntimeNetwork(t, networkDir, false)
	excluded := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "selection-excluded")
	defer killOwnedRuntimeDaemon(t, excluded)
	waitSelectionRuntimeMark(t, excluded, 1)
	waitSelectionRuntimeState(t, excluded, read, "enwebpass0", false, false)
	waitSelectionRuntimeState(t, excluded, read, "enatt0", true, true)
	assertSelectionRuntimeFallback(t, "10.50.9.2", "192.0.2.2")
	assertSelectionRuntimeFallback(t, "fd50:9::2", "2001:db8:b01:fe::2")
	assertStaticRuntimePacket(t, lan.namespace, backup.namespace, "udp4", "10.50.9.2:50103")
	assertStaticRuntimePacket(t, lan.namespace, backup.namespace, "udp6", "[fd50:9::2]:50104")
	setRuntimeNamespace(t, gateway)
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	waitSelectionRuntimeMark(t, excluded, 0)
	assertConfiguredRuntimePacketAbsent(t, gateway, lan.namespace, primary.namespace, "udp4", "10.50.9.2:50105")
	assertConfiguredRuntimePacketAbsent(t, gateway, lan.namespace, primary.namespace, "udp6", "[fd50:9::2]:50106")
}

func waitSelectionRuntimeState(t *testing.T, daemon *runtimeDaemon, read func() string, name string, enabled, carrying bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var document struct {
			Interfaces struct {
				Entries []struct {
					Name     string `json:"name"`
					Steering struct {
						State struct {
							Enabled  *bool `json:"administratively-enabled"`
							Carrying bool  `json:"carrying"`
						} `json:"state"`
					} `json:"goodkind-mwan-steering:steering"`
					V4 selectionRuntimeFamily `json:"ietf-ip:ipv4"`
					V6 selectionRuntimeFamily `json:"ietf-ip:ipv6"`
				} `json:"interface"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if json.Unmarshal([]byte(read()), &document) == nil {
			for _, entry := range document.Interfaces.Entries {
				if entry.Name == name && entry.Steering.State.Enabled != nil && *entry.Steering.State.Enabled == enabled && entry.Steering.State.Carrying == carrying && entry.V4.Ownership.Routing == "ready" && entry.V6.Ownership.Routing == "ready" && entry.V4.Translation.State.Ready && entry.V6.Translation.State.Ready {
					return
				}
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("provider %s administratively enabled=%t carrying=%t with both families ready absent: %s", name, enabled, carrying, read())
}

type selectionRuntimeFamily struct {
	Ownership struct {
		Routing string `json:"routing"`
	} `json:"goodkind-mwan-steering:ownership-family-state"`
	Translation struct {
		State struct {
			Ready bool `json:"ready"`
		} `json:"state"`
	} `json:"goodkind-mwan-steering:translation"`
}

func writeSelectionRuntimeNetwork(t *testing.T, directory string, enabled bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	interfaces := document["ietf-interfaces:interfaces"]
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	var selected []map[string]json.RawMessage
	for _, entry := range entries {
		name := string(entry["name"])
		if name == `"enmbrains0"` {
			if enabled {
				continue
			}
			entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":0,"weight":1}`)
			entry["goodkind-mwan-steering:wan"] = json.RawMessage(`{"name":"monkeybrains","table-id":300,"fw-mark":3,"fw-mark-prio":300,"from-prio":57,"health":{"enabled":false}}`)
		}
		if name == `"enwebpass0"` || name == `"enatt0"` || name == `"enmbrains0"` {
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"external"`)
			delete(entry, "goodkind-mwan-steering:link-files")
			delete(entry, "goodkind-mwan-steering:link")
			entry["ietf-ip:ipv4"] = json.RawMessage(`{"goodkind-mwan-steering:translation":{"mode":"native"}}`)
			entry["ietf-ip:ipv6"] = json.RawMessage(`{"goodkind-mwan-steering:translation":{"mode":"native"}}`)
			if name == `"enwebpass0"` {
				entry["goodkind-mwan-steering:wan"] = json.RawMessage(`{"name":"webpass","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,"health":{"enabled":false}}`)
				if !enabled {
					entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"enabled":false,"tier":1,"weight":1}`)
				}
			}
		}
		selected = append(selected, entry)
	}
	interfaces["interface"], err = json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "network.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSelectionRuntimeFallback(t *testing.T, destination, source string) {
	t.Helper()
	link, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := netlink.RouteGetWithOptions(net.ParseIP(destination), &netlink.RouteGetOptions{
		SrcAddr: net.ParseIP(source), IifIndex: link.Attrs().Index,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Table != 100 {
		t.Fatalf("unmarked ingress fallback for %s: %v", destination, routes)
	}
}

func waitSelectionRuntimeMark(t *testing.T, daemon *runtimeDaemon, mark int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var output []byte
	for time.Now().Before(deadline) {
		var err error
		output, err = exec.Command("nft", "list", "chain", "inet", "mwan_steer", "prerouting").CombinedOutput()
		if err == nil {
			if mark == 0 && !strings.Contains(string(output), "meta mark set") {
				return
			}
			if mark != 0 && selectionRuntimeFamilyMarks(string(output), mark) && !strings.Contains(string(output), fmt.Sprintf("meta mark set 0x%08x", 3-mark)) {
				return
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("selection mark %d absent: %s; daemon=%s", mark, output, runtimeLogTail(t, daemon, 100))
}

func selectionRuntimeFamilyMarks(output string, mark int) bool {
	v4, v6 := false, false
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, fmt.Sprintf("meta mark set 0x%08x", mark)) {
			continue
		}
		v4 = v4 || strings.Contains(line, "ip saddr")
		v6 = v6 || strings.Contains(line, "ip6 saddr")
	}
	return v4 && v6
}
