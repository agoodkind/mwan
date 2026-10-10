//go:build linux && firewallnetns

package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

// tunnelRuntimeStartsNotReady starts the daemon while the remote endpoint drops protocol 41.
// The new IPv6 path is not ready at startup. IPv4 works through the underlay.
func tunnelRuntimeStartsNotReady(t *testing.T, run tunnelRuntimeRun) *runtimeDaemon {
	t.Helper()
	setTunnelRuntimeFault(t, run.topology, true)
	writeTunnelRuntimeNetwork(t, run.networkDir, true)
	first := run.start(t, "tunnel-first")
	waitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	device := waitOwnedRuntimeLink(t, first, tunnelRuntimeDevice, 10*time.Second)
	if device.Type() != "sit" || device.Attrs().MTU != tunnelRuntimeMTU {
		t.Fatalf("tunnel device type=%s mtu=%d, want sit with MTU %d", device.Type(), device.Attrs().MTU, tunnelRuntimeMTU)
	}
	waitTunnelRuntimeFamily(t, first, run.read, tunnelRuntimeUnderlay, "ipv4", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitTunnelRuntimeFamily(t, first, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	waitTunnelRuntimeEndpointRoute(t, first, true)
	waitTunnelRuntimeFamily(t, first, run.read, tunnelRuntimeAlternate, "ipv4", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitTunnelRuntimeIPv4Selection(t, first, "an assignment over two providers", func(selection string) bool {
		return strings.Contains(selection, "mod 2")
	})
	requireTunnelRuntimeIPv4(t, first, run, 4101, "before the tunnel becomes ready")
	requireTunnelRuntimeNotForwarded(t, run.topology, 4201, "before the first successful probe")
	if input := tunnelRuntimeInputRules(t); !strings.Contains(input, tunnelRuntimeRemote) {
		t.Fatalf("the input chain has no protocol 41 permit for %s: %s", tunnelRuntimeRemote, input)
	}
	return first
}

// tunnelRuntimeBecomesReady enables the remote endpoint to accept protocol 41.
// One successful IPv6 probe makes the path ready. The provider does not have an IPv4 probe.
func tunnelRuntimeBecomesReady(t *testing.T, run tunnelRuntimeRun, daemon *runtimeDaemon) {
	t.Helper()
	setTunnelRuntimeFault(t, run.topology, false)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	entry, found := readTunnelRuntimeInterface(run.read, tunnelRuntimeDevice)
	if !found || len(entry.Steering.State.Probes) == 0 {
		t.Fatalf("the tunnel provider publishes no probe evidence: %+v", entry)
	}
	for _, probe := range entry.Steering.State.Probes {
		if probe.Family == "ipv4" && probe.Result != "none" || probe.Family == "ipv6" && probe.Result != "pass" {
			t.Fatalf("tunnel probe evidence = %+v, want an IPv6 pass and no IPv4 probe", entry.Steering.State.Probes)
		}
	}
}

func tunnelRuntimeTransfers(t *testing.T, run tunnelRuntimeRun, daemon *runtimeDaemon) {
	t.Helper()
	topology := run.topology
	capture := openTunnelRuntimeCapture(t, topology)
	exchanged := false
	defer func() {
		if exchanged {
			return
		}
		counts := make(map[string]int)
		for _, frame := range readTunnelRuntimeCapture(t, capture) {
			counts[fmt.Sprintf("ethertype=%#04x protocol=%d %s>%s inner %s>%s", frame.etherType, frame.protocol,
				frame.source, frame.destination, frame.innerSource, frame.innerDestination)]++
		}
		t.Logf("ISP link frames during the failed exchange: %v", counts)
	}()
	// The first connection sends packets sized to the client's 1500-byte link MTU.
	// The gateway answers with a packet-too-big response. The first connection completes without a
	// second attempt.
	firstPeer, err := tunnelRuntimeTransfer(t, topology, topology.remote, topology.client, "tcp6",
		net.JoinHostPort(tunnelRuntimeRemoteClient, "4210"), net.JoinHostPort(tunnelRuntimeClientV6, "0"), tunnelRuntimeTransferSize)
	if err != nil {
		t.Fatalf("first IPv6 transfer with packets above the tunnel MTU: %v; daemon=%s", err, tunnelRuntimeLog(t, daemon))
	}
	if firstPeer != netip.MustParseAddr(tunnelRuntimeClientV6) {
		t.Fatalf("the remote client observed source %s, want the unchanged client address %s", firstPeer, tunnelRuntimeClientV6)
	}
	if mtu := tunnelRuntimePathMTU(t, topology, topology.client, tunnelRuntimeRemoteClient); mtu != tunnelRuntimeMTU {
		t.Fatalf("internal client path MTU to the remote client = %d, want the tunnel MTU %d", mtu, tunnelRuntimeMTU)
	}
	requireTunnelRuntimeExchange(t, daemon, topology, 4220, tunnelRuntimeRecoverWindow)
	exchanged = true
	requireTunnelRuntimeEncapsulation(t, readTunnelRuntimeCapture(t, capture))
	requireTunnelRuntimeIPv4(t, daemon, run, 4102, "with the tunnel ready")
}

// tunnelRuntimeRepairsEndpointRoute deletes the endpoint route.
// The daemon restores the endpoint route without a periodic pass.
func tunnelRuntimeRepairsEndpointRoute(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	owned := waitTunnelRuntimeEndpointRoute(t, daemon, true)
	underlay, err := netlink.LinkByName(tunnelRuntimeUnderlay)
	if err != nil {
		t.Fatal(err)
	}
	if !owned.Gw.Equal(net.ParseIP(tunnelRuntimeISPGateway)) || owned.LinkIndex != underlay.Attrs().Index ||
		int(owned.Protocol) != tunnelRuntimeRouteProto {
		t.Fatalf("endpoint route = %+v, want %s via %s dev %s", owned, tunnelRuntimeRemote, tunnelRuntimeISPGateway, tunnelRuntimeUnderlay)
	}
	if err := netlink.RouteDel(&owned); err != nil {
		t.Fatal(err)
	}
	waitTunnelRuntimeEndpointRoute(t, daemon, true)
}
