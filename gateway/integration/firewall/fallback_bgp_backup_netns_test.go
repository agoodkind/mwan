//go:build linux && firewall

package firewall_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestBGPBackupDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runBGPBackupDaemonRuntime)
}

func runBGPBackupDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	home := netip.MustParsePrefix(testenv.BgpRuntimeHome)
	primary := testenv.BgpRuntimeDirectProvider(
		testenv.BgpRuntimeLocalASN,
		testenv.BgpRuntimeRemoteASN,
		defaultRoute.String(),
	)
	backup := testenv.BgpRuntimeProvider{
		Id:           "reserve",
		Device:       "enbgp1",
		RouterDevice: "rtr-b",
		Network:      "2001:db8:c",
		Table:        300,
		Mark:         3,
		Tier:         1,
		Metric:       310,
		LocalASN:     testenv.BgpRuntimeLocalASN,
		RemoteASN:    testenv.BgpRuntimeRemoteASN,
		Learned:      defaultRoute,
		Exports: []testenv.BgpRuntimeExport{
			{Prefix: testenv.BgpRuntimeHome, BackupFor: primary.Id},
		},
		Session: true,
	}
	run.Topology = testenv.BuildBGPRuntimeTopology(t, run.Gateway, primary, backup)
	testenv.WriteBGPRuntimeNetwork(t, run.NetworkDir, primary, backup)
	if err := os.WriteFile(
		filepath.Join(run.Root, testenv.BgpRuntimeStateFile),
		[]byte("{corrupt"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	daemon := run.Start(t, "bgp-backup")
	testenv.WaitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	primaryRouter := run.StartRouter(t, primary)
	backupRouter := run.StartRouter(t, backup)
	primaryRouter.Announce(t, defaultRoute, primary.Peer())
	backupRouter.Announce(t, defaultRoute, backup.Peer())

	established := func(session testenv.BgpRuntimeSession) bool {
		return session.Established && len(session.Accepted) == 1
	}
	for _, provider := range []testenv.BgpRuntimeProvider{primary, backup} {
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
		testenv.WaitBGPRuntimeSession(
			t,
			daemon,
			run.Read,
			provider.Device,
			"an established state",
			established,
		)
		testenv.WaitBGPRuntimeGatewayRoute(t, daemon, run.Read, provider.Device, defaultRoute, true)
	}
	testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, primaryRouter, home)
	testenv.RequireBGPRuntimeIPv4(t, run, daemon)
	testenv.WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		primary.Device,
		"the advertised home prefix",
		func(session testenv.BgpRuntimeSession) bool {
			return len(session.Advertised) == 1
		},
	)
	testenv.WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		backup.Device,
		"no advertised prefix",
		func(session testenv.BgpRuntimeSession) bool {
			return session.Established && len(session.Advertised) == 0
		},
	)
	testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, backupRouter)

	primaryRouter.DeleteNeighbor(t)
	testenv.WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		primary.Device,
		"ipv6",
		"not-ready",
		"not-ready",
		testenv.TunnelRuntimeFailureWindow,
	)
	testenv.WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		primary.Device,
		"no established state",
		func(session testenv.BgpRuntimeSession) bool {
			return !session.Established && len(session.Accepted) == 0
		},
	)
	testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, backupRouter, home)
	testenv.WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		backup.Device,
		"an established state and the advertised home prefix",
		func(session testenv.BgpRuntimeSession) bool {
			return established(session) && len(session.Advertised) == 1
		},
	)
	testenv.RequireBGPRuntimeFamily(t, run.Read, backup.Device, "ready", "ready")
	testenv.WaitBGPRuntime(
		t,
		daemon,
		run.Read,
		"home route on the backup router only",
		testenv.TunnelRuntimeFailureWindow,
		func() bool {
			return backupRouter.KernelRoute(t, home) && !primaryRouter.KernelRoute(t, home)
		},
	)

	primaryRouter.AddNeighbor(t)
	testenv.WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		primary.Device,
		"ipv6",
		"ready",
		"ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, primaryRouter, home)
	testenv.WaitBGPRuntimeReceived(t, daemon, run.Read, backupRouter)
	testenv.WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		backup.Device,
		"an established state and no advertised prefix",
		func(session testenv.BgpRuntimeSession) bool {
			return established(session) && len(session.Advertised) == 0
		},
	)
}
