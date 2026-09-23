// Package npt configures RFC 6296 prefix translation and the IPv6 edge address
// exceptions. Each WAN resolves its configured or delegated external prefix.
package npt

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/pd"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	moduleName         = "npt"
	alertKindPDMissing = "npt_pd_missing"
)

// reconcileAddrsFunc and listAddrsFunc are the netif address seams the module
// depends on. Injecting them lets module tests run without netlink.
type reconcileAddrsFunc func(context.Context, *slog.Logger, string, []netif.AddrSpec) error

type listAddrsFunc func(context.Context, *slog.Logger, string) ([]netif.CurrentAddr, error)

// WAN is one provider and its configured IPv6 translation policy.
type WAN struct {
	ifmgr.WANRef
	TranslationV4 *config.IPv4Translation
	Translation   *config.IPv6Translation
}

func (w WAN) expectsDelegation() bool {
	return w.Translation != nil && w.Translation.Mode == config.TranslationNPTv6 &&
		w.Translation.NPT != nil && w.Translation.NPT.ExternalSource == config.PrefixDelegated
}

// Config is the runtime config for the npt module. The WAN list, internal
// prefix, and edge addresses come from the shared [ifmgr.wan] section.
type Config struct {
	InternalIface  string
	InternalNetV4  string
	InternalPrefix string
	OpnsenseEdgeV6 string
	MwanbrEdgeV6   string
	WANs           []WAN
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return moduleName }

// Module configures prefix translation and edge address exceptions for WANs.
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
	translator     interface {
		Reconcile([]bpf.InterfacePolicy) ([]bpf.AttachmentState, error)
	}

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
	if m.translator == nil {
		translator, err := bpf.New()
		if err != nil {
			log.ErrorContext(ctx, "npt: load prefix translator failed", "err", err)
			return fmt.Errorf("load NPTv6 translator: %w", err)
		}
		m.translator = translator
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

// Reconcile implements ifmgr.Module. It resolves each WAN's external prefix,
// applies edge exceptions, and reconciles the prefix translator.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	log = log.With("op", "reconcile")

	var desired desiredRules
	missing := make(map[string]bool, len(m.cfg.WANs))
	delegated := make(map[string]netip.Prefix, len(m.cfg.WANs))
	pairs := make(map[string]bpf.PrefixPair, len(m.cfg.WANs))
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
			if wan.expectsDelegation() {
				missing[wan.Iface] = true
			}
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
		delegated[wan.Name] = built.externalPrefix
		pairs[wan.Name] = built.pair
		desired.add(built.rules)
	}
	m.pdMissing = missing

	applyErr := m.apply.Apply(ctx, log, desired)
	if applyErr != nil {
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("apply: %w", applyErr))
	}
	bpfReady := make(map[string]bool, len(pairs))
	nativeReady := false
	if m.translator != nil {
		policies, ifIndexes, internalIndex, err := m.interfacePolicies(ctx, log, pairs)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
		states, err := m.translator.Reconcile(policies)
		nativeReady = err == nil
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reconcile NPTv6 programs: %w", err))
		}
		internalReady := attachmentsReady(states, internalIndex)
		for name, index := range ifIndexes {
			bpfReady[name] = internalReady && attachmentsReady(states, index)
		}
	}
	m.publishLiveState(ctx, log, delegated, desired, applyErr == nil, bpfReady, nativeReady)
	return reconcileErr
}

func attachmentsReady(states []bpf.AttachmentState, index int) bool {
	if index == 0 {
		return false
	}
	ingress := false
	egress := false
	for _, state := range states {
		if state.IfIndex != index || !state.Ready {
			continue
		}
		if state.Direction == bpf.Ingress {
			ingress = true
		}
		if state.Direction == bpf.Egress {
			egress = true
		}
	}
	return ingress && egress
}

