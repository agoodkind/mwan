// Package npt programs the ip6 nat prerouting and postrouting chains for
// stateless IPv6 NPT, deriving each WAN's /60 from the live DHCPv6-PD
// delegation and translating the internal /60 onto it. It runs as a second
// module in the wan role and never tears its rules down on stop so the kernel
// keeps forwarding across a binary swap.
package npt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/pd"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	moduleName         = "npt"
	alertKindPDMissing = "npt_pd_missing"
	pdMaskBits         = 60
)

// reconcileAddrsFunc and listAddrsFunc are the netif address seams the module
// depends on. Injecting them lets module tests run without netlink.
type reconcileAddrsFunc func(context.Context, *slog.Logger, string, []netif.AddrSpec) error

type listAddrsFunc func(context.Context, *slog.Logger, string) ([]netif.CurrentAddr, error)

// WAN is one provider as npt sees it: the shared identity plus the translation
// prefix the configuration assigns it.
type WAN struct {
	ifmgr.WANRef
	// NptPrefix is the provider's configured external translation prefix,
	// exactly as the network configuration's npt-prefix leaf holds it, and
	// empty when the configuration names none. npt still derives the prefix it
	// programs from the live delegation; this value records only that a
	// delegation is expected at all.
	NptPrefix string
}

// expectsDelegation reports whether the configuration assigns this provider a
// translation prefix, and so whether an absent live delegation is a fault. A
// provider carrying none is an IPv4-only link, or one whose ISP delegates
// nothing by DHCPv6-PD, and neither is a fault npt can alert on.
func (w WAN) expectsDelegation() bool { return w.NptPrefix != "" }

// Config is the runtime config for the npt module. The WAN list, internal
// prefix, and edge addresses come from the shared [ifmgr.wan] section.
type Config struct {
	InternalPrefix string
	OpnsenseEdgeV6 string
	MwanbrEdgeV6   string
	WANs           []WAN
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return moduleName }

// Module owns the ip6 nat NPT rules for the configured WANs.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	// Parsed once at Init from cfg.
	internal     netip.Prefix
	opnsenseEdge netip.Addr
	mwanbrEdge   netip.Addr

	// Injectable seams (real implementations wired at Init when nil).
	src            pd.Source
	reconcileAddrs reconcileAddrsFunc
	listAddrs      listAddrsFunc
	apply          applier

	// pdMissing records which WAN ifaces had no delegated prefix on the last
	// reconcile, read by EvaluateAlerts. Guarded by the embedded mutex.
	pdMissing map[string]bool
}

// Init implements ifmgr.Module. Self-disables when the shared WAN list is empty
// so the wan role can list npt unconditionally.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	log.InfoContext(ctx, "npt: Init", "wan_count", len(m.cfg.WANs))

	if len(m.cfg.WANs) == 0 {
		log.WarnContext(ctx, "npt: no WAN config; disabling module")
		return fmt.Errorf("%w: npt: no [ifmgr.wan] WANs", ifmgr.ErrModuleDisabled)
	}
	if err := m.parse(); err != nil {
		log.WarnContext(ctx, "npt: config parse failed", "err", err)
		return err
	}

	if m.src == nil {
		m.src = pd.New(env.Log)
	}
	if m.apply == nil {
		m.apply = newNFTApplier()
	}
	if m.reconcileAddrs == nil {
		m.reconcileAddrs = netif.ReconcileAddrs
	}
	if m.listAddrs == nil {
		m.listAddrs = netif.ListAddrs
	}

	ifmgr.StartIfaceMonitors(ctx, log, moduleName, watchedIfaces(m.cfg), m.onMonitorEvent)

	// Watch the nftables ruleset so a reload that deletes the ip6 nat table
	// (nft -f or an nftables restart) triggers an immediate reconcile that
	// re-adds npt's rules, rather than waiting for the periodic tick. Those
	// reloads recreate the base table and chains from the static ruleset; a
	// bare `nft flush ruleset` with no reload is not repairable here because
	// Apply does not recreate the base structure. The recover keeps a monitor
	// panic from taking down the daemon.
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "npt: nft-watch panicked",
					"err", fmt.Sprint(recovered))
			}
		}()
		m.watchNFTChanges(ctx, log)
	}()
	return nil
}

