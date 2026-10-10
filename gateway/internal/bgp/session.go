package bgp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"
)

const (
	// sessionListenPort disables the GoBGP listener because a session initiates
	// only outbound connections and the internal speaker owns the BGP listening
	// port.
	sessionListenPort = -1

	// MaxSessionRejections limits the number of rejected prefixes a
	// SessionState reports.
	MaxSessionRejections = 32
)

// Rejection records one learned prefix with the reason the session did not
// install that prefix.
type Rejection struct {
	Prefix netip.Prefix
	Reason string
}

// SessionState is a snapshot of one external BGP session. FSMState stores the
// GoBGP finite state machine state name. UpSince is the zero time while the
// session is not established. Accepted and Advertised are sorted. Rejected
// lists the most recent rejection last.
type SessionState struct {
	Name        string
	FSMState    string
	Established bool
	UpSince     time.Time
	Accepted    []netip.Prefix
	Advertised  []netip.Prefix
	Rejected    []Rejection
}

// Session prevents route exchange with the internal speaker and other sessions
// by running one external BGP session in a separate embedded GoBGP server.
type Session struct {
	cfg     SessionConfig
	log     *slog.Logger
	fib     *FIB
	peerKey string

	mu          sync.Mutex
	server      *server.BgpServer
	cancelWatch context.CancelFunc
	started     bool
	fsmState    bgppkt.FSMState
	established bool
	upSince     time.Time
	accepted    map[netip.Prefix]netip.Addr
	advertised  map[netip.Prefix]struct{}
	rejected    []Rejection
	listeners   []func(SessionState)
	pending     []SessionState
	delivering  bool
}

// NewSession validates cfg and creates a stopped session.
func NewSession(cfg SessionConfig, log *slog.Logger) (*Session, error) {
	if log == nil {
		log = slog.Default()
	}
	validated, err := validatedSessionConfig(cfg)
	if err != nil {
		log.Error("bgp session configuration rejected", "session", cfg.Name, "error", err)
		return nil, fmt.Errorf("bgp session %q: %w", cfg.Name, err)
	}
	log = log.With("session", validated.Name)
	fibConfig := FIBConfig{
		Tables:        validated.Tables,
		InternalIface: validated.Interface,
		Metric:        validated.RouteMetric,
	}
	return &Session{
		cfg:         validated,
		log:         log,
		fib:         NewFIB(fibConfig, log),
		peerKey:     validated.PeerAddress.String(),
		mu:          sync.Mutex{},
		server:      nil,
		cancelWatch: nil,
		started:     false,
		fsmState:    bgppkt.BGP_FSM_IDLE,
		established: false,
		upSince:     time.Time{},
		accepted:    make(map[netip.Prefix]netip.Addr),
		advertised:  make(map[netip.Prefix]struct{}),
		rejected:    nil,
		listeners:   nil,
		pending:     nil,
		delivering:  false,
	}, nil
}

// OnChange registers a listener that receives the session state after the
// session establishes, loses the peer, changes the accepted prefixes, or
// rejects a prefix. Listeners run one at a time in the order of the state
// changes. Do not block within listeners.
func (s *Session) OnChange(listener func(SessionState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, listener)
}

// Start creates the GoBGP server, adds the peer, and begins connecting. The
// context bounds only the Start call. The context supplies logging values. The
// session runs until Stop even after the context ends.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}

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
	if err := s.configureServer(watchContext, bgpServer); err != nil {
		cancelWatch()
		bgpServer.Stop()
		return err
	}

	s.fib.ArmSweep()
	s.server = bgpServer
	s.cancelWatch = cancelWatch
	s.started = true
	s.log.InfoContext(
		ctx, "bgp session started",
		"local_asn", s.cfg.LocalASN,
		"remote_asn", s.cfg.RemoteASN,
		"peer", s.peerKey,
		"peer_port", s.cfg.PeerPort,
	)
	return nil
}

