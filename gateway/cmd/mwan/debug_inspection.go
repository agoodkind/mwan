package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/netip"
	"strings"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/pd"
)

type debugPrefixRecord struct {
	WAN     debugWAN     `json:"wan"`
	Prefix  netip.Prefix `json:"prefix"`
	Present bool         `json:"present"`
}
type debugPrefixesResult struct {
	Prefixes []debugPrefixRecord `json:"prefixes"`
}
type debugRoutesResult struct {
	Family string               `json:"family"`
	Routes []netif.CurrentRoute `json:"routes"`
}
type debugPolicyTable struct {
	WAN  debugWAN             `json:"wan"`
	IPv4 []netif.CurrentRoute `json:"ipv4"`
	IPv6 []netif.CurrentRoute `json:"ipv6"`
}
type debugPolicyResult struct {
	IPv4   []netif.CurrentRule `json:"ipv4"`
	IPv6   []netif.CurrentRule `json:"ipv6"`
	Tables []debugPolicyTable  `json:"tables"`
}
type debugStatusRecord struct {
	WAN           debugWAN            `json:"wan"`
	State         netif.LinkState     `json:"state"`
	Addresses     []netif.CurrentAddr `json:"addresses"`
	Prefix        netip.Prefix        `json:"prefix"`
	PrefixPresent bool                `json:"prefix_present"`
}
type debugStatusResult struct {
	Interfaces []debugStatusRecord `json:"interfaces"`
}
type debugStatsRecord struct {
	WAN   debugWAN        `json:"wan"`
	Stats netif.LinkStats `json:"stats"`
}
type debugStatsResult struct {
	Interfaces []debugStatsRecord `json:"interfaces"`
}
type debugLookupRecord struct {
	WAN     *debugWAN               `json:"wan"`
	Mark    uint32                  `json:"mark"`
	Route   netif.RouteLookupResult `json:"route"`
	Present bool                    `json:"present"`
}
type debugSimulationResult struct {
	Family        string              `json:"family"`
	Target        string              `json:"target"`
	Source        string              `json:"source"`
	SourceField   string              `json:"source_field"`
	SourcePresent bool                `json:"source_present"`
	Lookups       []debugLookupRecord `json:"lookups"`
}

func collectDebugNPT(ctx context.Context, logger *slog.Logger) (npt.RenderedTable, error) {
	table, err := npt.RenderTable(ctx, logger)
	if err != nil {
		return table, debugWrappedError(logger, "read ip6 nat table", err)
	}
	return table, nil
}

func collectDebugPrefixes(ctx context.Context, logger *slog.Logger, cfg *config.Config) (debugPrefixesResult, error) {
	result := debugPrefixesResult{Prefixes: make([]debugPrefixRecord, 0)}
	source := pd.New(logger)
	for _, wan := range debugWANs(cfg) {
		prefix, present, err := source.Prefix(ctx, wan.Iface)
		if err != nil {
			return result, debugWrappedError(logger, wan.Iface+": delegated prefix", err)
		}
		result.Prefixes = append(result.Prefixes, debugPrefixRecord{WAN: wan, Prefix: prefix, Present: present})
	}
	return result, nil
}

func collectDebugRoutes(ctx context.Context, logger *slog.Logger) (debugRoutesResult, error) {
	routes, err := netif.ListDHCPRoutes(ctx, logger, debugFamilyV6)
	if err != nil {
		return debugRoutesResult{}, debugWrappedError(logger, "list IPv6 DHCP routes", err)
	}
	return debugRoutesResult{Family: debugFamilyV6, Routes: routes}, nil
}

