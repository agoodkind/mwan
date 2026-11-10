package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
)

// WriteDebugText reports output failures shared by the inspection renderers.
func WriteDebugText(logger *slog.Logger, output io.Writer, text string) error {
	if _, err := io.WriteString(output, text); err != nil {
		logger.Warn("write debug output failed", "err", err)
		return fmt.Errorf("write debug text: %w", err)
	}

	return nil
}

func writeDebugNPTText(logger *slog.Logger, output io.Writer, table npt.RenderedTable) error {
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

	return WriteDebugText(logger, output, text.String())
}

func writeDebugPrefixesText(
	logger *slog.Logger,
	output io.Writer,
	result debugPrefixesResult,
) error {
	var text strings.Builder
	fmt.Fprintf(&text, "prefix records: %d\n", len(result.Prefixes))

	for _, record := range result.Prefixes {
		printDebugWAN(&text, record.WAN)
		fmt.Fprintf(&text, "  present: %t\n", record.Present)
		if record.Prefix.IsValid() {
			fmt.Fprintf(&text, "  prefix: %q\n", record.Prefix)
		}
	}

	return WriteDebugText(logger, output, text.String())
}

func writeDebugRoutesText(logger *slog.Logger, output io.Writer, result debugRoutesResult) error {
	var text strings.Builder
	fmt.Fprintf(&text, "family: %q\n", result.Family)
	printDebugRoutes(&text, result.Routes)

	return WriteDebugText(logger, output, text.String())
}

func writeDebugPolicyText(logger *slog.Logger, output io.Writer, result debugPolicyResult) error {
	var text strings.Builder
	fmt.Fprintln(&text, "== IPv4 rules ==")
	printDebugRules(&text, result.IPv4)
	fmt.Fprintln(&text, "\n== IPv6 rules ==")
	printDebugRules(&text, result.IPv6)
	fmt.Fprintf(&text, "\nWAN tables: %d\n", len(result.Tables))

	for _, table := range result.Tables {
		printDebugWAN(&text, table.WAN)
		fmt.Fprintf(&text, "  table ID: %d\n", table.WAN.TableID)
		fmt.Fprintln(&text, "== IPv4 routes ==")
		printDebugRoutes(&text, table.IPv4)
		fmt.Fprintln(&text, "== IPv6 routes ==")
		printDebugRoutes(&text, table.IPv6)
	}

	return WriteDebugText(logger, output, text.String())
}

func printDebugRules(output io.Writer, rules []netif.CurrentRule) {
	fmt.Fprintf(output, "rules: %d\n", len(rules))

	for _, rule := range rules {
		fmt.Fprintf(
			output,
			"  priority: %d\n  from: %q\n  mark: %d\n  input interface: %q\n  UID range: %q\n  table ID: %d\n",
			rule.Priority,
			rule.From,
			rule.Mark,
			rule.IifName,
			rule.UIDRange,
			rule.TableID,
		)
	}
}

func printDebugRoutes(output io.Writer, routes []netif.CurrentRoute) {
	fmt.Fprintf(output, "routes: %d\n", len(routes))

	for _, route := range routes {
		fmt.Fprintf(
			output,
			"  family: %q\n  destination: %q\n  gateway: %q\n  device: %q\n  table ID: %d\n  protocol: %d\n  metric: %d\n  scope: %d\n  type: %d\n  next hops: %d\n",
			route.Family,
			route.Dest,
			route.Via,
			route.Dev,
			route.TableID,
			route.Protocol,
			route.Metric,
			route.Scope,
			route.Type,
			len(route.NextHops),
		)

		for _, hop := range route.NextHops {
			fmt.Fprintf(
				output,
				"    gateway: %q\n    device: %q\n    link index: %d\n    weight: %d\n",
				hop.Via,
				hop.Dev,
				hop.LinkIndex,
				hop.Weight,
			)
		}
	}
}

func printDebugWAN(output io.Writer, wan debugWAN) {
	fmt.Fprintf(
		output,
		"WAN:\n  ID: %q\n  name: %q\n  interface: %q\n",
		wan.ID,
		wan.Name,
		wan.Iface,
	)
}

