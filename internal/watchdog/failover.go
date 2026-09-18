package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/alert"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/version"
)

// triggerFailover dispatches to the BGP route-control failover path.
func (w *watchdog) triggerFailover(ctx context.Context, cfg *config.Config, reason string) error {
	if cfg.Failover.LXCID == "" {
		return fmt.Errorf("failover config has no lxc_id; cannot failover")
	}
	if !cfg.BGP.Enabled {
		return fmt.Errorf("failover requires cfg.BGP.Enabled=true")
	}
	w.log.InfoContext(ctx, "FAILOVER: dispatching to BGP route control path", "reason", reason)
	return w.triggerBGPFailover(ctx, cfg, reason)
}

// triggerBGPFailover performs failover using BGP route control:
//  1. Withdraw routes from the primary MWAN VM
//  2. Check BGP status on the failover LXC
//  3. If the LXC is not yet announcing, tell it to announce
func (w *watchdog) triggerBGPFailover(ctx context.Context, cfg *config.Config, reason string) error {
	start := w.now()

	// Notify about BGP failover initiation. The notify boundary owns
	// per-(kind, key) state-change suppression so a stuck watchdog cannot
	// flood the inbox; the key is the primary vmid so a different vmid
	// retains its own failover state.
	failoverMsg := fmt.Sprintf("BGP FAILOVER: withdrawing routes from VM %s, announcing on LXC %s",
		cfg.MwanVMID, cfg.Failover.LXCID)
	w.notify.Notify(ctx, notify.Event{
		Now:     time.Time{},
		Level:   slog.LevelError,
		Kind:    "bgp-failover",
		Key:     cfg.MwanVMID,
		Message: failoverMsg,
		Fields: []slog.Attr{
			slog.String("reason", reason),
			slog.String("vmid", cfg.MwanVMID),
			slog.String("lxc", cfg.Failover.LXCID),
		},
		IsRecovery: false,
	})

	// Step 1: Withdraw routes from primary VM.
	w.log.InfoContext(ctx, "BGP_FAILOVER: withdrawing routes from primary VM", "vmid", cfg.MwanVMID)
	if err := w.ops.WithdrawRoutes(ctx, cfg.MwanVMID); err != nil {
		w.log.ErrorContext(ctx, "BGP_FAILOVER: withdraw routes failed on primary", "vmid", cfg.MwanVMID, "err", err)
		// Continue because the primary may already be unreachable.
	} else {
		w.log.InfoContext(ctx, "BGP_FAILOVER: routes withdrawn from primary VM", "vmid", cfg.MwanVMID)
	}

	// Step 2: Check BGP status on the failover LXC.
	w.log.InfoContext(ctx, "BGP_FAILOVER: checking BGP status on failover LXC", "lxc", cfg.Failover.LXCID)
	status, err := w.ops.GetBGPStatus(ctx, cfg.Failover.LXCID)
	if err != nil {
		w.log.ErrorContext(ctx, "BGP_FAILOVER: failed to get BGP status on LXC", "lxc", cfg.Failover.LXCID, "err", err)
		// Announce anyway because the LXC agent may still accept the command.
	}

	// Step 3: If LXC is not established or not already announcing, tell it to announce.
	needsAnnounce := true
	if status != nil && status.GetAllEstablished() && status.GetAnnouncing() {
		w.log.InfoContext(ctx, "BGP_FAILOVER: LXC already established and announcing; no action needed",
			"lxc", cfg.Failover.LXCID)
		needsAnnounce = false
	}

	if needsAnnounce {
		w.log.InfoContext(ctx, "BGP_FAILOVER: announcing routes on failover LXC", "lxc", cfg.Failover.LXCID)
		if err := w.ops.AnnounceRoutes(ctx, cfg.Failover.LXCID); err != nil {
			w.log.ErrorContext(ctx, "BGP_FAILOVER: announce routes failed on LXC",
				"lxc", cfg.Failover.LXCID, "err", err, "elapsed", w.since(start))
			return fmt.Errorf("BGP failover AnnounceRoutes on LXC %s: %w", cfg.Failover.LXCID, err)
		}
		w.log.InfoContext(ctx, "BGP_FAILOVER: routes announced on failover LXC", "lxc", cfg.Failover.LXCID)
	}

	w.log.InfoContext(ctx, "BGP_FAILOVER: complete",
		"lxc", cfg.Failover.LXCID,
		"elapsed", w.since(start),
	)

	// Notify that the failover sequence completed. Kind "bgp-failover-complete"
	// is a separate state-change so it does not collide with the active
	// "bgp-failover" alert; the latter stays open until recovery resolves it.
	completeMsg := fmt.Sprintf("BGP FAILOVER COMPLETE: LXC %s announcing", cfg.Failover.LXCID)
	elapsed := w.since(start).Round(time.Second)
	w.notify.Notify(ctx, notify.Event{
		Now:     time.Time{},
		Level:   slog.LevelError,
		Kind:    "bgp-failover-complete",
		Key:     cfg.MwanVMID,
		Message: completeMsg,
		Fields: []slog.Attr{
			slog.Duration("elapsed", elapsed),
			slog.String("vmid", cfg.MwanVMID),
			slog.String("lxc", cfg.Failover.LXCID),
		},
		IsRecovery: false,
	})

	// Record failover state under the mutex so the run loop can later detect
	// recovery and emit the BGP RECOVERED email exactly once.
	w.failoverMu.Lock()
	w.failoverActive = true
	w.failoverStartedAt = w.now()
	w.failoverReason = reason
	w.failoverMu.Unlock()

	return nil
}

