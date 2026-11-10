package interfaceintent

import (
	"fmt"
	"net/netip"
)

// ValidateConfiguredRoutes rejects routes outside the configured main-table contract.
func ValidateConfiguredRoutes(family string, routes []RouteIntent, defaultGateway bool) error {
	seen := make(map[netip.Prefix]bool)
	for _, route := range routes {
		destination := route.Destination
		if !destination.IsValid() || destination != destination.Masked() || destination.Addr().Is4In6() || destination.Addr().Is4() != (family == "ipv4") {
			return fmt.Errorf("%s route destination %q must be a canonical same-family network prefix", family, destination)
		}
		if route.TableID != 254 {
			return fmt.Errorf("%s configured routes require table-id 254", family)
		}
		if seen[destination] || destination.Bits() == 0 && defaultGateway {
			return fmt.Errorf("%s route destination %s is configured twice", family, destination)
		}
		seen[destination] = true
		gateway := route.Gateway
		if gateway.IsValid() && (gateway.Is4In6() || gateway.Is4() != (family == "ipv4") || gateway.IsUnspecified() || gateway.IsMulticast() || gateway.Zone() != "") {
			return fmt.Errorf("%s route %s has invalid same-family gateway %q", family, destination, gateway)
		}
	}
	return nil
}
