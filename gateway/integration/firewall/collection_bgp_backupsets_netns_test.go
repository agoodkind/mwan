//go:build linux && firewall

package firewall_test

import (
	"net/netip"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

const bgpRuntimeSecondHome = "2001:db8:b01:ff::/64"

func TestBGPBackupSetsDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPBackupSetsDaemonRuntime)
}

func runBGPBackupSetsDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(testenv.BgpRuntimeHome)
	secondHome := netip.MustParsePrefix(bgpRuntimeSecondHome)
	first := testenv.BgpRuntimeDirectProvider(
		testenv.BgpRuntimeLocalASN,
		testenv.BgpRuntimeRemoteASN,
		defaultRoute.String(),
	)
	second := testenv.BgpRuntimeProvider{
		Id:           "second",
		Device:       "enbgp1",
		RouterDevice: "rtr-b",
		Network:      "2001:db8:c",
		Table:        300,
		Mark:         3,
		Tier:         0,
		Metric:       310,
		LocalASN:     testenv.BgpRuntimeLocalASN,
		RemoteASN:    testenv.BgpRuntimeRemoteASN,
		Learned:      defaultRoute,
		Exports:      nil,
		Session:      true,
	}
	reserve := testenv.BgpRuntimeProvider{
		Id:           "reserve",
		Device:       "enbgp2",
		RouterDevice: "rtr-c",
		Network:      "2001:db8:d",
		Table:        600,
		Mark:         4,
		Tier:         1,
		Metric:       320,
		LocalASN:     testenv.BgpRuntimeLocalASN,
		RemoteASN:    testenv.BgpRuntimeRemoteASN,
		Learned:      defaultRoute,
		Session:      true,
		Exports: []testenv.BgpRuntimeExport{
			{Prefix: testenv.BgpRuntimeHome, BackupFor: first.Id},
			{Prefix: bgpRuntimeSecondHome, BackupFor: second.Id},
		},
	}
	providers := []testenv.BgpRuntimeProvider{first, second, reserve}
	run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway, providers...)
	testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir, providers...)
	daemon := run.Start(t, "bgp-backup-sets")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	for _, provider := range providers {
		run.StartRouter(t, provider).Announce(t, defaultRoute, provider.Peer())
	}
	for _, provider := range providers {
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			provider.Device,
			"ipv6",
			"ready",
			"ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
	}
	reserveRouter := run.Routers[reserve.Id]
	testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, reserveRouter)

	lost := []struct {
		provider testenv.BgpRuntimeProvider
		prefix   netip.Prefix
	}{
		{provider: first, prefix: home},
		{provider: second, prefix: secondHome},
	}
	for _, loss := range lost {
		router := run.Routers[loss.provider.Id]
		router.DeleteNeighbor(t)
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			loss.provider.Device,
			"ipv6",
			"not-ready",
			"not-ready",
			testenv.TunnelRuntimeFailureWindow,
		)
		testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, reserveRouter, loss.prefix)
		testenv.RequireBGPRuntimeFamily(t, run.Read, reserve.Device, "ready", "ready")

		router.AddNeighbor(t)
		testenv.WaitTunnelRuntimeFamily(
			t,
			daemon,
			run.Read,
			loss.provider.Device,
			"ipv6",
			"ready",
			"ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
		testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, reserveRouter)
	}
}
