package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sort"
	"strings"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
)

// WriteDebugText reports output failures shared by the inspection renderers.
func WriteDebugText(output io.Writer, text string) error {
	if _, err := io.WriteString(output, text); err != nil {
		slog.Warn("write debug output failed", "err", err)
		return fmt.Errorf("write debug text: %w", err)
	}
	return nil
}

func writeDebugNPTText(output io.Writer, table npt.RenderedTable) error {
	if len(table.Prerouting) == 0 && len(table.Postrouting) == 0 {
		return nil
	}
	var text strings.Builder
	fmt.Fprintln(&text, "table ip6 nat {\n    chain prerouting {")
	for _, line := range table.Prerouting {
		fmt.Fprintf(&text, "        %s\n", line)
	}
	fmt.Fprintln(&text, "    }\n    chain postrouting {")
	for _, line := range table.Postrouting {
		fmt.Fprintf(&text, "        %s\n", line)
	}
	fmt.Fprintln(&text, "    }\n}")
	return WriteDebugText(output, text.String())
}

func writeDebugPrefixesText(output io.Writer, result debugPrefixesResult) error {
	var text strings.Builder
	for _, record := range result.Prefixes {
		value := "none"
		if record.Present {
			value = record.Prefix.String()
		}
		fmt.Fprintf(&text, "%s: %s\n", record.WAN.Iface, value)
	}
	return WriteDebugText(output, text.String())
}

func writeDebugRoutesText(output io.Writer, result debugRoutesResult) error {
	var text strings.Builder
	printDebugRoutes(&text, result.Routes)
	return WriteDebugText(output, text.String())
}

func writeDebugPolicyText(output io.Writer, result debugPolicyResult) error {
	var text strings.Builder
	fmt.Fprintln(&text, "== IPv4 rules ==")
	printDebugRules(&text, result.IPv4)
	fmt.Fprintln(&text, "\n== IPv6 rules ==")
	printDebugRules(&text, result.IPv6)
	for _, table := range result.Tables {
		fmt.Fprintf(&text, "\n== table %d (%s) v4 ==\n", table.WAN.TableID, table.WAN.Name)
		printDebugRoutes(&text, table.IPv4)
		fmt.Fprintf(&text, "== table %d (%s) v6 ==\n", table.WAN.TableID, table.WAN.Name)
		printDebugRoutes(&text, table.IPv6)
	}
	return WriteDebugText(output, text.String())
}

func printDebugRules(output io.Writer, rules []netif.CurrentRule) {
	for _, rule := range rules {
		from := "all"
		if rule.From != "" {
			from = rule.From
		}
		fmt.Fprintf(output, "%d: from %s", rule.Priority, from)
		if rule.Mark != 0 {
			fmt.Fprintf(output, " fwmark %#x", rule.Mark)
		}
		if rule.IifName != "" {
			fmt.Fprintf(output, " iif %s", rule.IifName)
		}
		if rule.UIDRange != "" {
			fmt.Fprintf(output, " uidrange %s", rule.UIDRange)
		}
		fmt.Fprintf(output, " lookup %d\n", rule.TableID)
	}
}

func printDebugRoutes(output io.Writer, routes []netif.CurrentRoute) {
	for _, route := range routes {
		fmt.Fprint(output, route.Dest)
		if route.Via != "" {
			fmt.Fprintf(output, " via %s", route.Via)
		}
		if route.Dev != "" {
			fmt.Fprintf(output, " dev %s", route.Dev)
		}
		if route.Metric != 0 {
			fmt.Fprintf(output, " metric %d", route.Metric)
		}
		fmt.Fprintln(output)
	}
}

