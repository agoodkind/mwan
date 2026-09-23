// Package wanroutes ports the MWAN update-routes policy-routing inventory into
// an ifmgr module.
package wanroutes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	moduleName = "wan.routes"
	familyV4   = "inet"
	familyV6   = "inet6"

	// catchAllPriority is the rule pair that sends everything arriving on the
	// internal link to one provider's table. It sits above every mark rule, so
	// it is consulted only after those miss.
	catchAllPriority = 50

	// alertKindNextHopUnresolved is the alert kind for an internal next hop
	// with no usable neighbour entry.
	alertKindNextHopUnresolved = "wan_routes_next_hop_unresolved"
	// nextHopAlertThreshold is how many consecutive reconciles the next hop
	// must fail to resolve before the alert fires. Two ticks tolerate one
	// in-flight NDP resolution without a false alert.
	nextHopAlertThreshold = 2

	// hostPrefixBitsV4 is the prefix length of a single IPv4 address.
	hostPrefixBitsV4 = 32
)

// Config is the parsed [ifmgr.modules.wan.routes] runtime config.
type Config struct {
	InternalIface   string
	OpnsenseEdgeV6  string
	InternalNetV4   string
	HealthStateFile string
	WANs            []WAN
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return moduleName }

// WAN is one configured uplink and its owned policy-routing slots. The
// embedded ifmgr.WANRef carries the shared per-WAN identity (Name, Iface); the
// remaining fields are the wan.routes-specific per-WAN routing and steering
// data.
type WAN struct {
	ifmgr.WANRef
	TableID       int
	FwMark        uint32
	FwMarkPrio    int
	FromPrio      int
	TranslationV4 *config.IPv4Translation
	TranslationV6 *config.IPv6Translation
	// V4Source is the WAN's static IPv4 link address. When set, traffic the box
	// sources from that address is pinned to this WAN's table via a v4 source
	// rule at FromPrio, the IPv4 twin of the translated-prefix v6 source rule. Only
	// static-link WANs set it; a WAN on a dynamic link leaves it empty and gets
	// no v4 source rule.
	V4Source string
	// Tier is the preference tier this provider sits in, from configuration.
	// The lowest-numbered tier holding a healthy provider carries new
	// connections.
	Tier uint8
	// Weight is this provider's share of its tier. wan.routes does not spread
	// traffic itself, so it carries the value only to hand it to the management
	// surface, which publishes what the daemon loaded.
	Weight int
	// MappedExternals are the external addresses of this provider's static
	// mappings. One inside a subnet this link is connected to is held on the
	// link as a host address, because the upstream gateway resolves it on the
	// link; one outside every connected subnet is routed here and needs
	// nothing.
	MappedExternals []netip.Addr
}

type gatewaySet struct {
	V4 string
	V6 string
}

type gateways map[string]gatewaySet

type ruleSlot struct {
	family   string
	priority int
}

// Module owns the WAN policy-routing rules and routes.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	// resolveNextHop checks whether the internal next hop resolves in the
	// neighbour table. Injectable for tests; defaults to netif.NextHopResolves.
	resolveNextHop func(
		ctx context.Context, log *slog.Logger, dev string, addr string,
	) (bool, error)

	// nextHopMisses counts consecutive reconciles on which the internal next
	// hop did not resolve. Guarded by the embedded mutex; read by
	// EvaluateAlerts, which fires once the count reaches
	// nextHopAlertThreshold so one in-flight NDP resolution never alerts.
	nextHopMisses int

	// listAddrs and reconcileAddrs read and add provider link addresses.
	// Injectable for tests; Init fills them with the netif implementations.
	listAddrs      func(ctx context.Context, log *slog.Logger, iface string) ([]netif.CurrentAddr, error)
	reconcileAddrs func(ctx context.Context, log *slog.Logger, iface string, desired []netif.AddrSpec) error

	// ownedAddresses holds, per provider, the mapped addresses the last
	// reconcile held on the provider's link. Guarded by the embedded mutex and
	// replaced whole each pass, so a published snapshot never shares a slice a
	// later pass writes.
	ownedAddresses map[string][]netip.Addr
}

// gatewayDiscovery is one pass's gateway read. A provider whose link does not
// exist holds empty gateways, and missingLinks joins one error per family
// read that found no link.
type gatewayDiscovery struct {
	gateways     gateways
	missingLinks error
}

