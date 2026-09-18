package bgp

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"
)

// fakeBGPServer captures calls into the bgpServerAPI surface. Tests
// install one via Speaker.newServer to assert that the GR-related
// fields propagate into the GoBGP API requests built by the speaker.
type fakeBGPServer struct {
	mu                     sync.Mutex
	startReq               *apipb.StartBgpRequest
	stopReq                *apipb.StopBgpRequest
	addPeerReqs            []*apipb.AddPeerRequest
	addPeerGroupReqs       []*apipb.AddPeerGroupRequest
	addDynamicNeighborReqs []*apipb.AddDynamicNeighborRequest
	watchRegistered        bool
	watchCallbacks         server.WatchEventMessageCallbacks

	// listPaths is what ListPath serves as the table's current best paths;
	// listPathErr fails the listing instead.
	listPaths   []*apiutil.Path
	listPathErr error
	// listPeers is what ListPeer serves as the current sessions.
	listPeers []*apipb.Peer
}

func newFakeBGPServer() *fakeBGPServer {
	return &fakeBGPServer{
		mu:                     sync.Mutex{},
		startReq:               nil,
		stopReq:                nil,
		addPeerReqs:            nil,
		addPeerGroupReqs:       nil,
		addDynamicNeighborReqs: nil,
		watchRegistered:        false,
		listPaths:              nil,
		listPathErr:            nil,
		listPeers:              nil,
	}
}

func (f *fakeBGPServer) Serve() {}

func (f *fakeBGPServer) StartBgp(_ context.Context, r *apipb.StartBgpRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startReq = r
	return nil
}

func (f *fakeBGPServer) StopBgp(_ context.Context, r *apipb.StopBgpRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopReq = r
	return nil
}

func (f *fakeBGPServer) AddPeer(_ context.Context, r *apipb.AddPeerRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addPeerReqs = append(f.addPeerReqs, r)
	return nil
}

func (f *fakeBGPServer) AddPeerGroup(_ context.Context, r *apipb.AddPeerGroupRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addPeerGroupReqs = append(f.addPeerGroupReqs, r)
	return nil
}

func (f *fakeBGPServer) AddDynamicNeighbor(_ context.Context, r *apipb.AddDynamicNeighborRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addDynamicNeighborReqs = append(f.addDynamicNeighborReqs, r)
	return nil
}

func (f *fakeBGPServer) WatchEvent(_ context.Context, callbacks server.WatchEventMessageCallbacks, _ ...server.WatchOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchRegistered = true
	f.watchCallbacks = callbacks
	return nil
}

func (f *fakeBGPServer) ListPeer(_ context.Context, _ *apipb.ListPeerRequest, fn func(*apipb.Peer)) error {
	f.mu.Lock()
	peers := f.listPeers
	f.mu.Unlock()
	for _, peer := range peers {
		fn(peer)
	}
	return nil
}

func (f *fakeBGPServer) setListPeers(peers []*apipb.Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listPeers = peers
}

func (f *fakeBGPServer) ListPath(
	_ apiutil.ListPathRequest, fn func(bgppkt.NLRI, []*apiutil.Path),
) error {
	f.mu.Lock()
	paths := f.listPaths
	err := f.listPathErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	// Group per prefix like the real table listing, so a test that
	// serves several paths for one prefix exercises a multi-path
	// callback rather than one call per path.
	groups := make(map[string][]*apiutil.Path, len(paths))
	order := make([]string, 0, len(paths))
	for _, path := range paths {
		key := path.Nlri.String()
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], path)
	}
	for _, key := range order {
		group := groups[key]
		fn(group[0].Nlri, group)
	}
	return nil
}

func (f *fakeBGPServer) setListPaths(paths []*apiutil.Path) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listPaths = paths
}

func (f *fakeBGPServer) emitBestPaths(paths []*apiutil.Path) {
	f.mu.Lock()
	callback := f.watchCallbacks.OnBestPath
	f.mu.Unlock()
	if callback != nil {
		callback(paths, time.Time{})
	}
}

func (f *fakeBGPServer) emitPeerUpdate(event *apiutil.WatchEventMessage_PeerEvent) {
	f.mu.Lock()
	callback := f.watchCallbacks.OnPeerUpdate
	f.mu.Unlock()
	if callback != nil {
		callback(event, time.Time{})
	}
}

func (f *fakeBGPServer) AddPath(_ apiutil.AddPathRequest) ([]apiutil.AddPathResponse, error) {
	return nil, nil
}

