package commandline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/pd"
)

var (
	errDebugViewNotActiveProbe = errors.New("does not run an active probe")
	errUnknownActiveProbeView  = errors.New("unknown active debug probe view")
	errDebugViewNotHTTPProbe   = errors.New("does not render HTTP probes")
	errNoUsableWANInterfaces   = errors.New("no usable WAN interfaces are configured")
	errTooManyDebugInterfaces  = errors.New("accepts at most one positional interface")
	errEmptyDebugInterface     = errors.New("positional interface is empty")
	errNoDebugWANInterface     = errors.New("no configured WAN has a usable interface")
	errUnexpectedDebugArgs     = errors.New("accepts no positional arguments")
)

const (
	debugConnectivityAttempts = 2
	debugPingAttempts         = 3
	debugLoadBalanceCount     = 6
	debugConnectivityTimeout  = 2 * time.Second
	debugActiveProbeTimeout   = 5 * time.Second
	debugNoValue              = "<none>"
)

type (
	debugPingFunc func(context.Context, string, netip.Addr, time.Duration) (
		time.Duration,
		error,
	)
	debugHTTPGetFunc func(context.Context, string, string, string, time.Duration) (
		netif.HTTPResult,
		error,
	)
	debugListAddrsFunc func(context.Context, *slog.Logger, string) ([]netif.CurrentAddr, error)
	debugRenderNPTFunc func(context.Context, *slog.Logger) (npt.RenderedTable, error)
)

type debugProbeDependencies struct {
	ping4        debugPingFunc
	ping6        debugPingFunc
	httpGet      debugHTTPGetFunc
	listAddrs    debugListAddrsFunc
	renderNPT    debugRenderNPTFunc
	prefixSource pd.Source
}

type debugConnectivityWANResult struct {
	WAN           debugWAN            `json:"wan"`
	Addresses     []netif.CurrentAddr `json:"addresses"`
	IPv4          string              `json:"ipv4"`
	IPv4Present   bool                `json:"ipv4_present"`
	IPv6          string              `json:"ipv6"`
	IPv6Present   bool                `json:"ipv6_present"`
	Prefix        netip.Prefix        `json:"prefix"`
	PrefixPresent bool                `json:"prefix_present"`
	NPTPresent    bool                `json:"npt_present"`
	Ping4Status   string              `json:"ping4_status"`
	Ping6Status   string              `json:"ping6_status"`
	NPTStatus     string              `json:"npt_status"`
	Ping4         []netif.PingAttempt `json:"ping4"`
	Ping6         []netif.PingAttempt `json:"ping6"`
}

type debugHTTPObservation struct {
	netif.HTTPAttempt

	WAN *debugWAN `json:"wan,omitempty"`
}

func newDebugProbeDependencies(logger *slog.Logger) debugProbeDependencies {
	return debugProbeDependencies{
		ping4: func(
			ctx context.Context,
			iface string,
			target netip.Addr,
			timeout time.Duration,
		) (
			time.Duration,
			error,
		) {
			return netif.Ping4WithLogger(ctx, logger, iface, target, timeout)
		},
		ping6: func(
			ctx context.Context,
			iface string,
			target netip.Addr,
			timeout time.Duration,
		) (
			time.Duration,
			error,
		) {
			return netif.Ping6WithLogger(ctx, logger, iface, target, timeout)
		},
		httpGet: func(ctx context.Context, iface, family, target string, timeout time.Duration) (
			netif.HTTPResult,
			error,
		) {
			return netif.HTTPGet(ctx, logger, iface, family, target, timeout)
		},
		listAddrs:    netif.ListAddrs,
		renderNPT:    npt.RenderTable,
		prefixSource: pd.New(logger),
	}
}

func runDebugProbeView(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	view commandopts.DebugView,
	args []string,
	format string,
) error {
	return runDebugProbeViewWithDependencies(
		ctx,
		output,
		logger,
		cfg,
		view,
		args,
		newDebugProbeDependencies(logger),
		format,
	)
}

