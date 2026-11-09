// Package watchdog implements the mwan watchdog monitoring and auto-rollback subsystem.
package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"goodkind.io/mwan/internal/alert"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/redteam"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/version"
	"goodkind.io/mwan/internal/watchdogconfig"
)

// Options separates argument parsing from watchdog startup.
type Options struct {
	DryRun            bool
	RedTeam           string
	RedTeamLive       bool
	RedTeamIterations int
	ListScenarios     bool
}

func printScenarios() {
	// Explicit os.Stdout writes for human-facing CLI usage output;
	// not production diagnostics (which go through slog).
	fmt.Fprintln(os.Stdout, "Available red-team scenarios:")
	fmt.Fprintln(os.Stdout)

	for name, preset := range redteam.Presets {
		fmt.Fprintf(os.Stdout, "  %-22s %s\n", name, preset.Description)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(
		os.Stdout,
		"Usage: mwan watchdog --red-team <scenario> "+
			"[--red-team-live] [--red-team-iterations N]",
	)
}

// buildOpsLayer creates the logger, notifier, and operations layer,
// applying dry-run and red-team wrappers as needed. The slog email
// handler is intentionally not attached: notify owns email delivery
// for the watchdog now, so attaching the handler would produce a
// duplicate email per failover or recovery event.
func buildOpsLayer(
	cfg *watchdogconfig.Config,
	f Options,
) (*slog.Logger, ops.SysOps, notify.Notifier, error) {
	handlers := []slog.Handler{logging.StdoutJSON()}
	if p := cfg.Watchdog.LogFile; p != "" {
		handlers = append(handlers, logging.FileText(p, "[watchdog]"))
	}

	if p := cfg.Watchdog.JSONLogFile; p != "" {
		handlers = append(handlers, logging.FileJSON(p))
	}

	logger, _ := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers:     handlers,
	})
	logger.Info("mwan watchdog starting", "version", version.BuildVersionString())

	notifier := notify.FromConfig(cfg.Email, cfg.NotifyConfig(), logger, "mwan-watchdog")

	var baseOps ops.SysOps = ops.NewRealOps(cfg, logger)

	if f.DryRun {
		logger.Info("[MODE] Dry-run enabled; destructive ops will be logged only")
		baseOps = &dryRunOps{inner: baseOps, log: logger}
	}

	if f.RedTeam != "" {
		preset, ok := redteam.Presets[f.RedTeam]
		if !ok {
			return nil, nil, nil, fmt.Errorf(
				"unknown scenario %q (use --list-scenarios)",
				f.RedTeam,
			)
		}

		logger.Info(
			"[MODE] Red-team scenario",
			"scenario",
			f.RedTeam,
			"description",
			preset.Description,
			"live",
			f.RedTeamLive,
		)
		baseOps = redteam.NewOps(baseOps, preset, logger)
	}

	return logger, baseOps, notifier, nil
}

// Run is the entry point for the watchdog subcommand.
// It starts the watchdog with explicit options.
func Run(cfg *watchdogconfig.Config, options Options) error {
	f := options
	if f.RedTeam != "" && !f.RedTeamLive {
		f.DryRun = true
	}

	if os.Getenv("DRY_RUN") == "1" {
		f.DryRun = true
	}

	if f.ListScenarios {
		printScenarios()
		return nil
	}

	logger, baseOps, notifier, err := buildOpsLayer(cfg, f)
	if err != nil {
		return err
	}

	runID := tracing.NewID()
	logger = logger.With(
		slog.String(tracing.RunIDKey, runID),
		slog.String(tracing.ComponentKey, "watchdog"),
	)

	// Override config for red-team mode
	if f.RedTeam != "" {
		cfg.Watchdog.MaxIterations = f.RedTeamIterations
		cfg.Watchdog.CheckIntervalHealthy = 1
		cfg.Watchdog.CheckIntervalDegraded = 1
		cfg.Watchdog.ConnectivityTimeoutSeconds = 3
		cfg.Watchdog.PostRollbackGraceSeconds = 2
		cfg.Watchdog.AlertCooldownSeconds = 0
	}

	// Set up signal handling
	coord := &alert.Coord{}
	mainCtx, cancelMain := context.WithCancel(context.Background())

	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(
					mainCtx,
					"signal handler panic",
					"err",
					fmt.Errorf("panic: %v", recovered),
				)
			}
		}()
		<-sigCtx.Done()
		if coord.IsRollingBack() {
			logger.InfoContext(
				mainCtx,
				"Signal received during rollback; deferring until rollback completes",
			)
			coord.OnSignalDuringRollback()
			stopSig()

			return
		}

		logger.InfoContext(mainCtx, "Signal received; shutting down")
		stopSig()
		cancelMain()
	}()

	// Create watchdog instance and run
	w := newWatchdog(cfg, baseOps, notifier, coord, logger, runID)

	// Try to extract channel tracker for diagnostics
	w.tracker = extractTracker(baseOps)

	w.run(mainCtx)

	return nil
}

func extractTracker(baseOps ops.SysOps) *ops.ChannelTracker {
	switch v := baseOps.(type) {
	case *ops.RealOps:
		return v.ExtractTracker()
	case *dryRunOps:
		return extractTracker(v.inner)
	case *redteam.Ops:
		// Red team wraps another ops, try to extract from that
		// Since redteam.Ops.inner is private, we can't access it directly.
		// Return nil in this case.
		return nil
	default:
		return nil
	}
}