func (f *fakeBGPServer) DeletePath(_ apiutil.DeletePathRequest) error {
	return nil
}

// discardLogger returns a logger whose output is discarded so tests do
// not pollute stderr. The level is irrelevant for the assertions.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newSpeakerWithFake returns a Speaker with the given Config wired to
// the supplied fakeBGPServer. The newServer hook bypasses the real
// GoBGP constructor so Start does not bind a TCP listener.
func newSpeakerWithFake(cfg Config, fake *fakeBGPServer) *Speaker {
	return &Speaker{
		cfg:               cfg,
		log:               discardLogger(),
		server:            nil,
		fib:               nil,
		newServer:         func(_ *slog.Logger) bgpServerAPI { return fake },
		mu:                sync.Mutex{},
		announcing:        false,
		started:           false,
		sweepArmed:        false,
		startupGraceTimer: nil,
		afterFunc:         nil,
	}
}

// baseGRConfig builds a minimal Config that drives the addPeer path
// through both v4 and v6 branches so the test can assert MpGracefulRestart
// on every AfiSafi entry.
func baseGRConfig(grEnabled bool) Config {
	return Config{
		Enabled:          true,
		ASN:              65001,
		RouterID:         "10.0.0.1",
		NextHopV6:        "",
		KeepaliveSeconds: 10,
		HoldSeconds:      30,
		ListenPort:       179,
		Neighbors:        []NeighborConfig{{Address: "10.0.0.2"}},
		NeighborsV6:      []NeighborConfig{{Address: "fd00::2"}},
		Announce: AnnounceConfig{
			IPv4: []string{"0.0.0.0/0"},
			IPv6: []string{"::/0"},
		},
		GracefulRestart: GracefulRestartConfig{
			Enabled:             grEnabled,
			RestartTime:         30,
			NotificationEnabled: true,
		},
	}
}

func TestStartRegistersDynamicPeerGroupAndNeighbors(t *testing.T) {
	fake := newFakeBGPServer()
	cfg := baseGRConfig(true)
	cfg.DynamicNeighborPrefixes = []netip.Prefix{
		netip.MustParsePrefix("10.250.250.0/29"),
		netip.MustParsePrefix("3d06:bad:b01:fe::/64"),
	}
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if got, want := len(fake.addPeerReqs), 2; got != want {
		t.Fatalf("AddPeer call count = %d, want %d", got, want)
	}
	if got, want := len(fake.addPeerGroupReqs), 1; got != want {
		t.Fatalf("AddPeerGroup call count = %d, want %d", got, want)
	}
	peerGroup := fake.addPeerGroupReqs[0].PeerGroup
	if peerGroup == nil || peerGroup.Conf == nil {
		t.Fatal("dynamic peer group configuration is nil")
	}
	if got, want := peerGroup.Conf.PeerAsn, cfg.ASN; got != want {
		t.Fatalf("dynamic peer group ASN = %d, want %d", got, want)
	}
	if peerGroup.Transport == nil || !peerGroup.Transport.PassiveMode {
		t.Fatal("dynamic peer group must be passive")
	}
	if got, want := len(peerGroup.AfiSafis), 2; got != want {
		t.Fatalf("dynamic peer group AFI-SAFI count = %d, want %d", got, want)
	}
	for index, afiSafi := range peerGroup.AfiSafis {
		if afiSafi.MpGracefulRestart == nil || afiSafi.MpGracefulRestart.Config == nil {
			t.Fatalf("dynamic peer group AFI-SAFI[%d] MP graceful restart is nil", index)
		}
		if !afiSafi.MpGracefulRestart.Config.Enabled {
			t.Errorf("dynamic peer group AFI-SAFI[%d] MP graceful restart enabled = false, want true", index)
		}
	}
	if got, want := len(fake.addDynamicNeighborReqs), len(cfg.DynamicNeighborPrefixes); got != want {
		t.Fatalf("AddDynamicNeighbor call count = %d, want %d", got, want)
	}
	for index, prefix := range cfg.DynamicNeighborPrefixes {
		dynamicNeighbor := fake.addDynamicNeighborReqs[index].DynamicNeighbor
		if dynamicNeighbor == nil {
			t.Fatalf("AddDynamicNeighbor[%d] is nil", index)
		}
		if got, want := dynamicNeighbor.Prefix, prefix.String(); got != want {
			t.Fatalf("AddDynamicNeighbor[%d] prefix = %q, want %q", index, got, want)
		}
		if got, want := dynamicNeighbor.PeerGroup, dynamicPeerGroupName; got != want {
			t.Fatalf("AddDynamicNeighbor[%d] peer group = %q, want %q", index, got, want)
		}
	}
}

