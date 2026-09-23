package wanconfig

import (
	"hash/fnv"
	"strconv"

	"goodkind.io/mwan/internal/config"
)

// TranslationInstanceID is independent of member order and routing numbers.
func TranslationInstanceID(member string, family string) uint32 {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(member + "\x00" + family))
	return hasher.Sum32()
}

func translationItems(member Member) []Item {
	var items []Item
	base := interfacePath(member.Iface)
	if policy := member.TranslationV4; policy != nil {
		path := base + "/ietf-ip:ipv4/goodkind-mwan-steering:translation"
		items = append(items, Item{Path: path + "/mode", Value: string(policy.Mode)})
		for _, mapping := range policy.StaticMappings {
			items = append(items, Item{Path: path + "/static-mapping[external='" + mapping.External.String() + "']/internal", Value: mapping.Internal.String()})
		}
	}
	if policy := member.TranslationV6; policy != nil {
		path := base + "/ietf-ip:ipv6/goodkind-mwan-steering:translation"
		items = append(items, Item{Path: path + "/mode", Value: string(policy.Mode)})
		items = append(items, nptIntentItems(path, policy.NPT)...)
	}
	return items
}

func nptIntentItems(path string, prefix *config.NPTv6Translation) []Item {
	if prefix == nil {
		return nil
	}
	path += "/nptv6"
	items := []Item{{Path: path + "/internal-prefix", Value: prefix.InternalPrefix.String()}, {Path: path + "/external-source", Value: string(prefix.ExternalSource)}}
	if prefix.ExternalPrefix.IsValid() {
		items = append(items, Item{Path: path + "/external-prefix", Value: prefix.ExternalPrefix.String()})
	}
	if prefix.ExpectedPrefix.IsValid() {
		items = append(items, Item{Path: path + "/expected-prefix", Value: prefix.ExpectedPrefix.String()})
	}
	return items
}

func natInstanceItems(member Member) []Item {
	var items []Item
	if policy := member.TranslationV4; policy != nil && policy.Mode != config.TranslationNative {
		base := natPath + "/instances/instance[id='" + uintValue(uint64(member.TranslationIDV4)) + "']"
		items = append(items, Item{Path: base + "/name", Value: member.Name + "/ipv4"}, Item{Path: base + "/type", Value: string(policy.Mode)}, Item{Path: base + "/enable", Value: boolTrue})
	}
	if policy := member.TranslationV6; policy != nil && policy.Mode != config.TranslationNative {
		base := natPath + "/instances/instance[id='" + uintValue(uint64(member.TranslationIDV6)) + "']"
		items = append(items, Item{Path: base + "/name", Value: member.Name + "/ipv6"}, Item{Path: base + "/type", Value: string(policy.Mode)}, Item{Path: base + "/enable", Value: boolTrue})
		if prefix := policy.NPT; prefix != nil && prefix.ExternalSource == config.PrefixConfigured {
			path := base + "/policy[id='" + strconv.Itoa(natPolicyID) + "']/nptv6-prefixes[internal-ipv6-prefix='" + prefix.InternalPrefix.String() + "']"
			items = append(items, Item{Path: path + "/external-ipv6-prefix", Value: prefix.ExternalPrefix.String()})
		}
	}
	return items
}

func validateTranslation(member Member) error {
	if err := validateTranslationV4(member.Name, member.TranslationV4); err != nil {
		return err
	}
	return validateTranslationV6(member.Name, member.TranslationV6)
}

func validateTranslationV4(name string, policy *config.IPv4Translation) error {
	if policy == nil {
		return nil
	}
	switch policy.Mode {
	case config.TranslationNative:
		if len(policy.StaticMappings) != 0 {
			return invalid("member " + name + " native IPv4 cannot include mappings")
		}
	case config.TranslationNAPT44:
	case config.TranslationNPTv6:
		return invalid("member " + name + " has unsupported IPv4 translation mode")
	default:
		return invalid("member " + name + " has unsupported IPv4 translation mode")
	}
	return nil
}

func validateTranslationV6(name string, policy *config.IPv6Translation) error {
	if policy == nil {
		return nil
	}
	switch policy.Mode {
	case config.TranslationNative:
		if policy.NPT != nil {
			return invalid("member " + name + " native IPv6 cannot include NPT")
		}
	case config.TranslationNPTv6:
		return validateNPT(name, policy.NPT)
	case config.TranslationNAPT44:
		return invalid("member " + name + " has unsupported IPv6 translation mode")
	default:
		return invalid("member " + name + " has unsupported IPv6 translation mode")
	}
	return nil
}

func validateNPT(name string, prefix *config.NPTv6Translation) error {
	if prefix == nil {
		return invalid("member " + name + " NPT needs prefix configuration")
	}
	if !prefix.InternalPrefix.IsValid() || !prefix.InternalPrefix.Addr().Is6() {
		return invalid("member " + name + " NPT internal prefix must be IPv6")
	}
	switch prefix.ExternalSource {
	case config.PrefixConfigured:
		if !prefix.ExternalPrefix.IsValid() || !prefix.ExternalPrefix.Addr().Is6() || prefix.ExpectedPrefix.IsValid() {
			return invalid("member " + name + " configured NPT needs an external IPv6 prefix and no expected prefix")
		}
	case config.PrefixDelegated:
		if prefix.ExternalPrefix.IsValid() || (prefix.ExpectedPrefix.IsValid() && !prefix.ExpectedPrefix.Addr().Is6()) {
			return invalid("member " + name + " delegated NPT cannot configure an external prefix")
		}
	default:
		return invalid("member " + name + " has unsupported NPT prefix source")
	}
	return nil
}
