package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"

	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/clock"
)

// sessionListenPort disables the GoBGP listener because a session initiates
// only outbound connections and the internal speaker owns the BGP listening
// port.
const sessionListenPort = -1

// Session prevents route exchange with the internal speaker and other sessions
// by running one external BGP session in a separate embedded GoBGP server.
type Session struct {
	cfg     SessionConfig
	log     *slog.Logger
	fib     *FIB
	peerKey string
	clock   clock.Clock

	mu          sync.Mutex
	server      *server.BgpServer
	cancelWatch context.CancelFunc
	started     bool
	connected   bool
	fsmState    bgppkt.FSMState
	established bool
	upSince     time.Time
	accepted    map[netip.Prefix]netip.Addr
	unresolved  map[netip.Prefix]netip.Addr
	advertised  map[netip.Prefix]struct{}
	rejected    []Rejection
	// gracefulRestart is true while the established peer advertises the RFC
	// 4724 capability for IPv6 unicast.
	gracefulRestart bool
	// retaining is true from KeepRoutes until the IPv6 End-of-RIB marker or
	// the restart deadline, and retained lists the kernel routes of the
	// previous process that the peer has not sent again. retainGeneration
	// identifies the deadline timer of the current KeepRoutes call.
	retaining        bool
	retained         map[netip.Prefix]struct{}
	retainTimer      *time.Timer
	retainGeneration uint64
	listeners        []func(SessionState)
	pending          []SessionState
	delivering       bool
}

// NewSession validates cfg and creates a stopped session.
func NewSession(cfg SessionConfig, log *slog.Logger) (*Session, error) {
	if log == nil {
		log = slog.Default()
	}
	validated, err := bgpsession.Validated(cfg)
	if err != nil {
		log.Error("bgp session configuration rejected", "session", cfg.Name, "error", err)
		return nil, fmt.Errorf("bgp session %q: %w", cfg.Name, err)
	}
	log = log.With("session", validated.Name)
	fibConfig := FIBConfig{
		Tables:         validated.Tables,
		InternalIface:  validated.Interface,
		Metric:         validated.RouteMetric,
		ResolveNextHop: true,
	}
	return &Session{
		cfg:              validated,
		log:              log,
		fib:              NewFIB(fibConfig, log),
		peerKey:          validated.PeerAddress.String(),
		clock:            clock.Real{},
		mu:               sync.Mutex{},
		server:           nil,
		cancelWatch:      nil,
		started:          false,
		connected:        false,
		fsmState:         bgppkt.BGP_FSM_IDLE,
		established:      false,
		upSince:          time.Time{},
		accepted:         make(map[netip.Prefix]netip.Addr),
		unresolved:       make(map[netip.Prefix]netip.Addr),
		advertised:       make(map[netip.Prefix]struct{}),
		rejected:         nil,
		gracefulRestart:  false,
		retaining:        false,
		retained:         make(map[netip.Prefix]struct{}),
		retainTimer:      nil,
		retainGeneration: 0,
		listeners:        nil,
		pending:          nil,
		delivering:       false,
	}, nil
}

// Start creates the GoBGP server, adds the peer, and begins connecting. A
// session that keeps routes after KeepRoutes adds the peer only at Connect. The
// context bounds only the Start call. The context supplies logging values. The
// session runs until Stop even after the context ends.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if err := s.startServerLocked(ctx, !s.retaining); err != nil {
		return err
	}
	s.log.InfoContext(
		ctx, "bgp session started",
		"local_asn", s.cfg.LocalASN,
		"remote_asn", s.cfg.RemoteASN,
		"peer", s.peerKey,
		"peer_port", s.cfg.PeerPort,
		"connected", s.connected,
	)
	return nil
}

// startServerLocked adds the peer only when connect is true. A session without
// the peer runs no connection until Connect.
func (s *Session) startServerLocked(ctx context.Context, connect bool) error {
	logLevel := new(slog.LevelVar)
	logLevel.Set(slog.LevelInfo)
	bgpServer := server.NewBgpServer(server.LoggerOption(s.log, logLevel))
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				recoveredErr := fmt.Errorf("panic: %v", recovered)
				s.log.ErrorContext(ctx, "bgp session server panic", "error", recoveredErr)
			}
		}()
		bgpServer.Serve()
	}()

	watchContext, cancelWatch := context.WithCancel(context.WithoutCancel(ctx))
	if err := s.configureServer(watchContext, bgpServer, connect); err != nil {
		cancelWatch()
		bgpServer.Stop()
		return err
	}

	s.fib.ArmSweep()
	s.server = bgpServer
	s.cancelWatch = cancelWatch
	s.started = true
	s.connected = connect
	return nil
}

