//go:build linux && protocol

package protocol_test

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
	"goodkind.io/mwan/gateway/integration/internal/testenv"
	"goodkind.io/mwan/gateway/networkload"
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
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 IA tentative address daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv6IATentativeAddress(t *testing.T) {
	testenv.MountPrivateRuntimeDirectories(t)
	t.Setenv("SYSREPO_REPOSITORY_PATH", t.TempDir())
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
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	writeDHCPv6IARuntimeNetwork(t, networkDir, true)
	if _, err := networkload.Load(
		filepath.Join(networkDir, "network.json"),
		schemaDir,
	); err != nil {
		t.Fatalf("load runtime network: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = true\n",
		filepath.Join(root, "owned-links.json"),
		filepath.Join(root, "owned-addresses.json"),
		filepath.Join(root, "kernel-policy.json"),
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.Namespace.Close()
	downstream := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		[]string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"},
		"",
	)
	defer downstream.Namespace.Close()
	provider := testenv.NewRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	defer provider.Namespace.Close()
	testenv.SetRuntimeNamespace(t, provider.Namespace)
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
	testenv.ConfigureRuntimeLink(t, "wan-vlan", []string{"2001:db8:2::2/64"})
	testenv.WaitAutoconfigurationLinkLocal(t, "wan-vlan")
	testenv.SetRuntimeNamespace(t, downstream.Namespace)
	testenv.AddMappedRuntimeRoute(t, "2001:db8:2::/64", "2001:db8:b01:fe::3", "lan-host")
	testenv.SetRuntimeNamespace(t, gateway)
	if err := os.WriteFile(
		"/proc/sys/net/ipv6/conf/all/forwarding",
		[]byte("1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	testenv.AddMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	for _, directory := range []string{
		filepath.Join(root, "kea-run"),
		filepath.Join(root, "kea-state"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testenv.BindStartupDirectory(t, filepath.Join(root, "kea-run"), "/run/kea")
	testenv.BindStartupDirectory(t, filepath.Join(root, "kea-state"), "/var/lib/kea")
	service := startDHCPv6IARuntimeKea(t, root, true)
	defer func() {
		testenv.SetRuntimeNamespace(t, provider.Namespace)
		testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
		testenv.SetRuntimeNamespace(t, gateway)
	}()
	testenv.SetRuntimeNamespace(t, gateway)
	// The DHCPv6 client needs a link-local address before duplicate address
	// detection finishes on the static address. The test adds that link-local
	// address without duplicate address detection.
	for name, value := range map[string]string{
		"dad_transmits": dhcpv6TentativeDADTransmits,
		"addr_gen_mode": dhcpv6TentativeAddrGenNone,
	} {
		if err := os.WriteFile(
			"/proc/sys/net/ipv6/conf/default/"+name,
			[]byte(value),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	daemon := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(dhcpv6RuntimeBinaryEnv),
		configPath,
		root,
		"dhcpv6ia-tentative",
	)
	defer testenv.StopRuntimeDaemon(t, daemon)
	addDHCPv6TentativeLinkLocal(t, daemon)
	waitNPTEdgeBeforeLeasedAddressReady(t, daemon)
	testenv.WaitStaticRuntimeAddress(t, daemon, "enatt0", dhcpv6TentativeEdge, true)
}

func addDHCPv6TentativeLinkLocal(t *testing.T, daemon *testenv.RuntimeDaemon) {
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
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon did not create enatt0: %s", testenv.RuntimeLogTail(t, daemon, 60))
}

func waitNPTEdgeBeforeLeasedAddressReady(t *testing.T, daemon *testenv.RuntimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(dhcpv6TentativeWait)
	for time.Now().Before(deadline) {
		leasedFlags, leased := runtimeIPv6AddressFlags(t, "enatt0", dhcpv6TentativeLeasedAddress)
		_, edged := runtimeIPv6AddressFlags(t, "enatt0", dhcpv6TentativeEdge)
		leasedReady := leased && leasedFlags&unix.IFA_F_TENTATIVE == 0
		if leasedReady {
			t.Fatalf(
				"The kernel finished duplicate address detection for IA_NA address %s before the test observed NPT edge %s: edge=%t; daemon: %s",
				dhcpv6TentativeLeasedAddress,
				dhcpv6TentativeEdge,
				edged,
				testenv.RuntimeLogTail(t, daemon, 60),
			)
		}
		if edged {
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(
		"DHCPv6 IA_NA address %s and NPT edge %s did not appear: %s",
		dhcpv6TentativeLeasedAddress,
		dhcpv6TentativeEdge,
		testenv.RuntimeLogTail(t, daemon, 60),
	)
}