// triggerBGPRecovery moves routes back to the primary MWAN VM after it has
// returned to a healthy state. It mirrors triggerBGPFailover in shape:
// verify primary reachability, withdraw from the failover LXC, announce on
// the primary, send a recovery email, and clear failover state.
func (w *watchdog) triggerBGPRecovery(ctx context.Context, cfg *config.Config) error {
	start := w.now()

	w.failoverMu.Lock()
	startedAt := w.failoverStartedAt
	prevReason := w.failoverReason
	w.failoverMu.Unlock()

	// Step 1: confirm the primary VM is reachable. Reuse the existing host-
	// level Ping probes the watchdog already uses to gate failover decisions.
	w.log.InfoContext(ctx, "BGP_RECOVERY: probing primary VM reachability",
		"vmid", cfg.MwanVMID)
	v4ok := w.ops.Ping(ctx, "ping", cfg.Network.PingTargetIPv4)
	v6ok := w.ops.Ping(ctx, "ping6", cfg.Network.PingTargetIPv6)
	if !v4ok && !v6ok {
		return fmt.Errorf("BGP recovery: primary VM %s still unreachable", cfg.MwanVMID)
	}

	// Step 2: confirm primary's BGP session is established.
	w.log.InfoContext(ctx, "BGP_RECOVERY: checking BGP status on primary VM",
		"vmid", cfg.MwanVMID)
	status, err := w.ops.GetBGPStatus(ctx, cfg.MwanVMID)
	if err != nil {
		return fmt.Errorf("BGP recovery: GetBGPStatus on primary VM %s: %w", cfg.MwanVMID, err)
	}
	if status == nil || !status.GetAllEstablished() {
		return fmt.Errorf("BGP recovery: primary VM %s BGP not established", cfg.MwanVMID)
	}

	// Step 3: withdraw from the failover LXC.
	w.log.InfoContext(ctx, "BGP_RECOVERY: withdrawing routes from failover LXC",
		"lxc", cfg.Failover.LXCID)
	if err := w.ops.WithdrawRoutes(ctx, cfg.Failover.LXCID); err != nil {
		w.log.ErrorContext(ctx, "BGP_RECOVERY: withdraw routes failed on LXC",
			"lxc", cfg.Failover.LXCID, "err", err)
		return fmt.Errorf("BGP recovery WithdrawRoutes on LXC %s: %w",
			cfg.Failover.LXCID, err)
	}

	// Step 4: announce routes on the primary VM.
	w.log.InfoContext(ctx, "BGP_RECOVERY: announcing routes on primary VM",
		"vmid", cfg.MwanVMID)
	if err := w.ops.AnnounceRoutes(ctx, cfg.MwanVMID); err != nil {
		w.log.ErrorContext(ctx, "BGP_RECOVERY: announce routes failed on primary",
			"vmid", cfg.MwanVMID, "err", err)
		return fmt.Errorf("BGP recovery AnnounceRoutes on primary VM %s: %w",
			cfg.MwanVMID, err)
	}

	elapsedSinceFailover := time.Duration(0)
	if !startedAt.IsZero() {
		elapsedSinceFailover = w.now().Sub(startedAt).Round(time.Second)
	}

	// Resolve the open "bgp-failover" alert so the next failover reads as a
	// fresh transition rather than a repeat. The recovery line emits at the
	// same severity the original Notify used (ERROR), per the Manager's
	// resolveAt contract, so it crosses the same MinLevel threshold.
	recoveryMsg := "BGP RECOVERED: routes back on primary VM " + cfg.MwanVMID
	w.notify.Resolve(ctx, "bgp-failover", cfg.MwanVMID, recoveryMsg,
		slog.Duration("recovery_elapsed", w.since(start).Round(time.Second)),
		slog.String("original_reason", prevReason),
		slog.Duration("time_in_failover", elapsedSinceFailover),
		slog.String("lxc", cfg.Failover.LXCID),
		slog.String("vmid", cfg.MwanVMID),
	)
	// Also emit a dedicated "bgp-recovered" event so dashboards or tests that
	// gate on the explicit recovery kind see the transition.
	w.notify.Notify(ctx, notify.Event{
		Now:     time.Time{},
		Level:   slog.LevelError,
		Kind:    "bgp-recovered",
		Key:     cfg.MwanVMID,
		Message: recoveryMsg,
		Fields: []slog.Attr{
			slog.Duration("recovery_elapsed", w.since(start).Round(time.Second)),
			slog.String("original_reason", prevReason),
			slog.Duration("time_in_failover", elapsedSinceFailover),
			slog.String("lxc", cfg.Failover.LXCID),
			slog.String("vmid", cfg.MwanVMID),
		},
		IsRecovery: false,
	})

	w.failoverMu.Lock()
	w.failoverActive = false
	w.failoverStartedAt = time.Time{}
	w.failoverReason = ""
	w.failoverMu.Unlock()

	w.log.InfoContext(ctx, "BGP_RECOVERY: complete",
		"vmid", cfg.MwanVMID,
		"elapsed", w.since(start),
	)

	return nil
}

