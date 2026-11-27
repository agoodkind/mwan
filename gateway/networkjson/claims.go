package networkjson

// Resource claims record which component can write each link, address, or
// route. The compiler rejects a second claim for the same resource. For
// example, two mwan routes cannot both claim the same IPv4 default route
// metric in the main routing table.

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
)

var (
	errConflictingWriters       = errors.New("has writers")
	errMissingDHCPv4RouteMetric = errors.New("DHCPv4 route metric is absent")
)

func compileClaims(
	connections []interfaceintent.Connection,
	providers map[string]config.IfMgrWANEntry,
) ([]interfaceintent.Claim, error) {
	if err := validateConfiguredGatewayClaims(connections); err != nil {
		return nil, err
	}

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
			return fmt.Errorf(
				"resource %s %w %s and %s",
				key,
				errConflictingWriters,
				previous.Writer,
				claim.Writer,
			)
		}

		addressKey := claimedAddressKey(claim, names[claim.ConnectionID])
		if addressKey != "" {
			if previous, taken := addressOwners[addressKey]; taken {
				return fmt.Errorf(
					"address %s %w %s and %s",
					addressKey,
					errConflictingWriters,
					previous.Writer,
					claim.Writer,
				)
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

func claimConnectionResources(
	connection interfaceintent.Connection,
	add func(interfaceintent.Claim) error,
) error {
	writer := interfaceintent.WriterExternal
	switch connection.Owner {
	case interfaceintent.OwnerExternal:
	case interfaceintent.OwnerNetworkd:
		writer = interfaceintent.WriterNetworkd
	case interfaceintent.OwnerMWAN:
		writer = interfaceintent.WriterMWANLink
	}

	if connection.Link != nil {
		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceLink,
			Family:       "",
			Key:          connection.Name,
			Writer:       writer,
		}); err != nil {
			return err
		}
	}

	for _, family := range []struct {
		name   string
		config *interfaceintent.Family
	}{
		{name: string(FamilyIPv4), config: intentFamilyV4(connection.IPv4)},
		{name: string(FamilyIPv6), config: intentFamilyV6(connection.IPv6)},
	} {
		if family.config == nil {
			continue
		}

		if err := claimFamilyResources(
			connection,
			family.name,
			*family.config,
			writer,
			add,
		); err != nil {
			return err
		}
	}

	if connection.IPv6 != nil &&
		((connection.IPv6.AcceptRA != nil && *connection.IPv6.AcceptRA) ||
			(connection.IPv6.AutoConf != nil && *connection.IPv6.AutoConf)) {
		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceRASLAAC,
			Family:       string(FamilyIPv6),
			Key:          connection.Name,
			Writer:       interfaceintent.WriterKernel,
		}); err != nil {
			return err
		}
	}

	return nil
}

func claimFamilyResources(
	connection interfaceintent.Connection,
	name string,
	family interfaceintent.Family,
	writer interfaceintent.Writer,
	add func(interfaceintent.Claim) error,
) error {
	addressWriter := writer
	routeWriter := writer
	if connection.Owner == interfaceintent.OwnerMWAN {
		addressWriter = interfaceintent.WriterMWANAddress
		routeWriter = interfaceintent.WriterMWANRoute
	}

	for _, address := range family.Addresses {
		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceStaticAddress,
			Family:       name,
			Key:          connection.Name + "/" + address.Prefix.String(),
			Writer:       addressWriter,
		}); err != nil {
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

			metric = mainRouteMetric(name, metric)
			key = fmt.Sprintf("main/default/%d", metric)
		}

		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceMainRoute,
			Family:       name,
			Key:          key,
			Writer:       routeWriter,
		}); err != nil {
			return err
		}
	}

	for _, route := range family.Routes {
		destination := route.Destination.String()
		if route.Destination.Bits() == 0 {
			destination = "default"
		}

		key := fmt.Sprintf("main/%s/%d", destination, mainRouteMetric(name, route.Metric))
		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceMainRoute,
			Family:       name,
			Key:          key,
			Writer:       routeWriter,
		}); err != nil {
			return err
		}
	}

	if family.DHCP != nil && *family.DHCP {
		return claimDHCPResources(connection, name, family, writer, add)
	}

	return nil
}

