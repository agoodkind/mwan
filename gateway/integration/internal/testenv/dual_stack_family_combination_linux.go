//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"testing"
	"time"
)

func WaitCombinedFamilyUnavailable(t *testing.T, c *CombinedRun, family string, since time.Time) {
	t.Helper()
	transition := WaitCombinedTransition(
		t,
		c.Daemon,
		c.Run.Read,
		CombinedOrdinary,
		family,
		since,
		"unavailable with the probe dependency",
		func(transition CombinedTransition) bool {
			return transition.Current == CombinedUnavailable && transition.Dependency == "probe"
		},
	)
	RequireCombinedBound(
		t,
		"ordinary "+family+" probe loss to unavailable",
		transition.At.Sub(since),
		c.FaultBound(t, CombinedOrdinary),
	)
}

func CombinedOrdinaryFamilyLoss(t *testing.T, c *CombinedRun) {
	t.Helper()
	read := c.Run.Read
	since := time.Now()
	SetCombinedOrdinaryFault(t, c, "ip6", true)
	WaitCombinedFamilyUnavailable(t, c, "ipv6", since)
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, c.Provider.Device, TunnelRuntimeDevice)
	c.RequireIPv4Unchanged(t, since)

	restored := time.Now()
	SetCombinedOrdinaryFault(t, c, "ip6", false)
	measured := WaitCombinedReady(t, c.Daemon, read, CombinedOrdinary, "ipv6", restored)
	RequireCombinedBound(
		t,
		"ordinary ipv6 ready after the probe recovery",
		measured,
		c.FaultBound(t, CombinedOrdinary),
	)
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, c.Paths()...)
	c.RequireIPv4Unchanged(t, since)

	since = time.Now()
	SetCombinedOrdinaryFault(t, c, "ip", true)
	WaitCombinedFamilyUnavailable(t, c, "ipv4", since)
	WaitTunnelRuntimeIPv4Selection(
		t,
		c.Daemon,
		"an assignment without the ordinary provider",
		func(selection string) bool {
			return selection != c.Ipv4
		},
	)
	RequireCombinedUnchanged(t, read, since, "ipv6", c.Paths()...)
	RequireCombinedUnchanged(t, read, since, "ipv4", TunnelRuntimeUnderlay, TunnelRuntimeAlternate)
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, c.Paths()...)

	restored = time.Now()
	SetCombinedOrdinaryFault(t, c, "ip", false)
	measured = WaitCombinedReady(t, c.Daemon, read, CombinedOrdinary, "ipv4", restored)
	RequireCombinedBound(
		t,
		"ordinary ipv4 ready after the probe recovery",
		measured,
		c.FaultBound(t, CombinedOrdinary),
	)
	WaitTunnelRuntimeIPv4Selection(
		t,
		c.Daemon,
		"the assignment before the ordinary IPv4 probe loss",
		func(selection string) bool {
			return selection == c.Ipv4
		},
	)
	RequireCombinedUnchanged(t, read, since, "ipv6", c.Paths()...)
}
