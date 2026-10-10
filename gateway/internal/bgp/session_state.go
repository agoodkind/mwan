package bgp

import (
	"net/netip"
	"slices"
	"time"
)

// MaxSessionRejections limits the number of rejected prefixes a
// SessionState reports.
const MaxSessionRejections = 32

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

// OnChange registers a listener that receives the session state after the
// session establishes, loses the peer, changes the accepted prefixes, or
// rejects a prefix. Listeners run one at a time in the order of the state
// changes. Do not block within listeners.
func (s *Session) OnChange(listener func(SessionState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, listener)
}

// State returns a copy that later session events do not modify.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
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
