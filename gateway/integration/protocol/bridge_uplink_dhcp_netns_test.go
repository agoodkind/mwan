//go:build linux && protocol

package protocol_test

import (
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestOwnedDHCPv4UnrelatedBridgePortFlapDoesNotVerifyLease(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}

	gateway := dhcpReconnectGateway(t)
	reconnect := startDHCPOwnedRuntime(t, gateway)
	guest := testenv.NewRuntimePeer(t, reconnect.bridge, "guest0", "guest-peer", nil, nil, "")
	t.Cleanup(func() { _ = guest.Namespace.Close() })
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

	testenv.SetRuntimeNamespace(t, gateway)
	time.Sleep(3 * time.Second)
	testenv.AssertRuntimeDaemonRunning(t, reconnect.daemon)
	if count := len(reconnect.snapshot()) - packetsBefore; count != 0 {
		t.Fatalf(
			"Guest bridge port flap caused the client to send %d DHCP packets. Daemon log: %s",
			count,
			testenv.RuntimeLogTail(t, reconnect.daemon, 30),
		)
	}
}