// tryFailover attempts failover if the LXC has internet connectivity.
// Returns true if failover was triggered, false if skipped (LXC also down).
func (w *watchdog) tryFailover(ctx context.Context, cfg *config.Config, reason string) bool {
	if cfg.Failover.LXCID == "" {
		w.log.WarnContext(ctx, "FAILOVER: no failover LXC configured; skipping")
		return false
	}

	// Test LXC WAN connectivity before triggering failover.
	// If the LXC also has no internet, failover is pointless.
	w.log.InfoContext(ctx, "FAILOVER: testing LXC WAN connectivity", "lxc", cfg.Failover.LXCID)
	lxcV4 := w.ops.Ping(ctx, "ping", cfg.Network.PingTargetIPv4)
	lxcV6 := w.ops.Ping(ctx, "ping6", cfg.Network.PingTargetIPv6)
	// TODO: ping from inside LXC specifically, not from host.
	// For now, host-level ping is a proxy.

	if !lxcV4 && !lxcV6 {
		w.log.WarnContext(ctx, "FAILOVER: LXC also has no internet; skipping failover (real ISP outage)",
			"lxc", cfg.Failover.LXCID)
		return false
	}

	w.log.InfoContext(ctx, "FAILOVER: LXC has internet; proceeding",
		"lxc", cfg.Failover.LXCID, "v4", lxcV4, "v6", lxcV6)

	if err := w.triggerFailover(ctx, cfg, reason); err != nil {
		w.log.ErrorContext(ctx, "FAILOVER: failed", "err", err)
		return false
	}
	return true
}

// maybeTriggerRecovery fires triggerBGPRecovery when the watchdog is currently
// in a failover state and the latest probe cycle is healthy. It is a no-op
// when failover is not active so it can be called unconditionally from the
// healthy branch of the run loop. Recovery state is cleared only on success
// inside triggerBGPRecovery, so a retry happens on the next healthy cycle if
// the primary's BGP session is not yet established.
func (w *watchdog) maybeTriggerRecovery(ctx context.Context, cfg *config.Config) {
	w.failoverMu.Lock()
	active := w.failoverActive
	w.failoverMu.Unlock()
	if !active {
		return
	}
	w.log.InfoContext(ctx, "BGP_RECOVERY: healthy cycle while failover active; attempting recovery")
	if err := w.triggerBGPRecovery(ctx, cfg); err != nil {
		w.log.WarnContext(ctx, "BGP_RECOVERY: deferred (will retry on next healthy cycle)",
			"err", err)
	}
}

// FailoverRun is the entry point for `mwan watchdog failover`.
// It triggers failover immediately without the monitoring loop.
func FailoverRun(cfg *config.Config) error {
	handlers := []slog.Handler{logging.StdoutJSON()}
	if p := cfg.Watchdog.LogFile; p != "" {
		handlers = append(handlers, logging.FileText(p, "[watchdog]"))
	}
	if p := cfg.Watchdog.JSONLogFile; p != "" {
		handlers = append(handlers, logging.FileJSON(p))
	}
	// The slog email handler is intentionally not attached here: notify
	// owns email delivery for the watchdog now, so attaching the handler
	// would produce a duplicate email per failover or recovery event.
	logger, _ := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers:     handlers,
	})
	runID := tracing.NewID()
	logger = logger.With(
		slog.String(tracing.RunIDKey, runID),
		slog.String(tracing.ComponentKey, "watchdog"),
	)

	// Construct the notify boundary the watchdog uses for failover and
	// recovery emails. NullNotifier is returned when [email] is
	// unconfigured so the call sites are always safe.
	notifier := notify.FromConfig(cfg, logger, "mwan-watchdog")

	// Create watchdog instance
	w := newWatchdog(
		cfg, ops.NewRealOps(cfg, logger), notifier, &alert.Coord{}, logger, runID,
	)

	ctx := context.Background()
	logger.InfoContext(ctx, "Manual failover requested")

	if err := w.triggerFailover(ctx, cfg, "manual trigger via `mwan watchdog failover`"); err != nil {
		logger.ErrorContext(ctx, "Failover failed", "err", err)
		return err
	}

	logger.InfoContext(ctx, "Failover complete")
	return nil
}
