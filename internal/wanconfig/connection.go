package wanconfig

import (
	"fmt"
	"net/netip"

	"goodkind.io/mwan/internal/interfaceintent"
)

func validateFirewall(g Gateway) error {
	if !g.Firewall.Enabled {
		return nil
	}
	if err := g.Firewall.Validate(); err != nil {
		return invalid(fmt.Sprintf("firewall: %v", err))
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
	if connection.Owner == interfaceintent.OwnerMWAN &&
		(connection.Link == nil || connection.IPv4 != nil || connection.IPv6 != nil || len(connection.Networkd) != 0) {
		return invalid(fmt.Sprintf("interface %s must have only mwan-owned link intent", connection.Name))
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
	return validateFreeForm(connection.Name, connection.Networkd)
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
