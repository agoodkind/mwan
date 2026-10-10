package bgp_test

import (
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
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
