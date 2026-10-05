package networkjson

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/firewall"
)

type firewallWire struct {
	ManagementInterface   string                  `json:"management-interface"`
	ManagementServices    []managementServiceWire `json:"management-service"`
	PinnedProvider        string                  `json:"pinned-provider"`
	PinnedConnectionID    string                  `json:"pinned-connection-id"`
	PinnedSourceV4        string                  `json:"pinned-source-v4"`
	PinnedSourcePort      *uint16                 `json:"pinned-source-port"`
	PinnedDestinationPort *uint16                 `json:"pinned-destination-port"`
	PinnedSetV4Name       string                  `json:"pinned-set-v4-name"`
	PinnedSetV6Name       string                  `json:"pinned-set-v6-name"`
	PinnedV4              []string                `json:"pinned-v4"`
	PinnedV6              []string                `json:"pinned-v6"`
}

func pinnedConnectionID(wire *firewallWire) string {
	if wire == nil {
		return ""
	}
	return wire.PinnedConnectionID
}

type managementServiceWire struct {
	Protocol      string   `json:"protocol"`
	Port          *uint16  `json:"port"`
	AllowedSource []string `json:"allowed-source"`
}

type baselineDocument struct {
	Interfaces struct {
		Interface []struct {
			Name string          `json:"name"`
			WAN  json.RawMessage `json:"goodkind-mwan-steering:wan"`
		} `json:"interface"`
		SteeringGroup struct {
			Routes struct {
				InternalIface string `json:"internal-iface"`
			} `json:"routes"`
			Firewall *baselineFirewallWire `json:"firewall"`
		} `json:"goodkind-mwan-steering:steering-group"`
	} `json:"ietf-interfaces:interfaces"`
}

type baselineFirewallWire struct {
	ManagementInterface string                  `json:"management-interface"`
	ManagementServices  []managementServiceWire `json:"management-service"`
}

// LoadBaseline reads only the local settings needed before full network
// validation. A nil result means that daemon firewall ownership is disabled.
func LoadBaseline(path string) (*firewall.BaselineConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("networkjson: read firewall baseline failed", "path", path, "err", err)
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc baselineDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Error("networkjson: decode firewall baseline failed", "path", path, "err", err)
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	group := doc.Interfaces.SteeringGroup
	if group.Firewall == nil {
		return nil, nil
	}
	var providers []string
	var declared []string
	for _, entry := range doc.Interfaces.Interface {
		declared = append(declared, entry.Name)
		if len(entry.WAN) > 0 && string(entry.WAN) != "null" {
			providers = append(providers, entry.Name)
		}
	}
	baseline, err := buildBaseline(group.Firewall, group.Routes.InternalIface, declared, providers)
	if err != nil {
		slog.Error("networkjson: firewall baseline rejected", "path", path, "err", err)
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &baseline, nil
}

func buildBaseline(wire *baselineFirewallWire, internalInterface string, declared []string, providers []string) (firewall.BaselineConfig, error) {
	if wire == nil {
		return firewall.BaselineConfig{}, fmt.Errorf("steering-group/firewall is absent")
	}
	services, err := buildManagementServices(wire.ManagementServices)
	if err != nil {
		return firewall.BaselineConfig{}, err
	}
	baseline := firewall.BaselineConfig{
		ManagementInterface: wire.ManagementInterface,
		ManagementServices:  services,
		InternalInterface:   internalInterface,
		ProviderInterfaces:  nil,
		LocalPermits:        nil,
	}
	seen := make(map[string]bool)
	for _, name := range providers {
		if seen[name] {
			return firewall.BaselineConfig{}, fmt.Errorf("provider interface %q appears twice", name)
		}
		seen[name] = true
		baseline.ProviderInterfaces = append(baseline.ProviderInterfaces, name)
		baseline.LocalPermits = append(baseline.LocalPermits,
			newPermit(name, firewall.IPv4, "udp", 67, 68),
			newPermit(name, firewall.IPv6, "udp", 547, 546),
		)
	}
	for _, family := range []firewall.Family{firewall.IPv4, firewall.IPv6} {
		baseline.LocalPermits = append(baseline.LocalPermits,
			newPermit(baseline.InternalInterface, family, "tcp", 0, 179),
			newPermit(baseline.InternalInterface, family, "udp", 0, 3784),
			newPermit(baseline.InternalInterface, family, "udp", 0, 3785),
		)
	}
	if err := baseline.Validate(); err != nil {
		slog.Warn("networkjson: firewall baseline invalid", "err", err)
		return firewall.BaselineConfig{}, fmt.Errorf("steering-group/firewall baseline: %w", err)
	}
	known := make(map[string]bool, len(declared))
	for _, name := range declared {
		if known[name] {
			return firewall.BaselineConfig{}, fmt.Errorf("interface %q appears twice", name)
		}
		known[name] = true
	}
	if !known[baseline.ManagementInterface] || !known[baseline.InternalInterface] {
		return firewall.BaselineConfig{}, fmt.Errorf("firewall management or internal interface is not declared")
	}
	return baseline, nil
}

func buildManagementServices(entries []managementServiceWire) ([]firewall.Service, error) {
	services := make([]firewall.Service, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Port == nil {
			return nil, fmt.Errorf("firewall management-service %s has no port", entry.Protocol)
		}
		key := fmt.Sprintf("%s/%d", entry.Protocol, *entry.Port)
		if seen[key] {
			return nil, fmt.Errorf("firewall management-service %s is duplicated", key)
		}
		seen[key] = true
		service := firewall.Service{Protocol: entry.Protocol, Port: *entry.Port, Sources: nil}
		for _, raw := range entry.AllowedSource {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				slog.Warn("networkjson: management source invalid", "service", key, "source", raw, "err", err)
				return nil, fmt.Errorf("firewall management-service %s allowed-source %q: %w", key, raw, err)
			}
			service.Sources = append(service.Sources, prefix)
		}
		services = append(services, service)
	}
	return services, nil
}

