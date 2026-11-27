//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"testing"
	"time"
)

func (c *CombinedRun) Recovered(t *testing.T, device string, since time.Time, bound time.Duration) {
	t.Helper()
	measured := WaitCombinedReady(t, c.Daemon, c.Run.Read, device, "ipv6", since)
	RequireCombinedBound(t, device+" ready after the recovery", measured, bound)
	c.RequireAnnounced(t)
	RequireCombinedPaths(t, c.Daemon, c.Run.Read, c.Topology, c.Paths()...)
}

func CombinedRouteWithdrawal(t *testing.T, c *CombinedRun) {
	t.Helper()
	device := c.Provider.Device
	since := time.Now()
	c.Transit.WithdrawAll(t)
	measured := WaitCombinedUnavailable(t, c.Daemon, c.Run.Read, device, since, "bgp-route")
	RequireCombinedBound(t, "route withdrawal to unavailable", measured, c.FaultBound(t, device))
	WaitBGPRuntimeReceived(t, c.Daemon, c.Run.Read, c.Transit)
	RequireCombinedPaths(t, c.Daemon, c.Run.Read, c.Topology, CombinedOrdinary, TunnelRuntimeDevice)
	c.RequireOrdinaryUnchanged(t, since)

	restored := time.Now()
	c.Transit.Announce(t, CombinedDefault(), c.Provider.Peer())
	c.Recovered(t, device, restored, c.FaultBound(t, device))
	c.RequireOrdinaryUnchanged(t, since)
}

func CombinedSessionLoss(t *testing.T, c *CombinedRun) {
	t.Helper()
	device := c.Provider.Device
	since := time.Now()
	c.Transit.DeleteNeighbor(t)
	measured := WaitCombinedUnavailable(t, c.Daemon, c.Run.Read, device, since, "bgp-session")
	RequireCombinedBound(t, "session loss to unavailable", measured, c.FaultBound(t, device))
	WaitBGPRuntimeGatewayRoute(t, c.Daemon, c.Run.Read, device, CombinedDefault(), false)
	RequireCombinedPaths(t, c.Daemon, c.Run.Read, c.Topology, CombinedOrdinary, TunnelRuntimeDevice)
	c.RequireOrdinaryUnchanged(t, since)

	c.Transit.AddNeighbor(t)
	restored := time.Now()
	c.Recovered(t, device, restored, c.RecoverBound(t, device))
	c.RequireOrdinaryUnchanged(t, since)
}

func CombinedTunnelLoss(t *testing.T, c *CombinedRun) {
	t.Helper()
	since := time.Now()
	SetTunnelRuntimeFault(t, c.Topology.Tunnel, true)
	measured := WaitCombinedUnavailable(
		t,
		c.Daemon,
		c.Run.Read,
		TunnelRuntimeDevice,
		since,
		"probe",
		"bgp-session",
	)
	RequireCombinedBound(
		t,
		"tunnel loss to unavailable",
		measured,
		c.FaultBound(t, TunnelRuntimeDevice),
	)
	WaitBGPRuntime(
		t,
		c.Daemon,
		c.Run.Read,
		"endpoint without the home prefix",
		TunnelRuntimeRecoverWindow,
		func() bool {
			return !c.Tunnel.KernelRoute(t, CombinedHome())
		},
	)
	RequireCombinedPaths(t, c.Daemon, c.Run.Read, c.Topology, CombinedOrdinary, c.Provider.Device)
	c.RequireOrdinaryUnchanged(t, since)

	SetTunnelRuntimeFault(t, c.Topology.Tunnel, false)
	restored := time.Now()
	c.Recovered(t, TunnelRuntimeDevice, restored, c.RecoverBound(t, TunnelRuntimeDevice))
	c.RequireOrdinaryUnchanged(t, since)
}

func CombinedUnderlayLoss(t *testing.T, c *CombinedRun) {
	t.Helper()
	since := time.Now()
	SetTunnelRuntimeISPLink(t, c.Topology.Tunnel, false)
	measured := WaitCombinedUnavailable(
		t,
		c.Daemon,
		c.Run.Read,
		TunnelRuntimeDevice,
		since,
		"probe",
		"bgp-session",
		"underlay",
	)
	RequireCombinedBound(
		t,
		"underlay loss to unavailable",
		measured,
		c.FaultBound(t, TunnelRuntimeDevice),
	)
	WaitCombinedUnavailable(t, c.Daemon, c.Run.Read, TunnelRuntimeDevice, since, "underlay")
	RequireCombinedSince(t, c.Run.Read, TunnelRuntimeDevice, "ipv6")
	WaitBGPRuntime(
		t,
		c.Daemon,
		c.Run.Read,
		"underlay IPv4 path unavailable",
		TunnelRuntimeRecoverWindow,
		func() bool {
			return ReadCombinedPath(
				c.Run.Read,
				TunnelRuntimeUnderlay,
				"ipv4",
			).State == CombinedUnavailable
		},
	)
	RequireCombinedPaths(t, c.Daemon, c.Run.Read, c.Topology, CombinedOrdinary, c.Provider.Device)
	c.RequireIPv4PathsUnchanged(t, since, TunnelRuntimeAlternate, CombinedOrdinary)
	RequireCombinedUnchanged(t, c.Run.Read, since, "ipv6", CombinedOrdinary)

	SetTunnelRuntimeISPLink(t, c.Topology.Tunnel, true)
	restored := time.Now()
	WaitCombinedReady(t, c.Daemon, c.Run.Read, TunnelRuntimeUnderlay, "ipv4", restored)
	c.Recovered(t, TunnelRuntimeDevice, restored, c.RecoverBound(t, TunnelRuntimeDevice))
	c.RequireIPv4PathsUnchanged(t, since, TunnelRuntimeAlternate, CombinedOrdinary)
	RequireCombinedUnchanged(t, c.Run.Read, since, "ipv6", CombinedOrdinary)
	WaitTunnelRuntimeIPv4Selection(
		t,
		c.Daemon,
		"the assignment before the underlay fault",
		func(selection string) bool {
			return selection == c.Ipv4
		},
	)
}
