//go:build linux && systemd

package systemd_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/networkd"
)

func TestConnectionReleaseDaemonRuntime(t *testing.T) {
	requireSystemd(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer func() {
		testenv.SetRuntimeNamespace(t, gateway)
		for _, name := range []string{"enmgmt0", "enmwanbr0", "ownphys0", "service-parent"} {
			link, err := netlink.LinkByName(name)
			if err != nil {
				continue
			}
			if err := netlink.LinkDel(link); err != nil && !errors.Is(err, unix.ENODEV) {
				t.Errorf("delete fixture link %s index %d: %v", name, link.Attrs().Index, err)
			}
		}
	}()
	if err := os.MkdirAll("/var/lib/mwan", 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/var/lib/mwan", "connection-release-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	networkDir, unitDir := filepath.Join(root, "network"), filepath.Join(root, "units")
	for _, directory := range []string{networkDir, unitDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	for destination, source := range map[string]string{
		"/etc/mwan":                       networkDir,
		"/etc/systemd/network":            unitDir,
		"/usr/local/share/wanconfig/yang": schemaDir,
	} {
		testenv.BindStartupDirectory(t, source, destination)
		t.Cleanup(func() {
			if err := unix.Unmount(destination, 0); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(func() { requireSystemdOutput(t, "systemctl", "stop", "systemd-networkd") })
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"release-mgmt",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.Namespace.Close()
	lan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"release-lan",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		[]string{"192.0.2.2/29", "192.0.2.3/29", "192.0.2.4/29", "2001:db8:b01:fe::2/64"},
		"",
	)
	defer lan.Namespace.Close()
	owned := testenv.NewRuntimePeer(
		t,
		gateway,
		"ownphys0",
		"release-owned",
		nil,
		nil,
		testenv.OwnedRuntimeParentMAC,
	)
	defer owned.Namespace.Close()
	testenv.SetRuntimeNamespace(t, owned.Namespace)
	addReleasePeerVLAN(t, "release-owned", "peer397", 397, []string{"10.39.7.2/24", "fd39:7::2/64"})
	testenv.SetRuntimeNamespace(t, gateway)
	provider := testenv.NewRuntimePeer(t, gateway, "service-parent", "service-peer", nil, nil, "")
	defer provider.Namespace.Close()
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	addReleasePeerVLAN(
		t,
		"service-peer",
		"wan-vlan",
		53,
		[]string{"fd53::2/64", "10.53.0.2/24", "198.51.100.2/24"},
	)
	testenv.AddMappedRuntimeRoute(t, "2001:db8:53::/60", "fd53::1", "wan-vlan")
	testenv.SetRuntimeNamespace(t, lan.Namespace)
	testenv.AddMappedRuntimeRoute(t, "fd53::/64", "2001:db8:b01:fe::3", "release-lan")
	testenv.AddMappedRuntimeRoute(t, "10.53.0.0/24", "192.0.2.1", "release-lan")
	testenv.AddMappedRuntimeRoute(t, "198.51.100.0/24", "192.0.2.1", "release-lan")
	testenv.SetRuntimeNamespace(t, gateway)
	testenv.AddMappedRuntimeRoute(t, "2001:db8:b01::/60", "", "enmwanbr0")
	forwarding, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		"/proc/sys/net/ipv6/conf/all/forwarding",
		[]byte("1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	defer func() {
		testenv.SetRuntimeNamespace(t, gateway)
		if err := os.WriteFile(
			"/proc/sys/net/ipv6/conf/all/forwarding",
			forwarding,
			0o600,
		); err != nil {
			t.Error(err)
		}
	}()
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
	writeNPTServiceProvider(t, networkDir, false)
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"service-provider",
		"goodkind-mwan-steering:steering",
		json.RawMessage(`{"tier":0,"weight":1}`),
	)
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"service-provider",
		"ietf-ip:ipv4",
		json.RawMessage(
			`{"goodkind-mwan-steering:dhcp":false,"address":[{"ip":"10.53.0.1","prefix-length":24},{"ip":"10.53.0.9","prefix-length":32}],"goodkind-mwan-steering:gateway":"10.53.0.2","goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":"10.53.0.3","internal":"192.0.2.3","delivery":"local"},{"external":"10.53.0.9","internal":"192.0.2.4","delivery":"local"}]}}`,
		),
	)
	for name, unit := range map[string]string{
		"10-service.network": "[Match]\nName=enservice0\n[Network]\nAddress=fd53::1/64\nAddress=10.53.0.1/24\nAddress=10.53.0.9/32\nIPv6AcceptRA=no\nKeepConfiguration=yes\n[Route]\nGateway=fd53::2\nMetric=500\n[Route]\nGateway=10.53.0.2\nMetric=500\n",
		"10-service.netdev":  "[NetDev]\nName=enservice0\nKind=vlan\n[VLAN]\nId=53\n",
		"10-parent.network":  "[Match]\nName=service-parent\n[Network]\nVLAN=enservice0\nIPv6AcceptRA=no\n",
	} {
		if err := os.WriteFile(filepath.Join(unitDir, name), []byte(unit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	requireSystemdOutput(t, "systemctl", "start", "systemd-udevd")
	requireSystemdOutput(t, "systemctl", "restart", "systemd-networkd")
	configPath := filepath.Join(root, "config.toml")
	configuration := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nnetworkd_timeout_seconds = 1\nreconcile_interval = \"100ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n",
		filepath.Join(root, "links.json"),
		filepath.Join(root, "addresses.json"),
		filepath.Join(root, "kernel.json"),
	)
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := testenv.ProtocolTestBinary(t)
	daemon := testenv.StartRuntimeDaemon(t, binary, configPath, root, "release-owned")
	defer testenv.KillOwnedRuntimeDaemon(t, daemon)
	defer func() {
		if t.Failed() {
			t.Log(testenv.RuntimeDaemonLog(t, daemon))
			testenv.SetRuntimeNamespace(t, gateway)
			for _, arguments := range [][]string{
				{"-4", "route", "show", "table", "all"},
				{"-4", "rule", "show"},
				{"-4", "neigh", "show"},
				{"-6", "route", "show", "table", "all"},
				{"-6", "rule", "show"},
				{"-6", "neigh", "show"},
			} {
				output, err := systemdFixtureOutput("ip", arguments...)
				t.Logf("release ip %v: %s (%v)", arguments, output, err)
			}
			output, err := systemdFixtureOutput("nft", "list", "ruleset")
			t.Logf("release ruleset: %s (%v)", output, err)
		}
	}()
	testenv.WaitStaticRuntimeAddress(t, daemon, "owned397", "10.39.7.1/24", true)
	testenv.WaitConfiguredRuntimeRoute(t, daemon, "10.39.9.0/24")
	testenv.WaitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:53::1", 10*time.Second)
	testenv.WaitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t, 500, nil, "")
	assertNPTServiceReply(t, gateway, provider.Namespace, lan.Namespace)
	testenv.WaitStaticRuntimeAddress(t, daemon, "enservice0", "10.53.0.9/32", true)
	testenv.AssertMappedRuntimeReply(
		t,
		daemon,
		gateway,
		provider.Namespace,
		lan.Namespace,
		"udp4",
		"10.53.0.9:53009",
		"192.0.2.4:53009",
	)
	receipt := runtimeScopedNPTReceipt(
		t,
		filepath.Join(root, "addresses.json"),
		"2001:db8:53::1/128",
	)
	testenv.KillOwnedRuntimeDaemon(t, daemon)
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"service-provider",
		"ietf-ip:ipv4",
		json.RawMessage(
			`{"goodkind-mwan-steering:dhcp":true,"address":[{"ip":"10.53.0.1","prefix-length":24},{"ip":"10.53.0.9","prefix-length":32}],"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":"10.53.0.3","internal":"192.0.2.3","delivery":"local"},{"external":"198.51.100.101","internal":"192.0.2.4","delivery":"local"}]}}`,
		),
	)
	unitPath := filepath.Join(unitDir, "10-service.network")
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	unit = []byte(strings.Replace(string(unit), "[Network]\n", "[Network]\nDHCP=ipv4\n", 1))
	unit = []byte(strings.Replace(string(unit), "[Route]\nGateway=10.53.0.2\nMetric=500\n", "", 1))
	if err := os.WriteFile(unitPath, unit, 0o644); err != nil {
		t.Fatal(err)
	}
	requireSystemdOutput(t, "networkctl", "reload")
	requireSystemdOutput(t, "networkctl", "reconfigure", "enservice0")
	daemon = testenv.StartRuntimeDaemon(t, binary, configPath, root, "release-dhcp-primary")
	defer testenv.KillOwnedRuntimeDaemon(t, daemon)
	testenv.WaitStaticRuntimeLog(t, daemon, "networkd DHCPv4 primary address is not configured")
	testenv.WaitStaticRuntimeAddress(t, daemon, "enservice0", "198.51.100.101/32", false)
	assertServiceMappingReceipts(t, root)
	testenv.SetRuntimeNamespace(t, provider.Namespace)
	upstream, err := netlink.LinkByName("wan-vlan")
	if err != nil {
		t.Fatal(err)
	}
	staticUpstream, err := netlink.ParseAddr("10.53.0.2/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(upstream, staticUpstream); err != nil {
		t.Fatal(err)
	}
	service := testenv.StartDHCPv4RuntimeKea(
		t,
		root,
		true,
		600,
		"198.51.100.2",
		"198.51.100.100",
		true,
	)
	defer testenv.StopProtocolServices(t, []testenv.ProtocolService{service})
	defer func() {
		if t.Failed() {
			content, err := os.ReadFile(service.LogPath)
			t.Logf("networkd DHCPv4 Kea log: %s (%v)", content, err)
			output, err := systemdFixtureOutput(
				"journalctl",
				"-u",
				"systemd-networkd",
				"-n",
				"100",
				"--no-pager",
			)
			t.Logf("networkd DHCPv4 journal: %s (%v)", output, err)
		}
	}()
	for _, destination := range []string{"/run/kea", "/var/lib/kea"} {
		t.Cleanup(func() {
			if err := unix.Unmount(destination, 0); err != nil {
				t.Error(err)
			}
		})
	}
	testenv.SetRuntimeNamespace(t, gateway)
	requireSystemdOutput(t, "networkctl", "reconfigure", "enservice0")
	testenv.WaitStaticRuntimeAddress(t, daemon, "enservice0", "198.51.100.101/32", true)
	observed, err := networkd.Addresses(context.Background(), "enservice0")
	if err != nil {
		t.Fatal(err)
	}
	acquired := false
	for _, address := range observed {
		acquired = acquired ||
			(address.Prefix.String() == "198.51.100.101/32" && address.ConfigSource == "DHCPv4" && address.ConfigState == "configured")
	}
	if !acquired {
		t.Fatalf("DHCPv4 primary source missing: %+v", observed)
	}
	assertServiceMappingReceipts(t, root)
	testenv.KillOwnedRuntimeDaemon(t, daemon)
	requireSystemdOutput(t, "systemctl", "kill", "--signal=STOP", "systemd-networkd")
	defer requireSystemdOutput(t, "systemctl", "kill", "--signal=CONT", "systemd-networkd")
	daemon = testenv.StartRuntimeDaemon(
		t,
		binary,
		configPath,
		root,
		"release-dhcp-observation-timeout",
	)
	defer testenv.KillOwnedRuntimeDaemon(t, daemon)
	testenv.WaitStaticRuntimeLog(
		t,
		daemon,
		"observe networkd acquisition: find networkd link enservice0: context deadline exceeded",
	)
	requireSystemdOutput(t, "systemctl", "kill", "--signal=CONT", "systemd-networkd")
	testenv.AssertRuntimeTranslationSettled(t, daemon)
	testenv.SetReleaseOwner(t, networkDir, "service-provider", "external")
	assertReleaseCommand(t, binary, configPath, "service-provider", "networkd", false)
	assertReleaseCommand(t, binary, configPath, "service-provider", "mwan", false)
	serviceUnit, err := os.ReadFile(filepath.Join(unitDir, "10-service.network"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(unitDir, "10-service.network"),
		[]byte("[Match]\nName=enservice0\n[Link]\nUnmanaged=yes\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	requireSystemdOutput(t, "networkctl", "reload")
	requireSystemdOutput(t, "networkctl", "reconfigure", "enservice0")
	assertReleaseCommand(t, binary, configPath, "service-provider", "networkd", false)
	testenv.KillOwnedRuntimeDaemon(t, daemon)
	daemon = testenv.StartRuntimeDaemon(t, binary, configPath, root, "release-networkd-mappings")
	defer testenv.KillOwnedRuntimeDaemon(t, daemon)
	waitReleaseCommand(t, binary, configPath, "service-provider", "networkd", daemon)
	requireSystemdOutput(t, "systemctl", "kill", "--signal=STOP", "systemd-networkd")
	defer requireSystemdOutput(t, "systemctl", "kill", "--signal=CONT", "systemd-networkd")
	deadline, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	output, deadlineError := exec.CommandContext(
		deadline,
		binary,
		"debug",
		"check-release",
		"service-provider",
		"networkd",
		"--config",
		configPath,
	).CombinedOutput()
	deadlineExpired := deadline.Err()
	cancel()
	requireSystemdOutput(t, "systemctl", "kill", "--signal=CONT", "systemd-networkd")
	if deadlineError == nil || deadlineExpired != nil ||
		!strings.Contains(string(output), "context deadline exceeded") {
		t.Fatalf(
			"stopped networkd did not fail within its configured observation deadline: %s (%v, outer deadline %v)",
			output,
			deadlineError,
			deadlineExpired,
		)
	}
	t.Logf("configured networkd observation deadline: %s", output)
	testenv.SetReleaseOwner(t, networkDir, "service-provider", "networkd")
	testenv.SetReleaseConnectionField(
		t,
		networkDir,
		"service-provider",
		"goodkind-mwan-steering:link-files",
		json.RawMessage(`"rendered"`),
	)
	if err := os.WriteFile(
		filepath.Join(unitDir, "10-service.network"),
		serviceUnit,
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	requireSystemdOutput(t, "networkctl", "reload")
	requireSystemdOutput(t, "networkctl", "reconfigure", "enservice0")
	waitNPTServiceForwarding(t, 500, nil, "")
	testenv.SetReleaseOwner(t, networkDir, "owned397", "external")
	assertReleaseCommand(t, binary, configPath, "owned397", "mwan", false)
	testenv.KillOwnedRuntimeDaemon(t, daemon)
	restarted := testenv.StartRuntimeDaemon(t, binary, configPath, root, "release-external")
	defer testenv.KillOwnedRuntimeDaemon(t, restarted)
	waitReleaseCommand(t, binary, configPath, "owned397", "mwan", restarted)
	testenv.WaitMappedRuntimeRule(t, restarted, "ip6", "nat", "2001:db8:53::1", 10*time.Second)
	testenv.WaitRuntimeAttachedEdge(t, "enservice0", "2001:db8:53::1")
	waitNPTServiceForwarding(t, 500, nil, "")
	assertNPTServiceReply(t, gateway, provider.Namespace, lan.Namespace)
	after := runtimeScopedNPTReceipt(t, filepath.Join(root, "addresses.json"), "2001:db8:53::1/128")
	if !reflect.DeepEqual(receipt, after) {
		t.Fatalf("ordinary release changed NPT receipt: before=%s after=%s", receipt, after)
	}
	testenv.KillOwnedRuntimeDaemon(t, restarted)
	linksPath := filepath.Join(root, "links.json")
	links, err := os.ReadFile(linksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linksPath); err != nil {
		t.Fatal(err)
	}
	outputText := assertReleaseCommand(t, binary, configPath, "owned397", "mwan", true)
	if !strings.Contains(outputText, `"links_present":false`) {
		t.Fatalf("missing journal presence was not reported: %s", outputText)
	}
	if err := os.WriteFile(linksPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertReleaseCommand(t, binary, configPath, "owned397", "mwan", false)
	if err := os.WriteFile(linksPath, links, 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.SetReleaseOwner(t, networkDir, "service-provider", "external")
	addressesPath := filepath.Join(root, "addresses.json")
	addresses, err := os.ReadFile(addressesPath)
	if err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	previousBoot := strings.ReplaceAll(
		string(addresses),
		strings.TrimSpace(string(boot)),
		"00000000-0000-0000-0000-000000000000",
	)
	if previousBoot == string(addresses) {
		t.Fatal("production address journal has no current boot ID")
	}
	if err := os.WriteFile(addressesPath, []byte(previousBoot), 0o600); err != nil {
		t.Fatal(err)
	}
	outputText = assertReleaseCommand(t, binary, configPath, "service-provider", "mwan", false)
	if !strings.Contains(outputText, `"previous_boot":true`) {
		t.Fatalf("previous-boot receipt was not reported: %s", outputText)
	}
	unchanged, err := os.ReadFile(addressesPath)
	if err != nil || string(unchanged) != previousBoot {
		t.Fatalf("read-only verifier changed the previous-boot journal: %v", err)
	}
	if err := os.WriteFile(addressesPath, addresses, 0o600); err != nil {
		t.Fatal(err)
	}
	assertReleaseCommand(t, binary, configPath, "service-provider", "mwan", false)
}

func assertServiceMappingReceipts(t *testing.T, root string) {
	t.Helper()
	receipts, err := netif.InspectOwnedRelease(
		connectionid.ID("service-provider"),
		filepath.Join(root, "links.json"),
		filepath.Join(root, "addresses.json"),
		filepath.Join(root, "kernel.json"),
	)
	if err != nil || receipts.OrdinaryObjects != 1 || receipts.Promotions != 1 ||
		receipts.NPTEdges != 1 {
		t.Fatalf("service mapping receipts differ: %+v (%v)", receipts, err)
	}
}

func addReleasePeerVLAN(t *testing.T, parentName, name string, id int, addresses []string) {
	t.Helper()
	parent, err := netlink.LinkByName(parentName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: name, ParentIndex: parent.Attrs().Index},
		VlanId:    id,
	}); err != nil {
		t.Fatal(err)
	}
	testenv.ConfigureRuntimeLink(t, name, addresses)
}

func assertReleaseCommand(
	t *testing.T,
	binary string,
	configPath string,
	id string,
	previous string,
	success bool,
) string {
	t.Helper()
	output, err := systemdFixtureOutput(
		binary,
		"debug",
		"check-release",
		id,
		previous,
		"--config",
		configPath,
	)
	t.Logf("release %s previous=%s output=%s error=%v", id, previous, output, err)
	if (err == nil) != success {
		t.Fatalf("release %s expected success=%v: %s (%v)", id, success, output, err)
	}
	return string(output)
}

func waitReleaseCommand(
	t *testing.T,
	binary string,
	configPath string,
	id string,
	previous string,
	daemon *testenv.RuntimeDaemon,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var output []byte
	var err error
	for time.Now().Before(deadline) {
		output, err = systemdFixtureOutput(
			binary,
			"debug",
			"check-release",
			id,
			previous,
			"--config",
			configPath,
		)
		if err == nil {
			t.Logf("release %s previous=%s: %s", id, previous, output)
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"release %s previous=%s did not complete: %s (%v); daemon=%s",
		id,
		previous,
		output,
		err,
		testenv.RuntimeDaemonLog(t, daemon),
	)
}