func runDebugProbeViewWithDependencies(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
	cfg *config.Config,
	view commandopts.DebugView,
	args []string,
	dependencies debugProbeDependencies,
	format string,
) error {
	switch view {
	case commandopts.DebugViewConnectivity:
		if err := debugProbeNoArgs(logger, view, args); err != nil {
			return err
		}

		result, err := collectDebugConnectivity(ctx, logger, cfg, dependencies)
		if err != nil {
			return err
		}

		if format == debugJSONFormat {
			return WriteJSON(logger, output, result)
		}

		return writeDebugConnectivityText(output, logger, result)
	case commandopts.DebugViewPing4, commandopts.DebugViewPing6:
		iface, err := debugProbeIface(logger, cfg, view, args)
		if err != nil {
			return err
		}

		family, probe := debugFamilyV4, dependencies.ping4
		if view == commandopts.DebugViewPing6 {
			family, probe = debugFamilyV6, dependencies.ping6
		}

		attempts := collectDebugPing(
			ctx,
			iface,
			family,
			debugPingTargets(family),
			debugPingAttempts,
			debugActiveProbeTimeout,
			probe,
		)
		if format == debugJSONFormat {
			return WriteJSON(logger, output, attempts)
		}

		return writeDebugPingText(output, logger, view, attempts)
	case commandopts.DebugViewCurl4,
		commandopts.DebugViewCurl6,
		commandopts.DebugViewLB4,
		commandopts.DebugViewLB6,
		commandopts.DebugViewLB4Ifaces,
		commandopts.DebugViewLB6Ifaces:
		requests, err := debugHTTPRequests(logger, cfg, view, args)
		if err != nil {
			return err
		}

		observations := collectDebugHTTP(ctx, requests, dependencies.httpGet)
		if format == debugJSONFormat {
			return WriteJSON(logger, output, observations)
		}

		return writeDebugHTTPText(output, logger, view, observations)
	case commandopts.DebugViewNPT,
		commandopts.DebugViewPolicy,
		commandopts.DebugViewPrefixes,
		commandopts.DebugViewRoutes,
		commandopts.DebugViewSim4,
		commandopts.DebugViewSim6,
		commandopts.DebugViewStats,
		commandopts.DebugViewStatus,
		commandopts.DebugViewSystemd,
		commandopts.DebugViewTraceStart,
		commandopts.DebugViewTraceStop,
		commandopts.DebugViewTraceTail:
		logger.Warn("debug view does not support active probes", "view", view)

		return fmt.Errorf("debug view %q %w", view, errDebugViewNotActiveProbe)
	default:
		logger.Warn("unknown active probe view", "view", view)

		return fmt.Errorf("%w %q", errUnknownActiveProbeView, view)
	}
}

func collectDebugConnectivity(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	dependencies debugProbeDependencies,
) ([]debugConnectivityWANResult, error) {
	rendered, err := dependencies.renderNPT(ctx, logger)
	if err != nil {
		return nil, debugWrappedError(logger, "connectivity: inspect NPT table", err)
	}

	results := make([]debugConnectivityWANResult, 0, len(cfg.IfMgr.WAN))
	for _, wan := range debugActiveProbeWANs(cfg) {
		result, err := collectDebugConnectivityWAN(ctx, logger, wan, rendered, dependencies)
		if err != nil {
			return nil, err
		}

		results = append(results, result)
	}

	return results, nil
}

