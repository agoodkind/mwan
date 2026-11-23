package commandline

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/mwan/internal/commandline/commandopts"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/version"
)

const (
	debugFamilyV4   = "inet"
	debugFamilyV6   = "inet6"
	debugJSONFormat = "json"
	debugTargetV4   = "1.1.1.1"
	debugTargetV6   = "2606:4700:4700::1111"
)

func runDebugOptions(
	options commandopts.Debug,
	cfg *config.Config,
	output io.Writer,
	diagnostics io.Writer,
) int {
	var logger *slog.Logger
	var closer io.Closer
	if options.View == commandopts.DebugViewTraceStart ||
		options.View == commandopts.DebugViewTraceStop ||
		options.View == commandopts.DebugViewTraceTail {
		logger, closer = logging.New(logging.Config{
			BuildVersion: version.BuildVersionString(),
			Handlers: []slog.Handler{
				slog.NewTextHandler(diagnostics, &slog.HandlerOptions{Level: slog.LevelWarn}),
			},
		})
	} else {
		logger, closer = operatorLogger(diagnostics, options.Verbose)
	}

	defer closer.Close()
	loadDebugNetworkConfig(diagnostics, cfg)
	ctx := context.Background()
	view, args := options.View, options.Arguments
	var err error
	switch view {
	case commandopts.DebugViewNPT,
		commandopts.DebugViewPrefixes,
		commandopts.DebugViewRoutes,
		commandopts.DebugViewPolicy,
		commandopts.DebugViewStatus,
		commandopts.DebugViewStats,
		commandopts.DebugViewSim4,
		commandopts.DebugViewSim6,
		commandopts.DebugViewSystemd:
		err = runDebugInspection(ctx, output, diagnostics, logger, cfg, view, options.OutputFormat)
	case commandopts.DebugViewTraceStart:
		err = showDebugTraceStart(ctx, output, logger, cfg, args)
	case commandopts.DebugViewTraceTail:
		err = showDebugTraceTail(ctx, output, logger, cfg, args)
	case commandopts.DebugViewTraceStop:
		err = showDebugTraceStop(output)
	case commandopts.DebugViewConnectivity,
		commandopts.DebugViewPing4,
		commandopts.DebugViewPing6,
		commandopts.DebugViewCurl4,
		commandopts.DebugViewCurl6,
		commandopts.DebugViewLB4,
		commandopts.DebugViewLB6,
		commandopts.DebugViewLB4Ifaces,
		commandopts.DebugViewLB6Ifaces:
		err = runDebugProbeView(ctx, output, logger, cfg, view, args, options.OutputFormat)
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

// Network-independent inspections render when WAN configuration cannot load.
func loadDebugNetworkConfig(diagnostics io.Writer, cfg *config.Config) {
	if err := networkload.ApplyDefault(cfg); err != nil {
		fmt.Fprintf(
			diagnostics,
			"mwan debug: network configuration unreadable, WAN lists and route simulations will be empty: %v\n",
			err,
		)
	}
}
