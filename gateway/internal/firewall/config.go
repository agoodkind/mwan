package firewall

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
)

// Family selects the network header used by a local protocol permission.
type Family string

const (
	// IPv4 selects the IPv4 header.
	IPv4 Family = "ipv4"
	// IPv6 selects the IPv6 header.
	IPv6 Family = "ipv6"
)

// ProtocolIPv6InIPv4 is IP protocol 41 for encapsulating an IPv6 packet inside an IPv4 packet.
// The rules use protocol number 41 because nft resolves protocol names using the host's protocol database.
const ProtocolIPv6InIPv4 = "41"

// TransportPermit allows packets addressed to the gateway. Source and
// destination prefixes are optional. Ports apply only to TCP and UDP.
type TransportPermit struct {
	InputInterface  string
	Family          Family
	Source          netip.Prefix
	Destination     netip.Prefix
	Protocol        string
	SourcePort      uint16
	DestinationPort uint16
}

// ForwardingPath permits the indicated families between two logical links.
type ForwardingPath struct {
	InternalInterface string
	ExternalInterface string
	IPv4              bool
	IPv6              bool
}

// Mapping pairs an external IPv4 address with an internal IPv4 address.
type Mapping struct {
	External netip.Addr
	Internal netip.Addr
}

// Provider contains the packet policy for one external interface.
type Provider struct {
	Interface      string
	Mark           uint32
	ForcedDSCP     uint8
	MasqueradeIPv4 bool
	StaticMappings []Mapping
	// ReducedMTU marks an interface with an MTU below the internal interface MTU.
	// The gateway sends a packet-too-big error for a forwarded packet that exceeds that MTU.
	ReducedMTU bool
}

// Service permits a management port from selected source prefixes.
type Service struct {
	Protocol string
	Port     uint16
	Sources  []netip.Prefix
}

// ManagementPolicy selects whether a firewall configuration must name a
// management interface.
type ManagementPolicy int

const (
	// ManagementRequired rejects an empty management interface.
	// ManagementRequired is the zero value.
	ManagementRequired ManagementPolicy = iota
	// ManagementOptional accepts an empty management interface only when the
	// configuration lists no management services.
	ManagementOptional
)

// Absent returns true only for ManagementOptional with an empty interface.
func (p ManagementPolicy) Absent(managementInterface string) bool {
	return p == ManagementOptional && managementInterface == ""
}

// Config contains only inputs that affect the three firewall-owned tables.
// Routing and tunnel configuration are projected into permits and paths.
type Config struct {
	Enabled               bool
	ManagementPolicy      ManagementPolicy
	InternalInterface     string
	InternalNetworkIPv4   netip.Prefix
	ManagementInterface   string
	ManagementServices    []Service
	KnownInterfaces       []string
	LocalPermits          []TransportPermit
	Paths                 []ForwardingPath
	Providers             []Provider
	PinnedProvider        string
	PinnedSourceIPv4      netip.Addr
	PinnedSourceIPv6      netip.Addr
	PinnedSourcePort      uint16
	PinnedDestinationPort uint16
	PinnedIPv4            []netip.Prefix
	PinnedIPv6            []netip.Prefix
	PinnedSetV4Name       string
	PinnedSetV6Name       string
}

// ModuleConfigName identifies the ifmgr module configuration.
func (Config) ModuleConfigName() string { return "firewall" }

// BaselineConfig contains the local values needed before provider validation.
type BaselineConfig struct {
	ManagementPolicy    ManagementPolicy
	ManagementInterface string
	ManagementServices  []Service
	InternalInterface   string
	ProviderInterfaces  []string
	LocalPermits        []TransportPermit
}

