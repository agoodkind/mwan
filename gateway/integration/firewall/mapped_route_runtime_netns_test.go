//go:build linux && firewall

package firewall_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/internal/netif"
)

func TestOwnedMappedDaemonRuntime(t *testing.T) {
	if os.Getenv(testenv.MappedRuntimeChildEnv) == "1" {
		runOwnedMappedDaemonRuntime(t)
		return
	}
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedMappedDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		testenv.MappedRuntimeChildEnv+"=1",
		testenv.MappedRuntimeBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated mapped daemon test: %v: %s", err, output)
	}
}

func runOwnedMappedDaemonRuntime(t *testing.T) {
	testenv.MountPrivateRuntimeDirectories(t)
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
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n",
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
	lan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		[]string{"192.0.2.3/29", "192.0.2.4/29", "2001:db8:b01:fe::2/64"},
		"",
	)
	defer lan.Namespace.Close()
	legacy := testenv.NewRuntimePeer(
		t,
		gateway,
		"enwebpass0",
		"legacy-peer",
		[]string{"10.20.0.1/24", "fd20::1/64"},
		[]string{"10.20.0.2/24", "fd20::2/64"},
		testenv.LegacyRuntimeMAC,
	)
	defer legacy.Namespace.Close()
	testenv.SetRuntimeNamespace(t, legacy.Namespace)
	testenv.AddMappedRuntimeRoute(t, "2001:db8:beef:200::/60", "fd20::1", "legacy-peer")
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.AddRuntimeDefault(t, "enwebpass0", "fd20::2")
	parentPeer := testenv.NewRuntimePeer(
		t,
		gateway,
		"ownphys0",
		"owned-peer",
		nil,
		nil,
		testenv.OwnedRuntimeParentMAC,
	)
	defer parentPeer.Namespace.Close()
	testenv.SetRuntimeNamespace(t, parentPeer.Namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index},
		VlanId:    397,
	}); err != nil {
		t.Fatal(err)
	}
	testenv.ConfigureRuntimeLink(t, "peer397", []string{"10.39.7.2/24", "fd39:7::2/64"})
	testenv.AddMappedRuntimeRoute(t, "10.39.7.4/32", "10.39.7.1", "peer397")
	testenv.AddMappedRuntimeRoute(t, "2001:db8:beef:300::/60", "fd39:7::1", "peer397")
	testenv.AddMappedRuntimeRoute(t, "2001:db8:beef:400::/60", "fd39:7::1", "peer397")
	testenv.SetRuntimeNamespace(t, lan.Namespace)
	testenv.AddMappedRuntimeRoute(t, "10.39.7.0/24", "192.0.2.1", "lan-host")
	testenv.AddMappedRuntimeRoute(t, "fd39:7::/64", "2001:db8:b01:fe::3", "lan-host")
	testenv.AddMappedRuntimeRoute(t, "fd20::/64", "2001:db8:b01:fe::3", "lan-host")
	testenv.SetRuntimeNamespace(t, gateway)
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		"/proc/sys/net/ipv6/conf/all/forwarding",
		[]byte("1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	testenv.AddMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	testenv.WriteStaticRuntimeNetwork(
		t,
		networkDir,
		"10.39.7.1",
		"fd39:7::1",
		"10.39.7.2",
		"fd39:7::2",
		398,
		24,
		true,
	)
	testenv.WriteMappedRuntimeProvider(t, networkDir, "10.39.7.3", "2001:db8:beef:300::/60")
	first := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-first",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, first)
	defer func() {
		if t.Failed() {
			testenv.SetRuntimeNamespace(t, gateway)
			for _, arguments := range [][]string{
				{"-6", "rule", "show"},
				{"-6", "route", "show", "table", "all"},
				{"-6", "neigh", "show"},
			} {
				output, err := exec.Command("ip", arguments...).CombinedOutput()
				t.Logf("gateway ip %v: %s (%v)", arguments, output, err)
			}
			t.Logf("complete mapped-first daemon log: %s", testenv.RuntimeDaemonLog(t, first))
		}
	}()
	testenv.WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	testenv.WaitStaticRuntimeAddress(t, first, "owned397", "10.39.7.3/32", true)
	testenv.WaitStaticRuntimeAddress(t, first, "owned397", "10.39.7.4/32", false)
	testenv.WaitStaticRuntimeAddress(t, first, "owned397", "2001:db8:beef:300::1/128", true)
	testenv.WaitStaticRuntimeAddress(t, first, "enwebpass0", "2001:db8:beef:200::1/128", true)
	testenv.WaitMappedRuntimeRule(t, first, "ip", "nat", "10.39.7.3", 40*time.Second)
	testenv.WaitMappedRuntimeRule(t, first, "ip6", "nat", "2001:db8:beef:300::1", 40*time.Second)
	waitMappedRuntimeForwarding(t, first, 10*time.Second)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
		"2001:db8:beef:300::1/128",
	)
	testenv.AssertMappedRuntimeReply(
		t,
		first,
		gateway,
		parentPeer.Namespace,
		lan.Namespace,
		"udp4",
		"10.39.7.3:39803",
		"192.0.2.3:39803",
	)
	testenv.AssertMappedRuntimeReply(
		t,
		first,
		gateway,
		parentPeer.Namespace,
		lan.Namespace,
		"udp4",
		"10.39.7.4:39804",
		"192.0.2.4:39804",
	)
	testenv.AssertMappedRuntimeReply(
		t,
		first,
		gateway,
		parentPeer.Namespace,
		lan.Namespace,
		"udp6",
		"[2001:db8:beef:300::1]:39806",
		"[2001:db8:b01:fe::2]:39806",
	)
	testenv.AssertMappedRuntimeReply(
		t,
		first,
		gateway,
		legacy.Namespace,
		lan.Namespace,
		"udp6",
		"[2001:db8:beef:200::1]:20006",
		"[2001:db8:b01:fe::2]:20006",
	)
	testenv.AssertRuntimeTranslationSettled(t, first)
	for _, prefix := range []string{"10.20.0.3/32", "10.20.0.4/32", "10.20.0.5/32"} {
		testenv.WaitStaticRuntimeAddress(t, first, "enwebpass0", prefix, false)
	}
	testenv.KillOwnedRuntimeDaemon(t, first)
	testenv.SetReleaseOwner(t, networkDir, "webpass", "networkd")
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"webpass",
		"goodkind-mwan-steering:link-files",
		json.RawMessage(`"hand-authored"`),
	)
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"webpass",
		"ietf-ip:ipv4",
		json.RawMessage(
			`{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":"10.20.0.3","internal":"192.0.2.3"},{"external":"10.20.0.4","internal":"192.0.2.4","delivery":"local"},{"external":"10.20.0.5","internal":"192.0.2.4","delivery":"routed"}]}}`,
		),
	)
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"webpass",
		"ietf-ip:ipv6",
		json.RawMessage(
			`{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":"2001:db8:beef:200::/60"}}}`,
		),
	)
	legacyMappings := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-networkd",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, legacyMappings)
	defer func() {
		if t.Failed() {
			t.Logf(
				"complete mapped-networkd daemon log: %s",
				testenv.RuntimeDaemonLog(t, legacyMappings),
			)
		}
	}()
	for _, prefix := range []string{"10.20.0.3/32", "10.20.0.4/32"} {
		testenv.WaitStaticRuntimeAddress(t, legacyMappings, "enwebpass0", prefix, true)
	}
	testenv.WaitStaticRuntimeAddress(t, legacyMappings, "enwebpass0", "10.20.0.5/32", false)
	testenv.WaitStaticRuntimeAddress(t, legacyMappings, "enwebpass0", "10.20.0.1/24", true)
	assertLegacyMappingReceipts(t, root, 2, 1)
	testenv.WaitMappedRuntimeRule(t, legacyMappings, "ip", "nat", "10.20.0.3", 10*time.Second)
	waitMappedRuntimeForwarding(t, legacyMappings, 10*time.Second)
	testenv.AssertRuntimeTranslationSettled(t, legacyMappings)
	testenv.AssertMappedRuntimeReply(
		t,
		legacyMappings,
		gateway,
		legacy.Namespace,
		lan.Namespace,
		"udp6",
		"[2001:db8:beef:200::1]:20006",
		"[2001:db8:b01:fe::2]:20006",
	)
	testenv.KillOwnedRuntimeDaemon(t, legacyMappings)
	legacyLink, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName("owned397")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := netlink.ParseAddr("10.39.7.99/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, foreign); err != nil {
		t.Fatal(err)
	}
	testenv.WriteMappedRuntimeProvider(t, networkDir, "10.39.7.5", "2001:db8:beef:400::/60")
	testenv.SetMappedRuntimeIncompatibleChain(t)
	second := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-second",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, second)
	defer func() {
		if t.Failed() {
			t.Logf("complete mapped-second daemon log: %s", testenv.RuntimeDaemonLog(t, second))
		}
	}()
	testenv.WaitStaticRuntimeLog(t, second, "incompatible type, hook, priority, or policy")
	testenv.WaitStaticRuntimeAddress(t, second, "owned397", "2001:db8:beef:300::1/128", true)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
		"2001:db8:beef:300::1/128",
		"2001:db8:beef:400::1/128",
	)
	testenv.WaitMappedRuntimeRule(t, second, "ip6", "nat", "2001:db8:beef:300::1", 10*time.Second)
	if output, err := exec.Command(
		"nft",
		"delete",
		"chain",
		"ip6",
		"nat",
		"postrouting",
	).CombinedOutput(); err != nil {
		t.Fatalf("repair NPT postrouting chain: %v: %s", err, output)
	}
	testenv.WaitStaticRuntimeAddress(t, second, "owned397", "10.39.7.5/32", true)
	testenv.WaitStaticRuntimeAddress(t, second, "owned397", "10.39.7.3/32", false)
	testenv.WaitStaticRuntimeAddress(t, second, "owned397", "2001:db8:beef:400::1/128", true)
	testenv.WaitStaticRuntimeAddress(t, second, "owned397", "2001:db8:beef:300::1/128", false)
	testenv.WaitStaticRuntimeAddress(t, second, "owned397", "10.39.7.99/24", true)
	testenv.AssertMappedRuntimeRuleAbsent(t, "ip6", "nat", "2001:db8:beef:300::1")
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
		"2001:db8:beef:400::1/128",
	)
	testenv.AssertMappedRuntimeReply(
		t,
		second,
		gateway,
		legacy.Namespace,
		lan.Namespace,
		"udp6",
		"[2001:db8:beef:200::1]:20006",
		"[2001:db8:b01:fe::2]:20006",
	)
	for _, prefix := range []string{"10.20.0.3/32", "10.20.0.4/32", "10.20.0.5/32"} {
		testenv.WaitStaticRuntimeAddress(t, second, "enwebpass0", prefix, false)
	}
	assertLegacyMappingReceipts(t, root, 0, 0)
	testenv.WaitStaticRuntimeAddress(t, second, "enwebpass0", "10.20.0.1/24", true)
	recreated, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	oldIndex := recreated.Attrs().Index
	if err := netlink.LinkDel(recreated); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{Name: "enwebpass0"},
	}); err != nil {
		t.Fatal(err)
	}
	testenv.ConfigureRuntimeLink(t, "enwebpass0", []string{"fd20::1/64"})
	testenv.WaitStaticRuntimeAddress(t, second, "enwebpass0", "2001:db8:beef:200::1/128", true)
	recreated, err = netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Attrs().Index == oldIndex {
		t.Fatal("external link recreation did not change the kernel index")
	}
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
		"2001:db8:beef:400::1/128",
	)
	testenv.KillOwnedRuntimeDaemon(t, second)
	testenv.WriteMappedRuntimeProvider(t, networkDir, "10.39.7.5", "2001:db8:beef:401::/64")
	narrow := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-narrow",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, narrow)
	testenv.WaitStaticRuntimeAddress(t, narrow, "owned397", "2001:db8:beef:401::1/128", true)
	testenv.WaitStaticRuntimeAddress(t, narrow, "owned397", "2001:db8:beef:400::1/128", false)
	testenv.WaitMappedRuntimeRule(t, narrow, "ip6", "nat", "2001:db8:beef:401::1", 10*time.Second)
	testenv.KillOwnedRuntimeDaemon(t, narrow)
	testenv.WriteMappedRuntimeProvider(t, networkDir, "10.39.7.5", "2001:db8:beef:400::/60")
	testenv.SetMappedRuntimeIncompatibleChain(t)
	expanded := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-expanded",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, expanded)
	defer func() {
		if t.Failed() {
			t.Logf("complete mapped-expanded daemon log: %s", testenv.RuntimeDaemonLog(t, expanded))
		}
	}()
	testenv.WaitStaticRuntimeAddress(t, expanded, "owned397", "2001:db8:beef:400::1/128", true)
	testenv.WaitRuntimeNPTEdgeReady(t, expanded, "owned397", "2001:db8:beef:400::1/128")
	if output, err := exec.Command(
		"nft",
		"delete",
		"chain",
		"ip6",
		"nat",
		"postrouting",
	).CombinedOutput(); err != nil {
		t.Fatalf("repair expanded-prefix NPT chain: %v: %s", err, output)
	}
	testenv.WaitStaticRuntimeAddress(t, expanded, "owned397", "2001:db8:beef:401::1/128", false)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
		"2001:db8:beef:400::1/128",
	)
	testenv.KillOwnedRuntimeDaemon(t, expanded)
	link, err = netlink.LinkByName("owned397")
	if err != nil {
		t.Fatal(err)
	}
	foreignNPT, err := netlink.ParseAddr("2001:db8:beef:500::1/64")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, foreignNPT); err != nil {
		t.Fatal(err)
	}
	testenv.WriteMappedRuntimeProvider(t, networkDir, "10.39.7.5", "2001:db8:beef:500::/60")
	conflict := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-conflict",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, conflict)
	testenv.WaitStaticRuntimeLog(
		t,
		conflict,
		"foreign address uses 2001:db8:beef:500::1 with another prefix",
	)
	testenv.AssertMappedRuntimeRuleAbsent(t, "ip6", "nat", "2001:db8:beef:500::1")
	testenv.WaitStaticRuntimeAddress(t, conflict, "owned397", "2001:db8:beef:500::1/64", true)
	testenv.KillOwnedRuntimeDaemon(t, conflict)
	testenv.WriteRuntimeLegacyNPT(t, networkDir, "2001:db8:beef:200::/60", false)
	legacyConfig := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.addresses]\nstate_file = %q\n",
		filepath.Join(root, "owned-addresses.json"),
	)
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyOnly := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-legacy-only",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, legacyOnly)
	testenv.WaitMappedRuntimeRule(
		t,
		legacyOnly,
		"ip6",
		"nat",
		"2001:db8:beef:200::1",
		10*time.Second,
	)
	testenv.WaitStaticRuntimeAddress(t, legacyOnly, "enwebpass0", "fd20::1/64", true)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
	)
	testenv.KillOwnedRuntimeDaemon(t, legacyOnly)
	legacyRestart := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-legacy-restart",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, legacyRestart)
	testenv.WaitMappedRuntimeRule(
		t,
		legacyRestart,
		"ip6",
		"nat",
		"2001:db8:beef:200::1",
		10*time.Second,
	)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:200::1/128",
	)
	testenv.KillOwnedRuntimeDaemon(t, legacyRestart)
	legacyLink, err = netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	unrecorded, err := netlink.ParseAddr("2001:db8:beef:600::1/128")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(legacyLink, unrecorded); err != nil {
		t.Fatal(err)
	}
	testenv.WriteRuntimeLegacyNPT(t, networkDir, "2001:db8:beef:600::/60", false)
	unrecordedDaemon := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-unrecorded",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, unrecordedDaemon)
	testenv.WaitStaticRuntimeLog(
		t,
		unrecordedDaemon,
		"address 2001:db8:beef:600::1/128 already exists without ownership record",
	)
	testenv.WaitStaticRuntimeAddress(
		t,
		unrecordedDaemon,
		"enwebpass0",
		"2001:db8:beef:600::1/128",
		true,
	)
	testenv.WaitStaticRuntimeAddress(
		t,
		unrecordedDaemon,
		"enwebpass0",
		"2001:db8:beef:200::1/128",
		false,
	)
	testenv.AssertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	testenv.KillOwnedRuntimeDaemon(t, unrecordedDaemon)
	testenv.WriteRuntimeLegacyNPT(t, networkDir, "2001:db8:beef:700::/60", false)
	finalIntent := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-final-intent",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, finalIntent)
	testenv.WaitMappedRuntimeRule(
		t,
		finalIntent,
		"ip6",
		"nat",
		"2001:db8:beef:700::1",
		10*time.Second,
	)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:700::1/128",
	)
	testenv.KillOwnedRuntimeDaemon(t, finalIntent)
	legacyLink, err = netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetName(legacyLink, "renamed-wan"); err != nil {
		t.Fatal(err)
	}
	testenv.WriteRuntimeLegacyNPT(t, networkDir, "2001:db8:beef:700::/60", true)
	runtimeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		configPath,
		[]byte(
			strings.ReplaceAll(
				string(runtimeConfig),
				`reconcile_interval = "1h"`,
				`reconcile_interval = "100ms"`,
			),
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	removed := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-no-wans",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, removed)
	testenv.WaitStaticRuntimeLog(
		t,
		removed,
		"recorded NPT edge link enwebpass0 was renamed to renamed-wan",
	)
	testenv.WaitStaticRuntimeAddress(t, removed, "renamed-wan", "2001:db8:beef:700::1/128", true)
	testenv.AssertRuntimeNPTEdges(
		t,
		filepath.Join(root, "owned-addresses.json"),
		"2001:db8:beef:700::1/128",
	)
	if err := netlink.LinkSetName(legacyLink, "enwebpass0"); err != nil {
		t.Fatal(err)
	}
	testenv.WaitStaticRuntimeAddress(t, removed, "enwebpass0", "2001:db8:beef:700::1/128", false)
	testenv.WaitStaticRuntimeAddress(t, removed, "enwebpass0", "2001:db8:beef:600::1/128", true)
	testenv.WaitStaticRuntimeAddress(t, removed, "enwebpass0", "fd20::1/64", true)
	testenv.AssertMappedRuntimeRuleAbsent(t, "ip6", "nat", "2001:db8:beef:700::1")
	testenv.AssertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	testenv.KillOwnedRuntimeDaemon(t, removed)
	testenv.CheckRuntimeInternalNPTRelocation(t, gateway, configPath, networkDir, root)
	testenv.SetReleaseOwner(t, networkDir, "webpass", "networkd")
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"webpass",
		"goodkind-mwan-steering:link-files",
		json.RawMessage(`"hand-authored"`),
	)
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"webpass",
		"ietf-ip:ipv4",
		json.RawMessage(
			`{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":"10.20.0.4","internal":"192.0.2.4","delivery":"local"}]}}`,
		),
	)
	legacyLink, err = netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	unrecorded, err = netlink.ParseAddr("10.20.0.4/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(legacyLink, unrecorded); err != nil {
		t.Fatal(err)
	}
	rejected := testenv.StartRuntimeDaemon(
		t,
		os.Getenv(testenv.MappedRuntimeBinaryEnv),
		configPath,
		root,
		"mapped-unrecorded",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, rejected)
	testenv.WaitStaticRuntimeLog(
		t,
		rejected,
		"address 10.20.0.4/32 already exists without ownership record",
	)
	testenv.WaitStaticRuntimeAddress(t, rejected, "enwebpass0", "10.20.0.4/32", true)
	assertLegacyMappingReceipts(t, root, 0, 1)
}