func (m *Module) interfacePolicies(ctx context.Context, log *slog.Logger, pairs map[string]bpf.PrefixPair) ([]bpf.InterfacePolicy, map[string]int, int, error) {
	indices := make(map[string]int, len(pairs))
	if len(pairs) == 0 {
		return nil, indices, 0, nil
	}
	internal, err := netlink.LinkByName(m.cfg.InternalIface)
	if err != nil {
		log.ErrorContext(ctx, "npt: read internal interface", "iface", m.cfg.InternalIface, "err", err)
		return nil, indices, 0, fmt.Errorf("npt: internal interface %s: %w", m.cfg.InternalIface, err)
	}
	internalPolicy := bpf.InterfacePolicy{IfIndex: internal.Attrs().Index, Internal: true, Pairs: nil}
	policies := make([]bpf.InterfacePolicy, 0, len(pairs)+1)
	identities := make(map[uint16]string, len(pairs))
	var failures error
	for _, wan := range m.cfg.WANs {
		pair, configured := pairs[wan.Name]
		if !configured {
			continue
		}
		if previous, duplicate := identities[pair.ID]; duplicate {
			failures = errors.Join(failures, fmt.Errorf("npt: %s and %s have colliding prefix-pair IDs", previous, wan.Name))
			continue
		}
		identities[pair.ID] = wan.Name
		if err := internalPrefixRouteReady(pair.Internal, internal.Attrs().Index); err != nil {
			failures = errors.Join(failures, fmt.Errorf("npt: %s hairpin route: %w", wan.Name, err))
			continue
		}
		link, err := netlink.LinkByName(wan.Iface)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("npt: WAN interface %s: %w", wan.Iface, err))
			continue
		}
		indices[wan.Name] = link.Attrs().Index
		policies = append(policies, bpf.InterfacePolicy{IfIndex: link.Attrs().Index, Internal: false, Pairs: []bpf.PrefixPair{pair}})
		internalPolicy.Pairs = append(internalPolicy.Pairs, pair)
	}
	if len(internalPolicy.Pairs) != 0 {
		policies = append(policies, internalPolicy)
	}
	return policies, indices, internal.Attrs().Index, failures
}

func internalPrefixRouteReady(prefix netip.Prefix, internalIndex int) error {
	address := prefix.Masked().Addr().As16()
	destination := &net.IPNet{IP: net.IP(address[:]), Mask: net.CIDRMask(prefix.Bits(), 128)}
	filter := &netlink.Route{Dst: destination, Table: unix.RT_TABLE_MAIN}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, filter, netlink.RT_FILTER_DST|netlink.RT_FILTER_TABLE)
	if err != nil {
		slog.Warn("npt: read internal IPv6 prefix route failed", "prefix", prefix, "interface_index", internalIndex, "err", err)
		return fmt.Errorf("list IPv6 routes for %s: %w", prefix, err)
	}
	for _, route := range routes {
		if route.LinkIndex == internalIndex {
			return nil
		}
	}
	return fmt.Errorf("main-table route for %s does not use internal interface index %d", prefix, internalIndex)
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
	bpfReady map[string]bool,
	nativeReady bool,
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
	internalV4, internalV4Err := netip.ParsePrefix(m.cfg.InternalNetV4)
	for _, wan := range m.cfg.WANs {
		var member wanstate.MemberTranslation
		if wan.TranslationV4 != nil {
			member.V4 = IPv4TranslationReadiness(ctx, wan, internalV4)
			if internalV4Err != nil && wan.TranslationV4.Mode == config.TranslationNAPT44 {
				member.V4.Ready = false
				member.V4.Reason = "internal IPv4 source network is unavailable"
			}
		}
		member.V6 = ipv6TranslationReadiness(
			wan, delegated[wan.Name], applied && rendered.HasInterface(wan.Iface),
			bpfReady[wan.Name], nativeReady,
		)
		members[wan.Name] = member
	}
	m.Env.LiveState.SetTranslation(members)
}

func ipv6TranslationReadiness(
	wan WAN,
	external netip.Prefix,
	kernelReady bool,
	bpfReady bool,
	nativeReady bool,
) wanstate.FamilyTranslation {
	var result wanstate.FamilyTranslation
	if wan.Translation == nil {
		return result
	}
	result.Mode = string(wan.Translation.Mode)
	if wan.Translation.Mode == config.TranslationNative {
		result.Ready = nativeReady
		if !nativeReady {
			result.Reason = "stale IPv6 translation cleanup is unverified"
		}
		return result
	}
	if wan.Translation.NPT == nil {
		return result
	}
	result.InternalPrefix = wan.Translation.NPT.InternalPrefix
	result.ExternalPrefix = external
	result.Ready = kernelReady && bpfReady
	if !result.Ready {
		result.Reason = "required IPv6 translation is unavailable"
	}
	return result
}

