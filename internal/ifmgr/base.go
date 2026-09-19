package ifmgr

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"goodkind.io/mwan/internal/netif"
)

// BaseModule stores shared ifmgr module state and supplies default no-op hooks.
type BaseModule struct {
	sync.Mutex

	name string
	Env  *Env
	Log  *slog.Logger
}

// NewBaseModule returns the initialized shared state for an ifmgr module.
func NewBaseModule(name string) BaseModule {
	return BaseModule{
		Mutex: sync.Mutex{},
		name:  name,
		Env:   nil,
		Log:   nil,
	}
}

// InitBase stores env and derives the module logger for an embedding module.
// attrs is a flat sequence of string key/value pairs; a trailing unpaired
// element is ignored. Building []slog.Attr and using the handler keeps the
// signature off `any`, which the repo's no-empty-interface rule bans.
func (b *BaseModule) InitBase(env *Env, attrs ...string) *slog.Logger {
	b.Env = env
	if len(attrs) < 2 {
		b.Log = env.Log
		return b.Log
	}
	sattrs := make([]slog.Attr, 0, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		sattrs = append(sattrs, slog.String(attrs[i], attrs[i+1]))
	}
	b.Log = slog.New(env.Log.Handler().WithAttrs(sattrs))
	return b.Log
}

// Name returns the stable module identifier used in logs and config.
func (b *BaseModule) Name() string {
	return b.name
}

// OnKernelEvent is the default no-op kernel event hook.
func (b *BaseModule) OnKernelEvent(
	_ context.Context,
	_ *slog.Logger,
	_ netif.Event,
) error {
	return nil
}

// OnDHCPLease is the default no-op DHCP lease hook.
func (b *BaseModule) OnDHCPLease(
	_ context.Context,
	_ *slog.Logger,
	_ netif.LeaseInfo,
) error {
	return nil
}

// EvaluateAlerts is the default no-op alert evaluation hook.
func (b *BaseModule) EvaluateAlerts(_ context.Context, _ *slog.Logger, _ time.Time) {
}
