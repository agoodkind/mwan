package bgp_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"

	"goodkind.io/mwan/internal/bgp"
)

const (
	sessionASN        uint32 = 64512
	externalPeerASN   uint32 = 64513
	sessionRouterID          = "192.0.2.1"
	keepaliveSeconds  uint32 = 3
	holdSeconds       uint32 = 9
	waitTimeout              = 30 * time.Second
	pollInterval             = 50 * time.Millisecond
	peerStartAttempts        = 5
)

type testPeer struct {
	server   *server.BgpServer
	port     uint16
	neighbor netip.Addr
}

func sessionLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func unusedPort(t *testing.T, address netip.Addr) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(address.String(), "0"))
	if err != nil {
		t.Fatalf("reserve a TCP port on %s: %v", address, err)
	}
	port := netip.MustParseAddrPort(listener.Addr().String()).Port()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved TCP port: %v", err)
	}
	return port
}

func startTestPeer(
	t *testing.T,
	asn uint32,
	routerID string,
	listen netip.Addr,
	neighbor netip.Addr,
	neighborASN uint32,
) *testPeer {
	t.Helper()
	// The test retries each failed start on a new port because another process
	// can bind the reserved port before GoBGP binds that port.
	var bgpServer *server.BgpServer
	var port uint16
	var startErr error
	for range peerStartAttempts {
		port = unusedPort(t, listen)
		bgpServer = server.NewBgpServer()
		go bgpServer.Serve()
		global := &apipb.Global{
			Asn:             asn,
			RouterId:        routerID,
			ListenPort:      int32(port),
			ListenAddresses: []string{listen.String()},
		}
		startErr = bgpServer.StartBgp(t.Context(), &apipb.StartBgpRequest{Global: global})
		if startErr == nil {
			break
		}
		bgpServer.Stop()
	}
	if startErr != nil {
		t.Fatalf("start test peer after %d attempts: %v", peerStartAttempts, startErr)
	}
	t.Cleanup(bgpServer.Stop)
	peer := &apipb.Peer{
		Conf:      &apipb.PeerConf{NeighborAddress: neighbor.String(), PeerAsn: neighborASN},
		Transport: &apipb.Transport{PassiveMode: true},
		Timers: &apipb.Timers{Config: &apipb.TimersConfig{
			KeepaliveInterval: uint64(keepaliveSeconds),
			HoldTime:          uint64(holdSeconds),
		}},
		AfiSafis: []*apipb.AfiSafi{{Config: &apipb.AfiSafiConfig{
			Family:  &apipb.Family{Afi: apipb.Family_AFI_IP6, Safi: apipb.Family_SAFI_UNICAST},
			Enabled: true,
		}}},
	}
	if err := bgpServer.AddPeer(t.Context(), &apipb.AddPeerRequest{Peer: peer}); err != nil {
		t.Fatalf("add the session as a test peer neighbor: %v", err)
	}
	return &testPeer{server: bgpServer, port: port, neighbor: neighbor}
}

func (p *testPeer) announce(t *testing.T, prefix netip.Prefix, nextHop netip.Addr) {
	t.Helper()
	p.announceWithMED(t, prefix, nextHop, 0)
}

// GoBGP sends no update for a path that differs from the announced path only
// in the next hop. A different MED makes GoBGP send the new next hop.
func (p *testPeer) announceWithMED(t *testing.T, prefix netip.Prefix, nextHop netip.Addr, med uint32) {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("build NLRI for %s: %v", prefix, err)
	}
	mpReach, err := bgppkt.NewPathAttributeMpReachNLRI(
		bgppkt.RF_IPv6_UC, []bgppkt.PathNLRI{{NLRI: nlri}}, nextHop,
	)
	if err != nil {
		t.Fatalf("build MP_REACH_NLRI for %s: %v", prefix, err)
	}
	path := &apiutil.Path{
		Family: bgppkt.RF_IPv6_UC,
		Nlri:   nlri,
		Attrs: []bgppkt.PathAttributeInterface{
			bgppkt.NewPathAttributeOrigin(bgppkt.BGP_ORIGIN_ATTR_TYPE_IGP),
			mpReach,
			bgppkt.NewPathAttributeMultiExitDisc(med),
		},
	}
	if _, err := p.server.AddPath(apiutil.AddPathRequest{Paths: []*apiutil.Path{path}}); err != nil {
		t.Fatalf("test peer announce %s: %v", prefix, err)
	}
}