// Init implements ifmgr.Module.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	log.InfoContext(
		ctx, "wan.routes: Init",
		"wan_count", len(m.cfg.WANs),
		"health_state_file", m.cfg.HealthStateFile,
	)

	if len(m.cfg.WANs) == 0 {
		log.WarnContext(ctx, "wan.routes: missing WAN config; disabling module")
		return fmt.Errorf("%w: wan.routes: no [ifmgr.modules.wan.routes] section", ifmgr.ErrModuleDisabled)
	}
	if err := validateConfig(m.cfg); err != nil {
		log.WarnContext(ctx, "wan.routes: validateConfig failed", "err", err)
		return err
	}
	if m.resolveNextHop == nil {
		m.resolveNextHop = netif.NextHopResolves
	}
	if m.listAddrs == nil {
		m.listAddrs = netif.ListAddrs
	}
	if m.reconcileAddrs == nil {
		m.reconcileAddrs = netif.ReconcileAddrs
	}

	ifmgr.StartIfaceMonitors(ctx, log, moduleName, watchedIfaces(m.cfg), m.onMonitorEvent)
	return nil
}

// Reconcile implements ifmgr.Module.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	log = log.With("op", "reconcile")
	log.DebugContext(ctx, "wan.routes: Reconcile entry")

	// Ownership reads only each link's own addresses, so it runs before the
	// gateway and health reads that can end the pass early: a provider whose
	// default route is missing still answers for its on-link addresses.
	ownershipErr := m.ownMappedAddressesLocked(ctx, log)

	// A missing link does not end the pass, because the other providers must
	// keep their rules while one card is detached. Any other gateway read
	// error ends it, because a transient netlink failure must not strip a
	// healthy provider.
	discovery, err := m.discoverGateways(ctx, log)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: discoverGateways failed", "err", err)
		return errors.Join(ownershipErr, discovery.missingLinks, err)
	}
	currentGateways := discovery.gateways
	health, err := netif.ReadHealthState(m.cfg.HealthStateFile)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: ReadHealthState failed", "err", err)
		return errors.Join(
			ownershipErr,
			discovery.missingLinks,
			fmt.Errorf("read health state %q: %w", m.cfg.HealthStateFile, err),
		)
	}
	translations := m.translationState()
	_, routes := desiredState(currentGateways, health, m.cfg, translations)

	reconcileErr := errors.Join(ownershipErr, discovery.missingLinks)
	for _, route := range routes {
		if route.Dest == "default" {
			if err := reconcileTableDefault(ctx, log, route); err != nil {
				m.excludeRouteFamily(currentGateways, route.TableID, route.Family)
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf(
					"reconcile default route table=%d family=%s: %w",
					route.TableID,
					route.Family,
					err,
				))
			}
			continue
		}
		if err := netif.ReconcileTableRoute(ctx, log, route); err != nil {
			m.excludeRouteFamily(currentGateways, route.TableID, route.Family)
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf(
				"reconcile route table=%d family=%s dest=%s: %w",
				route.TableID,
				route.Family,
				route.Dest,
				err,
			))
		}
	}
	rules, _ := desiredState(currentGateways, health, m.cfg, translations)
	for _, rule := range rules {
		if rule.Priority == catchAllPriority {
			continue
		}
		if err := netif.ReconcileRules(ctx, log, []netif.DesiredRule{rule}); err != nil {
			m.excludeRouteFamily(currentGateways, rule.TableID, rule.Family)
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reconcile rule: %w", err))
		}
	}
	// Select fallback rules only after provider routes and rules have succeeded.
	rules, _ = desiredState(currentGateways, health, m.cfg, translations)
	for _, rule := range rules {
		if rule.Priority != catchAllPriority {
			continue
		}
		if err := netif.ReconcileRules(ctx, log, []netif.DesiredRule{rule}); err != nil {
			for _, wan := range m.cfg.WANs {
				m.excludeRouteFamily(currentGateways, wan.TableID, rule.Family)
			}
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reconcile fallback rule: %w", err))
		}
	}
	rules, _ = desiredState(currentGateways, health, m.cfg, translations)
	if err := removeDisabledRuleSlots(ctx, log, m.cfg, rules); err != nil {
		// A stale policy rule can override any provider selected by steering.
		clear(currentGateways)
		reconcileErr = errors.Join(reconcileErr, err)
	}
	m.checkNextHopLocked(ctx, log)
	m.publishLiveState(currentGateways, health, translations)
	return reconcileErr
}

