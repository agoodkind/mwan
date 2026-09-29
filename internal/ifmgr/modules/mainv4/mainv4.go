// Package mainv4 applies DHCPv4 lease state to the watched interface and
// to the main routing table. This is the failover analogue of oobv4
// (which applies to a separate OOB table for vault).
//
// Use this when the daemon owns DHCPv4 for the iface and you want the
// lease to drive the iface's primary v4 configuration. Pair with
// ifmgr.iface.<name>.dhcp_v4 = true.
//
// Registers as "mainv4". Selected by the failover role when dhcp_v4
// is enabled.
package mainv4

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/unix"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// Module owns the main-table v4 state for one iface.
type Module struct {
	ifmgr.BaseModule

	cfg   Config
	clock internalclock.Clock
	owner *netif.OwnedDHCPv4Reconciler

	lastBound        time.Time
	appliedExpiresAt time.Time
	appliedEpoch     uint64
	hasApplied       bool
}

// Config is the parsed [ifmgr.modules.mainv4] sub-config.
type Config struct {
	Iface     string
	StateFile string
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "mainv4" }

// Init implements ifmgr.Module. A disabled client withdraws journaled assignments.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "mainv4", "iface", m.cfg.Iface)
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	if env.DHCP != nil && m.cfg.Iface == "" {
		return fmt.Errorf("mainv4: iface is required when dhcp_v4 is enabled")
	}
	stateFile := m.cfg.StateFile
	if stateFile == "" {
		stateFile = "/var/lib/mwan/mainv4-dhcpv4.json"
	}
	owner, err := netif.NewOwnedDHCPv4Reconciler(stateFile)
	if err != nil {
		log.ErrorContext(ctx, "mainv4: ownership journal unavailable", "err", err)
		return fmt.Errorf("mainv4: open DHCPv4 ownership journal: %w", err)
	}
	if err := owner.PruneConsumer(ctx, "mainv4"); err != nil {
		log.ErrorContext(ctx, "mainv4: prior assignment withdrawal failed", "err", err)
		return fmt.Errorf("mainv4: withdraw prior DHCPv4 assignment: %w", err)
	}
	m.owner = owner
	if env.DHCP == nil {
		log.InfoContext(ctx, "mainv4: Init (inert: dhcp_v4 is disabled)")
		return nil
	}
	log.InfoContext(ctx, "mainv4: Init (active)")
	return nil
}

// Reconcile retries the latest valid assignment and expires the applied lease.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	if m.Env.DHCP == nil {
		return nil
	}
	lease := m.Env.DHCP.LastLease()
	if err := m.withdrawInvalidated(ctx, lease); err != nil {
		return err
	}
	if lease.State == netif.LeaseBound || lease.State == netif.LeaseExpired ||
		lease.State == netif.LeaseRenewing || lease.State == netif.LeaseRebinding {
		return m.OnDHCPLease(ctx, log, lease)
	}
	return nil
}

func (m *Module) withdrawInvalidated(ctx context.Context, lease netif.LeaseInfo) error {
	m.Lock()
	defer m.Unlock()
	if !m.hasApplied {
		if lease.InvalidationEpoch > m.appliedEpoch {
			m.appliedEpoch = lease.InvalidationEpoch
		}
		return nil
	}
	expired := !m.appliedExpiresAt.IsZero() && !m.clock.Now().Before(m.appliedExpiresAt)
	if !expired && lease.InvalidationEpoch <= m.appliedEpoch {
		return nil
	}
	if err := m.owner.Reconcile(ctx, "mainv4", m.cfg.Iface, m.cfg.Iface, unix.RT_TABLE_MAIN, 0, nil); err != nil {
		m.Log.WarnContext(ctx, "mainv4: invalidated assignment withdrawal failed", "err", err)
		return fmt.Errorf("mainv4: withdraw invalidated DHCPv4 assignment: %w", err)
	}
	m.appliedExpiresAt = time.Time{}
	if lease.InvalidationEpoch > m.appliedEpoch {
		m.appliedEpoch = lease.InvalidationEpoch
	}
	m.hasApplied = false
	return nil
}

