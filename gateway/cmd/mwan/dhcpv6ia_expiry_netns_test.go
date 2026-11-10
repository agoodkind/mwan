//go:build linux && firewallnetns

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/networkload"
)

const (
	dhcpv6ExpiryShortAddress = "2001:db8:30::100/128"
	dhcpv6ExpiryWANTable     = 100
	dhcpv6ExpiryValidAfter   = 6 * time.Second
	dhcpv6ExpirySettleAfter  = 3 * time.Second
)

func TestOwnedDHCPv6IAShortAddressExpiryKeepsRoutesRuntime(t *testing.T) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6IAShortAddressExpiry(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 IA expiry daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv6IAShortAddressExpiry(t *testing.T) {
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
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	writeDHCPv6IARuntimeNetwork(t, networkDir, true)
	if _, err := networkload.Load(filepath.Join(networkDir, "network.json"), schemaDir); err != nil {
		t.Fatalf("load runtime network: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = true\n", filepath.Join(root, "owned-links.json"), filepath.Join(root, "owned-addresses.json"), filepath.Join(root, "kernel-policy.json"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"}, "")
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	parent, err := netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: parent.Attrs().Index}, VlanId: 101}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "wan-vlan", []string{"2001:db8:2::2/64"})
	waitAutoconfigurationLinkLocal(t, "wan-vlan")
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/wan-vlan/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addMappedRuntimeRoute(t, "2001:db8:30::/60", "2001:db8:2::1", "wan-vlan")
	setRuntimeNamespace(t, downstream.namespace)
	addMappedRuntimeRoute(t, "2001:db8:2::/64", "2001:db8:b01:fe::3", "lan-host")
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	setRuntimeNamespace(t, provider.namespace)
	served, stopServer := startDHCPv6IAUnequalServer(t)
	defer stopServer()
	stopRouter := startDHCPv6RuntimeRouter(t, true)
	defer stopRouter()
	setRuntimeNamespace(t, gateway)
	daemon := startRuntimeDaemon(t, os.Getenv(dhcpv6RuntimeBinaryEnv), configPath, root, "dhcpv6ia-expiry")
	defer stopRuntimeDaemon(t, daemon)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve unequal DHCPv6 associations: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("DHCPv6 Request with unequal associations did not complete")
	}
	replied := time.Now()
	stopServer()
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpv6ExpiryShortAddress, true)
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
	waitDHCPv6ExpiryRouting(t, daemon)
	assertDHCPv6ExpiryRoutingHeld(t, daemon, replied.Add(dhcpv6ExpiryValidAfter+dhcpv6ExpirySettleAfter))
	waitStaticRuntimeAddress(t, daemon, "enatt0", "2001:db8:30::1/128", true)
}

func waitDHCPv6ExpiryRouting(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	missing := ""
	for time.Now().Before(deadline) {
		missing = dhcpv6ExpiryMissingRouting()
		if missing == "" {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("IPv6 WAN routing did not become ready: missing %s; daemon: %s", missing, runtimeLogTail(t, daemon, 40))
}

func assertDHCPv6ExpiryRoutingHeld(t *testing.T, daemon *runtimeDaemon, until time.Time) {
	t.Helper()
	started := time.Now()
	shortAddressGone := false
	for time.Now().Before(until) || !shortAddressGone {
		if time.Since(until) > 10*time.Second {
			t.Fatalf("short-lived address %s remains installed: %s", dhcpv6ExpiryShortAddress, runtimeLogTail(t, daemon, 40))
		}
		if missing := dhcpv6ExpiryMissingRouting(); missing != "" {
			routes, _ := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: dhcpv6ExpiryWANTable}, netlink.RT_FILTER_TABLE)
			rules, _ := netlink.RuleList(netlink.FAMILY_V6)
			t.Fatalf("IPv6 WAN routing lost %s after %s while the short-lived address expired: short_address_gone=%t table=%v rules=%v daemon: %s",
				missing, time.Since(started), shortAddressGone, routes, rules, runtimeLogTail(t, daemon, 60))
		}
		_, shortAddressPresent := runtimeIPv6AddressFlags(t, "enatt0", dhcpv6ExpiryShortAddress)
		shortAddressGone = !shortAddressPresent
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(10 * time.Millisecond)
	}
}
