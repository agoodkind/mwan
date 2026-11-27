//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

const CombinedFaultTable = "combinedfault"

func SetCombinedOrdinaryFault(t *testing.T, c *CombinedRun, family string, dropped bool) {
	t.Helper()
	SetRuntimeNamespace(t, c.Topology.Destinations[CombinedOrdinary])
	defer SetRuntimeNamespace(t, c.Run.Gateway)
	if !dropped {
		RunRuntimeNFT(t, "delete", "table", family, CombinedFaultTable)
		return
	}
	RunRuntimeNFT(t, "add", "table", family, CombinedFaultTable)
	RunRuntimeNFT(
		t,
		"add",
		"chain",
		family,
		CombinedFaultTable,
		"input",
		"{ type filter hook input priority 0; }",
	)
	RunRuntimeNFT(t, "add", "rule", family, CombinedFaultTable, "input", "drop")
}

func SetCombinedOrdinaryLink(t *testing.T, c *CombinedRun, up bool) {
	t.Helper()
	SetCombinedOrdinaryFault(t, c, "inet", !up)
}

func CountCombinedConnectionAttempts(t *testing.T, c *CombinedRun) {
	t.Helper()
	SetRuntimeNamespace(t, c.Run.Topology.Routers[c.Provider.Id])
	defer SetRuntimeNamespace(t, c.Run.Gateway)
	RunRuntimeNFT(t, "add", "table", "ip6", CombinedAttemptTable)
	RunRuntimeNFT(
		t,
		"add",
		"chain",
		"ip6",
		CombinedAttemptTable,
		"input",
		"{ type filter hook input priority 0; }",
	)
	RunRuntimeNFT(
		t,
		"add",
		"rule",
		"ip6",
		CombinedAttemptTable,
		"input",
		"tcp",
		"dport",
		"179",
		"counter",
	)
}

func WaitCombinedConnectionAttempt(t *testing.T, c *CombinedRun) {
	t.Helper()
	SetRuntimeNamespace(t, c.Run.Topology.Routers[c.Provider.Id])
	defer SetRuntimeNamespace(t, c.Run.Gateway)
	WaitBGPRuntime(
		t,
		c.Daemon,
		c.Run.Read,
		"a refused connection attempt at the direct router",
		TunnelRuntimeRecoverWindow,
		func() bool {
			output, err := exec.Command(
				"nft",
				"list",
				"table",
				"ip6",
				CombinedAttemptTable,
			).CombinedOutput()
			return err == nil && strings.Contains(string(output), "counter packets") &&
				!strings.Contains(string(output), "counter packets 0 ")
		},
	)
	RunRuntimeNFT(t, "delete", "table", "ip6", CombinedAttemptTable)
}

func CombinedSinglePaths(t *testing.T, c *CombinedRun) {
	t.Helper()
	read := c.Run.Read
	since := time.Now()
	SetCombinedOrdinaryLink(t, c, false)
	measured := WaitCombinedUnavailable(
		t,
		c.Daemon,
		read,
		CombinedOrdinary,
		since,
		"probe",
		"gateway",
	)
	RequireCombinedBound(
		t,
		"ordinary circuit loss to unavailable",
		measured,
		c.FaultBound(t, CombinedOrdinary),
	)
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, c.Provider.Device, TunnelRuntimeDevice)
	c.RequireIPv4PathsUnchanged(t, since, TunnelRuntimeUnderlay, TunnelRuntimeAlternate)

	withdrawn := time.Now()
	c.Transit.WithdrawAll(t)
	WaitCombinedUnavailable(t, c.Daemon, read, c.Provider.Device, withdrawn, "bgp-route")
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, TunnelRuntimeDevice)
	announced := time.Now()
	c.Transit.Announce(t, CombinedDefault(), c.Provider.Peer())
	WaitCombinedReady(t, c.Daemon, read, c.Provider.Device, "ipv6", announced)

	dropped := time.Now()
	SetTunnelRuntimeFault(t, c.Topology.Tunnel, true)
	WaitCombinedUnavailable(t, c.Daemon, read, TunnelRuntimeDevice, dropped, "probe", "bgp-session")
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, c.Provider.Device)
	c.RequireIPv4PathsUnchanged(t, since, TunnelRuntimeUnderlay, TunnelRuntimeAlternate)

	restored := time.Now()
	SetTunnelRuntimeFault(t, c.Topology.Tunnel, false)
	SetCombinedOrdinaryLink(t, c, true)
	for _, family := range []string{"ipv4", "ipv6"} {
		measured = WaitCombinedReady(t, c.Daemon, read, CombinedOrdinary, family, restored)
		RequireCombinedBound(
			t,
			"ordinary "+family+" ready after the recovery",
			measured,
			c.FaultBound(t, CombinedOrdinary),
		)
	}
	c.Recovered(t, TunnelRuntimeDevice, restored, c.RecoverBound(t, TunnelRuntimeDevice))
	c.RequireIPv4PathsUnchanged(t, since, TunnelRuntimeUnderlay, TunnelRuntimeAlternate)
	WaitTunnelRuntimeIPv4Selection(
		t,
		c.Daemon,
		"the assignment before the ordinary circuit loss",
		func(selection string) bool {
			return selection == c.Ipv4
		},
	)
}
