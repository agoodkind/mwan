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
	"slices"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/clock"
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

type listAddrsFunc func(context.Context, *slog.Logger, string) ([]netif.CurrentAddr, error)

// WAN is one provider and its configured IPv6 translation policy.
type WAN struct {
	ifmgr.WANRef
	Owned         bool
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
	src             pd.Source
	listAddrs       listAddrsFunc
	apply           applier
	removeDNAT      func(context.Context, *slog.Logger, string, []netip.Addr) error
	preparePolicies func(context.Context, *slog.Logger, map[string]bpf.PrefixPair) ([]bpf.InterfacePolicy, error)
	translator      interface {
		Reconcile([]bpf.InterfacePolicy) ([]bpf.AttachmentState, error)
	}

	// pdMissing records which WAN ifaces had no delegated prefix on the last
	// reconcile, read by EvaluateAlerts. Guarded by the embedded mutex.
	pdMissing     map[string]bool
	previousLocal map[string]map[netip.Addr]bool
	activePairs   map[string]bpf.PrefixPair
	activeRules   map[string][]natRule
}

// Init enables cleanup when the final configured WAN leaves journaled edges.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	log.InfoContext(ctx, "npt: Init", "wan_count", len(m.cfg.WANs))

	if len(m.cfg.WANs) == 0 && (env.NPTAddresses == nil || len(env.NPTAddresses.Recorded()) == 0) {
		log.WarnContext(ctx, "npt: no WAN config; disabling module")
		return fmt.Errorf("%w: npt: no [ifmgr.wan] WANs", ifmgr.ErrModuleDisabled)
	}
	if err := m.parse(); err != nil {
		log.WarnContext(ctx, "npt: config parse failed", "err", err)
		return err
	}

	if m.src == nil {
		m.src = pd.NewAssignmentSource(env.Connections, env.Delegations, pd.New(env.Log), clock.Real{})
	}
	if m.apply == nil {
		m.apply = newNFTApplier()
	}
	if m.removeDNAT == nil {
		m.removeDNAT = removeReverseDNAT
	}
	if m.translator == nil {
		translator, err := bpf.New()
		if err != nil {
			log.ErrorContext(ctx, "npt: load prefix translator failed", "err", err)
			return fmt.Errorf("load NPTv6 translator: %w", err)
		}
		m.translator = translator
	}
	if m.listAddrs == nil {
		m.listAddrs = netif.ListAddrs
	}
	if m.preparePolicies == nil {
		m.preparePolicies = func(ctx context.Context, log *slog.Logger, pairs map[string]bpf.PrefixPair) ([]bpf.InterfacePolicy, error) {
			policies, _, _, err := m.interfacePolicies(ctx, log, pairs)
			return policies, err
		}
	}
	env.PrepareLocalIPv6 = m.prepareLocalIPv6

	ifmgr.StartIfaceMonitors(ctx, log, moduleName, watchedIfaces(m.cfg), env.Connections, m.onMonitorEvent)

	// The watcher requests reconciliation after table or chain deletion.
	// The applier recreates missing structures and rules.
	// The recover keeps a monitor panic from taking down the daemon.
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
		if wan.Key() == "" {
			return fmt.Errorf("npt: wan[%d]: connection ID is required", i)
		}
		if wan.Iface == "" {
			return fmt.Errorf("npt: wan[%d] (%s): iface is required", i, wan.Key())
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
	if err := m.checkUnappliedWANs(); err != nil {
		return err
	}
	var desired desiredRules
	missing := make(map[string]bool, len(m.cfg.WANs))
	delegated := make(map[string]netip.Prefix, len(m.cfg.WANs))
	pairs := make(map[string]bpf.PrefixPair, len(m.cfg.WANs))
	rules := make(map[string][]natRule, len(m.cfg.WANs))
	var reconcileErr error
	desiredEdges := make(map[ifmgr.NPTEdgeRecord]bool)

	for _, wan := range m.cfg.WANs {
		rules[wan.Key()] = nil
		if m.addressesNotApplied(wan) {
			m.markDesiredEdges(desiredEdges, wan, "")
			previous := m.activeRules[wan.Key()]
			pair, hasPair := m.activePairs[wan.Key()]
			rules[wan.Key()] = previous
			desired.add(previous)
			if hasPair {
				pairs[wan.Key()] = pair
				delegated[wan.Key()] = pair.External
			}
			reconcileErr = errors.Join(reconcileErr,
				fmt.Errorf("npt: %s IPv6 addresses are not ready; retaining current translation", wan.Key()))
			continue
		}
		built, present, err := m.buildWANDesired(ctx, log, wan, true)
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
				m.markRecoveringEdges(desiredEdges, wan)
				missing[wan.Iface] = true
			}
			continue
		}
		ensureErr := m.ensureExternalAddress(ctx, log, wan, built.ensure[0])
		m.markDesiredEdges(desiredEdges, wan, built.ensure[0].CIDR)
		if ensureErr != nil {
			reconcileErr = errors.Join(reconcileErr,
				fmt.Errorf("ensure %s on %s: %w", built.ensure[0].CIDR, wan.Iface, ensureErr))
			missing[wan.Iface] = true
			continue
		}
		delegated[wan.Key()] = built.externalPrefix
		pairs[wan.Key()] = built.pair
		rules[wan.Key()] = built.rules
		desired.add(built.rules)
	}
	m.pdMissing = missing

	applyErr := m.apply.Apply(ctx, log, desired)
	if applyErr != nil {
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("apply: %w", applyErr))
	} else {
		m.activeRules = rules
	}
	bpfReady, nativeReady, desiredPolicies, policyErr := m.reconcileTranslator(ctx, log, pairs)
	reconcileErr = errors.Join(reconcileErr, policyErr)
	if err := m.releaseObsoleteEdges(ctx, log, desiredEdges, bpfReady, desiredPolicies); err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	}
	m.publishLiveState(ctx, log, delegated, desired, applyErr == nil, bpfReady, nativeReady)
	return reconcileErr
}

