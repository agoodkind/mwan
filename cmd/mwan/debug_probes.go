package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"text/tabwriter"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/pd"
)

const (
	debugConnectivityAttempts = 2
	debugPingAttempts         = 3
	debugLoadBalanceCount     = 6
	debugConnectivityTimeout  = 2 * time.Second
	debugActiveProbeTimeout   = 5 * time.Second
	debugNoValue              = "<none>"
)

type activeDebugProbeView string

const (
	activeDebugViewConnectivity activeDebugProbeView = "connectivity"
	activeDebugViewCurl4        activeDebugProbeView = "curl4"
	activeDebugViewCurl6        activeDebugProbeView = "curl6"
	activeDebugViewLB4          activeDebugProbeView = "lb4"
	activeDebugViewLB4Ifaces    activeDebugProbeView = "lb4-ifaces"
	activeDebugViewLB6          activeDebugProbeView = "lb6"
	activeDebugViewLB6Ifaces    activeDebugProbeView = "lb6-ifaces"
	activeDebugViewPing4        activeDebugProbeView = "ping4"
	activeDebugViewPing6        activeDebugProbeView = "ping6"
)

type debugPingFunc func(
	context.Context,
	string,
	netip.Addr,
	time.Duration,
) (time.Duration, error)

type debugHTTPGetFunc func(
	context.Context,
	string,
	string,
	string,
	time.Duration,
) (netif.HTTPResult, error)

type debugListAddrsFunc func(
	context.Context,
	*slog.Logger,
	string,
) ([]netif.CurrentAddr, error)

type debugRenderNPTFunc func(
	context.Context,
	*slog.Logger,
) (npt.RenderedTable, error)

type debugProbeDependencies struct {
	ping4        debugPingFunc
	ping6        debugPingFunc
	httpGet      debugHTTPGetFunc
	listAddrs    debugListAddrsFunc
	renderNPT    debugRenderNPTFunc
	prefixSource pd.Source
}

type debugConnectivityRow struct {
	wan    string
	iface  string
	ipv4   string
	ipv6   string
	ping4  string
	ping6  string
	npt    string
	prefix string
}

func newDebugProbeDependencies(logger *slog.Logger) debugProbeDependencies {
	return debugProbeDependencies{
		ping4:        netif.Ping4,
		ping6:        netif.Ping6,
		httpGet:      netif.HTTPGet,
		listAddrs:    netif.ListAddrs,
		renderNPT:    npt.RenderTable,
		prefixSource: pd.New(logger, pd.ReadOnly()),
	}
}

func runDebugProbeView(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	view string,
	args []string,
) error {
	dependencies := newDebugProbeDependencies(logger)
	return runDebugProbeViewWithDependencies(
		ctx,
		output,
		logger,
		cfg,
		view,
		args,
		dependencies,
	)
}

func runDebugProbeViewWithDependencies(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	view string,
	args []string,
	dependencies debugProbeDependencies,
) error {
	switch activeDebugProbeView(view) {
	case activeDebugViewConnectivity:
		if err := debugProbeNoArgs(view, args); err != nil {
			return err
		}
		return showDebugConnectivity(ctx, output, logger, cfg, dependencies)
	case activeDebugViewPing4:
		return dispatchDebugPing(
			ctx,
			output,
			cfg,
			view,
			args,
			debugFamilyV4,
			dependencies.ping4,
		)
	case activeDebugViewPing6:
		return dispatchDebugPing(
			ctx,
			output,
			cfg,
			view,
			args,
			debugFamilyV6,
			dependencies.ping6,
		)
	case activeDebugViewCurl4:
		return dispatchDebugCurl(
			ctx,
			output,
			cfg,
			view,
			args,
			debugFamilyV4,
			dependencies.httpGet,
		)
	case activeDebugViewCurl6:
		return dispatchDebugCurl(
			ctx,
			output,
			cfg,
			view,
			args,
			debugFamilyV6,
			dependencies.httpGet,
		)
	case activeDebugViewLB4:
		return dispatchDebugLoadBalance(
			ctx,
			output,
			view,
			args,
			debugFamilyV4,
			dependencies.httpGet,
		)
	case activeDebugViewLB6:
		return dispatchDebugLoadBalance(
			ctx,
			output,
			view,
			args,
			debugFamilyV6,
			dependencies.httpGet,
		)
	case activeDebugViewLB4Ifaces:
		return dispatchDebugLoadBalanceIfaces(
			ctx,
			output,
			cfg,
			view,
			args,
			debugFamilyV4,
			dependencies.httpGet,
		)
	case activeDebugViewLB6Ifaces:
		return dispatchDebugLoadBalanceIfaces(
			ctx,
			output,
			cfg,
			view,
			args,
			debugFamilyV6,
			dependencies.httpGet,
		)
	default:
		return fmt.Errorf("unknown active debug probe view %q", view)
	}
}

