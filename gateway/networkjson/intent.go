package networkjson

// Both dataplanes use the same connection intent. Each interface declaration
// supplies the link and address settings, and the interface owner determines
// which settings the compiler accepts.

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/networkd"
)

func buildConnection(entry ifaceEntry, id connectionid.ID) (interfaceintent.Connection, error) {
	connection := interfaceintent.Connection{
		ID:         id,
		Name:       entry.Name,
		Type:       entry.Type,
		Enabled:    entry.Enabled,
		Roles:      0,
		Owner:      interfaceintent.Owner(entry.Owner),
		Link:       nil,
		IPv4:       nil,
		IPv6:       nil,
		LeaseStore: entry.LeaseStore,
		Networkd:   nil,
	}
	if entry.WAN != nil {
		connection.Roles |= interfaceintent.RoleProvider
	}

	owner, err := validatedOwner(entry)
	if err != nil {
		return connection, err
	}

	connection.Owner = owner
	if entry.Link != nil {
		link, err := buildIntentLink(entry)
		if err != nil {
			return connection, err
		}

		connection.Link = link
	}

	if entry.IPv4 != nil {
		ipv4, err := buildIntentV4(entry.Name, *entry.IPv4)
		if err != nil {
			return connection, err
		}

		connection.IPv4 = ipv4
	}

	if entry.IPv6 != nil {
		ipv6, err := buildIntentV6(entry.Name, *entry.IPv6)
		if err != nil {
			return connection, err
		}

		connection.IPv6 = ipv6
	}

	if entry.Networkd != nil {
		files, err := buildIntentFiles(*entry.Networkd)
		if err != nil {
			slog.Error("networkjson: networkd entries invalid", "interface", entry.Name, "err", err)
			return connection, fmt.Errorf("interface %s: %w", entry.Name, err)
		}

		connection.Networkd = files
	}

	if owner == interfaceintent.OwnerMWAN {
		if err := validateMWANConnection(entry, connection); err != nil {
			return connection, err
		}
	}

	if err := validateRenderedConnection(entry, connection); err != nil {
		return connection, err
	}

	return connection, nil
}

func validatedOwner(entry ifaceEntry) (interfaceintent.Owner, error) {
	owner := interfaceintent.Owner(entry.Owner)
	if owner == "" {
		if entry.LinkFiles != "" {
			owner = interfaceintent.OwnerNetworkd
		} else {
			owner = interfaceintent.OwnerExternal
		}
	}

	if owner == interfaceintent.OwnerMWAN {
		return owner, validateMWANOwner(entry)
	}

	if owner != interfaceintent.OwnerNetworkd && owner != interfaceintent.OwnerExternal {
		return owner, fmt.Errorf("interface %s: unknown owner %q", entry.Name, owner)
	}

	if owner == interfaceintent.OwnerExternal && entry.LinkFiles != "" {
		return owner, fmt.Errorf("interface %s: external owner cannot set link-files", entry.Name)
	}

	if owner == interfaceintent.OwnerNetworkd && entry.LinkFiles == "" {
		return owner, fmt.Errorf("interface %s: networkd owner requires link-files", entry.Name)
	}

	if entry.LeaseStore != "" && !filepath.IsAbs(entry.LeaseStore) {
		return owner, fmt.Errorf("interface %s: lease-store must be an absolute path", entry.Name)
	}

	if entry.LinkFiles == linkFilesHandAuthored &&
		(entry.Link != nil || entry.Networkd != nil || handAuthoredAddressing(entry)) {
		return owner, fmt.Errorf(
			"interface %s: hand-authored files cannot include a renderable link or address intent",
			entry.Name,
		)
	}

	if entry.LinkFiles != "" && entry.LinkFiles != linkFilesRendered &&
		entry.LinkFiles != linkFilesHandAuthored {
		return owner, fmt.Errorf("interface %s: invalid link-files %q", entry.Name, entry.LinkFiles)
	}

	if entry.LinkFiles == linkFilesRendered && entry.Link == nil {
		return owner, fmt.Errorf(
			"interface %s: rendered link requires a link container",
			entry.Name,
		)
	}

	return owner, nil
}

