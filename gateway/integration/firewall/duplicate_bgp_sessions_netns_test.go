//go:build linux && firewall

package firewall_test

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

type bgpRepeatedSession struct {
	device  string
	learned netip.Prefix
	router  *testenv.BgpRuntimeRouter
}

func TestBGPRepeatedASNDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPRepeatedASNDaemonRuntime)
}

func addBGPRepeatedDirectProvider(
	t *testing.T,
	run *testenv.BgpRuntimeRun,
	provider testenv.BgpRuntimeProvider,
) {
	t.Helper()
	router := testenv.NewRuntimePeer(
		t,
		run.Gateway,
		provider.Device,
		provider.RouterDevice,
		[]string{provider.Local().String() + "/64"},
		[]string{provider.Peer().String() + "/64"},
		"",
	)
	remote := testenv.NewTunnelRuntimeNamespace(t)
	t.Cleanup(func() { _ = router.Namespace.Close() })
	t.Cleanup(func() { _ = remote.Close() })
	testenv.LinkTunnelRuntimeNamespaces(
		t,
		router.Namespace,
		provider.RouterDevice+"-far",
		[]string{provider.Network + ":99::1/64"},
		remote,
		"remote-host",
		[]string{provider.RemoteClient() + "/64"},
	)
	testenv.AddRuntimeDefault(t, "remote-host", provider.Network+":99::1")
	testenv.SetRuntimeNamespace(t, router.Namespace)
	testenv.WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	testenv.SetRuntimeNamespace(t, run.Gateway)
	run.Topology.Routers[provider.Id], run.Topology.Remotes[provider.Id] = router.Namespace, remote
}

func requireBGPRepeatedIntact(
	t *testing.T,
	run *testenv.BgpRuntimeRun,
	session bgpRepeatedSession,
	since time.Time,
) {
	t.Helper()
	entry, found := testenv.ReadBGPRuntimeInterface(run.Read, session.device)
	if !found || len(entry.V6.Ownership.Sessions) != 1 {
		t.Fatalf(
			"%s publishes the sessions %+v, want one session",
			session.device,
			entry.V6.Ownership.Sessions,
		)
	}
	published := entry.V6.Ownership.Sessions[0]
	if !published.Established ||
		!slices.Equal(published.Accepted, []string{session.learned.String()}) {
		t.Fatalf(
			"%s session = %+v, want an established state and the accepted prefix %s",
			session.device,
			published,
			session.learned,
		)
	}
	if !testenv.BgpRuntimeGatewayRoute(t, session.device, session.learned) {
		t.Fatalf("gateway has no BGP route to %s on %s", session.learned, session.device)
	}
	if received := session.router.Received(t); !slices.Equal(
		received,
		[]netip.Prefix{netip.MustParsePrefix(testenv.BgpRuntimeHome)},
	) {
		t.Fatalf(
			"router of %s received %v, want %s",
			session.device,
			received,
			testenv.BgpRuntimeHome,
		)
	}
	testenv.RequireBGPRuntimeIPv6Unchanged(t, run.Read, session.device, since)
}

func runBGPRepeatedASNDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	home := netip.MustParsePrefix(testenv.BgpRuntimeHome)
	vpsAddress := netip.MustParseAddr(testenv.TunnelRuntimeInnerRemote)
	direct := testenv.BgpRuntimeProvider{
		Id:           "direct",
		Device:       "enbgp1",
		RouterDevice: "rtr-b",
		Network:      "2001:db8:c",
		Table:        300,
		Mark:         3,
		Tier:         0,
		Metric:       310,
		LocalASN:     testenv.BgpRuntimeLocalASN,
		RemoteASN:    testenv.BgpRuntimeRemoteASN,
		Learned:      netip.MustParsePrefix("2001:db8:c:99::/64"),
		Exports:      []testenv.BgpRuntimeExport{{Prefix: testenv.BgpRuntimeHome, BackupFor: ""}},
		Session:      true,
	}
	topology := testenv.BuildBGPUpstreamRuntime(t, run)
	testenv.RemoveTunnelRuntimeReturnRoute(t, topology.TunnelRuntimeTopology)
	testenv.SetRuntimeNamespace(t, topology.Endpoint)
	testenv.AddRuntimeDefault(t, testenv.BgpUpstreamVPSDevice, testenv.BgpUpstreamAddress)
	testenv.SetRuntimeNamespace(t, run.Gateway)
	addBGPRepeatedDirectProvider(t, run, direct)
	testenv.WriteBGPTunnelRuntimeNetwork(
		t,
		run.NetworkDir,
		testenv.BgpTunnelRuntimeSession{
			Peer:      testenv.TunnelRuntimeInnerRemote,
			RemoteASN: testenv.BgpRuntimeRemoteASN,
			Tier:      1,
		},
		direct,
	)
	daemon := run.Start(t, "bgp-repeated")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)

	directRouter := run.StartRouter(t, direct)
	directRouter.Announce(t, direct.Learned, direct.Peer())
	tunnelRouter := testenv.StartBGPRuntimeRouter(
		t,
		topology.Endpoint,
		"far6in4",
		"198.51.100.1",
		testenv.BgpRuntimeRemoteASN,
		vpsAddress,
		netip.MustParseAddr(testenv.TunnelRuntimeInnerLocal),
		testenv.BgpRuntimeLocalASN,
	)
	tunnelRouter.Announce(t, netip.MustParsePrefix("::/0"), vpsAddress)
	sessions := []bgpRepeatedSession{
		{device: direct.Device, learned: direct.Learned, router: directRouter},
		{
			device:  testenv.TunnelRuntimeDevice,
			learned: netip.MustParsePrefix("::/0"),
			router:  tunnelRouter,
		},
	}
	ready := func(session bgpRepeatedSession) {
		t.Helper()
		testenv.WaitBGPRuntimeSession(
			t,
			daemon,
			run.Read,
			session.device,
			"an established state and the accepted prefix",
			func(published testenv.BgpRuntimeSession) bool {
				return published.Established &&
					slices.Equal(published.Accepted, []string{session.learned.String()})
			},
		)
		testenv.WaitBGPRuntimeGatewayRoute(
			t,
			daemon,
			run.Read,
			session.device,
			session.learned,
			true,
		)
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			session.device,
			"ipv6",
			"ready",
			"ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
		testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, session.router, home)
	}
	for _, session := range sessions {
		ready(session)
	}
	testenv.RequireBGPRuntimeExchange(t, run, daemon, direct)

	for index, lost := range sessions {
		other := sessions[1-index]
		since := time.Now()
		lost.router.DeleteNeighbor(t)
		testenv.WaitBGPRuntimeSession(
			t,
			daemon,
			run.Read,
			lost.device,
			"no established state",
			func(published testenv.BgpRuntimeSession) bool {
				return !published.Established && len(published.Accepted) == 0
			},
		)
		testenv.WaitBGPRuntimeReason(
			t,
			daemon,
			run.Read,
			lost.device,
			since,
			testenv.BgpRuntimeNotEstablished,
		)
		testenv.WaitBGPRuntimeGatewayRoute(t, daemon, run.Read, lost.device, lost.learned, false)
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			lost.device,
			"ipv6",
			"not-ready",
			"not-ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
		requireBGPRepeatedIntact(t, run, other, since)
		testenv.RequireBGPRuntimeIPv4(t, run, daemon)

		lost.router.AddNeighbor(t)
		ready(lost)
		requireBGPRepeatedIntact(t, run, other, since)
	}
	testenv.RequireBGPRuntimeExchange(t, run, daemon, direct)
}
