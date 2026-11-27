//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"strings"
	"testing"
	"time"
)

func TunnelRuntimeProbeFault(t *testing.T, run TunnelRuntimeRun, daemon *RuntimeDaemon) {
	t.Helper()
	topology := run.Topology
	faultStarted := time.Now().Add(-time.Second)
	SetTunnelRuntimeFault(t, topology, true)
	elapsed := WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		TunnelRuntimeFailureWindow,
	)
	t.Logf(
		"tunnel IPv6 readiness reported not-ready %s after the remote endpoint dropped protocol 41",
		elapsed,
	)
	WaitTunnelRuntimeReason(
		t,
		daemon,
		run.Read,
		faultStarted,
		"probe failed",
		TunnelRuntimeFailureWindow,
	)
	RequireTunnelRuntimeIPv4(t, daemon, run, 4103, "during the tunnel probe fault")
	RequireTunnelRuntimeNotForwarded(t, topology, 4230, "during the tunnel probe fault")
	SetTunnelRuntimeFault(t, topology, false)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	RequireTunnelRuntimeExchange(t, daemon, topology, 4240, TunnelRuntimeRecoverWindow)
	RequireTunnelRuntimeIPv4(t, daemon, run, 4104, "after the tunnel probe fault")
}

func TunnelRuntimeUnderlayFault(t *testing.T, run TunnelRuntimeRun, daemon *RuntimeDaemon) {
	t.Helper()
	topology := run.Topology
	daemonPID := daemon.Command.Process.Pid
	faultStarted := time.Now().Add(-time.Second)
	SetTunnelRuntimeISPLink(t, topology, false)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeUnderlay,
		"ipv4",
		"not-ready",
		"not-ready",
		TunnelRuntimeFailureWindow,
	)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		TunnelRuntimeFailureWindow,
	)
	WaitTunnelRuntimeReason(
		t,
		daemon,
		run.Read,
		faultStarted,
		"underlay not ready",
		TunnelRuntimeFailureWindow,
	)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeAlternate,
		"ipv4",
		"ready",
		"ready",
		time.Second,
	)
	WaitTunnelRuntimeIPv4Provider(
		t,
		daemon,
		run,
		4105,
		TunnelRuntimeAlternate,
		"with the underlay link down",
	)
	SetTunnelRuntimeISPLink(t, topology, true)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitTunnelRuntimeIPv4Baseline(t, daemon, run)
	RequireTunnelRuntimeExchange(t, daemon, topology, 4250, TunnelRuntimeRecoverWindow)
	RequireTunnelRuntimeIPv4(t, daemon, run, 4106, "after the underlay link returned")
	AssertRuntimeDaemonRunning(t, daemon)
	if daemon.Command.Process.Pid != daemonPID {
		t.Fatalf(
			"daemon PID changed from %d to %d during the underlay fault",
			daemonPID,
			daemon.Command.Process.Pid,
		)
	}
}

func TunnelRuntimeRestart(t *testing.T, run TunnelRuntimeRun, first *RuntimeDaemon) *RuntimeDaemon {
	t.Helper()
	device := WaitOwnedRuntimeLink(t, first, TunnelRuntimeDevice, 10*time.Second)
	tunnelIndex := device.Attrs().Index
	KillOwnedRuntimeDaemon(t, first)
	second := run.Start(t, "tunnel-second")
	WaitTunnelRuntimeFamily(
		t,
		second,
		run.Read,
		TunnelRuntimeDevice,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	if restarted := WaitOwnedRuntimeLink(
		t,
		second,
		TunnelRuntimeDevice,
		10*time.Second,
	); restarted.Attrs().Index != tunnelIndex {
		t.Fatalf(
			"restart recreated the tunnel device: index %d, want %d",
			restarted.Attrs().Index,
			tunnelIndex,
		)
	}
	WaitTunnelRuntimeIPv4Baseline(t, second, run)
	RequireTunnelRuntimeExchange(t, second, run.Topology, 4260, TunnelRuntimeRecoverWindow)
	RequireTunnelRuntimeIPv4(t, second, run, 4107, "after the daemon restart")
	return second
}

func TunnelRuntimeRemoval(t *testing.T, run TunnelRuntimeRun, second *RuntimeDaemon) {
	t.Helper()
	KillOwnedRuntimeDaemon(t, second)
	WriteTunnelRuntimeNetwork(t, run.NetworkDir, false)
	third := run.Start(t, "tunnel-removed")
	WaitOwnedRuntimeAbsent(t, third, TunnelRuntimeDevice, 10*time.Second)
	WaitTunnelRuntimeEndpointRoute(t, third, false)
	WaitTunnelRuntimeIPv4Baseline(t, third, run)
	if input := TunnelRuntimeInputRules(t); strings.Contains(input, TunnelRuntimeRemote) {
		t.Fatalf("the input chain still has the protocol 41 permit: %s", input)
	}
	RequireTunnelRuntimeIPv4(t, third, run, 4108, "after the tunnel removal")
}