func validateMWANOwner(entry ifaceEntry) error {
	if err := validateOwnedLinkName(entry.Name); err != nil {
		slog.Warn("networkjson: invalid owned link name", "interface", entry.Name, "err", err)
		return fmt.Errorf("interface %q: %w", entry.Name, err)
	}

	if entry.ConnectionID == "" {
		return fmt.Errorf("interface %s: mwan owner requires connection-id", entry.Name)
	}

	if entry.Link == nil {
		return fmt.Errorf("interface %s: mwan owner requires a link", entry.Name)
	}

	if entry.LinkFiles != "" || entry.Networkd != nil || entry.LeaseStore != "" ||
		(entry.WAN == nil && entry.Steering != nil) {
		return fmt.Errorf(
			"interface %s: mwan owner cannot use networkd files, a lease store, or steering without a provider",
			entry.Name,
		)
	}

	if err := validateMWANFamily(
		entry.Name,
		string(FamilyIPv4),
		intentFamilyV4Wire(entry.IPv4),
		entry.WAN != nil,
	); err != nil {
		return err
	}

	if err := validateMWANFamily(
		entry.Name,
		string(FamilyIPv6),
		intentFamilyV6Wire(entry.IPv6),
		entry.WAN != nil,
	); err != nil {
		return err
	}

	if entry.WAN != nil {
		return validateMWANProvider(entry)
	}

	return nil
}

func validateMWANProvider(entry ifaceEntry) error {
	if entry.IPv4 == nil && entry.IPv6 == nil {
		return fmt.Errorf("interface %s: mwan provider requires an address family", entry.Name)
	}

	if entry.IPv4 != nil && entry.IPv4.Translation != nil {
		for _, mapping := range entry.IPv4.Translation.StaticMappings {
			if mapping.Delivery != string(interfaceintent.DeliveryLocal) &&
				mapping.Delivery != string(interfaceintent.DeliveryRouted) {
				return fmt.Errorf(
					"interface %s: mwan provider mapping %s requires delivery local or routed",
					entry.Name,
					mapping.External,
				)
			}
		}
	}

	return nil
}

func intentFamilyV4Wire(family *familyV4) *familyWire {
	if family == nil {
		return nil
	}

	return &family.familyWire
}

func intentFamilyV6Wire(family *familyV6) *familyWire {
	if family == nil {
		return nil
	}

	return &family.familyWire
}

func validateMWANFamily(name, family string, wire *familyWire, provider bool) error {
	if wire == nil {
		return nil
	}

	dhcpv4 := family == string(FamilyIPv4) && wire.DHCP != nil && *wire.DHCP
	raMetric := family == string(FamilyIPv6) && wire.Gateway == "" && wire.RouteMetric != nil
	if wire.Enabled != nil && !*wire.Enabled ||
		(wire.Translation != nil && !provider) ||
		wire.RouteMetric != nil && wire.Gateway == "" && !dhcpv4 && !raMetric {
		if family == string(FamilyIPv4) {
			return fmt.Errorf(
				"interface %s: mwan ipv4 supports local addresses, forwarding, DHCPv4, and an optional gateway or DHCP route metric only",
				name,
			)
		}

		return fmt.Errorf(
			"interface %s: mwan %s supports static local addresses and an optional gateway with route metric only",
			name,
			family,
		)
	}

	return nil
}

func validateMWANStaticFamily(name, family string, intent *interfaceintent.Family) error {
	if intent.Gateway.IsValid() && intent.Gateway.Is4() != (family == string(FamilyIPv4)) {
		return fmt.Errorf("interface %s: %s gateway has wrong address family", name, family)
	}

	for _, address := range intent.Addresses {
		if !address.Prefix.IsValid() || address.Prefix.Addr().Is4() != (family == string(
			FamilyIPv4,
		)) ||
			address.Prefix.Addr().IsMulticast() ||
			address.Prefix.Addr().IsUnspecified() {
			return fmt.Errorf(
				"interface %s: %s has invalid local address %s",
				name,
				family,
				address.Prefix,
			)
		}
	}

	return nil
}

func validateMWANConnection(entry ifaceEntry, connection interfaceintent.Connection) error {
	if entry.IPv4 != nil {
		if len(entry.IPv4.SourceAddresses) != 0 {
			return fmt.Errorf(
				"interface %s: mwan ipv4 does not support source addresses",
				entry.Name,
			)
		}

		if err := validateMWANDHCPv4(entry.Name, entry.IPv4); err != nil {
			return err
		}

		if err := validateMWANStaticFamily(
			entry.Name,
			string(FamilyIPv4),
			&connection.IPv4.Family,
		); err != nil {
			return err
		}
	}

	if entry.IPv6 != nil {
		return validateMWANIPv6(entry.Name, entry.IPv6, connection.IPv6)
	}

	return nil
}