func (m *Module) excludeRouteFamily(current gateways, tableID int, family string) {
	for _, wan := range m.cfg.WANs {
		if wan.TableID != tableID {
			continue
		}
		gateway := current[wan.Name]
		if family == familyV4 {
			gateway.V4 = ""
		} else {
			gateway.V6 = ""
		}
		current[wan.Name] = gateway
	}
}

func (m *Module) translationState() map[string]wanstate.MemberTranslation {
	if m.Env == nil || m.Env.LiveState == nil {
		return nil
	}
	return m.Env.LiveState.Snapshot().Translation
}

// publishLiveState publishes eligible families and marks a provider carrying when either family selects its tier.
func (m *Module) publishLiveState(currentGateways gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	v4 := familyMembers(m.cfg, currentGateways, health, translations, familyV4)
	v6 := familyMembers(m.cfg, currentGateways, health, translations, familyV6)
	tierV4, readyV4 := netif.ActiveTier(v4, health)
	tierV6, readyV6 := netif.ActiveTier(v6, health)
	activeTier := tierV4
	if !readyV4 || (readyV6 && tierV6 < activeTier) {
		activeTier = tierV6
	}
	members := make(map[string]wanstate.MemberRouting, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		v4Ready := familyReady(wan, currentGateways[wan.Name], health, translations[wan.Name], familyV4)
		v6Ready := familyReady(wan, currentGateways[wan.Name], health, translations[wan.Name], familyV6)
		members[wan.Name] = wanstate.MemberRouting{
			Carrying: (readyV4 && wan.Tier == tierV4 && v4Ready) || (readyV6 && wan.Tier == tierV6 && v6Ready),
			V4Ready:  v4Ready, V6Ready: v6Ready,
			OwnedAddresses: slices.Clone(m.ownedAddresses[wan.Name]),
		}
	}
	m.Env.LiveState.SetRouting(activeTier, members)
}

// ownMappedAddressesLocked holds each provider's on-link mapped addresses on its
// link as host addresses and records what each link holds for the served tree.
// A link that cannot be read or written records nothing for its provider, and
// the pass moves on to the next provider. Callers hold the module lock.
func (m *Module) ownMappedAddressesLocked(ctx context.Context, log *slog.Logger) error {
	owned := make(map[string][]netip.Addr, len(m.cfg.WANs))
	var ownershipErr error
	for _, wan := range m.cfg.WANs {
		if len(wan.MappedExternals) == 0 {
			continue
		}
		addresses, err := m.ownLinkAddresses(ctx, log, wan)
		if err != nil {
			ownershipErr = errors.Join(ownershipErr, err)
			continue
		}
		owned[wan.Name] = addresses
	}
	m.ownedAddresses = owned
	return ownershipErr
}

// ownLinkAddresses reads one provider link's addresses, decides which mapped
// addresses the link must hold, and adds each as a /32.
func (m *Module) ownLinkAddresses(ctx context.Context, log *slog.Logger, wan WAN) ([]netip.Addr, error) {
	held, err := m.listAddrs(ctx, log, wan.Iface)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: list link addresses failed",
			"wan", wan.Name, "iface", wan.Iface, "err", err)
		return nil, fmt.Errorf("list addresses on %s: %w", wan.Iface, err)
	}
	onLink, err := OnLinkMappedAddresses(held, wan.MappedExternals)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: link addresses unreadable",
			"wan", wan.Name, "iface", wan.Iface, "err", err)
		return nil, fmt.Errorf("read addresses on %s: %w", wan.Iface, err)
	}
	if len(onLink) == 0 {
		return nil, nil
	}
	desired := make([]netif.AddrSpec, 0, len(onLink))
	for _, address := range onLink {
		desired = append(desired, netif.AddrSpec{
			CIDR:   netip.PrefixFrom(address, hostPrefixBitsV4).String(),
			Family: familyV4,
		})
	}
	if err := m.reconcileAddrs(ctx, log, wan.Iface, desired); err != nil {
		log.WarnContext(ctx, "wan.routes: hold mapped addresses failed",
			"wan", wan.Name, "iface", wan.Iface, "err", err)
		return nil, fmt.Errorf("hold mapped addresses on %s: %w", wan.Iface, err)
	}
	return onLink, nil
}

