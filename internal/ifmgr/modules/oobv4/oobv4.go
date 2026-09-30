// Package oobv4 implements the oob role's IPv4 module: applies DHCPv4
// lease state to the watched iface and the OOB routing table. Reacts
// to LeaseInfo events fanned out by the daemon.
//
// Registers itself as "oobv4". Selected by the oob role.
package oobv4

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// Module owns the OOB v4 state for one iface.
type Module struct {
	ifmgr.BaseModule

	cfg   Config
	clock internalclock.Clock
	owner *netif.OwnedDHCPv4Reconciler

	lastBound        time.Time // last time State==BOUND was observed
	appliedExpiresAt time.Time
	appliedEpoch     uint64
	hasApplied       bool
}

// Config is the parsed [ifmgr.modules.oobv4] sub-config.
type Config struct {
	Iface      string
	OOBTableID int
	StateFile  string
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "oobv4" }

// Init implements ifmgr.Module.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "oobv4", "iface", m.cfg.Iface)
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	log.InfoContext(ctx, "oobv4: Init", "oob_table_id", m.cfg.OOBTableID)
	if m.cfg.Iface == "" {
		return fmt.Errorf("oobv4: iface is required")
	}
	if m.cfg.OOBTableID <= 0 {
		return fmt.Errorf("oobv4: oob_table_id must be > 0")
	}
	if env.DHCP == nil {
		return fmt.Errorf("oobv4: requires DHCP client (set ifmgr.iface.<name>.dhcp_v4 = true)")
	}
	stateFile := m.cfg.StateFile
	if stateFile == "" {
		stateFile = "/var/lib/mwan/oobv4-dhcpv4.json"
	}
	owner, err := netif.NewOwnedDHCPv4Reconciler(stateFile)
	if err != nil {
		log.ErrorContext(ctx, "oobv4: ownership journal unavailable", "err", err)
		return fmt.Errorf("oobv4: open DHCPv4 ownership journal: %w", err)
	}
	if !env.DHCPRecoveryPending {
		if err := owner.PruneConsumer(ctx, "oobv4"); err != nil {
			log.ErrorContext(ctx, "oobv4: prior assignment withdrawal failed", "err", err)
			return fmt.Errorf("oobv4: withdraw prior DHCPv4 assignment: %w", err)
		}
	}
	m.owner = owner
	return nil
}

// Reconcile retries the latest valid assignment and expires the applied lease.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
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
	if err := m.owner.Reconcile(ctx, "oobv4", m.cfg.Iface, m.cfg.Iface, m.cfg.OOBTableID, 0, nil); err != nil {
		m.Log.WarnContext(ctx, "oobv4: invalidated assignment withdrawal failed", "err", err)
		return fmt.Errorf("oobv4: withdraw expired DHCPv4 assignment: %w", err)
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
	log = log.With("op", "lease-event", "state", lease.State.String())
	log.DebugContext(ctx, "oobv4: lease event", "info", lease.String())
	m.Lock()
	startupExpiry := lease.State == netif.LeaseExpired && lease.LinkIndex == 0 && m.Env.DHCPRecoveryPending && !m.hasApplied
	m.Unlock()
	if startupExpiry {
		return nil
	}
	if lease.State == netif.LeaseBound || lease.State == netif.LeaseRenewing ||
		lease.State == netif.LeaseRebinding || lease.State == netif.LeaseExpired {
		matches, err := lease.MatchesLink(m.cfg.Iface)
		if err != nil {
			return fmt.Errorf("oobv4: inspect DHCPv4 interface: %w", err)
		}
		if !matches {
			log.WarnContext(ctx, "oobv4: ignored lease from replaced interface")
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
		return fmt.Errorf("oobv4: lease BOUND without IP")
	}
	m.Lock()
	defer m.Unlock()
	if !lease.ExpiresAt.IsZero() && !m.clock.Now().Before(lease.ExpiresAt) {
		return nil
	}
	if err := m.owner.Reconcile(ctx, "oobv4", m.cfg.Iface, m.cfg.Iface, m.cfg.OOBTableID, 0, &lease); err != nil {
		m.Log.WarnContext(ctx, "oobv4: assignment application failed", "err", err)
		return fmt.Errorf("oobv4: apply DHCPv4 assignment: %w", err)
	}
	m.lastBound = m.clock.Now()
	m.appliedExpiresAt = lease.ExpiresAt
	m.appliedEpoch = lease.InvalidationEpoch
	m.hasApplied = true
	return nil
}

func (m *Module) applyExpired(ctx context.Context, log *slog.Logger) error {
	log.WarnContext(ctx, "oobv4: DHCPv4 lease expired")
	m.Lock()
	defer m.Unlock()
	if err := m.owner.Reconcile(ctx, "oobv4", m.cfg.Iface, m.cfg.Iface, m.cfg.OOBTableID, 0, nil); err != nil {
		return fmt.Errorf("oobv4: withdraw DHCPv4 assignment: %w", err)
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
		Iface:      "",
		OOBTableID: 0,
		StateFile:  "",
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("oobv4: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:       ifmgr.NewBaseModule("oobv4"),
		cfg:              c,
		clock:            nil,
		owner:            nil,
		lastBound:        time.Time{},
		appliedExpiresAt: time.Time{},
		appliedEpoch:     0,
		hasApplied:       false,
	}, nil
}

func init() { ifmgr.Register("oobv4", New) }
