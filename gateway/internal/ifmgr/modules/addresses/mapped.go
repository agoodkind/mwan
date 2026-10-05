package addresses

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkd"
)

func (module *Module) reconcileLegacyMappings(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, translation *config.IPv4Translation) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.WarnContext(ctx, "addresses: legacy mapped reconciliation failed", "connection", connection.ID, "err", resultErr)
		}
	}()
	link, err := netif.ObserveLegacyLink(connection)
	if err != nil {
		return fmt.Errorf("observe mapped connection %s: %w", connection.ID, err)
	}
	current, err := netif.ListAddrs(ctx, log, connection.Name)
	if err != nil {
		return fmt.Errorf("read mapped connection %s: %w", connection.ID, err)
	}
	var acquired map[netip.Addr]bool
	if connection.IPv4 != nil && connection.IPv4.DHCP != nil && *connection.IPv4.DHCP {
		if module.networkdTimeout <= 0 {
			return fmt.Errorf("networkd acquisition observation requires a positive watchdog timeout")
		}
		observationContext, cancel := context.WithTimeout(ctx, module.networkdTimeout)
		observed, err := networkd.Addresses(observationContext, connection.Name)
		cancel()
		if err != nil {
			return fmt.Errorf("observe networkd acquisition: %w", err)
		}
		acquired = make(map[netip.Addr]bool)
		for _, address := range observed {
			if address.Prefix.Addr().Is4() && address.ConfigSource == "DHCPv4" && address.ConfigState == "configured" {
				acquired[address.Prefix.Addr()] = true
			}
		}
		if len(acquired) == 0 {
			return fmt.Errorf("networkd DHCPv4 primary address is not configured")
		}
	}
	addresses, err := legacyMappedAddresses(connection, current, translation, acquired)
	if err != nil {
		return fmt.Errorf("select mapped connection %s: %w", connection.ID, err)
	}
	ready := netif.OwnedLinkResult{ConnectionID: connection.ID.String(), Name: connection.Name, ActualName: link.Attrs().Name, IfIndex: link.Attrs().Index, Status: netif.OwnedLinkReady, Dependency: "", Operation: "observe", Err: nil}
	settings := interfaceintent.Family{Enabled: nil, Forwarding: nil, Addresses: addresses, DHCP: nil, Gateway: netip.Addr{}, RouteMetric: nil, Routes: nil, DNS: nil, SearchDomains: nil}
	if err := module.reconciler.ReconcileFamilyRoutes(ctx, connection, "ipv4", settings, nil, ready); err != nil {
		return fmt.Errorf("reconcile mapped connection %s: %w", connection.ID, err)
	}
	return module.publishInstalled(ctx, log, connection, settings, ready, nil)
}

func legacyMappedAddresses(connection interfaceintent.Connection, current []netif.CurrentAddr, translation *config.IPv4Translation, acquired map[netip.Addr]bool) ([]interfaceintent.Address, error) {
	var inferred []netip.Addr
	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery != interfaceintent.DeliveryRouted {
			inferred = append(inferred, mapping.External)
		}
	}
	onLink, err := netif.OnLinkMappedAddresses(current, inferred)
	if err != nil {
		slog.Warn("addresses: legacy mapped subnet selection failed", "err", err)
		return nil, fmt.Errorf("select mapped subnets: %w", err)
	}
	assigned := make(map[netip.Addr]bool)
	for _, address := range current {
		if address.Family != "inet" {
			continue
		}
		prefix, err := netip.ParsePrefix(address.CIDR)
		if err != nil {
			slog.Warn("addresses: legacy address prefix parse failed", "cidr", address.CIDR, "err", err)
			return nil, fmt.Errorf("parse link address %q: %w", address.CIDR, err)
		}
		if prefix.Bits() < 32 || acquired[prefix.Addr()] {
			assigned[prefix.Addr()] = true
		}
	}
	var addresses []interfaceintent.Address
	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery == interfaceintent.DeliveryRouted || assigned[mapping.External] || staticAddressEquals(connection, mapping.External) {
			continue
		}
		if mapping.Delivery != interfaceintent.DeliveryLocal && !slices.Contains(onLink, mapping.External) {
			continue
		}
		addresses = append(addresses, interfaceintent.Address{Prefix: netip.PrefixFrom(mapping.External, 32), Purpose: interfaceintent.PurposeForward})
	}
	return addresses, nil
}

func networkdMappingProvider(connection interfaceintent.Connection, providers map[string]Provider) *config.IPv4Translation {
	if connection.Owner != interfaceintent.OwnerNetworkd {
		return nil
	}
	provider := providers[connection.ID.String()]
	if provider.IPv4 == nil || len(provider.IPv4.StaticMappings) == 0 {
		return nil
	}
	return provider.IPv4
}
