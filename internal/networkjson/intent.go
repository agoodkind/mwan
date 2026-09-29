package networkjson

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkd"
)

func buildConnection(entry ifaceEntry, id connectionid.ID) (interfaceintent.Connection, error) {
	connection := interfaceintent.Connection{
		ID: id, Name: entry.Name, Type: entry.Type, Enabled: entry.Enabled,
		Roles: 0, Owner: interfaceintent.Owner(entry.Owner), Link: nil,
		IPv4: nil, IPv6: nil, LeaseStore: entry.LeaseStore, Networkd: nil,
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
	if entry.LinkFiles == linkFilesHandAuthored && (entry.Link != nil || entry.Networkd != nil || handAuthoredAddressing(entry)) {
		return owner, fmt.Errorf("interface %s: hand-authored files cannot include a renderable link or address intent", entry.Name)
	}
	if entry.LinkFiles != "" && entry.LinkFiles != linkFilesRendered && entry.LinkFiles != linkFilesHandAuthored {
		return owner, fmt.Errorf("interface %s: invalid link-files %q", entry.Name, entry.LinkFiles)
	}
	if entry.LinkFiles == linkFilesRendered && entry.Link == nil {
		return owner, fmt.Errorf("interface %s: rendered link requires a link container", entry.Name)
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
		return fmt.Errorf("interface %s: mwan owner cannot use networkd files, a lease store, or steering without a provider", entry.Name)
	}
	if err := validateMWANFamily(entry.Name, "ipv4", intentFamilyV4Wire(entry.IPv4), entry.WAN != nil); err != nil {
		return err
	}
	if err := validateMWANFamily(entry.Name, "ipv6", intentFamilyV6Wire(entry.IPv6), entry.WAN != nil); err != nil {
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
			if mapping.Delivery != string(interfaceintent.DeliveryLocal) && mapping.Delivery != string(interfaceintent.DeliveryRouted) {
				return fmt.Errorf("interface %s: mwan provider mapping %s requires delivery local or routed", entry.Name, mapping.External)
			}
		}
	}
	if entry.IPv6 != nil && entry.IPv6.Translation != nil && entry.IPv6.Translation.NPT != nil &&
		entry.IPv6.Translation.NPT.ExternalSource != config.PrefixConfigured {
		return fmt.Errorf("interface %s: mwan provider supports configured NPT external prefixes until delegation acquisition is available", entry.Name)
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
	dhcpv4 := family == "ipv4" && wire.DHCP != nil && *wire.DHCP
	if wire.Enabled != nil && !*wire.Enabled || wire.Forwarding != nil ||
		(family == "ipv6" && wire.DHCP != nil) || wire.Resolver != nil ||
		(wire.Translation != nil && !provider) ||
		wire.RouteMetric != nil && wire.Gateway == "" && !dhcpv4 {
		if family == "ipv4" {
			return fmt.Errorf("interface %s: mwan ipv4 supports local addresses, DHCPv4, and an optional gateway or DHCP route metric only", name)
		}
		return fmt.Errorf("interface %s: mwan %s supports static local addresses and an optional gateway with route metric only", name, family)
	}
	return nil
}

func validateMWANStaticFamily(name, family string, intent *interfaceintent.Family) error {
	if intent.Gateway.IsValid() && intent.Gateway.Is4() != (family == "ipv4") {
		return fmt.Errorf("interface %s: %s gateway has wrong address family", name, family)
	}
	for _, address := range intent.Addresses {
		if !address.Prefix.IsValid() || address.Prefix.Addr().Is4() != (family == "ipv4") ||
			address.Prefix.Addr().IsMulticast() || address.Prefix.Addr().IsUnspecified() {
			return fmt.Errorf("interface %s: %s has invalid local address %s", name, family, address.Prefix)
		}
	}
	return nil
}

func validateMWANConnection(entry ifaceEntry, connection interfaceintent.Connection) error {
	if entry.IPv4 != nil {
		if len(entry.IPv4.SourceAddresses) != 0 {
			return fmt.Errorf("interface %s: mwan ipv4 does not support source addresses", entry.Name)
		}
		if err := validateMWANDHCPv4(entry.Name, entry.IPv4); err != nil {
			return err
		}
		if err := validateMWANStaticFamily(entry.Name, "ipv4", &connection.IPv4.Family); err != nil {
			return err
		}
	}
	if entry.IPv6 != nil {
		if entry.IPv6.AcceptRA != nil || entry.IPv6.AutoConf != nil || entry.IPv6.AcceptRADefaultRoute != nil ||
			entry.IPv6.UseRADNS != nil || entry.IPv6.Delegation != nil || entry.IPv6.DHCPv6 != nil ||
			len(entry.IPv6.ForwardingAddresses) != 0 {
			return fmt.Errorf("interface %s: mwan ipv6 does not support RA, DHCP, delegation, or forwarding addresses", entry.Name)
		}
		return validateMWANStaticFamily(entry.Name, "ipv6", &connection.IPv6.Family)
	}
	return nil
}

func validateMWANDHCPv4(name string, family *familyV4) error {
	dhcp := family.DHCP != nil && *family.DHCP
	routesEnabled := family.DHCPv4 == nil || family.DHCPv4.UseRoutes == nil || *family.DHCPv4.UseRoutes
	if dhcp && family.Gateway != "" && routesEnabled {
		return fmt.Errorf("interface %s: ipv4 cannot combine a static gateway with DHCP routes", name)
	}
	if family.DHCPv4 != nil && !dhcp {
		return fmt.Errorf("interface %s: ipv4/dhcpv4 requires ipv4/dhcp true", name)
	}
	if family.DHCPv4 != nil {
		if _, err := DecodeDHCPv4ClientID(family.DHCPv4.ClientID); err != nil {
			slog.Error("networkjson: DHCPv4 client ID invalid", "interface", name, "err", err)
			return fmt.Errorf("interface %s: ipv4/dhcpv4/client-id: %w", name, err)
		}
		if family.DHCPv4.UseDNS != nil && *family.DHCPv4.UseDNS {
			return fmt.Errorf("interface %s: ipv4/dhcpv4/use-dns requires resolver ownership", name)
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
		slog.Error("networkjson: networkd configuration invalid", "interface", entry.Name, "err", err)
		return fmt.Errorf("interface %s: %w", entry.Name, err)
	}
	return nil
}

func unsupportedNetworkdIntent(connection interfaceintent.Connection) error {
	if connection.Enabled != nil && !*connection.Enabled {
		return fmt.Errorf("interface %s: networkd renderer does not support disabled interfaces", connection.Name)
	}
	if connection.Link != nil && connection.Link.Kind == interfaceintent.KindBridge {
		return fmt.Errorf("interface %s: networkd renderer does not support bridge creation", connection.Name)
	}
	if connection.LeaseStore != "" {
		return fmt.Errorf("interface %s: networkd owner does not support lease-store", connection.Name)
	}
	if connection.IPv4 != nil {
		ipv4 := connection.IPv4
		if ipv4.Enabled != nil && !*ipv4.Enabled {
			return fmt.Errorf("interface %s: networkd renderer does not support disabled ipv4", connection.Name)
		}
		if ipv4.DHCPv4 != nil || len(ipv4.DNS) > 0 || len(ipv4.SearchDomains) > 0 {
			return fmt.Errorf("interface %s: networkd renderer does not support typed ipv4 client or resolver options", connection.Name)
		}
	}
	if connection.IPv6 != nil {
		ipv6 := connection.IPv6
		if ipv6.Enabled != nil && !*ipv6.Enabled {
			return fmt.Errorf("interface %s: networkd renderer does not support disabled ipv6", connection.Name)
		}
		if ipv6.Delegation != nil && ipv6.Delegation.IAID != nil {
			return fmt.Errorf("interface %s: networkd renderer does not support delegation iaid", connection.Name)
		}
		if ipv6.DHCPv6 != nil || ipv6.AutoConf != nil || ipv6.AcceptRADefaultRoute != nil || ipv6.UseRADNS != nil || len(ipv6.DNS) > 0 || len(ipv6.SearchDomains) > 0 || len(ipv6.ForwardingAddresses) > 0 {
			return fmt.Errorf("interface %s: networkd renderer does not support typed ipv6 client, router, resolver, or forwarding options", connection.Name)
		}
	}
	return nil
}

func buildIntentLink(entry ifaceEntry) (*interfaceintent.Link, error) {
	wire := entry.Link
	link := &interfaceintent.Link{
		Kind: interfaceintent.KindPhysical, Match: interfaceintent.Match{Driver: "", HardwareAddress: ""},
		HardwareAddress: wire.HardwareAddress, MTU: wire.MTU,
		VLAN: nil, BridgeMaster: wire.BridgeMaster,
	}
	if entry.Type == "iana-if-type:bridge" {
		link.Kind = interfaceintent.KindBridge
	}
	if wire.Match != nil {
		link.Match = interfaceintent.Match{Driver: wire.Match.Driver, HardwareAddress: wire.Match.HardwareAddress}
	}
	identities := 0
	if link.Match.Driver != "" {
		identities++
	}
	if link.Match.HardwareAddress != "" {
		identities++
	}
	if wire.VLAN != nil {
		if link.Kind == interfaceintent.KindBridge && entry.Owner == string(interfaceintent.OwnerMWAN) {
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
				return nil, fmt.Errorf("interface %s: bridge link cannot include a device match or vlan", entry.Name)
			}
		} else if identities != 1 {
			return nil, fmt.Errorf("interface %s: link requires exactly one identity (driver, hardware-address, or vlan), got %d", entry.Name, identities)
		}
	}
	if entry.Owner == string(interfaceintent.OwnerMWAN) && link.Kind == interfaceintent.KindPhysical &&
		link.Match.HardwareAddress == "" {
		return nil, fmt.Errorf("interface %s: mwan physical link requires a hardware-address match", entry.Name)
	}
	return link, nil
}

func buildIntentFamily(name string, family string, wire familyWire) (interfaceintent.Family, error) {
	built := interfaceintent.Family{
		Enabled: wire.Enabled, Forwarding: wire.Forwarding, Addresses: nil,
		DHCP: wire.DHCP, Gateway: netip.Addr{}, RouteMetric: wire.RouteMetric,
		DNS: nil, SearchDomains: nil,
	}
	for _, address := range wire.Address {
		ip, err := parseAddress("interface "+name, family+"/address", address.IP)
		if err != nil {
			return built, err
		}
		if address.PrefixLength == nil {
			return built, fmt.Errorf("interface %s: %s/address %s has no prefix-length", name, family, address.IP)
		}
		prefix := netip.PrefixFrom(ip, int(*address.PrefixLength))
		built.Addresses = append(built.Addresses, interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeLocal})
	}
	if wire.Gateway != "" {
		gateway, err := parseAddress("interface "+name, family+"/gateway", wire.Gateway)
		if err != nil {
			return built, err
		}
		built.Gateway = gateway
	}
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
	shared, err := buildIntentFamily(name, "ipv4", wire.familyWire)
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
			ClientID: wire.DHCPv4.ClientID, UseDNS: wire.DHCPv4.UseDNS, UseRoutes: wire.DHCPv4.UseRoutes,
		}
	}
	return built, nil
}

