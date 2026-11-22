//go:build linux && protocol

package commandline

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
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/commandline/commandopts"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	dhcpv4RuntimeChildEnv  = "MWAN_DHCPV4_RUNTIME_TEST_CHILD"
	dhcpv4RuntimeBinaryEnv = "MWAN_DHCPV4_RUNTIME_TEST_BINARY"
)

func TestOwnedDHCPv4DaemonRuntime(t *testing.T) {
	testOwnedDHCPv4DaemonRuntime(t, false, false, false, false)
}

func TestOwnedDHCPv4ClasslessDaemonRuntime(t *testing.T) {
	testOwnedDHCPv4DaemonRuntime(t, true, false, false, false)
}

func TestOwnedDHCPv4RebindDaemonRuntime(t *testing.T) {
	testOwnedDHCPv4DaemonRuntime(t, true, true, false, false)
}

func TestOwnedDHCPv4NAKDaemonRuntime(t *testing.T) {
	testOwnedDHCPv4DaemonRuntime(t, true, true, true, false)
}

func TestOwnedDHCPv4RejectedRecoveryRuntime(t *testing.T) {
	testOwnedDHCPv4DaemonRuntime(t, false, false, false, true)
}

func testOwnedDHCPv4DaemonRuntime(
	t *testing.T,
	classless bool,
	recoverAtT2 bool,
	replaceAddress bool,
	rejectedRecovery bool,
) {
	t.Helper()
	if os.Getenv(dhcpv4RuntimeChildEnv) == "1" {
		runOwnedDHCPv4DaemonRuntime(t, classless, recoverAtT2, replaceAddress, rejectedRecovery)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), dhcpv4RuntimeChildEnv+"=1", dhcpv4RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv4 daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv4DaemonRuntime(
	t *testing.T,
	classless bool,
	recoverAtT2 bool,
	replaceAddress bool,
	rejectedRecovery bool,
) {
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
	configPath := prepareDHCPv4WAN(t, root, true, true)
	leaseDirectory := filepath.Join(root, "leases")
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.namespace.Close()
	downstream := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	parent, err := netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: parent.Attrs().Index},
		VlanId:    101,
	}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "wan-vlan", []string{"198.51.100.2/24"})
	leaseSeconds := 8
	if recoverAtT2 {
		leaseSeconds = 24
	} else if rejectedRecovery {
		leaseSeconds = 40
	}
	service := startDHCPv4RuntimeKea(
		t,
		root,
		classless,
		leaseSeconds,
		"198.51.100.2",
		"198.51.100.100",
		true,
	)
	serviceRunning := true
	defer func() {
		if serviceRunning {
			stopProtocolServices(t, []protocolService{service})
		}
	}()
	setRuntimeNamespace(t, gateway)
	addRuntimeRoute(t, gateway, downstream.namespace, "198.51.100.0/24", "192.0.2.1")
	addRuntimeRoute(t, gateway, provider.namespace, "192.0.2.0/29", "198.51.100.100")
	runTimeout := 45 * time.Second
	if rejectedRecovery {
		runTimeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
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
				value, found, readErr := reader.ExportJSON(
					ctx,
					yangpub.DatastoreOperational,
					"/ietf-interfaces:*",
				)
				if readErr != nil || !found {
					return ""
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, []byte(value)); err != nil {
					t.Fatal(err)
				}
				return compact.String()
			}
			daemon := startRuntimeDaemon(
				t,
				os.Getenv(dhcpv4RuntimeBinaryEnv),
				configPath,
				root,
				"dhcpv4",
			)
			defer func() { stopRuntimeDaemon(t, daemon) }()
			prefix := "198.51.100.100/24"
			if classless {
				prefix = "198.51.100.100/32"
			}
			waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, true)
			waitStaticRuntimeRoute(t, daemon, "ipv4", "198.51.100.2", 20, true)
			if classless {
				waitDHCPv4RuntimeOnLinkRoute(t, daemon, "198.51.100.2", true)
			}
			waitRuntimeOwnershipRead(t, daemon, read, `"kind":"dhcpv4"`)
			waitRuntimeOwnershipRead(t, daemon, read, `"valid-until":`)
			if rejectedRecovery {
				wanLeaseDirectory := filepath.Join(leaseDirectory, "wan")
				waitDHCPRecoveryRecord(t, daemon, wanLeaseDirectory)
				stopRuntimeDaemon(t, daemon)
				entries, err := os.ReadDir(wanLeaseDirectory)
				if err != nil || len(entries) != 1 {
					t.Fatalf("saved WAN lease count: entries=%v err=%v", entries, err)
				}
				if err := os.WriteFile(
					filepath.Join(wanLeaseDirectory, entries[0].Name()),
					[]byte("corrupt saved lease"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				setRuntimeNamespace(t, provider.namespace)
				stopProtocolServices(t, []protocolService{service})
				serviceRunning = false
				setRuntimeNamespace(t, gateway)
				daemon = startRuntimeDaemon(
					t,
					os.Getenv(dhcpv4RuntimeBinaryEnv),
					configPath,
					root,
					"dhcpv4-rejected",
				)
				waitRuntimeOwnershipRead(t, daemon, read, `"acquisition":"recovery-rejected"`)
				waitRuntimeOwnershipRead(t, daemon, read, `"lease-persistence":"rejected"`)
				waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, false)
				setRuntimeNamespace(t, provider.namespace)
				service = startDHCPv4RuntimeKea(
					t,
					root,
					false,
					leaseSeconds,
					"198.51.100.2",
					"198.51.100.100",
					false,
				)
				serviceRunning = true
				setRuntimeNamespace(t, gateway)
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					link, err := netlink.LinkByName("enatt0")
					if err != nil {
						t.Fatal(err)
					}
					addresses, err := netlink.AddrList(link, unix.AF_INET)
					if err != nil {
						t.Fatal(err)
					}
					if slices.ContainsFunc(
						addresses,
						func(address netlink.Addr) bool { return address.IPNet.String() == prefix },
					) {
						break
					}
					assertRuntimeDaemonRunning(t, daemon)
					time.Sleep(50 * time.Millisecond)
				}
				waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, true)
				waitRuntimeOwnershipRead(t, daemon, read, `"lease-persistence":"saved"`)
				waitRuntimeOwnershipRead(t, daemon, read, `"acquisition":"bound"`)
				return
			}
			initialExpiry := time.Time{}
			if recoverAtT2 {
				served := read()
				index := strings.Index(served, `"valid-until":"`)
				if index < 0 {
					t.Fatal("DHCPv4 assignment has no valid-until timestamp")
				}
				validUntil, found := strings.CutPrefix(served[index:], `"valid-until":"`)
				if !found {
					t.Fatal("DHCPv4 assignment has no valid-until timestamp")
				}
				value, _, found := strings.Cut(validUntil, `"`)
				if !found {
					t.Fatal("DHCPv4 valid-until timestamp is unterminated")
				}
				initialExpiry, err = time.Parse(time.RFC3339Nano, value)
				if err != nil {
					t.Fatal(err)
				}
			}
			waitRuntimeTCP(
				t,
				daemon,
				gateway,
				provider.namespace,
				downstream.namespace,
				"tcp4",
				"198.51.100.2:30522",
				"192.0.2.2:0",
				time.Now().Add(15*time.Second),
			)
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
			serviceRunning = false
			setRuntimeNamespace(t, gateway)
			if recoverAtT2 {
				waitDHCPv4RuntimeState(t, daemon, "state=RENEWING", 15*time.Second)
				if wait := time.Until(initialExpiry.Add(-6 * time.Second)); wait > 0 {
					time.Sleep(wait)
				}
				setRuntimeNamespace(t, provider.namespace)
				link, err := netlink.LinkByName("wan-vlan")
				if err != nil {
					t.Fatal(err)
				}
				address, err := netlink.ParseAddr("198.51.100.3/24")
				if err != nil {
					t.Fatal(err)
				}
				if err := netlink.AddrAdd(link, address); err != nil {
					t.Fatal(err)
				}
				reserved := "198.51.100.100"
				if replaceAddress {
					reserved = "198.51.100.101"
				}
				service = startDHCPv4RuntimeKea(
					t,
					root,
					classless,
					leaseSeconds,
					"198.51.100.3",
					reserved,
					false,
				)
				serviceRunning = true
				setRuntimeNamespace(t, gateway)
				if replaceAddress {
					waitDHCPv4RuntimeState(t, daemon, `"type":"NAK"`, 10*time.Second)
					waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, false)
					if !time.Now().Before(initialExpiry) {
						t.Fatal("DHCPv4 NAK withdrew the old lease only after expiry")
					}
					prefix = "198.51.100.101/32"
					setRuntimeNamespace(t, provider.namespace)
					_, destination, err := net.ParseCIDR("192.0.2.0/29")
					if err != nil {
						t.Fatal(err)
					}
					if err := netlink.RouteReplace(&netlink.Route{
						Dst: destination,
						Gw:  net.ParseIP("198.51.100.101"),
					}); err != nil {
						t.Fatal(err)
					}
					setRuntimeNamespace(t, gateway)
					waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, true)
					waitRuntimeOwnershipRead(t, daemon, read, "198.51.100.101/32")
				}
				waitStaticRuntimeRoute(t, daemon, "ipv4", "198.51.100.3", 20, true)
				waitDHCPv4RuntimeState(t, daemon, "state=REBINDING", 2*time.Second)
				if !time.Now().Before(initialExpiry) {
					var dhcpLog []string
					for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
						if strings.Contains(line, `"component":"dhcp"`) {
							dhcpLog = append(dhcpLog, line)
						}
					}
					keaLog, _ := os.ReadFile(filepath.Join(root, "kea-dhcp4.log"))
					t.Fatalf(
						"changed DHCPv4 route appeared only after the original lease expired: %s\nKea: %s",
						strings.Join(dhcpLog, "\n"),
						keaLog,
					)
				}
				waitStaticRuntimeRoute(t, daemon, "ipv4", "198.51.100.2", 20, false)
				waitDHCPv4RuntimeOnLinkRoute(t, daemon, "198.51.100.3", true)
				waitDHCPv4RuntimeOnLinkRoute(t, daemon, "198.51.100.2", false)
				waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, true)
				waitRuntimeTCP(
					t,
					daemon,
					gateway,
					provider.namespace,
					downstream.namespace,
					"tcp4",
					"198.51.100.2:30524",
					"192.0.2.2:0",
					time.Now().Add(15*time.Second),
				)
				return
			}
			waitStaticRuntimeAddress(t, daemon, "enatt0", prefix, false)
			waitStaticRuntimeRoute(t, daemon, "ipv4", "198.51.100.2", 20, false)
			if classless {
				waitDHCPv4RuntimeOnLinkRoute(t, daemon, "198.51.100.2", false)
			}
			logContent := runtimeDaemonLog(t, daemon)
			for _, state := range []string{"state=RENEWING", "state=REBINDING", "state=EXPIRED"} {
				if !strings.Contains(logContent, state) {
					t.Fatalf(
						"DHCPv4 %s transition absent: %s",
						state,
						runtimeLogTail(t, daemon, 30),
					)
				}
			}
			assertRuntimeTCP(
				t,
				provider.namespace,
				downstream.namespace,
				"tcp4",
				"198.51.100.2:30523",
				"192.0.2.2:0",
				false,
			)
			setRuntimeNamespace(t, gateway)
			assertRuntimeDaemonRunning(t, daemon)
		},
	)
}