func showDebugConnectivity(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	dependencies debugProbeDependencies,
) error {
	renderedNPT, err := dependencies.renderNPT(ctx, logger)
	if err != nil {
		logger.WarnContext(
			ctx,
			"debug: inspect NPT table failed",
			"err",
			err,
		)
		return fmt.Errorf("connectivity: inspect NPT table: %w", err)
	}

	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "WAN\tIFACE\tIPv4\tIPv6\tP4\tP6\tNPT\tPD")
	for _, wan := range debugActiveProbeWANs(cfg) {
		row, err := inspectDebugConnectivityWAN(
			ctx,
			logger,
			wan,
			renderedNPT,
			dependencies,
		)
		if err != nil {
			return err
		}
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.wan,
			row.iface,
			row.ipv4,
			row.ipv6,
			row.ping4,
			row.ping6,
			row.npt,
			row.prefix,
		)
	}
	if err := writer.Flush(); err != nil {
		logger.WarnContext(
			ctx,
			"debug: render connectivity table failed",
			"err",
			err,
		)
		return fmt.Errorf("connectivity: render table: %w", err)
	}
	return nil
}

func inspectDebugConnectivityWAN(
	ctx context.Context,
	logger *slog.Logger,
	wan debugWAN,
	renderedNPT npt.RenderedTable,
	dependencies debugProbeDependencies,
) (debugConnectivityRow, error) {
	addresses, err := dependencies.listAddrs(ctx, logger, wan.Iface)
	if err != nil {
		logger.WarnContext(
			ctx,
			"debug: inspect WAN addresses failed",
			"iface",
			wan.Iface,
			"err",
			err,
		)
		return debugConnectivityRow{}, fmt.Errorf(
			"connectivity: %s addresses: %w",
			wan.Iface,
			err,
		)
	}
	ipv4, ipv6 := debugProbeGlobalAddresses(addresses)

	prefixValue := debugNoValue
	prefix, ok, err := dependencies.prefixSource.Prefix(ctx, wan.Iface)
	if err != nil {
		logger.WarnContext(
			ctx,
			"debug: inspect delegated prefix failed",
			"iface",
			wan.Iface,
			"err",
			err,
		)
		return debugConnectivityRow{}, fmt.Errorf(
			"connectivity: %s delegated prefix: %w",
			wan.Iface,
			err,
		)
	}
	if ok {
		prefixValue = prefix.String()
	}

	ping4Status := debugConnectivityProbeStatus(
		ctx,
		wan.Iface,
		ipv4,
		netip.MustParseAddr(debugTargetV4),
		dependencies.ping4,
	)
	ping6Status := debugConnectivityProbeStatus(
		ctx,
		wan.Iface,
		ipv6,
		netip.MustParseAddr(debugTargetV6),
		dependencies.ping6,
	)
	nptStatus := "FAIL"
	if renderedNPT.HasInterface(wan.Iface) {
		nptStatus = "OK"
	}
	return debugConnectivityRow{
		wan:    wan.Name,
		iface:  wan.Iface,
		ipv4:   ipv4,
		ipv6:   ipv6,
		ping4:  ping4Status,
		ping6:  ping6Status,
		npt:    nptStatus,
		prefix: prefixValue,
	}, nil
}

