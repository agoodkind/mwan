package bgp

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
)

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
