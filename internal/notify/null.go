package notify

import (
	"context"
	"log/slog"
)

// NullNotifier is the zero-value Notifier returned when [email] is
// unconfigured and the caller still needs an interface value. It drops
// every Notify and Resolve, and never reports anything as Active.
type NullNotifier struct{}

// Notify is a no-op.
func (NullNotifier) Notify(_ context.Context, _ Event) {}

// Resolve is a no-op.
func (NullNotifier) Resolve(_ context.Context, _, _, _ string, _ ...slog.Attr) {}

// Active always returns false.
func (NullNotifier) Active(_, _ string) bool { return false }