func collectDebugConnectivityWAN(
	ctx context.Context,
	logger *slog.Logger,
	wan debugWAN,
	rendered npt.RenderedTable,
	dependencies debugProbeDependencies,
) (debugConnectivityWANResult, error) {
	addresses, err := dependencies.listAddrs(ctx, logger, wan.Iface)
	if err != nil {
		return debugConnectivityWANResult{}, debugWrappedError(
			logger,
			fmt.Sprintf("connectivity: %s addresses", wan.Iface),
			err,
		)
	}

	prefix, present, err := dependencies.prefixSource.Prefix(ctx, wan.Iface)
	if err != nil {
		return debugConnectivityWANResult{}, debugWrappedError(
			logger,
			fmt.Sprintf("connectivity: %s delegated prefix", wan.Iface),
			err,
		)
	}

	ipv4, ipv6 := debugProbeGlobalAddresses(addresses)
	result := debugConnectivityWANResult{
		WAN:           wan,
		Addresses:     addresses,
		IPv4:          ipv4,
		IPv4Present:   ipv4 != "",
		IPv6:          ipv6,
		IPv6Present:   ipv6 != "",
		Prefix:        prefix,
		PrefixPresent: present,
		NPTPresent:    rendered.HasInterface(wan.Iface),
		NPTStatus:     "FAIL",
		Ping4:         make([]netif.PingAttempt, 0),
		Ping6:         make([]netif.PingAttempt, 0),
		Ping4Status:   "",
		Ping6Status:   "",
	}
	if result.IPv4Present {
		result.Ping4 = collectDebugPing(
			ctx,
			wan.Iface,
			debugFamilyV4,
			[]netip.Addr{netip.MustParseAddr(debugTargetV4)},
			debugConnectivityAttempts,
			debugConnectivityTimeout,
			dependencies.ping4,
		)
	}

	if result.IPv6Present {
		result.Ping6 = collectDebugPing(
			ctx,
			wan.Iface,
			debugFamilyV6,
			[]netip.Addr{netip.MustParseAddr(debugTargetV6)},
			debugConnectivityAttempts,
			debugConnectivityTimeout,
			dependencies.ping6,
		)
	}

	result.Ping4Status = debugConnectivityProbeStatus(result.Ping4)
	result.Ping6Status = debugConnectivityProbeStatus(result.Ping6)
	if result.NPTPresent {
		result.NPTStatus = "OK"
	}

	return result, nil
}

func debugProbeGlobalAddresses(addresses []netif.CurrentAddr) (string, string) {
	var ipv4, ipv6 string

	for _, current := range addresses {
		prefix, err := netip.ParsePrefix(current.CIDR)
		if err != nil || !prefix.Addr().IsGlobalUnicast() {
			continue
		}

		if prefix.Addr().Is4() && ipv4 == "" {
			ipv4 = current.CIDR
		}

		if prefix.Addr().Is6() && ipv6 == "" {
			ipv6 = current.CIDR
		}
	}

	return ipv4, ipv6
}

func debugConnectivityProbeStatus(attempts []netif.PingAttempt) string {
	if len(attempts) == 0 {
		return "SKIP"
	}

	for _, attempt := range attempts {
		if attempt.Error == "" {
			return "OK"
		}
	}

	return "FAIL"
}

func collectDebugPing(
	ctx context.Context,
	iface string,
	family string,
	targets []netip.Addr,
	count int,
	timeout time.Duration,
	probe debugPingFunc,
) []netif.PingAttempt {
	attempts := make([]netif.PingAttempt, 0, len(targets)*count)
	for _, target := range targets {
		for index := range count {
			rtt, err := probe(ctx, iface, target, timeout)
			attempt := netif.PingAttempt{
				Interface: iface,
				Family:    family,
				Target:    target.String(),
				Attempt:   index + 1,
				Timeout:   timeout,
				RTT:       rtt,
				Error:     "",
			}
			if err != nil {
				attempt.Error = err.Error()
			}

			attempts = append(attempts, attempt)
		}
	}

	return attempts
}

