//go:build linux && firewallnetns

package main

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TestBGPTunnelDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPTunnelDaemonRuntime)
}

func bgpTunnelRuntimeNetwork(t *testing.T) string {
	t.Helper()
	targets := `"targets-v6":["` + tunnelRuntimeRemoteClient + `"]}`
	session := fmt.Sprintf(`,"bgp-session":[{"name":"endpoint","peer-address":%q,"local-address":%q,`+
		`"local-as":%d,"remote-as":%d,"router-id":"192.0.2.1","keepalive":%d,"hold":%d,"route-metric":300,`+
		`"import":[{"prefix":"::/0","min-length":0,"max-length":0}],`+
		`"export":[{"prefix":%q,"mode":"always","next-hop":%q}]}]`,
		tunnelRuntimeInnerRemote, tunnelRuntimeInnerLocal, bgpRuntimeLocalASN, bgpRuntimeRemoteASN,
		bgpRuntimeKeepalive, bgpRuntimeHold, bgpRuntimeHome, tunnelRuntimeInnerLocal)
	edits := map[string]string{
		`"goodkind-mwan-steering:gateway":"` + tunnelRuntimeInnerRemote + `",`: "",
		targets: targets + session,
	}
	network := tunnelRuntimeNetwork(true)
	for old, replacement := range edits {
		if count := strings.Count(network, old); count != 1 {
			t.Fatalf("edit target %q occurs %d times, want 1", old, count)
		}
		network = strings.Replace(network, old, replacement, 1)
	}
	return network
}

func removeTunnelRuntimeReturnRoute(t *testing.T, topology tunnelRuntimeTopology) {
	t.Helper()
	setRuntimeNamespace(t, topology.endpoint)
	defer setRuntimeNamespace(t, topology.gateway)
	link, err := netlink.LinkByName("far6in4")
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := net.ParseCIDR(bgpRuntimeHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteDel(&netlink.Route{Dst: destination, LinkIndex: link.Attrs().Index}); err != nil {
		t.Fatalf("remove the configured return route: %v", err)
	}
}

func runBGPTunnelDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(bgpRuntimeHome)
	client := netip.MustParseAddr(tunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(tunnelRuntimeRemoteClient)
	peer := netip.MustParseAddr(tunnelRuntimeInnerRemote)
	topology := buildTunnelRuntimeTopology(t, run.gateway)
	removeTunnelRuntimeReturnRoute(t, topology)
	if err := os.WriteFile(filepath.Join(run.networkDir, "network.json"), []byte(bgpTunnelRuntimeNetwork(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := run.start(t, "bgp-tunnel")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	router := startBGPRuntimeRouter(t, topology.endpoint, "far6in4", "198.51.100.1", bgpRuntimeRemoteASN, peer,
		netip.MustParseAddr(tunnelRuntimeInnerLocal), bgpRuntimeLocalASN)
	router.announce(t, defaultRoute, peer)
	established := func(session bgpRuntimeSession) bool {
		return session.Established && slices.Equal(session.Accepted, []string{defaultRoute.String()})
	}
	exchange := func() {
		t.Helper()
		waitBGPRuntimeSession(t, daemon, run.read, tunnelRuntimeDevice, "an established state and the default route", established)
		waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntimeReceived(t, daemon, run.read, router, home)
		observed := waitTunnelRuntimeTransfer(t, daemon, topology, topology.remote, topology.client, "tcp6",
			net.JoinHostPort(remoteClient.String(), strconv.Itoa(bgpRuntimeRemotePort)), net.JoinHostPort(client.String(), "0"),
			tunnelRuntimeRecoverWindow)
		if observed != client {
			t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
		}
		observed = waitTunnelRuntimeTransfer(t, daemon, topology, topology.client, topology.remote, "tcp6",
			net.JoinHostPort(client.String(), strconv.Itoa(bgpRuntimeClientPort)), net.JoinHostPort(remoteClient.String(), "0"),
			tunnelRuntimeRecoverWindow)
		if observed != remoteClient {
			t.Fatalf("internal listener observed %s, want the remote client %s", observed, remoteClient)
		}
	}
	exchange()

	setTunnelRuntimeFault(t, topology, true)
	waitBGPRuntimeSession(t, daemon, run.read, tunnelRuntimeDevice, "no established state", func(session bgpRuntimeSession) bool {
		return !session.Established && len(session.Accepted) == 0
	})
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeGatewayRoute(t, daemon, run.read, tunnelRuntimeDevice, defaultRoute, false)
	waitBGPRuntime(t, daemon, run.read, "endpoint without the home prefix", tunnelRuntimeRecoverWindow, func() bool {
		return !router.kernelRoute(t, home)
	})

	setTunnelRuntimeFault(t, topology, false)
	exchange()
}
