// Package bridgeprobe implements an alert-only module for the
// failover role: when the watched iface has been silent
// (no RA observed AND no DHCP-server reply) for longer than a
// configured threshold AND the slaac_health module has already
// escalated without recovery, emit a WARN alert that the host-side
// veth on the iface's bridge is suspected dangling.
//
// The container cannot fix this from inside; alert-only.
//
// Registers as "bridge_probe".
package bridgeprobe

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// Module owns the bridge-suspected alert decision.
type Module struct {
	ifmgr.BaseModule

	cfg   Config
	clock internalclock.Clock

	lastRA     time.Time
	lastDHCP   time.Time
	lastLinkUp time.Time
}

// Config is the parsed [ifmgr.modules.bridge_probe] sub-config.
type Config struct {
	Iface              string
	NoSignalAlertAfter time.Duration
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "bridge_probe" }

// Init implements ifmgr.Module.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "bridge_probe", "iface", m.cfg.Iface)
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	log.InfoContext(ctx, "bridge_probe: Init",
		"no_signal_alert_after", m.cfg.NoSignalAlertAfter.String())
	if m.cfg.Iface == "" {
		return fmt.Errorf("bridge_probe: iface is required")
	}
	return nil
}

// Reconcile implements ifmgr.Module. No state to push; this is a passive
// observer that fires alerts on the periodic tick (handled in EvaluateAlerts).
func (m *Module) Reconcile(_ context.Context, _ *slog.Logger) error {
	return nil
}

// OnKernelEvent implements ifmgr.Module. Tracks the last time a RA-default
// was observed and whether the link is up.
func (m *Module) OnKernelEvent(_ context.Context, _ *slog.Logger, ev netif.Event) error {
	if ev.Iface != m.cfg.Iface {
		return nil
	}
	now := m.clock.Now()
	switch ev.Kind {
	case netif.EvRouteAdded:
		if ev.Family == "inet6" && ev.Dest == "default" {
			m.Lock()
			m.lastRA = now
			m.Unlock()
		}
	case netif.EvLinkUp:
		m.Lock()
		m.lastLinkUp = now
		m.Unlock()
	case netif.EvUnknown, netif.EvRouteDeleted, netif.EvAddrAdded, netif.EvAddrDeleted, netif.EvLinkDown:
		return nil
	}
	return nil
}

// OnDHCPLease implements ifmgr.Module. Tracks DHCP server-reply timing.
func (m *Module) OnDHCPLease(_ context.Context, _ *slog.Logger, lease netif.LeaseInfo) error {
	if lease.State == netif.LeaseBound {
		m.Lock()
		m.lastDHCP = m.clock.Now()
		m.Unlock()
	}
	return nil
}

// EvaluateAlerts implements ifmgr.Module. Fires bridge-suspected when:
//   - both lastRA and lastDHCP are stale beyond NoSignalAlertAfter, AND
//   - link is observed up, AND
//   - the slaac_health alert is currently active (so we know slaac_health
//     has already exhausted its self-heal options).
func (m *Module) EvaluateAlerts(ctx context.Context, _ *slog.Logger, now time.Time) {
	m.Lock()
	lastRA := m.lastRA
	lastDHCP := m.lastDHCP
	lastUp := m.lastLinkUp
	m.Unlock()

	thresh := m.cfg.NoSignalAlertAfter
	raStale := !lastRA.IsZero() && now.Sub(lastRA) > thresh
	dhcpStale := !lastDHCP.IsZero() && now.Sub(lastDHCP) > thresh
	linkObservedUp := !lastUp.IsZero()
	slaacActive := m.Env.Alerts.Active("slaac-degraded", m.cfg.Iface)

	suspected := raStale && dhcpStale && linkObservedUp && slaacActive

	if suspected {
		m.Env.Alerts.NotifyContext(
			ctx, now, slog.LevelWarn,
			"bridge-suspected-dangling", m.cfg.Iface,
			"bridge_probe: bridge-side veth attachment suspected dangling "+
				"(no RA, no DHCP, link observed up, SLAAC self-heal exhausted)",
			slog.String("last_ra", lastRA.Format(time.RFC3339)),
			slog.String("last_dhcp", lastDHCP.Format(time.RFC3339)),
			slog.Int("threshold_s", int(thresh.Seconds())),
			slog.String("hint", "host-side: verify veth is attached to expected bridge"),
		)
	} else if m.Env.Alerts.Active("bridge-suspected-dangling", m.cfg.Iface) {
		m.Env.Alerts.ResolveContext(ctx, now,
			"bridge-suspected-dangling", m.cfg.Iface,
			"bridge_probe: signal returned")
	}
}

// New is the Constructor.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Iface:              "",
		NoSignalAlertAfter: 120 * time.Second,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("bridge_probe: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule: ifmgr.NewBaseModule("bridge_probe"),
		cfg:        c,
		clock:      nil,
		lastRA:     time.Time{},
		lastDHCP:   time.Time{},
		lastLinkUp: time.Time{},
	}, nil
}

func init() { ifmgr.Register("bridge_probe", New) }
