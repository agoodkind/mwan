package bgp

import (
	"context"
	"fmt"
	"net/netip"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
)

const (
	retentionEndOfRIB = "end-of-rib"
	retentionDeadline = "restart time ended"
)

func (s *Session) handleEndOfRIB(ctx context.Context, path *apiutil.Path) {
	if path == nil || path.Family != bgppkt.RF_IPv6_UC {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	// A peer that restarts sends paths that equal the stale paths in the
	// table, and GoBGP reports no best-path change for them.
	changed, err := s.reapplyBestPathsLocked(ctx)
	if err != nil {
		// Without the current best paths, the session cannot tell which
		// retained prefixes the peer sent again. The retention stays active
		// until the next End-of-RIB marker or the restart deadline.
		s.log.WarnContext(ctx, "bgp session retains its routes after a failed best path listing",
			"retained", len(s.retained), "error", err)
	} else if s.endRetentionLocked(ctx, retentionEndOfRIB) {
		changed = true
	}
	if changed {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
}

// endRetention ignores the timer of an earlier KeepRoutes call and a timer
// that fires after a stop or an End-of-RIB marker ended the route retention.
// A session without a started server deletes the retained routes without a
// table to read.
func (s *Session) endRetention(ctx context.Context, generation uint64) {
	s.mu.Lock()
	if !s.retaining || s.retainGeneration != generation {
		s.mu.Unlock()
		return
	}
	if s.started {
		// The restart deadline is the upper bound for retained routes. The
		// session deletes them at the deadline even when the best path
		// listing fails.
		if _, err := s.reapplyBestPathsLocked(ctx); err != nil {
			s.log.WarnContext(ctx, "bgp session deletes its retained routes at the deadline after a failed best path listing",
				"error", err)
		}
	}
	s.endRetentionLocked(ctx, retentionDeadline)
	s.enqueueChangeLocked()
	s.mu.Unlock()

	s.deliverChanges()
}

// endRetentionLocked deletes the retained kernel routes and reports whether
// the session was retaining routes. Connect adds the peer.
func (s *Session) endRetentionLocked(ctx context.Context, reason string) bool {
	if !s.retaining {
		return false
	}
	withdrawn := len(s.retained)
	s.dropRetainedLocked(ctx)
	s.log.InfoContext(ctx, "bgp session deleted the retained routes", "reason", reason, "withdrawn", withdrawn)
	return true
}

// dropRetainedLocked deletes every retained kernel route. An accepted prefix
// is not in the retained set.
func (s *Session) dropRetainedLocked(ctx context.Context) {
	s.retaining = false
	s.stopRetainTimerLocked()
	for prefix := range s.retained {
		if err := s.fib.WithdrawExact(ctx, s.peerKey, prefix); err != nil {
			s.log.ErrorContext(ctx, "remove retained bgp session route failed", "prefix", prefix, "error", err)
		}
	}
	clear(s.retained)
}

func (s *Session) stopRetainTimerLocked() {
	if s.retainTimer == nil {
		return
	}
	s.retainTimer.Stop()
	s.retainTimer = nil
}

func (s *Session) withdrawRetainedLocked(ctx context.Context, prefix netip.Prefix) bool {
	if _, retained := s.retained[prefix]; !retained {
		return false
	}
	delete(s.retained, prefix)
	if err := s.fib.WithdrawExact(ctx, s.peerKey, prefix); err != nil {
		s.log.ErrorContext(ctx, "remove retained bgp session route failed", "prefix", prefix, "error", err)
	}
	return true
}

// reapplyBestPathsLocked reports whether applying current best paths changed route state.
func (s *Session) reapplyBestPathsLocked(ctx context.Context) (bool, error) {
	request := apiutil.ListPathRequest{
		TableType:      apipb.TableType_TABLE_TYPE_GLOBAL,
		Name:           "",
		Family:         bgppkt.RF_IPv6_UC,
		Prefixes:       nil,
		SortType:       0,
		EnableFiltered: false,
	}
	var best []*apiutil.Path
	err := s.server.ListPath(request, func(_ bgppkt.NLRI, paths []*apiutil.Path) {
		for _, path := range paths {
			if path != nil && path.Best && !path.Stale && !path.Withdrawal {
				best = append(best, path)
			}
		}
	})
	if err != nil {
		s.log.ErrorContext(ctx, "list bgp session best paths failed", "error", err)
		return false, fmt.Errorf("list bgp session %q IPv6 best paths: %w", s.cfg.Name, err)
	}
	changed := false
	for _, path := range best {
		if s.applyBestPathLocked(ctx, path) {
			changed = true
		}
	}
	return changed, nil
}