func debugProbeGlobalAddresses(addresses []netif.CurrentAddr) (string, string) {
	ipv4 := debugNoValue
	ipv6 := debugNoValue
	for _, current := range addresses {
		prefix, err := netip.ParsePrefix(current.CIDR)
		if err != nil || !prefix.Addr().IsGlobalUnicast() {
			continue
		}
		if prefix.Addr().Is4() && ipv4 == debugNoValue {
			ipv4 = current.CIDR
		}
		if prefix.Addr().Is6() && ipv6 == debugNoValue {
			ipv6 = current.CIDR
		}
	}
	return ipv4, ipv6
}

func debugConnectivityProbeStatus(
	ctx context.Context,
	iface string,
	address string,
	target netip.Addr,
	probe debugPingFunc,
) string {
	if address == debugNoValue {
		return "SKIP"
	}
	succeeded := false
	for range debugConnectivityAttempts {
		if _, err := probe(
			ctx,
			iface,
			target,
			debugConnectivityTimeout,
		); err == nil {
			succeeded = true
		}
	}
	if succeeded {
		return "OK"
	}
	return "FAIL"
}

func dispatchDebugPing(
	ctx context.Context,
	output io.Writer,
	cfg *config.Config,
	view string,
	args []string,
	family string,
	probe debugPingFunc,
) error {
	iface, err := debugProbeIface(cfg, view, args)
	if err != nil {
		return err
	}
	showDebugPing(ctx, output, view, iface, family, probe)
	return nil
}

func showDebugPing(
	ctx context.Context,
	output io.Writer,
	view string,
	iface string,
	family string,
	probe debugPingFunc,
) {
	for _, target := range debugPingTargets(family) {
		fmt.Fprintf(output, "%s %s via %s\n", debugPingHeading(view), target, iface)
		receivedCount := 0
		for attemptIndex := range debugPingAttempts {
			roundTripTime, err := probe(
				ctx,
				iface,
				target,
				debugActiveProbeTimeout,
			)
			if err != nil {
				fmt.Fprintf(
					output,
					"attempt %d: FAIL: %v\n",
					attemptIndex+1,
					err,
				)
				continue
			}
			receivedCount++
			fmt.Fprintf(
				output,
				"attempt %d: %s\n",
				attemptIndex+1,
				roundTripTime,
			)
		}
		fmt.Fprintf(
			output,
			"%d transmitted, %d received\n\n",
			debugPingAttempts,
			receivedCount,
		)
	}
}

func dispatchDebugCurl(
	ctx context.Context,
	output io.Writer,
	cfg *config.Config,
	view string,
	args []string,
	family string,
	httpGet debugHTTPGetFunc,
) error {
	iface, err := debugProbeIface(cfg, view, args)
	if err != nil {
		return err
	}
	for _, target := range debugHTTPTargets() {
		fmt.Fprintf(output, "%s %s via %s\n", view, target, iface)
		result, err := httpGet(
			ctx,
			iface,
			family,
			target,
			debugActiveProbeTimeout,
		)
		if err != nil {
			fmt.Fprintf(output, "FAIL: %v\n\n", err)
			continue
		}
		fmt.Fprintf(output, "%s\n\n", result.Body)
	}
	return nil
}