// Validate checks the local permits before baseline installation.
func (c BaselineConfig) Validate() error {
	managementAbsent := c.ManagementPolicy.Absent(c.ManagementInterface)
	if !managementAbsent {
		if err := validInterface(c.ManagementInterface); err != nil {
			slog.Warn("invalid baseline management interface", "err", err)
			return fmt.Errorf("management interface: %w", err)
		}
	}
	if err := validInterface(c.InternalInterface); err != nil {
		slog.Warn("invalid baseline internal interface", "err", err)
		return fmt.Errorf("internal interface: %w", err)
	}
	if err := validateManagementServiceCount(managementAbsent, len(c.ManagementServices)); err != nil {
		return err
	}
	for _, service := range c.ManagementServices {
		if service.Protocol != "tcp" && service.Protocol != "udp" || service.Port == 0 {
			return fmt.Errorf("invalid management service %q port %d", service.Protocol, service.Port)
		}
		for _, source := range service.Sources {
			if !source.IsValid() {
				return fmt.Errorf("management service has an invalid source prefix")
			}
		}
	}
	for _, name := range c.ProviderInterfaces {
		if err := validInterface(name); err != nil {
			slog.Warn("invalid baseline provider interface", "err", err)
			return fmt.Errorf("provider interface: %w", err)
		}
	}
	for _, permit := range c.LocalPermits {
		if err := validatePermit(permit); err != nil {
			slog.Warn("invalid baseline local permit", "err", err)
			return err
		}
		allowed := permit.InputInterface == c.ManagementInterface || permit.InputInterface == c.InternalInterface
		for _, provider := range c.ProviderInterfaces {
			if provider == permit.InputInterface {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("local permit interface %q is not declared", permit.InputInterface)
		}
	}
	return nil
}

// Validate checks every value before an applier can modify kernel rules.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if err := validInterface(c.InternalInterface); err != nil {
		slog.Warn("invalid firewall internal interface", "err", err)
		return fmt.Errorf("internal interface: %w", err)
	}
	managementAbsent := c.ManagementPolicy.Absent(c.ManagementInterface)
	if !managementAbsent {
		if err := validInterface(c.ManagementInterface); err != nil {
			slog.Warn("invalid firewall management interface", "err", err)
			return fmt.Errorf("management interface: %w", err)
		}
	}
	if !c.InternalNetworkIPv4.IsValid() || !c.InternalNetworkIPv4.Addr().Is4() {
		return fmt.Errorf("internal IPv4 network is required")
	}
	if err := validateManagementServiceCount(managementAbsent, len(c.ManagementServices)); err != nil {
		return err
	}
	for _, service := range c.ManagementServices {
		if service.Protocol != "tcp" && service.Protocol != "udp" {
			return fmt.Errorf("management service protocol %q is not TCP or UDP", service.Protocol)
		}
		if service.Port == 0 {
			return fmt.Errorf("management service %s has no destination port", service.Protocol)
		}
		for _, source := range service.Sources {
			if !source.IsValid() {
				return fmt.Errorf("management service has an invalid source prefix")
			}
		}
	}
	providerNames, err := c.validateProviders()
	if err != nil {
		return err
	}
	if err := c.validatePin(providerNames); err != nil {
		return err
	}
	return c.validateReferences()
}

func validateManagementServiceCount(managementAbsent bool, count int) error {
	if managementAbsent {
		if count != 0 {
			return fmt.Errorf("management services require a management interface")
		}
		return nil
	}
	if count == 0 {
		return fmt.Errorf("at least one management service is required")
	}
	return nil
}

func (c Config) validateProviders() (map[string]bool, error) {
	providerNames := make(map[string]bool, len(c.Providers))
	providerMarks := make(map[uint32]bool, len(c.Providers))
	if len(c.Providers) == 0 {
		return nil, fmt.Errorf("at least one provider is required")
	}
	for _, provider := range c.Providers {
		if err := validInterface(provider.Interface); err != nil {
			slog.Warn("invalid firewall provider interface", "err", err)
			return nil, fmt.Errorf("provider: %w", err)
		}
		if providerNames[provider.Interface] {
			return nil, fmt.Errorf("provider interface %q is duplicated", provider.Interface)
		}
		providerNames[provider.Interface] = true
		if provider.Mark == 0 || providerMarks[provider.Mark] {
			return nil, fmt.Errorf("provider %q has a zero or duplicated mark", provider.Interface)
		}
		providerMarks[provider.Mark] = true
		if provider.ForcedDSCP > 63 {
			return nil, fmt.Errorf("provider %q has DSCP above 63", provider.Interface)
		}
		for _, mapping := range provider.StaticMappings {
			if !provider.MasqueradeIPv4 || !mapping.External.Is4() || !mapping.Internal.Is4() {
				return nil, fmt.Errorf("provider %q has a mapping outside IPv4 NAPT", provider.Interface)
			}
		}
	}
	return providerNames, nil
}

func (c Config) validatePin(providerNames map[string]bool) error {
	if c.PinnedProvider != "" {
		if err := c.validatePinSettings(providerNames); err != nil {
			return err
		}
	}
	for _, prefix := range c.PinnedIPv4 {
		if !prefix.IsValid() || !prefix.Addr().Is4() {
			return fmt.Errorf("pinned IPv4 set contains an invalid prefix")
		}
	}
	for _, prefix := range c.PinnedIPv6 {
		if !prefix.IsValid() || !prefix.Addr().Is6() {
			return fmt.Errorf("pinned IPv6 set contains an invalid prefix")
		}
	}
	return nil
}

