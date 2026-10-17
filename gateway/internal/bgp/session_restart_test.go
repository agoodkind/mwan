package bgp_test

import (
	"net/netip"
	"testing"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"

	"goodkind.io/mwan/internal/bgp"
)

const sessionRestartSeconds uint32 = 60

func startGracefulTestPeer(t *testing.T, address netip.Addr) *testPeer {
	t.Helper()
	return startTestPeerWithOptions(t, testPeerOptions{
		asn:          externalPeerASN,
		routerID:     "192.0.2.2",
		listen:       address,
		neighbor:     address,
		neighborASN:  sessionASN,
		neighborPort: 0,
		families:     []apipb.Family_Afi{apipb.Family_AFI_IP6},
		restartTime:  sessionRestartSeconds,
	})
}

// peerPath reports whether the Adj-RIB-In of the peer lists a path for prefix
// and whether the peer marked that path stale after the session stopped.
func peerPath(t *testing.T, peer *testPeer, prefix netip.Prefix) (present, stale bool) {
	t.Helper()
	path, found := peer.received(t)[prefix]
	if !found {
		return false, false
	}
	return true, path.Stale
}

// stopKeepingExport stops an established session that advertises exported
// with StopRetaining, and waits until the peer marks the path stale.
func stopKeepingExport(t *testing.T, session *bgp.Session, peer *testPeer, exported netip.Prefix) {
	t.Helper()
	waitEstablished(t, session)
	if err := session.SetAdvertisement(true, nil); err != nil {
		t.Fatalf("SetAdvertisement: %v", err)
	}
	waitReceived(t, peer, exported)
	if err := session.StopRetaining(t.Context()); err != nil {
		t.Fatalf("StopRetaining: %v", err)
	}
	eventually(t, "the peer to mark the exported path stale", func() bool {
		present, stale := peerPath(t, peer, exported)
		return present && stale
	})
}

// restartKeepingRoutes creates and starts the session of the next process.
func restartKeepingRoutes(t *testing.T, cfg bgp.SessionConfig) *bgp.Session {
	t.Helper()
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := session.KeepRoutes(t.Context(), time.Now().Add(time.Duration(sessionRestartSeconds)*time.Second)); err != nil {
		t.Fatalf("KeepRoutes: %v", err)
	}
	if err := session.Start(t.Context()); err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Stop(t.Context()); err != nil {
			t.Errorf("stop session: %v", err)
		}
	})
	return session
}

func gracefulExportConfig(name string, peer *testPeer, exported netip.Prefix) bgp.SessionConfig {
	cfg := baseSessionConfig(name, externalPeerASN, peer)
	cfg.GracefulRestartSeconds = sessionRestartSeconds
	cfg.Export = []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: netip.MustParseAddr("2001:db8:ffff::2")}}
	return cfg
}

func TestSessionRestartRefreshesTheAdvertisedPrefixAtThePeer(t *testing.T) {
	exported := netip.MustParsePrefix("3fff:a::/48")
	peer := startGracefulTestPeer(t, netip.IPv6Loopback())
	cfg := gracefulExportConfig("restart", peer, exported)
	stopKeepingExport(t, startSession(t, cfg), peer, exported)

	restarted := restartKeepingRoutes(t, cfg)
	if present, stale := peerPath(t, peer, exported); restarted.State().Established || !present || !stale {
		t.Fatalf("restarted session established=%t, peer path present=%t stale=%t, want no connection before Connect",
			restarted.State().Established, present, stale)
	}
	if err := restarted.SetAdvertisement(true, nil); err != nil {
		t.Fatalf("SetAdvertisement: %v", err)
	}
	if err := restarted.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitEstablished(t, restarted)
	eventually(t, "the peer to replace the stale path with the advertised path", func() bool {
		present, stale := peerPath(t, peer, exported)
		return present && !stale
	})
}

func TestSessionRestartWithdrawsAPrefixThatTheNextProcessDoesNotAdvertise(t *testing.T) {
	exported := netip.MustParsePrefix("3fff:a::/48")
	peer := startGracefulTestPeer(t, netip.IPv6Loopback())
	cfg := gracefulExportConfig("withdrawn", peer, exported)
	stopKeepingExport(t, startSession(t, cfg), peer, exported)

	restarted := restartKeepingRoutes(t, cfg)
	if err := restarted.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitEstablished(t, restarted)
	waitReceived(t, peer)
}