// parse validates and stores the shared prefixes and edge addresses.
func (m *Module) parse() error {
	if err := validateWANs(m.cfg.WANs); err != nil {
		return err
	}
	internal, err := netip.ParsePrefix(m.cfg.InternalPrefix)
	if err != nil {
		slog.Warn("npt: invalid internal_prefix", "value", m.cfg.InternalPrefix, "err", err)
		return fmt.Errorf("npt: internal_prefix %q: %w", m.cfg.InternalPrefix, err)
	}
	edge, err := netip.ParseAddr(m.cfg.OpnsenseEdgeV6)
	if err != nil {
		slog.Warn("npt: invalid opnsense_edge_v6", "value", m.cfg.OpnsenseEdgeV6, "err", err)
		return fmt.Errorf("npt: opnsense_edge_v6 %q: %w", m.cfg.OpnsenseEdgeV6, err)
	}
	mwanbr, err := netip.ParseAddr(m.cfg.MwanbrEdgeV6)
	if err != nil {
		slog.Warn("npt: invalid mwanbr_edge_v6", "value", m.cfg.MwanbrEdgeV6, "err", err)
		return fmt.Errorf("npt: mwanbr_edge_v6 %q: %w", m.cfg.MwanbrEdgeV6, err)
	}
	m.internal = internal.Masked()
	m.opnsenseEdge = edge
	m.mwanbrEdge = mwanbr
	return nil
}

func validateWANs(wans []WAN) error {
	seen := make(map[string]bool, len(wans))
	for i, wan := range wans {
		if wan.Name == "" {
			return fmt.Errorf("npt: wan[%d]: name is required", i)
		}
		if wan.Iface == "" {
			return fmt.Errorf("npt: wan[%d] (%s): iface is required", i, wan.Name)
		}
		if seen[wan.Iface] {
			return fmt.Errorf("npt: wan[%d]: duplicate iface %q", i, wan.Iface)
		}
		seen[wan.Iface] = true
	}
	return nil
}

// Reconcile implements ifmgr.Module. It computes the union of every WAN's NPT
// rules from the live PD, then applies them in one atomic transaction.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	log = log.With("op", "reconcile")

	var desired desiredRules
	missing := make(map[string]bool, len(m.cfg.WANs))
	delegated := make(map[string]netip.Prefix, len(m.cfg.WANs))
	var reconcileErr error

	for _, wan := range m.cfg.WANs {
		built, present, err := m.buildWANDesired(ctx, log, wan)
		if err != nil {
			// A hard address-op error follows the same skip-and-alert contract
			// as a PD miss: exclude the WAN from the union and mark it missing so
			// EvaluateAlerts fires rather than falsely resolving it.
			reconcileErr = errors.Join(reconcileErr, err)
			missing[wan.Iface] = true
			continue
		}
		if !present {
			missing[wan.Iface] = true
			continue
		}
		// The <pd>::1/128 address add is the only address write in a reconcile. A
		// failure to ensure the address skips and alerts the WAN like any other
		// address op.
		if err := m.reconcileAddrs(ctx, log, wan.Iface, built.ensure); err != nil {
			reconcileErr = errors.Join(reconcileErr,
				fmt.Errorf("ensure %s on %s: %w", built.ensure[0].CIDR, wan.Iface, err))
			missing[wan.Iface] = true
			continue
		}
		delegated[wan.Name] = built.pd60
		desired.add(built.rules)
	}
	m.pdMissing = missing

	applyErr := m.apply.Apply(ctx, log, desired)
	if applyErr != nil {
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("apply: %w", applyErr))
	}
	m.publishLiveState(ctx, log, delegated, desired, applyErr == nil)
	return reconcileErr
}

