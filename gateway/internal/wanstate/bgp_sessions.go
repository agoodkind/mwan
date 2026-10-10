package wanstate

import (
	"slices"

	"goodkind.io/mwan/internal/bgp"
)

func cloneBGPSessions(sessions []bgp.SessionState) []bgp.SessionState {
	copied := slices.Clone(sessions)
	for i := range copied {
		copied[i].Accepted = slices.Clone(copied[i].Accepted)
		copied[i].Advertised = slices.Clone(copied[i].Advertised)
		copied[i].Rejected = slices.Clone(copied[i].Rejected)
	}
	return copied
}

// SetBGPSessions replaces the external BGP session states of one connection with a copy of sessions.
func (s *Store) SetBGPSessions(connectionID string, sessions []bgp.SessionState) {
	copied := cloneBGPSessions(sessions)
	s.mu.Lock()
	s.bgpSessions[connectionID] = copied
	s.mu.Unlock()
}

// BGPSessions returns a copy of the external BGP session states of one connection.
func (s *Store) BGPSessions(connectionID string) []bgp.SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneBGPSessions(s.bgpSessions[connectionID])
}
