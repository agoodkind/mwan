//go:build linux && firewallnetns

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestOOBDHCPv4BridgeUplinkReturnReplacesLease(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	gateway := dhcpReconnectGateway(t)
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "mwan-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
	setRuntimeLoopback(t)
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	t.Cleanup(func() { _ = downstream.namespace.Close() })
	guest := newRuntimePeer(t, gateway, "guest0", "guest-peer", nil, nil, "")
	t.Cleanup(func() { _ = guest.namespace.Close() })
	provider := newRuntimePeer(t, gateway, "uplink0", "upstream0", nil, nil, "")
	t.Cleanup(func() { _ = provider.namespace.Close() })
	uplinkParent, err := netlink.LinkByName("uplink0")
	if err != nil {
		t.Fatal(err)
	}
	guestPort, err := netlink.LinkByName("guest0")
	if err != nil {
		t.Fatal(err)
	}
	uplinkPort := dhcpOwnedAddLink(t, &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "uplink101", ParentIndex: uplinkParent.Attrs().Index}, VlanId: 101})
	bridge := dhcpOwnedAddLink(t, &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "enatt0"}})
	for _, port := range []netlink.Link{guestPort, uplinkPort} {
		if err := netlink.LinkSetMaster(port, bridge); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []netlink.Link{uplinkPort, bridge} {
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	setRuntimeNamespace(t, provider.namespace)
	upstream, err := netlink.LinkByName("upstream0")
	if err != nil {
		t.Fatal(err)
	}
	dhcpOwnedAddLink(t, &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "wan-vlan", ParentIndex: upstream.Attrs().Index}, VlanId: 101})
	configureRuntimeLink(t, "wan-vlan", []string{dhcpReconnectServerAddress + "/24"})
	reconnect := &dhcpReconnectRuntime{}
	server, err := server4.NewServer("wan-vlan", &net.UDPAddr{IP: net.IPv4zero, Port: 67},
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
	configPath := writeDHCPAcquireBackoffConfig(t, root)
	reconnect.daemon = startRoleDHCPv4RuntimeDaemon(t, os.Getenv(dhcpAcquireBackoffBinaryEnv), configPath, root, "oob")
	t.Cleanup(func() { stopRuntimeDaemon(t, reconnect.daemon) })
	waitStaticRuntimeAddress(t, reconnect.daemon, "enatt0", dhcpReconnectOriginalAddress+"/24", true)
	reconnect.link, err = netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}

	packetsBefore := len(reconnect.snapshot())
	if err := netlink.LinkSetDown(guestPort); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(guestPort); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if count := len(reconnect.snapshot()) - packetsBefore; count != 0 {
		t.Fatalf("Guest bridge port flap caused the client to send %d DHCP packets; daemon log: %s", count, runtimeLogTail(t, reconnect.daemon, 30))
	}

	reconnect.behavior.Store(int32(dhcpReconnectNAKOriginal))
	flappedAt := time.Now()
	if err := netlink.LinkSetDown(uplinkParent); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := netlink.LinkSetUp(uplinkParent); err != nil {
		t.Fatal(err)
	}
	reconnect.requireVerification(t, flappedAt)
	reconnect.requireReplacement(t, dhcpReconnectVerifyWait)
}
