package wanroutes

import (
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const catchAllSlotCount = 2

type gatewaySet struct {
	V4       string
	V6       string
	V6Routed bool
}

type gateways map[string]gatewaySet

type ruleSlot struct {
	family   string
	priority int
}

func familyMembers(
	cfg Config,
	gateways gateways,
	health netif.HealthStates,
	translations map[string]wanstate.MemberTranslation,
	family string,
) []netif.TierMember {
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

func familyReady(
	wan WAN,
	gateways gatewaySet,
	health netif.HealthStates,
	translation wanstate.MemberTranslation,
	family string,
) bool {
	if !familyConfigured(wan, family) {
		return false
	}

	return conditionsMet(
		interfaceintent.PolicyFamilyConditions(),
		wan,
		gateways,
		health,
		translation,
		family,
	)
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
	gatewayKnown := gateways.V4 != ""
	translationReady := translation.V4.Ready
	if family == familyV6 {
		gatewayKnown = gateways.V6 != "" || gateways.V6Routed
		translationReady = translation.V6.Ready
	}

	for _, condition := range conditions {
		met := false
		switch condition {
		case interfaceintent.PolicyConditionTranslation:
			met = translationReady
		case interfaceintent.PolicyConditionGateway:
			met = gatewayKnown
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

func appendWANRules(
	rules []netif.DesiredRule,
	wan WAN,
	gateways gatewaySet,
	health netif.HealthStates,
	translation wanstate.MemberTranslation,
) []netif.DesiredRule {
	prefixTranslation, externalConfigured, external := wan.TranslationV6.PolicySource()

	provider := interfaceintent.NewPolicyProvider(
		connectionid.ID(wan.Key()),
		wan.TableID,
		wan.FwMark,
		wan.FwMarkPrio,
		wan.FromPrio,
		wan.TranslationV4 != nil,
		wan.V4Source,
		wan.TranslationV6 != nil,
		prefixTranslation,
		externalConfigured,
		external,
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

		rules = append(
			rules,
			netif.DesiredRule{
				Family:   family,
				Priority: intent.Priority,
				From:     from,
				Mark:     intent.Mark,
				IifName:  "",
				UIDRange: "",
				Table:    "",
				TableID:  intent.TableID,
			},
		)
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
		routes = append(
			routes,
			netif.RouteSpec{
				Family:   familyV4,
				Dest:     cfg.InternalNetV4,
				Via:      "",
				Dev:      cfg.InternalIface,
				TableID:  wan.TableID,
				Metric:   0,
				Protocol: 0,
			},
		)
	}

	if wan.TranslationV6 != nil {
		routes = append(
			routes,
			netif.RouteSpec{
				Family:   familyV6,
				Dest:     withPrefix(cfg.OpnsenseEdgeV6, "128"),
				Via:      "",
				Dev:      cfg.InternalIface,
				TableID:  wan.TableID,
				Metric:   0,
				Protocol: 0,
			},
		)
	}

	return routes
}

func desiredRuleSlots(rules []netif.DesiredRule) map[ruleSlot]bool {
	slots := make(map[ruleSlot]bool, len(rules))
	for _, rule := range rules {
		slots[ruleSlot{family: rule.Family, priority: rule.Priority}] = true
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

func withPrefix(value string, prefix string) string {
	for _, char := range value {
		if char == '/' {
			return value
		}
	}

	return value + "/" + prefix
}

func ownedRuleSlots(cfg Config) []ruleSlot {
	seenSlots := make(map[ruleSlot]bool, len(cfg.WANs)*4+catchAllSlotCount)
	slots := make([]ruleSlot, 0, len(cfg.WANs)*4+catchAllSlotCount)
	appendSlot := func(slot ruleSlot) {
		if seenSlots[slot] {
			return
		}

		seenSlots[slot] = true
		slots = append(slots, slot)
	}
	appendSlot(ruleSlot{
		family:   familyV4,
		priority: catchAllPriority,
	})
	appendSlot(ruleSlot{
		family:   familyV6,
		priority: catchAllPriority,
	})

	for _, wan := range cfg.WANs {
		appendSlot(ruleSlot{
			family:   familyV4,
			priority: wan.FwMarkPrio,
		})
		appendSlot(ruleSlot{
			family:   familyV6,
			priority: wan.FwMarkPrio,
		})
		appendSlot(ruleSlot{
			family:   familyV4,
			priority: wan.FromPrio,
		})
		appendSlot(ruleSlot{
			family:   familyV6,
			priority: wan.FromPrio,
		})
	}

	return slots
}