func debugHTTPRequests(
	logger *slog.Logger,
	cfg *config.Config,
	view commandopts.DebugView,
	args []string,
) ([]debugHTTPObservation, error) {
	family := debugFamilyV4
	if view == commandopts.DebugViewCurl6 || view == commandopts.DebugViewLB6 ||
		view == commandopts.DebugViewLB6Ifaces {
		family = debugFamilyV6
	}

	var iface string
	var wans []debugWAN
	count := debugLoadBalanceCount
	switch view {
	case commandopts.DebugViewCurl4, commandopts.DebugViewCurl6:
		selected, err := debugProbeIface(logger, cfg, view, args)
		if err != nil {
			return nil, err
		}

		iface = selected
		count = len(debugHTTPTargets())
	case commandopts.DebugViewLB4,
		commandopts.DebugViewLB6,
		commandopts.DebugViewLB4Ifaces,
		commandopts.DebugViewLB6Ifaces:
		if err := debugProbeNoArgs(logger, view, args); err != nil {
			return nil, err
		}

		if view == commandopts.DebugViewLB4Ifaces || view == commandopts.DebugViewLB6Ifaces {
			wans = debugActiveProbeWANs(cfg)
			if len(wans) == 0 {
				logger.Warn("active probe has no usable WAN interfaces", "view", view)

				return nil, fmt.Errorf("%s: %w", view, errNoUsableWANInterfaces)
			}
		}
	case commandopts.DebugViewConnectivity,
		commandopts.DebugViewNPT,
		commandopts.DebugViewPing4,
		commandopts.DebugViewPing6,
		commandopts.DebugViewPolicy,
		commandopts.DebugViewPrefixes,
		commandopts.DebugViewRoutes,
		commandopts.DebugViewSim4,
		commandopts.DebugViewSim6,
		commandopts.DebugViewStats,
		commandopts.DebugViewStatus,
		commandopts.DebugViewSystemd,
		commandopts.DebugViewTraceStart,
		commandopts.DebugViewTraceStop,
		commandopts.DebugViewTraceTail:
		logger.Warn("debug view does not support HTTP probes", "view", view)

		return nil, fmt.Errorf("debug view %q %w", view, errDebugViewNotHTTPProbe)
	default:
		logger.Warn("unknown HTTP probe view", "view", view)

		return nil, fmt.Errorf("debug view %q %w", view, errDebugViewNotHTTPProbe)
	}

	targets := debugHTTPTargets()

	requests := make([]debugHTTPObservation, 0, count)
	for index := range count {
		result := netif.HTTPResult{StatusCode: 0, Body: ""}
		attempt := netif.HTTPAttempt{
			Interface:  iface,
			Family:     family,
			Target:     targets[index%len(targets)],
			Iteration:  index + 1,
			Timeout:    debugActiveProbeTimeout,
			HTTPResult: result,
			Error:      "",
		}
		request := debugHTTPObservation{HTTPAttempt: attempt, WAN: nil}
		if len(wans) > 0 {
			wan := wans[index%len(wans)]
			request.WAN = &wan
			request.Interface = wan.Iface
		}

		requests = append(requests, request)
	}

	return requests, nil
}

func collectDebugHTTP(
	ctx context.Context,
	requests []debugHTTPObservation,
	get debugHTTPGetFunc,
) []debugHTTPObservation {
	observations := make([]debugHTTPObservation, 0, len(requests))
	for _, request := range requests {
		result, err := get(ctx, request.Interface, request.Family, request.Target, request.Timeout)
		request.HTTPResult = result
		if err != nil {
			request.Error = err.Error()
		}

		observations = append(observations, request)
	}

	return observations
}

func debugProbeIface(
	logger *slog.Logger,
	cfg *config.Config,
	view commandopts.DebugView,
	args []string,
) (string, error) {
	if len(args) > 1 {
		logger.Warn(
			"probe accepts at most one interface",
			"view",
			view,
			"argument_count",
			len(args),
		)

		return "", fmt.Errorf("%s %w", view, errTooManyDebugInterfaces)
	}

	if len(args) == 1 {
		if strings.TrimSpace(args[0]) == "" {
			logger.Warn("probe interface argument is empty", "view", view)

			return "", fmt.Errorf("%s %w", view, errEmptyDebugInterface)
		}

		return args[0], nil
	}

	wans := debugActiveProbeWANs(cfg)
	if len(wans) == 0 {
		logger.Warn("probe has no usable WAN interface", "view", view)

		return "", fmt.Errorf("%s: %w", view, errNoDebugWANInterface)
	}

	lowest := wans[0]
	for _, wan := range wans[1:] {
		if wan.FwMark < lowest.FwMark {
			lowest = wan
		}
	}

	return lowest.Iface, nil
}

func debugProbeNoArgs(
	logger *slog.Logger,
	view commandopts.DebugView,
	args []string,
) error {
	if len(args) != 0 {
		logger.Warn("probe does not accept arguments", "view", view, "argument_count", len(args))

		return fmt.Errorf("%s %w", view, errUnexpectedDebugArgs)
	}

	return nil
}

func debugActiveProbeWANs(cfg *config.Config) []debugWAN {
	wans := make([]debugWAN, 0, len(cfg.IfMgr.WAN))
	for _, wan := range debugWANs(cfg) {
		if strings.TrimSpace(wan.Iface) != "" {
			wans = append(wans, wan)
		}
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

	return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}
}

func debugHTTPTargets() []string {
	return []string{"https://ifconfig.co", "https://api.ipify.org"}
}

func debugPingHeading(view commandopts.DebugView) string {
	if view == commandopts.DebugViewPing6 {
		return "Ping6"
	}

	return "Ping4"
}