func (p *testPeer) withdraw(t *testing.T, prefix netip.Prefix) {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("build NLRI for %s: %v", prefix, err)
	}
	path := &apiutil.Path{Family: bgppkt.RF_IPv6_UC, Nlri: nlri, Withdrawal: true}
	request := apiutil.DeletePathRequest{Paths: []*apiutil.Path{path}}
	if err := p.server.DeletePath(request); err != nil {
		t.Fatalf("test peer withdraw %s: %v", prefix, err)
	}
}

func (p *testPeer) received(t *testing.T) map[netip.Prefix]*apiutil.Path {
	t.Helper()
	request := apiutil.ListPathRequest{
		TableType: apipb.TableType_TABLE_TYPE_ADJ_IN,
		Name:      p.neighbor.String(),
		Family:    bgppkt.RF_IPv6_UC,
	}
	learned := make(map[netip.Prefix]*apiutil.Path)
	err := p.server.ListPath(request, func(nlri bgppkt.NLRI, paths []*apiutil.Path) {
		for _, path := range paths {
			learned[netip.MustParsePrefix(nlri.String())] = path
		}
	})
	if err != nil {
		t.Fatalf("list the test peer's received paths: %v", err)
	}
	return learned
}

func (p *testPeer) receivedPrefixes(t *testing.T) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0)
	for prefix := range p.received(t) {
		prefixes = append(prefixes, prefix)
	}
	slices.SortFunc(prefixes, netip.Prefix.Compare)
	return prefixes
}

func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func waitReceived(t *testing.T, peer *testPeer, want ...netip.Prefix) {
	t.Helper()
	var got []netip.Prefix
	eventually(t, "the test peer to receive the wanted prefixes", func() bool {
		got = peer.receivedPrefixes(t)
		return slices.Equal(got, want)
	})
}

func baseSessionConfig(name string, remoteASN uint32, peer *testPeer) bgp.SessionConfig {
	return bgp.SessionConfig{
		Name:             name,
		RouterID:         netip.MustParseAddr(sessionRouterID),
		LocalASN:         sessionASN,
		RemoteASN:        remoteASN,
		PeerAddress:      peer.neighbor,
		PeerPort:         peer.port,
		KeepaliveSeconds: keepaliveSeconds,
		HoldSeconds:      holdSeconds,
	}
}

func startSession(t *testing.T, cfg bgp.SessionConfig) *bgp.Session {
	t.Helper()
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session %q: %v", cfg.Name, err)
	}
	if err := session.Start(t.Context()); err != nil {
		t.Fatalf("start session %q: %v", cfg.Name, err)
	}
	t.Cleanup(func() {
		if err := session.Stop(); err != nil {
			t.Errorf("stop session %q: %v", cfg.Name, err)
		}
	})
	return session
}

func waitEstablished(t *testing.T, session *bgp.Session) {
	t.Helper()
	eventually(t, "the session to establish", func() bool {
		return session.State().Established
	})
}

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