func writeDebugStatusText(logger *slog.Logger, output io.Writer, result debugStatusResult) error {
	var text strings.Builder
	fmt.Fprintf(&text, "interfaces: %d\n", len(result.Interfaces))

	for index, record := range result.Interfaces {
		if index > 0 {
			fmt.Fprintln(&text)
		}

		printDebugWAN(&text, record.WAN)
		fmt.Fprintf(
			&text,
			"oper: %q\ncarrier: %t\naddresses: %d\n",
			record.State.OperState,
			record.State.Carrier,
			len(record.Addresses),
		)

		for _, address := range record.Addresses {
			fmt.Fprintf(
				&text,
				"  CIDR: %q\n  family: %q\n  flags: %d\n  scope: %d\n  preferred lifetime (s): %d\n  valid lifetime (s): %d\n  observed at: %s\n  origin: %q\n",
				address.CIDR,
				address.Family,
				address.Flags,
				address.Scope,
				address.PreferredLifetime,
				address.ValidLifetime,
				address.ObservedAt.Format(time.RFC3339Nano),
				address.Origin,
			)
		}

		fmt.Fprintf(&text, "prefix present: %t\n", record.PrefixPresent)
		if record.Prefix.IsValid() {
			fmt.Fprintf(&text, "delegated prefix: %q\n", record.Prefix)
		}
	}

	return WriteDebugText(logger, output, text.String())
}

func writeDebugStatsText(logger *slog.Logger, output io.Writer, result debugStatsResult) error {
	var text strings.Builder
	fmt.Fprintf(&text, "interfaces: %d\n", len(result.Interfaces))

	for index, record := range result.Interfaces {
		if index > 0 {
			fmt.Fprintln(&text)
		}

		stats := record.Stats
		printDebugWAN(&text, record.WAN)
		fmt.Fprintf(
			&text,
			"RX: bytes %d packets %d errors %d dropped %d\nTX: bytes %d packets %d errors %d dropped %d\n",
			stats.RxBytes,
			stats.RxPackets,
			stats.RxErrors,
			stats.RxDropped,
			stats.TxBytes,
			stats.TxPackets,
			stats.TxErrors,
			stats.TxDropped,
		)
	}

	return WriteDebugText(logger, output, text.String())
}

func writeDebugSimulationText(
	logger *slog.Logger,
	output io.Writer,
	result debugSimulationResult,
) error {
	var text strings.Builder
	fmt.Fprintf(
		&text,
		"family: %q\ntarget: %q\nsource: %q\nsource present: %t\nlookups: %d\n",
		result.Family,
		result.Target,
		result.Source,
		result.SourcePresent,
		len(result.Lookups),
	)

	for _, lookup := range result.Lookups {
		if lookup.WAN != nil {
			printDebugWAN(&text, *lookup.WAN)
		} else {
			fmt.Fprintln(&text, "default lookup:")
		}

		fmt.Fprintf(&text, "  mark: %d\n  present: %t\n", lookup.Mark, lookup.Present)
		fmt.Fprintf(
			&text,
			"  gateway: %q\n  output interface: %q\n  source: %q\n",
			lookup.Route.Gateway,
			lookup.Route.OIF,
			lookup.Route.Source,
		)
	}

	return WriteDebugText(logger, output, text.String())
}

func debugSimulationName(family string) string {
	if family == debugFamilyV6 {
		return "sim6"
	}

	return "sim4"
}

func showDebugNPT(ctx context.Context, output io.Writer, logger *slog.Logger) error {
	result, err := collectDebugNPT(ctx, logger)
	if err != nil {
		return err
	}

	return writeDebugNPTText(logger, output, result)
}

func showDebugRoutes(ctx context.Context, output io.Writer, logger *slog.Logger) error {
	result, err := collectDebugRoutes(ctx, logger)
	if err != nil {
		return err
	}

	return writeDebugRoutesText(logger, output, result)
}

func showDebugPolicy(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
) error {
	result, err := collectDebugPolicy(ctx, logger, cfg)
	if err != nil {
		return err
	}

	return writeDebugPolicyText(logger, output, result)
}
