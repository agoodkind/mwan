//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"testing"
	"time"
)

const bgpRuntimeSecondHome = "2001:db8:b01:ff::/64"

func TestBGPBackupSetsDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPBackupSetsDaemonRuntime)
}

func runBGPBackupSetsDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(bgpRuntimeHome)
	secondHome := netip.MustParsePrefix(bgpRuntimeSecondHome)
	first := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, defaultRoute.String())
	second := bgpRuntimeProvider{
		id: "second", device: "enbgp1", routerDevice: "rtr-b", network: "2001:db8:c", table: 300, mark: 3, tier: 0, metric: 310,
		localASN: bgpRuntimeLocalASN, remoteASN: bgpRuntimeRemoteASN, learned: defaultRoute, exports: nil, session: true,
	}
	reserve := bgpRuntimeProvider{
		id: "reserve", device: "enbgp2", routerDevice: "rtr-c", network: "2001:db8:d", table: 600, mark: 4, tier: 1, metric: 320,
		localASN: bgpRuntimeLocalASN, remoteASN: bgpRuntimeRemoteASN, learned: defaultRoute, session: true,
		exports: []bgpRuntimeExport{
			{prefix: bgpRuntimeHome, backupFor: first.id},
			{prefix: bgpRuntimeSecondHome, backupFor: second.id},
		},
	}
	providers := []bgpRuntimeProvider{first, second, reserve}
	run.topology = buildBGPRuntimeTopology(t, run.gateway, providers...)
	writeBGPRuntimeNetwork(t, run.networkDir, providers...)
	daemon := run.start(t, "bgp-backup-sets")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	for _, provider := range providers {
		run.startRouter(t, provider).announce(t, defaultRoute, provider.peer())
	}
	for _, provider := range providers {
		waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	}
	reserveRouter := run.routers[reserve.id]
	waitBGPRuntimeReceived(t, daemon, run.read, reserveRouter)

	lost := []struct {
		provider bgpRuntimeProvider
		prefix   netip.Prefix
	}{{provider: first, prefix: home}, {provider: second, prefix: secondHome}}
	for _, loss := range lost {
		router := run.routers[loss.provider.id]
		router.deleteNeighbor(t)
		waitTunnelRuntimeFamily(t, daemon, run.read, loss.provider.device, "ipv6", "not-ready", "not-ready", tunnelRuntimeFailureWindow)
		waitBGPRuntimeReceived(t, daemon, run.read, reserveRouter, loss.prefix)
		requireBGPRuntimeFamily(t, run.read, reserve.device, "ready", "ready")

		router.addNeighbor(t)
		waitTunnelRuntimeFamily(t, daemon, run.read, loss.provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntimeReceived(t, daemon, run.read, reserveRouter)
	}
}