func claimDHCPResources(
	connection interfaceintent.Connection,
	name string,
	family interfaceintent.Family,
	writer interfaceintent.Writer,
	add func(interfaceintent.Claim) error,
) error {
	if connection.Owner == interfaceintent.OwnerMWAN && name == string(FamilyIPv4) &&
		(connection.IPv4.DHCPv4 == nil || connection.IPv4.DHCPv4.UseRoutes == nil || *connection.IPv4.DHCPv4.UseRoutes) {
		if family.RouteMetric == nil {
			return fmt.Errorf("connection %s %w", connection.ID, errMissingDHCPv4RouteMetric)
		}

		key := fmt.Sprintf("main/default/%d", *family.RouteMetric)
		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceMainRoute,
			Family:       name,
			Key:          key,
			Writer:       interfaceintent.WriterMWANRoute,
		}); err != nil {
			return err
		}
	}

	kind := interfaceintent.ResourceDHCPv4
	if name == string(FamilyIPv6) {
		kind = interfaceintent.ResourceDHCPv6
	}

	clientWriter := writer
	acquiredWriter := writer
	if connection.Owner == interfaceintent.OwnerMWAN {
		clientWriter = interfaceintent.WriterMWANProtocol
		acquiredWriter = interfaceintent.WriterMWANAddress
	}

	if err := add(interfaceintent.Claim{
		ConnectionID: connection.ID,
		Kind:         kind,
		Family:       name,
		Key:          connection.Name,
		Writer:       clientWriter,
	}); err != nil {
		return err
	}

	return add(interfaceintent.Claim{
		ConnectionID: connection.ID,
		Kind:         interfaceintent.ResourceAcquiredAddress,
		Family:       name,
		Key:          connection.Name,
		Writer:       acquiredWriter,
	})
}

func claimProviderResources(
	connection interfaceintent.Connection,
	provider config.IfMgrWANEntry,
	add func(interfaceintent.Claim) error,
) error {
	addressWriter := interfaceintent.WriterMWANAddress
	if connection.Owner == interfaceintent.OwnerExternal {
		addressWriter = interfaceintent.WriterExternal
	}

	nptWriter := interfaceintent.WriterNPT
	if connection.Owner == interfaceintent.OwnerMWAN {
		nptWriter = interfaceintent.WriterMWANAddress
	}

	if err := add(interfaceintent.Claim{
		ConnectionID: connection.ID,
		Kind:         interfaceintent.ResourceProviderRoute,
		Family:       "ipv4+ipv6",
		Key:          strconv.Itoa(provider.TableID),
		Writer:       interfaceintent.WriterWANRoutes,
	}); err != nil {
		return err
	}

	for _, priority := range []int{provider.FwMarkPrio, provider.FromPrio} {
		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourcePolicyRule,
			Family:       "ipv4+ipv6",
			Key:          strconv.Itoa(priority),
			Writer:       interfaceintent.WriterWANRoutes,
		}); err != nil {
			return err
		}
	}

	if provider.TranslationV4 != nil {
		for _, mapping := range provider.TranslationV4.StaticMappings {
			if !mappingNeedsAddressClaim(connection, mapping) {
				continue
			}

			if err := add(interfaceintent.Claim{
				ConnectionID: connection.ID,
				Kind:         interfaceintent.ResourceMappedAddress,
				Family:       string(FamilyIPv4),
				Key:          mapping.External.String(),
				Writer:       addressWriter,
			}); err != nil {
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

		if err := add(interfaceintent.Claim{
			ConnectionID: connection.ID,
			Kind:         interfaceintent.ResourceNPTExternalAddress,
			Family:       string(FamilyIPv6),
			Key:          key + "/::1/128",
			Writer:       nptWriter,
		}); err != nil {
			return err
		}
	}

	return nil
}

func mappingNeedsAddressClaim(
	connection interfaceintent.Connection,
	mapping config.StaticMapping,
) bool {
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
		if address.Purpose != interfaceintent.PurposeLocal ||
			address.Prefix.Addr() == mapping.External {
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
