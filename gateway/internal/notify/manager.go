package notify

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Manager tracks incidents independently from the email repeat cadence.
// Every emitted alert uses the service logger, including alerts without an email sink.
// Concurrent Notify and Resolve calls share the same lock.
type Manager struct {
	cfg   Config
	log   *slog.Logger
	sink  Sink
	clock clock
	mu    sync.Mutex
	state map[alertID]alertState
}

// Sink receives each initial alert, configured repeat, and recovery.
// A nil Sink disables email delivery while preserving service logs.
type Sink interface {
	Send(ctx context.Context, level slog.Level, msg string, fields []slog.Attr) error
}

type alertID struct {
	kind string
	key  string
}

type alertState struct {
	active   bool
	lastEmit time.Time
	// Recovery uses the last emitted severity for minimum-level delivery filtering.
	lastLevel    slog.Level
	firstFailure time.Time
	lastFailure  time.Time
	lastMessage  string
	lastFields   []slog.Attr
}

var errLogRequired = errors.New("notify: log is required")

func newManager(log *slog.Logger, cfg Config, sink Sink) (*Manager, error) {
	if log == nil {
		return nil, errLogRequired
	}
	return &Manager{
		cfg:   cfg,
		log:   log.With("component", "notify"),
		sink:  sink,
		clock: realClock{},
		mu:    sync.Mutex{},
		state: map[alertID]alertState{},
	}, nil
}

// Notify uses the observation timestamp from Event.Now, or the wall clock
// when the caller omits it. Zero RepeatEvery suppresses repeated active alerts.
func (m *Manager) Notify(ctx context.Context, ev Event) {
	now := ev.Now
	if now.IsZero() {
		now = m.clock.Now()
	}
	if ev.IsRecovery {
		m.resolveAt(ctx, now, ev.Kind, ev.Key, ev.Message, ev.Fields...)
		return
	}
	m.notifyAt(ctx, now, ev.Level, ev.Kind, ev.Key, ev.Message, ev.Fields...)
}

// Resolve uses the last emitted alert severity for minimum-level delivery filtering.
func (m *Manager) Resolve(ctx context.Context, kind, key, msg string, fields ...slog.Attr) {
	m.resolveAt(ctx, m.clock.Now(), kind, key, msg, fields...)
}

func (m *Manager) Active(kind, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[alertID{kind: kind, key: key}]
	return ok && st.active
}

func (m *Manager) notifyAt(
	ctx context.Context, now time.Time, level slog.Level, kind, key, msg string, fields ...slog.Attr,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := alertID{kind: kind, key: key}
	st, exists := m.state[id]
	transition := !exists || !st.active
	shouldEmit := transition
	repeatEvery := m.cfg.RepeatEvery
	if duration, ok := m.cfg.PerKind[kind]; ok {
		repeatEvery = duration
	}
	if !shouldEmit && repeatEvery > 0 && now.Sub(st.lastEmit) >= repeatEvery {
		shouldEmit = true
	}
	if transition {
		st.firstFailure = now
	}
	st.active = true
	st.lastFailure = now
	st.lastMessage = msg
	st.lastFields = slices.Clone(fields)
	if shouldEmit {
		st.lastEmit = now
		st.lastLevel = level
		m.emit(ctx, level, kind, key, msg, transition, false, st.observations(now, fields))
	}
	m.state[id] = st
}

func (m *Manager) resolveAt(
	ctx context.Context, now time.Time, kind, key, msg string, fields ...slog.Attr,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := alertID{kind: kind, key: key}
	st, exists := m.state[id]
	if !exists || !st.active {
		return
	}
	delete(m.state, id)
	if !strings.HasPrefix(msg, "RECOVERED:") {
		msg = "RECOVERED: " + msg
	}
	fields = st.observations(now, fields)
	previous := append([]slog.Attr{slog.String("message", st.lastMessage)}, st.lastFields...)
	fields = append(fields, slog.GroupAttrs("last_failure", previous...))
	m.emit(ctx, st.lastLevel, kind, key, msg, false, true, fields)
}

func (st alertState) observations(now time.Time, fields []slog.Attr) []slog.Attr {
	attrs := slices.Clone(fields)
	return append(attrs,
		slog.Time("first_failure_observed", st.firstFailure),
		slog.Time("last_failure_observed", st.lastFailure),
		slog.Time("observed_at", now),
		slog.Duration("incident_duration", now.Sub(st.firstFailure)),
	)
}

// Delivery failures use WARN in the service log. Email delivery does not replace
// the original alert record or change the configured repeat cadence.
func (m *Manager) emit(
	ctx context.Context,
	level slog.Level,
	kind, key, msg string,
	transition, resolved bool,
	fields []slog.Attr,
) {
	attrs := make([]slog.Attr, 0, len(fields)+4)
	attrs = append(attrs,
		slog.String("alert_kind", kind),
		slog.String("alert_key", key),
		slog.Bool("resolved", resolved),
		slog.Bool("transition", transition),
	)
	attrs = append(attrs, fields...)
	m.log.LogAttrs(ctx, level, msg, attrs...)
	if m.sink == nil {
		return
	}
	if err := m.sink.Send(ctx, level, msg, attrs); err != nil {
		m.log.WarnContext(ctx, "notify sink send failed",
			"err", err, "alert_kind", kind, "alert_key", key)
	}
}
