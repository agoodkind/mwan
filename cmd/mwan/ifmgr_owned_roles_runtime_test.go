//go:build linux && firewallnetns

package main

import (
	"context"
	"crypto/sha256"
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
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/forwardingready"
	"goodkind.io/mwan/internal/resolved"
	"goodkind.io/mwan/internal/yangpub"
)

func TestOwnedRolesDaemonRuntime(t *testing.T) {
	if os.Getenv("MWAN_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires a dedicated container with real systemd-resolved and systemd PID 1")
	}
	if os.Getenv("MWAN_OWNED_ROLES_TEST_CHILD") == "1" {
		runOwnedRolesDaemonRuntime(t)
		return
	}
	// Repeated private repositories require separate sysrepo processes.
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedRolesDaemonRuntime$", "-test.v")
	child.Env = append(os.Environ(), "MWAN_OWNED_ROLES_TEST_CHILD=1")
	output, err := child.CombinedOutput()
	t.Logf("owned-role daemon acceptance:\n%s", output)
	if err != nil {
		t.Fatalf("owned-role daemon acceptance: %v", err)
	}
}

func runOwnedRolesDaemonRuntime(t *testing.T) {
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
	for source, target := range map[string]string{networkDir: "/etc/mwan", networkdDir: "/etc/systemd/network", schemaDir: "/usr/local/share/wanconfig/yang"} {
		bindStartupDirectory(t, source, target)
		t.Cleanup(func() {
			if err := unix.Unmount(target, 0); err != nil {
				t.Errorf("unmount %s: %v", target, err)
			}
		})
	}
	setupOwnedRoleUpdater(t, networkDir)
	sentinel := []byte("[Match]\nName=unrelated0\n[Network]\nAddress=198.51.100.1/24\n")
	if err := os.WriteFile(filepath.Join(networkdDir, "unrelated.network"), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	for _, path := range []string{"/proc/sys/net/ipv4/ip_forward", "/proc/sys/net/ipv6/conf/all/forwarding"} {
		if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upstream := newRuntimePeer(t, gateway, "ownphys0", "roles-upstream", nil, nil, ownedRuntimeParentMAC)
	defer upstream.namespace.Close()
	setRuntimeNamespace(t, upstream.namespace)
	addOwnedRolePeerVLAN(t, "roles-upstream", "roles-wan", 397, []string{"10.39.7.2/24", "fd39:7::2/64"})
	addOwnedRolePeerVLAN(t, "roles-upstream", "roles-mgmt", 396, []string{"fd39:6::2/64"})
	queries := startResolverAuthority(t)
	for _, specification := range []struct{ destination, gateway string }{{"192.0.2.0/29", "10.39.7.1"}, {"2001:db8:b01:fe::/64", "fd39:7::1"}, {"fd39:5::/64", "fd39:7::1"}} {
		_, destination, parseErr := net.ParseCIDR(specification.destination)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if err := netlink.RouteAdd(&netlink.Route{Dst: destination, Gw: net.ParseIP(specification.gateway)}); err != nil {
			t.Fatal(err)
		}
	}
	setRuntimeNamespace(t, gateway)
	unrelated := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "roles-other"}}
	if err := netlink.LinkAdd(unrelated); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netlink.LinkDel(unrelated); err != nil {
			t.Errorf("delete unrelated control link: %v", err)
		}
	})
	configureRuntimeLink(t, "roles-other", []string{"198.51.100.1/24"})
	runResolverCommand(t, "resolvectl", "dns", "roles-other", "198.51.100.53")
	runResolverCommand(t, "resolvectl", "domain", "roles-other", "~unrelated.test")
	otherDNS, otherDomains := resolverRuntimeValues(t, "roles-other")
	binary := protocolTestBinary(t)
	configPath := filepath.Join(root, "config.toml")
	socket := filepath.Join(root, "forwarding.sock")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"200ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[ifmgr.modules.resolver]\nstate_file = %q\n[wanconfig]\npublish = true\n[bgp]\nenabled = true\nuse_wanconfig = true\n[bgp.forwarding_readiness]\nsocket_path = %q\npoll_interval_milliseconds = 100\nread_timeout_milliseconds = 500\n", filepath.Join(root, "links.json"), filepath.Join(root, "addresses.json"), filepath.Join(root, "kernel.json"), filepath.Join(root, "resolver.json"), socket)
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
		value, found, exportErr := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if exportErr != nil || !found {
			return ""
		}
		return value
	}
	writeOwnedRoleNetwork(t, networkDir, nil)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "roles-missing-parent")
	defer func() { killOwnedRuntimeDaemon(t, daemon) }()
	defer func() {
		setRuntimeNamespace(t, gateway)
		if t.Failed() {
			t.Logf("public state: %s", read())
			var failures []string
			for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
				if strings.Contains(line, `"level":"WARN"`) || strings.Contains(line, `"level":"ERROR"`) {
					failures = append(failures, line)
				}
			}
			if len(failures) > 10 {
				failures = failures[len(failures)-10:]
			}
			t.Log(strings.Join(failures, "\n"))
		}
	}()
	waitStaticRuntimeAddress(t, daemon, "enmgmt0", "fd39:6::1/64", true)
	waitOwnedRoleProvider(t, daemon, read)
	waitOwnedRoleUpdater(t, daemon)
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{})
	waitOwnedRuntimeAbsent(t, daemon, "enmwanbr0", 10*time.Second)
	wantedDNS := []resolved.DNS{{Family: unix.AF_INET6, Address: net.ParseIP("fd39:6::2").To16(), Port: 0, ServerName: ""}}
	wantedDomains := []resolved.Domain{{Name: "lab.test", RoutingOnly: false}}
	actualDNS, actualDomains := resolverRuntimeValues(t, "enmgmt0")
	if !reflect.DeepEqual(actualDNS, wantedDNS) || !reflect.DeepEqual(actualDomains, wantedDomains) {
		t.Fatalf("management resolver: DNS=%+v domains=%+v", actualDNS, actualDomains)
	}
	if output := runResolverCommand(t, "resolvectl", "query", "sensor"); !strings.Contains(output, "10.39.7.99") || queries.Load() == 0 {
		t.Fatalf("management DNS query: %s; queries=%d", output, queries.Load())
	}
	assertStaticRuntimePacket(t, gateway, upstream.namespace, "udp6", "[fd39:6::2]:39601")
	transit := newRuntimePeer(t, gateway, "latephys0", "roles-down", nil, nil, ownedRuntimeLateMAC)
	defer transit.namespace.Close()
	setRuntimeNamespace(t, transit.namespace)
	addOwnedRolePeerVLAN(t, "roles-down", "roles-lan", 395, []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"})
	configureRuntimeLink(t, "lo", []string{"fd39:5::2/128"})
	for _, address := range []string{"192.0.2.1", "2001:db8:b01:fe::1"} {
		if err := netlink.RouteAdd(&netlink.Route{Gw: net.ParseIP(address)}); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(runResolverCommand(t, "ip", "-6", "route", "get", "fd39:7::2"))
	setRuntimeNamespace(t, gateway)
	waitStaticRuntimeAddress(t, daemon, "enmwanbr0", "192.0.2.1/29", true)
	waitStaticRuntimeAddress(t, daemon, "enmwanbr0", "2001:db8:b01:fe::1/64", true)
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: true})
	assertOwnedRolePackets(t, gateway, transit.namespace, upstream.namespace)
	route := waitConfiguredRuntimeRoute(t, daemon, "fd39:5::/64")
	if err := netlink.RouteDel(&route); err != nil {
		t.Fatal(err)
	}
	waitConfiguredRuntimeRoute(t, daemon, "fd39:5::/64")
	assertStaticRuntimePacket(t, gateway, transit.namespace, "udp6", "[fd39:5::2]:39505")
	setRuntimeNamespace(t, gateway)
	killOwnedRuntimeDaemon(t, daemon)
	enabled := true
	writeOwnedRoleNetwork(t, networkDir, &enabled)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "roles-restart")
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: true})
	for _, family := range []string{"ipv4", "ipv6"} {
		value, readErr := os.ReadFile("/proc/sys/net/" + family + "/conf/enmwanbr0/forwarding")
		if readErr != nil || string(value) != "1\n" {
			t.Fatalf("transit %s forwarding=%q err=%v", family, value, readErr)
		}
	}
	assertOwnedRolePackets(t, gateway, transit.namespace, upstream.namespace)
	killOwnedRuntimeDaemon(t, daemon)
	writeOwnedRoleNetwork(t, networkDir, &enabled)
	setReleaseConnectionField(t, networkDir, "owned397", "ietf-ip:ipv6", json.RawMessage(`{"address":[{"ip":"fd39:7::1","prefix-length":64}],"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:accept-ra":false,"goodkind-mwan-steering:gateway":"fd39:7::2","goodkind-mwan-steering:route-metric":398,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":"2001:db8:540::/60"}}}`))
	configureRuntimeLink(t, "owned397", []string{"2001:db8:540::1/128"})
	daemon = startRuntimeDaemon(t, binary, configPath, root, "roles-ipv6-failure")
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: false})
	assertStaticRuntimePacket(t, transit.namespace, upstream.namespace, "udp4", "10.39.7.2:39506")
	setRuntimeNamespace(t, gateway)
	assertStaticRuntimePacket(t, upstream.namespace, transit.namespace, "udp4", "192.0.2.2:39507")
	setRuntimeNamespace(t, gateway)
	killOwnedRuntimeDaemon(t, daemon)
	enabled = false
	writeOwnedRoleNetwork(t, networkDir, &enabled)
	daemon = startRuntimeDaemon(t, binary, configPath, root, "roles-forwarding-disabled")
	waitOwnedRoleProvider(t, daemon, read)
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{})
	for _, family := range []string{"ipv4", "ipv6"} {
		value, readErr := os.ReadFile("/proc/sys/net/" + family + "/conf/enmwanbr0/forwarding")
		if readErr != nil || string(value) != "0\n" {
			t.Fatalf("disabled transit %s forwarding=%q err=%v", family, value, readErr)
		}
	}
	files, err := os.ReadDir(networkdDir)
	if err != nil || len(files) != 1 || files[0].Name() != "unrelated.network" {
		t.Fatalf("owned-role networkd units: files=%v err=%v", files, err)
	}
	contents, err := os.ReadFile(filepath.Join(networkdDir, "unrelated.network"))
	if err != nil || string(contents) != string(sentinel) {
		t.Fatalf("unrelated unit changed: %q err=%v", contents, err)
	}
	actualDNS, actualDomains = resolverRuntimeValues(t, "roles-other")
	if !reflect.DeepEqual(actualDNS, otherDNS) || !reflect.DeepEqual(actualDomains, otherDomains) {
		t.Fatalf("unrelated resolver changed: DNS=%+v domains=%+v", actualDNS, actualDomains)
	}
	waitStaticRuntimeAddress(t, daemon, "roles-other", "198.51.100.1/24", true)
}

