//go:build linux && systemd

package commandline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/resolved"
	"goodkind.io/mwan/gateway/internal/yangpub"
)

func TestStaticResolverDaemonRuntime(t *testing.T) {
	RequireSystemd(t)
	requireSystemdOutput(t, "systemctl", "is-active", "systemd-resolved", "dbus")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	var peerLinks []string
	defer func() { removeSystemdPeerLinks(t, gateway, peerLinks) }()
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
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
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
	management := newRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"resolver-mgmt",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	peerLinks = append(peerLinks, "enmgmt0")
	defer management.namespace.Close()
	lan := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"resolver-lan",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	peerLinks = append(peerLinks, "enmwanbr0")
	defer lan.namespace.Close()
	peer := newRuntimePeer(t, gateway, "ownphys0", "resolver-dns", nil, nil, ownedRuntimeParentMAC)
	peerLinks = append(peerLinks, "ownphys0")
	defer peer.namespace.Close()
	setRuntimeNamespace(t, peer.namespace)
	parent, err := netlink.LinkByName("resolver-dns")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: "resolver397", ParentIndex: parent.Attrs().Index},
		VlanId:    397,
	}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(
		t,
		"resolver397",
		[]string{"10.39.7.2/24", "10.39.7.53/24", "10.39.7.54/24", "fd39:7::2/64"},
	)
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
	requireSystemdOutput(t, "resolvectl", "dns", "resolver-other", "198.51.100.53")
	requireSystemdOutput(t, "resolvectl", "domain", "resolver-other", "~unrelated.test")
	otherDNS, otherDomains := resolverRuntimeValues(t, "resolver-other")
	binary := protocolTestBinary(t)
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"200ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[ifmgr.modules.resolver]\nstate_file = %q\n[wanconfig]\npublish = true\n",
		filepath.Join(root, "links.json"),
		filepath.Join(root, "addresses.json"),
		filepath.Join(root, "kernel-policy.json"),
		filepath.Join(root, "resolver.json"),
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	leaseDirectory := filepath.Join(root, "leases")
	if err := os.Mkdir(leaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	config = strings.Replace(
		config,
		"[ifmgr.iface.enmwanbr0]",
		fmt.Sprintf("lease_directory = %q\n[ifmgr.iface.enmwanbr0]", leaseDirectory),
		1,
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	withPrivateRepositoryForTest(
		t,
		ctx,
		slog.Default(),
		commandopts.Selftest{
			Repository: filepath.Join(root, "repository"),
			ModelsDir:  selftestModelsDir(t),
		},
		func(reader yangpub.Publisher) {
			read := func() string {
				value, found, err := reader.ExportJSON(
					ctx,
					yangpub.DatastoreOperational,
					"/ietf-interfaces:*",
				)
				if err != nil || !found {
					return ""
				}
				return value
			}
			wantedDNS := []resolved.DNS{
				{Family: 2, Address: []byte{10, 39, 7, 2}, Port: 0, ServerName: ""},
				{Family: 10, Address: net.ParseIP("fd39:7::2").To16(), Port: 0, ServerName: ""},
			}
			wantedDomains := []resolved.Domain{
				{Name: "lab.test", RoutingOnly: false},
				{Name: "v6.test", RoutingOnly: false},
			}
			writeResolverRuntimeNetwork(t, networkDir, false, false, false)
			daemon := startRuntimeDaemon(t, binary, configPath, root, "resolver-bootstrap")
			defer func() { killOwnedRuntimeDaemon(t, daemon) }()
			waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
			waitStaticRuntimeAddress(t, daemon, "owned397", "fd39:7::1/64", true)
			killOwnedRuntimeDaemon(t, daemon)
			requireSystemdOutput(
				t,
				"resolvectl",
				"dns",
				"owned397",
				"192.0.2.53:5300#baseline.example.test",
			)
			requireSystemdOutput(
				t,
				"resolvectl",
				"domain",
				"owned397",
				"baseline.test",
				"~route.test",
			)
			baselineDNS, baselineDomains := resolverRuntimeValues(t, "owned397")
			if len(baselineDNS) != 1 || baselineDNS[0].Port != 5300 ||
				baselineDNS[0].ServerName != "baseline.example.test" ||
				len(baselineDomains) != 2 ||
				!baselineDomains[1].RoutingOnly {
				t.Fatalf(
					"resolved baseline lacks extended server/domain values: %+v %+v",
					baselineDNS,
					baselineDomains,
				)
			}
			writeResolverRuntimeNetwork(t, networkDir, true, true, false)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-first")
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			killOwnedRuntimeDaemon(t, daemon)
			writeAcquiredResolverRuntimeNetwork(t, networkDir)
			setRuntimeNamespace(t, peer.namespace)
			waitAutoconfigurationLinkLocal(t, "resolver397")
			stopDHCP := startResolverDHCPv6Kea(t, root)
			stopRA := startResolverRouter(t)
			setRuntimeNamespace(t, gateway)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-acquired")
			acquiredDaemon := daemon
			t.Cleanup(func() {
				if !t.Failed() {
					return
				}
				for _, line := range strings.Split(runtimeDaemonLog(t, acquiredDaemon), "\n") {
					if strings.Contains(strings.ToLower(line), "dhcpv6") {
						t.Log(line)
					}
				}
				content, err := os.ReadFile(filepath.Join(root, "resolver-kea.log"))
				t.Logf("resolver Kea: %s (%v)", content, err)
			})
			acquiredDNS := append(
				append([]resolved.DNS(nil), wantedDNS...),
				resolved.DNS{
					Family:     10,
					Address:    net.ParseIP("fd39:7::53").To16(),
					Port:       0,
					ServerName: "",
				},
				resolved.DNS{
					Family:     10,
					Address:    net.ParseIP("fd39:7::54").To16(),
					Port:       0,
					ServerName: "",
				},
			)
			waitResolverRuntimeValues(t, daemon, acquiredDNS, wantedDomains)
			t.Logf("resolved acquired DHCPv6 and RA DNS with configured servers: %+v", acquiredDNS)
			stopRA()
			stopDHCP()
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			t.Logf(
				"resolved removed expired DHCPv6 and RA DNS and retained configured servers: %+v",
				wantedDNS,
			)
			actualOtherDNS, actualOtherDomains := resolverRuntimeValues(t, "resolver-other")
			if !reflect.DeepEqual(actualOtherDNS, otherDNS) ||
				!reflect.DeepEqual(actualOtherDomains, otherDomains) {
				t.Fatalf(
					"acquired DNS changed unrelated settings: %+v %+v",
					actualOtherDNS,
					actualOtherDomains,
				)
			}
			killOwnedRuntimeDaemon(t, daemon)
			writeDHCPv4ResolverRuntimeNetwork(t, networkDir, false, true)
			setRuntimeNamespace(t, peer.namespace)
			stopDHCP4 := startResolverDHCPv4Kea(t, root, "10.39.7.2")
			setRuntimeNamespace(t, gateway)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-dhcpv4")
			dns4 := []resolved.DNS{
				{Family: 2, Address: []byte{10, 39, 7, 2}, Port: 0, ServerName: ""},
			}
			waitResolverRuntimeValues(t, daemon, dns4, baselineDomains)
			requireSystemdOutput(t, "resolvectl", "flush-caches")
			beforeDHCP4Query := queries.Load()
			if output := requireSystemdOutput(
				t,
				"resolvectl",
				"query",
				"--interface=owned397",
				"sensor.route.test",
			); !strings.Contains(output, "10.39.7.99") ||
				queries.Load() <= beforeDHCP4Query {
				t.Fatalf(
					"acquired DHCPv4 DNS query: %s; authoritative queries before=%d after=%d",
					output,
					beforeDHCP4Query,
					queries.Load(),
				)
			}
			stopDHCP4()
			setRuntimeNamespace(t, peer.namespace)
			stopDHCP4 = startResolverDHCPv4Kea(t, root, "")
			setRuntimeNamespace(t, gateway)
			waitResolverRuntimeValues(t, daemon, baselineDNS, baselineDomains)
			stopDHCP4()
			setRuntimeNamespace(t, peer.namespace)
			stopDHCP4 = startResolverDHCPv4Kea(t, root, "10.39.7.54")
			setRuntimeNamespace(t, gateway)
			dns4 = []resolved.DNS{
				{Family: 2, Address: []byte{10, 39, 7, 54}, Port: 0, ServerName: ""},
			}
			waitResolverRuntimeValues(t, daemon, dns4, baselineDomains)
			t.Log("DHCPv4 renewal replaced an absent DNS option with 10.39.7.54")
			killOwnedRuntimeDaemon(t, daemon)
			writeDHCPv4ResolverRuntimeNetwork(t, networkDir, true, true)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-dhcpv4-renewed")
			dns4 = append(
				append([]resolved.DNS(nil), wantedDNS...),
				resolved.DNS{Family: 2, Address: []byte{10, 39, 7, 54}, Port: 0, ServerName: ""},
			)
			waitResolverRuntimeValues(t, daemon, dns4, wantedDomains)
			killOwnedRuntimeDaemon(t, daemon)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-dhcpv4-recovered")
			waitStaticRuntimeLog(t, daemon, `state=BOUND`)
			waitResolverRuntimeValues(t, daemon, dns4, wantedDomains)
			stopDHCP4()
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			killOwnedRuntimeDaemon(t, daemon)
			writeDHCPv4ResolverRuntimeNetwork(t, networkDir, true, false)
			setRuntimeNamespace(t, peer.namespace)
			stopDHCP4 = startResolverDHCPv4Kea(t, root, "10.39.7.53")
			setRuntimeNamespace(t, gateway)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-dhcpv4-disabled")
			waitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.100/24", true)
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			actualOtherDNS, actualOtherDomains = resolverRuntimeValues(t, "resolver-other")
			if !reflect.DeepEqual(actualOtherDNS, otherDNS) ||
				!reflect.DeepEqual(actualOtherDomains, otherDomains) {
				t.Fatalf(
					"DHCPv4 DNS changed unrelated settings: %+v %+v",
					actualOtherDNS,
					actualOtherDomains,
				)
			}
			stopDHCP4()
			killOwnedRuntimeDaemon(t, daemon)
			writeResolverRuntimeNetwork(t, networkDir, true, true, false)
			daemon = startRuntimeDaemon(
				t,
				binary,
				configPath,
				root,
				"resolver-static-after-acquired",
			)
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			waitRuntimeOwnershipRead(t, daemon, read, `"search"`)
			if output := requireSystemdOutput(
				t,
				"resolvectl",
				"query",
				"sensor",
			); !strings.Contains(output, "10.39.7.99") ||
				queries.Load() == 0 {
				t.Fatalf(
					"single-label DNS query: %s; authoritative queries=%d",
					output,
					queries.Load(),
				)
			}
			initialOtherDNS, initialOtherDomains := resolverRuntimeValues(t, "resolver-other")
			if !reflect.DeepEqual(initialOtherDNS, otherDNS) ||
				!reflect.DeepEqual(initialOtherDomains, otherDomains) {
				t.Fatalf(
					"initial apply changed unrelated settings: %+v %+v",
					initialOtherDNS,
					initialOtherDomains,
				)
			}
			killOwnedRuntimeDaemon(t, daemon)
			writeResolverRuntimeNetwork(t, networkDir, true, true, true)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-ipv6-only")
			waitResolverRuntimeValues(t, daemon, wantedDNS[1:], wantedDomains)
			requireSystemdOutput(t, "resolvectl", "flush-caches")
			beforeIPv6Query := queries.Load()
			if output := requireSystemdOutput(
				t,
				"resolvectl",
				"query",
				"sensor",
			); !strings.Contains(output, "10.39.7.99") ||
				queries.Load() <= beforeIPv6Query {
				t.Fatalf(
					"IPv6-only DNS query: %s; authoritative queries before=%d after=%d",
					output,
					beforeIPv6Query,
					queries.Load(),
				)
			}
			killOwnedRuntimeDaemon(t, daemon)
			writeResolverRuntimeNetwork(t, networkDir, true, true, false)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-restart")
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			resolvedMasked := true
			t.Cleanup(func() {
				if resolvedMasked {
					requireSystemdOutput(t, "systemctl", "unmask", "--runtime", "systemd-resolved")
					requireSystemdOutput(t, "systemctl", "start", "systemd-resolved")
				}
			})
			requireSystemdOutput(t, "systemctl", "mask", "--runtime", "--now", "systemd-resolved")
			waitResolverRuntimeFailureLogs(t, daemon)
			requireSystemdOutput(t, "systemctl", "unmask", "--runtime", "systemd-resolved")
			requireSystemdOutput(t, "systemctl", "start", "systemd-resolved")
			resolvedMasked = false
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			requireSystemdOutput(t, "systemctl", "restart", "systemd-resolved")
			waitResolverRuntimeValues(t, daemon, wantedDNS, wantedDomains)
			// Restore unrelated resolver settings after restarting systemd-resolved.
			requireSystemdOutput(t, "resolvectl", "dns", "resolver-other", "198.51.100.53")
			requireSystemdOutput(t, "resolvectl", "domain", "resolver-other", "~unrelated.test")
			if output := requireSystemdOutput(
				t,
				"resolvectl",
				"query",
				"sensor",
			); !strings.Contains(output, "10.39.7.99") {
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
			if !reflect.DeepEqual(actualDNS, otherDNS) ||
				!reflect.DeepEqual(actualDomains, otherDomains) {
				t.Fatalf("unrelated settings changed: %+v %+v", actualDNS, actualDomains)
			}
			killOwnedRuntimeDaemon(t, daemon)
			writeResolverRuntimeNetwork(t, networkDir, false, true, false)
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-external-control")
			waitResolverRuntimeValues(t, daemon, baselineDNS, wantedDomains)
			killOwnedRuntimeDaemon(t, daemon)
			requireSystemdOutput(t, "resolvectl", "domain", "owned397", "~external.test")
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
			cleanupConfig := fmt.Sprintf(
				"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"200ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.resolver]\nstate_file = %q\n[wanconfig]\npublish = true\n",
				filepath.Join(root, "resolver.json"),
			)
			if err := os.WriteFile(configPath, []byte(cleanupConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-cleanup-only")
			waitStaticRuntimeLog(t, daemon, `"phase":"initial-reconcile","module":"resolver"`)
			killOwnedRuntimeDaemon(t, daemon)
			emptyJournal := filepath.Join(root, "resolver-empty.json")
			if err := os.WriteFile(
				configPath,
				[]byte(
					strings.Replace(
						cleanupConfig,
						filepath.Join(root, "resolver.json"),
						emptyJournal,
						1,
					),
				),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			daemon = startRuntimeDaemon(t, binary, configPath, root, "resolver-empty-cleanup")
			waitStaticRuntimeLog(t, daemon, `"phase":"periodic-reconcile","module":"resolver"`)
			killOwnedRuntimeDaemon(t, daemon)
			if _, err := os.Stat(emptyJournal); !os.IsNotExist(err) {
				t.Errorf("empty cleanup created a resolver journal: %v", err)
			}
			for _, name := range []string{
				"resolver-bootstrap",
				"resolver-first",
				"resolver-acquired",
				"resolver-dhcpv4",
				"resolver-dhcpv4-renewed",
				"resolver-dhcpv4-recovered",
				"resolver-dhcpv4-disabled",
				"resolver-static-after-acquired",
				"resolver-ipv6-only",
				"resolver-restart",
				"resolver-dns-remove",
				"resolver-domains-remove",
				"resolver-remove",
				"resolver-external-control",
				"resolver-external-preserve",
				"resolver-prune",
				"resolver-cleanup-only",
				"resolver-empty-cleanup",
			} {
				data, err := os.ReadFile(filepath.Join(root, name+".log"))
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("%s daemon log:\n%s", name, data)
			}
		},
	)
}

func writeAcquiredResolverRuntimeNetwork(t *testing.T, directory string) {
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
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name != "owned397" {
			continue
		}
		entry["goodkind-mwan-steering:wan"] = json.RawMessage(
			`{"name":"resolver","table-id":101,"fw-mark":2,"fw-mark-prio":200,"from-prio":60}`,
		)
		entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":0,"weight":1}`)
		var ipv4 map[string]json.RawMessage
		if err := json.Unmarshal(entry["ietf-ip:ipv4"], &ipv4); err != nil {
			t.Fatal(err)
		}
		ipv4["goodkind-mwan-steering:translation"] = json.RawMessage(`{"mode":"native"}`)
		entry["ietf-ip:ipv4"], err = json.Marshal(ipv4)
		if err != nil {
			t.Fatal(err)
		}
		var family map[string]json.RawMessage
		if err := json.Unmarshal(entry["ietf-ip:ipv6"], &family); err != nil {
			t.Fatal(err)
		}
		family["goodkind-mwan-steering:accept-ra"] = json.RawMessage(`true`)
		family["goodkind-mwan-steering:use-ra-dns"] = json.RawMessage(`true`)
		family["goodkind-mwan-steering:dhcp"] = json.RawMessage(`true`)
		family["goodkind-mwan-steering:translation"] = json.RawMessage(`{"mode":"native"}`)
		family["goodkind-mwan-steering:dhcpv6-client"] = json.RawMessage(
			`{"duid":"00:01:00:01:2a:5b:3c:4d:02:00:5e:00:53:01","prefix-iaid":397,"request-prefix":true,"without-ra":"solicit","use-dns":true}`,
		)
		entry["ietf-ip:ipv6"], err = json.Marshal(family)
		if err != nil {
			t.Fatal(err)
		}
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

func writeDHCPv4ResolverRuntimeNetwork(t *testing.T, directory string, staticDNS, useDNS bool) {
	t.Helper()
	writeResolverRuntimeNetwork(t, directory, staticDNS, staticDNS, false)
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
			continue
		}
		var family map[string]json.RawMessage
		if err := json.Unmarshal(entry["ietf-ip:ipv4"], &family); err != nil {
			t.Fatal(err)
		}
		family["goodkind-mwan-steering:dhcp"] = json.RawMessage(`true`)
		family["goodkind-mwan-steering:dhcpv4"] = json.RawMessage(
			fmt.Sprintf(`{"client-id":"hex:01aabb","use-dns":%t,"use-routes":true}`, useDNS),
		)
		family["goodkind-mwan-steering:route-metric"] = json.RawMessage(`100`)
		entry["ietf-ip:ipv4"], err = json.Marshal(family)
		if err != nil {
			t.Fatal(err)
		}
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

func startResolverDHCPv4Kea(t *testing.T, root, server string) func() {
	t.Helper()
	configPath := filepath.Join(root, "resolver-kea4.conf")
	options := `{"name":"routers","data":"10.39.7.2"}`
	if server != "" {
		options += fmt.Sprintf(`,{"name":"domain-name-servers","data":%q}`, server)
	}
	config := fmt.Sprintf(
		`{"Dhcp4":{"interfaces-config":{"interfaces":["resolver397"]},"lease-database":{"type":"memfile","persist":false},"valid-lifetime":6,"renew-timer":2,"rebind-timer":4,"subnet4":[{"id":1,"subnet":"10.39.7.0/24","interface":"resolver397","pools":[{"pool":"10.39.7.100-10.39.7.100"}],"option-data":[%s]}]}}`,
		options,
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, fmt.Sprintf("resolver-kea4-%s.log", server))
	return startResolverKea(t, "kea-dhcp4", configPath, logPath, "DHCP4_STARTED")
}

func startResolverDHCPv6Kea(t *testing.T, root string) func() {
	t.Helper()
	configPath := filepath.Join(root, "resolver-kea.conf")
	config := `{"Dhcp6":{"interfaces-config":{"interfaces":["resolver397"]},"lease-database":{"type":"memfile","persist":false},"valid-lifetime":8,"preferred-lifetime":4,"subnet6":[{"id":1,"subnet":"fd39:7::/64","interface":"resolver397","pd-pools":[{"prefix":"2001:db8:397::","prefix-len":56,"delegated-len":56}],"option-data":[{"name":"dns-servers","data":"fd39:7::53"}]}]}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "resolver-kea.log")
	return startResolverKea(t, "kea-dhcp6", configPath, logPath, "DHCP6_STARTED")
}

func startResolverKea(t *testing.T, name, configPath, logPath, ready string) func() {
	t.Helper()
	service := startProtocolService(t, name, logPath, "-c", configPath, "-d")
	stop := sync.OnceFunc(func() { stopProtocolService(t, service) })
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(content), ready) {
			return stop
		}
		time.Sleep(25 * time.Millisecond)
	}
	content, _ := os.ReadFile(logPath)
	t.Fatalf("resolver %s did not start: %s", name, content)
	return stop
}

func startResolverRouter(t *testing.T) func() {
	t.Helper()
	link, err := net.InterfaceByName("resolver397")
	if err != nil {
		t.Fatal(err)
	}
	connection, _, err := ndp.Listen(link, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	advertisement := &ndp.RouterAdvertisement{
		Options: []ndp.Option{
			&ndp.RecursiveDNSServer{
				Lifetime: 3 * time.Second,
				Servers:  []netip.Addr{netip.MustParseAddr("fd39:7::54")},
			},
		},
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := connection.WriteTo(
					advertisement,
					nil,
					netip.MustParseAddr("ff02::1"),
				); err != nil {
					t.Errorf("write resolver RA: %v", err)
					return
				}
			}
		}
	}()
	var once sync.Once
	finish := func() {
		once.Do(func() { close(stop); <-done; connection.Close() })
	}
	t.Cleanup(finish)
	return finish
}

func waitResolverRuntimeFailureLogs(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		operationFound := false
		applicationFound := false
		finalFound := false
		for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
			if strings.Contains(line, "WARN resolver operation failed") {
				if !strings.HasSuffix(
					line,
					`WARN resolver operation failed operation="get resolved link" result=failed`,
				) {
					t.Fatalf(
						"resolver operation event repeats the error or lacks its outcome: %s",
						line,
					)
				}
				operationFound = true
			}
			var event struct {
				Message   string          `json:"msg"`
				Level     string          `json:"level"`
				Operation string          `json:"operation"`
				Result    string          `json:"result"`
				Module    string          `json:"module"`
				TraceID   string          `json:"trace_id"`
				Err       json.RawMessage `json:"err"`
				Error     json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(line), &event) != nil {
				continue
			}
			if event.Message == "resolver operation failed" {
				if event.Level != "WARN" || event.Operation != "get resolved link" ||
					event.Result != "failed" ||
					len(event.Err) != 0 ||
					len(event.Error) != 0 {
					t.Fatalf(
						"resolver operation event repeats the error or lacks its outcome: %s",
						line,
					)
				}
				operationFound = true
			}
			if event.Message == "resolver application failed" {
				if event.Level != "WARN" || event.Module != "resolver" || event.Operation == "" ||
					event.Result != "failed" ||
					len(event.Err) != 0 ||
					len(event.Error) != 0 {
					t.Fatalf(
						"resolver application event repeats the error or lacks its outcome: %s",
						line,
					)
				}
				applicationFound = true
			}
			if event.Message == "ifmgr: module Reconcile failed" && event.Module == "resolver" {
				var diagnostic string
				if err := json.Unmarshal(
					event.Err,
					&diagnostic,
				); err != nil || event.TraceID == "" || !strings.Contains(
					diagnostic,
					"connection ",
				) ||
					!strings.Contains(diagnostic, "get resolved link") {
					t.Fatalf(
						"resolver final diagnostic lacks connection, operation, or trace context: %s",
						line,
					)
				}
				finalFound = true
			}
			if event.Message == "resolver: reconcile failed" ||
				event.Message == "resolved: link apply failed" ||
				event.Message == "resolved: reconcile resolver fields failed" {
				t.Fatalf("resolver emitted an intermediate duplicate diagnostic: %s", line)
			}
		}
		if operationFound && applicationFound && finalFound {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"resolver dependency failure lacks operation and final diagnostics: %s",
		runtimeLogTail(t, daemon, 40),
	)
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

func resolverRuntimeValues(t *testing.T, name string) ([]resolved.DNS, []resolved.Domain) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	const (
		service       = "org.freedesktop.resolve1"
		linkInterface = service + ".Link"
		getProperty   = "org.freedesktop.DBus.Properties.Get"
	)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	bus, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	var path dbus.ObjectPath
	if err := bus.Object(service, "/org/freedesktop/resolve1").CallWithContext(
		ctx,
		service+".Manager.GetLink",
		0,
		int32(link.Attrs().Index),
	).Store(
		&path,
	); err != nil {
		t.Fatal(err)
	}
	object := bus.Object(service, path)
	var dnsProperty, domainProperty dbus.Variant
	if err := object.CallWithContext(ctx, getProperty, 0, linkInterface, "DNSEx").Store(
		&dnsProperty,
	); err != nil {
		t.Fatal(err)
	}
	if err := object.CallWithContext(ctx, getProperty, 0, linkInterface, "Domains").Store(
		&domainProperty,
	); err != nil {
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

func waitResolverRuntimeValues(
	t *testing.T,
	daemon *runtimeDaemon,
	dns []resolved.DNS,
	domains []resolved.Domain,
) {
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
	t.Fatalf(
		"resolved values differ: DNS=%+v domains=%+v; expected DNS=%+v domains=%+v; %s",
		actualDNS,
		actualDomains,
		dns,
		domains,
		runtimeLogTail(t, daemon, 40),
	)
}

func writeResolverRuntimeNetwork(
	t *testing.T,
	directory string,
	dnsEnabled bool,
	domainsEnabled bool,
	ipv6Only bool,
) {
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
		if name == "owned-br" || name == "late397" || name == "latephys0" || name == "enwebpass0" ||
			name == "enmbrains0" {
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
		resolver4, err := json.Marshal(
			struct {
				DNS    []string `json:"dns"`
				Search []string `json:"search"`
			}{
				DNS:    servers,
				Search: search4,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		servers6 := []string{}
		if dnsEnabled {
			servers6 = append(servers6, "fd39:7::2")
		}
		resolver6, err := json.Marshal(
			struct {
				DNS    []string `json:"dns"`
				Search []string `json:"search"`
			}{
				DNS:    servers6,
				Search: search6,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		entry["ietf-ip:ipv4"] = json.RawMessage(
			fmt.Sprintf(
				`{"address":[{"ip":"10.39.7.1","prefix-length":24}],"goodkind-mwan-steering:resolver":%s}`,
				resolver4,
			),
		)
		entry["ietf-ip:ipv6"] = json.RawMessage(
			fmt.Sprintf(
				`{"address":[{"ip":"fd39:7::1","prefix-length":64}],"goodkind-mwan-steering:resolver":%s}`,
				resolver6,
			),
		)
		if !dnsEnabled && !domainsEnabled {
			entry["ietf-ip:ipv4"] = json.RawMessage(
				`{"address":[{"ip":"10.39.7.1","prefix-length":24}]}`,
			)
			entry["ietf-ip:ipv6"] = json.RawMessage(
				`{"address":[{"ip":"fd39:7::1","prefix-length":64}]}`,
			)
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
				t.Logf(
					"authoritative DNS query: name=%s type=%s source=%s",
					question.Name.String(),
					question.Type.String(),
					peer.String(),
				)
				if (question.Name.String() == "sensor.lab.test." || question.Name.String() == "sensor.route.test.") &&
					question.Type == dnsmessage.TypeA {
					message.Answers = append(
						message.Answers,
						dnsmessage.Resource{
							Header: dnsmessage.ResourceHeader{
								Name:   question.Name,
								Type:   dnsmessage.TypeA,
								Class:  dnsmessage.ClassINET,
								TTL:    1,
								Length: 0,
							},
							Body: &dnsmessage.AResource{A: [4]byte{10, 39, 7, 99}},
						},
					)
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