// OnLinkMappedAddresses returns the mapped addresses a link must hold as host
// addresses: each one inside an IPv4 subnet the link is connected to, except an
// address the link already holds with a shorter prefix. That exception compares
// addresses rather than CIDR strings, because the netif write replaces an
// address it is asked for at a different prefix length, and asking for the
// link's own address at /32 would drop the link's connected route. A /32 the
// link holds is not a connected subnet, so a mapped address this function
// returned on an earlier pass is returned again. Held entries are the netif
// CIDR strings; one that does not parse is an error rather than a skip, because
// a skipped subnet would silently stop ownership on that link.
func OnLinkMappedAddresses(held []netif.CurrentAddr, mapped []netip.Addr) ([]netip.Addr, error) {
	subnets := make([]netip.Prefix, 0, len(held))
	linkAddresses := make(map[netip.Addr]bool, len(held))
	for _, current := range held {
		if current.Family != familyV4 {
			continue
		}
		prefix, err := netip.ParsePrefix(current.CIDR)
		if err != nil {
			slog.Warn("wan.routes: link address unparsable", "cidr", current.CIDR, "err", err)
			return nil, fmt.Errorf("parse link address %q: %w", current.CIDR, err)
		}
		if prefix.Bits() >= hostPrefixBitsV4 {
			continue
		}
		subnets = append(subnets, prefix.Masked())
		linkAddresses[prefix.Addr()] = true
	}
	onLink := make([]netip.Addr, 0, len(mapped))
	for _, address := range mapped {
		if linkAddresses[address] {
			continue
		}
		if slices.ContainsFunc(subnets, func(subnet netip.Prefix) bool { return subnet.Contains(address) }) {
			onLink = append(onLink, address)
		}
	}
	return onLink, nil
}

// checkNextHopLocked probes the internal next hop's neighbour entry and
// updates the consecutive-miss counter EvaluateAlerts reads. The internal
// prefix routes just installed use this address as their Via, so an
// unresolvable entry means they black-hole. A check error changes nothing:
// unknown is not evidence in either direction. Callers hold the module lock.
func (m *Module) checkNextHopLocked(ctx context.Context, log *slog.Logger) {
	resolved, err := m.resolveNextHop(ctx, log, m.cfg.InternalIface, m.cfg.OpnsenseEdgeV6)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: next hop check failed",
			"iface", m.cfg.InternalIface, "next_hop", m.cfg.OpnsenseEdgeV6, "err", err)
		return
	}
	if resolved {
		m.nextHopMisses = 0
		return
	}
	m.nextHopMisses++
	log.WarnContext(ctx, "wan.routes: internal next hop did not resolve",
		"iface", m.cfg.InternalIface,
		"next_hop", m.cfg.OpnsenseEdgeV6,
		"consecutive_misses", m.nextHopMisses)
}

// EvaluateAlerts implements ifmgr.Module. It raises a standing alert once the
// internal next hop has failed to resolve on nextHopAlertThreshold
// consecutive reconciles, and resolves it when resolution returns.
func (m *Module) EvaluateAlerts(ctx context.Context, _ *slog.Logger, now time.Time) {
	if m.Env == nil || m.Env.Alerts == nil {
		return
	}
	m.Lock()
	misses := m.nextHopMisses
	m.Unlock()

	fields := []slog.Attr{
		slog.String("iface", m.cfg.InternalIface),
		slog.String("next_hop", m.cfg.OpnsenseEdgeV6),
	}
	if misses >= nextHopAlertThreshold {
		m.Env.Alerts.NotifyContext(ctx, now, slog.LevelWarn,
			alertKindNextHopUnresolved, m.cfg.OpnsenseEdgeV6,
			"wan.routes: internal next hop does not resolve; routes via it black-hole",
			fields...)
		return
	}
	m.Env.Alerts.ResolveContext(ctx, now,
		alertKindNextHopUnresolved, m.cfg.OpnsenseEdgeV6,
		"wan.routes: internal next hop resolves again", fields...)
}

