package main

import (
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/addresses"
	"goodkind.io/mwan/internal/ifmgr/modules/autoconfiguration"
	"goodkind.io/mwan/internal/ifmgr/modules/resolver"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

func buildResolverConfig(ifmgrCfg config.IfMgrSection) resolver.Config {
	resolverConfig := resolver.Config{Connections: ifmgrCfg.Connections, StateFile: ""}
	if ifmgrCfg.Modules.Resolver != nil {
		resolverConfig.StateFile = ifmgrCfg.Modules.Resolver.StateFile
	}
	return resolverConfig
}

func buildAutoconfigurationConfig(ifmgrCfg config.IfMgrSection) autoconfiguration.Config {
	policyConfig := autoconfiguration.Config{Connections: ifmgrCfg.Connections, StateFile: ""}
	if ifmgrCfg.Modules.Autoconfiguration != nil {
		policyConfig.StateFile = ifmgrCfg.Modules.Autoconfiguration.StateFile
	}
	return policyConfig
}

func buildAddressesConfig(ifmgrCfg config.IfMgrSection) (addresses.Config, error) {
	providers := make(map[string]addresses.Provider)
	clientIDs := make(map[string][]byte)
	for _, connection := range ifmgrCfg.Connections {
		if provider, found := ifmgrCfg.WAN[connection.ID.String()]; found {
			providers[connection.ID.String()] = addresses.Provider{IPv4: provider.TranslationV4, IPv6: provider.TranslationV6}
		}
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		if connection.IPv4 == nil || connection.IPv4.DHCP == nil || !*connection.IPv4.DHCP {
			continue
		}
		clientID := ""
		if connection.IPv4.DHCPv4 != nil {
			clientID = connection.IPv4.DHCPv4.ClientID
		}
		decoded, err := networkjson.DecodeDHCPv4ClientID(clientID)
		if err != nil {
			slog.Warn("ifmgr: invalid DHCPv4 client ID", "interface", connection.Name, "err", err)
			return addresses.Config{}, fmt.Errorf("interface %s dhcpv4 client-id: %w", connection.Name, err)
		}
		clientIDs[connection.ID.String()] = decoded
	}
	settings := addresses.Config{Connections: ifmgrCfg.Connections, Providers: providers, ClientIDs: clientIDs, StateFile: "", LeaseDirectory: ifmgrCfg.LeaseDirectory}
	if ifmgrCfg.Modules.Addresses != nil {
		settings.StateFile = ifmgrCfg.Modules.Addresses.StateFile
	}
	return settings, nil
}
