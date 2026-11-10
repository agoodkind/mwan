package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkload"
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

type debugOptions struct {
	view         debugView
	arguments    []string
	outputFormat string
	verbose      bool
}

func runDebugOptions(options debugOptions, cfg *config.Config, output io.Writer, diagnostics io.Writer) int {
	var logger *slog.Logger
	var closer io.Closer
	if options.view == debugViewTraceStart || options.view == debugViewTraceStop || options.view == debugViewTraceTail {
		logger, closer = logging.New(logging.Config{Handlers: []slog.Handler{slog.NewTextHandler(diagnostics, &slog.HandlerOptions{Level: slog.LevelWarn})}})
	} else {
		logger, closer = operatorLogger(diagnostics, options.verbose)
	}
	defer closer.Close()
	loadDebugNetworkConfig(diagnostics, cfg)
	ctx := context.Background()
	view, args := options.view, options.arguments
	var err error
	switch view {
	case debugViewNPT, debugViewPrefixes, debugViewRoutes, debugViewPolicy, debugViewStatus, debugViewStats, debugViewSim4, debugViewSim6, debugViewSystemd:
		err = runDebugInspection(ctx, output, diagnostics, logger, cfg, view, options.outputFormat)
	case debugViewTraceStart:
		err = showDebugTraceStart(ctx, output, logger, cfg, args)
	case debugViewTraceTail:
		err = showDebugTraceTail(ctx, output, logger, cfg, args)
	case debugViewTraceStop:
		err = showDebugTraceStop(output)
	case debugViewConnectivity, debugViewPing4, debugViewPing6, debugViewCurl4, debugViewCurl6, debugViewLB4, debugViewLB6, debugViewLB4Ifaces, debugViewLB6Ifaces:
		err = runDebugProbeView(ctx, output, logger, cfg, view, args, options.outputFormat)
	default:
		fmt.Fprintf(diagnostics, "unknown debug view %q\n", view)
		return 1
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan debug %s: %v\n", view, err)
		return 1
	}
	return 0
}

// Independent inspections remain available when the network configuration fails.
func loadDebugNetworkConfig(diagnostics io.Writer, cfg *config.Config) {
	if err := networkload.ApplyDefault(cfg); err != nil {
		fmt.Fprintf(diagnostics, "mwan debug: network configuration unreadable, WAN lists and route simulations will be empty: %v\n", err)
	}
}
