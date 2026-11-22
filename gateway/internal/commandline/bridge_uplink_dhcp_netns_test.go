//go:build linux && protocol

package commandline

import (
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TestOwnedDHCPv4UnrelatedBridgePortFlapDoesNotVerifyLease(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}

	gateway := dhcpReconnectGateway(t)
	reconnect := startDHCPOwnedRuntime(t, gateway)
	guest := newRuntimePeer(t, reconnect.bridge, "guest0", "guest-peer", nil, nil, "")
	t.Cleanup(func() { _ = guest.namespace.Close() })
	bridge, err := netlink.LinkByName("br0")
	if err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetHardwareAddr(bridge, bridge.Attrs().HardwareAddr); err != nil {
		t.Fatal(err)
	}

	port, err := netlink.LinkByName("guest0")
	if err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetMaster(port, bridge); err != nil {
		t.Fatal(err)
	}

	waitDHCPv4RuntimeState(t, reconnect.daemon, `"to":"healthy"`, dhcpReconnectVerifyWait)
	time.Sleep(2 * time.Second)
	packetsBefore := len(reconnect.snapshot())
	if err := netlink.LinkSetDown(port); err != nil {
		t.Fatal(err)
	}

	if err := netlink.LinkSetUp(port); err != nil {
		t.Fatal(err)
	}

	setRuntimeNamespace(t, gateway)
	time.Sleep(3 * time.Second)
	assertRuntimeDaemonRunning(t, reconnect.daemon)
	if count := len(reconnect.snapshot()) - packetsBefore; count != 0 {
		t.Fatalf(
			"Guest bridge port flap caused the client to send %d DHCP packets. Daemon log: %s",
			count,
			runtimeLogTail(t, reconnect.daemon, 30),
		)
	}
}