func (m *Module) onMonitorEvent(ctx context.Context, log *slog.Logger, event netif.Event) {
	if !isDefaultRouteEvent(event) {
		return
	}
	eventLog := log.With(
		"kind", event.Kind.String(),
		"family", event.Family,
		"via", event.Via,
	)
	eventLog.DebugContext(ctx, "wan.routes: default route event, reconciling")
	if err := m.Reconcile(ctx, eventLog); err != nil {
		eventLog.WarnContext(ctx, "wan.routes: reconcile after route event failed", "err", err)
	}
}

func desiredState(
	currentGateways gateways,
	health netif.HealthStates,
	cfg Config,
	translations map[string]wanstate.MemberTranslation,
) ([]netif.DesiredRule, []netif.RouteSpec) {
	rules := make([]netif.DesiredRule, 0, len(cfg.WANs)*3+2)
	routes := make([]netif.RouteSpec, 0, len(cfg.WANs)*5+1)

	for _, wan := range cfg.WANs {
		wanGateways := currentGateways[wan.Name]
		routes = appendWANDefaultRoutes(routes, wan, wanGateways, health, translations[wan.Name])
		routes = appendWANInternalRoutes(routes, cfg, wan)

		rules = appendWANRules(rules, wan, wanGateways, health, translations[wan.Name])
	}

	for _, family := range []string{familyV4, familyV6} {
		if carrier := catchAllCarrier(cfg, currentGateways, health, translations, family); carrier != nil {
			rules = append(rules, netif.DesiredRule{Family: family, Priority: catchAllPriority, From: "", Mark: 0, IifName: cfg.InternalIface, UIDRange: "", Table: "", TableID: carrier.TableID})
		}
	}

	return rules, routes
}

// catchAllCarrier selects a lone eligible provider below the family's first configured tier.
func catchAllCarrier(cfg Config, gateways gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation, family string) *WAN {
	tier, available := netif.ActiveTier(familyMembers(cfg, gateways, health, translations, family), health)
	if !available {
		return nil
	}
	lowest := tier
	for _, wan := range cfg.WANs {
		if familyConfigured(wan, family) && wan.Tier < lowest {
			lowest = wan.Tier
		}
	}
	if tier == lowest {
		return nil
	}
	var carrier *WAN
	for i := range cfg.WANs {
		wan := &cfg.WANs[i]
		if wan.Tier != tier || !familyReady(*wan, gateways[wan.Name], health, translations[wan.Name], family) {
			continue
		}
		if carrier != nil {
			return nil
		}
		carrier = wan
	}
	return carrier
}

func familyMembers(cfg Config, gateways gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation, family string) []netif.TierMember {
	members := make([]netif.TierMember, 0, len(cfg.WANs))
	for _, wan := range cfg.WANs {
		if familyReady(wan, gateways[wan.Name], health, translations[wan.Name], family) {
			members = append(members, netif.TierMember{Name: wan.Name, Tier: wan.Tier})
		}
	}
	return members
}

func familyConfigured(wan WAN, family string) bool {
	if family == familyV4 {
		return wan.TranslationV4 != nil
	}
	return wan.TranslationV6 != nil
}

func familyReady(wan WAN, gateways gatewaySet, health netif.HealthStates, translation wanstate.MemberTranslation, family string) bool {
	if !familyConfigured(wan, family) {
		return false
	}
	if family == familyV4 {
		return translation.V4.Ready && wanEnabled(gateways.V4, health.State(wan.Name))
	}
	return translation.V6.Ready && wanEnabled(gateways.V6, health.State(wan.Name))
}

func appendWANDefaultRoutes(routes []netif.RouteSpec, wan WAN, gateways gatewaySet, health netif.HealthStates, translation wanstate.MemberTranslation) []netif.RouteSpec {
	for _, family := range []string{familyV4, familyV6} {
		via := ""
		if familyReady(wan, gateways, health, translation, family) {
			via = gateways.V4
			if family == familyV6 {
				via = gateways.V6
			}
		}
		routes = append(routes, netif.RouteSpec{Family: family, Dest: "default", Via: via, Dev: wan.Iface, TableID: wan.TableID, Metric: 0, Protocol: 0})
	}
	return routes
}