func TestSessionImportAcceptsOnlyDeclaredPrefixes(t *testing.T) {
	loopback := netip.IPv6Loopback()
	allowed := netip.MustParsePrefix("2001:db8:100::/48")
	tooLong := netip.MustParsePrefix("2001:db8:200::/56")
	undeclared := netip.MustParsePrefix("3fff:100::/32")
	peerNextHop := netip.MustParseAddr("2001:db8:ffff::2")

	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("import", externalPeerASN, peer)
	cfg.Import = []bgp.ImportRule{
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	changes := make(chan bgp.SessionState, 64)
	session.OnChange(func(state bgp.SessionState) {
		changes <- state
	})
	waitChange := func(description string, condition func(bgp.SessionState) bool) bgp.SessionState {
		t.Helper()
		timeout := time.After(waitTimeout)
		for {
			select {
			case state := <-changes:
				if condition(state) {
					return state
				}
			case <-timeout:
				t.Fatalf("timed out waiting for a change signal with %s", description)
			}
		}
	}
	if err := session.Start(t.Context()); err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Stop(); err != nil {
			t.Errorf("stop session: %v", err)
		}
	})

	established := waitChange("an established session", func(state bgp.SessionState) bool {
		return state.Established
	})
	if established.UpSince.IsZero() {
		t.Error("established state has a zero uptime start")
	}
	if got, want := established.FSMState, bgppkt.BGP_FSM_ESTABLISHED.String(); got != want {
		t.Errorf("established FSM state = %q, want %q", got, want)
	}

	peer.announce(t, allowed, peerNextHop)
	peer.announce(t, tooLong, peerNextHop)
	peer.announce(t, undeclared, peerNextHop)
	state := waitChange("one accepted and two rejected prefixes", func(state bgp.SessionState) bool {
		return len(state.Accepted) == 1 && len(state.Rejected) == 2
	})
	if got, want := state.Accepted, []netip.Prefix{allowed}; !slices.Equal(got, want) {
		t.Errorf("accepted prefixes = %v, want %v", got, want)
	}
	reasons := make(map[netip.Prefix]string, len(state.Rejected))
	for _, rejection := range state.Rejected {
		reasons[rejection.Prefix] = rejection.Reason
	}
	if got, want := reasons[undeclared], "no import rule covers the prefix"; got != want {
		t.Errorf("undeclared prefix rejection reason = %q, want %q", got, want)
	}
	if got, want := reasons[tooLong], "prefix length 56 is outside the bounds"; !strings.Contains(got, want) {
		t.Errorf("over-long prefix rejection reason = %q, want a reason containing %q", got, want)
	}

	peer.withdraw(t, allowed)
	waitChange("no accepted prefix after the peer withdrawal", func(state bgp.SessionState) bool {
		return len(state.Accepted) == 0
	})

	peer.announce(t, allowed, peerNextHop)
	waitChange("the prefix accepted again", func(state bgp.SessionState) bool {
		return slices.Equal(state.Accepted, []netip.Prefix{allowed})
	})
	peer.server.Stop()
	lost := waitChange("a lost session", func(state bgp.SessionState) bool {
		return !state.Established
	})
	if len(lost.Accepted) != 0 {
		t.Errorf("accepted prefixes after peer loss = %v, want none", lost.Accepted)
	}
	if !lost.UpSince.IsZero() {
		t.Errorf("uptime start after peer loss = %s, want the zero time", lost.UpSince)
	}
}

func TestSessionsWithSameASNsReportIndependentState(t *testing.T) {
	loopback := netip.IPv6Loopback()
	firstLearned := netip.MustParsePrefix("2001:db8:100::/48")
	secondLearned := netip.MustParsePrefix("2001:db8:200::/48")
	exported := netip.MustParsePrefix("3fff:a::/48")
	hop := netip.MustParseAddr("2001:db8:ffff::2")
	importRules := []bgp.ImportRule{
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	exportRules := []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: hop}}

	firstPeer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	secondPeer := startTestPeer(t, externalPeerASN, "192.0.2.3", loopback, loopback, sessionASN)
	firstConfig := baseSessionConfig("first", externalPeerASN, firstPeer)
	firstConfig.Import = importRules
	firstConfig.Export = exportRules
	secondConfig := baseSessionConfig("second", externalPeerASN, secondPeer)
	secondConfig.Import = importRules
	secondConfig.Export = exportRules
	first := startSession(t, firstConfig)
	second := startSession(t, secondConfig)
	waitEstablished(t, first)
	waitEstablished(t, second)

	firstPeer.announce(t, firstLearned, hop)
	secondPeer.announce(t, secondLearned, hop)
	eventually(t, "each session to accept its own peer's prefix", func() bool {
		return slices.Equal(first.State().Accepted, []netip.Prefix{firstLearned}) &&
			slices.Equal(second.State().Accepted, []netip.Prefix{secondLearned})
	})

	if err := first.SetAdvertisement(true, false); err != nil {
		t.Fatalf("first SetAdvertisement: %v", err)
	}
	waitReceived(t, firstPeer, exported)
	if got := secondPeer.receivedPrefixes(t); len(got) != 0 {
		t.Errorf("second peer received %v from a session that advertises no prefix", got)
	}
	if got := second.State().Advertised; len(got) != 0 {
		t.Errorf("second session advertised prefixes = %v, want none", got)
	}
	if got, want := first.State().Name, "first"; got != want {
		t.Errorf("first session name = %q, want %q", got, want)
	}

	firstPeer.server.Stop()
	eventually(t, "the first session to lose its peer", func() bool {
		return !first.State().Established
	})
	secondState := second.State()
	if !secondState.Established {
		t.Error("second session lost its peer when the first session's peer stopped")
	}
	if got, want := secondState.Accepted, []netip.Prefix{secondLearned}; !slices.Equal(got, want) {
		t.Errorf("second session accepted prefixes = %v, want %v", got, want)
	}
}

