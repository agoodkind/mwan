//go:build linux && firewallnetns

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	dhcpOwnedUplink        = "uplink0"
	dhcpOwnedUpstream      = "upstream0"
	dhcpOwnedHealth        = `"from-prio":55,"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,"check-interval":1,"targets-v4":["198.51.100.2"],"targets-v6":[]}}`
	dhcpOwnedNewServerPeer = "198.51.100.3/24"
)

type dhcpOwnedRuntime struct {
	*dhcpReconnectRuntime
	gateway  netns.NsHandle
	bridge   netns.NsHandle
	upstream netns.NsHandle
}

func writeDHCPOwnedRuntimeNetwork(t *testing.T, directory string) {
	t.Helper()
	writeDHCPv4RuntimeNetwork(t, directory)
	path := filepath.Join(directory, "network.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(content), `"from-prio":55}`, dhcpOwnedHealth, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dhcpOwnedAddLink(t *testing.T, link netlink.Link) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	added, err := netlink.LinkByName(link.Attrs().Name)
	if err != nil {
		t.Fatal(err)
	}
	return added
}

func dhcpOwnedSetAddress(t *testing.T, name, remove, add string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(add)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	if remove == "" {
		return
	}
	address, err = netlink.ParseAddr(remove)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(link, address); err != nil {
		t.Fatal(err)
	}
}

func startDHCPOwnedRuntime(t *testing.T, gateway netns.NsHandle) *dhcpOwnedRuntime {
	t.Helper()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
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
	writeDHCPOwnedRuntimeNetwork(t, networkDir)
	configPath := filepath.Join(root, "config.toml")
	config := "[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n" +
		"[ifmgr.modules.links]\nstate_file = \"" + filepath.Join(root, "owned-links.json") + "\"\n" +
		"[ifmgr.modules.addresses]\nstate_file = \"" + filepath.Join(root, "owned-addresses.json") + "\"\n" +
		"[ifmgr.modules.autoconfiguration]\nstate_file = \"" + filepath.Join(root, "kernel-policy.json") + "\"\n" +
		"[ifmgr.modules.health]\nstate_file = \"" + filepath.Join(root, "health.state") + "\"\n" +
		"[ifmgr.modules.wan.routes]\nhealth_state_file = \"" + filepath.Join(root, "health.state") + "\"\n" +
		"[wanconfig]\npublish = false\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	t.Cleanup(func() { _ = management.namespace.Close() })
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	t.Cleanup(func() { _ = downstream.namespace.Close() })
	bridgeHost := newRuntimePeer(t, gateway, "wan-under", "wan-host", nil, nil, "")
	t.Cleanup(func() { _ = bridgeHost.namespace.Close() })

	setRuntimeNamespace(t, bridgeHost.namespace)
	parent, err := netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	guestPort := dhcpOwnedAddLink(t, &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: parent.Attrs().Index}, VlanId: 101})
	bridge := dhcpOwnedAddLink(t, &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}})
	uplink := dhcpOwnedAddLink(t, &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: dhcpOwnedUplink}, PeerName: dhcpOwnedUpstream})
	for _, port := range []netlink.Link{guestPort, uplink} {
		if err := netlink.LinkSetMaster(port, bridge); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []netlink.Link{bridge, guestPort, uplink} {
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	upstreamLink, err := netlink.LinkByName(dhcpOwnedUpstream)
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	setRuntimeNamespace(t, bridgeHost.namespace)
	if err := netlink.LinkSetNsFd(upstreamLink, int(upstream)); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, upstream)
	dhcpOwnedSetAddress(t, dhcpOwnedUpstream, "", dhcpReconnectServerAddress+"/24")
	upstreamLink, err = netlink.LinkByName(dhcpOwnedUpstream)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(upstreamLink); err != nil {
		t.Fatal(err)
	}
	reconnect := &dhcpReconnectRuntime{}
	reconnect.captureLeaseRequests(t, dhcpOwnedUpstream)
	server, err := server4.NewServer(dhcpOwnedUpstream, &net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			reconnect.answer(conn, peer, packet, dhcpReconnectLongT1, dhcpReconnectLongT2)
		})
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("server4.Server.Serve: %v", err)
		}
	})
	setRuntimeNamespace(t, gateway)
	reconnect.daemon = startRuntimeDaemon(t, os.Getenv(dhcpAcquireBackoffBinaryEnv), configPath, root, "dhcpv4")
	t.Cleanup(func() { stopRuntimeDaemon(t, reconnect.daemon) })
	waitStaticRuntimeAddress(t, reconnect.daemon, "enatt0", dhcpReconnectOriginalAddress+"/24", true)
	reconnect.link, err = netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	return &dhcpOwnedRuntime{dhcpReconnectRuntime: reconnect, gateway: gateway, bridge: bridgeHost.namespace, upstream: upstream}
}

func TestOwnedDHCPv4ReconnectNAKReplacesLease(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPOwnedRuntime(t, dhcpReconnectGateway(t))
	reconnect.behavior.Store(int32(dhcpReconnectNAKOriginal))
	parent, err := netlink.LinkByName("wan-under")
	if err != nil {
		t.Fatal(err)
	}
	flappedAt := time.Now()
	if err := netlink.LinkSetDown(parent); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(parent); err != nil {
		t.Fatal(err)
	}
	reconnect.requireVerification(t, flappedAt)
	reconnect.requireReplacement(t, dhcpReconnectVerifyWait)
}

func TestOwnedDHCPv4UpstreamChangeBehindBridgeReplacesLease(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPOwnedRuntime(t, dhcpReconnectGateway(t))
	waitDHCPv4RuntimeState(t, reconnect.daemon, `"to":"healthy"`, dhcpReconnectVerifyWait)
	updates := make(chan netlink.LinkUpdate, 64)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	if err := netlink.LinkSubscribe(updates, done); err != nil {
		t.Fatal(err)
	}
	var guestUpdates atomic.Int64
	go func() {
		for update := range updates {
			if update.Attrs().Name == "enatt0" {
				guestUpdates.Add(1)
			}
		}
	}()
	changedAt := time.Now()
	reconnect.behavior.Store(int32(dhcpReconnectNAKOriginal))
	setRuntimeNamespace(t, reconnect.bridge)
	uplink, err := netlink.LinkByName(dhcpOwnedUplink)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(uplink); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, reconnect.upstream)
	dhcpOwnedSetAddress(t, dhcpOwnedUpstream, dhcpReconnectServerAddress+"/24", dhcpOwnedNewServerPeer)
	setRuntimeNamespace(t, reconnect.bridge)
	if err := netlink.LinkSetUp(uplink); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, reconnect.gateway)
	reconnect.requireVerification(t, changedAt)
	if count := guestUpdates.Load(); count != 0 {
		t.Fatalf("The upstream change behind the bridge produced %d link updates for the guest interface; expected 0. Daemon log: %s", count, runtimeLogTail(t, reconnect.daemon, 30))
	}
	reconnect.requireReplacement(t, dhcpReconnectVerifyWait)
}
