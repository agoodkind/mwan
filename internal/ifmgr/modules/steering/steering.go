// Package steering assigns each new connection to a provider. It owns the nft
// table and chain that carry the balancing rules, computes the split from the
// active tier's healthy providers and their weights, and reprograms the chain on
// every reconcile.
//
// It runs in the wan role after wan.routes, so the policy rules its marks select
// are installed before any mark is set, and before npt, which translates the
// prefix the chosen provider delegates. It never deletes a table or a chain, so
// the kernel keeps marking on the last programmed rules across a binary swap.
package steering

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

const (
	moduleName = "steering"

	// maxSlots bounds the sum of the configured weights. The sum becomes the
	// generator's modulus and the map's element count, so a runaway weight in
	// inventory would otherwise build a map with millions of entries inside one
	// netlink batch. A gateway with a handful of providers never approaches it.
	maxSlots = 1024
)

// readHealthFunc is the health-state seam. Injecting it lets module tests run
// against a verdict set without a file on disk.
type readHealthFunc func(path string) (netif.HealthStates, error)

// Member is one provider as the balancer sees it: the shared identity, the mark
// that selects its routing table, the tier it sits in, and its share of that
// tier.
type Member struct {
	ifmgr.WANRef
	Mark   uint32
	Tier   uint8
	Weight int
}

// Config is the runtime config for the steering module. The provider list, the
// translation prefix, and the edge address come from the shared network
// configuration; the internal link and network come from the same wan.routes
// section the routing module reads, so the rules that set a mark and the rules
// that act on it name one link.
type Config struct {
	InternalIface   string
	InternalNetV4   string
	InternalPrefix  string
	OpnsenseEdgeV6  string
	HashMode        string
	HealthStateFile string
	Members         []Member
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return moduleName }

// Module owns the inet steering table and its prerouting chain.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	// Parsed once at Init from cfg.
	internalNetV4  netip.Prefix
	internalPrefix netip.Prefix
	opnsenseEdge   netip.Addr
	mode           hashMode

	// Injectable seams (real implementations wired at Init when nil).
	apply      applier
	readHealth readHealthFunc
}

// Init implements ifmgr.Module. It self-disables when the provider list is
// empty, so the wan role can list the module unconditionally.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	log.InfoContext(ctx, "steering: Init",
		"member_count", len(m.cfg.Members),
		"hash_mode", m.cfg.HashMode,
		"health_state_file", m.cfg.HealthStateFile)

	if len(m.cfg.Members) == 0 {
		log.WarnContext(ctx, "steering: no providers configured; disabling module")
		return fmt.Errorf("%w: steering: no providers", ifmgr.ErrModuleDisabled)
	}
	if err := m.parse(); err != nil {
		log.WarnContext(ctx, "steering: config parse failed", "err", err)
		return err
	}
	if m.apply == nil {
		m.apply = newNFTApplier()
	}
	if m.readHealth == nil {
		m.readHealth = netif.ReadHealthState
	}

	// The rules match on the internal link by name, so a link that comes back
	// with a new index needs the chain rewritten. The provider links are not
	// watched: a provider going away is a health verdict, which the health
	// module owns and which already drives a reconcile.
	ifmgr.StartIfaceMonitors(ctx, log, moduleName, []string{m.cfg.InternalIface}, m.onMonitorEvent)

	// The recover keeps a monitor panic from taking down the daemon.
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "steering: nft-watch panicked",
					"err", fmt.Sprint(recovered))
			}
		}()
		m.watchNFTChanges(ctx, log)
	}()
	return nil
}

// parse validates the configuration and stores the parsed prefixes, the edge
// address, and the hash mode.
func (m *Module) parse() error {
	if err := validateConfig(m.cfg); err != nil {
		return err
	}
	internalNetV4, err := netip.ParsePrefix(m.cfg.InternalNetV4)
	if err != nil {
		slog.Warn("steering: invalid internal_net_v4", "value", m.cfg.InternalNetV4, "err", err)
		return fmt.Errorf("steering: internal_net_v4 %q: %w", m.cfg.InternalNetV4, err)
	}
	if !internalNetV4.Addr().Is4() {
		return fmt.Errorf("steering: internal_net_v4 %q is not IPv4", m.cfg.InternalNetV4)
	}
	internalPrefix, err := netip.ParsePrefix(m.cfg.InternalPrefix)
	if err != nil {
		slog.Warn("steering: invalid internal_prefix", "value", m.cfg.InternalPrefix, "err", err)
		return fmt.Errorf("steering: internal_prefix %q: %w", m.cfg.InternalPrefix, err)
	}
	if internalPrefix.Addr().Is4() {
		return fmt.Errorf("steering: internal_prefix %q is not IPv6", m.cfg.InternalPrefix)
	}
	edge, err := netip.ParseAddr(m.cfg.OpnsenseEdgeV6)
	if err != nil {
		slog.Warn("steering: invalid opnsense_edge_v6", "value", m.cfg.OpnsenseEdgeV6, "err", err)
		return fmt.Errorf("steering: opnsense_edge_v6 %q: %w", m.cfg.OpnsenseEdgeV6, err)
	}
	if edge.Is4() {
		return fmt.Errorf("steering: opnsense_edge_v6 %q is not IPv6", m.cfg.OpnsenseEdgeV6)
	}
	m.internalNetV4 = internalNetV4.Masked()
	m.internalPrefix = internalPrefix.Masked()
	m.opnsenseEdge = edge
	m.mode = hashMode(m.cfg.HashMode)
	return nil
}