func dispatchDebugLoadBalance(
	ctx context.Context,
	output io.Writer,
	view string,
	args []string,
	family string,
	httpGet debugHTTPGetFunc,
) error {
	if err := debugProbeNoArgs(view, args); err != nil {
		return err
	}
	targets := debugHTTPTargets()
	for iterationIndex := range debugLoadBalanceCount {
		target := targets[iterationIndex%len(targets)]
		fmt.Fprintf(
			output,
			"%s iter %d -> %s: ",
			view,
			iterationIndex+1,
			target,
		)
		debugPrintHTTPResult(
			ctx,
			output,
			httpGet,
			"",
			family,
			target,
		)
	}
	return nil
}

func dispatchDebugLoadBalanceIfaces(
	ctx context.Context,
	output io.Writer,
	cfg *config.Config,
	view string,
	args []string,
	family string,
	httpGet debugHTTPGetFunc,
) error {
	if err := debugProbeNoArgs(view, args); err != nil {
		return err
	}
	wans := debugActiveProbeWANs(cfg)
	if len(wans) == 0 {
		return fmt.Errorf("%s: no usable WAN interfaces are configured", view)
	}
	targets := debugHTTPTargets()
	for iterationIndex := range debugLoadBalanceCount {
		wan := wans[iterationIndex%len(wans)]
		target := targets[iterationIndex%len(targets)]
		fmt.Fprintf(
			output,
			"%s iter %d via %s -> %s: ",
			view,
			iterationIndex+1,
			wan.Iface,
			target,
		)
		debugPrintHTTPResult(
			ctx,
			output,
			httpGet,
			wan.Iface,
			family,
			target,
		)
	}
	return nil
}

func debugPrintHTTPResult(
	ctx context.Context,
	output io.Writer,
	httpGet debugHTTPGetFunc,
	iface string,
	family string,
	target string,
) {
	result, err := httpGet(
		ctx,
		iface,
		family,
		target,
		debugActiveProbeTimeout,
	)
	if err != nil {
		fmt.Fprintf(output, "FAIL: %v\n", err)
		return
	}
	fmt.Fprintln(output, result.Body)
}

func debugProbeIface(
	cfg *config.Config,
	view string,
	args []string,
) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s accepts at most one positional interface", view)
	}
	if len(args) == 1 {
		if strings.TrimSpace(args[0]) == "" {
			return "", fmt.Errorf("%s positional interface is empty", view)
		}
		return args[0], nil
	}
	wans := debugActiveProbeWANs(cfg)
	if len(wans) == 0 {
		return "", fmt.Errorf("%s: no configured WAN has a usable interface", view)
	}
	// The default is the provider with the lowest firewall mark. Nothing in the
	// configuration says which provider a bare probe should use, and the mark
	// order is the one an operator already reads as the provider order.
	lowest := wans[0]
	for _, wan := range wans[1:] {
		if wan.FwMark < lowest.FwMark {
			lowest = wan
		}
	}
	return lowest.Iface, nil
}

func debugProbeNoArgs(view string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%s accepts no positional arguments", view)
	}
	return nil
}

// debugActiveProbeWANs returns every configured provider that has a usable
// interface, ordered by name. The set comes from the loaded configuration
// rather than a list of names in code, so a provider added to inventory appears
// in every active probe view with the binary unchanged.
func debugActiveProbeWANs(cfg *config.Config) []debugWAN {
	wans := make([]debugWAN, 0, len(cfg.IfMgr.WAN))
	for _, wan := range debugWANs(cfg) {
		if strings.TrimSpace(wan.Iface) == "" {
			continue
		}
		wans = append(wans, wan)
	}
	return wans
}

func debugPingTargets(family string) []netip.Addr {
	if family == debugFamilyV6 {
		return []netip.Addr{
			netip.MustParseAddr("2606:4700:4700::1111"),
			netip.MustParseAddr("2620:fe::9"),
		}
	}
	return []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("8.8.8.8"),
	}
}

func debugHTTPTargets() []string {
	return []string{
		"https://ifconfig.co",
		"https://api.ipify.org",
	}
}

func debugPingHeading(view string) string {
	if view == "ping6" {
		return "Ping6"
	}
	return "Ping4"
}
