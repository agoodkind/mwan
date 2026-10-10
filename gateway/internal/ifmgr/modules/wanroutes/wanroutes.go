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

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
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
	SelectionEnabled *bool
	Owned            bool
	TableID          int
	FwMark           uint32
	FwMarkPrio       int
	FromPrio         int
	TranslationV4    *config.IPv4Translation
	TranslationV6    *config.IPv6Translation
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
	MappedExternals      []netip.Addr
	LocalMappedExternals []netip.Addr
	// A tunnel provider depends on the provider with the interface in Tunnel.Underlay.
	Tunnel *interfaceintent.Tunnel
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

	// The mutex protects the verified mapping snapshot published by addresses.
	ownedAddresses map[string][]netip.Addr

	// The mutex protects the last pass's tunnel results indexed by connection ID.
	// endpointRoutes stores the endpoint routes that the pass installed or verified.
	endpointRoutes map[string]netif.RouteSpec
	// endpointWatch also stores the last installed route of a tunnel with a failed write.
	endpointWatch map[string]netif.RouteSpec
	tunnelReasons map[string]string
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
	for _, wan := range m.cfg.WANs {
		if wan.Tunnel != nil && env.TunnelEndpointRoutes == nil {
			log.WarnContext(ctx, "wan.routes: tunnel provider has no endpoint route journal", "connection_id", wan.Key())
			return fmt.Errorf("wan.routes: tunnel provider %s requires the addresses module state_file", wan.Key())
		}
	}
	if m.resolveNextHop == nil {
		m.resolveNextHop = netif.NextHopResolves
	}
	if err := netif.StartRuleMonitor(ctx, log, func(event netif.RuleEvent) {
		m.onRuleEvent(ctx, log, event)
	}); err != nil {
		return fmt.Errorf("start policy-rule monitor: %w", err)
	}
	ifmgr.StartIfaceMonitors(ctx, log, moduleName, watchedIfaces(m.cfg), env.Connections, m.onMonitorEvent)
	return nil
}

// Reconcile implements ifmgr.Module.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	log = log.With("op", "reconcile")
	log.DebugContext(ctx, "wan.routes: Reconcile entry")

	m.ownMappedAddressesLocked()

	// A missing link does not end the pass, because the other providers must
	// keep their rules while one card is detached. Any other gateway read
	// error ends it, because a transient netlink failure must not strip a
	// healthy provider.
	discovery, err := m.discoverGateways(ctx, log)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: discoverGateways failed", "err", err)
		return errors.Join(discovery.missingLinks, err)
	}
	currentGateways := discovery.gateways
	m.excludeUnreadyOwnedFamilies(currentGateways)
	health, err := netif.ReadHealthState(m.cfg.HealthStateFile)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: ReadHealthState failed", "err", err)
		return errors.Join(
			discovery.missingLinks,
			fmt.Errorf("read health state %q: %w", m.cfg.HealthStateFile, err),
		)
	}
	translations := m.translationState()
	endpointErr := m.reconcileEndpointRoutes(ctx, log, currentGateways)
	_, routes := m.desiredStateForPass(currentGateways, health, translations)

	reconcileErr := errors.Join(discovery.missingLinks, endpointErr)
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
	rules, _ := m.desiredStateForPass(currentGateways, health, translations)
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
	rules, _ = m.desiredStateForPass(currentGateways, health, translations)
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
	rules, _ = m.desiredStateForPass(currentGateways, health, translations)
	if err := removeDisabledRuleSlots(ctx, log, m.cfg, rules); err != nil {
		// A stale policy rule can override any provider selected by steering.
		clear(currentGateways)
		reconcileErr = errors.Join(reconcileErr, err)
	}
	m.checkNextHopLocked(ctx, log)
	m.publishLiveState(currentGateways, health, translations)
	return reconcileErr
}

