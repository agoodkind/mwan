package netif

import (
	"net/netip"

	"github.com/vishvananda/netlink"
)

func sameDHCPv4Owner(a, b ownedDHCPv4Object) bool {
	return a.ConsumerID == b.ConsumerID && a.ConnectionID == b.ConnectionID
}

func sameDHCPv4Link(a, b ownedDHCPv4Object) bool {
	return a.LinkIndex == b.LinkIndex && a.LinkIdentity == b.LinkIdentity &&
		a.LinkName == b.LinkName
}

func sameDHCPv4Object(a, b ownedDHCPv4Object) bool {
	return sameDHCPv4Owner(a, b) && sameDHCPv4Link(a, b) && a.TableID == b.TableID &&
		a.Prefix == b.Prefix && a.Destination == b.Destination && a.Gateway == b.Gateway &&
		a.Metric == b.Metric && a.LegacyProtocol == b.LegacyProtocol
}

func dhcpv4Owner(consumerID, connectionID string, tableID int) ownedDHCPv4Object {
	var value ownedDHCPv4Object
	value.ConsumerID = consumerID
	value.ConnectionID = connectionID
	value.TableID = tableID

	return value
}

func dhcpv4DefaultRoute(route netlink.Route) bool {
	if route.Dst == nil {
		return true
	}

	bits, size := route.Dst.Mask.Size()

	return size == 32 && bits == 0
}

func dhcpv4RouteMatches(route netlink.Route, value ownedDHCPv4Object) bool {
	destination, err := netip.ParsePrefix(value.Destination)
	if err != nil {
		return false
	}

	if route.Dst == nil {
		return destination.Bits() == 0
	}

	return route.Dst.String() == destination.String()
}

func (r *OwnedDHCPv4Reconciler) recordedRoute(route netlink.Route, value ownedDHCPv4Object) bool {
	for _, old := range r.journal.Objects {
		if old.Prefix != "" || !sameDHCPv4Owner(old, value) || !sameDHCPv4Link(old, value) ||
			old.TableID != value.TableID ||
			old.Metric != value.Metric ||
			!dhcpv4RouteMatches(route, old) {
			continue
		}

		if route.Protocol == dhcpv4RouteProtocol(old) && route.LinkIndex == old.LinkIndex &&
			route.Gw.Equal(dhcpv4Gateway(old.Gateway)) {
			return true
		}
	}

	return false
}

func dhcpv4RouteProtocol(value ownedDHCPv4Object) netlink.RouteProtocol {
	if value.LegacyProtocol != 0 {
		return netlink.RouteProtocol(value.LegacyProtocol)
	}

	return OwnedDHCPv4RouteProtocol
}