// reconcileTableDefault verifies both removal and installation before returning success.
func reconcileTableDefault(ctx context.Context, log *slog.Logger, desired netif.RouteSpec) error {
	if err := netif.ReconcileTableDefault(ctx, log, desired); err != nil {
		log.WarnContext(ctx, "wan.routes: default route reconciliation failed", "family", desired.Family, "table_id", desired.TableID, "err", err)
		return fmt.Errorf("reconcile table default: %w", err)
	}
	routes, err := netif.ListTableRoutes(ctx, log, desired.Family, desired.TableID)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: default route readback failed", "family", desired.Family, "table_id", desired.TableID, "err", err)
		return fmt.Errorf("read back default route: %w", err)
	}
	matched := false
	for _, route := range routes {
		if route.Dest != "default" {
			continue
		}
		if desired.Via == "" {
			return fmt.Errorf("default route still present after removal: %+v", route)
		}
		if route.Via != desired.Via || route.Dev != desired.Dev {
			return fmt.Errorf("default route differs after reconciliation: got %+v, want %+v", route, desired)
		}
		matched = true
	}
	if desired.Via != "" && !matched {
		return fmt.Errorf("default route missing after reconciliation: %+v", desired)
	}
	return nil
}

func appendWANRules(
	rules []netif.DesiredRule,
	wan WAN,
	gateways gatewaySet,
	health netif.HealthStates,
	translation wanstate.MemberTranslation,
) []netif.DesiredRule {
	if familyReady(wan, gateways, health, translation, familyV4) {
		rules = append(rules, netif.DesiredRule{
			Family:   familyV4,
			Priority: wan.FwMarkPrio,
			From:     "",
			Mark:     wan.FwMark,
			IifName:  "",
			UIDRange: "",
			Table:    "",
			TableID:  wan.TableID,
		})
		if wan.V4Source != "" {
			rules = append(rules, netif.DesiredRule{
				Family:   familyV4,
				Priority: wan.FromPrio,
				From:     wan.V4Source,
				Mark:     0,
				IifName:  "",
				UIDRange: "",
				Table:    "",
				TableID:  wan.TableID,
			})
		}
	}
	if familyReady(wan, gateways, health, translation, familyV6) {
		rules = append(rules, netif.DesiredRule{
			Family:   familyV6,
			Priority: wan.FwMarkPrio,
			From:     "",
			Mark:     wan.FwMark,
			IifName:  "",
			UIDRange: "",
			Table:    "",
			TableID:  wan.TableID,
		})
		if prefix := sourcePrefixV6(wan); prefix != "" {
			rules = append(rules, netif.DesiredRule{
				Family:   familyV6,
				Priority: wan.FromPrio,
				From:     prefix,
				Mark:     0,
				IifName:  "",
				UIDRange: "",
				Table:    "",
				TableID:  wan.TableID,
			})
		}
	}
	return rules
}

func sourcePrefixV6(wan WAN) string {
	translation := wan.TranslationV6
	if translation == nil || translation.Mode != config.TranslationNPTv6 || translation.NPT == nil {
		return ""
	}
	if translation.NPT.ExternalSource == config.PrefixConfigured {
		return translation.NPT.ExternalPrefix.String()
	}
	if translation.NPT.ExpectedPrefix.IsValid() {
		return translation.NPT.ExpectedPrefix.String()
	}
	return ""
}

func appendWANInternalRoutes(routes []netif.RouteSpec, cfg Config, wan WAN) []netif.RouteSpec {
	if wan.TranslationV4 != nil {
		routes = append(routes, netif.RouteSpec{Family: familyV4, Dest: cfg.InternalNetV4, Via: "", Dev: cfg.InternalIface, TableID: wan.TableID, Metric: 0, Protocol: 0})
	}
	if wan.TranslationV6 != nil {
		routes = append(routes, netif.RouteSpec{Family: familyV6, Dest: withPrefix(cfg.OpnsenseEdgeV6, "128"), Via: "", Dev: cfg.InternalIface, TableID: wan.TableID, Metric: 0, Protocol: 0})
	}
	return routes
}