// A failed route or rule write in the current pass can remove the underlay gateway.
// desiredStateForPass applies the tunnel dependencies before each computation.
// Callers must lock the module.
func (m *Module) desiredStateForPass(currentGateways gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation) ([]netif.DesiredRule, []netif.RouteSpec) {
	m.tunnelReasons = m.excludeUnreadyTunnelFamilies(currentGateways, health, translations)
	return desiredState(currentGateways, health, m.cfg, translations)
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
	m.tunnelReasons = m.excludeUnreadyTunnelFamilies(currentGateways, health, translations)
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
		v4Ready := familyReady(wan, currentGateways[wan.Key()], health, translations[wan.Key()], familyV4)
		v6Ready := familyReady(wan, currentGateways[wan.Key()], health, translations[wan.Key()], familyV6)
		selectionEnabled := config.ConnectionSelectionEnabled(wan.SelectionEnabled)
		members[wan.Key()] = wanstate.MemberRouting{
			Carrying: selectionEnabled && ((readyV4 && wan.Tier == tierV4 && v4Ready) || (readyV6 && wan.Tier == tierV6 && v6Ready)),
			V4Ready:  v4Ready, V6Ready: v6Ready,
			V4Reason:       familyReason(wan, currentGateways[wan.Key()], health, translations[wan.Key()], familyV4, ""),
			V6Reason:       familyReason(wan, currentGateways[wan.Key()], health, translations[wan.Key()], familyV6, m.tunnelReasons[wan.Key()]),
			OwnedAddresses: slices.Clone(m.ownedAddresses[wan.Key()]),
		}
	}
	m.Env.LiveState.SetRouting(activeTier, members)
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
	if event.Kind == netif.EvResync {
		m.requestRepair("interface monitor resubscribed")
		return
	}
	if event.Kind == netif.EvRouteDeleted && m.ownsInternalRouteDeletion(event) {
		log.WarnContext(ctx, "wan.routes: owned return route removed",
			"family", event.Family, "table_id", event.TableID, "dest", event.Dest)
		m.requestRepair("owned return route deleted")
		return
	}
	if event.Kind == netif.EvRouteDeleted && m.ownsEndpointRouteDeletion(event) {
		log.WarnContext(ctx, "wan.routes: owned tunnel endpoint route removed",
			"family", event.Family, "table_id", event.TableID, "dest", event.Dest)
		m.requestRepair("owned tunnel endpoint route deleted")
		return
	}
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
	if event.TableID == unix.RT_TABLE_MAIN {
		// Policy-table defaults are written by this module and must not queue another pass.
		m.requestRepair("provider main default route changed")
	}
}

func (m *Module) requestRepair(reason string) {
	if m.Env != nil && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile(reason)
	}
}

func (m *Module) ownsInternalRouteDeletion(event netif.Event) bool {
	if event.Iface != m.cfg.InternalIface || event.Dest == "default" {
		return false
	}
	for _, wan := range m.cfg.WANs {
		for _, desired := range appendWANInternalRoutes(nil, m.cfg, wan) {
			protocol := desired.Protocol
			if protocol == 0 {
				protocol = unix.RTPROT_BOOT
			}
			if event.Family == desired.Family && event.Dest == desired.Dest &&
				event.Via == desired.Via && event.TableID == desired.TableID &&
				event.Metric == desired.Metric && event.Protocol == protocol {
				return true
			}
		}
	}
	return false
}

