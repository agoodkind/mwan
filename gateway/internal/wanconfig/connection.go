package wanconfig

import (
	"fmt"
	"net/netip"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

func validateFirewall(g Gateway) error {
	if !g.Firewall.Enabled {
		return nil
	}
	if err := g.Firewall.Validate(); err != nil {
		return invalid(fmt.Sprintf("firewall: %v", err))
	}
	if g.Firewall.ManagementInterface == "" {
		return nil
	}
	return validateKey("management link", g.Firewall.ManagementInterface)
}

func validateConnections(connections []interfaceintent.Connection) (map[string]bool, error) {
	seen := make(map[string]bool, len(connections))
	for _, connection := range connections {
		if err := validateKey("connection name", connection.Name); err != nil {
			return nil, err
		}
		if connection.Type == "" {
			return nil, invalid("connection " + connection.Name + " has no interface type")
		}
		if seen[connection.Name] {
			return nil, invalid("interface " + connection.Name + " is duplicated")
		}
		seen[connection.Name] = true
		if err := validateConnection(connection); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

func validateMembers(members []Member, seen map[string]bool) error {
	instances := make(map[uint32]string)
	for _, member := range members {
		if err := validateMember(member, seen, instances); err != nil {
			return err
		}
	}
	return nil
}

func validateMember(member Member, seen map[string]bool, instances map[uint32]string) error {
	if err := validateKey("member name", member.Name); err != nil {
		return err
	}
	if err := validateKey("member "+member.Name+" link", member.Iface); err != nil {
		return err
	}
	if member.Weight == 0 {
		return invalid(fmt.Sprintf("member %s weight must be at least 1", member.Name))
	}
	if !seen[member.Iface] {
		return invalid(fmt.Sprintf("member %s interface %q is absent", member.Name, member.Iface))
	}
	for family, id := range map[string]uint32{"ipv4": member.TranslationIDV4, "ipv6": member.TranslationIDV6} {
		if id != TranslationInstanceID(member.Name, family) {
			return invalid("member " + member.Name + " has an invalid translation instance ID")
		}
		if prior, exists := instances[id]; exists {
			return invalid("translation instance ID collision between " + prior + " and " + member.Name + "/" + family)
		}
		instances[id] = member.Name + "/" + family
	}
	if err := validateTranslation(member); err != nil {
		return err
	}
	return validateProvider(member)
}

func validateConnection(connection interfaceintent.Connection) error {
	if connection.Owner != interfaceintent.OwnerExternal && connection.Owner != interfaceintent.OwnerNetworkd &&
		connection.Owner != interfaceintent.OwnerMWAN {
		return invalid(fmt.Sprintf("interface %s has invalid owner %q", connection.Name, connection.Owner))
	}
	if connection.Owner == interfaceintent.OwnerMWAN && (connection.Link == nil || len(connection.Networkd) != 0) {
		return invalid(fmt.Sprintf("interface %s must have a mwan-owned link without networkd files", connection.Name))
	}
	if err := validateVLAN(connection.Name, connection.Link); err != nil {
		return err
	}
	if connection.IPv4 != nil {
		if err := validateFamily(connection.Name, "ipv4", connection.IPv4.Family, netip.Addr.Is4); err != nil {
			return err
		}
	}
	if connection.IPv6 != nil {
		if err := validateFamily(connection.Name, "ipv6", connection.IPv6.Family, netip.Addr.Is6); err != nil {
			return err
		}
		if delegation := connection.IPv6.Delegation; delegation != nil && delegation.Hint.IsValid() && !delegation.Hint.Addr().Is6() {
			return invalid(fmt.Sprintf("interface %s delegation hint %q is not IPv6", connection.Name, delegation.Hint))
		}
	}
	if connection.Owner == interfaceintent.OwnerMWAN {
		if err := validateMWANConnection(connection); err != nil {
			return err
		}
	}
	return validateFreeForm(connection.Name, connection.Networkd)
}

func validateMWANConnection(connection interfaceintent.Connection) error {
	if ipv4 := connection.IPv4; ipv4 != nil &&
		len(ipv4.SourceAddresses) != 0 {
		return invalid(fmt.Sprintf("interface %s has unsupported mwan ipv4 intent", connection.Name))
	}
	if connection.IPv4 != nil {
		if err := validateMWANIPv4(connection.Name, connection.IPv4); err != nil {
			return err
		}
	}
	if ipv6 := connection.IPv6; ipv6 != nil && len(ipv6.ForwardingAddresses) != 0 {
		return invalid(fmt.Sprintf("interface %s has unsupported mwan ipv6 intent", connection.Name))
	}
	if err := validateMWANDHCPv6(connection); err != nil {
		return err
	}
	if connection.IPv6 != nil {
		// An IPv6 route-metric with no gateway sets the metric of the router advertisement default route.
		return validateMWANStaticFamily(connection.Name, "ipv6", connection.IPv6.Family, true)
	}
	return nil
}

func validateMWANDHCPv6(connection interfaceintent.Connection) error {
	ipv6 := connection.IPv6
	if ipv6 == nil || ipv6.DHCP == nil && ipv6.Delegation == nil && ipv6.DHCPv6 == nil {
		return nil
	}
	if err := networkjson.ValidateOwnedDHCPv6(connection.Name, ipv6); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func validateMWANIPv4(name string, ipv4 *interfaceintent.IPv4) error {
	dhcp := ipv4.DHCP != nil && *ipv4.DHCP
	routesEnabled := ipv4.DHCPv4 == nil || ipv4.DHCPv4.UseRoutes == nil || *ipv4.DHCPv4.UseRoutes
	if dhcp && ipv4.Gateway.IsValid() && routesEnabled {
		return invalid(fmt.Sprintf("interface %s ipv4 cannot combine a static gateway with DHCP routes", name))
	}
	if ipv4.DHCPv4 != nil {
		if !dhcp {
			return invalid(fmt.Sprintf("interface %s dhcpv4 requires ipv4/dhcp true", name))
		}
		if _, err := networkjson.DecodeDHCPv4ClientID(ipv4.DHCPv4.ClientID); err != nil {
			return invalid(fmt.Sprintf("interface %s dhcpv4 client-id: %v", name, err))
		}
	}
	if dhcp && routesEnabled && ipv4.RouteMetric == nil {
		return invalid(fmt.Sprintf("interface %s dhcpv4 routes require route-metric", name))
	}
	return validateMWANStaticFamily(name, "ipv4", ipv4.Family, dhcp)
}

func validateMWANStaticFamily(name, familyName string, family interfaceintent.Family, acquiredRouteMetric bool) error {
	if family.Enabled != nil && !*family.Enabled ||
		family.RouteMetric != nil && !family.Gateway.IsValid() && !acquiredRouteMetric {
		return invalid(fmt.Sprintf("interface %s has unsupported mwan %s settings", name, familyName))
	}
	for _, address := range family.Addresses {
		if !address.Prefix.IsValid() || address.Prefix.Addr().IsUnspecified() || address.Prefix.Addr().IsMulticast() ||
			address.Purpose != interfaceintent.PurposeLocal {
			return invalid(fmt.Sprintf("interface %s has unsupported mwan %s address", name, familyName))
		}
	}
	return nil
}

func validateVLAN(name string, link *interfaceintent.Link) error {
	if link == nil || link.VLAN == nil {
		return nil
	}
	if err := validateKey("interface "+name+" vlan parent", link.VLAN.Parent); err != nil {
		return err
	}
	if link.VLAN.ID < 1 || link.VLAN.ID > maxVLANID {
		return invalid(fmt.Sprintf("interface %s has invalid vlan id %d", name, link.VLAN.ID))
	}
	return nil
}

func validateFamily(name string, familyName string, family interfaceintent.Family, inFamily func(netip.Addr) bool) error {
	if err := interfaceintent.ValidateConfiguredRoutes(familyName, family.Routes, family.Gateway.IsValid()); err != nil {
		return invalid(fmt.Sprintf("interface %s: %v", name, err))
	}
	for _, address := range family.Addresses {
		if !inFamily(address.Prefix.Addr()) {
			return invalid(fmt.Sprintf("interface %s %s address %q has the wrong family", name, familyName, address.Prefix))
		}
	}
	if family.Gateway.IsValid() && !inFamily(family.Gateway) {
		return invalid(fmt.Sprintf("interface %s %s gateway %q has the wrong family", name, familyName, family.Gateway))
	}
	for _, server := range family.DNS {
		if !inFamily(server) {
			return invalid(fmt.Sprintf("interface %s %s dns-server %q has the wrong family", name, familyName, server))
		}
	}
	return nil
}

func validateFreeForm(name string, files []interfaceintent.UnitFile) error {
	type unitKind string
	const (
		unitLink    unitKind = "link"
		unitNetwork unitKind = "network"
		unitNetdev  unitKind = "netdev"
	)
	for _, file := range files {
		switch unitKind(file.Kind) {
		case unitLink, unitNetwork, unitNetdev:
		default:
			return invalid(fmt.Sprintf("interface %s has invalid networkd file kind %q", name, file.Kind))
		}
		for _, section := range file.Sections {
			if section.Name == "" {
				return invalid(fmt.Sprintf("interface %s has unnamed networkd section", name))
			}
			for _, entry := range section.Entries {
				if entry.Key == "" {
					return invalid(fmt.Sprintf("interface %s has unnamed networkd entry", name))
				}
			}
		}
	}
	return nil
}
