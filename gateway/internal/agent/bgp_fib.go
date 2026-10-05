package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
)

const staleSweepReconcileInterval = time.Minute

type staleSweeper interface {
	SweepStale(context.Context) error
}

type staleSweepTicker interface {
	Channel() <-chan time.Time
	Stop()
}

type staleSweepClock interface {
	NewTicker(time.Duration) staleSweepTicker
}

type realStaleSweepTicker struct {
	ticker *time.Ticker
}

func (t realStaleSweepTicker) Channel() <-chan time.Time {
	return t.ticker.C
}

func (t realStaleSweepTicker) Stop() {
	t.ticker.Stop()
}

type realStaleSweepClock struct{}

func (realStaleSweepClock) NewTicker(interval time.Duration) staleSweepTicker {
	return realStaleSweepTicker{ticker: time.NewTicker(interval)}
}

type staleSweepReconciler struct {
	sweeper staleSweeper
	clock   staleSweepClock
	log     *slog.Logger
}

func configurePlatformBGP(
	ctx context.Context,
	cfg *config.Config,
	speaker *bgp.Speaker,
	log *slog.Logger,
) error {
	return configureBGPFIB(
		ctx,
		cfg,
		speaker,
		log,
		networkjson.DefaultPath,
		networkjson.DefaultSchemaDir,
	)
}

// configureBGPFIB is configurePlatformBGP with the network configuration's
// location named, so a test drives the same load against a fixture rather than
// the paths the deploy installs.
func configureBGPFIB(
	ctx context.Context,
	cfg *config.Config,
	speaker *bgp.Speaker,
	log *slog.Logger,
	networkPath string,
	schemaDir string,
) error {
	if cfg.BGP.UseWanconfig {
		// The per-provider tables come from the network configuration, which
		// config.toml no longer carries. Without it the installer would own the
		// main table alone: every route learned for a provider would go
		// uninstalled while the speaker still looked healthy, and traffic
		// policy-routed into those tables would leave over the WAN instead of
		// returning to the router. A host that declares it uses the wanconfig
		// network configuration and cannot read it does not start.
		if err := networkjson.ApplyFrom(cfg, networkPath, schemaDir); err != nil {
			log.ErrorContext(
				ctx,
				"network configuration unusable, BGP route installer cannot own the per-provider tables",
				"path", networkPath,
				"schema_dir", schemaDir,
				"error", err,
			)
			return fmt.Errorf("load network configuration: %w", err)
		}
	}
	if cfg.BGP.LearnedRouteIface == "" {
		return nil
	}
	speaker.SetFIB(bgp.NewFIB(bgp.FIBConfig{
		Tables:        tablesFromConfig(cfg),
		InternalIface: cfg.BGP.LearnedRouteIface,
	}, log))
	startStaleSweepReconciler(ctx, speaker, log, realStaleSweepClock{})
	return nil
}

func startStaleSweepReconciler(
	ctx context.Context,
	sweeper staleSweeper,
	log *slog.Logger,
	clock staleSweepClock,
) {
	reconciler := staleSweepReconciler{sweeper: sweeper, clock: clock, log: log}
	go func() {
		// Last resort only: each tick recovers its own sweep panic in
		// sweep(), so this catches nothing short of a loop-level failure.
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "BGP stale sweep reconciler crashed", "error", fmt.Errorf("panic: %v", recovered))
			}
		}()
		reconciler.run(ctx)
	}()
}

func (r staleSweepReconciler) run(ctx context.Context) {
	ticker := r.clock.NewTicker(staleSweepReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Channel():
			r.sweep(ctx)
		}
	}
}

func (r staleSweepReconciler) sweep(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.log.ErrorContext(ctx, "BGP stale sweep reconcile panic", "error", fmt.Errorf("panic: %v", recovered))
		}
	}()
	if err := r.sweeper.SweepStale(ctx); err != nil {
		r.log.ErrorContext(ctx, "reconcile BGP stale routes", "error", err)
	}
}

func tablesFromConfig(cfg *config.Config) []int {
	tables := []int{unix.RT_TABLE_MAIN}
	wanNames := make([]string, 0, len(cfg.IfMgr.WAN))
	for wanName := range cfg.IfMgr.WAN {
		wanNames = append(wanNames, wanName)
	}
	sort.Strings(wanNames)
	for _, wanName := range wanNames {
		tables = append(tables, cfg.IfMgr.WAN[wanName].TableID)
	}
	return tables
}
