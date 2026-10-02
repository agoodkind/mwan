package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkd"
)

func ifMgrRole(cfg *config.Config, flags ifmgrFlags) (string, error) {
	role := cfg.IfMgr.Role
	if flags.role != "" {
		role = flags.role
	}
	if role == "" {
		return "", fmt.Errorf("ifmgr: role required (set [ifmgr].role in config or pass --role)")
	}
	return role, nil
}

func runLegacyNPTTransition(ctx context.Context, log *slog.Logger, cfg *config.Config, flags ifmgrFlags, role string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.ErrorContext(ctx, "legacy NPT transition rejected", "err", resultErr)
		}
	}()
	if role != "wan" || flags.dryRun {
		return fmt.Errorf("legacy NPT transition requires the WAN role without dry-run")
	}
	if flags.captureLegacyNPT != "" && flags.adoptLegacyNPT != "" {
		return fmt.Errorf("capture and apply are separate operations")
	}
	loaded, err := parseNetworkConfig(ctx, log, cfg, role)
	if err != nil {
		return err
	}
	if len(loaded.Rejected) != 0 {
		return fmt.Errorf("legacy NPT transition requires every configured provider to be accepted")
	}
	if cfg.IfMgr.Modules.Addresses == nil || cfg.IfMgr.Modules.Addresses.StateFile == "" {
		return fmt.Errorf("legacy NPT transition requires the configured address journal")
	}
	configs, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		return err
	}
	nptConfig, ok := configs["npt"].(npt.Config)
	if !ok {
		return fmt.Errorf("legacy NPT transition requires configured NPT")
	}
	edges, err := npt.ReadLegacyNPTEdges(ctx, log, nptConfig, cfg.IfMgr.Connections)
	if err != nil {
		return fmt.Errorf("read legacy NPT transition: %w", err)
	}
	mappings, err := readLegacyMappedTransition(ctx, log, cfg, nptConfig)
	if err != nil {
		return fmt.Errorf("read legacy mapped transition: %w", err)
	}
	journal := cfg.IfMgr.Modules.Addresses.StateFile
	if flags.captureLegacyNPT != "" {
		if err := netif.WriteLegacyNPTTransition(flags.captureLegacyNPT, flags.producerPID, flags.producerSHA256, journal, edges, mappings); err != nil {
			return fmt.Errorf("capture legacy NPT transition: %w", err)
		}
		return nil
	}
	if err := netif.ApplyLegacyNPTTransition(flags.adoptLegacyNPT, journal, edges, mappings); err != nil {
		return fmt.Errorf("adopt legacy NPT transition: %w", err)
	}
	return nil
}

func readLegacyMappedTransition(ctx context.Context, log *slog.Logger, cfg *config.Config, settings npt.Config) ([]netif.LegacyMappedAddress, error) {
	var result []netif.LegacyMappedAddress
	for _, wan := range settings.WANs {
		if wan.TranslationV4 == nil || len(wan.TranslationV4.StaticMappings) == 0 {
			continue
		}
		for _, connection := range cfg.IfMgr.Connections {
			if connection.ID != wan.ID || connection.Name != wan.Iface {
				continue
			}
			if connection.Owner != interfaceintent.OwnerNetworkd {
				return nil, fmt.Errorf("mapped producer transition requires unchanged networkd ownership for %s", connection.ID)
			}
			prefixes, err := legacyMappedTransitionPrefixes(ctx, log, cfg, connection, wan.TranslationV4.StaticMappings)
			if err != nil {
				return nil, err
			}
			intent, err := json.Marshal(struct {
				Connection  interfaceintent.Connection
				Translation config.IPv4Translation
			}{connection, *wan.TranslationV4})
			if err != nil {
				return nil, netif.NewLegacyNPTError("marshal legacy mapping intent", err)
			}
			digest := sha256.Sum256(intent)
			observed, err := netif.ReadLegacyMappedAddresses(connection, prefixes, hex.EncodeToString(digest[:]))
			if err != nil {
				return nil, err
			}
			result = append(result, observed...)
		}
	}
	return result, nil
}

func legacyMappedTransitionPrefixes(ctx context.Context, log *slog.Logger, cfg *config.Config, connection interfaceintent.Connection, mappings []config.StaticMapping) ([]netip.Prefix, error) {
	current, err := netif.ListAddrs(ctx, log, connection.Name)
	if err != nil {
		return nil, err
	}
	primary := make(map[netip.Addr]bool)
	for _, address := range current {
		if address.Family != "inet" {
			continue
		}
		prefix, err := netip.ParsePrefix(address.CIDR)
		if err != nil {
			return nil, netif.NewLegacyNPTError("parse legacy mapping subnet", err)
		}
		if prefix.Bits() < 32 {
			primary[prefix.Addr()] = true
		}
	}
	if connection.IPv4 != nil {
		for _, address := range connection.IPv4.Addresses {
			primary[address.Prefix.Addr()] = true
		}
		if connection.IPv4.DHCP != nil && *connection.IPv4.DHCP {
			if cfg.Watchdog.ConnectivityTimeoutSeconds <= 0 {
				return nil, fmt.Errorf("legacy mapping observation requires a positive networkd timeout")
			}
			bounded, cancel := context.WithTimeout(ctx, time.Duration(cfg.Watchdog.ConnectivityTimeoutSeconds)*time.Second)
			addresses, err := networkd.Addresses(bounded, connection.Name)
			cancel()
			if err != nil {
				return nil, err
			}
			configured := false
			for _, address := range addresses {
				if address.Prefix.Addr().Is4() && address.ConfigSource == "DHCPv4" && address.ConfigState == "configured" {
					primary[address.Prefix.Addr()] = true
					configured = true
				}
			}
			if !configured {
				return nil, fmt.Errorf("networkd DHCPv4 primary address is not configured")
			}
		}
	}
	var external []netip.Addr
	for _, mapping := range mappings {
		if mapping.Delivery != interfaceintent.DeliveryRouted {
			external = append(external, mapping.External)
		}
	}
	onLink, err := netif.OnLinkMappedAddresses(current, external)
	if err != nil {
		return nil, err
	}
	var prefixes []netip.Prefix
	for _, mapping := range mappings {
		if primary[mapping.External] || mapping.Delivery == interfaceintent.DeliveryRouted {
			continue
		}
		if mapping.Delivery != interfaceintent.DeliveryLocal && !slices.Contains(onLink, mapping.External) {
			continue
		}
		prefixes = append(prefixes, netip.PrefixFrom(mapping.External, 32))
	}
	return prefixes, nil
}
