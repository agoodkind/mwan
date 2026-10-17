package bgp

import (
	"context"
	"fmt"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
)

// KeepRoutes prepares a stopped session after StopRetaining ended the previous
// process. The session keeps the kernel routes of the previous process on the
// session interface and metric. The next Start runs the server without the
// peer, and Connect adds the peer. The session removes each kept route that
// the peer did not send again after the IPv6 End-of-RIB marker of the peer or
// at deadline.
func (s *Session) KeepRoutes(ctx context.Context, deadline time.Time) error {
	if s.cfg.GracefulRestartSeconds == 0 {
		return fmt.Errorf("bgp session %q has no graceful restart time", s.cfg.Name)
	}
	installed, err := s.fib.Installed(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "read the kept bgp session routes failed", "error", err)
		return fmt.Errorf("read bgp session %q kept routes: %w", s.cfg.Name, err)
	}
	timerContext := context.WithoutCancel(ctx)
	s.mu.Lock()
	if s.started || s.retaining {
		s.mu.Unlock()
		return fmt.Errorf("bgp session %q already started or keeps routes", s.cfg.Name)
	}
	s.retaining = true
	for _, prefix := range installed {
		s.retained[prefix] = struct{}{}
	}
	// The callback locks the mutex, and it reads timer only after this
	// function releases the mutex.
	var timer *time.Timer
	timer = time.AfterFunc(deadline.Sub(s.clock.Now()), func() {
		s.endRetention(timerContext, timer)
	})
	s.retainTimer = timer
	s.enqueueChangeLocked()
	s.mu.Unlock()

	s.deliverChanges()
	s.log.InfoContext(ctx, "bgp session keeps the routes of the previous process",
		"peer", s.peerKey, "retained", installed, "deadline", deadline)
	return nil
}

// Connect adds the peer to a session that Start started after KeepRoutes, and
// returns nil for a session that already added the peer.
func (s *Session) Connect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return fmt.Errorf("bgp session %q is not started", s.cfg.Name)
	}
	if s.connected {
		return nil
	}
	if err := s.addPeer(ctx, s.server); err != nil {
		return err
	}
	s.connected = true
	s.log.InfoContext(ctx, "bgp session connecting after a start with kept routes",
		"peer", s.peerKey, "advertised", len(s.advertised), "retaining", s.retaining)
	return nil
}

// StopRetaining ends the session without a NOTIFICATION message and keeps the
// installed kernel routes. A peer that negotiated graceful restart keeps the
// routes of the session for the restart time. Use Stop for a transport change.
func (s *Session) StopRetaining(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	if s.cfg.GracefulRestartSeconds == 0 {
		return fmt.Errorf("bgp session %q has no graceful restart time", s.cfg.Name)
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.cancelWatch()
	s.stopRetainTimerLocked()
	// The process exits after this call. BgpServer.Stop repeats StopBgp
	// without AllowGracefulRestart.
	stopErr := s.server.StopBgp(ctx, &apipb.StopBgpRequest{AllowGracefulRestart: true})
	wasEstablished := s.established
	s.started = false
	s.connected = false
	s.retaining = false
	s.server = nil
	s.cancelWatch = nil
	s.fsmState = bgppkt.BGP_FSM_IDLE
	s.established = false
	s.upSince = time.Time{}
	clear(s.accepted)
	clear(s.unresolved)
	clear(s.advertised)
	clear(s.retained)
	if wasEstablished {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
	if stopErr != nil {
		s.log.ErrorContext(ctx, "stop bgp session with kept routes failed", "error", stopErr)
		return fmt.Errorf("stop bgp session %q with kept routes: %w", s.cfg.Name, stopErr)
	}
	s.log.InfoContext(ctx, "bgp session stopped with kept routes")
	return nil
}
