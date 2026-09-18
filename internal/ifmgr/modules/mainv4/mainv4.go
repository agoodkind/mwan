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

	currentCIDR string
	currentGW   string
	lastBound   time.Time
}

// Config is the parsed [ifmgr.modules.mainv4] sub-config.
type Config struct {
	Iface string
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "mainv4" }

// Init implements ifmgr.Module. Inert when DHCP is disabled so that the
// shared failover role still works on hosts that do not own DHCPv4
// (prod LXC 116 today). A no-op Init lets the role include this module
// unconditionally without breaking those hosts.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "mainv4", "iface", m.cfg.Iface)
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	if env.DHCP == nil {
		// Inert when DHCPv4 is disabled. iface is unused in this mode, so
		// don't require it; lets roles include the module unconditionally
		// without forcing every host's config to declare a placeholder iface.
		log.InfoContext(ctx, "mainv4: Init (inert: dhcp_v4 is disabled)")
		return nil
	}
	if m.cfg.Iface == "" {
		return fmt.Errorf("mainv4: iface is required when dhcp_v4 is enabled")
	}
	log.InfoContext(ctx, "mainv4: Init (active)")
	return nil
}

// Reconcile implements ifmgr.Module. v4 reacts to lease events.
func (m *Module) Reconcile(_ context.Context, _ *slog.Logger) error {
	return nil
}

// OnDHCPLease implements ifmgr.Module. Applies BOUND leases to the iface
// addr and the main-table default route. Logs every transition. When
// inert (no DHCP client wired) this is never invoked because no lease
// events are produced; this method is a no-op in that case anyway.
func (m *Module) OnDHCPLease(
	ctx context.Context, log *slog.Logger, lease netif.LeaseInfo,
) error {
	if m.Env.DHCP == nil {
		return nil
	}
	log = log.With("op", "lease-event", "state", lease.State.String())
	log.DebugContext(ctx, "mainv4: lease event", "info", lease.String())
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
		return fmt.Errorf("mainv4: lease BOUND without IP")
	}
	prefix := lease.PrefixLen
	if prefix <= 0 || prefix > 32 {
		log.WarnContext(ctx, "mainv4: lease has unusable subnet mask, defaulting to /32",
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
		TableID:  unix.RT_TABLE_MAIN,
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
		log.InfoContext(ctx, "mainv4: lease IP changed", "old", m.currentCIDR, "new", cidr)
	}
	if m.currentGW != "" && m.currentGW != want.Via {
		log.InfoContext(ctx, "mainv4: lease gateway changed", "old", m.currentGW, "new", want.Via)
	}
	m.currentCIDR = cidr
	m.currentGW = want.Via
	m.lastBound = m.clock.Now()
	return nil
}

func (m *Module) applyExpired(ctx context.Context, log *slog.Logger) error {
	log.WarnContext(ctx, "mainv4: lease expired; clearing main-table default v4")
	clearRoute := netif.RouteSpec{
		Family:   "inet",
		Dest:     "default",
		Dev:      m.cfg.Iface,
		Via:      "",
		Metric:   0,
		TableID:  unix.RT_TABLE_MAIN,
		Protocol: 0,
	}
	if err := netif.ReconcileTableDefault(ctx, log, clearRoute); err != nil {
		return fmt.Errorf("clear main-table default v4: %w", err)
	}
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
		Iface: "",
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("mainv4: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:  ifmgr.NewBaseModule("mainv4"),
		cfg:         c,
		clock:       nil,
		currentCIDR: "",
		currentGW:   "",
		lastBound:   time.Time{},
	}, nil
}

func init() { ifmgr.Register("mainv4", New) }
