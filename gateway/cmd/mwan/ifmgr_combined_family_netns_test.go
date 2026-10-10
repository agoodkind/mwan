//go:build linux && firewallnetns

package main

import (
	"testing"
	"time"
)

func waitCombinedFamilyUnavailable(t *testing.T, c *combinedRun, family string, since time.Time) {
	t.Helper()
	transition := waitCombinedTransition(t, c.daemon, c.run.read, combinedOrdinary, family, since, "unavailable with the probe dependency",
		func(transition combinedTransition) bool {
			return transition.Current == combinedUnavailable && transition.Dependency == "probe"
		})
	requireCombinedBound(t, "ordinary "+family+" probe loss to unavailable", transition.At.Sub(since), c.faultBound(t, combinedOrdinary))
}

func combinedOrdinaryFamilyLoss(t *testing.T, c *combinedRun) {
	t.Helper()
	read := c.run.read
	since := time.Now()
	setCombinedOrdinaryFault(t, c, "ip6", true)
	waitCombinedFamilyUnavailable(t, c, "ipv6", since)
	requireCombinedPaths(t, c.daemon, read, c.topology, c.provider.device, tunnelRuntimeDevice)
	c.requireIPv4Unchanged(t, since, c.ipv4Providers()...)

	restored := time.Now()
	setCombinedOrdinaryFault(t, c, "ip6", false)
	measured := waitCombinedReady(t, c.daemon, read, combinedOrdinary, "ipv6", restored)
	requireCombinedBound(t, "ordinary ipv6 ready after the probe recovery", measured, c.faultBound(t, combinedOrdinary))
	requireCombinedPaths(t, c.daemon, read, c.topology, c.paths()...)
	c.requireIPv4Unchanged(t, since, c.ipv4Providers()...)

	since = time.Now()
	setCombinedOrdinaryFault(t, c, "ip", true)
	waitCombinedFamilyUnavailable(t, c, "ipv4", since)
	waitTunnelRuntimeIPv4Selection(t, c.daemon, "an assignment without the ordinary provider", func(selection string) bool {
		return selection != c.ipv4
	})
	requireCombinedUnchanged(t, read, since, "ipv6", c.paths()...)
	requireCombinedUnchanged(t, read, since, "ipv4", tunnelRuntimeUnderlay, tunnelRuntimeAlternate)
	requireCombinedPaths(t, c.daemon, read, c.topology, c.paths()...)

	restored = time.Now()
	setCombinedOrdinaryFault(t, c, "ip", false)
	measured = waitCombinedReady(t, c.daemon, read, combinedOrdinary, "ipv4", restored)
	requireCombinedBound(t, "ordinary ipv4 ready after the probe recovery", measured, c.faultBound(t, combinedOrdinary))
	waitTunnelRuntimeIPv4Selection(t, c.daemon, "the assignment before the ordinary IPv4 probe loss", func(selection string) bool {
		return selection == c.ipv4
	})
	requireCombinedUnchanged(t, read, since, "ipv6", c.paths()...)
}
