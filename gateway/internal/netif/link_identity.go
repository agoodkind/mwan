package netif

import "github.com/vishvananda/netlink"

// LinkOwnershipIdentity prefers an ownership alias, then the permanent MAC address.
// The current MAC address identifies links without either value.
func LinkOwnershipIdentity(link netlink.Link) string {
	if link.Attrs().Alias != "" {
		return "alias:" + link.Attrs().Alias
	}
	if len(link.Attrs().PermHWAddr) != 0 {
		return "mac:" + link.Attrs().PermHWAddr.String()
	}
	return "mac:" + link.Attrs().HardwareAddr.String()
}

// LinkMatchesIdentity requires an observation from the current kernel.
// Journal owners separately verify the boot and any required interface name.
func LinkMatchesIdentity(link netlink.Link, index int, identity string) bool {
	return link.Attrs().Index == index && LinkOwnershipIdentity(link) == identity
}
