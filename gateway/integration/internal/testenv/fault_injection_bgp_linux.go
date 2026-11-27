//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"
)

const (
	BgpRuntimeClientPort      = 18179
	BgpRuntimeRemotePort      = 18180
	BgpRuntimeISPPort         = 18181
	BgpRuntimeRestartTime     = 10
	BgpRuntimeRestartDuration = BgpRuntimeRestartTime * time.Second
)

func RequireBGPRuntimeIPv4(t *testing.T, run *BgpRuntimeRun, daemon *RuntimeDaemon) {
	t.Helper()
	gateway := TunnelRuntimeTopology{Gateway: run.Gateway}
	destination := net.JoinHostPort(TunnelRuntimeISPGateway, strconv.Itoa(BgpRuntimeISPPort))
	WaitTunnelRuntimeTransfer(
		t,
		daemon,
		gateway,
		run.Topology.Isp,
		run.Topology.Client,
		"tcp4",
		destination,
		net.JoinHostPort(TunnelRuntimeClientV4, "0"),
		TunnelRuntimeRecoverWindow,
	)
}

func RequireBGPRuntimeIPv4Unchanged(t *testing.T, run *BgpRuntimeRun, since time.Time) {
	t.Helper()
	entry, found := ReadTunnelRuntimeInterface(run.Read, TunnelRuntimeUnderlay)
	if state := entry.V4.Ownership; !found || state.Routing != "ready" ||
		state.Readiness != "ready" {
		t.Fatalf(
			"%s ipv4 routing=%s readiness=%s, want ready",
			TunnelRuntimeUnderlay,
			state.Routing,
			state.Readiness,
		)
	}
	for _, transition := range entry.Ownership.Transitions {
		evaluation := transition.Operation == "evaluate-routing" ||
			transition.Operation == "evaluate-forwarding"
		if transition.Family == "ipv4" && evaluation && !transition.At.Before(since) {
			t.Fatalf(
				"%s ipv4 changed during the IPv6 fault: %+v",
				TunnelRuntimeUnderlay,
				transition,
			)
		}
	}
}

func RequireBGPRuntimeExchange(
	t *testing.T,
	run *BgpRuntimeRun,
	daemon *RuntimeDaemon,
	provider BgpRuntimeProvider,
) {
	t.Helper()
	gateway := TunnelRuntimeTopology{Gateway: run.Gateway}
	remote := run.Topology.Remotes[provider.Id]
	client := netip.MustParseAddr(TunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(provider.RemoteClient())
	if !run.Routers[provider.Id].KernelRoute(t, netip.MustParsePrefix(BgpRuntimeHome)) {
		t.Fatalf("router has no BGP route to %s", BgpRuntimeHome)
	}
	observed := WaitTunnelRuntimeTransfer(
		t,
		daemon,
		gateway,
		remote,
		run.Topology.Client,
		"tcp6",
		net.JoinHostPort(remoteClient.String(), strconv.Itoa(BgpRuntimeRemotePort)),
		net.JoinHostPort(client.String(), "0"),
		TunnelRuntimeRecoverWindow,
	)
	if observed != client {
		t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
	}
	observed = WaitTunnelRuntimeTransfer(
		t,
		daemon,
		gateway,
		run.Topology.Client,
		remote,
		"tcp6",
		net.JoinHostPort(client.String(), strconv.Itoa(BgpRuntimeClientPort)),
		net.JoinHostPort(remoteClient.String(), "0"),
		TunnelRuntimeRecoverWindow,
	)
	if observed != remoteClient {
		t.Fatalf("internal listener observed %s, want the remote client %s", observed, remoteClient)
	}
	RequireBGPRuntimeIPv4(t, run, daemon)
}

func BgpRuntimePeerLoss(
	t *testing.T,
	run *BgpRuntimeRun,
	daemon *RuntimeDaemon,
	provider BgpRuntimeProvider,
) {
	t.Helper()
	router := run.Routers[provider.Id]
	home := netip.MustParsePrefix(BgpRuntimeHome)
	since := time.Now()
	router.DeleteNeighbor(t)
	WaitBGPRuntimeGatewayRoute(t, daemon, run.Read, provider.Device, provider.Learned, false)
	WaitBGPRuntimeReason(t, daemon, run.Read, provider.Device, since, BgpRuntimeNotEstablished)
	WaitBGPRuntime(
		t,
		daemon,
		run.Read,
		"router without the home prefix",
		TunnelRuntimeFailureWindow,
		func() bool {
			return !router.KernelRoute(t, home)
		},
	)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		provider.Device,
		"ipv6",
		"not-ready",
		"not-ready",
		TunnelRuntimeFailureWindow,
	)
	RequireBGPRuntimeIPv4(t, run, daemon)

	router.AddNeighbor(t)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		provider.Device,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitBGPRuntimeReceived(t, daemon, run.Read, router, home)
	RequireBGPRuntimeExchange(t, run, daemon, provider)
	RequireBGPRuntimeIPv4Unchanged(t, run, since)
}

