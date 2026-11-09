package wanconfig

import "goodkind.io/mwan/internal/interfaceintent"

func appendLinkFamilies(items []Item, base string, connection interfaceintent.Connection) []Item {
	if connection.IPv4 != nil {
		family := base + "/ietf-ip:ipv4"

		items = append(items, familyItems(family, connection.IPv4.Family)...)
		for _, address := range connection.IPv4.SourceAddresses {
			items = append(
				items,
				Item{
					Path:  family + "/goodkind-mwan-steering:source-addresses",
					Value: address.String(),
				},
			)
		}
	}

	if connection.IPv6 != nil {
		family := base + "/ietf-ip:ipv6"
		items = append(items, familyItems(family, connection.IPv6.Family)...)
		if connection.IPv6.AcceptRA != nil {
			items = append(
				items,
				Item{
					Path:  family + "/goodkind-mwan-steering:accept-ra",
					Value: boolValue(*connection.IPv6.AcceptRA),
				},
			)
		}

		if connection.IPv6.Delegation != nil {
			items = append(
				items,
				delegationItems(
					family+"/goodkind-mwan-steering:delegation",
					*connection.IPv6.Delegation,
				)...,
			)
		}
	}

	return items
}
