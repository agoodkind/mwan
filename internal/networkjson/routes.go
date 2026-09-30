package networkjson

import (
	"fmt"
	"net/netip"

	"goodkind.io/mwan/internal/interfaceintent"
)

func buildIntentRoutes(name, family string, routes []routeWire, defaultGateway bool) ([]interfaceintent.RouteIntent, error) {
	var built []interfaceintent.RouteIntent
	seen := make(map[netip.Prefix]bool)
	for _, route := range routes {
		destination, err := netip.ParsePrefix(route.Destination)
		if err != nil || destination != destination.Masked() || destination.Addr().Is4In6() || destination.Addr().Is4() != (family == "ipv4") {
			return built, fmt.Errorf("interface %s: %s route destination %q must be a canonical same-family network prefix", name, family, route.Destination)
		}
		if seen[destination] || destination.Bits() == 0 && defaultGateway {
			return built, fmt.Errorf("interface %s: %s route destination %s is configured twice", name, family, destination)
		}
		seen[destination] = true
		if route.TableID != nil && *route.TableID != 254 {
			return built, fmt.Errorf("interface %s: %s configured routes require table-id 254", name, family)
		}
		gateway := netip.Addr{}
		if route.Gateway != "" {
			gateway, err = netip.ParseAddr(route.Gateway)
			if err != nil || gateway.Is4In6() || gateway.Is4() != (family == "ipv4") || gateway.IsUnspecified() || gateway.IsMulticast() || gateway.Zone() != "" {
				return built, fmt.Errorf("interface %s: %s route %s has invalid same-family gateway %q", name, family, destination, route.Gateway)
			}
		}
		built = append(built, interfaceintent.RouteIntent{Destination: destination, Gateway: gateway, TableID: 254, Metric: route.Metric})
	}
	return built, nil
}

func mainRouteMetric(family string, metric uint32) uint32 {
	if family == "ipv6" && metric == 0 {
		return 1024
	}
	return metric
}

func validateConfiguredGatewayClaims(connections []interfaceintent.Connection) error {
	gateways := gatewayRouteSlots(connections)
	for _, connection := range connections {
		for name, family := range routeClaimFamilies(connection) {
			for _, route := range family.Routes {
				if route.Destination.Bits() != 0 {
					continue
				}
				key := fmt.Sprintf("%s/main/default/%d", name, mainRouteMetric(name, route.Metric))
				if prior, exists := gateways[key]; exists {
					return fmt.Errorf("configured route %s on %s conflicts with gateway on %s", key, connection.Name, prior)
				}
			}
		}
	}
	return nil
}

func gatewayRouteSlots(connections []interfaceintent.Connection) map[string]string {
	slots := make(map[string]string)
	for _, connection := range connections {
		for name, family := range routeClaimFamilies(connection) {
			if !family.Gateway.IsValid() {
				continue
			}
			metric := uint32(0)
			if family.RouteMetric != nil {
				metric = *family.RouteMetric
			}
			key := fmt.Sprintf("%s/main/default/%d", name, mainRouteMetric(name, metric))
			slots[key] = connection.Name
		}
	}
	return slots
}

func routeClaimFamilies(connection interfaceintent.Connection) map[string]interfaceintent.Family {
	families := make(map[string]interfaceintent.Family, 2)
	if connection.IPv4 != nil {
		families["ipv4"] = connection.IPv4.Family
	}
	if connection.IPv6 != nil {
		families["ipv6"] = connection.IPv6.Family
	}
	return families
}