func buildIntentV6(name string, wire familyV6) (*interfaceintent.IPv6, error) {
	shared, err := buildIntentFamily(name, "ipv6", wire.familyWire)
	if err != nil {
		return nil, err
	}
	built := &interfaceintent.IPv6{
		Family: shared, AcceptRA: wire.AcceptRA, AutoConf: wire.AutoConf,
		AcceptRADefaultRoute: wire.AcceptRADefaultRoute, UseRADNS: wire.UseRADNS,
		Delegation: nil, DHCPv6: nil, ForwardingAddresses: nil,
	}
	if wire.Delegation != nil {
		delegation := &interfaceintent.Delegation{
			Hint: netip.Prefix{}, DUIDType: wire.Delegation.DUIDType,
			DUID: wire.Delegation.DUID, WithoutRA: wire.Delegation.WithoutRA,
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
			DUIDType: client.DUIDType, DUID: client.DUID, IANAIAID: client.IANAIAID,
			IAPDIAID: client.IAPDIAID, RequestAddress: client.RequestAddress,
			RequestPrefix: client.RequestPrefix, WithoutRA: client.WithoutRA, UseDNS: client.UseDNS,
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
		built.ForwardingAddresses = append(built.ForwardingAddresses,
			interfaceintent.ForwardingAddress{Address: address, Delivery: interfaceintent.AddressDelivery(raw.Delivery)})
	}
	return built, nil
}

func validateDelegationClient(name string, delegation interfaceintent.Delegation, client interfaceintent.DHCPv6) error {
	if delegation.DUIDType != "" && client.DUIDType != "" && delegation.DUIDType != client.DUIDType {
		return fmt.Errorf("interface %s: delegation and dhcpv6-client duid-type conflict", name)
	}
	if delegation.DUID != "" && client.DUID != "" && delegation.DUID != client.DUID {
		return fmt.Errorf("interface %s: delegation and dhcpv6-client duid conflict", name)
	}
	if delegation.WithoutRA != "" && client.WithoutRA != "" && delegation.WithoutRA != client.WithoutRA {
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
			builtSection := interfaceintent.UnitSection{Index: *section.Index, Name: section.Name, Entries: nil}
			for _, entry := range section.Entries {
				if entry.Index == nil {
					return nil, fmt.Errorf("networkd file %s section %s has an entry with no index", file.Kind, section.Name)
				}
				builtSection.Entries = append(builtSection.Entries,
					interfaceintent.UnitEntry{Index: *entry.Index, Key: entry.Key, Value: entry.Value})
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

func compileConnections(entries []ifaceEntry, ids map[string]connectionid.ID, internal string, firewall *firewallWire) ([]interfaceintent.Connection, []Rejection, error) {
	connections := make([]interfaceintent.Connection, 0, len(entries))
	var rejected []Rejection
	declared := make(map[string]bool, len(entries))
	for _, entry := range entries {
		declared[entry.Name] = true
	}
	if internal != "" && !declared[internal] {
		return nil, nil, fmt.Errorf("internal interface %s is not declared", internal)
	}
	if firewall != nil && firewall.ManagementInterface != "" && !declared[firewall.ManagementInterface] {
		return nil, nil, fmt.Errorf("management interface %s is not declared", firewall.ManagementInterface)
	}
	for _, entry := range entries {
		if entry.Owner == string(interfaceintent.OwnerMWAN) {
			if entry.Name == internal || (firewall != nil && entry.Name == firewall.ManagementInterface) {
				return nil, nil, fmt.Errorf("interface %s: mwan owner cannot manage internal or firewall-management interfaces", entry.Name)
			}
		}
		connection, err := buildConnection(entry, ids[entry.Name])
		if err != nil {
			if entry.WAN == nil || entry.Owner == string(interfaceintent.OwnerMWAN) {
				return nil, nil, err
			}
			rejected = append(rejected, rejectEntry(entry, err))
			continue
		}
		if entry.Name == internal {
			connection.Roles |= interfaceintent.RoleInternal
		}
		if firewall != nil && entry.Name == firewall.ManagementInterface {
			connection.Roles |= interfaceintent.RoleManagement
		}
		connections = append(connections, connection)
	}
	if err := validateConnectionDependencies(connections, declared, internal, firewall); err != nil {
		return nil, nil, err
	}
	return connections, rejected, nil
}

func validateConnectionDependencies(connections []interfaceintent.Connection, declared map[string]bool, internal string, firewall *firewallWire) error {
	indexes := make(map[string]int, len(connections))
	for i, connection := range connections {
		indexes[connection.Name] = i
	}
	if err := validateConnectionDevices(connections); err != nil {
		return err
	}
	if err := validateRequiredRoles(indexes, internal, firewall); err != nil {
		return err
	}
	for _, connection := range connections {
		if connection.Link == nil {
			continue
		}
		for _, parent := range []string{vlanParent(connection.Link), connection.Link.BridgeMaster} {
			if parent == "" {
				continue
			}
			if !declared[parent] {
				return fmt.Errorf("interface %s: parent %s is not declared", connection.Name, parent)
			}
			index, accepted := indexes[parent]
			if !accepted {
				return fmt.Errorf("interface %s: parent %s was rejected", connection.Name, parent)
			}
			connections[index].Roles |= interfaceintent.RoleParent
		}
	}
	state := make(map[string]uint8, len(connections))
	for _, connection := range connections {
		if err := checkParentCycle(connection.Name, connections, indexes, state); err != nil {
			return err
		}
	}
	for _, connection := range connections {
		if connection.Link == nil || connection.Link.VLAN == nil || connection.Owner != interfaceintent.OwnerNetworkd {
			continue
		}
		parent := connections[indexes[connection.Link.VLAN.Parent]]
		if parent.Owner != interfaceintent.OwnerNetworkd || parent.Link == nil {
			return fmt.Errorf("interface %s: vlan parent %s needs a rendered networkd link", connection.Name, parent.Name)
		}
	}
	return nil
}

func validateRequiredRoles(indexes map[string]int, internal string, firewall *firewallWire) error {
	if _, ok := indexes[internal]; !ok {
		return fmt.Errorf("internal interface %s was rejected", internal)
	}
	if firewall != nil && firewall.ManagementInterface != "" {
		if _, ok := indexes[firewall.ManagementInterface]; !ok {
			return fmt.Errorf("management interface %s was rejected", firewall.ManagementInterface)
		}
	}
	return nil
}

func validateConnectionDevices(connections []interfaceintent.Connection) error {
	devices := make(map[string]string, len(connections))
	for _, connection := range connections {
		if connection.Link == nil {
			continue
		}
		keys := make([]string, 0, 2)
		if connection.Link.Match.Driver != "" {
			keys = append(keys, "driver:"+connection.Link.Match.Driver)
		}
		if connection.Link.Match.HardwareAddress != "" {
			keys = append(keys, "mac:"+strings.ToLower(connection.Link.Match.HardwareAddress))
		}
		for _, key := range keys {
			if prior, taken := devices[key]; taken {
				return fmt.Errorf("interfaces %s and %s match the same device %q", prior, connection.Name, key)
			}
			devices[key] = connection.Name
		}
	}
	return nil
}

func vlanParent(link *interfaceintent.Link) string {
	if link.VLAN == nil {
		return ""
	}
	return link.VLAN.Parent
}

func validateActiveDependencies(connections []interfaceintent.Connection) error {
	available := make(map[string]bool, len(connections))
	for _, connection := range connections {
		available[connection.Name] = true
	}
	for _, connection := range connections {
		if connection.Link == nil {
			continue
		}
		for _, parent := range []string{vlanParent(connection.Link), connection.Link.BridgeMaster} {
			if parent != "" && !available[parent] {
				return fmt.Errorf("interface %s requires rejected parent %s", connection.Name, parent)
			}
		}
	}
	return nil
}

func checkParentCycle(name string, connections []interfaceintent.Connection, indexes map[string]int, state map[string]uint8) error {
	if state[name] == 2 {
		return nil
	}
	if state[name] == 1 {
		return fmt.Errorf("interface %s: parent cycle", name)
	}
	state[name] = 1
	connection := connections[indexes[name]]
	if connection.Link != nil {
		for _, parent := range []string{vlanParent(connection.Link), connection.Link.BridgeMaster} {
			if parent == "" {
				continue
			}
			if err := checkParentCycle(parent, connections, indexes, state); err != nil {
				return err
			}
		}
	}
	state[name] = 2
	return nil
}

func compileClaims(connections []interfaceintent.Connection, providers map[string]config.IfMgrWANEntry) ([]interfaceintent.Claim, error) {
	var claims []interfaceintent.Claim
	owners := make(map[string]interfaceintent.Claim)
	addressOwners := make(map[string]interfaceintent.Claim)
	names := make(map[connectionid.ID]string, len(connections))
	for _, connection := range connections {
		names[connection.ID] = connection.Name
	}
	add := func(claim interfaceintent.Claim) error {
		key := string(claim.Kind) + "/" + claim.Family + "/" + claim.Key
		if previous, taken := owners[key]; taken {
			return fmt.Errorf("resource %s has writers %s and %s", key, previous.Writer, claim.Writer)
		}
		addressKey := claimedAddressKey(claim, names[claim.ConnectionID])
		if addressKey != "" {
			if previous, taken := addressOwners[addressKey]; taken {
				return fmt.Errorf("address %s has writers %s and %s", addressKey, previous.Writer, claim.Writer)
			}
			addressOwners[addressKey] = claim
		}
		owners[key] = claim
		claims = append(claims, claim)
		return nil
	}
	for _, connection := range connections {
		if err := claimConnectionResources(connection, add); err != nil {
			return nil, err
		}
		if provider, ok := providers[connection.ID.String()]; ok {
			if err := claimProviderResources(connection, provider, add); err != nil {
				return nil, err
			}
		}
	}
	return claims, nil
}

func claimedAddressKey(claim interfaceintent.Claim, name string) string {
	if claim.Kind == interfaceintent.ResourceStaticAddress {
		prefix, err := netip.ParsePrefix(strings.TrimPrefix(claim.Key, name+"/"))
		if err != nil {
			return ""
		}
		return claim.Family + "/" + name + "/" + prefix.String()
	}
	if claim.Kind == interfaceintent.ResourceMappedAddress {
		return claim.Family + "/" + name + "/" + claim.Key
	}
	return ""
}

func claimConnectionResources(connection interfaceintent.Connection, add func(interfaceintent.Claim) error) error {
	writer := interfaceintent.WriterExternal
	switch connection.Owner {
	case interfaceintent.OwnerExternal:
	case interfaceintent.OwnerNetworkd:
		writer = interfaceintent.WriterNetworkd
	case interfaceintent.OwnerMWAN:
		writer = interfaceintent.WriterMWANLink
	}
	if connection.Link != nil {
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceLink, Family: "", Key: connection.Name, Writer: writer}); err != nil {
			return err
		}
	}
	for _, family := range []struct {
		name   string
		config *interfaceintent.Family
	}{
		{name: "ipv4", config: intentFamilyV4(connection.IPv4)},
		{name: "ipv6", config: intentFamilyV6(connection.IPv6)},
	} {
		if family.config == nil {
			continue
		}
		if err := claimFamilyResources(connection, family.name, *family.config, writer, add); err != nil {
			return err
		}
	}
	if connection.IPv6 != nil && ((connection.IPv6.AcceptRA != nil && *connection.IPv6.AcceptRA) || (connection.IPv6.AutoConf != nil && *connection.IPv6.AutoConf)) {
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceRASLAAC, Family: "ipv6", Key: connection.Name, Writer: interfaceintent.WriterKernel}); err != nil {
			return err
		}
	}
	return nil
}

