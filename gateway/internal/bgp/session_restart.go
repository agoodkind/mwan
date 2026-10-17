package bgp

import (
	"context"
	"fmt"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
)

// KeepRoutes prepares a stopped session after the previous process called
// StopRetaining before it exited. KeepRoutes records the kernel routes that
// the previous process installed on the session interface and metric, and the
// session does not delete them. The next Start runs the server without the
// peer, and Connect adds the peer. After the peer sends its IPv6 End-of-RIB
// marker or the deadline expires, the session deletes each recorded route that
// the peer did not send again.
func (s *Session) KeepRoutes(ctx context.Context, deadline time.Time) error {
	if s.cfg.GracefulRestartSeconds == 0 {
		return fmt.Errorf("bgp session %q has no graceful restart time", s.cfg.Name)
	}
	installed, err := s.fib.Installed(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "failed to read the retained BGP session routes", "error", err)
		return fmt.Errorf("read bgp session %q retained routes: %w", s.cfg.Name, err)
	}
	timerContext := context.WithoutCancel(ctx)
	s.mu.Lock()
	if s.started || s.retaining {
		s.mu.Unlock()
		return fmt.Errorf("bgp session %q already started or already retains routes", s.cfg.Name)
	}
	s.retaining = true
	for _, prefix := range installed {
		s.retained[prefix] = struct{}{}
	}
	// The callback receives the generation by value and compares it with the
	// generation field only after it locks the mutex.
	s.retainGeneration++
	generation := s.retainGeneration
	s.retainTimer = time.AfterFunc(deadline.Sub(s.clock.Now()), func() {
		s.endRetention(timerContext, generation)
	})
	s.enqueueChangeLocked()
	s.mu.Unlock()

	s.deliverChanges()
	s.log.InfoContext(ctx, "bgp session retains the kernel routes of the previous process",
		"peer", s.peerKey, "retained", installed, "deadline", deadline)
	return nil
}

// GracefulRestartNegotiated reports whether the established peer advertised
// the RFC 4724 capability for IPv6 unicast to a session with a restart time. A
// peer without that capability removes the session routes at StopRetaining.
func (s *Session) GracefulRestartNegotiated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.established && s.gracefulRestart
}

func ipv6GracefulRestart(capabilities []bgppkt.ParameterCapabilityInterface) bool {
	for _, capability := range capabilities {
		gracefulRestart, ok := capability.(*bgppkt.CapGracefulRestart)
		if !ok {
			continue
		}
		for _, tuple := range gracefulRestart.Tuples {
			if tuple.AFI == bgppkt.AFI_IP6 && tuple.SAFI == bgppkt.SAFI_UNICAST {
				return true
			}
		}
	}
	return false
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
	s.log.InfoContext(ctx, "bgp session connecting after a start with retained routes",
		"peer", s.peerKey, "advertised", len(s.advertised), "retaining", s.retaining)
	return nil
}

// StopRetaining ends the session without a NOTIFICATION message and does not
// delete the installed kernel routes. A peer that negotiated graceful restart
// marks the session paths stale and withdraws them only after the restart time
// or after the End-of-RIB marker of the next session. Use Stop for a transport
// change.
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
	s.resetStoppedLocked()
	if wasEstablished {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
	if stopErr != nil {
		s.log.ErrorContext(ctx, "stop bgp session with retained routes failed", "error", stopErr)
		return fmt.Errorf("stop bgp session %q with retained routes: %w", s.cfg.Name, stopErr)
	}
	s.log.InfoContext(ctx, "bgp session stopped with retained routes")
	return nil
}
