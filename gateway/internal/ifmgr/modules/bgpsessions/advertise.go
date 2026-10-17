package bgpsessions

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/wanstate"
)

const readinessReady = "ready"

// A session calls its listeners one at a time. The listener reads and writes
// previous without a lock.
func (m *Module) listener(cfg Session) func(bgp.SessionState) {
	var previous bgp.SessionState
	return func(state bgp.SessionState) {
		m.publish(cfg.Key())
		changed := state.Established != previous.Established || !slices.Equal(state.Accepted, previous.Accepted) ||
			!slices.Equal(state.Retained, previous.Retained)
		previous = state
		if changed && m.Env.RequestReconcile != nil {
			m.Env.RequestReconcile("bgp session " + cfg.Config.Name + " changed")
		}
	}
}

func (m *Module) publish(connectionID string) {
	if m.Env.LiveState == nil {
		return
	}
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	states := make([]bgp.SessionState, 0, len(m.sessions))
	for _, entry := range m.sessions {
		if entry.cfg.Key() == connectionID {
			states = append(states, entry.session.State())
		}
	}
	m.Env.LiveState.SetBGPSessions(connectionID, states)
}

func ipv6Ready(snapshot wanstate.Snapshot, connectionID string) bool {
	return snapshot.Connections[connectionID].IPv6.Readiness == readinessReady
}

// OnFamilyReadiness sets each running session's advertisement from the published IPv6 readiness of the connections.
func (m *Module) OnFamilyReadiness(ctx context.Context, log *slog.Logger) {
	m.Lock()
	defer m.Unlock()
	if m.Env.LiveState == nil {
		return
	}
	snapshot := m.Env.LiveState.Snapshot()
	for _, entry := range m.sessions {
		if !entry.running {
			continue
		}
		eligible := ipv6Ready(snapshot, entry.cfg.Key())
		backupActive := make(map[netip.Prefix]bool, len(entry.cfg.BackupFor))
		for prefix, connections := range entry.cfg.BackupFor {
			backupActive[prefix] = !slices.ContainsFunc(connections, func(id connectionid.ID) bool {
				return ipv6Ready(snapshot, id.String())
			})
		}
		advertiseErr := entry.session.SetAdvertisement(eligible, backupActive)
		if advertiseErr != nil {
			log.WarnContext(ctx, "bgp_sessions: advertisement change failed",
				"session", entry.cfg.Config.Name, "err", advertiseErr)
		}
		// When the previous process stops with StopRetaining, the kernel retains
		// its routes. A nonzero connectBy marks a session that starts without its
		// peer because the previous process left those routes installed.
		// For that session, connectLocked in restart.go adds the peer only after
		// SetAdvertisement succeeds and the IPv6 path has a ready or unavailable
		// verdict.
		// connectLocked logs a failed connection. The next Reconcile pass calls
		// connectLocked again and returns its error. The FamilyReadinessObserver
		// interface does not return an error. OnFamilyReadiness only logs the
		// failure.
		if advertiseErr == nil && !entry.connectBy.IsZero() && ipv6PathEvaluated(snapshot, entry.cfg.Key()) {
			if err := m.connectLocked(ctx, log, entry, "the ipv6 path has a verdict"); err != nil {
				log.DebugContext(ctx, "bgp_sessions: the next reconcile pass retries the peer connection",
					"session", entry.cfg.Config.Name)
			}
		}
		m.publish(entry.cfg.Key())
	}
}