func (s *Session) configureServer(ctx context.Context, bgpServer *server.BgpServer) error {
	global := &apipb.Global{
		Asn:        s.cfg.LocalASN,
		RouterId:   s.cfg.RouterID.String(),
		ListenPort: sessionListenPort,
	}
	if err := bgpServer.StartBgp(ctx, &apipb.StartBgpRequest{Global: global}); err != nil {
		s.log.ErrorContext(ctx, "start bgp session server failed", "error", err)
		return fmt.Errorf("start bgp session %q: %w", s.cfg.Name, err)
	}
	if err := bgpServer.AddPeer(ctx, &apipb.AddPeerRequest{Peer: s.peer()}); err != nil {
		s.log.ErrorContext(ctx, "add bgp session peer failed", "peer", s.peerKey, "error", err)
		return fmt.Errorf("add bgp session %q peer %s: %w", s.cfg.Name, s.peerKey, err)
	}
	callbacks := server.WatchEventMessageCallbacks{
		OnPeerUpdate: func(event *apiutil.WatchEventMessage_PeerEvent, timestamp time.Time) {
			s.handlePeerEvent(ctx, event, timestamp)
		},
		OnBestPath: func(paths []*apiutil.Path, _ time.Time) {
			s.handleBestPaths(ctx, paths)
		},
	}
	err := bgpServer.WatchEvent(ctx, callbacks, server.WatchPeer(), server.WatchBestPath(true))
	if err != nil {
		s.log.ErrorContext(ctx, "bgp session watch registration failed", "error", err)
		return fmt.Errorf("watch bgp session %q events: %w", s.cfg.Name, err)
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
	return peer
}

// Stop shuts down the GoBGP server and removes the installed kernel routes. A
// later Start can restart the stopped session after a route removal error.
func (s *Session) Stop() error {
	ctx := context.Background()
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
	wasEstablished := s.established
	withdrawErr := s.fib.WithdrawPeer(ctx, s.peerKey)
	s.started = false
	s.server = nil
	s.cancelWatch = nil
	s.fsmState = bgppkt.BGP_FSM_IDLE
	s.established = false
	s.upSince = time.Time{}
	clear(s.accepted)
	clear(s.advertised)
	if wasEstablished {
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

// SetAdvertisement originates or withdraws the export prefixes.
// SetAdvertisement originates ExportAlways prefixes while eligible is true.
// SetAdvertisement originates ExportBackup prefixes while eligible and
// backupActive are both true. SetAdvertisement makes no GoBGP request for a
// prefix already in the wanted state.
func (s *Session) SetAdvertisement(eligible, backupActive bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return fmt.Errorf("bgp session %q is not started", s.cfg.Name)
	}

	var reconcileErr error
	for _, rule := range s.cfg.Export {
		wanted := exportAdvertised(rule.Mode, eligible, backupActive)
		_, advertised := s.advertised[rule.Prefix]
		if wanted == advertised {
			continue
		}
		if err := s.reconcileExportLocked(rule, wanted); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func (s *Session) reconcileExportLocked(rule ExportRule, wanted bool) error {
	if !wanted {
		path, err := withdrawalPath(s.log, rule.Prefix)
		if err != nil {
			return err
		}
		request := apiutil.DeletePathRequest{Paths: []*apiutil.Path{path}}
		if err := s.server.DeletePath(request); err != nil {
			s.log.Error("withdraw bgp session prefix failed", "prefix", rule.Prefix, "error", err)
			return fmt.Errorf("withdraw %s: %w", rule.Prefix, err)
		}
		delete(s.advertised, rule.Prefix)
		return nil
	}

	path, err := exportPath(s.log, s.cfg, rule)
	if err != nil {
		return err
	}
	request := apiutil.AddPathRequest{Paths: []*apiutil.Path{path}}
	if _, err := s.server.AddPath(request); err != nil {
		s.log.Error("advertise bgp session prefix failed", "prefix", rule.Prefix, "error", err)
		return fmt.Errorf("advertise %s: %w", rule.Prefix, err)
	}
	s.advertised[rule.Prefix] = struct{}{}
	return nil
}

// State returns a copy that later session events do not modify.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// SweepStale removes kernel BGP routes on the session interface when the
// session does not currently accept the routes. The removed routes can include
// routes from a previous process in the configured tables.
func (s *Session) SweepStale(ctx context.Context) error {
	if err := s.fib.SweepStale(ctx); err != nil {
		s.log.ErrorContext(ctx, "sweep bgp session routes failed", "error", err)
		return fmt.Errorf("sweep bgp session %q routes: %w", s.cfg.Name, err)
	}
	return nil
}

func (s *Session) snapshotLocked() SessionState {
	accepted := make([]netip.Prefix, 0, len(s.accepted))
	for prefix := range s.accepted {
		accepted = append(accepted, prefix)
	}
	slices.SortFunc(accepted, netip.Prefix.Compare)
	advertised := make([]netip.Prefix, 0, len(s.advertised))
	for prefix := range s.advertised {
		advertised = append(advertised, prefix)
	}
	slices.SortFunc(advertised, netip.Prefix.Compare)
	return SessionState{
		Name:        s.cfg.Name,
		FSMState:    s.fsmState.String(),
		Established: s.established,
		UpSince:     s.upSince,
		Accepted:    accepted,
		Advertised:  advertised,
		Rejected:    slices.Clone(s.rejected),
	}
}

func (s *Session) enqueueChangeLocked() {
	s.pending = append(s.pending, s.snapshotLocked())
}

// deliverChanges calls the listeners with the queued states in queue order. One
// goroutine delivers at a time. A caller returns if delivery is already in
// progress. The active delivery goroutine delivers that caller's queued states.
func (s *Session) deliverChanges() {
	s.mu.Lock()
	if s.delivering {
		s.mu.Unlock()
		return
	}
	s.delivering = true
	for len(s.pending) > 0 {
		state := s.pending[0]
		s.pending = s.pending[1:]
		listeners := slices.Clone(s.listeners)
		s.mu.Unlock()
		for _, listener := range listeners {
			listener(state)
		}
		s.mu.Lock()
	}
	s.delivering = false
	s.mu.Unlock()
}

func (s *Session) handlePeerEvent(
	ctx context.Context,
	event *apiutil.WatchEventMessage_PeerEvent,
	timestamp time.Time,
) {
	if event.Type != apiutil.PEER_EVENT_STATE {
		return
	}
	fsmState := event.Peer.State.SessionState
	established := fsmState == bgppkt.BGP_FSM_ESTABLISHED

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	wasEstablished := s.established
	s.fsmState = fsmState
	s.established = established
	if established && !wasEstablished {
		s.upSince = timestamp
		s.log.InfoContext(ctx, "bgp session established", "peer", s.peerKey)
	}
	if !established && wasEstablished {
		s.upSince = time.Time{}
		clear(s.accepted)
		if err := s.fib.WithdrawPeer(ctx, s.peerKey); err != nil {
			s.log.ErrorContext(ctx, "remove bgp session routes failed", "error", err)
		}
		s.log.WarnContext(ctx, "bgp session lost", "peer", s.peerKey, "fsm_state", fsmState.String())
	}
	if established != wasEstablished {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
}

func (s *Session) handleBestPaths(ctx context.Context, paths []*apiutil.Path) {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	changed := false
	for _, path := range paths {
		if s.applyBestPathLocked(ctx, path) {
			changed = true
		}
	}
	if changed {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
}

// applyBestPathLocked reports whether the accepted or rejected prefixes
// changed. A path without a peer address is a locally originated export.
func (s *Session) applyBestPathLocked(ctx context.Context, path *apiutil.Path) bool {
	if path == nil || path.Family != bgppkt.RF_IPv6_UC || path.Nlri == nil {
		return false
	}
	if !path.PeerAddress.IsValid() {
		return false
	}
	prefix, err := netip.ParsePrefix(path.Nlri.String())
	if err != nil {
		s.log.WarnContext(ctx, "ignore bgp session path with invalid prefix", "error", err)
		return false
	}
	prefix = prefix.Masked()
	if path.Withdrawal {
		return s.withdrawAcceptedLocked(ctx, prefix)
	}

	if reason := importRejection(s.cfg, prefix); reason != "" {
		s.rejectLocked(ctx, prefix, reason)
		return true
	}
	nextHop, err := bestPathNextHop(path)
	if err != nil {
		s.rejectLocked(ctx, prefix, rejectedNoNextHop)
		return true
	}
	event := PathEvent{Peer: s.peerKey, Prefix: prefix, NextHop: nextHop, Withdrawn: false}
	if err := s.fib.Apply(ctx, event); err != nil {
		s.log.ErrorContext(ctx, "install bgp session route failed", "prefix", prefix, "error", err)
		// A failed replacement does not remove the earlier route for the prefix
		// from the kernel. The peer no longer announces that route's next hop.
		s.removeAcceptedLocked(ctx, prefix)
		s.rejectLocked(ctx, prefix, fmt.Sprintf("%s: %v", rejectedNotInstall, err))
		return true
	}
	previous, known := s.accepted[prefix]
	s.accepted[prefix] = nextHop
	return !known || previous != nextHop
}

func (s *Session) withdrawAcceptedLocked(ctx context.Context, prefix netip.Prefix) bool {
	if _, known := s.accepted[prefix]; !known {
		return false
	}
	s.removeAcceptedLocked(ctx, prefix)
	return true
}

func (s *Session) removeAcceptedLocked(ctx context.Context, prefix netip.Prefix) {
	delete(s.accepted, prefix)
	if err := s.fib.WithdrawExact(ctx, s.peerKey, prefix); err != nil {
		s.log.ErrorContext(ctx, "remove bgp session route failed", "prefix", prefix, "error", err)
	}
}

func (s *Session) rejectLocked(ctx context.Context, prefix netip.Prefix, reason string) {
	s.log.DebugContext(ctx, "bgp session rejected a learned prefix", "prefix", prefix, "reason", reason)
	s.rejected = slices.DeleteFunc(s.rejected, func(rejection Rejection) bool {
		return rejection.Prefix == prefix
	})
	s.rejected = append(s.rejected, Rejection{Prefix: prefix, Reason: reason})
	if len(s.rejected) > MaxSessionRejections {
		s.rejected = slices.Delete(s.rejected, 0, len(s.rejected)-MaxSessionRejections)
	}
}