func writeDHCPv4RuntimeNetwork(t *testing.T, directory string) {
	t.Helper()
	writeDHCPv4WANNetwork(t, directory, true)
}

func writeDHCPv4WANNetwork(t *testing.T, directory string, enabled bool) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	dhcpOptions := ""
	if enabled {
		dhcpOptions = `,"goodkind-mwan-steering:route-metric":20,"goodkind-mwan-steering:dhcpv4":{"client-id":"hex:01aabb","use-dns":false,"use-routes":true}`
	}

	owned := fmt.Sprintf(
		`{"name":"enatt0","type":"iana-if-type:l2vlan","enabled":true,"goodkind-mwan-steering:connection-id":"att","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"wan-under","id":101}},"ietf-ip:ipv4":{"goodkind-mwan-steering:dhcp":%t%s,"goodkind-mwan-steering:translation":{"mode":"native"}},"goodkind-mwan-steering:wan":{"name":"att","table-id":100,"fw-mark":1,"fw-mark-prio":100,"from-prio":55},"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
		enabled,
		dhcpOptions,
	)
	interfaces["interface"] = json.RawMessage(
		`[` + owned + `,{"name":"wan-under","type":"iana-if-type:ethernetCsmacd"},{"name":"enmwanbr0","type":"iana-if-type:other"},{"name":"enmgmt0","type":"iana-if-type:other"}]`,
	)
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "network.json"), output, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitDHCPv4RuntimeOnLinkRoute(
	t *testing.T,
	daemon *runtimeDaemon,
	gateway string,
	present bool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(
			unix.AF_INET,
			&netlink.Route{Table: unix.RT_TABLE_MAIN},
			netlink.RT_FILTER_TABLE,
		)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() == gateway+"/32" && route.Priority == 20 &&
				route.Protocol == netif.OwnedStaticRouteProtocol && (route.Gw == nil || route.Gw.Equal(
				net.IPv4zero,
			)) {
				found = true
			}
		}
		if found == present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("on-link route present=%t not observed: %s", present, runtimeLogTail(t, daemon, 40))
}

func waitDHCPv4RuntimeState(
	t *testing.T,
	daemon *runtimeDaemon,
	state string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(runtimeDaemonLog(t, daemon), state) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("DHCPv4 %s transition absent: %s", state, runtimeLogTail(t, daemon, 30))
}

func prepareDHCPv4WAN(t *testing.T, root string, enabled, publish bool) string {
	t.Helper()
	networkDir := filepath.Join(root, "mwan")
	networkdDir := filepath.Join(root, "networkd")
	stateDir := filepath.Join(root, "mwan-state")
	leaseDir := filepath.Join(root, "leases")
	for _, directory := range []string{networkDir, networkdDir, stateDir, leaseDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	writeDHCPv4WANNetwork(t, networkDir, enabled)
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		`[ifmgr]
role = "wan"
reconcile_interval = "1h"
lease_directory = %q
[ifmgr.iface.enmwanbr0]
[ifmgr.modules.links]
state_file = %q
[ifmgr.modules.addresses]
state_file = %q
[ifmgr.modules.autoconfiguration]
state_file = %q
[wanconfig]
publish = %t
`,
		leaseDir,
		filepath.Join(root, "owned-links.json"),
		filepath.Join(root, "owned-addresses.json"),
		filepath.Join(root, "kernel-policy.json"),
		publish,
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func newDHCPv4WANProvider(
	t *testing.T,
	gateway netns.NsHandle,
	address string,
) runtimePeer {
	t.Helper()
	provider := newRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	setRuntimeNamespace(t, provider.namespace)
	parent, err := netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	dhcpOwnedAddLink(
		t,
		&netlink.Vlan{
			LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: parent.Attrs().Index},
			VlanId:    101,
		},
	)
	configureRuntimeLink(t, "wan-vlan", []string{address})
	setRuntimeNamespace(t, gateway)
	return provider
}