func assertLegacyMappingReceipts(t *testing.T, root string, objects, promotions int) {
	t.Helper()
	receipts, err := netif.InspectOwnedRelease(
		connectionid.ID("webpass"),
		filepath.Join(root, "owned-links.json"),
		filepath.Join(root, "owned-addresses.json"),
		filepath.Join(root, "kernel-policy.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipts.PreviousBoot || receipts.OrdinaryObjects != objects ||
		receipts.Promotions != promotions ||
		receipts.NPTEdges != 1 {
		t.Fatalf(
			"legacy mapping receipts: %+v; expected %d objects, %d promotions, and one NPT edge",
			receipts,
			objects,
			promotions,
		)
	}
}

func waitMappedRuntimeForwarding(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	timeout time.Duration,
) {
	t.Helper()
	checks := []struct {
		arguments []string
		lines     [][]string
	}{
		{
			[]string{"nft", "list", "chain", "inet", "mangle", "prerouting"},
			[][]string{
				{`iifname "owned397" ct state new meta mark set 0x00000004`},
				{"ct state established,related meta mark set ct mark"},
			},
		},
		{
			[]string{"nft", "list", "chain", "inet", "mangle", "postrouting"},
			[][]string{{"ct mark set meta mark"}},
		},
		{
			[]string{"nft", "list", "chain", "inet", "mwan_steer", "forward"},
			[][]string{
				{
					`iifname "enmwanbr0" oifname "owned397" meta nfproto ipv4 meta mark != 0x00000004 drop`,
				},
				{
					`iifname "enmwanbr0" oifname "owned397" meta nfproto ipv6 meta mark != 0x00000002 meta mark != 0x00000004 drop`,
				},
				{
					`iifname "enmwanbr0" oifname "enwebpass0" meta nfproto ipv6 meta mark != 0x00000002 meta mark != 0x00000004 drop`,
				},
			},
		},
	}
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ready := true
		for _, check := range checks {
			output, err := exec.Command(check.arguments[0], check.arguments[1:]...).CombinedOutput()
			if err != nil {
				last = fmt.Sprintf("%s: %v: %s", strings.Join(check.arguments, " "), err, output)
				ready = false
				break
			}
			for _, parts := range check.lines {
				if !testenv.MappedRuntimeHasLine(string(output), parts) {
					last = fmt.Sprintf(
						"%s lacks line containing %q: %s",
						strings.Join(check.arguments, " "),
						parts,
						output,
					)
					ready = false
					break
				}
			}
			if !ready {
				break
			}
		}
		if ready {
			ready, last = mappedRuntimeKernelForwardingReady()
		}
		if ready {
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"mapped forwarding did not become ready: %s; daemon=%s",
		last,
		testenv.RuntimeLogTail(t, daemon, 100),
	)
}

func mappedRuntimeKernelForwardingReady() (bool, string) {
	rules, err := netlink.RuleList(unix.AF_INET6)
	if err != nil {
		return false, fmt.Sprintf("list IPv6 policy rules: %v", err)
	}
	foundRule := false
	for _, rule := range rules {
		if rule.Priority == 398 && rule.Table == 398 && rule.Mark == 4 {
			foundRule = true
			break
		}
	}
	if !foundRule {
		return false, fmt.Sprintf("IPv6 mark-4 rule for table 398 missing: %v", rules)
	}
	wan, err := netlink.LinkByName("owned397")
	if err != nil {
		return false, fmt.Sprintf("find owned397: %v", err)
	}
	lan, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		return false, fmt.Sprintf("find enmwanbr0: %v", err)
	}
	routes, err := netlink.RouteListFiltered(
		unix.AF_INET6,
		&netlink.Route{Table: 398},
		netlink.RT_FILTER_TABLE,
	)
	if err != nil {
		return false, fmt.Sprintf("list IPv6 routes in table 398: %v", err)
	}
	defaultReady, edgeReady := false, false
	for _, route := range routes {
		if route.LinkIndex == wan.Attrs().Index && route.Gw.Equal(net.ParseIP("fd39:7::2")) &&
			(route.Dst == nil || route.Dst.String() == "::/0") {
			defaultReady = true
		}
		if route.LinkIndex == lan.Attrs().Index && route.Dst != nil &&
			route.Dst.String() == "2001:db8:b01:fe::2/128" {
			edgeReady = true
		}
	}
	if !defaultReady || !edgeReady {
		return false, fmt.Sprintf("IPv6 default or edge route missing in table 398: %v", routes)
	}
	return true, ""
}