func (m *Module) ownsDesiredRuleDeletion(ctx context.Context, log *slog.Logger, event netif.RuleEvent) bool {
	discovery, err := m.discoverGateways(ctx, log)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: rule deletion gateway read failed", "err", err)
		return false
	}
	health, err := netif.ReadHealthState(m.cfg.HealthStateFile)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: rule deletion health read failed", "err", err)
		return false
	}
	translations := m.translationState()
	// Reconcile removes the rules of a tunnel with an unmet dependency.
	// The unmet dependency exclusion classifies removal of the tunnel's rules as the module's own deletion.
	m.Lock()
	m.excludeUnreadyTunnelFamilies(discovery.gateways, health, translations)
	m.Unlock()
	rules, _ := desiredState(discovery.gateways, health, m.cfg, translations)
	for _, desired := range rules {
		if event.Family == desired.Family && event.Priority == desired.Priority &&
			event.TableID == desired.TableID && event.From == desired.From &&
			event.Mark == desired.Mark && event.IifName == desired.IifName &&
			event.UIDRange == desired.UIDRange {
			return true
		}
	}
	return false
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
		wanGateways := currentGateways[wan.Key()]
		routes = appendWANDefaultRoutes(routes, wan, wanGateways, health, translations[wan.Key()])
		routes = appendWANInternalRoutes(routes, cfg, wan)

		rules = appendWANRules(rules, wan, wanGateways, health, translations[wan.Key()])
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
		if !config.ConnectionSelectionEnabled(wan.SelectionEnabled) || wan.Tier != tier || !familyReady(*wan, gateways[wan.Key()], health, translations[wan.Key()], family) {
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
		if !config.ConnectionSelectionEnabled(wan.SelectionEnabled) {
			continue
		}
		if familyReady(wan, gateways[wan.Key()], health, translations[wan.Key()], family) {
			members = append(members, netif.TierMember{Name: wan.Key(), Tier: wan.Tier})
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
	return conditionsMet(interfaceintent.PolicyFamilyConditions(), wan, gateways, health, translation, family)
}

// Unsupported activation conditions evaluate to false.
func conditionsMet(
	conditions []interfaceintent.PolicyCondition,
	wan WAN,
	gateways gatewaySet,
	health netif.HealthStates,
	translation wanstate.MemberTranslation,
	family string,
) bool {
	gateway := gateways.V4
	translationReady := translation.V4.Ready
	if family == familyV6 {
		gateway = gateways.V6
		translationReady = translation.V6.Ready
	}
	for _, condition := range conditions {
		met := false
		switch condition {
		case interfaceintent.PolicyConditionTranslation:
			met = translationReady
		case interfaceintent.PolicyConditionGateway:
			met = gateway != ""
		case interfaceintent.PolicyConditionHealth:
			met = netif.HealthIsHealthy(health.State(wan.Key()))
		case interfaceintent.PolicyConditionSourcePrefix:
			met = sourcePrefixV6(wan, translation) != ""
		}
		if !met {
			return false
		}
	}
	return true
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
	prefixTranslation, externalConfigured, external := wan.TranslationV6.PolicySource()
	provider := interfaceintent.NewPolicyProvider(
		connectionid.ID(wan.Key()), wan.TableID, wan.FwMark, wan.FwMarkPrio, wan.FromPrio,
		wan.TranslationV4 != nil, wan.V4Source,
		wan.TranslationV6 != nil, prefixTranslation, externalConfigured, external,
	)
	for _, intent := range interfaceintent.ConfiguredPolicyRules(provider) {
		family := familyV4
		if intent.Family == interfaceintent.PolicyFamilyIPv6 {
			family = familyV6
		}
		if !conditionsMet(intent.ActivationConditions, wan, gateways, health, translation, family) {
			continue
		}
		from := ""
		if intent.Kind == interfaceintent.PolicyRuleSource {
			// IPv6 source rules use the external prefix from translation state.
			from = wan.V4Source
			if family == familyV6 {
				from = sourcePrefixV6(wan, translation)
			}
		}
		rules = append(rules, netif.DesiredRule{
			Family:   family,
			Priority: intent.Priority,
			From:     from,
			Mark:     intent.Mark,
			IifName:  "",
			UIDRange: "",
			Table:    "",
			TableID:  intent.TableID,
		})
	}
	return rules
}

func sourcePrefixV6(wan WAN, state wanstate.MemberTranslation) string {
	policy := wan.TranslationV6
	if policy == nil || policy.Mode != config.TranslationNPTv6 || policy.NPT == nil {
		return ""
	}
	if state.V6.ExternalPrefix.IsValid() {
		return state.V6.ExternalPrefix.String()
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
			wrapped := fmt.Errorf("%s %s default gateway: %w", wan.Key(), family, err)
			if netif.IsLinkNotFound(err) {
				// The provider keeps an empty gateway, so this pass installs no
				// rules for it and writes nothing to its table default. No write
				// is needed there because the kernel removed every route through
				// the device, including that default, when the device went away.
				log.WarnContext(ctx, "wan.routes: provider link missing; treating it as having no gateway",
					"wan", wan.Key(), "iface", wan.Iface, "family", family, "err", err)
				discovery.missingLinks = errors.Join(discovery.missingLinks, wrapped)
				continue
			}
			log.WarnContext(ctx, "wan.routes: default gateway read failed",
				"wan", wan.Key(), "iface", wan.Iface, "family", family, "err", err)
			gatewayErr = errors.Join(gatewayErr, wrapped)
		}
		discovery.gateways[wan.Key()] = wanGateways
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
	seenIDs := make(map[string]bool, len(cfg.WANs))
	seenSlots := map[ruleSlot]bool{}
	for i, wan := range cfg.WANs {
		if err := validateWAN(wan); err != nil {
			return fmt.Errorf("wan.routes.wan[%d]: %w", i, err)
		}
		if err := validateTunnelWAN(cfg, wan); err != nil {
			return fmt.Errorf("wan.routes.wan[%d]: %w", i, err)
		}
		if seenIDs[wan.Key()] {
			slog.Warn("wan.routes: duplicate connection ID", "connection_id", wan.Key())
			return fmt.Errorf("wan.routes.wan[%d]: duplicate connection ID %q", i, wan.Key())
		}
		seenIDs[wan.Key()] = true
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
	if wan.Key() == "" {
		return fmt.Errorf("connection ID is required")
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
		BaseModule:     ifmgr.NewBaseModule(moduleName),
		cfg:            c,
		resolveNextHop: nil,
		nextHopMisses:  0,
		ownedAddresses: nil,
		endpointRoutes: nil,
		endpointWatch:  nil,
		tunnelReasons:  nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