func collectDebugPolicy(ctx context.Context, logger *slog.Logger, cfg *config.Config) (debugPolicyResult, error) {
	result := debugPolicyResult{Tables: make([]debugPolicyTable, 0)}
	var err error
	result.IPv4, err = netif.ListRules(ctx, logger, debugFamilyV4)
	if err != nil {
		return result, debugWrappedError(logger, "list IPv4 rules", err)
	}
	result.IPv6, err = netif.ListRules(ctx, logger, debugFamilyV6)
	if err != nil {
		return result, debugWrappedError(logger, "list IPv6 rules", err)
	}
	for _, wan := range debugWANs(cfg) {
		table := debugPolicyTable{WAN: wan}
		table.IPv4, err = netif.ListTableRoutes(ctx, logger, debugFamilyV4, wan.TableID)
		if err != nil {
			return result, debugWrappedError(logger, fmt.Sprintf("table %d (%s) v4", wan.TableID, wan.Name), err)
		}
		table.IPv6, err = netif.ListTableRoutes(ctx, logger, debugFamilyV6, wan.TableID)
		if err != nil {
			return result, debugWrappedError(logger, fmt.Sprintf("table %d (%s) v6", wan.TableID, wan.Name), err)
		}
		result.Tables = append(result.Tables, table)
	}
	return result, nil
}

func collectDebugStatus(ctx context.Context, logger *slog.Logger, cfg *config.Config) (debugStatusResult, error) {
	result := debugStatusResult{Interfaces: make([]debugStatusRecord, 0)}
	source := pd.New(logger)
	for _, wan := range debugWANs(cfg) {
		record := debugStatusRecord{WAN: wan}
		var err error
		record.State, err = netif.ReadLinkState(logger, wan.Iface)
		if err != nil {
			return result, debugWrappedError(logger, wan.Iface+": link state", err)
		}
		record.Addresses, err = netif.ListAddrs(ctx, logger, wan.Iface)
		if err != nil {
			return result, debugWrappedError(logger, wan.Iface+": addresses", err)
		}
		record.Prefix, record.PrefixPresent, err = source.Prefix(ctx, wan.Iface)
		if err != nil {
			return result, debugWrappedError(logger, wan.Iface+": delegated prefix", err)
		}
		result.Interfaces = append(result.Interfaces, record)
	}
	return result, nil
}

func collectDebugStats(logger *slog.Logger, cfg *config.Config) (debugStatsResult, error) {
	result := debugStatsResult{Interfaces: make([]debugStatsRecord, 0)}
	for _, wan := range debugWANs(cfg) {
		stats, err := netif.ReadLinkStats(logger, wan.Iface)
		if err != nil {
			return result, debugWrappedError(logger, wan.Iface+": link statistics", err)
		}
		result.Interfaces = append(result.Interfaces, debugStatsRecord{WAN: wan, Stats: stats})
	}
	return result, nil
}

func collectDebugSimulation(ctx context.Context, logger *slog.Logger, cfg *config.Config, family string) (debugSimulationResult, error) {
	target, source, field, present, err := debugSimulationInputs(logger, cfg, family)
	result := debugSimulationResult{Family: family, Target: target, Source: source, SourceField: field, SourcePresent: present, Lookups: make([]debugLookupRecord, 0)}
	if err != nil || !present {
		return result, err
	}
	route, ok, err := netif.RouteLookup(ctx, logger, family, target, source, 0)
	if err != nil {
		return result, debugWrappedError(logger, "default lookup", err)
	}
	result.Lookups = append(result.Lookups, debugLookupRecord{Route: route, Present: ok})
	for _, wan := range debugWANs(cfg) {
		if wan.FwMark < 0 || uint64(wan.FwMark) > math.MaxUint32 {
			return result, fmt.Errorf("WAN %s fw_mark %d is outside uint32", wan.Name, wan.FwMark)
		}
		mark := uint32(wan.FwMark)
		route, ok, err = netif.RouteLookup(ctx, logger, family, target, source, mark)
		if err != nil {
			return result, debugWrappedError(logger, fmt.Sprintf("mark %d (%s)", wan.FwMark, wan.Name), err)
		}
		result.Lookups = append(result.Lookups, debugLookupRecord{WAN: &wan, Mark: mark, Route: route, Present: ok})
	}
	return result, nil
}

