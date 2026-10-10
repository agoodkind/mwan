package bgp

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
)

func (s *Session) installLocked(ctx context.Context, prefix netip.Prefix, nextHop netip.Addr) bool {
	event := PathEvent{Peer: s.peerKey, Prefix: prefix, NextHop: nextHop, Withdrawn: false}
	if err := s.fib.Apply(ctx, event); err != nil {
		s.failInstallLocked(ctx, prefix, nextHop, err)
		return true
	}
	previous, known := s.accepted[prefix]
	s.acceptLocked(prefix, nextHop)
	return !known || previous != nextHop
}

func (s *Session) acceptLocked(prefix netip.Prefix, nextHop netip.Addr) {
	delete(s.unresolved, prefix)
	s.accepted[prefix] = nextHop
	s.rejected = slices.DeleteFunc(s.rejected, func(rejection Rejection) bool {
		return rejection.Prefix == prefix
	})
}

func (s *Session) failInstallLocked(ctx context.Context, prefix netip.Prefix, nextHop netip.Addr, err error) {
	s.log.ErrorContext(ctx, "install bgp session route failed", "prefix", prefix, "error", err)
	// A failed replacement does not remove the earlier route for the prefix
	// from the kernel. The peer no longer announces that route's next hop.
	s.removeAcceptedLocked(ctx, prefix)
	s.rejectLocked(ctx, prefix, fmt.Sprintf("%s: %v", rejectedNotInstall, err))
	s.unresolved[prefix] = nextHop
}

// RefreshRoutes reinstalls each accepted route that is absent from the kernel or uses another gateway than the kernel route to the BGP next hop, and retries each path that failed installation.
func (s *Session) RefreshRoutes(ctx context.Context) {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	changed := false
	for prefix, nextHop := range s.unresolved {
		event := PathEvent{Peer: s.peerKey, Prefix: prefix, NextHop: nextHop, Withdrawn: false}
		if err := s.fib.Apply(ctx, event); err != nil {
			s.removeAcceptedLocked(ctx, prefix)
			continue
		}
		s.acceptLocked(prefix, nextHop)
		changed = true
	}
	rejected, err := s.fib.Refresh(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "refresh bgp session routes failed; the installed routes stay", "error", err)
	}
	for prefix, cause := range rejected {
		nextHop, known := s.accepted[prefix]
		if !known {
			continue
		}
		s.failInstallLocked(ctx, prefix, nextHop, cause)
		changed = true
	}
	if changed {
		s.enqueueChangeLocked()
	}
	s.mu.Unlock()

	s.deliverChanges()
}