// TestStatus_ReportsTheSessionAddressForDynamicPeers pins the address a
// peer is reported under. GoBGP leaves the configured address zero for a
// neighbor it accepted from a dynamic prefix and carries the router's
// address only in the session state; reporting the configured one named
// every dynamic session "invalid IP", which the management surface could
// not key. A configured neighbor whose session state carries no address
// still reports its configured address.
func TestStatus_ReportsTheSessionAddressForDynamicPeers(t *testing.T) {
	fake := newFakeBGPServer()
	cfg := baseGRConfig(false)
	cfg.DynamicNeighborPrefixes = []netip.Prefix{netip.MustParsePrefix("10.250.250.0/29")}
	s := newSpeakerWithFake(cfg, fake)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	fake.setListPeers([]*apipb.Peer{
		{
			Conf: &apipb.PeerConf{NeighborAddress: netip.Addr{}.String()},
			State: &apipb.PeerState{
				NeighborAddress: "10.250.250.4",
				SessionState:    apipb.PeerState_SESSION_STATE_ESTABLISHED,
			},
		},
		{
			Conf: &apipb.PeerConf{NeighborAddress: "10.0.0.2"},
			State: &apipb.PeerState{
				NeighborAddress: "",
				SessionState:    apipb.PeerState_SESSION_STATE_ACTIVE,
			},
		},
	})

	st := s.Status()

	if len(st.Peers) != 2 {
		t.Fatalf("peers = %+v", st.Peers)
	}
	if st.Peers[0].Address != "10.250.250.4" || !st.Peers[0].Established {
		t.Fatalf("dynamic peer = %+v, want its session address, established", st.Peers[0])
	}
	if st.Peers[1].Address != "10.0.0.2" || st.Peers[1].Established {
		t.Fatalf("configured peer = %+v, want its configured address, not established", st.Peers[1])
	}
}

func TestDynamicPeerGroupOmitsMpGracefulRestartWhenDisabled(t *testing.T) {
	fake := newFakeBGPServer()
	cfg := baseGRConfig(false)
	cfg.DynamicNeighborPrefixes = []netip.Prefix{netip.MustParsePrefix("10.250.250.0/29")}
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if got, want := len(fake.addPeerGroupReqs), 1; got != want {
		t.Fatalf("AddPeerGroup call count = %d, want %d", got, want)
	}
	for index, afiSafi := range fake.addPeerGroupReqs[0].PeerGroup.AfiSafis {
		if afiSafi.MpGracefulRestart != nil {
			t.Errorf("dynamic peer group AFI-SAFI[%d] MP graceful restart = %+v, want nil", index, afiSafi.MpGracefulRestart)
		}
	}
}

func watchPath(t *testing.T, peer string, prefix netip.Prefix, withdrawn bool) *apiutil.Path {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("create NLRI: %v", err)
	}
	nextHop := netip.MustParseAddr(peer)
	attribute, err := bgppkt.NewPathAttributeMpReachNLRI(
		bgppkt.RF_IPv6_UC,
		[]bgppkt.PathNLRI{{NLRI: nlri}},
		nextHop,
	)
	if err != nil {
		t.Fatalf("create MP_REACH_NLRI: %v", err)
	}
	return &apiutil.Path{
		Family:      bgppkt.RF_IPv6_UC,
		Nlri:        nlri,
		Attrs:       []bgppkt.PathAttributeInterface{attribute},
		Withdrawal:  withdrawn,
		PeerAddress: nextHop,
	}
}

func peerStateEvent(peer string, state bgppkt.FSMState) *apiutil.WatchEventMessage_PeerEvent {
	return &apiutil.WatchEventMessage_PeerEvent{
		Type: apiutil.PEER_EVENT_STATE,
		Peer: apiutil.Peer{State: apiutil.PeerState{
			NeighborAddress: netip.MustParseAddr(peer),
			SessionState:    state,
		}},
	}
}

func TestStartPropagatesGracefulRestartToGlobal(t *testing.T) {
	t.Parallel()
	fake := newFakeBGPServer()
	cfg := baseGRConfig(true)
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if fake.startReq == nil {
		t.Fatal("StartBgp was not called")
	}
	if fake.startReq.Global == nil {
		t.Fatal("StartBgp called with nil Global")
	}
	gr := fake.startReq.Global.GracefulRestart
	if gr == nil {
		t.Fatal("Global.GracefulRestart is nil; expected GR config to propagate")
	}
	if !gr.Enabled {
		t.Error("Global.GracefulRestart.Enabled = false; want true")
	}
	if gr.RestartTime != 30 {
		t.Errorf("Global.GracefulRestart.RestartTime = %d; want 30", gr.RestartTime)
	}
	if !gr.NotificationEnabled {
		t.Error("Global.GracefulRestart.NotificationEnabled = false; want true")
	}
}

