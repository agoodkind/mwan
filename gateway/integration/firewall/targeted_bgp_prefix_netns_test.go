//go:build linux && firewall

package firewall_test

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

const bgpRuntimeOutsidePort = 18182

func TestBGPSpecificPrefixDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPSpecificPrefixDaemonRuntime)
}

func runBGPSpecificPrefixDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	specific := testenv.BgpRuntimeDirectProvider(
		testenv.BgpRuntimeLocalASN,
		testenv.BgpRuntimeLocalASN,
		"2001:db8:a:99::/64",
	)
	other := testenv.BgpRuntimeProvider{
		Id:           "other",
		Device:       "enbgp1",
		RouterDevice: "rtr-b",
		Network:      "2001:db8:c",
		Table:        300,
		Mark:         3,
		Tier:         1,
		Metric:       310,
		LocalASN:     testenv.BgpRuntimeLocalASN,
		RemoteASN:    testenv.BgpRuntimeRemoteASN,
		Learned:      defaultRoute,
		Exports:      []testenv.BgpRuntimeExport{{Prefix: testenv.BgpRuntimeHome, BackupFor: ""}},
		Session:      true,
	}
	run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway, specific, other)
	testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir, specific, other)
	daemon := run.Start(t, "bgp-specific")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	providers := []testenv.BgpRuntimeProvider{specific, other}
	for _, provider := range providers {
		run.StartRouter(t, provider).Announce(t, provider.Learned, provider.Peer())
	}
	for _, provider := range providers {
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			provider.Device,
			"ipv6",
			"ready",
			"ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
	}
	testenv.WaitBGPRuntimeReceived(
		t,
		daemon,
		run.Read,
		run.Routers[other.Id],
		netip.MustParsePrefix(testenv.BgpRuntimeHome),
	)
	testenv.RequireBGPRuntimeExchange(t, run, daemon, specific)

	testenv.SetRuntimeNamespace(t, run.Topology.Remotes[other.Id])
	destination := net.JoinHostPort(other.RemoteClient(), strconv.Itoa(bgpRuntimeOutsidePort))
	listener, err := net.Listen("tcp6", destination)
	if err != nil {
		t.Fatalf("listen on %s: %v", destination, err)
	}
	defer func() { _ = listener.Close() }()
	testenv.SetRuntimeNamespace(t, run.Topology.Client)
	local, err := net.ResolveTCPAddr("tcp6", net.JoinHostPort(testenv.TunnelRuntimeClientV6, "0"))
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: testenv.TunnelRuntimeFailureWindow}
	connection, err := dialer.Dial("tcp6", destination)
	testenv.SetRuntimeNamespace(t, run.Gateway)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("the internal client connected to %s through another provider", destination)
	}
	if !errors.Is(err, syscall.ENETUNREACH) && !errors.Is(err, syscall.EHOSTUNREACH) {
		t.Fatalf("dial %s: %v, want an ICMPv6 unreachable error", destination, err)
	}
	if tcpListener, isTCP := listener.(*net.TCPListener); isTCP {
		_ = tcpListener.SetDeadline(time.Now().Add(time.Second))
	}
	if accepted, acceptErr := listener.Accept(); acceptErr == nil {
		_ = accepted.Close()
		t.Fatalf("a connection arrived at %s through another provider", destination)
	}
}