func newPermit(iface string, family firewall.Family, protocol string, sourcePort uint16, destinationPort uint16) firewall.TransportPermit {
	return firewall.TransportPermit{
		InputInterface:  iface,
		Family:          family,
		Source:          netip.Prefix{},
		Destination:     netip.Prefix{},
		Protocol:        protocol,
		SourcePort:      sourcePort,
		DestinationPort: destinationPort,
	}
}

func buildFirewall(doc *document, loaded *Config) (firewall.Config, error) {
	var none firewall.Config
	group := doc.Interfaces.SteeringGroup
	if group.Firewall == nil {
		return none, nil
	}
	var providerInterfaces []string
	var declared []string
	for _, entry := range doc.Interfaces.Interface {
		declared = append(declared, entry.Name)
		if entry.WAN != nil {
			providerInterfaces = append(providerInterfaces, entry.Name)
		}
	}
	baselineWire := &baselineFirewallWire{
		ManagementInterface: group.Firewall.ManagementInterface,
		ManagementServices:  group.Firewall.ManagementServices,
	}
	baseline, err := buildBaseline(baselineWire, group.Routes.InternalIface, declared, providerInterfaces)
	if err != nil {
		return none, err
	}
	network, err := netip.ParsePrefix(group.Routes.InternalNetV4)
	if err != nil {
		slog.Warn("networkjson: firewall internal network invalid", "value", group.Routes.InternalNetV4, "err", err)
		return none, fmt.Errorf("firewall internal-net-v4: %w", err)
	}
	var cfg firewall.Config
	cfg.Enabled = true
	cfg.InternalInterface = baseline.InternalInterface
	cfg.InternalNetworkIPv4 = network
	cfg.ManagementInterface = baseline.ManagementInterface
	cfg.ManagementServices = baseline.ManagementServices
	cfg.LocalPermits = baseline.LocalPermits
	cfg.PinnedSetV4Name = group.Firewall.PinnedSetV4Name
	cfg.PinnedSetV6Name = group.Firewall.PinnedSetV6Name
	for _, entry := range doc.Interfaces.Interface {
		cfg.KnownInterfaces = append(cfg.KnownInterfaces, entry.Name)
	}
	if err := populateFirewallProviders(&cfg, doc, loaded, group.Firewall); err != nil {
		return none, err
	}
	if err := populateFirewallPins(&cfg, group.Firewall, group.Translation.OpnsenseEdgeV6); err != nil {
		return none, err
	}
	if err := cfg.Validate(); err != nil {
		slog.Warn("networkjson: firewall configuration invalid", "err", err)
		return none, fmt.Errorf("steering-group/firewall: %w", err)
	}
	return cfg, nil
}

