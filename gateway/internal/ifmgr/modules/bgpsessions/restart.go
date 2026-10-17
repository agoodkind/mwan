package bgpsessions

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"goodkind.io/mwan/internal/wanstate"
)

// retentionDeadline returns the future journal deadline of a configured
// graceful restart session.
func (m *Module) retentionDeadline(cfg Session, now time.Time) (time.Time, bool) {
	if cfg.Config.GracefulRestartSeconds == 0 {
		return time.Time{}, false
	}
	owner := sessionOwner(cfg)
	for _, record := range m.retaining {
		if record.routeOwner == owner && record.Deadline.After(now) {
			return record.Deadline, true
		}
	}
	return time.Time{}, false
}

// persistShutdownRetention writes a deadline for each graceful restart session
// that keeps its routes and returns the owners of those sessions. An
// established session gets a deadline one restart time from now only when the
// peer negotiated graceful restart for IPv6 unicast. Any other peer removes the
// session routes at the stop, and the session then stops with route removal.
// A session that has not established since a start with kept routes keeps the
// earlier deadline, because the peer started its restart timer at the earlier
// stop. After a journal write failure every session removes its routes,
// because the next process cannot read the deadline.
func (m *Module) persistShutdownRetention(ctx context.Context, log *slog.Logger, now time.Time) []routeOwner {
	var records []retention
	var unnegotiated []string
	for _, entry := range m.sessions {
		if entry.cfg.Config.GracefulRestartSeconds == 0 {
			continue
		}
		owner := sessionOwner(entry.cfg)
		if entry.running && entry.session.State().Established {
			if !entry.session.GracefulRestartNegotiated() {
				unnegotiated = append(unnegotiated, entry.cfg.Config.Name)
				continue
			}
			restart := time.Duration(entry.cfg.Config.GracefulRestartSeconds) * time.Second
			records = append(records, retention{routeOwner: owner, Deadline: now.Add(restart)})
			continue
		}
		if deadline, kept := m.retentionDeadline(entry.cfg, now); kept {
			records = append(records, retention{routeOwner: owner, Deadline: deadline})
		}
	}
	if len(unnegotiated) > 0 {
		log.InfoContext(ctx, "bgp_sessions: the peers did not negotiate graceful restart; the sessions remove their routes",
			"sessions", unnegotiated)
	}
	if len(records) == 0 && len(m.retaining) == 0 {
		return nil
	}
	if err := m.persistRetaining(ctx, log, records); err != nil {
		log.ErrorContext(ctx, "bgp_sessions: restart deadline not recorded; sessions remove their routes", "err", err)
		return nil
	}
	owners := make([]routeOwner, 0, len(records))
	for _, record := range records {
		owners = append(owners, record.routeOwner)
	}
	return owners
}

// pruneRetention removes each journal deadline that has passed or that no
// longer protects kept routes. A deadline protects the routes of a configured
// graceful restart session that has not started or that still keeps routes.
func (m *Module) pruneRetention(ctx context.Context, log *slog.Logger, now time.Time) error {
	protected := slices.DeleteFunc(slices.Clone(m.retaining), func(record retention) bool {
		return !m.protects(record, now)
	})
	if len(protected) == len(m.retaining) {
		return nil
	}
	return m.persistRetaining(ctx, log, protected)
}

func (m *Module) protects(record retention, now time.Time) bool {
	if !record.Deadline.After(now) {
		return false
	}
	for _, entry := range m.sessions {
		if sessionOwner(entry.cfg) != record.routeOwner || entry.cfg.Config.GracefulRestartSeconds == 0 {
			continue
		}
		return !entry.running || len(entry.session.State().Retained) > 0
	}
	return false
}

func (m *Module) retainedOwner(owner routeOwner, now time.Time) bool {
	return slices.ContainsFunc(m.retaining, func(record retention) bool {
		return record.routeOwner == owner && record.Deadline.After(now)
	})
}

// keepRoutes runs at Init, before the first routing evaluation reads the
// session state. A session with a future journal deadline publishes the kept
// routes, and its start delays the peer. The session first sets the
// advertisement from the IPv6 path, and the peer then receives the advertised
// prefixes before the End-of-RIB marker. The hold time before the deadline is
// the latest connection time that still lets the session establish before the
// peer removes the kept routes.
func (m *Module) keepRoutes(ctx context.Context, log *slog.Logger, entry *managedSession, now time.Time) {
	deadline, kept := m.retentionDeadline(entry.cfg, now)
	if !kept {
		return
	}
	if err := entry.session.KeepRoutes(ctx, deadline); err != nil {
		log.ErrorContext(ctx, "bgp_sessions: kept routes unreadable; the session starts without them",
			"session", entry.cfg.Config.Name, "err", err)
		return
	}
	hold := time.Duration(entry.cfg.Config.HoldSeconds) * time.Second
	entry.connectBy = deadline.Add(-hold)
	log.InfoContext(ctx, "bgp_sessions: session keeps the routes of the previous process",
		"session", entry.cfg.Config.Name, "deadline", deadline, "connect_by", entry.connectBy)
}

func (m *Module) connectWhenDue(ctx context.Context, log *slog.Logger, entry *managedSession, now time.Time) {
	if entry.connectBy.IsZero() || now.Before(entry.connectBy) {
		return
	}
	m.connectLocked(ctx, log, entry, "the restart deadline is near")
}

func (m *Module) connectLocked(ctx context.Context, log *slog.Logger, entry *managedSession, reason string) {
	if err := entry.session.Connect(ctx); err != nil {
		log.WarnContext(ctx, "bgp_sessions: session peer connection failed", "session", entry.cfg.Config.Name, "err", err)
		return
	}
	log.InfoContext(ctx, "bgp_sessions: session connects to the peer", "session", entry.cfg.Config.Name, "reason", reason)
	entry.connectBy = time.Time{}
}

func (m *Module) stopRetainingLocked(ctx context.Context, log *slog.Logger, entry *managedSession) {
	if err := entry.session.StopRetaining(ctx); err != nil {
		log.WarnContext(ctx, "bgp_sessions: session stop with kept routes failed", "session", entry.cfg.Config.Name, "err", err)
	}
	entry.running, entry.transport, entry.connectBy = false, "", time.Time{}
}

// ipv6PathEvaluated reports whether the IPv6 path of the connection has a
// first verdict in this daemon run.
func ipv6PathEvaluated(snapshot wanstate.Snapshot, connectionID string) bool {
	state := snapshot.Connections[connectionID].IPv6.Path.State
	return state == wanstate.PathReady || state == wanstate.PathUnavailable
}
