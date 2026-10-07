package networkjson

import (
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"strconv"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

// Family uses the YANG address-family identifiers in projection keys.
type Family string

const (
	// FamilyIPv4 uses the YANG IPv4 family identifier.
	FamilyIPv4 Family = "ipv4"
	// FamilyIPv6 uses the YANG IPv6 family identifier.
	FamilyIPv6 Family = "ipv6"
)

// RouteSource distinguishes route-list entries from projected gateway defaults.
type RouteSource string

const (
	// RouteSourceRoute identifies a YANG route-list entry.
	RouteSourceRoute RouteSource = "route"
	// RouteSourceGateway identifies a gateway leaf projected as a default route.
	RouteSourceGateway RouteSource = "gateway"
)

const (
	keySeparator        = "|"
	mainTableID  uint32 = 254
)

// RouteKey matches the YANG route key path of interface, family, and destination.
type RouteKey struct {
	Interface   string
	Family      Family
	Destination netip.Prefix
}

// String formats <interface>|<family>|<destination> with [netip.Prefix.String].
// IPv6 destinations use lowercase.
func (k RouteKey) String() string {
	return k.Interface + keySeparator + string(k.Family) + keySeparator + k.Destination.String()
}

// ConfiguredRoute records the route-list or gateway leaf source of a main-table route.
type ConfiguredRoute struct {
	interfaceintent.RouteIntent
	Source RouteSource
}

// ProviderDefaultKey uses a derived connection ID, family, and provider table ID.
type ProviderDefaultKey struct {
	ConnectionID connectionid.ID
	Family       Family
	TableID      uint32
}

// String formats <connection-id>|<family>|<table-id> using the derived connection ID.
func (k ProviderDefaultKey) String() string {
	return k.ConnectionID.String() + keySeparator + string(k.Family) + keySeparator +
		strconv.FormatUint(uint64(k.TableID), 10)
}

// ProviderDefault projects the default and internal routes in a provider table.
// The daemon reads the default gateway from the kernel at runtime.
// The internal destination is internal-net-v4 or opnsense-edge-v6/128.
type ProviderDefault struct {
	Interface           string
	Family              Family
	TableID             uint32
	InternalDestination netip.Prefix
	InternalInterface   string
}

// ConfiguredRoutes projects route-list entries and gateway leaves from accepted connections.
// A gateway leaf becomes a family default route in table 254.
// The gateway metric is route-metric or zero when route-metric is absent.
// Decode rejects a gateway leaf with a /0 route-list entry in the same family.
func (c *Config) ConfiguredRoutes() map[RouteKey]ConfiguredRoute {
	routes := make(map[RouteKey]ConfiguredRoute)
	for _, connection := range c.Connections {
		for name, family := range routeClaimFamilies(connection) {
			addFamilyRoutes(routes, connection.Name, Family(name), family)
		}
	}
	return routes
}

func addFamilyRoutes(routes map[RouteKey]ConfiguredRoute, iface string, name Family, family interfaceintent.Family) {
	if family.Gateway.IsValid() {
		metric := uint32(0)
		if family.RouteMetric != nil {
			metric = *family.RouteMetric
		}
		gatewayRoute := interfaceintent.RouteIntent{
			Destination: defaultPrefix(name),
			Gateway:     family.Gateway,
			TableID:     mainTableID,
			Metric:      metric,
		}
		key := RouteKey{Interface: iface, Family: name, Destination: gatewayRoute.Destination}
		routes[key] = ConfiguredRoute{RouteIntent: gatewayRoute, Source: RouteSourceGateway}
	}
	for _, route := range family.Routes {
		key := RouteKey{Interface: iface, Family: name, Destination: route.Destination}
		routes[key] = ConfiguredRoute{RouteIntent: route, Source: RouteSourceRoute}
	}
}

func defaultPrefix(family Family) netip.Prefix {
	if family == FamilyIPv4 {
		return netip.PrefixFrom(netip.IPv4Unspecified(), 0)
	}
	return netip.PrefixFrom(netip.IPv6Unspecified(), 0)
}

// ProviderDefaults projects each accepted provider's translated families.
// Keys use the derived connection ID, family, and table ID.
// ProviderDefaults rejects invalid internal destinations and table IDs outside uint32.
// Only libyang checks the table-id range during loading.
func (c *Config) ProviderDefaults() (map[ProviderDefaultKey]ProviderDefault, error) {
	defaults := make(map[ProviderDefaultKey]ProviderDefault)
	for id, provider := range c.WAN {
		if provider.TableID < 0 || uint64(provider.TableID) > math.MaxUint32 {
			err := fmt.Errorf("wan %s: table-id %d is outside 0 to %d", id, provider.TableID, uint32(math.MaxUint32))
			slog.Error("networkjson: provider table-id outside uint32", "err", err)
			return nil, err
		}
		tableID := uint32(provider.TableID)
		var families []Family
		if provider.TranslationV4 != nil {
			families = append(families, FamilyIPv4)
		}
		if provider.TranslationV6 != nil {
			families = append(families, FamilyIPv6)
		}
		for _, family := range families {
			destination, err := c.internalDestination(family)
			if err != nil {
				return nil, err
			}
			key := ProviderDefaultKey{ConnectionID: connectionid.ID(id), Family: family, TableID: tableID}
			defaults[key] = ProviderDefault{
				Interface:           provider.Iface,
				Family:              family,
				TableID:             tableID,
				InternalDestination: destination,
				InternalInterface:   c.InternalIface,
			}
		}
	}
	return defaults, nil
}

func (c *Config) internalDestination(family Family) (netip.Prefix, error) {
	if family == FamilyIPv4 {
		destination, err := netip.ParsePrefix(c.InternalNetV4)
		if err != nil {
			slog.Error("networkjson: internal-net-v4 unparsable", "value", c.InternalNetV4, "err", err)
			return netip.Prefix{}, fmt.Errorf("steering-group/routes/internal-net-v4 %q: %w", c.InternalNetV4, err)
		}
		return destination, nil
	}
	edge, err := netip.ParseAddr(c.OpnsenseEdgeV6)
	if err != nil {
		slog.Error("networkjson: opnsense-edge-v6 unparsable", "value", c.OpnsenseEdgeV6, "err", err)
		return netip.Prefix{}, fmt.Errorf("steering-group/translation/opnsense-edge-v6 %q: %w", c.OpnsenseEdgeV6, err)
	}
	return netip.PrefixFrom(edge, edge.BitLen()), nil
}
