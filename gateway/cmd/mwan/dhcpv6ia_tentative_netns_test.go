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

	"github.com/agoodkind/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/networkload"
)

const (
	dhcpv6TentativeLeasedAddress = "2001:db8:2::100/128"
	dhcpv6TentativeEdge          = "2001:db8:30::1/128"
	dhcpv6TentativeLinkLocal     = "fe80::5/64"
	dhcpv6TentativeDADTransmits  = "3"
	dhcpv6TentativeAddrGenNone   = "1"
	dhcpv6TentativeWait          = 20 * time.Second
)

func TestOwnedDHCPv6IATentativeAddressInstallsNPTEdgeRuntime(t *testing.T) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6IATentativeAddress(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 IA tentative address daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv6IATentativeAddress(t *testing.T) {
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
	setRuntimeNamespace(t, downstream.namespace)
	addMappedRuntimeRoute(t, "2001:db8:2::/64", "2001:db8:b01:fe::3", "lan-host")
	setRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	setRuntimeNamespace(t, provider.namespace)
	for _, directory := range []string{filepath.Join(root, "kea-run"), filepath.Join(root, "kea-state")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bindStartupDirectory(t, filepath.Join(root, "kea-run"), "/run/kea")
	bindStartupDirectory(t, filepath.Join(root, "kea-state"), "/var/lib/kea")
	service := startDHCPv6IARuntimeKea(t, root, true)
	defer func() {
		setRuntimeNamespace(t, provider.namespace)
		stopProtocolServices(t, []protocolService{service})
		setRuntimeNamespace(t, gateway)
	}()
	setRuntimeNamespace(t, gateway)
	// enatt0 must inherit these defaults when the daemon creates it. DHCPv6 must reply while the
	// static address remains tentative. Kernel link local detection would otherwise delay the reply
	// until the static address becomes ready and let the test pass on main despite the NPT defect.
	for name, value := range map[string]string{"dad_transmits": dhcpv6TentativeDADTransmits, "addr_gen_mode": dhcpv6TentativeAddrGenNone} {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/default/"+name, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	daemon := startRuntimeDaemon(t, os.Getenv(dhcpv6RuntimeBinaryEnv), configPath, root, "dhcpv6ia-tentative")
	defer stopRuntimeDaemon(t, daemon)
	addDHCPv6TentativeLinkLocal(t, daemon)
	waitNPTEdgeBeforeLeasedAddressReady(t, daemon)
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpv6TentativeEdge, true)
}

func addDHCPv6TentativeLinkLocal(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(dhcpv6TentativeWait)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName("enatt0")
		if err == nil {
			address, err := netlink.ParseAddr(dhcpv6TentativeLinkLocal)
			if err != nil {
				t.Fatal(err)
			}
			address.Flags = unix.IFA_F_NODAD
			if err := netlink.AddrAdd(link, address); err != nil {
				t.Fatalf("add link-local address to enatt0: %v", err)
			}
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon did not create enatt0: %s", runtimeLogTail(t, daemon, 60))
}

func waitNPTEdgeBeforeLeasedAddressReady(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(dhcpv6TentativeWait)
	for time.Now().Before(deadline) {
		leasedFlags, leased := runtimeIPv6AddressFlags(t, "enatt0", dhcpv6TentativeLeasedAddress)
		_, edged := runtimeIPv6AddressFlags(t, "enatt0", dhcpv6TentativeEdge)
		leasedReady := leased && leasedFlags&unix.IFA_F_TENTATIVE == 0
		if leasedReady {
			t.Fatalf("IA_NA address %s finished duplicate address detection before NPT edge %s was installed: edge=%t; daemon: %s",
				dhcpv6TentativeLeasedAddress, dhcpv6TentativeEdge, edged, runtimeLogTail(t, daemon, 60))
		}
		if edged {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("DHCPv6 IA_NA address %s and NPT edge %s did not appear: %s", dhcpv6TentativeLeasedAddress, dhcpv6TentativeEdge, runtimeLogTail(t, daemon, 60))
}
