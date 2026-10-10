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
		changed := state.Established != previous.Established || !slices.Equal(state.Accepted, previous.Accepted)
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
		if err := entry.session.SetAdvertisement(eligible, backupActive); err != nil {
			log.WarnContext(ctx, "bgp_sessions: advertisement change failed",
				"session", entry.cfg.Config.Name, "err", err)
		}
		m.publish(entry.cfg.Key())
	}
}
