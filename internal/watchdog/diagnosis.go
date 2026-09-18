package watchdog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"goodkind.io/mwan/internal/tracing"
)

func (w *watchdog) sendPartialAlert(ctx context.Context, proto string) {
	log := w.tracedLogger(ctx)
	if !w.limiter.TrySendPartial(w.now()) {
		remaining := w.limiter.PartialCooldownRemaining(w.now())
		log.InfoContext(ctx,
			"Partial alert suppressed (cooldown)",
			"protocol", proto,
			"remaining", remaining.Round(time.Second),
		)
		return
	}
	log.InfoContext(ctx,
		"Sending partial-degradation alert",
		"protocol_down", proto,
	)
	log.WarnContext(ctx,
		"connectivity partial loss",
		"proto", proto,
		"vm_id", w.cfg.MwanVMID,
		"node", w.cfg.PVE.Node,
	)
}

func (w *watchdog) sendTotalAlert(ctx context.Context, reason, detail string) {
	log := w.tracedLogger(ctx)
	_ = detail
	if !w.limiter.TrySendTotal(w.now()) {
		remaining := w.limiter.TotalCooldownRemaining(w.now())
		log.InfoContext(ctx,
			"Total alert suppressed (cooldown)",
			"remaining", remaining.Round(time.Second),
			"reason", reason,
		)
		return
	}
	log.InfoContext(ctx, "Sending total-loss alert", "reason", reason)
	log.ErrorContext(ctx,
		"connectivity total loss",
		"reason", reason,
		"vm_id", w.cfg.MwanVMID,
		"node", w.cfg.PVE.Node,
		"err", reason,
	)
}

// logPushedStatus records what the gateway last said about its providers and
// how old that report is. It replaces the per-interface pings this diagnosis
// used to run through guest-exec from a hand-typed interface list: the gateway
// probes every provider on its own cadence with hysteresis, its verdict is
// fresher, and it covers every provider rather than the two the list named.
func (w *watchdog) logPushedStatus(ctx context.Context) {
	log := w.tracedLogger(ctx)
	if w.status == nil {
		log.InfoContext(ctx, "No gateway status listener configured")
		w.appendProbe("Gateway status: no listener configured")
		return
	}
	status, receivedAt, ok := w.status.Latest()
	if !ok {
		log.InfoContext(ctx, "No gateway status received yet")
		w.appendProbe("Gateway status: none received")
		return
	}
	names := make([]string, 0, len(status.Providers))
	for name := range status.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	verdicts := make([]string, 0, len(names))
	for _, name := range names {
		verdicts = append(verdicts, name+"="+status.Providers[name])
	}
	joined := strings.Join(verdicts, " ")
	age := w.now().Sub(receivedAt).Round(time.Second)
	log.InfoContext(ctx,
		"Gateway provider status",
		"active_tier", status.ActiveTier,
		"providers", joined,
		"sent_at", status.SentAt.Format(time.RFC3339),
		"age", age,
	)
	w.appendProbe(fmt.Sprintf(
		"Gateway status (age %s): active tier %d, %s",
		age, status.ActiveTier, joined,
	))
}

// diagnoseNoRecentChange runs VM connectivity diagnostics and logs the
// gateway's last pushed provider status when no recent config change was
// detected. It may trigger a failover to the LXC. Returns true if a failover
// was triggered (caller should return early).
func (w *watchdog) diagnoseNoRecentChange(ctx context.Context) bool {
	log := w.tracedLogger(ctx)
	log.InfoContext(ctx,
		"No recent config change; running diagnostics for alert context",
	)
	vmOK := w.testVMConnectivity(ctx)
	w.logPushedStatus(ctx)

	var reason, detail string
	if vmOK {
		reason = "Proxmox host cannot reach internet but MWAN VM can"
		detail = "VM has internet via default route. " +
			"This suggests a Proxmox-side routing or " +
			"OPNsense issue, not an MWAN configuration " +
			"problem.\nNo rollback triggered. " +
			"Manual investigation needed."
	} else {
		// VM has no internet and no config changed.
		// Check if failover LXC can reach the internet.
		// If yes: failover is useful (VM routing broken, LXC WAN works).
		// If no: real ISP outage, failover is pointless.
		if w.cfg.Failover.LXCID != "" {
			if w.tryFailover(ctx, w.cfg, "Total connectivity loss on primary, no recent config change") {
				return true
			}
		}
		reason = "Total connectivity loss, no recent config change"
		detail = fmt.Sprintf(
			"All connectivity tests failed and no config "+
				"change detected within %dm.\n"+
				"Failover LXC also unreachable or not configured. "+
				"Treating as external outage.",
			w.cfg.Watchdog.DeployWindowMinutes,
		)
	}

	w.sendTotalAlert(ctx, reason, detail)
	log.InfoContext(ctx,
		"--- DIAGNOSIS END (no recent config change) ---",
		"reason", reason,
	)
	return false
}

func (w *watchdog) handleTimeoutExceeded(ctx context.Context) {
	diagCtx := tracing.WithOperation(ctx, "diagnose_connectivity")
	diagCtx, _ = tracing.StartTrace(diagCtx, "", "diagnose_connectivity")
	log := w.tracedLogger(diagCtx)
	log.InfoContext(ctx, "--- DIAGNOSIS START ---")

	log.InfoContext(ctx, "Step 1: checking for recent config change...")
	deployTS, recent := w.checkDeploy(diagCtx)

	if !recent {
		if w.diagnoseNoRecentChange(diagCtx) {
			log.InfoContext(ctx, "--- DIAGNOSIS END (failover triggered) ---")
		}
		sleepOrDone(diagCtx, 60*time.Second)
		return
	}

	if w.cfg.Watchdog.DeployGracePeriodSeconds > 0 {
		rawDeployTS, dOK := w.readGuestUnix(
			diagCtx, w.cfg.Network.LastDeployPath,
		)
		if dOK {
			deployAge := w.now().Unix() - rawDeployTS
			grace := int64(w.cfg.Watchdog.DeployGracePeriodSeconds)
			if deployAge >= 0 && deployAge < grace {
				remaining := grace - deployAge
				log.InfoContext(ctx,
					"Within deploy grace period; waiting for "+
						"VM to stabilize",
					"deploy_ts", rawDeployTS,
					"deploy_age_seconds", deployAge,
					"remaining_seconds", remaining,
					"grace_period_seconds",
					w.cfg.Watchdog.DeployGracePeriodSeconds,
				)
				log.InfoContext(ctx,
					"--- DIAGNOSIS END (deploy grace period) ---",
				)
				sleepOrDone(
					ctx,
					time.Duration(remaining)*time.Second,
				)
				return
			}
		}
	}

	log.InfoContext(ctx,
		"Config recently changed and connectivity still down",
		"deploy_ts", deployTS,
	)

	if w.attemptRollbackForDeploy(diagCtx, deployTS) {
		log.InfoContext(ctx,
			"Waiting for VM to boot and routes to converge after rollback",
			"grace", w.cfg.Watchdog.PostRollbackGraceSeconds,
		)
		sleepOrDone(diagCtx, w.cfg.Watchdog.PostRollbackGrace())
	} else {
		sleepOrDone(diagCtx, 60*time.Second)
	}
}
