package bgp_test

import (
	"net/netip"
	"slices"
	"testing"

	apipb "github.com/osrg/gobgp/v4/api"

	"goodkind.io/mwan/internal/bgp"
)

func TestSessionWithdrawsExportsAtPeerLoss(t *testing.T) {
	loopback := netip.IPv6Loopback()
	exported := netip.MustParsePrefix("2001:db8:a::/48")
	learned := netip.MustParsePrefix("2001:db8:f::/48")
	hop := netip.MustParseAddr("2001:db8:ffff::1")
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("peer-loss", externalPeerASN, peer)
	cfg.Export = []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: hop}}
	cfg.Import = []bgp.ImportRule{{Prefix: learned, MinLength: 48, MaxLength: 48}}
	session := startSession(t, cfg)
	waitEstablished(t, session)
	if err := session.SetAdvertisement(true, nil); err != nil {
		t.Fatalf("SetAdvertisement: %v", err)
	}
	waitReceived(t, peer, exported)

	neighbor := peer.neighbor.String()
	if err := peer.server.DeletePeer(t.Context(), &apipb.DeletePeerRequest{Address: neighbor}); err != nil {
		t.Fatalf("delete the session as a test peer neighbor: %v", err)
	}
	eventually(t, "the session to report the peer loss", func() bool {
		return !session.State().Established
	})
	if advertised := session.State().Advertised; len(advertised) != 0 {
		t.Fatalf("advertised prefixes after the peer loss = %v, want none", advertised)
	}

	returned := &apipb.Peer{
		Conf:      &apipb.PeerConf{NeighborAddress: neighbor, PeerAsn: sessionASN},
		Transport: &apipb.Transport{PassiveMode: true},
		Timers: &apipb.Timers{Config: &apipb.TimersConfig{
			KeepaliveInterval: uint64(keepaliveSeconds), HoldTime: uint64(holdSeconds),
		}},
		AfiSafis: []*apipb.AfiSafi{{Config: &apipb.AfiSafiConfig{
			Family: &apipb.Family{Afi: apipb.Family_AFI_IP6, Safi: apipb.Family_SAFI_UNICAST}, Enabled: true,
		}}},
	}
	if err := peer.server.AddPeer(t.Context(), &apipb.AddPeerRequest{Peer: returned}); err != nil {
		t.Fatalf("add the session as a test peer neighbor: %v", err)
	}
	waitEstablished(t, session)
	peer.announce(t, learned, hop)
	eventually(t, "the session to accept the peer's prefix", func() bool {
		return slices.Equal(session.State().Accepted, []netip.Prefix{learned})
	})
	if received := peer.receivedPrefixes(t); len(received) != 0 {
		t.Fatalf("prefixes received before the next SetAdvertisement = %v, want none", received)
	}

	if err := session.SetAdvertisement(true, nil); err != nil {
		t.Fatalf("SetAdvertisement after the peer return: %v", err)
	}
	waitReceived(t, peer, exported)
}