func (m *Module) reconcileTranslator(ctx context.Context, log *slog.Logger, pairs map[string]bpf.PrefixPair) (map[string]bool, bool, []bpf.InterfacePolicy, error) {
	ready := make(map[string]bool, len(pairs))
	if m.translator == nil {
		return ready, false, nil, nil
	}
	policies, ifIndexes, internalIndex, policyErr := m.interfacePolicies(ctx, log, pairs)
	states, err := m.translator.Reconcile(policies)
	if err != nil {
		log.WarnContext(ctx, "NPTv6 program reconciliation failed", "err", err)
		policyErr = errors.Join(policyErr, fmt.Errorf("reconcile NPTv6 programs: %w", err))
	} else if policyErr == nil {
		m.activePairs = maps.Clone(pairs)
	}
	internalReady := attachmentsReady(states, internalIndex)
	for name, index := range ifIndexes {
		ready[name] = internalReady && attachmentsReady(states, index)
	}
	return ready, err == nil, policies, policyErr
}

func (m *Module) checkUnappliedWANs() error {
	for _, wan := range m.cfg.WANs {
		if !m.addressesNotApplied(wan) {
			continue
		}
		previous, known := m.activeRules[wan.Key()]
		_, hasPair := m.activePairs[wan.Key()]
		if !known || (len(previous) != 0 && !hasPair) {
			return fmt.Errorf("npt: %s IPv6 addresses are not ready; retaining current translation", wan.Key())
		}
	}
	return nil
}

func (m *Module) addressesNotApplied(wan WAN) bool {
	return wan.Owned && wan.Translation != nil && wan.Translation.Mode == config.TranslationNPTv6 &&
		(m.Env == nil || m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyApplied(wan.Key(), "ipv6"))
}