func writeDebugStatusText(output io.Writer, result debugStatusResult) error {
	var text strings.Builder
	for index, record := range result.Interfaces {
		if index > 0 {
			fmt.Fprintln(&text)
		}
		fmt.Fprintf(&text, "== %s ==\noper: %s\ncarrier: %s\nglobal addresses:\n", record.WAN.Iface, record.State.OperState, debugCarrierState(record.State.Carrier))
		global := debugGlobalAddresses(record.Addresses)
		if len(global) == 0 {
			fmt.Fprintln(&text, "  none")
		}
		for _, address := range global {
			fmt.Fprintf(&text, "  %s\n", address)
		}
		value := "none"
		if record.PrefixPresent {
			value = record.Prefix.String()
		}
		fmt.Fprintf(&text, "delegated prefix: %s\n", value)
	}
	return WriteDebugText(output, text.String())
}

func debugCarrierState(carrier bool) string {
	if carrier {
		return "up"
	}
	return "down"
}

func debugGlobalAddresses(addresses []netif.CurrentAddr) []string {
	global := make([]string, 0, len(addresses))
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.CIDR)
		if err != nil || !prefix.Addr().IsGlobalUnicast() {
			continue
		}
		global = append(global, address.CIDR)
	}
	sort.Strings(global)
	return global
}

func writeDebugStatsText(output io.Writer, result debugStatsResult) error {
	var text strings.Builder
	for index, record := range result.Interfaces {
		if index > 0 {
			fmt.Fprintln(&text)
		}
		stats := record.Stats
		fmt.Fprintf(&text, "== %s ==\nRX: bytes %d packets %d errors %d dropped %d\nTX: bytes %d packets %d errors %d dropped %d\n", record.WAN.Iface, stats.RxBytes, stats.RxPackets, stats.RxErrors, stats.RxDropped, stats.TxBytes, stats.TxPackets, stats.TxErrors, stats.TxDropped)
	}
	return WriteDebugText(output, text.String())
}

func writeDebugSimulationText(output io.Writer, result debugSimulationResult) error {
	if !result.SourcePresent {
		return nil
	}
	var text strings.Builder
	printDebugSimulationHeader(&text, result.Family, result.Target)
	for _, lookup := range result.Lookups {
		if lookup.WAN != nil {
			fmt.Fprintf(&text, "-- mark %d (%s) --\n", lookup.Mark, lookup.WAN.Name)
		}
		printDebugRouteLookup(&text, result.Target, lookup.Route, lookup.Present)
	}
	return WriteDebugText(output, text.String())
}

func debugSimulationName(family string) string {
	if family == debugFamilyV6 {
		return "sim6"
	}
	return "sim4"
}

func printDebugSimulationHeader(output io.Writer, family, target string) {
	if family == debugFamilyV6 {
		fmt.Fprintf(output, "== ip -6 route get %s from LAN ==\n", target)
		return
	}
	fmt.Fprintf(output, "== ip route get %s from LAN ==\n", target)
}

func printDebugRouteLookup(output io.Writer, target string, result netif.RouteLookupResult, ok bool) {
	if !ok {
		fmt.Fprintf(output, "%s: no route\n", target)
		return
	}
	fmt.Fprintf(output, "%s via %s oif %s src %s\n", target, debugValueOrNone(result.Gateway), debugValueOrNone(result.OIF), debugValueOrNone(result.Source))
}

func debugValueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func showDebugNPT(ctx context.Context, output io.Writer, logger *slog.Logger) error {
	result, err := collectDebugNPT(ctx, logger)
	if err != nil {
		return err
	}
	return writeDebugNPTText(output, result)
}

func showDebugRoutes(ctx context.Context, output io.Writer, logger *slog.Logger) error {
	result, err := collectDebugRoutes(ctx, logger)
	if err != nil {
		return err
	}
	return writeDebugRoutesText(output, result)
}

func showDebugPolicy(ctx context.Context, output io.Writer, logger *slog.Logger, cfg *config.Config) error {
	result, err := collectDebugPolicy(ctx, logger, cfg)
	if err != nil {
		return err
	}
	return writeDebugPolicyText(output, result)
}