func (s *Session) configureServer(ctx context.Context, bgpServer *server.BgpServer, connect bool) error {
	global := &apipb.Global{
		Asn:        s.cfg.LocalASN,
		RouterId:   s.cfg.RouterID.String(),
		ListenPort: sessionListenPort,
	}
	if err := bgpServer.StartBgp(ctx, &apipb.StartBgpRequest{Global: global}); err != nil {
		s.log.ErrorContext(ctx, "start bgp session server failed", "error", err)
		return fmt.Errorf("start bgp session %q: %w", s.cfg.Name, err)
	}
	callbacks := server.WatchEventMessageCallbacks{
		OnPeerUpdate: func(event *apiutil.WatchEventMessage_PeerEvent, timestamp time.Time) {
			s.handlePeerEvent(ctx, event, timestamp)
		},
		OnBestPath: func(paths []*apiutil.Path, _ time.Time) {
			s.handleBestPaths(ctx, paths)
		},
		OnPathEor: func(path *apiutil.Path, _ time.Time) {
			s.handleEndOfRIB(ctx, path)
		},
	}
	options := []server.WatchOption{server.WatchPeer(), server.WatchBestPath(true)}
	if s.cfg.GracefulRestartSeconds > 0 {
		options = append(options, server.WatchEor(false))
	}
	if err := bgpServer.WatchEvent(ctx, callbacks, options...); err != nil {
		s.log.ErrorContext(ctx, "bgp session watch registration failed", "error", err)
		return fmt.Errorf("watch bgp session %q events: %w", s.cfg.Name, err)
	}
	if !connect {
		return nil
	}
	return s.addPeer(ctx, bgpServer)
}

func (s *Session) addPeer(ctx context.Context, bgpServer *server.BgpServer) error {
	if err := bgpServer.AddPeer(ctx, &apipb.AddPeerRequest{Peer: s.peer()}); err != nil {
		s.log.ErrorContext(ctx, "add bgp session peer failed", "peer", s.peerKey, "error", err)
		return fmt.Errorf("add bgp session %q peer %s: %w", s.cfg.Name, s.peerKey, err)
	}
	return nil
}

func (s *Session) peer() *apipb.Peer {
	transport := &apipb.Transport{
		RemotePort:    uint32(s.cfg.PeerPort),
		BindInterface: s.cfg.Interface,
	}
	if s.cfg.LocalAddress.IsValid() {
		transport.LocalAddress = s.cfg.LocalAddress.String()
	}
	peer := &apipb.Peer{
		Conf: &apipb.PeerConf{
			NeighborAddress: s.peerKey,
			PeerAsn:         s.cfg.RemoteASN,
		},
		Timers: &apipb.Timers{
			Config: &apipb.TimersConfig{
				ConnectRetry:      uint64(s.cfg.ConnectRetrySeconds),
				KeepaliveInterval: uint64(s.cfg.KeepaliveSeconds),
				HoldTime:          uint64(s.cfg.HoldSeconds),
			},
		},
		Transport: transport,
		AfiSafis: []*apipb.AfiSafi{{
			Config: &apipb.AfiSafiConfig{
				Family:  &apipb.Family{Afi: apipb.Family_AFI_IP6, Safi: apipb.Family_SAFI_UNICAST},
				Enabled: true,
			},
		}},
	}
	if s.cfg.MultihopTTL > 0 {
		peer.EbgpMultihop = &apipb.EbgpMultihop{
			Enabled:     true,
			MultihopTtl: uint32(s.cfg.MultihopTTL),
		}
	}
	if s.cfg.GracefulRestartSeconds > 0 {
		// LocalRestarting sets the Restart State bit, and GoBGP then sends the
		// session's paths only after the peer's End-of-RIB marker or the
		// deferral time.
		peer.GracefulRestart = &apipb.GracefulRestart{
			Enabled:         true,
			RestartTime:     s.cfg.GracefulRestartSeconds,
			DeferralTime:    s.cfg.GracefulRestartSeconds,
			LocalRestarting: s.retaining,
		}
		peer.AfiSafis[0].MpGracefulRestart = &apipb.MpGracefulRestart{
			Config: &apipb.MpGracefulRestartConfig{Enabled: true},
		}
	}
	return peer
}

// Stop shuts down the GoBGP server and removes the installed kernel routes. A
// later Start can restart the stopped session after a route removal error.
func (s *Session) Stop(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}

	// The server's management loop handles the watch goroutine's
	// deregistration. The management loop exits during the server's Stop.
	s.cancelWatch()
	// BgpServer.Stop logs a StopBgp failure. BgpServer.Stop ends the BFD loop.
	// StopBgp alone does not stop the BFD loop.
	s.server.Stop()
	stateChanged := s.established || len(s.retained) > 0
	s.dropRetainedLocked(ctx)
	withdrawErr := s.fib.WithdrawPeer(ctx, s.peerKey)
	s.started = false
	s.connected = false
	s.server = nil
	s.cancelWatch = nil
	s.fsmState = bgppkt.BGP_FSM_IDLE
	s.established = false
	s.gracefulRestart = false
	s.upSince = time.Time{}
	clear(s.accepted)
	clear(s.unresolved)
	clear(s.advertised)
	if stateChanged {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
	s.log.InfoContext(ctx, "bgp session stopped")
	if withdrawErr != nil {
		s.log.ErrorContext(ctx, "remove bgp session routes failed", "error", withdrawErr)
		return fmt.Errorf("remove bgp session %q routes: %w", s.cfg.Name, withdrawErr)
	}
	return nil
}
