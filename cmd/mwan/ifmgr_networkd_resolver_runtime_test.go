//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/networkjson"
)

func TestNetworkdResolverDaemonRuntime(t *testing.T) {
	if os.Getenv("MWAN_NETWORKD_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires a dedicated container with real systemd-networkd and systemd-resolved")
	}
	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(initName)) != "systemd" || os.Geteuid() != 0 {
		t.Fatalf("requires root and systemd PID 1: %q, %v", initName, err)
	}
	networkdResolverCommand(t, "systemctl", "start", "systemd-udevd", "systemd-resolved")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "network")
	unitDir := filepath.Join(root, "units")
	for _, directory := range []string{networkDir, unitDir} {
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
	networkdResolverCommand(t, "systemctl", "restart", "systemd-networkd")
	networkdResolverCommand(t, "systemctl", "is-active", "systemd-udevd", "systemd-networkd", "systemd-resolved", "dbus")
	t.Cleanup(func() { networkdResolverCommand(t, "systemctl", "stop", "systemd-networkd") })
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "dns-mgmt", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "dns-lan", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enwebpass0", "dns-server", nil, []string{"10.52.1.2/24", "fd52:1::2/64"}, "02:00:5e:52:10:01")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	queries := startNetworkdResolverAuthority(t)
	setRuntimeNamespace(t, gateway)
	writeNetworkdResolverFixture(t, networkDir, "")
	for _, key := range []string{"DNS", "Domains"} {
		writeNetworkdResolverFixture(t, networkDir, key)
		loaded, err := networkjson.Load(filepath.Join(networkDir, "network.json"), schemaDir)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, rejection := range loaded.Rejected {
			if rejection.Interface == "enwebpass0" && strings.Contains(rejection.Err.Error(), "section Network key "+key+" is set by") {
				found = true
			}
		}
		if !found {
			t.Fatalf("typed %s collision was not rejected: %+v", key, loaded.Rejected)
		}
	}
	writeNetworkdResolverFixture(t, networkDir, "")
	binary := filepath.Join(root, "mwan")
	networkdResolverCommand(t, "go", "build", "-o", binary, ".")
	configPath := filepath.Join(root, "config.toml")
	configuration := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = false\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startRuntimeDaemon(t, binary, configPath, root, "networkd-resolver")
	defer killOwnedRuntimeDaemon(t, daemon)
	waitNetworkdResolverValues(t, daemon)
	waitStaticRuntimeAddress(t, daemon, "enwebpass0", "10.52.1.1/24", true)
	waitStaticRuntimeAddress(t, daemon, "enwebpass0", "fd52:1::1/64", true)
	for _, name := range []string{"owned-br", "owned397", "late397"} {
		if _, err := os.Stat(filepath.Join(unitDir, "20-"+name+".network")); !os.IsNotExist(err) {
			t.Fatalf("MWAN-owned link %s produced a networkd unit: %v", name, err)
		}
	}
	output := networkdResolverCommand(t, "resolvectl", "query", "--cache=no", "--interface=enwebpass0", "sensor")
	if !strings.Contains(output, "10.52.1.99") || queries.Load() == 0 {
		t.Fatalf("single-label resolution failed: %s; authoritative queries=%d", output, queries.Load())
	}
	t.Logf("single-label resolution: %s; authoritative queries=%d", strings.TrimSpace(output), queries.Load())
}

func networkdResolverCommand(t *testing.T, name string, arguments ...string) string {
	t.Helper()
	output, err := exec.Command(name, arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, arguments, err, output)
	}
	return string(output)
}

func waitNetworkdResolverValues(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		servers, serversErr := exec.Command("resolvectl", "dns", "enwebpass0").CombinedOutput()
		domains, domainsErr := exec.Command("resolvectl", "domain", "enwebpass0").CombinedOutput()
		if serversErr == nil && domainsErr == nil && strings.HasSuffix(strings.TrimSpace(string(servers)), "10.52.1.2 fd52:1::2") && strings.HasSuffix(strings.TrimSpace(string(domains)), "networkd.test other.test") {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	status, statusErr := exec.Command("networkctl", "status", "enwebpass0", "--no-pager").CombinedOutput()
	journal, journalErr := exec.Command("journalctl", "-u", "systemd-networkd", "--no-pager", "-n", "25").CombinedOutput()
	t.Fatalf("networkd resolver values were not installed: status=%s (%v); journal=%s (%v); daemon=%s", status, statusErr, journal, journalErr, runtimeDaemonLog(t, daemon))
}

func writeNetworkdResolverFixture(t *testing.T, directory, collision string) {
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
		if string(entry["name"]) == `"enmbrains0"` {
			entry["goodkind-mwan-steering:link-files"] = json.RawMessage(`"rendered"`)
		}
		if string(entry["name"]) != `"enwebpass0"` {
			continue
		}
		entry["goodkind-mwan-steering:link-files"] = json.RawMessage(`"rendered"`)
		entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"networkd"`)
		entry["goodkind-mwan-steering:link"] = json.RawMessage(`{"match":{"hardware-address":"02:00:5e:52:10:01"}}`)
		entry["ietf-ip:ipv4"] = json.RawMessage(`{"goodkind-mwan-steering:dhcp":false,"address":[{"ip":"10.52.1.1","prefix-length":24}],"goodkind-mwan-steering:resolver":{"dns":["10.52.1.2"],"search":["networkd.test"]},"goodkind-mwan-steering:translation":{"mode":"native"}}`)
		entry["ietf-ip:ipv6"] = json.RawMessage(`{"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:accept-ra":false,"address":[{"ip":"fd52:1::1","prefix-length":64}],"goodkind-mwan-steering:resolver":{"dns":["fd52:1::2"],"search":["networkd.test","other.test"]},"goodkind-mwan-steering:translation":{"mode":"native"}}`)
		value := "no"
		key := "LLMNR"
		if collision != "" {
			key = collision
			value = "198.51.100.53"
			if key == "Domains" {
				value = "collision.test"
			}
		}
		entry["goodkind-mwan-steering:networkd"] = json.RawMessage(fmt.Sprintf(`{"file":[{"kind":"network","section":[{"index":0,"name":"Network","entry":[{"index":0,"key":%q,"value":%q}]}]}]}`, key, value))
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

func startNetworkdResolverAuthority(t *testing.T) *atomic.Int64 {
	t.Helper()
	connection, err := net.ListenPacket("udp", ":53")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { connection.Close(); <-done })
	queries := &atomic.Int64{}
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			size, peer, err := connection.ReadFrom(buffer)
			if err != nil {
				return
			}
			var message dnsmessage.Message
			if err := message.Unpack(buffer[:size]); err != nil {
				t.Errorf("parse DNS query: %v", err)
				return
			}
			message.Header.Response = true
			message.Header.Authoritative = true
			for _, question := range message.Questions {
				if question.Name.String() == "sensor.networkd.test." && question.Type == dnsmessage.TypeA {
					queries.Add(1)
					message.Answers = append(message.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1, Length: 0}, Body: &dnsmessage.AResource{A: [4]byte{10, 52, 1, 99}}})
				}
			}
			response, err := message.Pack()
			if err != nil {
				t.Errorf("encode DNS response: %v", err)
				return
			}
			if _, err := connection.WriteTo(response, peer); err != nil {
				t.Errorf("write DNS response: %v", err)
				return
			}
		}
	}()
	return queries
}
