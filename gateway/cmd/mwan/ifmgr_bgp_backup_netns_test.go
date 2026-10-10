//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBGPBackupDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPBackupDaemonRuntime)
}

func runBGPBackupDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(bgpRuntimeHome)
	primary := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, defaultRoute.String())
	backup := bgpRuntimeProvider{
		id: "reserve", device: "enbgp1", routerDevice: "rtr-b", network: "2001:db8:c", table: 300, mark: 3, tier: 1, metric: 310,
		localASN: bgpRuntimeLocalASN, remoteASN: bgpRuntimeRemoteASN, learned: defaultRoute,
		exports: []bgpRuntimeExport{{prefix: bgpRuntimeHome, backupFor: primary.id}}, session: true,
	}
	run.topology = buildBGPRuntimeTopology(t, run.gateway, primary, backup)
	writeBGPRuntimeNetwork(t, run.networkDir, primary, backup)
	if err := os.WriteFile(filepath.Join(run.root, bgpRuntimeStateFile), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := run.start(t, "bgp-backup")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	primaryRouter := run.startRouter(t, primary)
	backupRouter := run.startRouter(t, backup)
	primaryRouter.announce(t, defaultRoute, primary.peer())
	backupRouter.announce(t, defaultRoute, backup.peer())

	established := func(session bgpRuntimeSession) bool { return session.Established && len(session.Accepted) == 1 }
	for _, provider := range []bgpRuntimeProvider{primary, backup} {
		waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntimeSession(t, daemon, run.read, provider.device, "an established state", established)
		waitBGPRuntimeGatewayRoute(t, daemon, run.read, provider.device, defaultRoute, true)
	}
	waitBGPRuntimeReceived(t, daemon, run.read, primaryRouter, home)
	requireBGPRuntimeIPv4(t, run, daemon)
	waitBGPRuntimeSession(t, daemon, run.read, primary.device, "the advertised home prefix", func(session bgpRuntimeSession) bool {
		return len(session.Advertised) == 1
	})
	waitBGPRuntimeSession(t, daemon, run.read, backup.device, "no advertised prefix", func(session bgpRuntimeSession) bool {
		return session.Established && len(session.Advertised) == 0
	})
	waitBGPRuntimeReceived(t, daemon, run.read, backupRouter)

	primaryRouter.deleteNeighbor(t)
	waitTunnelRuntimeFamily(t, daemon, run.read, primary.device, "ipv6", "not-ready", "not-ready", tunnelRuntimeFailureWindow)
	waitBGPRuntimeSession(t, daemon, run.read, primary.device, "no established state", func(session bgpRuntimeSession) bool {
		return !session.Established && len(session.Accepted) == 0
	})
	waitBGPRuntimeReceived(t, daemon, run.read, backupRouter, home)
	waitBGPRuntimeSession(t, daemon, run.read, backup.device, "an established state and the advertised home prefix", func(session bgpRuntimeSession) bool {
		return established(session) && len(session.Advertised) == 1
	})
	requireBGPRuntimeFamily(t, run.read, backup.device, "ready", "ready")
	waitBGPRuntime(t, daemon, run.read, "home route on the backup router only", tunnelRuntimeFailureWindow, func() bool {
		return backupRouter.kernelRoute(t, home) && !primaryRouter.kernelRoute(t, home)
	})

	primaryRouter.addNeighbor(t)
	waitTunnelRuntimeFamily(t, daemon, run.read, primary.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeReceived(t, daemon, run.read, primaryRouter, home)
	waitBGPRuntimeReceived(t, daemon, run.read, backupRouter)
	waitBGPRuntimeSession(t, daemon, run.read, backup.device, "an established state and no advertised prefix", func(session bgpRuntimeSession) bool {
		return established(session) && len(session.Advertised) == 0
	})
}