func debugSimulationInputs(logger *slog.Logger, cfg *config.Config, family string) (target string, source string, sourceLabel string, ok bool, err error) {
	if family == debugFamilyV6 {
		rawSource := strings.TrimSpace(cfg.IfMgr.OpnsenseEdgeV6)
		return debugTargetV6, rawSource, "ifmgr.opnsense_edge_v6", rawSource != "", nil
	}
	rawPrefix := ""
	if cfg.IfMgr.Modules.WAN != nil && cfg.IfMgr.Modules.WAN.Routes != nil {
		rawPrefix = strings.TrimSpace(cfg.IfMgr.Modules.WAN.Routes.InternalNetV4)
	}
	if rawPrefix == "" {
		return debugTargetV4, "", "ifmgr.modules.wan.routes.internal_net_v4", false, nil
	}
	source, err = debugIPv4Source(logger, rawPrefix)
	if err != nil {
		return "", "", "", false, err
	}
	return debugTargetV4, source, "ifmgr.modules.wan.routes.internal_net_v4", true, nil
}

func runDebugInspection(ctx context.Context, output, diagnostics io.Writer, logger *slog.Logger, cfg *config.Config, view debugView, format string) error {
	switch view {
	case debugViewNPT:
		result, err := collectDebugNPT(ctx, logger)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugNPTText)
	case debugViewPrefixes:
		result, err := collectDebugPrefixes(ctx, logger, cfg)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugPrefixesText)
	case debugViewRoutes:
		result, err := collectDebugRoutes(ctx, logger)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugRoutesText)
	case debugViewPolicy:
		result, err := collectDebugPolicy(ctx, logger, cfg)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugPolicyText)
	case debugViewStatus:
		result, err := collectDebugStatus(ctx, logger, cfg)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugStatusText)
	case debugViewStats:
		result, err := collectDebugStats(logger, cfg)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugStatsText)
	case debugViewSim4:
		return runDebugSimulationInspection(ctx, output, diagnostics, logger, cfg, debugFamilyV4, format)
	case debugViewSim6:
		return runDebugSimulationInspection(ctx, output, diagnostics, logger, cfg, debugFamilyV6, format)
	case debugViewSystemd:
		result, err := collectDebugSystemd(ctx, logger)
		return writeDebugInspectionResult(logger, output, format, result, err, writeDebugSystemdText)
	case debugViewConnectivity, debugViewCurl4, debugViewCurl6, debugViewLB4, debugViewLB4Ifaces, debugViewLB6, debugViewLB6Ifaces, debugViewPing4, debugViewPing6, debugViewTraceStart, debugViewTraceStop, debugViewTraceTail:
		return fmt.Errorf("debug view %q is not a passive inspection", view)
	default:
		return fmt.Errorf("unknown debug inspection %q", view)
	}
}

func writeDebugInspectionResult[T outputValue](logger *slog.Logger, output io.Writer, format string, result T, collectionError error, writeText func(*slog.Logger, io.Writer, T) error) error {
	if collectionError != nil {
		return collectionError
	}
	if format == "json" {
		return WriteJSON(output, result)
	}
	return writeText(logger, output, result)
}

func runDebugSimulationInspection(ctx context.Context, output, diagnostics io.Writer, logger *slog.Logger, cfg *config.Config, family, format string) error {
	result, err := collectDebugSimulation(ctx, logger, cfg, family)
	if err != nil {
		return err
	}
	if !result.SourcePresent {
		if _, err := fmt.Fprintf(diagnostics, "mwan debug %s: %s is unset; skipping probes\n", debugSimulationName(family), result.SourceField); err != nil {
			logger.Warn("write simulation diagnostic failed", "err", err)
			return fmt.Errorf("write simulation diagnostic: %w", err)
		}
	}
	return writeDebugInspectionResult(logger, output, format, result, nil, writeDebugSimulationText)
}
