//go:build linux && firewallnetns

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/installspec"
	"goodkind.io/mwan/internal/networkd"
)

func TestNetworkdOrderedDaemonStartup(t *testing.T) {
	if os.Getenv("MWAN_NETWORKD_STARTUP_SYSTEMD_TEST") != "1" {
		t.Skip("requires a dedicated root container with systemd PID 1, networkd and resolved")
	}
	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(initName)) != "systemd" || os.Geteuid() != 0 {
		t.Fatalf("requires root and systemd PID 1: %q, %v", initName, err)
	}
	networkdResolverCommand(t, "systemctl", "start", "dbus", "systemd-udevd", "systemd-resolved")
	networkdResolverCommand(t, "systemctl", "stop", "systemd-networkd", "systemd-networkd.socket")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root, err := os.MkdirTemp("/run", "mwan-startup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	networkDir, unitDir := filepath.Join(root, "network"), filepath.Join(root, "units")
	for _, directory := range []string{networkDir, unitDir, "/var/lib/mwan"} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	for destination, source := range map[string]string{"/etc/mwan": networkDir, "/etc/systemd/network": unitDir, "/usr/local/share/wanconfig/yang": schemaDir} {
		bindStartupDirectory(t, source, destination)
		t.Cleanup(func() {
			if err := unix.Unmount(destination, 0); err != nil {
				t.Errorf("unmount %s: %v", destination, err)
			}
		})
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "startup-mgmt", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "startup-lan", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enwebpass0", "startup-dns", nil, []string{"10.52.1.2/24", "fd52:1::2/64"}, "02:00:5e:52:10:01")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	queries := startNetworkdResolverAuthority(t)
	setRuntimeNamespace(t, gateway)
	binary := protocolTestBinary(t)
	if binary == "/usr/local/bin/mwan" {
		t.Fatal("selected executable must differ from the isolated service installation path")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/usr/local/bin/mwan", data, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("getent", "group", "sysrepo").CombinedOutput(); err != nil {
		t.Logf("provision sysrepo group: %s", output)
		networkdResolverCommand(t, "groupadd", "--system", "sysrepo")
	}
	configPath := filepath.Join(root, "config.toml")
	configuration := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel.json"))
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	installStartupDaemonUnit(t, configPath)
	t.Cleanup(func() {
		networkdResolverCommand(t, "systemctl", "stop", "mwan-ifmgr@wan.service", "systemd-networkd", "systemd-networkd.socket")
		for _, path := range []string{"/etc/systemd/system/mwan-ifmgr@.service", "/etc/systemd/system/mwan-ifmgr@wan.service.d", "/usr/local/bin/mwan"} {
			if err := os.RemoveAll(path); err != nil {
				t.Errorf("remove test dependency %s: %v", path, err)
			}
		}
		networkdResolverCommand(t, "systemctl", "daemon-reload")
	})
	defer func() {
		if t.Failed() {
			t.Log(networkdResolverCommand(t, "journalctl", "-u", "mwan-ifmgr@wan", "-u", "systemd-networkd", "--no-pager", "-n", "80"))
		}
	}()
	writeNetworkdResolverFixture(t, networkDir, "")
	// Cold boot must render before networkd starts without activating networkd through D-Bus.
	startOrderedDaemon(t, "start")
	state := networkdResolverCommand(t, "systemctl", "show", "systemd-networkd", "--property=ActiveState", "--value")
	if strings.TrimSpace(state) != "inactive" {
		t.Fatalf("cold startup activated networkd: %s", state)
	}
	assertStartupFirewall(t)
	networkdResolverCommand(t, "systemctl", "stop", "mwan-ifmgr@wan")
	networkdResolverCommand(t, "systemctl", "start", "systemd-networkd")
	for index, address := range []string{"10.52.1.3", "10.52.1.4"} {
		writeStartupProviderAddress(t, networkDir, address)
		operation := "start"
		if index > 0 {
			operation = "restart"
		}
		startOrderedDaemon(t, operation)
		waitStartupProviderApplied(t, address)
		assertStartupFirewall(t)
		selected := networkdResolverCommand(t, "networkctl", "status", "enwebpass0", "--no-pager")
		if !strings.Contains(selected, "/etc/systemd/network/20-enwebpass0.network") {
			t.Fatalf("generated unit was not selected: %s", selected)
		}
		output := networkdResolverCommand(t, "resolvectl", "query", "--cache=no", "--interface=enwebpass0", "sensor")
		if !strings.Contains(output, "10.52.1.99") || queries.Load() == 0 {
			t.Fatalf("real DNS query failed: %s; queries=%d", output, queries.Load())
		}
	}
	assertStartupNamingTransfer(t, networkDir, unitDir)
}

func assertStartupNamingTransfer(t *testing.T, networkDir, unitDir string) {
	t.Helper()
	foreign := filepath.Join(unitDir, "05-competing.link")
	content := "[Match]\nPermanentMACAddress=02:00:5e:52:20:01\n[Link]\nName=competing0\n"
	for _, competitor := range []string{"none", "foreign", "retained", "symlink"} {
		t.Run("naming-"+competitor, func(t *testing.T) {
			writeStartupNamingConnection(t, networkDir, "networkd", competitor == "retained")
			startOrderedDaemon(t, "restart")
			// Reset the active unit's start allowance between independent rejection controls.
			networkdResolverCommand(t, "systemctl", "reset-failed", "mwan-ifmgr@wan")
			legacy := filepath.Join(unitDir, "20-ownedboot0.link")
			if _, err := os.Stat(legacy); err != nil {
				t.Fatalf("networkd naming file absent: %v", err)
			}
			if competitor == "foreign" {
				if err := os.WriteFile(foreign, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if competitor == "symlink" {
				target := filepath.Join(networkDir, "marked-target.link")
				if err := os.WriteFile(target, []byte(networkd.Marker+"\n"+content), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, foreign); err != nil {
					t.Fatal(err)
				}
			}
			writeStartupNamingConnection(t, networkDir, "mwan", competitor == "retained")
			before := checkedUnitDirectory(t, unitDir)
			if competitor != "none" {
				ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
				defer cancel()
				output, err := exec.CommandContext(ctx, "systemctl", "restart", "mwan-ifmgr@wan").CombinedOutput()
				if err == nil {
					t.Fatalf("competing naming file accepted: %s", output)
				}
				networkdResolverCommand(t, "systemctl", "stop", "mwan-ifmgr@wan")
				if after := checkedUnitDirectory(t, unitDir); !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected naming transition changed unit files: before=%+v after=%+v", before, after)
				}
				if competitor == "foreign" || competitor == "symlink" {
					if err := os.Remove(foreign); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			startOrderedDaemon(t, "restart")
			if _, err := os.Stat(legacy); !os.IsNotExist(err) {
				t.Fatalf("obsolete naming file remains: %v", err)
			}
			after := checkedUnitDirectory(t, unitDir)
			ownedNames := 0
			for path, state := range after {
				if strings.HasPrefix(filepath.Base(path), "10-mwan-") &&
					strings.Contains(state.Content, "Name=ownedboot0\n") {
					ownedNames++
				}
			}
			if ownedNames != 1 {
				t.Fatalf("owned physical naming files=%d, want one", ownedNames)
			}
			for path, state := range before {
				if filepath.Base(path) == "20-ownedboot0.link" || filepath.Base(path) == "20-ownedboot0.network" || path == unitDir {
					continue
				}
				if after[path] != state {
					t.Fatalf("naming transfer changed unrelated file %s", path)
				}
			}
		})
	}
}

func writeStartupNamingConnection(t *testing.T, directory, owner string, retained bool) {
	t.Helper()
	writeNetworkdResolverFixture(t, directory, "")
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
	var entries []json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	linkFiles := ""
	if owner == "networkd" {
		linkFiles = `,"goodkind-mwan-steering:link-files":"rendered"`
	}
	entries = append(entries, json.RawMessage(fmt.Sprintf(`{"name":"ownedboot0","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"ownedboot0","goodkind-mwan-steering:owner":%q,"goodkind-mwan-steering:link":{"match":{"hardware-address":"02:00:5e:52:20:01"}}%s}`, owner, linkFiles)))
	if retained {
		entries = append(entries, json.RawMessage(`{"name":"retainedboot0","type":"iana-if-type:other","goodkind-mwan-steering:owner":"networkd","goodkind-mwan-steering:link-files":"rendered","goodkind-mwan-steering:link":{"match":{"driver":"virtio_net"}}}`))
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

func installStartupDaemonUnit(t *testing.T, configPath string) {
	t.Helper()
	for _, file := range []struct{ source, destination string }{
		{"mwan-ifmgr@.service", "/etc/systemd/system/mwan-ifmgr@.service"},
		{"mwan-ifmgr-wan.conf", "/etc/systemd/system/mwan-ifmgr@wan.service.d/firewall.conf"},
	} {
		data, err := installspec.Read(file.source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(file.destination), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.destination, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The short deadline detects startup deadlock without changing the released unit.
	watchdog := fmt.Sprintf("[Service]\nEnvironment=MWAN_CONFIG=%s\nTimeoutStartSec=8s\n", configPath)
	if err := os.WriteFile("/etc/systemd/system/mwan-ifmgr@wan.service.d/test.conf", []byte(watchdog), 0o644); err != nil {
		t.Fatal(err)
	}
	networkdResolverCommand(t, "systemctl", "daemon-reload")
}

func startOrderedDaemon(t *testing.T, operation string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", operation, "mwan-ifmgr@wan").CombinedOutput()
	if err != nil {
		t.Fatalf("ordered daemon startup failed: %v: %s", err, output)
	}
	state := networkdResolverCommand(t, "systemctl", "show", "mwan-ifmgr@wan", "--property=ActiveState", "--property=NRestarts", "--property=Type", "--property=Before")
	if !strings.Contains(state, "ActiveState=active") || !strings.Contains(state, "NRestarts=0") || !strings.Contains(state, "Type=notify") || !strings.Contains(state, "systemd-networkd.service") {
		t.Fatalf("production daemon startup contract failed: %s", state)
	}
}

func assertStartupFirewall(t *testing.T) {
	t.Helper()
	input := networkdResolverCommand(t, "nft", "list", "chain", "inet", "filter", "input")
	if !strings.Contains(input, "policy drop") || !strings.Contains(input, "enmgmt0") || !strings.Contains(input, "dport 22") {
		t.Fatalf("protective management firewall absent: %s", input)
	}
}

func writeStartupProviderAddress(t *testing.T, directory, address string) {
	t.Helper()
	writeNetworkdResolverFixture(t, directory, "")
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), `"10.52.1.1"`, fmt.Sprintf("%q", address)))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitStartupProviderApplied(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName("enwebpass0")
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range addresses {
			found = found || candidate.IP.Equal(net.ParseIP(address))
		}
		servers, serverErr := exec.Command("resolvectl", "dns", "enwebpass0").CombinedOutput()
		domains, domainErr := exec.Command("resolvectl", "domain", "enwebpass0").CombinedOutput()
		if found && serverErr == nil && domainErr == nil && strings.Contains(string(servers), "10.52.1.2 fd52:1::2") && strings.Contains(string(domains), "networkd.test other.test") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("networkd did not apply address %s and typed DNS", address)
}
