package bgp_test

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"

	"goodkind.io/mwan/internal/bgp"
)

func asPathLength(path *apiutil.Path) int {
	length := 0
	for _, attribute := range path.Attrs {
		asPath, ok := attribute.(*bgppkt.PathAttributeAsPath)
		if !ok {
			continue
		}
		for _, segment := range asPath.Value {
			length += segment.ASLen()
		}
	}
	return length
}

func multiExitDisc(path *apiutil.Path) (uint32, bool) {
	for _, attribute := range path.Attrs {
		if med, ok := attribute.(*bgppkt.PathAttributeMultiExitDisc); ok {
			return med.Value, true
		}
	}
	return 0, false
}

func localPreference(path *apiutil.Path) (uint32, bool) {
	for _, attribute := range path.Attrs {
		if localPref, ok := attribute.(*bgppkt.PathAttributeLocalPref); ok {
			return localPref.Value, true
		}
	}
	return 0, false
}

func communities(path *apiutil.Path) []uint32 {
	for _, attribute := range path.Attrs {
		if value, ok := attribute.(*bgppkt.PathAttributeCommunities); ok {
			return value.Value
		}
	}
	return nil
}

func largeCommunities(path *apiutil.Path) []bgppkt.LargeCommunity {
	values := make([]bgppkt.LargeCommunity, 0)
	for _, attribute := range path.Attrs {
		large, ok := attribute.(*bgppkt.PathAttributeLargeCommunities)
		if !ok {
			continue
		}
		for _, value := range large.Values {
			values = append(values, *value)
		}
	}
	return values
}

func nextHop(path *apiutil.Path) netip.Addr {
	for _, attribute := range path.Attrs {
		if mpReach, ok := attribute.(*bgppkt.PathAttributeMpReachNLRI); ok {
			return mpReach.Nexthop
		}
	}
	return netip.Addr{}
}

func TestSessionAdvertisesExportPrefixes(t *testing.T) {
	loopback := netip.IPv6Loopback()
	alwaysPrefix := netip.MustParsePrefix("2001:db8:a::/48")
	backupPrefix := netip.MustParsePrefix("2001:db8:b::/48")
	exportNextHop := netip.MustParseAddr("2001:db8:ffff::1")
	med := uint32(50)
	localPref := uint32(250)

	cases := []struct {
		name             string
		peerASN          uint32
		peerRouterID     string
		prependCount     uint8
		wantASPathLength int
		wantLocalPref    bool
	}{
		{
			name: "ebgp", peerASN: externalPeerASN, peerRouterID: "192.0.2.2",
			prependCount: 2, wantASPathLength: 3, wantLocalPref: false,
		},
		{
			name: "ibgp", peerASN: sessionASN, peerRouterID: "192.0.2.3",
			prependCount: 0, wantASPathLength: 0, wantLocalPref: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			peer := startTestPeer(t, testCase.peerASN, testCase.peerRouterID, loopback, loopback, sessionASN)
			cfg := baseSessionConfig("export-"+testCase.name, testCase.peerASN, peer)
			cfg.Export = []bgp.ExportRule{
				{
					Prefix:           alwaysPrefix,
					Mode:             bgp.ExportAlways,
					LocalPreference:  &localPref,
					MED:              &med,
					PrependCount:     testCase.prependCount,
					Communities:      []bgp.Community{{ASN: 64512, Value: 100}},
					LargeCommunities: []bgp.LargeCommunity{{GlobalAdmin: 64512, LocalData1: 1, LocalData2: 2}},
					NextHop:          exportNextHop,
				},
				{Prefix: backupPrefix, Mode: bgp.ExportBackup, NextHop: exportNextHop},
			}
			session := startSession(t, cfg)
			waitEstablished(t, session)
			if got := peer.receivedPrefixes(t); len(got) != 0 {
				t.Fatalf("prefixes received before the first SetAdvertisement = %v, want none", got)
			}

			if err := session.SetAdvertisement(true, false); err != nil {
				t.Fatalf("SetAdvertisement(true, false): %v", err)
			}
			waitReceived(t, peer, alwaysPrefix)
			path := peer.received(t)[alwaysPrefix]
			if got, ok := multiExitDisc(path); !ok || got != med {
				t.Errorf("MED = %d (present %t), want %d", got, ok, med)
			}
			if got, want := communities(path), []uint32{64512<<16 | 100}; !slices.Equal(got, want) {
				t.Errorf("communities = %v, want %v", got, want)
			}
			wantLarge := []bgppkt.LargeCommunity{{ASN: 64512, LocalData1: 1, LocalData2: 2}}
			if got := largeCommunities(path); !slices.Equal(got, wantLarge) {
				t.Errorf("large communities = %v, want %v", got, wantLarge)
			}
			if got := asPathLength(path); got != testCase.wantASPathLength {
				t.Errorf("AS_PATH length = %d, want %d", got, testCase.wantASPathLength)
			}
			gotLocalPref, hasLocalPref := localPreference(path)
			if hasLocalPref != testCase.wantLocalPref {
				t.Errorf("LOCAL_PREF present = %t, want %t", hasLocalPref, testCase.wantLocalPref)
			}
			if testCase.wantLocalPref && gotLocalPref != localPref {
				t.Errorf("LOCAL_PREF = %d, want %d", gotLocalPref, localPref)
			}
			if got := nextHop(path); got != exportNextHop {
				t.Errorf("next hop = %s, want %s", got, exportNextHop)
			}

			if err := session.SetAdvertisement(true, true); err != nil {
				t.Fatalf("SetAdvertisement(true, true): %v", err)
			}
			waitReceived(t, peer, alwaysPrefix, backupPrefix)
			wantAdvertised := []netip.Prefix{alwaysPrefix, backupPrefix}
			if got := session.State().Advertised; !slices.Equal(got, wantAdvertised) {
				t.Errorf("advertised prefixes = %v, want %v", got, wantAdvertised)
			}

			if err := session.SetAdvertisement(false, false); err != nil {
				t.Fatalf("SetAdvertisement(false, false): %v", err)
			}
			waitReceived(t, peer)
			if got := session.State().Advertised; len(got) != 0 {
				t.Errorf("advertised prefixes after withdrawal = %v, want none", got)
			}
		})
	}
}
