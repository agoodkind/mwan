package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/netip"
	"os"
	"sort"
	"strings"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/pd"
)

const (
	debugFamilyV4 = "inet"
	debugFamilyV6 = "inet6"
	debugTargetV4 = "1.1.1.1"
	debugTargetV6 = "2606:4700:4700::1111"
)

type debugView string

const (
	debugViewConnectivity debugView = "connectivity"
	debugViewCurl4        debugView = "curl4"
	debugViewCurl6        debugView = "curl6"
	debugViewLB4          debugView = "lb4"
	debugViewLB4Ifaces    debugView = "lb4-ifaces"
	debugViewLB6          debugView = "lb6"
	debugViewLB6Ifaces    debugView = "lb6-ifaces"
	debugViewNPT          debugView = "npt"
	debugViewPing4        debugView = "ping4"
	debugViewPing6        debugView = "ping6"
	debugViewPolicy       debugView = "policy"
	debugViewPrefixes     debugView = "prefixes"
	debugViewRoutes       debugView = "routes"
	debugViewSim4         debugView = "sim4"
	debugViewSim6         debugView = "sim6"
	debugViewStats        debugView = "stats"
	debugViewStatus       debugView = "status"
	debugViewSystemd      debugView = "systemd"
	debugViewTraceStart   debugView = "trace-start"
	debugViewTraceStop    debugView = "trace-stop"
	debugViewTraceTail    debugView = "trace-tail"
)

type debugProbeDispatcher func(
	context.Context,
	io.Writer,
	*slog.Logger,
	*config.Config,
	string,
	[]string,
) error

func runDebug(args []string, cfg *config.Config) int {
	return runDebugWithWriters(
		args,
		cfg,
		os.Stdout,
		os.Stderr,
		runDebugProbeView,
	)
}