func TestStartOmitsGracefulRestartWhenDisabled(t *testing.T) {
	t.Parallel()
	fake := newFakeBGPServer()
	cfg := baseGRConfig(false)
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if fake.startReq == nil {
		t.Fatal("StartBgp was not called")
	}
	if fake.startReq.Global == nil {
		t.Fatal("StartBgp called with nil Global")
	}
	if fake.startReq.Global.GracefulRestart != nil {
		t.Errorf("Global.GracefulRestart = %+v; want nil when GR disabled", fake.startReq.Global.GracefulRestart)
	}
}

func TestAddPeerSetsGracefulRestartAndMpGracefulRestart(t *testing.T) {
	t.Parallel()
	fake := newFakeBGPServer()
	cfg := baseGRConfig(true)
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if len(fake.addPeerReqs) != 2 {
		t.Fatalf("AddPeer call count = %d; want 2 (one v4, one v6)", len(fake.addPeerReqs))
	}
	for i, req := range fake.addPeerReqs {
		if req.Peer == nil {
			t.Fatalf("AddPeer[%d].Peer is nil", i)
		}
		if req.Peer.GracefulRestart == nil {
			t.Errorf("AddPeer[%d].Peer.GracefulRestart is nil; want set when GR enabled", i)
			continue
		}
		if !req.Peer.GracefulRestart.Enabled {
			t.Errorf("AddPeer[%d].Peer.GracefulRestart.Enabled = false; want true", i)
		}
		if req.Peer.GracefulRestart.RestartTime != 30 {
			t.Errorf("AddPeer[%d].Peer.GracefulRestart.RestartTime = %d; want 30", i, req.Peer.GracefulRestart.RestartTime)
		}
		if len(req.Peer.AfiSafis) != 1 {
			t.Fatalf("AddPeer[%d].Peer.AfiSafis len = %d; want 1", i, len(req.Peer.AfiSafis))
		}
		af := req.Peer.AfiSafis[0]
		if af.MpGracefulRestart == nil || af.MpGracefulRestart.Config == nil {
			t.Errorf("AddPeer[%d] AfiSafi[0].MpGracefulRestart.Config is nil; want set when GR enabled", i)
			continue
		}
		if !af.MpGracefulRestart.Config.Enabled {
			t.Errorf("AddPeer[%d] AfiSafi[0].MpGracefulRestart.Config.Enabled = false; want true", i)
		}
	}
}

func TestAddPeerOmitsGracefulRestartWhenDisabled(t *testing.T) {
	t.Parallel()
	fake := newFakeBGPServer()
	cfg := baseGRConfig(false)
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	for i, req := range fake.addPeerReqs {
		if req.Peer.GracefulRestart != nil {
			t.Errorf("AddPeer[%d].Peer.GracefulRestart = %+v; want nil when GR disabled", i, req.Peer.GracefulRestart)
		}
		for j, af := range req.Peer.AfiSafis {
			if af.MpGracefulRestart != nil {
				t.Errorf("AddPeer[%d] AfiSafi[%d].MpGracefulRestart = %+v; want nil when GR disabled", i, j, af.MpGracefulRestart)
			}
		}
	}
}

func TestStopPassesAllowGracefulRestartWhenEnabled(t *testing.T) {
	t.Parallel()
	fake := newFakeBGPServer()
	cfg := baseGRConfig(true)
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	if fake.stopReq == nil {
		t.Fatal("StopBgp was not called")
	}
	if !fake.stopReq.AllowGracefulRestart {
		t.Error("StopBgpRequest.AllowGracefulRestart = false; want true when GR enabled")
	}
}

func TestStopOmitsAllowGracefulRestartWhenDisabled(t *testing.T) {
	t.Parallel()
	fake := newFakeBGPServer()
	cfg := baseGRConfig(false)
	s := newSpeakerWithFake(cfg, fake)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	if fake.stopReq == nil {
		t.Fatal("StopBgp was not called")
	}
	if fake.stopReq.AllowGracefulRestart {
		t.Error("StopBgpRequest.AllowGracefulRestart = true; want false when GR disabled")
	}
}