func validateMWANIPv6(name string, wire *familyV6, intent *interfaceintent.IPv6) error {
	if len(wire.ForwardingAddresses) != 0 {
		return fmt.Errorf("interface %s: mwan ipv6 does not support forwarding addresses", name)
	}

	if wire.DHCP != nil && *wire.DHCP || wire.Delegation != nil || wire.DHCPv6 != nil {
		if err := ValidateOwnedDHCPv6(name, intent); err != nil {
			return err
		}
	}

	acceptsRA := wire.AcceptRA != nil && *wire.AcceptRA
	if !acceptsRA && (wire.AutoConf != nil && *wire.AutoConf ||
		wire.AcceptRADefaultRoute != nil && *wire.AcceptRADefaultRoute || wire.UseRADNS != nil && *wire.UseRADNS) {
		return fmt.Errorf(
			"interface %s: ipv6 autoconf, RA default route and RA DNS require accept-ra",
			name,
		)
	}

	if wire.RouteMetric != nil && wire.Gateway == "" {
		if !acceptsRA || wire.AcceptRADefaultRoute != nil && !*wire.AcceptRADefaultRoute {
			return fmt.Errorf(
				"interface %s: ipv6 RA route metric requires an accepted RA default route",
				name,
			)
		}

		if *wire.RouteMetric == 0 {
			return fmt.Errorf("interface %s: ipv6 RA route metric must be greater than zero", name)
		}
	}

	return validateMWANStaticFamily(name, string(FamilyIPv6), &intent.Family)
}

func validateMWANDHCPv4(name string, family *familyV4) error {
	dhcp := family.DHCP != nil && *family.DHCP
	routesEnabled := family.DHCPv4 == nil || family.DHCPv4.UseRoutes == nil ||
		*family.DHCPv4.UseRoutes
	if dhcp && family.Gateway != "" && routesEnabled {
		return fmt.Errorf(
			"interface %s: ipv4 cannot combine a static gateway with DHCP routes",
			name,
		)
	}

	if family.DHCPv4 != nil && !dhcp {
		return fmt.Errorf("interface %s: ipv4/dhcpv4 requires ipv4/dhcp true", name)
	}

	if family.DHCPv4 != nil {
		if _, err := DecodeDHCPv4ClientID(family.DHCPv4.ClientID); err != nil {
			slog.Error("networkjson: DHCPv4 client ID invalid", "interface", name, "err", err)
			return fmt.Errorf("interface %s: ipv4/dhcpv4/client-id: %w", name, err)
		}
	}

	if dhcp && routesEnabled && family.RouteMetric == nil {
		return fmt.Errorf("interface %s: ipv4 DHCP routes require route-metric", name)
	}

	return nil
}

// DecodeDHCPv4ClientID decodes the complete DHCP option 61 payload.
func DecodeDHCPv4ClientID(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}

	if !strings.HasPrefix(value, "hex:") {
		return nil, fmt.Errorf("expected hex: followed by 2 to 255 bytes")
	}

	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "hex:"))
	if err != nil || len(decoded) < 2 || len(decoded) > 255 {
		return nil, fmt.Errorf("expected hex: followed by 2 to 255 bytes")
	}

	return decoded, nil
}

func validateOwnedLinkName(name string) error {
	if len(name) == 0 || len(name) > 15 {
		return fmt.Errorf("mwan link name must contain 1 to 15 ASCII characters")
	}

	for index := range len(name) {
		char := name[index]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.' {
			continue
		}

		return fmt.Errorf("mwan link name contains an invalid character")
	}

	if name == "." || name == ".." {
		return fmt.Errorf("mwan link name cannot be %q", name)
	}

	return nil
}