func (m *Module) prepareLocalIPv6(ctx context.Context, log *slog.Logger, iface string, local []netip.Addr) error {
	m.Lock()
	defer m.Unlock()

	if len(local) == 0 || !m.ownsTranslatedIface(iface) {
		return nil
	}
	pairs, err := m.preparationPairs(ctx, log, iface, local)
	if err != nil {
		return err
	}
	policies, err := m.preparePolicies(ctx, log, pairs)
	if err != nil {
		return err
	}
	if _, err := m.translator.Reconcile(policies); err != nil {
		log.ErrorContext(ctx, "npt: prepare NPTv6 exceptions failed", "iface", iface, "err", err)
		return fmt.Errorf("prepare NPTv6 exceptions: %w", err)
	}
	m.activePairs = maps.Clone(pairs)
	m.rememberLocal(iface, local)
	if err := m.removeDNAT(ctx, log, iface, local); err != nil {
		for _, wan := range m.cfg.WANs {
			if wan.Iface == iface {
				delete(m.activeRules, wan.Key())
			}
		}
		log.ErrorContext(ctx, "npt: prepare reverse DNAT failed", "iface", iface, "err", err)
		return fmt.Errorf("prepare reverse DNAT for %s: %w", iface, err)
	}
	for _, wan := range m.cfg.WANs {
		if wan.Iface != iface {
			continue
		}
		previous, known := m.activeRules[wan.Key()]
		if !known {
			break
		}
		retained := make([]natRule, 0, len(previous))
		for _, rule := range previous {
			if rule.Op != opDNAT || !slices.Contains(local, rule.Match.Addr()) {
				retained = append(retained, rule)
			}
		}
		m.activeRules[wan.Key()] = retained
		break
	}
	return nil
}

func (m *Module) rememberLocal(iface string, addresses []netip.Addr) {
	if m.previousLocal == nil {
		m.previousLocal = make(map[string]map[netip.Addr]bool)
	}
	if m.previousLocal[iface] == nil {
		m.previousLocal[iface] = make(map[netip.Addr]bool)
	}
	for _, address := range addresses {
		m.previousLocal[iface][address] = true
	}
}

func (m *Module) ownsTranslatedIface(iface string) bool {
	for _, wan := range m.cfg.WANs {
		if wan.Iface == iface && wan.Owned && wan.Translation != nil && wan.Translation.Mode == config.TranslationNPTv6 {
			return true
		}
	}
	return false
}

func (m *Module) preparationPairs(ctx context.Context, log *slog.Logger, iface string, local []netip.Addr) (map[string]bpf.PrefixPair, error) {
	pairs := make(map[string]bpf.PrefixPair, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		built, present, err := m.buildWANDesired(ctx, log, wan, false)
		if err != nil {
			return nil, err
		}
		if !present {
			if wan.Iface == iface {
				return nil, fmt.Errorf("npt: %s has no external prefix for local IPv6 preparation", wan.Key())
			}
			if previous, ok := m.activePairs[wan.Key()]; ok {
				pairs[wan.Key()] = previous
			}
			continue
		}
		pair := built.pair
		if wan.Iface == iface {
			pair, err = addPendingExceptions(pair, local)
			if err != nil {
				return nil, err
			}
			if err := bpf.ValidatePair(pair); err != nil {
				log.ErrorContext(ctx, "npt: local IPv6 exception validation failed", "wan", wan.Key(), "err", err)
				return nil, fmt.Errorf("npt: %s local IPv6 preparation: %w", wan.Key(), err)
			}
		}
		pairs[wan.Key()] = pair
	}
	return pairs, nil
}

func addPendingExceptions(pair bpf.PrefixPair, local []netip.Addr) (bpf.PrefixPair, error) {
	for _, address := range local {
		if !address.Is6() || address.Is4In6() || !address.IsGlobalUnicast() {
			return pair, fmt.Errorf("npt: invalid local IPv6 address %s", address)
		}
		if address == externalHostOne(pair.External) {
			return pair, fmt.Errorf("npt: local IPv6 address %s conflicts with the NPT edge address", address)
		}
		if pair.External.Contains(address) && !slices.Contains(pair.DestinationExceptions, address) {
			pair.DestinationExceptions = append(pair.DestinationExceptions, address)
		}
	}
	return pair, nil
}

