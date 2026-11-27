//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TunnelRuntimeStartsNotReady(t *testing.T, run TunnelRuntimeRun) *RuntimeDaemon {
	t.Helper()
	SetTunnelRuntimeFault(t, run.Topology, true)
	WriteTunnelRuntimeNetwork(t, run.NetworkDir, true)
	first := run.Start(t, "tunnel-first")
	WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
	device := WaitOwnedRuntimeLink(t, first, TunnelRuntimeDevice, 10*time.Second)
	if device.Type() != "sit" || device.Attrs().MTU != TunnelRuntimeMTU {
		t.Fatalf(
			"tunnel device type=%s mtu=%d, want sit with MTU %d",
			device.Type(),
			device.Attrs().MTU,
			TunnelRuntimeMTU,
		)
	}
	WaitTunnelRuntimeFamily(
		t,
		first,
		run.Read,
		TunnelRuntimeUnderlay,
		"ipv4",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitTunnelRuntimeFamily(
		t,
		first,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitTunnelRuntimeEndpointRoute(t, first, true)
	WaitTunnelRuntimeFamily(
		t,
		first,
		run.Read,
		TunnelRuntimeAlternate,
		"ipv4",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitTunnelRuntimeIPv4Selection(
		t,
		first,
		"an assignment over two providers",
		func(selection string) bool {
			return strings.Contains(selection, "mod 2")
		},
	)
	RequireTunnelRuntimeIPv4(t, first, run, 4101, "before the tunnel becomes ready")
	RequireTunnelRuntimeNotForwarded(t, run.Topology, 4201, "before the first successful probe")
	if input := TunnelRuntimeInputRules(t); !strings.Contains(input, TunnelRuntimeRemote) {
		t.Fatalf("the input chain has no protocol 41 permit for %s: %s", TunnelRuntimeRemote, input)
	}
	return first
}

func TunnelRuntimeBecomesReady(t *testing.T, run TunnelRuntimeRun, daemon *RuntimeDaemon) {
	t.Helper()
	SetTunnelRuntimeFault(t, run.Topology, false)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	entry, found := ReadTunnelRuntimeInterface(run.Read, TunnelRuntimeDevice)
	if !found || len(entry.Steering.State.Probes) == 0 {
		t.Fatalf("the tunnel provider publishes no probe evidence: %+v", entry)
	}
	for _, probe := range entry.Steering.State.Probes {
		if probe.Family == "ipv4" && probe.Result != "none" ||
			probe.Family == "ipv6" && probe.Result != "pass" {
			t.Fatalf(
				"tunnel probe evidence = %+v, want an IPv6 pass and no IPv4 probe",
				entry.Steering.State.Probes,
			)
		}
	}
}

func TunnelRuntimeTransfers(t *testing.T, run TunnelRuntimeRun, daemon *RuntimeDaemon) {
	t.Helper()
	topology := run.Topology
	capture := OpenTunnelRuntimeCapture(t, topology)
	exchanged := false
	defer func() {
		if exchanged {
			return
		}
		counts := make(map[string]int)
		for _, frame := range ReadTunnelRuntimeCapture(t, capture) {
			counts[fmt.Sprintf(
				"ethertype=%#04x protocol=%d %s>%s inner %s>%s",
				frame.EtherType,
				frame.Protocol,
				frame.Source,
				frame.Destination,
				frame.InnerSource,
				frame.InnerDestination,
			)]++
		}
		t.Logf("ISP link frames during the failed exchange: %v", counts)
	}()
	firstPeer, err := TunnelRuntimeTransfer(
		t,
		topology,
		topology.Remote,
		topology.Client,
		"tcp6",
		net.JoinHostPort(TunnelRuntimeRemoteClient, "4210"),
		net.JoinHostPort(TunnelRuntimeClientV6, "0"),
		TunnelRuntimeTransferSize,
	)
	if err != nil {
		t.Fatalf(
			"first IPv6 transfer with packets above the tunnel MTU: %v; daemon=%s",
			err,
			TunnelRuntimeLog(t, daemon),
		)
	}
	if firstPeer != netip.MustParseAddr(TunnelRuntimeClientV6) {
		t.Fatalf(
			"the remote client observed source %s, want the unchanged client address %s",
			firstPeer,
			TunnelRuntimeClientV6,
		)
	}
	if mtu := TunnelRuntimePathMTU(
		t,
		topology,
		topology.Client,
		TunnelRuntimeRemoteClient,
	); mtu != TunnelRuntimeMTU {
		t.Fatalf(
			"internal client path MTU to the remote client = %d, want the tunnel MTU %d",
			mtu,
			TunnelRuntimeMTU,
		)
	}
	RequireTunnelRuntimeExchange(t, daemon, topology, 4220, TunnelRuntimeRecoverWindow)
	exchanged = true
	RequireTunnelRuntimeEncapsulation(t, ReadTunnelRuntimeCapture(t, capture))
	RequireTunnelRuntimeIPv4(t, daemon, run, 4102, "with the tunnel ready")
}

func TunnelRuntimeRepairsEndpointRoute(t *testing.T, daemon *RuntimeDaemon) {
	t.Helper()
	owned := WaitTunnelRuntimeEndpointRoute(t, daemon, true)
	underlay, err := netlink.LinkByName(TunnelRuntimeUnderlay)
	if err != nil {
		t.Fatal(err)
	}
	if !owned.Gw.Equal(net.ParseIP(TunnelRuntimeISPGateway)) ||
		owned.LinkIndex != underlay.Attrs().Index ||
		int(owned.Protocol) != TunnelRuntimeRouteProto {
		t.Fatalf(
			"endpoint route = %+v, want %s via %s dev %s",
			owned,
			TunnelRuntimeRemote,
			TunnelRuntimeISPGateway,
			TunnelRuntimeUnderlay,
		)
	}
	if err := netlink.RouteDel(&owned); err != nil {
		t.Fatal(err)
	}
	WaitTunnelRuntimeEndpointRoute(t, daemon, true)
}