func validateRenderedConnection(entry ifaceEntry, connection interfaceintent.Connection) error {
	if entry.LinkFiles != linkFilesRendered {
		return nil
	}

	if entry.WAN != nil {
		if connection.IPv4 != nil && connection.IPv4.DHCP == nil {
			return fmt.Errorf("interface %s: ipv4/dhcp is required", entry.Name)
		}

		if connection.IPv6 != nil && connection.IPv6.DHCP == nil {
			return fmt.Errorf("interface %s: ipv6/dhcp is required", entry.Name)
		}
	}

	if err := unsupportedNetworkdIntent(connection); err != nil {
		return err
	}

	if err := networkd.Validate(connection); err != nil {
		slog.Error(
			"networkjson: networkd configuration invalid",
			"interface",
			entry.Name,
			"err",
			err,
		)

		return fmt.Errorf("interface %s: %w", entry.Name, err)
	}

	return nil
}

func unsupportedNetworkdIntent(connection interfaceintent.Connection) error {
	if connection.Enabled != nil && !*connection.Enabled {
		return fmt.Errorf(
			"interface %s: networkd renderer does not support disabled interfaces",
			connection.Name,
		)
	}

	if connection.Link != nil && connection.Link.Kind == interfaceintent.KindBridge {
		return fmt.Errorf(
			"interface %s: networkd renderer does not support bridge creation",
			connection.Name,
		)
	}

	if connection.LeaseStore != "" {
		return fmt.Errorf(
			"interface %s: networkd owner does not support lease-store",
			connection.Name,
		)
	}

	if connection.IPv4 != nil {
		ipv4 := connection.IPv4
		if ipv4.Enabled != nil && !*ipv4.Enabled {
			return fmt.Errorf(
				"interface %s: networkd renderer does not support disabled ipv4",
				connection.Name,
			)
		}

		if ipv4.DHCPv4 != nil {
			return fmt.Errorf(
				"interface %s: networkd renderer does not support typed ipv4 client options",
				connection.Name,
			)
		}
	}

	if connection.IPv6 != nil {
		ipv6 := connection.IPv6
		if ipv6.Enabled != nil && !*ipv6.Enabled {
			return fmt.Errorf(
				"interface %s: networkd renderer does not support disabled ipv6",
				connection.Name,
			)
		}

		if ipv6.Delegation != nil && ipv6.Delegation.IAID != nil {
			return fmt.Errorf(
				"interface %s: networkd renderer does not support delegation iaid",
				connection.Name,
			)
		}

		if ipv6.DHCPv6 != nil || ipv6.AutoConf != nil || ipv6.AcceptRADefaultRoute != nil ||
			ipv6.UseRADNS != nil ||
			len(ipv6.ForwardingAddresses) > 0 {
			return fmt.Errorf(
				"interface %s: networkd renderer does not support typed ipv6 client, router, or forwarding options",
				connection.Name,
			)
		}
	}

	return nil
}

func buildIntentLink(entry ifaceEntry) (*interfaceintent.Link, error) {
	wire := entry.Link
	link := &interfaceintent.Link{
		Kind:            interfaceintent.KindPhysical,
		Match:           interfaceintent.Match{Driver: "", HardwareAddress: ""},
		HardwareAddress: wire.HardwareAddress,
		MTU:             wire.MTU,
		VLAN:            nil,
		BridgeMaster:    wire.BridgeMaster,
		Tunnel:          nil,
	}
	if wire.Tunnel != nil {
		tunnel, err := buildIntentTunnel(entry)
		if err != nil {
			return nil, err
		}

		link.Kind, link.Tunnel = interfaceintent.KindTunnel, tunnel

		return link, nil
	}

	if entry.Type == "iana-if-type:bridge" {
		link.Kind = interfaceintent.KindBridge
	}

	if wire.Match != nil {
		link.Match = interfaceintent.Match{
			Driver:          wire.Match.Driver,
			HardwareAddress: wire.Match.HardwareAddress,
		}
	}

	identities := 0
	if link.Match.Driver != "" {
		identities++
	}

	if link.Match.HardwareAddress != "" {
		identities++
	}

	if wire.VLAN != nil {
		if link.Kind == interfaceintent.KindBridge &&
			entry.Owner == string(interfaceintent.OwnerMWAN) {
			return nil, fmt.Errorf("interface %s: bridge link cannot include a vlan", entry.Name)
		}

		identities++
		if wire.VLAN.ID == nil {
			return nil, fmt.Errorf("interface %s: link/vlan/id is required", entry.Name)
		}

		link.Kind = interfaceintent.KindVLAN
		link.VLAN = &interfaceintent.VLAN{Parent: wire.VLAN.Parent, ID: *wire.VLAN.ID}
	}

	if entry.LinkFiles == linkFilesRendered || entry.Owner == string(interfaceintent.OwnerMWAN) {
		if link.Kind == interfaceintent.KindBridge {
			if identities != 0 {
				return nil, fmt.Errorf(
					"interface %s: bridge link cannot include a device match or vlan",
					entry.Name,
				)
			}
		} else if identities != 1 {
			return nil, fmt.Errorf(
				"interface %s: link requires exactly one identity (driver, hardware-address, or vlan), got %d",
				entry.Name,
				identities,
			)
		}
	}

	if entry.Owner == string(interfaceintent.OwnerMWAN) &&
		link.Kind == interfaceintent.KindPhysical &&
		link.Match.HardwareAddress == "" {
		return nil, fmt.Errorf(
			"interface %s: mwan physical link requires a hardware-address match",
			entry.Name,
		)
	}

	return link, nil
}

