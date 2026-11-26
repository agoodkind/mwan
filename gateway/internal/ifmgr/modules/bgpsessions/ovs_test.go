package bgpsessions_test

import (
	"context"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"

	"goodkind.io/mwan/gateway/internal/bgp"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/bgpsessions"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

func TestOVSAdvertisesConfiguredPrefixWithoutKernelRouting(t *testing.T) {
	prefix := netip.MustParsePrefix("2001:db8:ab::/48")
	session := bgpsessions.Session{
		WANRef: ifmgr.WANRef{ID: "upstream", Name: "upstream", Iface: "lo"},
		Config: bgp.SessionConfig{
			Name:             "upstream",
			Families:         []bgp.Family{bgp.FamilyIPv6Unicast},
			RouterID:         netip.MustParseAddr("192.0.2.1"),
			LocalASN:         64512,
			RemoteASN:        64513,
			PeerAddress:      netip.IPv6Loopback(),
			PeerPort:         179,
			KeepaliveSeconds: 1,
			RouteMetric:      101,
			HoldSeconds:      3,
			Export: []bgp.ExportRule{
				{Prefix: prefix, Mode: bgp.ExportAlways, NextHop: netip.IPv6Loopback()},
			},
		},
	}
	instance, err := bgpsessions.New(bgpsessions.Config{
		OVSDataplane: true,
		Sessions:     []bgpsessions.Session{session},
		StateFile:    filepath.Join(t.TempDir(), "sessions.json"),
	})
	if err != nil {
		t.Fatal(err)
	}

	module := instance.(*bgpsessions.Module)
	store := wanstate.New()
	log := slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		module.Wait()
	})
	if err := module.Init(ctx, &ifmgr.Env{Log: log, LiveState: store}); err != nil {
		t.Fatal(err)
	}

	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatal(err)
	}

	module.OnFamilyReadiness(ctx, log)
	states := store.Snapshot().BGPSessions["upstream"]
	if len(states) != 1 || !slices.Contains(states[0].Advertised, prefix) {
		t.Fatalf("BGP session did not originate %s: %+v", prefix, states)
	}
}