// discoverGateways reads every provider's default gateway in both families. A
// family whose link does not exist reads as no gateway, which is what the
// kernel holds for a link it does not have, and its error goes to
// missingLinks. Any other read error is returned with nil gateways.
func (m *Module) discoverGateways(ctx context.Context, log *slog.Logger) (gatewayDiscovery, error) {
	discovery := gatewayDiscovery{gateways: make(gateways, len(m.cfg.WANs)), missingLinks: nil}
	var gatewayErr error
	for _, wan := range m.cfg.WANs {
		wanGateways := gatewaySet{V4: "", V6: ""}
		for _, family := range []string{familyV4, familyV6} {
			if !familyConfigured(wan, family) {
				continue
			}
			gateway, err := netif.IfaceDefaultGateway(family, wan.Iface)
			if err == nil {
				if family == familyV4 {
					wanGateways.V4 = gateway
				} else {
					wanGateways.V6 = gateway
				}
				continue
			}
			wrapped := fmt.Errorf("%s %s default gateway: %w", wan.Name, family, err)
			if netif.IsLinkNotFound(err) {
				// The provider keeps an empty gateway, so this pass installs no
				// rules for it and writes nothing to its table default. No write
				// is needed there because the kernel removed every route through
				// the device, including that default, when the device went away.
				log.WarnContext(ctx, "wan.routes: provider link missing; treating it as having no gateway",
					"wan", wan.Name, "iface", wan.Iface, "family", family, "err", err)
				discovery.missingLinks = errors.Join(discovery.missingLinks, wrapped)
				continue
			}
			log.WarnContext(ctx, "wan.routes: default gateway read failed",
				"wan", wan.Name, "iface", wan.Iface, "family", family, "err", err)
			gatewayErr = errors.Join(gatewayErr, wrapped)
		}
		discovery.gateways[wan.Name] = wanGateways
	}
	if gatewayErr != nil {
		discovery.gateways = nil
		return discovery, gatewayErr
	}
	return discovery, nil
}

func removeDisabledRuleSlots(
	ctx context.Context,
	log *slog.Logger,
	cfg Config,
	rules []netif.DesiredRule,
) error {
	desiredSlots := desiredRuleSlots(rules)
	var removeErr error
	for _, slot := range ownedRuleSlots(cfg) {
		if desiredSlots[slot] {
			continue
		}
		if err := netif.RemoveRuleAtPriority(ctx, log, slot.family, slot.priority); err != nil {
			removeErr = errors.Join(removeErr, fmt.Errorf(
				"remove disabled rule family=%s priority=%d: %w",
				slot.family,
				slot.priority,
				err,
			))
		}
	}
	return removeErr
}

func desiredRuleSlots(rules []netif.DesiredRule) map[ruleSlot]bool {
	slots := make(map[ruleSlot]bool, len(rules))
	for _, rule := range rules {
		slots[ruleSlot{family: rule.Family, priority: rule.Priority}] = true
	}
	return slots
}

func ownedRuleSlots(cfg Config) []ruleSlot {
	seenSlots := make(map[ruleSlot]bool, len(cfg.WANs)*4+2)
	slots := make([]ruleSlot, 0, len(cfg.WANs)*4+2)
	appendSlot := func(slot ruleSlot) {
		if seenSlots[slot] {
			return
		}
		seenSlots[slot] = true
		slots = append(slots, slot)
	}
	appendSlot(ruleSlot{family: familyV4, priority: catchAllPriority})
	appendSlot(ruleSlot{family: familyV6, priority: catchAllPriority})
	for _, wan := range cfg.WANs {
		appendSlot(ruleSlot{family: familyV4, priority: wan.FwMarkPrio})
		appendSlot(ruleSlot{family: familyV6, priority: wan.FwMarkPrio})
		appendSlot(ruleSlot{family: familyV4, priority: wan.FromPrio})
		appendSlot(ruleSlot{family: familyV6, priority: wan.FromPrio})
	}
	return slots
}

func watchedIfaces(cfg Config) []string {
	seenIfaces := map[string]bool{}
	ifaces := make([]string, 0, len(cfg.WANs)+1)
	appendIface := func(iface string) {
		if iface == "" || seenIfaces[iface] {
			return
		}
		seenIfaces[iface] = true
		ifaces = append(ifaces, iface)
	}
	appendIface(cfg.InternalIface)
	for _, wan := range cfg.WANs {
		appendIface(wan.Iface)
	}
	return ifaces
}

