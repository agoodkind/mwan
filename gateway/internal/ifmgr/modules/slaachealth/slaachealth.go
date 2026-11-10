// Package slaachealth implements the failover SLAAC health
// module: detects when a global IPv6 SLAAC address has gone "deprecated"
// (preferred_lft 0) or when probes to upstream targets fail, then sends
// Router Solicitations and alerts.
//
// Registers as "slaac_health".
package slaachealth

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// Module owns SLAAC health for one iface.
type Module struct {
	ifmgr.BaseModule

	cfg   Config
	clock internalclock.Clock

	degradedSince time.Time
}

// Config is the parsed [ifmgr.modules.slaac_health] sub-config.
type Config struct {
	Iface          string
	DegradedAfter  time.Duration
	AlertAfter     time.Duration
	ProbeTargetsV6 []netip.Addr
	ProbeTimeout   time.Duration
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "slaac_health" }

// Init implements ifmgr.Module.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "slaac_health", "iface", m.cfg.Iface)
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	log.InfoContext(
		ctx, "slaac_health: Init",
		"degraded_after", m.cfg.DegradedAfter.String(),
		"alert_after", m.cfg.AlertAfter.String(),
		"probe_targets", len(m.cfg.ProbeTargetsV6),
	)
	if m.cfg.Iface == "" {
		return fmt.Errorf("slaac_health: iface is required")
	}
	return nil
}

// Reconcile implements ifmgr.Module. Runs the staged health-check on
// every tick:
//
//  1. Health check: at least one global v6 with preferred_lft > 0; default
//     v6 gateway link-local responds; configured probe targets respond.
//  2. Degraded for > DegradedAfter: send Router Solicitation if RA client
//     is available.
//  3. Degraded for > AlertAfter: emit WARN alert (idempotent).
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	log = log.With("op", "reconcile")

	healthy := m.checkHealth(ctx, log)
	now := m.clock.Now()

	if healthy {
		m.handleHealthy(ctx, now, log)
		return nil
	}
	m.handleDegraded(ctx, now, log)
	return nil
}

// checkHealth runs the cheap health probes. Returns true iff all pass.
func (m *Module) checkHealth(ctx context.Context, log *slog.Logger) bool {
	if !m.hasNonDeprecatedGlobalV6(log) {
		log.DebugContext(ctx, "slaac_health: no non-deprecated global v6")
		return false
	}
	probe := netif.NewV6Probe(m.cfg.Iface, log)
	for _, t := range m.cfg.ProbeTargetsV6 {
		_, err := probe.PingICMP6(ctx, t, m.cfg.ProbeTimeout)
		if err != nil {
			log.DebugContext(ctx, "slaac_health: probe failed",
				"target", t.String(), "err", err)
			return false
		}
	}
	return true
}

// hasNonDeprecatedGlobalV6 checks netlink.AddrList for at least one global
// IPv6 address with PreferredLft > 0. A deprecated address (preferred=0)
// is a sign the upstream stopped advertising the prefix.
func (m *Module) hasNonDeprecatedGlobalV6(log *slog.Logger) bool {
	link, err := netlink.LinkByName(m.cfg.Iface)
	if err != nil {
		log.Warn("slaac_health: LinkByName failed", "err", err)
		return false
	}
	addrs, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		log.Warn("slaac_health: AddrList failed", "err", err)
		return false
	}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			continue
		}
		// Skip link-local.
		if strings.HasPrefix(strings.ToLower(a.IP.String()), "fe80") {
			continue
		}
		if a.PreferedLft > 0 {
			return true
		}
	}
	return false
}

// handleHealthy resets the degraded clock and resolves any active alert.
func (m *Module) handleHealthy(ctx context.Context, now time.Time, log *slog.Logger) {
	m.Lock()
	wasDegraded := !m.degradedSince.IsZero()
	m.degradedSince = time.Time{}
	m.Unlock()

	if wasDegraded {
		log.InfoContext(ctx, "slaac_health: recovered to healthy")
		m.Env.Alerts.ResolveContext(ctx, now, "slaac-degraded", m.cfg.Iface,
			"slaac_health: recovered")
	}
}

// handleDegraded escalates per the staged strategy.
func (m *Module) handleDegraded(ctx context.Context, now time.Time, log *slog.Logger) {
	m.Lock()
	if m.degradedSince.IsZero() {
		m.degradedSince = now
	}
	since := m.degradedSince
	m.Unlock()

	age := now.Sub(since)
	log.DebugContext(ctx, "slaac_health: degraded", "age_s", int(age.Seconds()))

	if age >= m.cfg.AlertAfter {
		m.Env.Alerts.NotifyContext(
			ctx, now, slog.LevelWarn,
			"slaac-degraded", m.cfg.Iface,
			"slaac_health: degraded beyond alert threshold",
			slog.Int("age_s", int(age.Seconds())),
			slog.Int("alert_after_s", int(m.cfg.AlertAfter.Seconds())),
		)
	}
	if age >= m.cfg.DegradedAfter {
		m.trySolicit(ctx, log)
	}
}

// trySolicit sends one Router Solicitation if the RA client is available.
// Non-fatal on failure.
func (m *Module) trySolicit(ctx context.Context, log *slog.Logger) {
	if m.Env.RA == nil {
		log.DebugContext(ctx, "slaac_health: no RA client; skipping solicit")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := m.Env.RA.SolicitRA(cctx, 5*time.Second)
	log.InfoContext(ctx, "slaac_health: sent Router Solicitation", "err", err)
}

// New is the Constructor.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Iface:          "",
		DegradedAfter:  0,
		AlertAfter:     0,
		ProbeTargetsV6: nil,
		ProbeTimeout:   2 * time.Second,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("slaac_health: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:    ifmgr.NewBaseModule("slaac_health"),
		cfg:           c,
		clock:         nil,
		degradedSince: time.Time{},
	}, nil
}

func init() { ifmgr.Register("slaac_health", New) }
