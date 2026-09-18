package watchdog

import (
	"context"
	"os"
	"strings"

	"goodkind.io/mwan/internal/rollback"
	"goodkind.io/mwan/internal/version"
)

// logStartupConfig emits structured log entries describing the watchdog's
// configuration. Called once at the beginning of run().
func (w *watchdog) logStartupConfig(ctx context.Context) {
	log := w.log
	log.InfoContext(ctx,
		"Starting MWAN watchdog",
		"vmid", w.cfg.MwanVMID,
		"deploy_window_minutes", w.cfg.Watchdog.DeployWindowMinutes,
		"connectivity_timeout_seconds", w.cfg.Watchdog.ConnectivityTimeoutSeconds,
		"check_interval_healthy", w.cfg.Watchdog.HealthyInterval(),
		"check_interval_degraded", w.cfg.Watchdog.DegradedInterval(),
		"alert_cooldown_seconds", w.cfg.Watchdog.AlertCooldownSeconds,
	)
	log.InfoContext(ctx,
		"Network config",
		"ping_target_ipv4", w.cfg.Network.PingTargetIPv4,
		"ping_target_ipv6", w.cfg.Network.PingTargetIPv6,
		"last_deploy_path", w.cfg.Network.LastDeployPath,
	)
	log.InfoContext(ctx,
		"PVE",
		"node", w.cfg.PVE.Node,
		"pve_api_configured", w.cfg.PVE.TokenID != "",
		"vsock_cid", w.cfg.Watchdog.VsockCID,
		"vsock_port", w.cfg.Watchdog.VsockPort,
	)
}

// runStartupChecks performs one-time checks when the watchdog loop begins:
// recovering from interrupted rollbacks, verifying snapshots exist, probing
// connectivity, and checking the config hash.
func (w *watchdog) runStartupChecks(ctx context.Context) {
	log := w.tracedLogger(ctx)
	w.recoverInterrupted(ctx)

	// A watchdog that died mid-snapshot can leave the guest frozen with no
	// process left to thaw it; recover that state before anything else
	// depends on guest-exec.
	w.ensureGuestThawed(ctx, "startup")

	// Startup checks.
	if listOut, lErr := w.ops.VMSnapshots(ctx, w.cfg.MwanVMID); lErr == nil {
		if rollback.ExtractLatestSnapshot(listOut) == "" {
			log.WarnContext(ctx,
				"No rollback snapshots found at startup",
				"vmid", w.cfg.MwanVMID,
				"note", "rollback will not be possible until a snapshot is created",
			)
		}
	} else {
		log.WarnContext(ctx, "Could not check snapshots at startup", "err", lErr)
	}
	if data, lErr := os.ReadFile(w.cfg.Watchdog.RollbackLockFile); lErr == nil {
		log.WarnContext(ctx,
			"Stale rollback lock file found at startup",
			"path", w.cfg.Watchdog.RollbackLockFile,
			"content", strings.TrimSpace(string(data)),
			"note", "a previous run may have crashed mid-rollback",
		)
	}

	// Run a connectivity probe and config hash check before the startup log
	// so the channel tracker and ping results reflect actual startup state.
	// Only probe if the VM is currently running.
	var startupV4, startupV6 bool
	if running, err := w.ops.VMStatus(ctx, w.cfg.MwanVMID); err == nil && running {
		startupV4, startupV6 = w.probeConnectivity(ctx)
		w.checkConfigHash(ctx)
	}
	v4str, v6str := "OK", "OK"
	if !startupV4 {
		v4str = "FAIL"
	}
	if !startupV6 {
		v6str = "FAIL"
	}
	log.InfoContext(ctx,
		"watchdog started",
		"vm_id", w.cfg.MwanVMID,
		"node", w.cfg.PVE.Node,
		"build", version.BuildVersionString(),
		"ipv4", v4str,
		"ipv6", v6str,
		"deploy_window_minutes", w.cfg.Watchdog.DeployWindowMinutes,
		"check_interval_healthy", w.cfg.Watchdog.HealthyInterval(),
	)
}
