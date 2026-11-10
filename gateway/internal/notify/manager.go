package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Manager is the concrete Notifier. It owns the per-(kind, key) state
// map plus the repeat cadence resolver, and routes the rendered email
// through an optional Sink. The journald path is the same slog logger
// the rest of mwan uses, so alerts still surface in logs even when the
// email Sink is nil.
//
// Concurrency: safe for concurrent Notify and Resolve calls.
type Manager struct {
	cfg   Config
	log   *slog.Logger
	sink  Sink
	clock clock

	mu    sync.Mutex
	state map[string]alertState
}

// Sink is the email-delivery boundary. The Manager calls Send for each
// emit decision (transition or repeat). A nil Sink degrades the
// Manager to journald-only behaviour.
type Sink interface {
	Send(ctx context.Context, level slog.Level, msg string, fields []slog.Attr) error
}

// alertState is the per-(kind, key) memory: was the alert active last
// time we saw it, when did we last emit, and at what level.
//
// lastLevel is recorded at Notify-emit time so Resolve can re-emit the
// recovery line at the same severity. Without this, Resolve would log
// at INFO and be filtered out by EmailConfig.MinLevel (default ERROR),
// leaving open alerts in the inbox with no visible close.
type alertState struct {
	active    bool
	lastEmit  time.Time
	lastLevel slog.Level
}

// errLogRequired signals a structural precondition: the Manager needs
// a non-nil [slog.Logger] because every emit goes through journald
// even when the Sink is nil. Returned from newManager.
var errLogRequired = errors.New("notify: log is required")

// newManager constructs a Manager with the given config, logger, and
// sink. log must be non-nil; the constructor wires
// log.With("component", "notify") on the supplied [slog.Logger] so
// emits show up grouped in journald. Returns an error rather than
// panicking so callers can surface the precondition through their
// startup error path.
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
		state: map[string]alertState{},
	}, nil
}

// Notify emits an alert if either the (kind, key) was not active before
// (transition into bad state) or the per-kind repeat cadence has
// elapsed since the last emit. After emit, the state is marked active.
// Call Resolve to clear. ev.Now drives the clock so tests can replay
// time deterministically; an empty ev.Now defaults to the wall clock.
func (m *Manager) Notify(ctx context.Context, ev Event) {
	if ev.IsRecovery {
		now := ev.Now
		if now.IsZero() {
			now = m.clock.Now()
		}
		m.resolveAt(ctx, now, ev.Kind, ev.Key, ev.Message, ev.Fields...)
		return
	}
	now := ev.Now
	if now.IsZero() {
		now = m.clock.Now()
	}
	m.notifyAt(ctx, now, ev.Level, ev.Kind, ev.Key, ev.Message, ev.Fields...)
}

// Resolve clears the (kind, key) so the next Notify will be treated as
// a fresh transition. The recovery line emits at the same severity the
// original Notify used so it crosses the same MinLevel threshold the
// open alert did.
func (m *Manager) Resolve(ctx context.Context, kind, key, msg string, fields ...slog.Attr) {
	m.resolveAt(ctx, m.clock.Now(), kind, key, msg, fields...)
}

// Active reports whether the named alert is currently in the "fired
// but not resolved" state.
func (m *Manager) Active(kind, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[kind+"|"+key]
	return ok && st.active
}

// notifyAt is the deterministic-clock entry point used by the
// state-change tests. The public Notify consults the injected clock
// when ev.Now is zero, then funnels here.
func (m *Manager) notifyAt(
	ctx context.Context, now time.Time, level slog.Level, kind, key, msg string, fields ...slog.Attr,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := kind + "|" + key
	st, exists := m.state[id]
	shouldEmit := !exists || !st.active
	repeatEvery := m.cfg.RepeatEvery
	if d, ok := m.cfg.PerKind[kind]; ok {
		repeatEvery = d
	}
	if !shouldEmit && repeatEvery > 0 && now.Sub(st.lastEmit) >= repeatEvery {
		shouldEmit = true
	}
	transition := !exists || !st.active
	st.active = true
	if shouldEmit {
		st.lastEmit = now
		st.lastLevel = level
		m.emit(ctx, level, kind, key, msg, transition, false, fields)
	}
	m.state[id] = st
}

// resolveAt is the deterministic-clock entry point for Resolve. The
// public Resolve consults the injected clock then funnels here.
func (m *Manager) resolveAt(
	ctx context.Context, now time.Time, kind, key, msg string, fields ...slog.Attr,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := kind + "|" + key
	st, exists := m.state[id]
	if !exists || !st.active {
		return
	}
	st.active = false
	st.lastEmit = now
	m.state[id] = st
	level := st.lastLevel
	// Defensive: an alertState may exist with active=true but lastLevel
	// unset if a future code path ever marks active without going
	// through the emit branch. Fall back to WARN so the recovery still
	// surfaces.
	if level == 0 {
		level = slog.LevelWarn
	}
	if !strings.HasPrefix(msg, "RECOVERED:") {
		msg = "RECOVERED: " + msg
	}
	m.emit(ctx, level, kind, key, msg, false, true, fields)
}

// emit logs the record through the slog journal path and (when the
// sink is non-nil) hands it to the email sink. The sink errors are
// logged at WARN since failing to deliver an alert is itself an
// alert-worthy condition, but the slog emit always succeeds.
func (m *Manager) emit(
	ctx context.Context,
	level slog.Level,
	kind, key, msg string,
	transition, resolved bool,
	fields []slog.Attr,
) {
	attrs := make([]slog.Attr, 0, len(fields)+3)
	attrs = append(attrs, slog.String("alert_kind", kind))
	attrs = append(attrs, slog.String("alert_key", key))
	if resolved {
		attrs = append(attrs, slog.Bool("resolved", true))
	} else {
		attrs = append(attrs, slog.Bool("transition", transition))
	}
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
