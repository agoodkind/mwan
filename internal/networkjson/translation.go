package networkjson

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
)

type familyTranslation struct {
	Mode           config.TranslationMode `json:"mode"`
	StaticMappings []staticMapping        `json:"static-mapping"`
	NPT            *nptTranslation        `json:"nptv6"`
}

type nptTranslation struct {
	InternalPrefix string              `json:"internal-prefix"`
	ExternalSource config.PrefixSource `json:"external-source"`
	ExternalPrefix string              `json:"external-prefix"`
	ExpectedPrefix string              `json:"expected-prefix"`
}

func handAuthoredAddressing(entry ifaceEntry) bool {
	if entry.IPv4 != nil {
		family := entry.IPv4
		if family.Forwarding != nil || len(family.Address) != 0 || family.DHCP != nil || family.Gateway != "" || family.RouteMetric != nil || len(family.SourceAddresses) != 0 {
			return true
		}
	}
	if entry.IPv6 != nil {
		family := entry.IPv6
		if family.Forwarding != nil || len(family.Address) != 0 || family.Gateway != "" || family.RouteMetric != nil || family.AcceptRA != nil {
			return true
		}
	}
	return false
}

func translationInterface(label, name string, mode config.TranslationMode) error {
	if mode != config.TranslationNative && (name == "" || name == "." || name == ".." || len(name) >= unix.IFNAMSIZ || strings.ContainsAny(name, "/: \t\n\v\f\r")) {
		return fmt.Errorf("%s: translated family requires a usable outgoing interface", label)
	}
	return nil
}

func buildTranslationV4(label string, entry ifaceEntry) (*config.IPv4Translation, error) {
	if entry.IPv4 == nil {
		return nil, nil
	}
	policy := entry.IPv4.Translation
	if policy == nil {
		return nil, fmt.Errorf("%s: translation policy is required", label)
	}
	if policy.Mode != config.TranslationNative && policy.Mode != config.TranslationNAPT44 {
		return nil, fmt.Errorf("%s: translation mode %q is not supported", label, policy.Mode)
	}
	if err := translationInterface(label, entry.Name, policy.Mode); err != nil {
		return nil, err
	}
	if policy.NPT != nil {
		return nil, fmt.Errorf("%s: nptv6 data is not valid for IPv4", label)
	}
	if policy.Mode == config.TranslationNative && len(policy.StaticMappings) != 0 {
		return nil, fmt.Errorf("%s: static mappings require NAPT44", label)
	}
	mappings, err := buildStaticMappings(label, policy.StaticMappings)
	if err != nil {
		return nil, err
	}
	return &config.IPv4Translation{Mode: policy.Mode, StaticMappings: mappings}, nil
}

func buildTranslationV6(label string, entry ifaceEntry) (*config.IPv6Translation, error) {
	if entry.IPv6 == nil {
		return nil, nil
	}
	policy := entry.IPv6.Translation
	if policy == nil {
		return nil, fmt.Errorf("%s: translation policy is required", label)
	}
	if policy.Mode != config.TranslationNative && policy.Mode != config.TranslationNPTv6 {
		return nil, fmt.Errorf("%s: translation mode %q is not supported", label, policy.Mode)
	}
	if err := translationInterface(label, entry.Name, policy.Mode); err != nil {
		return nil, err
	}
	if len(policy.StaticMappings) != 0 {
		return nil, fmt.Errorf("%s: IPv4 static mappings are not valid for IPv6", label)
	}
	if policy.Mode == config.TranslationNative {
		if policy.NPT != nil {
			return nil, fmt.Errorf("%s: native mode cannot configure NPTv6", label)
		}
		return &config.IPv6Translation{Mode: policy.Mode, NPT: nil}, nil
	}
	if policy.NPT == nil {
		return nil, fmt.Errorf("%s: NPTv6 prefix data is required", label)
	}
	wire := policy.NPT
	internal, err := translationPrefix(label, "internal-prefix", wire.InternalPrefix)
	if err != nil {
		return nil, err
	}
	npt := &config.NPTv6Translation{InternalPrefix: internal, ExternalSource: wire.ExternalSource, ExternalPrefix: netip.Prefix{}, ExpectedPrefix: netip.Prefix{}}
	switch wire.ExternalSource {
	case config.PrefixConfigured:
		npt.ExternalPrefix, err = translationPrefix(label, "external-prefix", wire.ExternalPrefix)
		if err != nil {
			return nil, err
		}
		if wire.ExpectedPrefix != "" {
			return nil, fmt.Errorf("%s: expected-prefix requires a delegated source", label)
		}
	case config.PrefixDelegated:
		if entry.IPv6.Delegation == nil || entry.IPv6.DHCP == nil || !*entry.IPv6.DHCP {
			return nil, fmt.Errorf("%s: delegated NPTv6 requires DHCPv6 delegation configuration", label)
		}
		if wire.ExternalPrefix != "" {
			return nil, fmt.Errorf("%s: delegated source cannot configure external-prefix", label)
		}
		if wire.ExpectedPrefix != "" {
			npt.ExpectedPrefix, err = translationPrefix(label, "expected-prefix", wire.ExpectedPrefix)
			if err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("%s: NPTv6 external-source must be configured or delegated", label)
	}
	return &config.IPv6Translation{Mode: policy.Mode, NPT: npt}, nil
}

func translationPrefix(label, leaf, value string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%s: NPTv6 %s requires an IPv6 prefix, got %q", label, leaf, value)
	}
	if prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("%s: NPTv6 %s %q has host bits set", label, leaf, value)
	}
	if prefix.Bits() > 64 {
		return netip.Prefix{}, fmt.Errorf("%s: NPTv6 %s %q must be /64 or shorter", label, leaf, value)
	}
	return prefix, nil
}