func (c Config) validatePinSettings(providerNames map[string]bool) error {
	if c.PinnedSetV4Name == "" || c.PinnedSetV6Name == "" {
		return fmt.Errorf("pinned destination set names are required")
	}
	if err := validSetName(c.PinnedSetV4Name); err != nil {
		return err
	}
	if err := validSetName(c.PinnedSetV6Name); err != nil {
		return err
	}
	if !providerNames[c.PinnedProvider] {
		return fmt.Errorf("pinned provider interface %q is not configured", c.PinnedProvider)
	}
	if !c.PinnedSourceIPv4.Is4() || !c.PinnedSourceIPv6.Is6() {
		return fmt.Errorf("pinned source addresses must include IPv4 and IPv6")
	}
	if c.PinnedSourcePort == 0 || c.PinnedDestinationPort == 0 {
		return fmt.Errorf("pinned UDP ports are required")
	}
	return nil
}

func (c Config) validateReferences() error {
	known := make(map[string]bool, len(c.KnownInterfaces))
	for _, name := range c.KnownInterfaces {
		if err := validInterface(name); err != nil {
			return err
		}
		known[name] = true
	}
	for _, provider := range c.Providers {
		if !known[provider.Interface] {
			return fmt.Errorf("provider interface %q is not declared", provider.Interface)
		}
	}
	managementAbsent := c.ManagementPolicy.Absent(c.ManagementInterface)
	managementUndeclared := !managementAbsent && !known[c.ManagementInterface]
	if managementUndeclared || !known[c.InternalInterface] {
		return fmt.Errorf("management or internal interface is not declared")
	}
	for _, permit := range c.LocalPermits {
		if err := validatePermit(permit); err != nil {
			return err
		}
		if !known[permit.InputInterface] {
			return fmt.Errorf("local permit interface %q is not declared", permit.InputInterface)
		}
	}
	for _, path := range c.Paths {
		if err := validInterface(path.InternalInterface); err != nil {
			slog.Warn("invalid forwarding path internal interface", "err", err)
			return fmt.Errorf("forwarding path internal interface: %w", err)
		}
		if err := validInterface(path.ExternalInterface); err != nil {
			slog.Warn("invalid forwarding path external interface", "err", err)
			return fmt.Errorf("forwarding path external interface: %w", err)
		}
		if !path.IPv4 && !path.IPv6 {
			return fmt.Errorf("forwarding path %q enables no family", path.ExternalInterface)
		}
		if !known[path.InternalInterface] || !known[path.ExternalInterface] {
			return fmt.Errorf("forwarding path %q to %q has an undeclared interface", path.InternalInterface, path.ExternalInterface)
		}
	}
	return nil
}

func validatePermit(permit TransportPermit) error {
	if err := validInterface(permit.InputInterface); err != nil {
		slog.Warn("invalid local permit interface", "err", err)
		return fmt.Errorf("local permit: %w", err)
	}
	if permit.Family != IPv4 && permit.Family != IPv6 {
		return fmt.Errorf("local permit has invalid family %q", permit.Family)
	}
	if permit.Source.IsValid() && familyOf(permit.Source) != permit.Family {
		return fmt.Errorf("local permit source has the wrong family")
	}
	if permit.Destination.IsValid() && familyOf(permit.Destination) != permit.Family {
		return fmt.Errorf("local permit destination has the wrong family")
	}
	if permit.Protocol != "tcp" && permit.Protocol != "udp" && permit.Protocol != "icmp" && permit.Protocol != "icmpv6" && permit.Protocol != "gre" && permit.Protocol != "ipip" && permit.Protocol != ProtocolIPv6InIPv4 {
		return fmt.Errorf("local permit protocol %q is unsupported", permit.Protocol)
	}
	if permit.Protocol == ProtocolIPv6InIPv4 && permit.Family != IPv4 {
		return fmt.Errorf("local permit protocol %s requires the IPv4 family", permit.Protocol)
	}
	if (permit.Protocol != "tcp" && permit.Protocol != "udp") && (permit.SourcePort != 0 || permit.DestinationPort != 0) {
		return fmt.Errorf("local permit %s cannot use ports", permit.Protocol)
	}
	if permit.Protocol == "icmp" && permit.Family != IPv4 || permit.Protocol == "icmpv6" && permit.Family != IPv6 {
		return fmt.Errorf("local permit ICMP family does not match")
	}
	return nil
}

func familyOf(prefix netip.Prefix) Family {
	if prefix.Addr().Is4() {
		return IPv4
	}
	return IPv6
}

func validInterface(name string) error {
	if name == "" || len(name) > 15 || strings.ContainsAny(name, " \t\n\r\"\\;{}") || slices.Contains([]string{".", ".."}, name) {
		return fmt.Errorf("invalid interface name %q", name)
	}
	return nil
}

func validSetName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("invalid set name %q", name)
	}
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' {
			continue
		}
		return fmt.Errorf("invalid set name %q", name)
	}
	return nil
}
