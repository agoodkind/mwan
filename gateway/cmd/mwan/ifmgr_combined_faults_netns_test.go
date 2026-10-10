//go:build linux && firewallnetns

package main

import (
	"testing"
	"time"
)

func (c *combinedRun) recovered(t *testing.T, device string, since time.Time, bound time.Duration) {
	t.Helper()
	measured := waitCombinedReady(t, c.daemon, c.run.read, device, "ipv6", since)
	requireCombinedBound(t, device+" ready after the recovery", measured, bound)
	c.requireAnnounced(t)
	requireCombinedPaths(t, c.daemon, c.run.read, c.topology, c.paths()...)
}

func combinedRouteWithdrawal(t *testing.T, c *combinedRun) {
	t.Helper()
	device := c.provider.device
	since := time.Now()
	c.transit.withdrawAll(t)
	measured := waitCombinedUnavailable(t, c.daemon, c.run.read, device, since, "bgp-route")
	requireCombinedBound(t, "route withdrawal to unavailable", measured, c.faultBound(t, device))
	waitBGPRuntimeReceived(t, c.daemon, c.run.read, c.transit)
	requireCombinedPaths(t, c.daemon, c.run.read, c.topology, combinedOrdinary, tunnelRuntimeDevice)
	c.requireOrdinaryUnchanged(t, since)

	restored := time.Now()
	c.transit.announce(t, combinedDefault(), c.provider.peer())
	c.recovered(t, device, restored, c.faultBound(t, device))
	c.requireOrdinaryUnchanged(t, since)
}

func combinedSessionLoss(t *testing.T, c *combinedRun) {
	t.Helper()
	device := c.provider.device
	since := time.Now()
	c.transit.deleteNeighbor(t)
	measured := waitCombinedUnavailable(t, c.daemon, c.run.read, device, since, "bgp-session")
	requireCombinedBound(t, "session loss to unavailable", measured, c.faultBound(t, device))
	waitBGPRuntimeGatewayRoute(t, c.daemon, c.run.read, device, combinedDefault(), false)
	requireCombinedPaths(t, c.daemon, c.run.read, c.topology, combinedOrdinary, tunnelRuntimeDevice)
	c.requireOrdinaryUnchanged(t, since)

	c.transit.addNeighbor(t)
	restored := time.Now()
	c.recovered(t, device, restored, c.recoverBound(t, device))
	c.requireOrdinaryUnchanged(t, since)
}

func combinedTunnelLoss(t *testing.T, c *combinedRun) {
	t.Helper()
	since := time.Now()
	setTunnelRuntimeFault(t, c.topology.tunnel, true)
	measured := waitCombinedUnavailable(t, c.daemon, c.run.read, tunnelRuntimeDevice, since, "probe", "bgp-session")
	requireCombinedBound(t, "tunnel loss to unavailable", measured, c.faultBound(t, tunnelRuntimeDevice))
	waitBGPRuntime(t, c.daemon, c.run.read, "endpoint without the home prefix", tunnelRuntimeRecoverWindow, func() bool {
		return !c.tunnel.kernelRoute(t, combinedHome())
	})
	requireCombinedPaths(t, c.daemon, c.run.read, c.topology, combinedOrdinary, c.provider.device)
	c.requireOrdinaryUnchanged(t, since)

	setTunnelRuntimeFault(t, c.topology.tunnel, false)
	restored := time.Now()
	c.recovered(t, tunnelRuntimeDevice, restored, c.recoverBound(t, tunnelRuntimeDevice))
	c.requireOrdinaryUnchanged(t, since)
}

func combinedUnderlayLoss(t *testing.T, c *combinedRun) {
	t.Helper()
	since := time.Now()
	setTunnelRuntimeISPLink(t, c.topology.tunnel, false)
	measured := waitCombinedUnavailable(t, c.daemon, c.run.read, tunnelRuntimeDevice, since, "probe", "bgp-session", "underlay")
	requireCombinedBound(t, "underlay loss to unavailable", measured, c.faultBound(t, tunnelRuntimeDevice))
	waitCombinedUnavailable(t, c.daemon, c.run.read, tunnelRuntimeDevice, since, "underlay")
	requireCombinedSince(t, c.run.read, tunnelRuntimeDevice, "ipv6")
	waitBGPRuntime(t, c.daemon, c.run.read, "underlay IPv4 path unavailable", tunnelRuntimeRecoverWindow, func() bool {
		return readCombinedPath(c.run.read, tunnelRuntimeUnderlay, "ipv4").State == combinedUnavailable
	})
	requireCombinedPaths(t, c.daemon, c.run.read, c.topology, combinedOrdinary, c.provider.device)
	c.requireIPv4PathsUnchanged(t, since, tunnelRuntimeAlternate, combinedOrdinary)
	requireCombinedUnchanged(t, c.run.read, since, "ipv6", combinedOrdinary)

	setTunnelRuntimeISPLink(t, c.topology.tunnel, true)
	restored := time.Now()
	waitCombinedReady(t, c.daemon, c.run.read, tunnelRuntimeUnderlay, "ipv4", restored)
	c.recovered(t, tunnelRuntimeDevice, restored, c.recoverBound(t, tunnelRuntimeDevice))
	c.requireIPv4PathsUnchanged(t, since, tunnelRuntimeAlternate, combinedOrdinary)
	requireCombinedUnchanged(t, c.run.read, since, "ipv6", combinedOrdinary)
	waitTunnelRuntimeIPv4Selection(t, c.daemon, "the assignment before the underlay fault", func(selection string) bool {
		return selection == c.ipv4
	})
}