func populateFirewallProviders(cfg *firewall.Config, doc *document, loaded *Config, wire *firewallWire) error {
	if wire.PinnedProvider != "" && wire.PinnedConnectionID != "" {
		return fmt.Errorf("firewall must select one of pinned-provider or pinned-connection-id")
	}
	matches := 0
	for _, entry := range doc.Interfaces.Interface {
		if entry.WAN == nil {
			continue
		}
		id := loaded.ConnectionIDs[entry.Name].String()
		wan, accepted := loaded.WAN[id]
		if !accepted {
			continue
		}
		if wan.FwMark < 0 || uint64(wan.FwMark) > math.MaxUint32 {
			return fmt.Errorf("firewall provider %s has invalid mark %d", entry.WAN.Name, wan.FwMark)
		}
		if wan.ForcedDSCP < 0 || wan.ForcedDSCP > 63 {
			return fmt.Errorf("firewall provider %s has invalid DSCP %d", entry.WAN.Name, wan.ForcedDSCP)
		}
		var provider firewall.Provider
		provider.Interface = wan.Iface
		provider.Mark = uint32(wan.FwMark)
		provider.ForcedDSCP = uint8(wan.ForcedDSCP)
		if policy := wan.TranslationV4; policy != nil && policy.Mode == config.TranslationNAPT44 {
			provider.MasqueradeIPv4 = true
			for _, mapping := range policy.StaticMappings {
				provider.StaticMappings = append(provider.StaticMappings, firewall.Mapping{External: mapping.External, Internal: mapping.Internal})
			}
		}
		cfg.Providers = append(cfg.Providers, provider)
		cfg.Paths = append(cfg.Paths, firewall.ForwardingPath{
			InternalInterface: cfg.InternalInterface,
			ExternalInterface: wan.Iface,
			IPv4:              true,
			IPv6:              true,
		})
		if (wire.PinnedConnectionID != "" && id == wire.PinnedConnectionID) ||
			(wire.PinnedProvider != "" && entry.WAN.Name == wire.PinnedProvider) {
			cfg.PinnedProvider = wan.Iface
			matches++
		}
	}
	if matches > 1 {
		return fmt.Errorf("firewall pinned-provider %q matches multiple connections; use pinned-connection-id", wire.PinnedProvider)
	}
	return nil
}

func populateFirewallPins(cfg *firewall.Config, wire *firewallWire, opnsenseEdgeV6 string) error {
	if wire.PinnedProvider != "" || wire.PinnedConnectionID != "" {
		if cfg.PinnedProvider == "" {
			return fmt.Errorf("firewall pin target is not an accepted connection")
		}
		sourceV4, err := netip.ParseAddr(wire.PinnedSourceV4)
		if err != nil {
			slog.Warn("networkjson: firewall pinned IPv4 source invalid", "value", wire.PinnedSourceV4, "err", err)
			return fmt.Errorf("firewall pinned-source-v4: %w", err)
		}
		cfg.PinnedSourceIPv4 = sourceV4
		sourceV6, err := netip.ParseAddr(opnsenseEdgeV6)
		if err != nil {
			slog.Warn("networkjson: firewall pinned IPv6 source invalid", "value", opnsenseEdgeV6, "err", err)
			return fmt.Errorf("firewall opnsense-edge-v6: %w", err)
		}
		cfg.PinnedSourceIPv6 = sourceV6
		if wire.PinnedSourcePort == nil || wire.PinnedDestinationPort == nil {
			return fmt.Errorf("firewall pinned UDP ports are required")
		}
		cfg.PinnedSourcePort = *wire.PinnedSourcePort
		cfg.PinnedDestinationPort = *wire.PinnedDestinationPort
	}
	for _, raw := range wire.PinnedV4 {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			slog.Warn("networkjson: firewall pinned IPv4 network invalid", "value", raw, "err", err)
			return fmt.Errorf("firewall pinned-v4 %q: %w", raw, err)
		}
		cfg.PinnedIPv4 = append(cfg.PinnedIPv4, prefix)
	}
	for _, raw := range wire.PinnedV6 {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			slog.Warn("networkjson: firewall pinned IPv6 network invalid", "value", raw, "err", err)
			return fmt.Errorf("firewall pinned-v6 %q: %w", raw, err)
		}
		cfg.PinnedIPv6 = append(cfg.PinnedIPv6, prefix)
	}
	return nil
}
