//go:build linux && firewallnetns

package main

import (
	"strings"
	"testing"
	"time"
)

func tunnelRuntimeProbeFault(t *testing.T, run tunnelRuntimeRun, daemon *runtimeDaemon) {
	t.Helper()
	topology := run.topology
	faultStarted := time.Now().Add(-time.Second)
	setTunnelRuntimeFault(t, topology, true)
	elapsed := waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeFailureWindow)
	t.Logf("tunnel IPv6 readiness reported not-ready %s after the remote endpoint dropped protocol 41", elapsed)
	waitTunnelRuntimeReason(t, daemon, run.read, faultStarted, "probe failed", tunnelRuntimeFailureWindow)
	requireTunnelRuntimeIPv4(t, daemon, run, 4103, "during the tunnel probe fault")
	requireTunnelRuntimeNotForwarded(t, topology, 4230, "during the tunnel probe fault")
	setTunnelRuntimeFault(t, topology, false)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	requireTunnelRuntimeExchange(t, daemon, topology, 4240, tunnelRuntimeRecoverWindow)
	requireTunnelRuntimeIPv4(t, daemon, run, 4104, "after the tunnel probe fault")
}

func tunnelRuntimeUnderlayFault(t *testing.T, run tunnelRuntimeRun, daemon *runtimeDaemon) {
	t.Helper()
	topology := run.topology
	daemonPID := daemon.command.Process.Pid
	faultStarted := time.Now().Add(-time.Second)
	setTunnelRuntimeISPLink(t, topology, false)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeUnderlay, "ipv4", "not-ready", "not-ready", tunnelRuntimeFailureWindow)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeFailureWindow)
	waitTunnelRuntimeReason(t, daemon, run.read, faultStarted, "underlay not ready", tunnelRuntimeFailureWindow)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeAlternate, "ipv4", "ready", "ready", time.Second)
	waitTunnelRuntimeIPv4Provider(t, daemon, run, 4105, tunnelRuntimeAlternate, "with the underlay link down")
	setTunnelRuntimeISPLink(t, topology, true)
	waitTunnelRuntimeFamily(t, daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitTunnelRuntimeIPv4Baseline(t, daemon, run)
	requireTunnelRuntimeExchange(t, daemon, topology, 4250, tunnelRuntimeRecoverWindow)
	requireTunnelRuntimeIPv4(t, daemon, run, 4106, "after the underlay link returned")
	assertRuntimeDaemonRunning(t, daemon)
	if daemon.command.Process.Pid != daemonPID {
		t.Fatalf("daemon PID changed from %d to %d during the underlay fault", daemonPID, daemon.command.Process.Pid)
	}
}

func tunnelRuntimeRestart(t *testing.T, run tunnelRuntimeRun, first *runtimeDaemon) *runtimeDaemon {
	t.Helper()
	device := waitOwnedRuntimeLink(t, first, tunnelRuntimeDevice, 10*time.Second)
	tunnelIndex := device.Attrs().Index
	killOwnedRuntimeDaemon(t, first)
	second := run.start(t, "tunnel-second")
	waitTunnelRuntimeFamily(t, second, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	if restarted := waitOwnedRuntimeLink(t, second, tunnelRuntimeDevice, 10*time.Second); restarted.Attrs().Index != tunnelIndex {
		t.Fatalf("restart recreated the tunnel device: index %d, want %d", restarted.Attrs().Index, tunnelIndex)
	}
	waitTunnelRuntimeIPv4Baseline(t, second, run)
	requireTunnelRuntimeExchange(t, second, run.topology, 4260, tunnelRuntimeRecoverWindow)
	requireTunnelRuntimeIPv4(t, second, run, 4107, "after the daemon restart")
	return second
}

func tunnelRuntimeRemoval(t *testing.T, run tunnelRuntimeRun, second *runtimeDaemon) {
	t.Helper()
	killOwnedRuntimeDaemon(t, second)
	writeTunnelRuntimeNetwork(t, run.networkDir, false)
	third := run.start(t, "tunnel-removed")
	waitOwnedRuntimeAbsent(t, third, tunnelRuntimeDevice, 10*time.Second)
	waitTunnelRuntimeEndpointRoute(t, third, false)
	waitTunnelRuntimeIPv4Baseline(t, third, run)
	if input := tunnelRuntimeInputRules(t); strings.Contains(input, tunnelRuntimeRemote) {
		t.Fatalf("the input chain still has the protocol 41 permit: %s", input)
	}
	requireTunnelRuntimeIPv4(t, third, run, 4108, "after the tunnel removal")
}