// OnDHCPLease implements ifmgr.Module.
func (m *Module) OnDHCPLease(
	ctx context.Context, log *slog.Logger, lease netif.LeaseInfo,
) error {
	if m.Env.DHCP == nil {
		return nil
	}
	log = log.With("op", "lease-event", "state", lease.State.String())
	log.DebugContext(ctx, "mainv4: lease event", "info", lease.String())
	if lease.State == netif.LeaseBound || lease.State == netif.LeaseRenewing ||
		lease.State == netif.LeaseRebinding || lease.State == netif.LeaseExpired {
		matches, err := lease.MatchesLink(m.cfg.Iface)
		if err != nil {
			return fmt.Errorf("mainv4: inspect DHCPv4 interface: %w", err)
		}
		if !matches {
			log.WarnContext(ctx, "mainv4: ignored lease from replaced interface")
			return nil
		}
	}
	m.Lock()
	stale := lease.InvalidationEpoch < m.appliedEpoch
	m.Unlock()
	if stale {
		return nil
	}
	if err := m.withdrawInvalidated(ctx, lease); err != nil {
		return err
	}
	switch lease.State {
	case netif.LeaseBound:
		return m.applyBound(ctx, lease)
	case netif.LeaseExpired:
		return m.applyExpired(ctx, log)
	case netif.LeaseRenewing, netif.LeaseRebinding:
		m.Lock()
		applied := m.hasApplied
		m.Unlock()
		if !applied {
			lease.State = netif.LeaseBound
			return m.applyBound(ctx, lease)
		}
		return nil
	case netif.LeaseInit, netif.LeaseSelecting, netif.LeaseRequesting:
		return nil
	}
	return nil
}

func (m *Module) applyBound(ctx context.Context, lease netif.LeaseInfo) error {
	if lease.IP == nil {
		return fmt.Errorf("mainv4: lease BOUND without IP")
	}
	m.Lock()
	defer m.Unlock()
	if !lease.ExpiresAt.IsZero() && !m.clock.Now().Before(lease.ExpiresAt) {
		return nil
	}
	if err := m.owner.Reconcile(ctx, "mainv4", m.cfg.Iface, m.cfg.Iface, unix.RT_TABLE_MAIN, 0, &lease); err != nil {
		m.Log.WarnContext(ctx, "mainv4: assignment application failed", "err", err)
		return fmt.Errorf("mainv4: apply DHCPv4 assignment: %w", err)
	}
	m.lastBound = m.clock.Now()
	m.appliedExpiresAt = lease.ExpiresAt
	m.appliedEpoch = lease.InvalidationEpoch
	m.hasApplied = true
	return nil
}

func (m *Module) applyExpired(ctx context.Context, log *slog.Logger) error {
	log.WarnContext(ctx, "mainv4: DHCPv4 lease expired")
	m.Lock()
	defer m.Unlock()
	if err := m.owner.Reconcile(ctx, "mainv4", m.cfg.Iface, m.cfg.Iface, unix.RT_TABLE_MAIN, 0, nil); err != nil {
		return fmt.Errorf("mainv4: withdraw DHCPv4 assignment: %w", err)
	}
	m.appliedExpiresAt = time.Time{}
	m.hasApplied = false
	return nil
}

// LastBound exposes the last BOUND timestamp for ralost.
func (m *Module) LastBound() time.Time {
	m.Lock()
	defer m.Unlock()
	return m.lastBound
}

// New is the Constructor.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Iface:     "",
		StateFile: "",
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("mainv4: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:       ifmgr.NewBaseModule("mainv4"),
		cfg:              c,
		clock:            nil,
		owner:            nil,
		lastBound:        time.Time{},
		appliedExpiresAt: time.Time{},
		appliedEpoch:     0,
		hasApplied:       false,
	}, nil
}

func init() { ifmgr.Register("mainv4", New) }
