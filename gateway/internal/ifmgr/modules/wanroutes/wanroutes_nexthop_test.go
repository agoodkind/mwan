package wanroutes

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// stubResolver returns a fixed sequence of results, repeating the last one.
func stubResolver(results ...bool) func(
	ctx context.Context, log *slog.Logger, dev string, addr string,
) (bool, error) {
	callCount := 0
	return func(context.Context, *slog.Logger, string, string) (bool, error) {
		result := results[min(callCount, len(results)-1)]
		callCount++
		return result, nil
	}
}

func newNextHopModule(t *testing.T) *Module {
	t.Helper()
	module := &Module{cfg: testConfig()}
	module.InitBase(testEnv(), "module", moduleName)
	return module
}

func (m *Module) runNextHopCheck(ctx context.Context, log *slog.Logger) {
	m.Lock()
	defer m.Unlock()
	m.checkNextHopLocked(ctx, log)
}

// The alert fires only after two consecutive unresolved checks, and a
// successful resolution afterwards clears it through the real notify manager.
func TestNextHopAlertLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	module := newNextHopModule(t)
	module.resolveNextHop = stubResolver(false)
	log := module.Log
	now := time.Unix(1700000000, 0)

	module.runNextHopCheck(ctx, log)
	module.EvaluateAlerts(ctx, log, now)
	if module.Env.Alerts.Active(alertKindNextHopUnresolved, module.cfg.OpnsenseEdgeV6) {
		t.Fatal("alert fired after a single miss; threshold is two")
	}

	module.runNextHopCheck(ctx, log)
	module.EvaluateAlerts(ctx, log, now.Add(time.Minute))
	if !module.Env.Alerts.Active(alertKindNextHopUnresolved, module.cfg.OpnsenseEdgeV6) {
		t.Fatal("alert did not fire after two consecutive misses")
	}

	module.resolveNextHop = stubResolver(true)
	module.runNextHopCheck(ctx, log)
	module.EvaluateAlerts(ctx, log, now.Add(2*time.Minute))
	if module.Env.Alerts.Active(alertKindNextHopUnresolved, module.cfg.OpnsenseEdgeV6) {
		t.Fatal("alert did not clear after the next hop resolved")
	}
}

// A check error leaves the miss counter untouched, so an active alert stays
// active and an inactive one does not fire on unknown state.
func TestNextHopCheckErrorKeepsState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	module := newNextHopModule(t)
	module.resolveNextHop = stubResolver(false)
	log := module.Log
	now := time.Unix(1700000000, 0)

	module.runNextHopCheck(ctx, log)
	module.runNextHopCheck(ctx, log)
	module.EvaluateAlerts(ctx, log, now)
	if !module.Env.Alerts.Active(alertKindNextHopUnresolved, module.cfg.OpnsenseEdgeV6) {
		t.Fatal("alert did not fire after two consecutive misses")
	}

	module.resolveNextHop = func(
		context.Context, *slog.Logger, string, string,
	) (bool, error) {
		return false, errors.New("netlink unavailable")
	}
	module.runNextHopCheck(ctx, log)
	module.EvaluateAlerts(ctx, log, now.Add(time.Minute))
	if !module.Env.Alerts.Active(alertKindNextHopUnresolved, module.cfg.OpnsenseEdgeV6) {
		t.Fatal("check error cleared the standing alert; unknown is not resolution")
	}
}
