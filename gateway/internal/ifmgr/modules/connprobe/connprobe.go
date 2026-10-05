// Package connprobe runs periodic active connectivity probes against
// configured targets and publishes a healthy/unhealthy state. Used by
// the failover role to verify upstream actually works,
// independent of whether SLAAC and routes look correct.
//
// Failures emit a WARN; recoveries log INFO and clear the alert.
//
// Registers as "connectivity_probe".
package connprobe

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// Module owns the connectivity health state.
type Module struct {
	ifmgr.BaseModule

	cfg   Config
	clock internalclock.Clock

	lastResult    map[string]bool // key=target string, val=last probe healthy?
	lastRunAt     time.Time
	firstFailedAt map[string]time.Time // key=target string, val=first time it began failing in current run
}

// Config is the parsed [ifmgr.modules.connectivity_probe] sub-config.
type Config struct {
	Iface          string
	TargetsV6      []netip.Addr
	Timeout        time.Duration
	UnhealthyAfter time.Duration // a single failed probe must persist this long before alert
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "connectivity_probe" }

// Init implements ifmgr.Module.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "connectivity_probe", "iface", m.cfg.Iface)
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	log.InfoContext(
		ctx, "connectivity_probe: Init",
		"target_count", len(m.cfg.TargetsV6),
		"timeout", m.cfg.Timeout.String(),
		"unhealthy_after", m.cfg.UnhealthyAfter.String(),
	)
	if m.cfg.Iface == "" {
		return fmt.Errorf("connectivity_probe: iface is required")
	}
	if len(m.cfg.TargetsV6) == 0 {
		return fmt.Errorf("connectivity_probe: at least one targets_v6 entry is required")
	}
	m.lastResult = map[string]bool{}
	m.firstFailedAt = map[string]time.Time{}
	return nil
}

// Reconcile implements ifmgr.Module. Runs each probe in series and updates
// per-target failure-onset tracking used by the unhealthy_after debounce.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	probe := netif.NewV6Probe(m.cfg.Iface, log)
	now := m.clock.Now()
	results := map[string]bool{}
	for _, t := range m.cfg.TargetsV6 {
		_, err := probe.PingICMP6(ctx, t, m.cfg.Timeout)
		ok := err == nil
		results[t.String()] = ok
		log.DebugContext(ctx, "connectivity_probe: probe result",
			"target", t.String(), "ok", ok, "err", err)
	}
	m.Lock()
	for tgt, ok := range results {
		if ok {
			delete(m.firstFailedAt, tgt)
			continue
		}
		if _, already := m.firstFailedAt[tgt]; !already {
			m.firstFailedAt[tgt] = now
			log.DebugContext(ctx, "connectivity_probe: target entered failing state",
				"target", tgt, "unhealthy_after", m.cfg.UnhealthyAfter.String())
		}
	}
	m.lastResult = results
	m.lastRunAt = now
	m.Unlock()
	return nil
}

// EvaluateAlerts implements ifmgr.Module. Aggregates per-target results
// into a single iface-level alert: a target is debounced past UnhealthyAfter
// before contributing. All failing targets must be past their debounce
// before the alert fires. Resolution is immediate once all targets succeed.
func (m *Module) EvaluateAlerts(ctx context.Context, log *slog.Logger, now time.Time) {
	m.Lock()
	results := m.lastResult
	last := m.lastRunAt
	firstFailed := make(map[string]time.Time, len(m.firstFailedAt))
	maps.Copy(firstFailed, m.firstFailedAt)
	m.Unlock()

	if last.IsZero() {
		return // no probes have run yet
	}

	allOK := true
	failingTargets := []string{}
	pendingTargets := []string{}
	for t, ok := range results {
		if ok {
			continue
		}
		allOK = false
		first, tracked := firstFailed[t]
		if !tracked {
			// Defensive: treated as just-failed if we somehow missed Reconcile bookkeeping.
			pendingTargets = append(pendingTargets, t)
			continue
		}
		if now.Sub(first) >= m.cfg.UnhealthyAfter {
			failingTargets = append(failingTargets, t)
		} else {
			pendingTargets = append(pendingTargets, t)
		}
	}

	if allOK {
		if m.Env.Alerts.Active("connectivity-down", m.cfg.Iface) {
			m.Env.Alerts.ResolveContext(ctx, now,
				"connectivity-down", m.cfg.Iface,
				"connectivity_probe: all targets responding again")
		}
		return
	}
	if len(failingTargets) == 0 {
		// Some failures, none debounced past threshold yet. Stay quiet but trace it.
		log.DebugContext(ctx, "connectivity_probe: failures within debounce window",
			"pending_targets", pendingTargets,
			"unhealthy_after_s", int(m.cfg.UnhealthyAfter.Seconds()))
		return
	}
	m.Env.Alerts.NotifyContext(
		ctx, now, slog.LevelWarn,
		"connectivity-down", m.cfg.Iface,
		"connectivity_probe: one or more upstream targets unreachable",
		slog.Any("failing_targets", failingTargets),
		slog.Any("pending_targets", pendingTargets),
		slog.Int("target_count", len(results)),
		slog.Int("unhealthy_after_s", int(m.cfg.UnhealthyAfter.Seconds())),
	)
}

// New is the Constructor.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Iface:          "",
		TargetsV6:      nil,
		Timeout:        2 * time.Second,
		UnhealthyAfter: 10 * time.Second,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("connectivity_probe: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:    ifmgr.NewBaseModule("connectivity_probe"),
		cfg:           c,
		clock:         nil,
		lastResult:    nil,
		lastRunAt:     time.Time{},
		firstFailedAt: nil,
	}, nil
}

func init() { ifmgr.Register("connectivity_probe", New) }