func BgpRuntimeForwardingLoss(
	t *testing.T,
	run *BgpRuntimeRun,
	daemon *RuntimeDaemon,
	provider BgpRuntimeProvider,
) {
	t.Helper()
	router := run.Routers[provider.Id]
	since := time.Now()
	SetBGPRuntimeFault(t, run.Topology, provider, true)
	WaitBGPRuntimeReason(t, daemon, run.Read, provider.Device, since, BgpRuntimeProbeFailed)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		provider.Device,
		"ipv6",
		"not-ready",
		"not-ready",
		TunnelRuntimeFailureWindow,
	)
	WaitBGPRuntimeReceived(t, daemon, run.Read, router)
	WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		provider.Device,
		"an established state and no advertised prefix",
		func(session BgpRuntimeSession) bool {
			return session.Established && len(session.Advertised) == 0 && len(session.Accepted) == 1
		},
	)
	RequireBGPRuntimeIPv4(t, run, daemon)

	SetBGPRuntimeFault(t, run.Topology, provider, false)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		provider.Device,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitBGPRuntimeReceived(t, daemon, run.Read, router, netip.MustParsePrefix(BgpRuntimeHome))
	RequireBGPRuntimeIPv4Unchanged(t, run, since)
}

func BgpRuntimeGracefulRestart(
	t *testing.T,
	run *BgpRuntimeRun,
	first *RuntimeDaemon,
	provider BgpRuntimeProvider,
) *RuntimeDaemon {
	t.Helper()
	router := run.Routers[provider.Id]
	home := netip.MustParsePrefix(BgpRuntimeHome)
	router.RetainRoutesOnStop()
	StopRuntimeDaemon(t, first)
	WaitBGPRuntime(
		t,
		nil,
		run.Read,
		"retained routes while the daemon is stopped",
		BgpRuntimeRestartDuration,
		func() bool {
			return router.ObservedGracefulStop() &&
				BgpRuntimeGatewayRoute(t, provider.Device, provider.Learned) &&
				router.KernelRoute(t, home) && !router.HasFreshPath(t, home) &&
				slices.Equal(router.Received(t), []netip.Prefix{home})
		},
	)
	second := run.Start(t, "bgp-graceful")
	WaitBGPRuntime(
		t,
		second,
		run.Read,
		"the refreshed home prefix at the router",
		TunnelRuntimeRecoverWindow,
		func() bool {
			return router.HasFreshPath(t, home) && router.KernelRoute(t, home)
		},
	)
	WaitTunnelRuntimeFamily(
		t,
		second,
		run.Read,
		provider.Device,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitBGPRuntimeSession(
		t,
		second,
		run.Read,
		provider.Device,
		"the refreshed routes",
		func(session BgpRuntimeSession) bool {
			return session.Established && len(session.Retained) == 0 &&
				slices.Equal(session.Accepted, []string{provider.Learned.String()}) &&
				slices.Equal(session.Advertised, []string{home.String()})
		},
	)
	RequireBGPRuntimeExchange(t, run, second, provider)
	return second
}