// validateConfig rejects a configuration the module cannot program. The
// set-wide routing-number checks run at load time in networkjson; what is left
// here is what this module alone needs.
func validateConfig(cfg Config) error {
	if cfg.InternalIface == "" {
		slog.Warn("steering: missing internal_iface")
		return fmt.Errorf("steering: internal_iface is required")
	}
	switch hashMode(cfg.HashMode) {
	case hashModeRandom, hashModeSource, hashModeSourceDestination:
	default:
		slog.Warn("steering: unknown hash mode", "value", cfg.HashMode)
		return fmt.Errorf("steering: hash_mode %q is not one of random, source, source-destination",
			cfg.HashMode)
	}
	seenNames := make(map[string]bool, len(cfg.Members))
	weightSum := 0
	for i, member := range cfg.Members {
		if member.Name == "" {
			return fmt.Errorf("steering: member[%d]: name is required", i)
		}
		if member.Iface == "" {
			return fmt.Errorf("steering: member[%d] (%s): iface is required", i, member.Name)
		}
		if seenNames[member.Name] {
			return fmt.Errorf("steering: member[%d]: duplicate name %q", i, member.Name)
		}
		seenNames[member.Name] = true
		if member.Mark == 0 {
			return fmt.Errorf("steering: member[%d] (%s): mark must be > 0", i, member.Name)
		}
		if member.Weight < 1 {
			return fmt.Errorf("steering: member[%d] (%s): weight must be >= 1", i, member.Name)
		}
		weightSum += member.Weight
	}
	if weightSum > maxSlots {
		return fmt.Errorf("steering: weights sum to %d, above the %d the balancer programs",
			weightSum, maxSlots)
	}
	return nil
}

// Reconcile implements ifmgr.Module. It reads the current verdicts, computes
// the split across the active tier, and replaces the chain's contents with it.
// The applier is called even when nothing is healthy, so a stale split is
// cleared rather than left marking traffic at a provider that failed.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	log = log.With("op", "reconcile")

	health, err := m.readHealth(m.cfg.HealthStateFile)
	if err != nil {
		log.WarnContext(ctx, "steering: ReadHealthState failed", "err", err)
		return fmt.Errorf("read health state %q: %w", m.cfg.HealthStateFile, err)
	}
	desired := m.desiredRules(health)
	if applyErr := m.apply.Apply(ctx, log, desired); applyErr != nil {
		return fmt.Errorf("apply: %w", applyErr)
	}
	return nil
}

// desiredRules computes this pass's rule set. No healthy provider anywhere
// programs no rules, which leaves the chain empty and every packet unmarked, so
// traffic falls to whatever the main table holds rather than being sent at a
// provider that failed its probes.
func (m *Module) desiredRules(health netif.HealthStates) []steerRule {
	assign, anyCarrying := balancerFor(m.cfg.Members, health)
	if !anyCarrying {
		return nil
	}
	return buildRules(ruleInput{
		InternalIface:  m.cfg.InternalIface,
		InternalNetV4:  m.internalNetV4,
		InternalPrefix: m.internalPrefix,
		OpnsenseEdgeV6: m.opnsenseEdge,
		Mode:           m.mode,
		Assign:         assign,
	})
}

// onMonitorEvent reconciles when the internal link changes state. The rules
// match that link by name, so a link that goes down and comes back needs the
// chain rewritten against its new index.
func (m *Module) onMonitorEvent(ctx context.Context, log *slog.Logger, event netif.Event) {
	if event.Kind != netif.EvLinkUp && event.Kind != netif.EvLinkDown {
		return
	}
	eventLog := log.With("kind", event.Kind.String(), "iface", event.Iface)
	eventLog.DebugContext(ctx, "steering: internal link event, reconciling")
	if err := m.Reconcile(ctx, eventLog); err != nil {
		eventLog.WarnContext(ctx, "steering: reconcile after link event failed", "err", err)
	}
}

// New is the Constructor registered with ifmgr.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		InternalIface:   "",
		InternalNetV4:   "",
		InternalPrefix:  "",
		OpnsenseEdgeV6:  "",
		HashMode:        "",
		HealthStateFile: "",
		Members:         nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("steering: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	if c.HealthStateFile == "" && len(c.Members) > 0 {
		c.HealthStateFile = netif.DefaultHealthStatePath
	}
	return &Module{
		BaseModule:     ifmgr.NewBaseModule(moduleName),
		cfg:            c,
		internalNetV4:  netip.Prefix{},
		internalPrefix: netip.Prefix{},
		opnsenseEdge:   netip.Addr{},
		mode:           "",
		apply:          nil,
		readHealth:     nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