func TestSessionRejectsLearnedPrefixThatTheSessionExports(t *testing.T) {
	loopback := netip.IPv6Loopback()
	exported := netip.MustParsePrefix("2001:db8:a::/48")
	other := netip.MustParsePrefix("2001:db8:100::/48")
	hop := netip.MustParseAddr("2001:db8:ffff::2")
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("own-export", externalPeerASN, peer)
	cfg.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("::/0"), MinLength: 0, MaxLength: 48}}
	cfg.Export = []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: hop}}
	session := startSession(t, cfg)
	waitEstablished(t, session)

	peer.announce(t, exported, hop)
	peer.announce(t, other, hop)
	eventually(t, "one accepted prefix and one rejected prefix", func() bool {
		state := session.State()
		return len(state.Accepted) == 1 && len(state.Rejected) == 1
	})
	state := session.State()
	if got, want := state.Accepted, []netip.Prefix{other}; !slices.Equal(got, want) {
		t.Errorf("accepted prefixes = %v, want %v", got, want)
	}
	wantRejected := []bgp.Rejection{{Prefix: exported, Reason: "the session exports the prefix"}}
	if got := state.Rejected; !slices.Equal(got, wantRejected) {
		t.Errorf("rejected prefixes = %v, want %v", got, wantRejected)
	}

	peer.withdraw(t, exported)
	peer.withdraw(t, other)
	eventually(t, "no accepted prefix after the peer withdrawals", func() bool {
		return len(session.State().Accepted) == 0
	})
}