func buildIntentFamily(
	name string,
	family string,
	wire familyWire,
) (interfaceintent.Family, error) {
	built := interfaceintent.Family{
		Enabled:       wire.Enabled,
		Forwarding:    wire.Forwarding,
		Addresses:     nil,
		DHCP:          wire.DHCP,
		Gateway:       netip.Addr{},
		RouteMetric:   wire.RouteMetric,
		DNS:           nil,
		SearchDomains: nil,
		Routes:        nil,
	}
	for _, address := range wire.Address {
		ip, err := parseAddress("interface "+name, family+"/address", address.IP)
		if err != nil {
			return built, err
		}

		if address.PrefixLength == nil {
			return built, fmt.Errorf(
				"interface %s: %s/address %s has no prefix-length",
				name,
				family,
				address.IP,
			)
		}

		prefix := netip.PrefixFrom(ip, int(*address.PrefixLength))
		built.Addresses = append(
			built.Addresses,
			interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeLocal},
		)
	}

	if wire.Gateway != "" {
		gateway, err := parseAddress("interface "+name, family+"/gateway", wire.Gateway)
		if err != nil {
			return built, err
		}

		built.Gateway = gateway
	}

	routes, err := buildIntentRoutes(name, family, wire.Routes, built.Gateway.IsValid())
	if err != nil {
		return built, err
	}

	built.Routes = routes
	if wire.Resolver != nil {
		for _, raw := range wire.Resolver.DNSServers {
			server, err := parseAddress("interface "+name, family+"/resolver/dns", raw)
			if err != nil {
				return built, err
			}

			built.DNS = append(built.DNS, server)
		}

		built.SearchDomains = append(built.SearchDomains, wire.Resolver.SearchDomains...)
	}

	return built, nil
}

func buildIntentV4(name string, wire familyV4) (*interfaceintent.IPv4, error) {
	shared, err := buildIntentFamily(name, string(FamilyIPv4), wire.familyWire)
	if err != nil {
		return nil, err
	}

	built := &interfaceintent.IPv4{Family: shared, SourceAddresses: nil, DHCPv4: nil}
	for _, raw := range wire.SourceAddresses {
		address, err := parseAddress("interface "+name, "ipv4/source-addresses", raw)
		if err != nil {
			return nil, err
		}

		built.SourceAddresses = append(built.SourceAddresses, address)
	}

	if wire.DHCPv4 != nil {
		built.DHCPv4 = &interfaceintent.DHCPv4{
			ClientID:  wire.DHCPv4.ClientID,
			UseDNS:    wire.DHCPv4.UseDNS,
			UseRoutes: wire.DHCPv4.UseRoutes,
		}
	}

	return built, nil
}

