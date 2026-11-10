package ifmgr

import (
	"context"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/notify"
)

// AlertManager is the ifmgr-facing adapter over the notify package.
// The state machine (per-(kind, key) active/lastEmit/lastLevel plus
// repeat cadence) lives in notify.Manager. This wrapper keeps the
// existing module call surface centered on [time.Time] plus
// variadic [slog.Attr] fields.
//
// Concurrency: forwards to notify.Notifier, which is safe for concurrent
// Notify and Resolve calls.
type AlertManager struct {
	n notify.Notifier
}

// WrapNotifier adapts an existing notify.Notifier to the AlertManager
// surface. The daemon path uses this so cmd/mwan can build the
// underlying Manager once via notify.FromConfig (with the email sink
// wired) and pass it through DaemonConfig.
func WrapNotifier(n notify.Notifier) *AlertManager {
	if n == nil {
		n = notify.NullNotifier{}
	}
	return &AlertManager{n: n}
}

// NotifyContext emits an alert with the caller's context.
func (a *AlertManager) NotifyContext(
	ctx context.Context,
	now time.Time,
	level slog.Level,
	kind,
	key,
	msg string,
	fields ...slog.Attr,
) {
	a.n.Notify(ctx, notify.Event{
		Now:        now,
		Level:      level,
		Kind:       kind,
		Key:        key,
		Message:    msg,
		Fields:     fields,
		IsRecovery: false,
	})
}

// Notify emits an alert at the given level via the wrapped Notifier.
func (a *AlertManager) Notify(
	now time.Time, level slog.Level, kind, key, msg string, fields ...slog.Attr,
) {
	a.NotifyContext(context.Background(), now, level, kind, key, msg, fields...)
}

// ResolveContext clears an alert with the caller's context.
func (a *AlertManager) ResolveContext(
	ctx context.Context,
	now time.Time,
	kind,
	key,
	msg string,
	fields ...slog.Attr,
) {
	// notify.Notifier.Resolve does not take a now; the wrapped Manager
	// reads its clock internally. The now argument is preserved on this
	// adapter only so module call sites stay unchanged.
	_ = now
	a.n.Resolve(ctx, kind, key, msg, fields...)
}

// Resolve clears the (kind, key) so the next Notify is treated as a
// fresh transition. The wrapped Notifier emits the recovery line at
// the level the original Notify used; see notify.Manager for the
// state-change semantics.
func (a *AlertManager) Resolve(now time.Time, kind, key, msg string, fields ...slog.Attr) {
	a.ResolveContext(context.Background(), now, kind, key, msg, fields...)
}

// Active reports whether the named alert is currently in the "fired
// but not resolved" state. Forwards to the wrapped Notifier.
func (a *AlertManager) Active(kind, key string) bool {
	return a.n.Active(kind, key)
}