func runDebugWithWriters(
	args []string,
	cfg *config.Config,
	output io.Writer,
	diagnostics io.Writer,
	probeDispatcher debugProbeDispatcher,
) int {
	logger := slog.New(slog.NewTextHandler(diagnostics, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	if len(args) < 1 || args[0] == "" {
		printDebugUsage(diagnostics)
		return 1
	}
	loadDebugNetworkConfig(diagnostics, cfg)

	ctx := context.Background()
	view := debugView(args[0])
	var err error
	switch view {
	case debugViewNPT:
		err = showDebugNPT(ctx, output, logger)
	case debugViewPrefixes:
		err = showDebugPrefixes(ctx, output, logger, cfg)
	case debugViewRoutes:
		err = showDebugRoutes(ctx, output, logger)
	case debugViewPolicy:
		err = showDebugPolicy(ctx, output, logger, cfg)
	case debugViewStatus:
		err = showDebugStatus(ctx, output, logger, cfg)
	case debugViewStats:
		err = showDebugStats(output, logger, cfg)
	case debugViewSim4:
		err = showDebugSimulation(ctx, output, diagnostics, logger, cfg, debugFamilyV4)
	case debugViewSim6:
		err = showDebugSimulation(ctx, output, diagnostics, logger, cfg, debugFamilyV6)
	case debugViewSystemd:
		err = showDebugSystemd(ctx, output, logger)
	case debugViewTraceStart:
		err = showDebugTraceStart(ctx, output, logger, cfg, args[1:])
	case debugViewTraceTail:
		err = showDebugTraceTail(ctx, output, logger, cfg, args[1:])
	case debugViewTraceStop:
		err = showDebugTraceStop(output)
	case debugViewConnectivity,
		debugViewPing4,
		debugViewPing6,
		debugViewCurl4,
		debugViewCurl6,
		debugViewLB4,
		debugViewLB6,
		debugViewLB4Ifaces,
		debugViewLB6Ifaces:
		err = probeDispatcher(
			ctx,
			output,
			logger,
			cfg,
			string(view),
			args[1:],
		)
	default:
		printDebugUsage(diagnostics)
		return 1
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan debug %s: %v\n", view, err)
		return 1
	}
	return 0
}

// loadDebugNetworkConfig fills the network tree the provider views read. A
// diagnostic run must survive a gateway whose network configuration is missing
// or broken, because that is one of the states an operator runs it to inspect,
// so a failure is reported and the views that need no provider still run. The
// tree is left untouched on failure, which is what keeps the WAN lists visibly
// empty rather than half filled.
func loadDebugNetworkConfig(diagnostics io.Writer, cfg *config.Config) {
	if err := networkjson.ApplyDefault(cfg); err != nil {
		fmt.Fprintf(
			diagnostics,
			"mwan debug: network configuration unreadable, "+
				"WAN lists and route simulations will be empty: %v\n",
			err,
		)
	}
}

func printDebugUsage(output io.Writer) {
	fmt.Fprintln(
		output,
		"usage: mwan debug "+
			"<npt|prefixes|routes|policy|status|stats|sim4|sim6|"+
			"connectivity|ping4|ping6|curl4|curl6|lb4|lb6|"+
			"lb4-ifaces|lb6-ifaces|systemd|"+
			"trace-start|trace-tail|trace-stop>",
	)
}

func showDebugNPT(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
) error {
	table, err := npt.RenderTable(ctx, logger)
	if err != nil {
		return debugWrappedError(logger, "read ip6 nat table", err)
	}
	if len(table.Prerouting) == 0 && len(table.Postrouting) == 0 {
		return nil
	}

	fmt.Fprintln(output, "table ip6 nat {")
	fmt.Fprintln(output, "    chain prerouting {")
	for _, line := range table.Prerouting {
		fmt.Fprintf(output, "        %s\n", line)
	}
	fmt.Fprintln(output, "    }")
	fmt.Fprintln(output, "    chain postrouting {")
	for _, line := range table.Postrouting {
		fmt.Fprintf(output, "        %s\n", line)
	}
	fmt.Fprintln(output, "    }")
	fmt.Fprintln(output, "}")
	return nil
}

func showDebugPrefixes(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
) error {
	source := pd.New(logger, pd.ReadOnly())
	for _, wan := range debugWANs(cfg) {
		prefix, ok, err := source.Prefix(ctx, wan.Iface)
		if err != nil {
			message := wan.Iface + ": delegated prefix"
			return debugWrappedError(logger, message, err)
		}
		value := "none"
		if ok {
			value = prefix.String()
		}
		fmt.Fprintf(output, "%s: %s\n", wan.Iface, value)
	}
	return nil
}

func showDebugRoutes(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
) error {
	routes, err := netif.ListDHCPRoutes(ctx, logger, debugFamilyV6)
	if err != nil {
		return debugWrappedError(logger, "list IPv6 DHCP routes", err)
	}
	printDebugRoutes(output, routes)
	return nil
}

func showDebugPolicy(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
) error {
	fmt.Fprintln(output, "== IPv4 rules ==")
	rulesV4, err := netif.ListRules(ctx, logger, debugFamilyV4)
	if err != nil {
		return debugWrappedError(logger, "list IPv4 rules", err)
	}
	printDebugRules(output, rulesV4)

	fmt.Fprintln(output)
	fmt.Fprintln(output, "== IPv6 rules ==")
	rulesV6, err := netif.ListRules(ctx, logger, debugFamilyV6)
	if err != nil {
		return debugWrappedError(logger, "list IPv6 rules", err)
	}
	printDebugRules(output, rulesV6)

	for _, wan := range debugWANs(cfg) {
		fmt.Fprintln(output)
		fmt.Fprintf(output, "== table %d (%s) v4 ==\n", wan.TableID, wan.Name)
		routesV4, err := netif.ListTableRoutes(ctx, logger, debugFamilyV4, wan.TableID)
		if err != nil {
			message := fmt.Sprintf("table %d (%s) v4", wan.TableID, wan.Name)
			return debugWrappedError(logger, message, err)
		}
		printDebugRoutes(output, routesV4)

		fmt.Fprintf(output, "== table %d (%s) v6 ==\n", wan.TableID, wan.Name)
		routesV6, err := netif.ListTableRoutes(ctx, logger, debugFamilyV6, wan.TableID)
		if err != nil {
			message := fmt.Sprintf("table %d (%s) v6", wan.TableID, wan.Name)
			return debugWrappedError(logger, message, err)
		}
		printDebugRoutes(output, routesV6)
	}
	return nil
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

func showDebugStatus(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
) error {
	source := pd.New(logger, pd.ReadOnly())
	for index, wan := range debugWANs(cfg) {
		if index > 0 {
			fmt.Fprintln(output)
		}
		fmt.Fprintf(output, "== %s ==\n", wan.Iface)

		state, err := netif.ReadLinkState(logger, wan.Iface)
		if err != nil {
			message := wan.Iface + ": link state"
			return debugWrappedError(logger, message, err)
		}
		fmt.Fprintf(output, "oper: %s\n", state.OperState)
		fmt.Fprintf(output, "carrier: %s\n", debugCarrierState(state.Carrier))

		addresses, err := netif.ListAddrs(ctx, logger, wan.Iface)
		if err != nil {
			message := wan.Iface + ": addresses"
			return debugWrappedError(logger, message, err)
		}
		fmt.Fprintln(output, "global addresses:")
		global := debugGlobalAddresses(addresses)
		if len(global) == 0 {
			fmt.Fprintln(output, "  none")
		} else {
			for _, address := range global {
				fmt.Fprintf(output, "  %s\n", address)
			}
		}

		prefix, ok, err := source.Prefix(ctx, wan.Iface)
		if err != nil {
			message := wan.Iface + ": delegated prefix"
			return debugWrappedError(logger, message, err)
		}
		prefixValue := "none"
		if ok {
			prefixValue = prefix.String()
		}
		fmt.Fprintf(output, "delegated prefix: %s\n", prefixValue)
	}
	return nil
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

func showDebugStats(
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
) error {
	for index, wan := range debugWANs(cfg) {
		if index > 0 {
			fmt.Fprintln(output)
		}
		fmt.Fprintf(output, "== %s ==\n", wan.Iface)
		statistics, err := netif.ReadLinkStats(logger, wan.Iface)
		if err != nil {
			message := wan.Iface + ": link statistics"
			return debugWrappedError(logger, message, err)
		}
		fmt.Fprintf(
			output,
			"RX: bytes %d packets %d errors %d dropped %d\n",
			statistics.RxBytes,
			statistics.RxPackets,
			statistics.RxErrors,
			statistics.RxDropped,
		)
		fmt.Fprintf(
			output,
			"TX: bytes %d packets %d errors %d dropped %d\n",
			statistics.TxBytes,
			statistics.TxPackets,
			statistics.TxErrors,
			statistics.TxDropped,
		)
	}
	return nil
}

func showDebugSimulation(
	ctx context.Context,
	output io.Writer,
	diagnostics io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	family string,
) error {
	target, source, sourceLabel, ok, err := debugSimulationInputs(logger, cfg, family)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(
			diagnostics,
			"mwan debug %s: %s is unset; skipping probes\n",
			debugSimulationName(family),
			sourceLabel,
		)
		return nil
	}

	printDebugSimulationHeader(output, family, target)
	result, ok, err := netif.RouteLookup(ctx, logger, family, target, source, 0)
	if err != nil {
		return debugWrappedError(logger, "default lookup", err)
	}
	printDebugRouteLookup(output, target, result, ok)

	for _, wan := range debugWANs(cfg) {
		if wan.FwMark < 0 || uint64(wan.FwMark) > math.MaxUint32 {
			return fmt.Errorf("WAN %s fw_mark %d is outside uint32", wan.Name, wan.FwMark)
		}
		fmt.Fprintf(output, "-- mark %d (%s) --\n", wan.FwMark, wan.Name)
		result, ok, err := netif.RouteLookup(
			ctx,
			logger,
			family,
			target,
			source,
			uint32(wan.FwMark),
		)
		if err != nil {
			message := fmt.Sprintf("mark %d (%s)", wan.FwMark, wan.Name)
			return debugWrappedError(logger, message, err)
		}
		printDebugRouteLookup(output, target, result, ok)
	}
	return nil
}

func debugSimulationInputs(
	logger *slog.Logger,
	cfg *config.Config,
	family string,
) (target string, source string, sourceLabel string, ok bool, err error) {
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

func debugSimulationName(family string) string {
	if family == debugFamilyV6 {
		return "sim6"
	}
	return "sim4"
}

func printDebugSimulationHeader(output io.Writer, family string, target string) {
	if family == debugFamilyV6 {
		fmt.Fprintf(output, "== ip -6 route get %s from LAN ==\n", target)
		return
	}
	fmt.Fprintf(output, "== ip route get %s from LAN ==\n", target)
}

func printDebugRouteLookup(
	output io.Writer,
	target string,
	result netif.RouteLookupResult,
	ok bool,
) {
	if !ok {
		fmt.Fprintf(output, "%s: no route\n", target)
		return
	}
	fmt.Fprintf(
		output,
		"%s via %s oif %s src %s\n",
		target,
		debugValueOrNone(result.Gateway),
		debugValueOrNone(result.OIF),
		debugValueOrNone(result.Source),
	)
}

func debugValueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