func addOwnedRolePeerVLAN(t *testing.T, parentName, name string, tag int, addresses []string) {
	t.Helper()
	parent, err := netlink.LinkByName(parentName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: name, ParentIndex: parent.Attrs().Index}, VlanId: tag}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, name, addresses)
}

func assertOwnedRolePackets(t *testing.T, gateway, transit, upstream netns.NsHandle) {
	t.Helper()
	assertStaticRuntimePacket(t, transit, upstream, "udp4", "10.39.7.2:39501")
	setRuntimeNamespace(t, gateway)
	assertStaticRuntimePacket(t, transit, upstream, "udp6", "[fd39:7::2]:39502")
	setRuntimeNamespace(t, gateway)
	assertStaticRuntimePacket(t, upstream, transit, "udp4", "192.0.2.2:39503")
	setRuntimeNamespace(t, gateway)
	// Provider ingress uses its marked table; this packet exercises the configured main-table route.
	assertStaticRuntimePacket(t, gateway, transit, "udp6", "[fd39:5::2]:39504")
	setRuntimeNamespace(t, gateway)
}

func waitOwnedRoleForwarding(t *testing.T, daemon *runtimeDaemon, socket string, expected forwardingready.State) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var state forwardingready.State
	var err error
	var stableAt time.Time
	for time.Now().Before(deadline) {
		state, err = forwardingready.Read(context.Background(), socket, 500*time.Millisecond)
		if err == nil && state == expected {
			if stableAt.IsZero() {
				stableAt = time.Now()
			}
			if time.Since(stableAt) >= time.Second {
				return
			}
		} else {
			stableAt = time.Time{}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("forwarding readiness=%+v expected=%+v err=%v; %s", state, expected, err, runtimeLogTail(t, daemon, 50))
}

func waitOwnedRoleProvider(t *testing.T, daemon *runtimeDaemon, read func() string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var document struct {
			Interfaces struct {
				Entries []struct {
					Name     string `json:"name"`
					Steering struct {
						State struct {
							Health string `json:"health"`
							Probe  []struct {
								Family string `json:"family"`
								Result string `json:"last-result"`
							} `json:"probe"`
						} `json:"state"`
					} `json:"goodkind-mwan-steering:steering"`
				} `json:"interface"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if json.Unmarshal([]byte(read()), &document) == nil {
			for _, entry := range document.Interfaces.Entries {
				if entry.Name != "owned397" || entry.Steering.State.Health != "healthy" {
					continue
				}
				passed := 0
				for _, probe := range entry.Steering.State.Probe {
					if probe.Result == "pass" && (probe.Family == "ipv4" || probe.Family == "ipv6") {
						passed++
					}
				}
				if passed == 2 {
					return
				}
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("healthy dual-family provider absent: %s; %s", read(), runtimeLogTail(t, daemon, 40))
}

func writeOwnedRoleNetwork(t *testing.T, directory string, forwarding *bool) {
	t.Helper()
	writeStaticRuntimeNetwork(t, directory, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
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
		if name != "ownphys0" && name != "latephys0" && name != "owned397" && name != "enmgmt0" && name != "enmwanbr0" {
			continue
		}
		if name == "owned397" {
			entry["goodkind-mwan-steering:wan"] = json.RawMessage(`{"name":"static","table-id":397,"fw-mark":4,"fw-mark-prio":397,"from-prio":97,"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":1,"recovery-threshold":1,"check-interval":1,"targets-v4":["10.39.7.2"],"targets-v6":["fd39:7::2"]}}`)
		}
		if name == "enmgmt0" || name == "enmwanbr0" {
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"mwan"`)
			entry["goodkind-mwan-steering:connection-id"] = json.RawMessage(fmt.Sprintf("%q", name))
			entry["enabled"] = json.RawMessage(`true`)
			parent, tag := "ownphys0", 396
			if name == "enmwanbr0" {
				parent, tag = "latephys0", 395
			}
			entry["goodkind-mwan-steering:link"] = json.RawMessage(fmt.Sprintf(`{"vlan":{"parent":%q,"id":%d}}`, parent, tag))
			if name == "enmgmt0" {
				entry["ietf-ip:ipv6"] = json.RawMessage(`{"address":[{"ip":"fd39:6::1","prefix-length":64}],"forwarding":false,"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:accept-ra":false,"goodkind-mwan-steering:resolver":{"dns":["fd39:6::2"],"search":["lab.test"]}}`)
			} else {
				forwardingField := ""
				if forwarding != nil {
					forwardingField = fmt.Sprintf(`,"forwarding":%t`, *forwarding)
				}
				entry["ietf-ip:ipv4"] = json.RawMessage(`{"address":[{"ip":"192.0.2.1","prefix-length":29}]` + forwardingField + `}`)
				entry["ietf-ip:ipv6"] = json.RawMessage(`{"address":[{"ip":"2001:db8:b01:fe::1","prefix-length":64}],"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:accept-ra":false,"goodkind-mwan-steering:route":[{"destination":"fd39:5::/64","gateway":"2001:db8:b01:fe::2"}]` + forwardingField + `}`)
			}
		}
		configured = append(configured, entry)
	}
	var group map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["goodkind-mwan-steering:steering-group"], &group); err != nil {
		t.Fatal(err)
	}
	group["firewall"] = json.RawMessage(`{"management-interface":"enmgmt0","management-service":[{"protocol":"tcp","port":22}],"pinned-provider":"static","pinned-source-v4":"192.0.2.1","pinned-source-port":51820,"pinned-destination-port":51821,"pinned-set-v4-name":"roles_pinned_v4","pinned-set-v6-name":"roles_pinned_v6"}`)
	interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(group)
	if err != nil {
		t.Fatal(err)
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

func setupOwnedRoleUpdater(t *testing.T, networkDir string) {
	t.Helper()
	bootstrap := os.Getenv("MWAN_OWNED_ROLE_BOOTSTRAP_DIR")
	if bootstrap == "" {
		t.Fatal("MWAN_OWNED_ROLE_BOOTSTRAP_DIR must contain the production Configs destination-refresh service and script")
	}
	service := "mwan-update-att-pinned-dests.service"
	t.Cleanup(func() { runResolverCommand(t, "systemctl", "daemon-reload") })
	for _, file := range []struct {
		name, destination string
		mode              os.FileMode
	}{
		{service, "/etc/systemd/system/" + service, 0o644},
		{"update-att-pinned-dests.sh", "/usr/local/bin/update-att-pinned-dests.sh", 0o755},
	} {
		if _, err := os.Stat(file.destination); !os.IsNotExist(err) {
			t.Fatalf("bootstrap destination must be absent: %s, %v", file.destination, err)
		}
		data, err := os.ReadFile(filepath.Join(bootstrap, file.name))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("production dependency %s sha256=%x", file.name, sha256.Sum256(data))
		if err := os.WriteFile(file.destination, data, file.mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(file.destination); err != nil {
				t.Errorf("remove production dependency %s: %v", file.destination, err)
			}
		})
	}
	configuration := "MWAN_PINNED_SET_V4_NAME=roles_pinned_v4\nMWAN_PINNED_SET_V6_NAME=roles_pinned_v6\nMWAN_ATT_PINNED_V4_SEED_CIDRS=198.51.100.0/24\nMWAN_ATT_PINNED_V6_SEED_CIDRS=2001:db8:100::/48\n"
	if err := os.WriteFile(filepath.Join(networkDir, "mwan.env"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	runResolverCommand(t, "systemctl", "daemon-reload")
	t.Cleanup(func() {
		runResolverCommand(t, "systemctl", "stop", service)
	})
}

func waitOwnedRoleUpdater(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var status, sets []byte
	for time.Now().Before(deadline) {
		status, _ = exec.Command("systemctl", "show", "mwan-update-att-pinned-dests.service", "--property=Result", "--property=ActiveState").CombinedOutput()
		sets, _ = exec.Command("nft", "list", "table", "inet", "mangle").CombinedOutput()
		if strings.Contains(string(status), "Result=success") && strings.Contains(string(status), "ActiveState=inactive") && strings.Contains(string(sets), "198.51.100.0/24") && strings.Contains(string(sets), "2001:db8:100::/48") {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("production destination refresh incomplete: status=%s sets=%s", status, sets)
}
