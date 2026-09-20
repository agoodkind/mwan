// Package pinned fills the pinned-destination address sets in the inet mangle
// table: the seed ranges the configuration lists, the addresses its host
// names resolve to, and the prefixes the published feed lists. The marking
// rules in that table read those sets, so their contents decide which
// destinations leave over the pinned provider. The module owns the contents of
// the two sets and nothing else in the table.
package pinned

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
)

const (
	moduleName = "pinned"

	// defaultRefreshInterval is the cadence the shell refresher's timer ran at,
	// which this module takes over.
	defaultRefreshInterval = 6 * time.Hour

	// defaultRefreshTimeout bounds one refresh. A refresh resolves host names
	// and fetches the feed inside the daemon's reconcile pass, so an
	// unreachable resolver or feed must not hold that pass open indefinitely.
	defaultRefreshTimeout = 2 * time.Minute

	// refreshRetryInterval is how long a failed refresh waits before the next
	// attempt. A failed write leaves the sets holding whatever the last
	// successful write left, so retrying sooner than the full interval is what
	// keeps a transient netlink or network failure from costing six hours of
	// stale contents.
	refreshRetryInterval = 10 * time.Minute
)

// Config is the runtime config for the pinned module. Enabled is the gate: the
// module runs only where the configuration turns it on, because the shell
// refresher still writes the same two sets.
type Config struct {
	Enabled         bool
	RefreshInterval time.Duration
	RefreshTimeout  time.Duration
	// FeedURL is the published prefix list, empty when the configuration lists
	// none. The body is read as one prefix per line, whatever the URL ends in.
	FeedURL     string
	SeedCIDRsV4 []string
	SeedCIDRsV6 []string
	FQDNsV4     []string
	FQDNsV6     []string
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return moduleName }

// Module owns the contents of the two pinned-destination sets.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	// Parsed once at Init from cfg.
	seedsV4 []netip.Prefix
	seedsV6 []netip.Prefix

	// Injectable seams (real implementations wired at Init when nil).
	resolver   *net.Resolver
	httpClient *http.Client
	apply      applier
	clock      internalclock.Clock

	// nextRefresh is the earliest wall-clock time the next refresh may run.
	// Guarded by the embedded mutex.
	nextRefresh time.Time
}

// Init implements ifmgr.Module. Self-disables when the configuration does not
// turn the module on, so the wan role can list it unconditionally.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)

	if !m.cfg.Enabled {
		log.InfoContext(ctx, "pinned: disabled by configuration")
		return fmt.Errorf("%w: pinned: [ifmgr.modules.pinned] enabled is false", ifmgr.ErrModuleDisabled)
	}
	if err := m.parse(); err != nil {
		log.WarnContext(ctx, "pinned: config parse failed", "err", err)
		return err
	}

	if m.cfg.RefreshInterval <= 0 {
		m.cfg.RefreshInterval = defaultRefreshInterval
	}
	if m.cfg.RefreshTimeout <= 0 {
		m.cfg.RefreshTimeout = defaultRefreshTimeout
	}
	if m.resolver == nil {
		m.resolver = net.DefaultResolver
	}
	if m.httpClient == nil {
		m.httpClient = &http.Client{
			Transport:     nil,
			CheckRedirect: nil,
			Jar:           nil,
			Timeout:       m.cfg.RefreshTimeout,
		}
	}
	if m.apply == nil {
		m.apply = newNFTApplier()
	}
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}

	log.InfoContext(ctx, "pinned: Init",
		"seed_v4", len(m.seedsV4), "seed_v6", len(m.seedsV6),
		"fqdn_v4", len(m.cfg.FQDNsV4), "fqdn_v6", len(m.cfg.FQDNsV6),
		"feed", m.cfg.FeedURL != "",
		"refresh_interval", m.cfg.RefreshInterval.String())
	return nil
}

// parse validates the seed ranges, so a typo fails startup rather than
// silently dropping a range from every refresh.
func (m *Module) parse() error {
	var err error
	m.seedsV4, err = parsePrefixes(m.cfg.SeedCIDRsV4, familyV4, "seed_cidrs_v4")
	if err != nil {
		return err
	}
	m.seedsV6, err = parsePrefixes(m.cfg.SeedCIDRsV6, familyV6, "seed_cidrs_v6")
	if err != nil {
		return err
	}
	return nil
}

// Reconcile implements ifmgr.Module. The daemon ticks far more often than the
// refresh cadence, so a pass that is not yet due returns without resolving a
// name, fetching the feed, or opening a netlink socket.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	now := m.clock.Now()
	if now.Before(m.nextRefresh) {
		return nil
	}
	log = log.With("op", "refresh")

	refreshCtx, cancel := context.WithTimeout(ctx, m.cfg.RefreshTimeout)
	defer cancel()

	desired := m.collect(refreshCtx, log)
	if err := m.apply.Apply(refreshCtx, log, desired); err != nil {
		m.nextRefresh = now.Add(refreshRetryInterval)
		return err
	}
	m.nextRefresh = now.Add(m.cfg.RefreshInterval)
	log.InfoContext(ctx, "pinned: sets refreshed",
		"v4_ranges", len(desired.V4), "v6_ranges", len(desired.V6),
		"next_refresh", m.nextRefresh.Format(time.RFC3339))
	return nil
}

// collect builds the two desired range lists from every source. A source that
// fails contributes nothing and the refresh still writes the rest, which is
// what keeps an unreachable feed or resolver from being an outage of the pin
// itself. Every source failure is logged where it happens.
func (m *Module) collect(ctx context.Context, log *slog.Logger) desiredSets {
	prefixesV4 := make([]netip.Prefix, 0, len(m.seedsV4)+len(m.cfg.FQDNsV4))
	prefixesV4 = append(prefixesV4, m.seedsV4...)
	prefixesV4 = append(prefixesV4, m.resolveAll(ctx, log, familyV4, m.cfg.FQDNsV4)...)

	prefixesV6 := make([]netip.Prefix, 0, len(m.seedsV6)+len(m.cfg.FQDNsV6))
	prefixesV6 = append(prefixesV6, m.seedsV6...)
	prefixesV6 = append(prefixesV6, m.resolveAll(ctx, log, familyV6, m.cfg.FQDNsV6)...)

	feedV4, feedV6 := m.fetchFeed(ctx, log)
	prefixesV4 = append(prefixesV4, feedV4...)
	prefixesV6 = append(prefixesV6, feedV6...)

	return desiredSets{
		V4: mergePrefixes(prefixesV4),
		V6: mergePrefixes(prefixesV6),
	}
}

// New is the Constructor registered with ifmgr.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Enabled:         false,
		RefreshInterval: 0,
		RefreshTimeout:  0,
		FeedURL:         "",
		SeedCIDRsV4:     nil,
		SeedCIDRsV6:     nil,
		FQDNsV4:         nil,
		FQDNsV6:         nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("pinned: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:  ifmgr.NewBaseModule(moduleName),
		cfg:         c,
		seedsV4:     nil,
		seedsV6:     nil,
		resolver:    nil,
		httpClient:  nil,
		apply:       nil,
		clock:       nil,
		nextRefresh: time.Time{},
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
