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

	currentCIDR string    // last-applied address (e.g. "158.247.70.13/26")
	currentGW   string    // last-applied default gateway in oob table
	lastBound   time.Time // last time State==BOUND was observed
}

// Config is the parsed [ifmgr.modules.oobv4] sub-config.
type Config struct {
	Iface      string
	OOBTableID int
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
	return nil
}

// Reconcile implements ifmgr.Module. v4 reacts to lease events; nothing
// to do on the periodic tick beyond what OnDHCPLease already applied.
func (m *Module) Reconcile(_ context.Context, _ *slog.Logger) error {
	return nil
}

// OnDHCPLease implements ifmgr.Module. Translates lease state to kernel
// mutations (address replace, OOB-table default replace/clear).
func (m *Module) OnDHCPLease(
	ctx context.Context, log *slog.Logger, lease netif.LeaseInfo,
) error {
	log = log.With("op", "lease-event", "state", lease.State.String())
	log.DebugContext(ctx, "oobv4: lease event", "info", lease.String())

	switch lease.State {
	case netif.LeaseBound:
		return m.applyBound(ctx, log, lease)
	case netif.LeaseExpired:
		return m.applyExpired(ctx, log)
	case netif.LeaseInit, netif.LeaseSelecting, netif.LeaseRequesting, netif.LeaseRenewing:
		return nil
	}
	return nil
}

func (m *Module) applyBound(
	ctx context.Context, log *slog.Logger, lease netif.LeaseInfo,
) error {
	if lease.IP == nil {
		return fmt.Errorf("oobv4: lease BOUND without IP")
	}
	prefix := lease.PrefixLen
	if prefix <= 0 || prefix > 32 {
		log.WarnContext(ctx, "oobv4: lease has unusable subnet mask, defaulting to /32",
			"prefix_len", lease.PrefixLen)
		prefix = 32
	}
	cidr := fmt.Sprintf("%s/%d", lease.IP.String(), prefix)

	if err := netif.ReconcileAddrs(ctx, log, m.cfg.Iface, []netif.AddrSpec{
		{CIDR: cidr, Family: "inet"},
	}); err != nil {
		return fmt.Errorf("apply lease addr %s: %w", cidr, err)
	}

	want := netif.RouteSpec{
		Family:   "inet",
		Dest:     "default",
		Dev:      m.cfg.Iface,
		Via:      "",
		Metric:   0,
		TableID:  m.cfg.OOBTableID,
		Protocol: 0,
	}
	if lease.Gateway != nil {
		want.Via = lease.Gateway.String()
	}
	if err := netif.ReconcileTableDefault(ctx, log, want); err != nil {
		return fmt.Errorf("apply lease default route: %w", err)
	}

	m.Lock()
	defer m.Unlock()
	if m.currentCIDR != "" && m.currentCIDR != cidr {
		log.InfoContext(ctx, "oobv4: lease IP changed", "old", m.currentCIDR, "new", cidr)
	}
	if m.currentGW != "" && m.currentGW != want.Via {
		log.InfoContext(ctx, "oobv4: lease gateway changed", "old", m.currentGW, "new", want.Via)
	}
	m.currentCIDR = cidr
	m.currentGW = want.Via
	m.lastBound = m.clock.Now()
	return nil
}

func (m *Module) applyExpired(ctx context.Context, log *slog.Logger) error {
	log.WarnContext(ctx, "oobv4: lease expired; clearing oob default v4")
	clearRoute := netif.RouteSpec{
		Family:   "inet",
		Dest:     "default",
		Dev:      m.cfg.Iface,
		Via:      "",
		Metric:   0,
		TableID:  m.cfg.OOBTableID,
		Protocol: 0,
	}
	if err := netif.ReconcileTableDefault(ctx, log, clearRoute); err != nil {
		return fmt.Errorf("clear oob default v4: %w", err)
	}
	// Address removal intentionally NOT done; kernel keeps lease IP until
	// next BOUND replaces it atomically.
	m.Lock()
	defer m.Unlock()
	m.currentGW = ""
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
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("oobv4: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:  ifmgr.NewBaseModule("oobv4"),
		cfg:         c,
		clock:       nil,
		currentCIDR: "",
		currentGW:   "",
		lastBound:   time.Time{},
	}, nil
}

func init() { ifmgr.Register("oobv4", New) }