func validateConfig(cfg Config) error {
	if cfg.InternalIface == "" {
		slog.Warn("wan.routes: missing internal_iface")
		return fmt.Errorf("wan.routes: internal_iface is required")
	}
	if cfg.OpnsenseEdgeV6 == "" {
		slog.Warn("wan.routes: missing opnsense_edge_v6")
		return fmt.Errorf("wan.routes: opnsense_edge_v6 is required")
	}
	if cfg.InternalNetV4 == "" {
		slog.Warn("wan.routes: missing internal_net_v4")
		return fmt.Errorf("wan.routes: internal_net_v4 is required")
	}
	seenNames := make(map[string]bool, len(cfg.WANs))
	seenSlots := map[ruleSlot]bool{}
	for i, wan := range cfg.WANs {
		if err := validateWAN(wan); err != nil {
			return fmt.Errorf("wan.routes.wan[%d]: %w", i, err)
		}
		if seenNames[wan.Name] {
			slog.Warn("wan.routes: duplicate WAN name", "name", wan.Name)
			return fmt.Errorf("wan.routes.wan[%d]: duplicate name %q", i, wan.Name)
		}
		seenNames[wan.Name] = true
		for _, slot := range wanRuleSlots(wan) {
			if seenSlots[slot] {
				slog.Warn("wan.routes: duplicate rule slot",
					"family", slot.family, "priority", slot.priority)
				return fmt.Errorf(
					"wan.routes.wan[%d]: duplicate rule slot family=%s priority=%d",
					i,
					slot.family,
					slot.priority,
				)
			}
			seenSlots[slot] = true
		}
	}
	return nil
}

// validateWAN checks the structure of one provider's entry. The set-wide checks
// (unique routing numbers, no reserved table, a weight of at least one) run at
// load time in networkjson, because they need every provider at once and the
// reserved list beside them.
func validateWAN(wan WAN) error {
	if wan.Name == "" {
		return fmt.Errorf("name is required")
	}
	if wan.Iface == "" {
		return fmt.Errorf("iface is required")
	}
	if wan.TableID <= 0 {
		return fmt.Errorf("table_id must be > 0")
	}
	if wan.FwMark == 0 {
		return fmt.Errorf("fw_mark must be > 0")
	}
	if wan.FwMarkPrio <= 0 {
		return fmt.Errorf("fw_mark_prio must be > 0")
	}
	if wan.FromPrio <= 0 {
		return fmt.Errorf("from_prio must be > 0")
	}
	if wan.FwMarkPrio == catchAllPriority || wan.FromPrio == catchAllPriority {
		return fmt.Errorf("rule priorities must not equal the catch-all priority %d", catchAllPriority)
	}
	return nil
}

func wanRuleSlots(wan WAN) []ruleSlot {
	return []ruleSlot{
		{family: familyV4, priority: wan.FwMarkPrio},
		{family: familyV6, priority: wan.FwMarkPrio},
		{family: familyV4, priority: wan.FromPrio},
		{family: familyV6, priority: wan.FromPrio},
	}
}

func isDefaultRouteEvent(event netif.Event) bool {
	if event.Dest != "default" {
		return false
	}
	return event.Kind == netif.EvRouteAdded || event.Kind == netif.EvRouteDeleted
}

func wanEnabled(gateway string, healthState string) bool {
	if gateway == "" {
		return false
	}
	return netif.HealthIsHealthy(healthState)
}

func withPrefix(value string, prefix string) string {
	for _, char := range value {
		if char == '/' {
			return value
		}
	}
	return value + "/" + prefix
}

// New is the Constructor registered with ifmgr.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		InternalIface:   "",
		OpnsenseEdgeV6:  "",
		InternalNetV4:   "",
		HealthStateFile: "",
		WANs:            nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("wan.routes: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	if c.HealthStateFile == "" && len(c.WANs) > 0 {
		c.HealthStateFile = netif.DefaultHealthStatePath
	}
	return &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		cfg:        c,
		// Init fills the three seams with the netif implementations; the
		// counter starts at zero misses, and no pass has owned an address yet.
		resolveNextHop: nil,
		nextHopMisses:  0,
		listAddrs:      nil,
		reconcileAddrs: nil,
		ownedAddresses: nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
