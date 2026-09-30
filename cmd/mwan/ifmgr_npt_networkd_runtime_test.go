//go:build linux && firewallnetns

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestNetworkdNPTEdgeDaemonRuntime(t *testing.T) {
	if os.Getenv("MWAN_NETWORKD_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires a dedicated container with real systemd-networkd")
	}
	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(initName)) != "systemd" || os.Geteuid() != 0 {
		t.Fatalf("requires root and systemd PID 1: %q, %v", initName, err)
	}
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
	networkdResolverCommand(t, "systemctl", "start", "systemd-udevd")
	networkdResolverCommand(t, "systemctl", "set-environment", "SYSTEMD_LOG_LEVEL=debug")
	t.Cleanup(func() { networkdResolverCommand(t, "systemctl", "stop", "systemd-networkd") })
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "npt-mgmt", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	lan := newRuntimePeer(t, gateway, "enmwanbr0", "npt-lan", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.3/29", "2001:db8:b01:fe::2/64"}, "")
	defer lan.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enwebpass0", "wan-host", nil, []string{"198.51.100.2/24", "2001:db8:2::2/64"}, legacyRuntimeMAC)
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	waitAutoconfigurationLinkLocal(t, "wan-host")
	services := startProtocolServicesForInterface(t, root, "wan-host")
	defer stopProtocolServices(t, services)
	for _, destination := range []string{"/run/kea", "/var/lib/kea"} {
		t.Cleanup(func() {
			if err := unix.Unmount(destination, 0); err != nil {
				t.Errorf("unmount %s: %v", destination, err)
			}
		})
	}
	setRuntimeNamespace(t, gateway)
	writeStaticRuntimeNetwork(t, networkDir, "10.39.7.1", "fd39:7::1", "10.39.7.2", "fd39:7::2", 398, 24, true)
	writeMappedRuntimeProvider(t, networkDir, "10.39.7.3", "2001:db8:beef:300::/60")
	writeRuntimeLegacyNPT(t, networkDir, "2001:db8:30::/56", false)
	path := filepath.Join(networkDir, "network.json")
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input = []byte(strings.ReplaceAll(string(input), `"external-source":"configured","external-prefix":"2001:db8:30::/56"`, `"external-source":"delegated","expected-prefix":"2001:db8:30::/56"`))
	input = []byte(strings.ReplaceAll(string(input), `"ietf-ip:ipv6":{"goodkind-mwan-steering:translation"`, `"ietf-ip:ipv6":{"goodkind-mwan-steering:dhcp":true,"goodkind-mwan-steering:delegation":{"hint":"::/56","without-ra":"solicit","use-delegated-prefix":true},"goodkind-mwan-steering:translation"`))
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	unit := "[Match]\nName=enwebpass0\n[Network]\nDHCP=ipv6\nIPv6AcceptRA=yes\nKeepConfiguration=yes\n[IPv6AcceptRA]\nDHCPv6Client=always\n[DHCPv6]\nWithoutRA=solicit\nUseAddress=yes\nUseDelegatedPrefix=yes\nPrefixDelegationHint=::/56\n"
	if err := os.WriteFile(filepath.Join(unitDir, "10-legacy.network"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	networkdResolverCommand(t, "systemctl", "restart", "systemd-networkd")
	configuration := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"100ms\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.addresses]\nstate_file = %q\n", filepath.Join(root, "addresses.json"))
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := protocolTestBinary(t)
	daemon := startRuntimeDaemon(t, binary, configPath, root, "networkd-npt")
	defer killOwnedRuntimeDaemon(t, daemon)
	defer func() {
		if t.Failed() {
			t.Logf("network JSON: %s", input)
			t.Logf("complete networkd NPT daemon log: %s", runtimeDaemonLog(t, daemon))
			t.Logf("networkd state: %s", networkdResolverCommand(t, "networkctl", "status", "enwebpass0", "--no-pager"))
			t.Logf("networkd journal: %s", networkdResolverCommand(t, "journalctl", "-u", "systemd-networkd", "--no-pager", "-n", "150"))
			for _, service := range services {
				data, err := os.ReadFile(service.logPath)
				t.Logf("%s log: %s (%v)", service.name, data, err)
			}
		}
	}()
	waitMappedRuntimeRule(t, daemon, "ip6", "nat", "2001:db8:30::1", 10*time.Second)
	assertRuntimeNPTEdges(t, filepath.Join(root, "addresses.json"), "2001:db8:30::1/128")
	link, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	before, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	status := networkdResolverCommand(t, "networkctl", "status", "enwebpass0", "--no-pager")
	prefix, err := exec.Command(binary, "pd", "enwebpass0").Output()
	if err != nil || !strings.Contains(status, "10-legacy.network") || strings.TrimSpace(string(prefix)) != "2001:db8:30::/56" {
		t.Fatalf("networkd did not acquire the delegated prefix: prefix=%q error=%v status=%s", prefix, err, status)
	}
	killOwnedRuntimeDaemon(t, daemon)
	restarted := startRuntimeDaemon(t, binary, configPath, root, "networkd-npt-restarted")
	defer killOwnedRuntimeDaemon(t, restarted)
	waitMappedRuntimeRule(t, restarted, "ip6", "nat", "2001:db8:30::1", 10*time.Second)
	assertRuntimeNPTEdges(t, filepath.Join(root, "addresses.json"), "2001:db8:30::1/128")
	after, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range before {
		found := false
		for _, current := range after {
			if current.IPNet.String() == address.IPNet.String() {
				found = true
			}
		}
		if !found {
			t.Fatalf("restart removed networkd address %s", address.IPNet)
		}
	}
	networkdResolverCommand(t, "systemctl", "is-active", "systemd-networkd")
	assertRuntimeDaemonRunning(t, restarted)
}
