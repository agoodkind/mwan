//go:build linux && firewallnetns

package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

const combinedFaultTable = "combinedfault"

func setCombinedOrdinaryFault(t *testing.T, c *combinedRun, family string, dropped bool) {
	t.Helper()
	setRuntimeNamespace(t, c.topology.destinations[combinedOrdinary])
	defer setRuntimeNamespace(t, c.run.gateway)
	if !dropped {
		runRuntimeNFT(t, "delete", "table", family, combinedFaultTable)
		return
	}
	runRuntimeNFT(t, "add", "table", family, combinedFaultTable)
	runRuntimeNFT(t, "add", "chain", family, combinedFaultTable, "input", "{ type filter hook input priority 0; }")
	runRuntimeNFT(t, "add", "rule", family, combinedFaultTable, "input", "drop")
}

func setCombinedOrdinaryLink(t *testing.T, c *combinedRun, up bool) {
	t.Helper()
	setCombinedOrdinaryFault(t, c, "inet", !up)
}

func waitCombinedConnectionAttempt(t *testing.T, c *combinedRun) {
	t.Helper()
	setRuntimeNamespace(t, c.run.topology.routers[c.provider.id])
	defer setRuntimeNamespace(t, c.run.gateway)
	runRuntimeNFT(t, "add", "table", "ip6", combinedAttemptTable)
	runRuntimeNFT(t, "add", "chain", "ip6", combinedAttemptTable, "input", "{ type filter hook input priority 0; }")
	runRuntimeNFT(t, "add", "rule", "ip6", combinedAttemptTable, "input", "tcp", "dport", "179", "counter")
	waitBGPRuntime(t, c.daemon, c.run.read, "a refused connection attempt at the direct router", tunnelRuntimeRecoverWindow, func() bool {
		output, err := exec.Command("nft", "list", "table", "ip6", combinedAttemptTable).CombinedOutput()
		return err == nil && strings.Contains(string(output), "counter packets") && !strings.Contains(string(output), "counter packets 0 ")
	})
	runRuntimeNFT(t, "delete", "table", "ip6", combinedAttemptTable)
}

func combinedSinglePaths(t *testing.T, c *combinedRun) {
	t.Helper()
	read := c.run.read
	since := time.Now()
	setCombinedOrdinaryLink(t, c, false)
	measured := waitCombinedUnavailable(t, c.daemon, read, combinedOrdinary, since, "probe", "gateway")
	requireCombinedBound(t, "ordinary circuit loss to unavailable", measured, c.faultBound(t, combinedOrdinary))
	requireCombinedPaths(t, c.daemon, read, c.topology, c.provider.device, tunnelRuntimeDevice)
	c.requireIPv4Unchanged(t, since, tunnelRuntimeUnderlay, tunnelRuntimeAlternate)

	withdrawn := time.Now()
	c.transit.withdrawAll(t)
	waitCombinedUnavailable(t, c.daemon, read, c.provider.device, withdrawn, "bgp-route")
	requireCombinedPaths(t, c.daemon, read, c.topology, tunnelRuntimeDevice)
	announced := time.Now()
	c.transit.announce(t, combinedDefault(), c.provider.peer())
	waitCombinedReady(t, c.daemon, read, c.provider.device, "ipv6", announced)

	dropped := time.Now()
	setTunnelRuntimeFault(t, c.topology.tunnel, true)
	waitCombinedUnavailable(t, c.daemon, read, tunnelRuntimeDevice, dropped, "probe", "bgp-session")
	requireCombinedPaths(t, c.daemon, read, c.topology, c.provider.device)
	c.requireIPv4Unchanged(t, since, tunnelRuntimeUnderlay, tunnelRuntimeAlternate)

	restored := time.Now()
	setTunnelRuntimeFault(t, c.topology.tunnel, false)
	setCombinedOrdinaryLink(t, c, true)
	for _, family := range []string{"ipv4", "ipv6"} {
		measured = waitCombinedReady(t, c.daemon, read, combinedOrdinary, family, restored)
		requireCombinedBound(t, "ordinary "+family+" ready after the recovery", measured, c.faultBound(t, combinedOrdinary))
	}
	c.recovered(t, tunnelRuntimeDevice, restored, c.recoverBound(t, tunnelRuntimeDevice))
	c.requireIPv4Unchanged(t, since, tunnelRuntimeUnderlay, tunnelRuntimeAlternate)
	waitTunnelRuntimeIPv4Selection(t, c.daemon, "the assignment before the ordinary circuit loss", func(selection string) bool {
		return selection == c.ipv4
	})
}