// publishLiveState writes this pass's translation outcome to the
// management surface's snapshot store, when this host serves one. Kernel
// presence is read back from the live ip6 nat table after the apply, so
// the served value reports what the kernel holds rather than what the
// apply intended; a failed apply or read-back reports absent.
func (m *Module) publishLiveState(
	ctx context.Context,
	log *slog.Logger,
	delegated map[string]netip.Prefix,
	desired desiredRules,
	applied bool,
) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	// The intended ruleset is this pass's desired rules rendered in the
	// same text form the inspector renders live rules, so the served
	// intent and any live listing read alike.
	m.Env.LiveState.SetIntendedRuleset(renderIntended(desired))
	rendered := emptyRenderedTable()
	if applied {
		table, err := RenderTable(ctx, log)
		if err != nil {
			log.WarnContext(ctx, "npt: kernel read-back for the surface failed",
				"err", err)
		} else {
			rendered = table
		}
	}
	members := make(map[string]wanstate.MemberTranslation, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		members[wan.Name] = wanstate.MemberTranslation{
			Delegated:     delegated[wan.Name],
			KernelPresent: applied && rendered.HasInterface(wan.Iface),
		}
	}
	m.Env.LiveState.SetTranslation(members)
}

// wanDesired is one WAN's computed reconcile plan: the typed rules to program
// and the <pd>::1/128 address to ensure on the iface. Building the plan is a
// pure read; the caller applies ensure.
type wanDesired struct {
	rules  []natRule
	ensure []netif.AddrSpec
	// pd60 is the resolved live delegation, retained for the management
	// surface.
	pd60 netip.Prefix
}

// buildWANDesired resolves one WAN's live /60 and returns its reconcile plan.
// It only reads (PD lookup plus the extra-/128 enumeration); the caller
// performs the address write. present is false (skip + alert, no static
// fallback) when the PD source has no prefix or errors; err is returned only
// for a hard address-op read failure.
func (m *Module) buildWANDesired(
	ctx context.Context, log *slog.Logger, wan WAN,
) (wanDesired, bool, error) {
	pfx, ok, err := m.src.Prefix(ctx, wan.Iface)
	if err != nil {
		log.WarnContext(ctx, "npt: pd lookup failed; skipping WAN",
			"wan", wan.Name, "iface", wan.Iface, "err", err)
		return wanDesired{rules: nil, ensure: nil, pd60: netip.Prefix{}}, false, nil
	}
	if !ok {
		// A provider the configuration assigns no translation prefix is never
		// expected to carry a delegation, so its absence is the normal steady
		// state rather than a fault, and it records at debug so the journal
		// does not carry a warning every reconcile.
		level := slog.LevelWarn
		if !wan.expectsDelegation() {
			level = slog.LevelDebug
		}
		log.Log(ctx, level, "npt: no delegated prefix; skipping WAN",
			"wan", wan.Name, "iface", wan.Iface)
		return wanDesired{rules: nil, ensure: nil, pd60: netip.Prefix{}}, false, nil
	}

	pd60 := netip.PrefixFrom(pfx.Addr(), pdMaskBits).Masked()
	pd1 := pdHostOne(pd60)

	ensure := []netif.AddrSpec{{CIDR: netip.PrefixFrom(pd1, 128).String(), Family: "inet6"}}

	extra, err := m.extraGlobal128s(ctx, log, wan.Iface, pd1)
	if err != nil {
		return wanDesired{rules: nil, ensure: nil, pd60: netip.Prefix{}}, false,
			fmt.Errorf("enumerate extra /128 on %s: %w", wan.Iface, err)
	}

	rules := buildWANRules(wanRuleInput{
		Iface:        wan.Iface,
		PD60:         pd60,
		Internal:     m.internal,
		OpnsenseEdge: m.opnsenseEdge,
		MwanbrEdge:   m.mwanbrEdge,
		ExtraDNAT:    extra,
	})
	return wanDesired{rules: rules, ensure: ensure, pd60: pd60}, true, nil
}

// extraGlobal128s returns the global-scope /128 addresses on iface, excluding
// <pd>::1, each of which gets a reverse DNAT to the OPNsense edge. Mirrors the
// shell's `ip -6 addr show scope global` /128 scan.
func (m *Module) extraGlobal128s(
	ctx context.Context, log *slog.Logger, iface string, pd1 netip.Addr,
) ([]netip.Addr, error) {
	addrs, err := m.listAddrs(ctx, log, iface)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, current := range addrs {
		if current.Family != "inet6" {
			continue
		}
		prefix, err := netip.ParsePrefix(current.CIDR)
		if err != nil {
			continue
		}
		if prefix.Bits() != 128 {
			continue
		}
		addr := prefix.Addr()
		if !addr.IsGlobalUnicast() {
			continue
		}
		if addr == pd1 {
			continue
		}
		out = append(out, addr)
	}
	return out, nil
}