func buildIntentV6(name string, wire familyV6) (*interfaceintent.IPv6, error) {
	shared, err := buildIntentFamily(name, string(FamilyIPv6), wire.familyWire)
	if err != nil {
		return nil, err
	}

	built := &interfaceintent.IPv6{
		Family:               shared,
		AcceptRA:             wire.AcceptRA,
		AutoConf:             wire.AutoConf,
		AcceptRADefaultRoute: wire.AcceptRADefaultRoute,
		UseRADNS:             wire.UseRADNS,
		Delegation:           nil,
		DHCPv6:               nil,
		ForwardingAddresses:  nil,
	}
	if wire.Delegation != nil {
		delegation := &interfaceintent.Delegation{
			Hint:                  netip.Prefix{},
			DUIDType:              wire.Delegation.DUIDType,
			DUID:                  wire.Delegation.DUID,
			WithoutRA:             wire.Delegation.WithoutRA,
			UseDelegatedPrefix:    wire.Delegation.UseDelegatedPrefix,
			RouterLifetimeSeconds: wire.Delegation.RouterLifetimeSeconds,
			IAID:                  wire.Delegation.IAID,
		}
		if wire.Delegation.Hint != "" {
			hint, err := netip.ParsePrefix(wire.Delegation.Hint)
			if err != nil {
				slog.Error("networkjson: delegation hint invalid", "interface", name, "err", err)
				return nil, fmt.Errorf("interface %s: ipv6/delegation/hint: %w", name, err)
			}

			delegation.Hint = hint
		}

		built.Delegation = delegation
	}

	if wire.DHCPv6 != nil {
		client := wire.DHCPv6
		built.DHCPv6 = &interfaceintent.DHCPv6{
			DUIDType:       client.DUIDType,
			DUID:           client.DUID,
			IANAIAID:       client.IANAIAID,
			IAPDIAID:       client.IAPDIAID,
			RequestAddress: client.RequestAddress,
			RequestPrefix:  client.RequestPrefix,
			WithoutRA:      client.WithoutRA,
			UseDNS:         client.UseDNS,
		}
		if built.Delegation != nil {
			if err := validateDelegationClient(name, *built.Delegation, *built.DHCPv6); err != nil {
				return nil, err
			}
		}
	}

	for _, raw := range wire.ForwardingAddresses {
		address, err := parseAddress("interface "+name, "ipv6/forwarding-address", raw.Address)
		if err != nil {
			return nil, err
		}

		built.ForwardingAddresses = append(
			built.ForwardingAddresses,
			interfaceintent.ForwardingAddress{
				Address:  address,
				Delivery: interfaceintent.AddressDelivery(raw.Delivery),
			},
		)
	}

	return built, nil
}

func validateDelegationClient(
	name string,
	delegation interfaceintent.Delegation,
	client interfaceintent.DHCPv6,
) error {
	if delegation.DUIDType != "" && client.DUIDType != "" &&
		delegation.DUIDType != client.DUIDType {
		return fmt.Errorf("interface %s: delegation and dhcpv6-client duid-type conflict", name)
	}

	if delegation.DUID != "" && client.DUID != "" && delegation.DUID != client.DUID {
		return fmt.Errorf("interface %s: delegation and dhcpv6-client duid conflict", name)
	}

	if delegation.WithoutRA != "" && client.WithoutRA != "" &&
		delegation.WithoutRA != client.WithoutRA {
		return fmt.Errorf("interface %s: delegation and dhcpv6-client without-ra conflict", name)
	}

	if delegation.IAID != nil && client.IAPDIAID != nil && *delegation.IAID != *client.IAPDIAID {
		return fmt.Errorf("interface %s: delegation and dhcpv6-client ia-pd-iaid conflict", name)
	}

	return nil
}

func buildIntentFiles(container networkdContainer) ([]interfaceintent.UnitFile, error) {
	files := make([]interfaceintent.UnitFile, 0, len(container.Files))
	for _, file := range container.Files {
		built := interfaceintent.UnitFile{Kind: file.Kind, Sections: nil}
		for _, section := range file.Sections {
			if section.Index == nil {
				return nil, fmt.Errorf("networkd file %s has a section with no index", file.Kind)
			}

			builtSection := interfaceintent.UnitSection{
				Index:   *section.Index,
				Name:    section.Name,
				Entries: nil,
			}
			for _, entry := range section.Entries {
				if entry.Index == nil {
					return nil, fmt.Errorf(
						"networkd file %s section %s has an entry with no index",
						file.Kind,
						section.Name,
					)
				}

				builtSection.Entries = append(
					builtSection.Entries,
					interfaceintent.UnitEntry{
						Index: *entry.Index,
						Key:   entry.Key,
						Value: entry.Value,
					},
				)
			}

			built.Sections = append(built.Sections, builtSection)
		}

		files = append(files, built)
	}

	return files, nil
}

func staticV4Source(connection interfaceintent.Connection) string {
	if connection.IPv4 == nil || len(connection.IPv4.Addresses) == 0 {
		return ""
	}

	return connection.IPv4.Addresses[0].Prefix.Addr().String()
}