func (m *Module) ensureExternalAddress(ctx context.Context, log *slog.Logger, wan WAN, desired netif.AddrSpec) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.WarnContext(ctx, "NPT edge authorization failed", "connection", wan.ID, "err", resultErr)
		}
	}()
	if m.Env == nil || m.Env.NPTAddresses == nil {
		return fmt.Errorf("NPT edge address authority is unavailable")
	}
	prefix, err := netip.ParsePrefix(desired.CIDR)
	if err != nil {
		return fmt.Errorf("parse NPT edge: %w", err)
	}
	_, err = m.Env.NPTAddresses.Ensure(ctx, log, ifmgr.NPTEdgeRequest{ConnectionID: wan.ID, Interface: wan.Iface, Prefix: prefix})
	if err != nil {
		return fmt.Errorf("ensure authorized NPT edge: %w", err)
	}
	return nil
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
		pair, configured := pairs[wan.Key()]
		if !configured {
			continue
		}
		if previous, duplicate := identities[pair.ID]; duplicate {
			failures = errors.Join(failures, fmt.Errorf("npt: %s and %s have colliding prefix-pair IDs", previous, wan.Key()))
			continue
		}
		identities[pair.ID] = wan.Key()
		if err := internalPrefixRouteReady(pair.Internal, internal.Attrs().Index); err != nil {
			failures = errors.Join(failures, fmt.Errorf("npt: %s hairpin route: %w", wan.Key(), err))
			continue
		}
		link, err := netlink.LinkByName(wan.Iface)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("npt: WAN interface %s: %w", wan.Iface, err))
			continue
		}
		indices[wan.Key()] = link.Attrs().Index
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