func claimFamilyResources(connection interfaceintent.Connection, name string, family interfaceintent.Family, writer interfaceintent.Writer, add func(interfaceintent.Claim) error) error {
	addressWriter := writer
	routeWriter := writer
	if connection.Owner == interfaceintent.OwnerMWAN {
		addressWriter = interfaceintent.WriterMWANAddress
		routeWriter = interfaceintent.WriterMWANRoute
	}
	for _, address := range family.Addresses {
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceStaticAddress, Family: name, Key: connection.Name + "/" + address.Prefix.String(), Writer: addressWriter}); err != nil {
			return err
		}
	}
	if family.Gateway.IsValid() {
		key := connection.Name + "/" + family.Gateway.String()
		if connection.Owner == interfaceintent.OwnerMWAN {
			metric := uint32(0)
			if family.RouteMetric != nil {
				metric = *family.RouteMetric
			}
			key = fmt.Sprintf("main/default/%d", metric)
		}
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceMainRoute, Family: name, Key: key, Writer: routeWriter}); err != nil {
			return err
		}
	}
	if family.DHCP != nil && *family.DHCP {
		return claimDHCPResources(connection, name, family, writer, add)
	}
	return nil
}

func claimDHCPResources(connection interfaceintent.Connection, name string, family interfaceintent.Family, writer interfaceintent.Writer, add func(interfaceintent.Claim) error) error {
	if connection.Owner == interfaceintent.OwnerMWAN && name == "ipv4" &&
		(connection.IPv4.DHCPv4 == nil || connection.IPv4.DHCPv4.UseRoutes == nil || *connection.IPv4.DHCPv4.UseRoutes) {
		if family.RouteMetric == nil {
			return fmt.Errorf("connection %s DHCPv4 route metric is absent", connection.ID)
		}
		key := fmt.Sprintf("main/default/%d", *family.RouteMetric)
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceMainRoute, Family: name, Key: key, Writer: interfaceintent.WriterMWANRoute}); err != nil {
			return err
		}
	}
	kind := interfaceintent.ResourceDHCPv4
	if name == "ipv6" {
		kind = interfaceintent.ResourceDHCPv6
	}
	clientWriter := writer
	acquiredWriter := writer
	if connection.Owner == interfaceintent.OwnerMWAN {
		clientWriter = interfaceintent.WriterMWANProtocol
		acquiredWriter = interfaceintent.WriterMWANAddress
	}
	if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: kind, Family: name, Key: connection.Name, Writer: clientWriter}); err != nil {
		return err
	}
	return add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceAcquiredAddress, Family: name, Key: connection.Name, Writer: acquiredWriter})
}