// EvaluateAlerts fires a per-iface WARN for each WAN that the configuration
// assigns a translation prefix and that had no delegated prefix on the last
// reconcile, and resolves it once the prefix returns. A WAN the configuration
// assigns no prefix never fires, because it is never expected to carry a
// delegation, but any alert already active for it still clears, so a
// provider that loses its npt-prefix mid-run does not carry a stale alert
// forever.
func (m *Module) EvaluateAlerts(ctx context.Context, _ *slog.Logger, now time.Time) {
	if m.Env == nil || m.Env.Alerts == nil {
		return
	}
	m.Lock()
	missing := make(map[string]bool, len(m.pdMissing))
	maps.Copy(missing, m.pdMissing)
	m.Unlock()

	for _, wan := range m.cfg.WANs {
		fields := []slog.Attr{slog.String("wan", wan.Name), slog.String("iface", wan.Iface)}
		if !wan.expectsDelegation() {
			// The configuration names no translation prefix for this provider,
			// so a missing delegation is its steady state rather than a fault,
			// and alerting on it would leave a warning that can never clear.
			// Still clear an alert already active for it: a WAN that lost its
			// npt-prefix mid-run, after an alert fired under the prior
			// configuration, must not carry that alert forever. The Active
			// guard keeps a provider that never alarmed from emitting a
			// spurious recovery.
			if m.Env.Alerts.Active(alertKindPDMissing, wan.Iface) {
				m.Env.Alerts.ResolveContext(ctx, now, alertKindPDMissing, wan.Iface,
					"npt: delegation no longer expected", fields...)
			}
			continue
		}
		if missing[wan.Iface] {
			m.Env.Alerts.NotifyContext(ctx, now, slog.LevelWarn, alertKindPDMissing, wan.Iface,
				"npt: no delegated prefix for WAN", fields...)
			continue
		}
		m.Env.Alerts.ResolveContext(ctx, now, alertKindPDMissing, wan.Iface,
			"npt: delegated prefix restored", fields...)
	}
}

func (m *Module) onMonitorEvent(ctx context.Context, log *slog.Logger, event netif.Event) {
	if !isAddrEvent(event) {
		return
	}
	eventLog := log.With("kind", event.Kind.String(), "iface", event.Iface, "cidr", event.CIDR)
	eventLog.DebugContext(ctx, "npt: addr event, reconciling")
	if err := m.Reconcile(ctx, eventLog); err != nil {
		eventLog.WarnContext(ctx, "npt: reconcile after addr event failed", "err", err)
	}
}

// isAddrEvent selects address add/delete events: a PD renumber surfaces as an
// address change on the WAN iface, which is what should trigger a reconcile.
func isAddrEvent(event netif.Event) bool {
	return event.Kind == netif.EvAddrAdded || event.Kind == netif.EvAddrDeleted
}

func watchedIfaces(cfg Config) []string {
	seen := make(map[string]bool, len(cfg.WANs))
	ifaces := make([]string, 0, len(cfg.WANs))
	for _, wan := range cfg.WANs {
		if wan.Iface == "" || seen[wan.Iface] {
			continue
		}
		seen[wan.Iface] = true
		ifaces = append(ifaces, wan.Iface)
	}
	return ifaces
}

// New is the Constructor registered with ifmgr.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		InternalPrefix: "",
		OpnsenseEdgeV6: "",
		MwanbrEdgeV6:   "",
		WANs:           nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("npt: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule:     ifmgr.NewBaseModule(moduleName),
		cfg:            c,
		internal:       netip.Prefix{},
		opnsenseEdge:   netip.Addr{},
		mwanbrEdge:     netip.Addr{},
		src:            nil,
		reconcileAddrs: nil,
		listAddrs:      nil,
		apply:          nil,
		pdMissing:      nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