// Native readiness requires verified nft absence even after an apply fails.
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
	rendered, inspectErr := RenderTable(ctx, log)
	if inspectErr != nil {
		log.WarnContext(ctx, "npt: kernel read-back for the surface failed",
			"err", inspectErr)
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
			if wan.Owned && (m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyReady(wan.Key(), "ipv4")) {
				member.V4.Ready = false
				member.V4.Reason = "MWAN-owned IPv4 addresses are not verified"
			}
		}
		member.V6 = ipv6TranslationReadiness(
			wan, delegated[wan.Key()], applied && rendered.HasInterface(wan.Iface),
			bpfReady[wan.Key()], nativeReady && inspectErr == nil && rendered.interfaceAbsent(wan.Iface),
		)
		members[wan.Key()] = member
	}
	if m.Env.LiveState.SetTranslation(members) && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("NPT translation readiness changed")
	}
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
// The caller performs the address write. present is false (skip + alert, no static
// fallback) when the PD source has no prefix or errors; err is returned only
// for a hard address-op read failure.
func (m *Module) buildWANDesired(
	ctx context.Context, log *slog.Logger, wan WAN, finalized bool,
) (wanDesired, bool, error) {
	var empty wanDesired
	if wan.Translation == nil || wan.Translation.Mode == config.TranslationNative {
		return empty, false, nil
	}
	policy := wan.Translation.NPT
	if policy == nil {
		return empty, false, fmt.Errorf("npt: %s has no NPTv6 policy", wan.Key())
	}
	externalPrefix := policy.ExternalPrefix.Masked()
	if policy.ExternalSource != config.PrefixConfigured {
		pfx, ok, err := m.src.Prefix(ctx, wan.Iface)
		if err != nil {
			log.WarnContext(ctx, "npt: pd lookup failed; skipping WAN",
				"wan", wan.Key(), "iface", wan.Iface, "err", err)
			return empty, false, nil
		}
		if !ok {
			log.WarnContext(ctx, "npt: no delegated prefix; skipping WAN",
				"wan", wan.Key(), "iface", wan.Iface)
			return empty, false, nil
		}
		bits := policy.InternalPrefix.Bits()
		if policy.ExpectedPrefix.IsValid() {
			bits = policy.ExpectedPrefix.Bits()
		}
		if bits < pfx.Bits() {
			return empty, false, fmt.Errorf("npt: %s delegated %s is longer than required /%d", wan.Key(), pfx, bits)
		}
		externalPrefix = netip.PrefixFrom(pfx.Addr(), bits).Masked()
	}
	pd1 := externalHostOne(externalPrefix)

	ensure := []netif.AddrSpec{{CIDR: netip.PrefixFrom(pd1, 128).String(), Family: "inet6"}}

	extra, local, err := m.extraGlobal128s(ctx, log, wan, pd1, finalized)
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
	destinationExceptions := append([]netip.Addr{pd1}, extra...)
	for _, addr := range local {
		if externalPrefix.Contains(addr) {
			destinationExceptions = append(destinationExceptions, addr)
		}
	}
	pair := bpf.PrefixPair{
		ID:                    pairID(wan.Key()),
		Internal:              policy.InternalPrefix.Masked(),
		External:              externalPrefix,
		SourceExceptions:      []netip.Addr{m.opnsenseEdge, m.mwanbrEdge},
		DestinationExceptions: destinationExceptions,
	}
	if err := bpf.ValidatePair(pair); err != nil {
		return empty, false, fmt.Errorf("npt: %s: %w", wan.Key(), err)
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

// extraGlobal128s separates current local IA_NA addresses from addresses that
// require reverse DNAT. Legacy WANs retain the global /128 classification.
func (m *Module) extraGlobal128s(
	ctx context.Context, log *slog.Logger, wan WAN, pd1 netip.Addr, finalized bool,
) ([]netip.Addr, []netip.Addr, error) {
	addrs, err := m.listAddrs(ctx, log, wan.Iface)
	if err != nil {
		return nil, nil, err
	}
	localAssignments := m.localAssignmentSet(wan)
	forward := make([]netip.Addr, 0, len(addrs))
	local := make([]netip.Addr, 0, len(localAssignments))
	for _, current := range addrs {
		addr, eligible := global128Address(current, pd1)
		if !eligible {
			continue
		}
		if m.journaledEdge(wan.Iface, addr) {
			continue
		}
		if localAssignments[addr] {
			local = append(local, addr)
			continue
		}
		forward = append(forward, addr)
	}
	if finalized && wan.Owned {
		if m.previousLocal == nil {
			m.previousLocal = make(map[string]map[netip.Addr]bool)
		}
		m.previousLocal[wan.Iface] = make(map[netip.Addr]bool, len(local))
		for _, address := range local {
			m.previousLocal[wan.Iface][address] = true
		}
	}
	return forward, local, nil
}

func (m *Module) localAssignmentSet(wan WAN) map[netip.Addr]bool {
	local := make(map[netip.Addr]bool)
	for address := range m.previousLocal[wan.Iface] {
		local[address] = true
	}
	if !wan.Owned || m.Env == nil || m.Env.Delegations == nil {
		return local
	}
	lease, ok := m.Env.Delegations.Get(wan.Iface)
	if !ok {
		return local
	}
	now := (clock.Real{}).Now()
	for _, address := range lease.Addresses {
		if now.Before(address.ValidUntil) {
			local[address.Address] = true
		}
	}
	return local
}

func global128Address(current netif.CurrentAddr, pd1 netip.Addr) (netip.Addr, bool) {
	if current.Family != "inet6" {
		return netip.Addr{}, false
	}
	prefix, err := netip.ParsePrefix(current.CIDR)
	if err != nil || prefix.Bits() != 128 || !prefix.Addr().IsGlobalUnicast() || prefix.Addr() == pd1 {
		return netip.Addr{}, false
	}
	return prefix.Addr(), true
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
		fields := []slog.Attr{slog.String("wan", wan.Key()), slog.String("iface", wan.Iface)}
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
		BaseModule:      ifmgr.NewBaseModule(moduleName),
		cfg:             c,
		internal:        netip.Prefix{},
		opnsenseEdge:    netip.Addr{},
		mwanbrEdge:      netip.Addr{},
		src:             nil,
		listAddrs:       nil,
		apply:           nil,
		removeDNAT:      nil,
		preparePolicies: nil,
		translator:      nil,
		pdMissing:       nil,
		previousLocal:   nil,
		activePairs:     nil,
		activeRules:     nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