func claimProviderResources(connection interfaceintent.Connection, provider config.IfMgrWANEntry, add func(interfaceintent.Claim) error) error {
	addressWriter := interfaceintent.WriterWANRoutes
	nptWriter := interfaceintent.WriterNPT
	if connection.Owner == interfaceintent.OwnerMWAN {
		addressWriter = interfaceintent.WriterMWANAddress
		nptWriter = interfaceintent.WriterMWANAddress
	}
	if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceProviderRoute, Family: "ipv4+ipv6", Key: strconv.Itoa(provider.TableID), Writer: interfaceintent.WriterWANRoutes}); err != nil {
		return err
	}
	for _, priority := range []int{provider.FwMarkPrio, provider.FromPrio} {
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourcePolicyRule, Family: "ipv4+ipv6", Key: strconv.Itoa(priority), Writer: interfaceintent.WriterWANRoutes}); err != nil {
			return err
		}
	}
	if provider.TranslationV4 != nil {
		for _, mapping := range provider.TranslationV4.StaticMappings {
			if !mappingNeedsAddressClaim(connection, mapping) {
				continue
			}
			if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceMappedAddress, Family: "ipv4", Key: mapping.External.String(), Writer: addressWriter}); err != nil {
				return err
			}
		}
	}
	if provider.TranslationV6 != nil && provider.TranslationV6.NPT != nil {
		prefix := provider.TranslationV6.NPT.ExternalPrefix
		key := connection.Name + "/dynamic-prefix"
		if prefix.IsValid() {
			key = prefix.String()
		}
		if err := add(interfaceintent.Claim{ConnectionID: connection.ID, Kind: interfaceintent.ResourceNPTExternalAddress, Family: "ipv6", Key: key + "/::1/128", Writer: nptWriter}); err != nil {
			return err
		}
	}
	return nil
}