// wanDesired contains one WAN's edge rules, prefix pair, and address to ensure.
type wanDesired struct {
	rules  []natRule
	ensure []netif.AddrSpec
	pair   bpf.PrefixPair
	// externalPrefix is the resolved provider prefix, retained for the management
	// surface.
	externalPrefix netip.Prefix
}

// buildWANDesired resolves one WAN's external prefix and returns its reconcile plan.
// It only reads (PD lookup plus the extra-/128 enumeration); the caller
// performs the address write. present is false (skip + alert, no static
// fallback) when the PD source has no prefix or errors; err is returned only
// for a hard address-op read failure.
func (m *Module) buildWANDesired(
	ctx context.Context, log *slog.Logger, wan WAN,
) (wanDesired, bool, error) {
	var empty wanDesired
	if wan.Translation == nil || wan.Translation.Mode == config.TranslationNative {
		return empty, false, nil
	}
	policy := wan.Translation.NPT
	if policy == nil {
		return empty, false, fmt.Errorf("npt: %s has no NPTv6 policy", wan.Name)
	}
	externalPrefix := policy.ExternalPrefix.Masked()
	if policy.ExternalSource != config.PrefixConfigured {
		pfx, ok, err := m.src.Prefix(ctx, wan.Iface)
		if err != nil {
			log.WarnContext(ctx, "npt: pd lookup failed; skipping WAN",
				"wan", wan.Name, "iface", wan.Iface, "err", err)
			return empty, false, nil
		}
		if !ok {
			log.WarnContext(ctx, "npt: no delegated prefix; skipping WAN",
				"wan", wan.Name, "iface", wan.Iface)
			return empty, false, nil
		}
		bits := policy.InternalPrefix.Bits()
		if policy.ExpectedPrefix.IsValid() {
			bits = policy.ExpectedPrefix.Bits()
		}
		if bits < pfx.Bits() {
			return empty, false, fmt.Errorf("npt: %s delegated %s is longer than required /%d", wan.Name, pfx, bits)
		}
		externalPrefix = netip.PrefixFrom(pfx.Addr(), bits).Masked()
	}
	pd1 := externalHostOne(externalPrefix)

	ensure := []netif.AddrSpec{{CIDR: netip.PrefixFrom(pd1, 128).String(), Family: "inet6"}}

	extra, err := m.extraGlobal128s(ctx, log, wan.Iface, pd1)
	if err != nil {
		return empty, false,
			fmt.Errorf("enumerate extra /128 on %s: %w", wan.Iface, err)
	}

	rules := buildWANRules(wanRuleInput{
		Iface:        wan.Iface,
		External:     externalPrefix,
		OpnsenseEdge: m.opnsenseEdge,
		MwanbrEdge:   m.mwanbrEdge,
		ExtraDNAT:    extra,
	})
	pair := bpf.PrefixPair{
		ID:                    pairID(wan.Name),
		Internal:              policy.InternalPrefix.Masked(),
		External:              externalPrefix,
		SourceExceptions:      []netip.Addr{m.opnsenseEdge, m.mwanbrEdge},
		DestinationExceptions: append([]netip.Addr{pd1}, extra...),
	}
	if err := bpf.ValidatePair(pair); err != nil {
		return empty, false, fmt.Errorf("npt: %s: %w", wan.Name, err)
	}
	return wanDesired{rules: rules, ensure: ensure, pair: pair, externalPrefix: externalPrefix}, true, nil
}

func pairID(name string) uint16 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(name))
	id := uint16(hash.Sum32() & 0xffff)
	if id == 0 {
		return 1
	}
	return id
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
		InternalIface:  "",
		InternalNetV4:  "",
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
		translator:     nil,
		pdMissing:      nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
