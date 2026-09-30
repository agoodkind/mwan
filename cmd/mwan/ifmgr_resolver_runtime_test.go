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
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/resolved"
	"goodkind.io/mwan/internal/yangpub"
)

func TestStaticResolverDaemonRuntime(t *testing.T) {
	if os.Getenv("MWAN_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires a dedicated container with real systemd-resolved and systemd PID 1")
	}
	if os.Geteuid() != 0 {
		t.Fatal("resolver runtime container requires root")
	}
	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(initName)) != "systemd" {
		t.Fatalf("systemd PID 1: %q, %v", initName, err)
	}
	runResolverCommand(t, "systemctl", "is-active", "systemd-resolved", "dbus")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "network")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	t.Cleanup(func() {
		if err := unix.Unmount("/etc/mwan", 0); err != nil {
			t.Errorf("unmount network directory: %v", err)
		}
	})
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	t.Cleanup(func() {
		if err := unix.Unmount("/usr/local/share/wanconfig/yang", 0); err != nil {
			t.Errorf("unmount schemas: %v", err)
		}
	})
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "resolver-mgmt", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "resolver-lan", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer lan.namespace.Close()
	peer := newRuntimePeer(t, gateway, "ownphys0", "resolver-dns", nil, nil, ownedRuntimeParentMAC)
	defer peer.namespace.Close()
	setRuntimeNamespace(t, peer.namespace)
	parent, err := netlink.LinkByName("resolver-dns")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "resolver397", ParentIndex: parent.Attrs().Index}, VlanId: 397}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "resolver397", []string{"10.39.7.2/24", "fd39:7::2/64"})
	queries := startResolverAuthority(t)
	setRuntimeNamespace(t, gateway)
	unrelated := &netlink.Dummy{LinkAttrs: netlink.NewLinkAttrs()}
	unrelated.Name = "resolver-other"
	if err := netlink.LinkAdd(unrelated); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netlink.LinkDel(unrelated); err != nil {
			t.Errorf("delete unrelated link: %v", err)
		}
	})
	runResolverCommand(t, "resolvectl", "dns", "resolver-other", "198.51.100.53")
	runResolverCommand(t, "resolvectl", "domain", "resolver-other", "~unrelated.test")
	otherDNS, otherDomains := resolverRuntimeValues(t, "resolver-other")
	binary := filepath.Join(root, "mwan")
	runResolverCommand(t, "go", "build", "-o", binary, ".")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"200ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[ifmgr.modules.resolver]\nstate_file = %q\n[wanconfig]\npublish = true\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel-policy.json"), filepath.Join(root, "resolver.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reader, closeRepository, err := openPrivateRepository(ctx, slog.Default(), selftestFlags{repository: filepath.Join(root, "repository"), modelsDir: selftestModelsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	read := func() string {
		value, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if err != nil || !found {
			return ""
		}
		return value
	}
	wantedDNS := []resolved.DNS{{Family: 2, Address: []byte{10, 39, 7, 2}, Port: 0, ServerName: ""}, {Family: 10, Address: net.ParseIP("fd39:7::2").To16(), Port: 0, ServerName: ""}}
	wantedDomains := []resolved.Domain{{Name: "lab.test", RoutingOnly: false}, {Name: "v6.test", RoutingOnly: false}}
	writeResolverRuntimeNetwork(t, networkDir, false, false, false)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "resolver-bootstrap")
	defer func() { killOwnedRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
	waitStaticRuntimeAddress(t, daemon, "owned397", "fd39:7::1/64", true)
	killOwnedRuntimeDaemon(t, daemon)
	runResolverCommand(t, "resolvectl", "dns", "owned397", "192.0.2.53:5300#baseline.example.test")
	runResolverCommand(t, "resolvectl", "domain", "owned397", "baseline.test", "~route.test")
	baselineDNS, baselineDomains := resolverRuntimeValues(t, "owned397")
	if len(baselineDNS) != 1 || baselineDNS[0].Port != 5300 || baselineDNS[0].ServerName != "baseline.example.test" || len(baselineDomains) != 2 || !baselineDomains[1].RoutingOnly {
		t.Fatalf("resolved baseline lacks extended server/domain values: %+v %+v", baselineDNS, baselineDomains)
	}
	writeResolverRuntimeNetwork(t, networkDir, true, true, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-first")
	waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
	waitRuntimeOwnershipRead(t, daemon, read, `"search"`)
	if output := runResolverCommand(t, "resolvectl", "query", "sensor"); !strings.Contains(output, "10.39.7.99") || queries.Load() == 0 {
		t.Fatalf("single-label DNS query: %s; authoritative queries=%d", output, queries.Load())
	}
	initialOtherDNS, initialOtherDomains := resolverRuntimeValues(t, "resolver-other")
	if !reflect.DeepEqual(initialOtherDNS, otherDNS) || !reflect.DeepEqual(initialOtherDomains, otherDomains) {
		t.Fatalf("initial apply changed unrelated settings: %+v %+v", initialOtherDNS, initialOtherDomains)
	}
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverRuntimeNetwork(t, networkDir, true, true, true)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-ipv6-only")
	waitResolverRuntimeValues(t, daemon, wantedDNS[1:], wantedDomains)
	runResolverCommand(t, "resolvectl", "flush-caches")
	beforeIPv6Query := queries.Load()
	if output := runResolverCommand(t, "resolvectl", "query", "sensor"); !strings.Contains(output, "10.39.7.99") || queries.Load() <= beforeIPv6Query {
		t.Fatalf("IPv6-only DNS query: %s; authoritative queries before=%d after=%d", output, beforeIPv6Query, queries.Load())
	}
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverRuntimeNetwork(t, networkDir, true, true, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-restart")
	waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
	runResolverCommand(t, "systemctl", "restart", "systemd-resolved")
	waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
	// Resolved restart resets unrelated per-link settings; reset the control before subsequent reconciles.
	runResolverCommand(t, "resolvectl", "dns", "resolver-other", "198.51.100.53")
	runResolverCommand(t, "resolvectl", "domain", "resolver-other", "~unrelated.test")
	if output := runResolverCommand(t, "resolvectl", "query", "sensor"); !strings.Contains(output, "10.39.7.99") {
		t.Fatalf("query after resolved restart: %s", output)
	}
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverRuntimeNetwork(t, networkDir, false, true, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-dns-remove")
	waitResolverRuntimeValues(t, daemon, baselineDNS, wantedDomains)
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverRuntimeNetwork(t, networkDir, true, false, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-domains-remove")
	waitResolverRuntimeValues(t, daemon, wantedDNS, baselineDomains)
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverRuntimeNetwork(t, networkDir, false, false, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-remove")
	waitResolverRuntimeValues(t, daemon, baselineDNS, baselineDomains)
	actualDNS, actualDomains := resolverRuntimeValues(t, "resolver-other")
	if !reflect.DeepEqual(actualDNS, otherDNS) || !reflect.DeepEqual(actualDomains, otherDomains) {
		t.Fatalf("unrelated settings changed: %+v %+v", actualDNS, actualDomains)
	}
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverRuntimeNetwork(t, networkDir, false, true, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-external-control")
	waitResolverRuntimeValues(t, daemon, baselineDNS, wantedDomains)
	killOwnedRuntimeDaemon(t, daemon)
	runResolverCommand(t, "resolvectl", "domain", "owned397", "~external.test")
	_, externalDomains := resolverRuntimeValues(t, "owned397")
	writeResolverRuntimeNetwork(t, networkDir, false, false, false)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-external-preserve")
	waitResolverRuntimeValues(t, daemon, baselineDNS, externalDomains)
	waitStaticRuntimeLog(t, daemon, "external resolver change preserved")
	killOwnedRuntimeDaemon(t, daemon)
	writeResolverCleanupNetwork(t, networkDir)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-prune")
	waitOwnedRuntimeAbsent(t, daemon, "owned397", 10*time.Second)
	waitStaticRuntimeLog(t, daemon, `"phase":"initial-reconcile","module":"resolver"`)
	killOwnedRuntimeDaemon(t, daemon)
	cleanupConfig := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"200ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.resolver]\nstate_file = %q\n[wanconfig]\npublish = true\n", filepath.Join(root, "resolver.json"))
	if err := os.WriteFile(configPath, []byte(cleanupConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-cleanup-only")
	waitStaticRuntimeLog(t, daemon, `"phase":"initial-reconcile","module":"resolver"`)
	for _, name := range []string{"resolver-bootstrap", "resolver-first", "resolver-ipv6-only", "resolver-restart", "resolver-dns-remove", "resolver-domains-remove", "resolver-remove", "resolver-external-control", "resolver-external-preserve", "resolver-prune", "resolver-cleanup-only"} {
		data, err := os.ReadFile(filepath.Join(root, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s daemon log:\n%s", name, data)
	}
}

func writeResolverCleanupNetwork(t *testing.T, directory string) {
	t.Helper()
	writeResolverRuntimeNetwork(t, directory, false, false, false)
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
	configured := entries[:0]
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name != "owned397" {
			configured = append(configured, entry)
		}
	}
	interfaces["interface"], err = json.Marshal(configured)
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

func runResolverCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, output)
	}
	return string(output)
}

func resolverRuntimeValues(t *testing.T, name string) ([]resolved.DNS, []resolved.Domain) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := dbus.SystemBusPrivate()
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := bus.Auth(nil); err != nil {
		t.Fatal(err)
	}
	if err := bus.Hello(); err != nil {
		t.Fatal(err)
	}
	var path dbus.ObjectPath
	if err := bus.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").Call("org.freedesktop.resolve1.Manager.GetLink", 0, int32(link.Attrs().Index)).Store(&path); err != nil {
		t.Fatal(err)
	}
	object := bus.Object("org.freedesktop.resolve1", path)
	dnsProperty, err := object.GetProperty("org.freedesktop.resolve1.Link.DNSEx")
	if err != nil {
		t.Fatal(err)
	}
	domainProperty, err := object.GetProperty("org.freedesktop.resolve1.Link.Domains")
	if err != nil {
		t.Fatal(err)
	}
	servers := []resolved.DNS{}
	domains := []resolved.Domain{}
	if err := dnsProperty.Store(&servers); err != nil {
		t.Fatal(err)
	}
	if err := domainProperty.Store(&domains); err != nil {
		t.Fatal(err)
	}
	return servers, domains
}

func waitResolverRuntimeValues(t *testing.T, daemon *runtimeDaemon, dns []resolved.DNS, domains []resolved.Domain) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		actualDNS, actualDomains := resolverRuntimeValues(t, "owned397")
		if reflect.DeepEqual(actualDNS, dns) && reflect.DeepEqual(actualDomains, domains) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	actualDNS, actualDomains := resolverRuntimeValues(t, "owned397")
	t.Fatalf("resolved values differ: DNS=%+v domains=%+v; expected DNS=%+v domains=%+v; %s", actualDNS, actualDomains, dns, domains, runtimeLogTail(t, daemon, 40))
}

func writeResolverRuntimeNetwork(t *testing.T, directory string, dnsEnabled, domainsEnabled, ipv6Only bool) {
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
	configured := entries[:0]
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name == "owned-br" || name == "late397" || name == "latephys0" || name == "enwebpass0" || name == "enmbrains0" {
			continue
		}
		configured = append(configured, entry)
		if name != "owned397" {
			continue
		}
		entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"mwan"`)
		entry["enabled"] = json.RawMessage(`true`)
		servers := []string{}
		search4, search6 := []string{}, []string{}
		if dnsEnabled && !ipv6Only {
			servers = append(servers, "10.39.7.2")
		}
		if domainsEnabled {
			search4 = append(search4, "lab.test")
			search6 = append(search6, "lab.test", "v6.test")
		}
		resolver4, err := json.Marshal(struct {
			DNS    []string `json:"dns"`
			Search []string `json:"search"`
		}{DNS: servers, Search: search4})
		if err != nil {
			t.Fatal(err)
		}
		servers6 := []string{}
		if dnsEnabled {
			servers6 = append(servers6, "fd39:7::2")
		}
		resolver6, err := json.Marshal(struct {
			DNS    []string `json:"dns"`
			Search []string `json:"search"`
		}{DNS: servers6, Search: search6})
		if err != nil {
			t.Fatal(err)
		}
		entry["ietf-ip:ipv4"] = json.RawMessage(fmt.Sprintf(`{"address":[{"ip":"10.39.7.1","prefix-length":24}],"goodkind-mwan-steering:resolver":%s}`, resolver4))
		entry["ietf-ip:ipv6"] = json.RawMessage(fmt.Sprintf(`{"address":[{"ip":"fd39:7::1","prefix-length":64}],"goodkind-mwan-steering:resolver":%s}`, resolver6))
		if !dnsEnabled && !domainsEnabled {
			entry["ietf-ip:ipv4"] = json.RawMessage(`{"address":[{"ip":"10.39.7.1","prefix-length":24}]}`)
			entry["ietf-ip:ipv6"] = json.RawMessage(`{"address":[{"ip":"fd39:7::1","prefix-length":64}]}`)
		}
	}
	interfaces["interface"], err = json.Marshal(configured)
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

func startResolverAuthority(t *testing.T) *atomic.Int64 {
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
			queries.Add(1)
			message.Header.Response = true
			message.Header.Authoritative = true
			for _, question := range message.Questions {
				t.Logf("authoritative DNS query: name=%s type=%s source=%s", question.Name.String(), question.Type.String(), peer.String())
				if question.Name.String() == "sensor.lab.test." && question.Type == dnsmessage.TypeA {
					message.Answers = append(message.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1, Length: 0}, Body: &dnsmessage.AResource{A: [4]byte{10, 39, 7, 99}}})
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