func mappingNeedsAddressClaim(connection interfaceintent.Connection, mapping config.StaticMapping) bool {
	if mapping.Delivery == interfaceintent.DeliveryRouted {
		return false
	}
	if mapping.Delivery == interfaceintent.DeliveryLocal {
		return !mappingUsesStaticAddress(connection, mapping.External)
	}
	if connection.IPv4 == nil {
		return false
	}
	for _, address := range connection.IPv4.Addresses {
		if address.Purpose != interfaceintent.PurposeLocal || address.Prefix.Addr() == mapping.External {
			continue
		}
		if address.Prefix.Bits() < 32 && address.Prefix.Masked().Contains(mapping.External) {
			return true
		}
	}
	return false
}

func mappingUsesStaticAddress(connection interfaceintent.Connection, external netip.Addr) bool {
	if connection.IPv4 == nil {
		return false
	}
	for _, address := range connection.IPv4.Addresses {
		if address.Purpose == interfaceintent.PurposeLocal && address.Prefix.Addr() == external {
			return true
		}
	}
	return false
}

func intentFamilyV4(family *interfaceintent.IPv4) *interfaceintent.Family {
	if family == nil {
		return nil
	}
	return &family.Family
}

func intentFamilyV6(family *interfaceintent.IPv6) *interfaceintent.Family {
	if family == nil {
		return nil
	}
	return &family.Family
}