func TestSessionOutlivesStartContext(t *testing.T) {
	loopback := netip.IPv6Loopback()
	learned := netip.MustParsePrefix("2001:db8:100::/48")
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("start-context", externalPeerASN, peer)
	cfg.Import = []bgp.ImportRule{
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	startContext, cancelStart := context.WithCancel(t.Context())
	if err := session.Start(startContext); err != nil {
		t.Fatalf("start session: %v", err)
	}
	cancelStart()
	t.Cleanup(func() {
		if err := session.Stop(); err != nil {
			t.Errorf("stop session: %v", err)
		}
	})

	waitEstablished(t, session)
	peer.announce(t, learned, netip.MustParseAddr("2001:db8:ffff::2"))
	eventually(t, "the session to accept a prefix after the Start context ended", func() bool {
		return slices.Equal(session.State().Accepted, []netip.Prefix{learned})
	})
}

// The test verifies that Stop does not run a second delivery while the test
// blocks one delivery. The OnChange contract excludes listeners that block.
func TestSessionDeliversChangesOneAtATimeInOrder(t *testing.T) {
	loopback := netip.IPv6Loopback()
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	session, err := bgp.NewSession(baseSessionConfig("ordered", externalPeerASN, peer), sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var active atomic.Int32
	var overlapped atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	delivered := make(chan bgp.SessionState, 8)
	session.OnChange(func(state bgp.SessionState) {
		if active.Add(1) > 1 {
			overlapped.Store(true)
		}
		if state.Established {
			entered <- struct{}{}
			<-release
		}
		delivered <- state
		active.Add(-1)
	})
	if err := session.Start(t.Context()); err != nil {
		t.Fatalf("start session: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the established change")
	}
	if err := session.Stop(); err != nil {
		t.Fatalf("stop session: %v", err)
	}
	close(release)

	wantEstablished := []bool{true, false}
	for index, want := range wantEstablished {
		select {
		case state := <-delivered:
			if state.Established != want {
				t.Fatalf("delivered state %d established = %t, want %t", index, state.Established, want)
			}
		case <-time.After(waitTimeout):
			t.Fatalf("timed out waiting for delivered state %d", index)
		}
	}
	if overlapped.Load() {
		t.Error("two change deliveries ran at the same time")
	}
}

func TestNewSessionRejectsInvalidConfiguration(t *testing.T) {
	valid := bgp.SessionConfig{
		Name:             "invalid",
		RouterID:         netip.MustParseAddr(sessionRouterID),
		LocalASN:         sessionASN,
		RemoteASN:        externalPeerASN,
		PeerAddress:      netip.MustParseAddr("2001:db8:ffff::2"),
		PeerPort:         179,
		KeepaliveSeconds: keepaliveSeconds,
		HoldSeconds:      holdSeconds,
	}
	hop := netip.MustParseAddr("2001:db8:ffff::1")
	exportPrefix := netip.MustParsePrefix("2001:db8:a::/48")
	cases := []struct {
		name    string
		mutate  func(*bgp.SessionConfig)
		wantErr string
	}{
		{"missing name", func(c *bgp.SessionConfig) { c.Name = "" }, "name is required"},
		{"zero local ASN", func(c *bgp.SessionConfig) { c.LocalASN = 0 }, "local ASN is required"},
		{"zero remote ASN", func(c *bgp.SessionConfig) { c.RemoteASN = 0 }, "remote ASN is required"},
		{"invalid peer address", func(c *bgp.SessionConfig) { c.PeerAddress = netip.Addr{} }, "peer address"},
		{"zero peer port", func(c *bgp.SessionConfig) { c.PeerPort = 0 }, "peer port is required"},
		{"IPv4 import prefix", func(c *bgp.SessionConfig) {
			c.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("192.0.2.0/24"), MinLength: 24, MaxLength: 24}}
		}, "is not an IPv6 prefix"},
		{"import minimum below the prefix length", func(c *bgp.SessionConfig) {
			c.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 24, MaxLength: 48}}
		}, "inconsistent length bounds"},
		{"import maximum below the minimum", func(c *bgp.SessionConfig) {
			c.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 48, MaxLength: 40}}
		}, "inconsistent length bounds"},
		{"IPv4 export prefix", func(c *bgp.SessionConfig) {
			c.Export = []bgp.ExportRule{{Prefix: netip.MustParsePrefix("192.0.2.0/24"), Mode: bgp.ExportAlways, NextHop: hop}}
		}, "is not an IPv6 prefix"},
		{"duplicate export prefix", func(c *bgp.SessionConfig) {
			c.Export = []bgp.ExportRule{
				{Prefix: exportPrefix, Mode: bgp.ExportAlways, NextHop: hop},
				{Prefix: exportPrefix, Mode: bgp.ExportBackup, NextHop: hop},
			}
		}, "configured more than once"},
		{"unspecified router ID", func(c *bgp.SessionConfig) {
			c.RouterID = netip.IPv4Unspecified()
		}, "is not a usable IPv4 address"},
		{"hold timer below three seconds", func(c *bgp.SessionConfig) {
			c.KeepaliveSeconds = 1
			c.HoldSeconds = 2
		}, "hold timer 2 is outside the range"},
		{"hold timer above 16 bits", func(c *bgp.SessionConfig) {
			c.HoldSeconds = 65536
		}, "hold timer 65536 is outside the range"},
		{"zero route metric with kernel tables", func(c *bgp.SessionConfig) {
			c.Interface = "bgpext0"
			c.Tables = []int{100}
		}, "route metric 0 collides"},
		{"kernel default route metric with kernel tables", func(c *bgp.SessionConfig) {
			c.Interface = "bgpext0"
			c.Tables = []int{100}
			c.RouteMetric = 1024
		}, "route metric 1024 collides"},
		{"prepend on an iBGP session", func(c *bgp.SessionConfig) {
			c.RemoteASN = c.LocalASN
			c.Export = []bgp.ExportRule{{Prefix: exportPrefix, Mode: bgp.ExportAlways, PrependCount: 1, NextHop: hop}}
		}, "prepends the local ASN on an iBGP session"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := valid
			testCase.mutate(&cfg)
			session, err := bgp.NewSession(cfg, sessionLogger())
			if err == nil {
				t.Fatalf("NewSession returned session %v, want an error containing %q", session, testCase.wantErr)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Errorf("NewSession error = %q, want an error containing %q", err, testCase.wantErr)
			}
		})
	}
}
