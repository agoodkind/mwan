package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/tracing"
)

const (
	alertKindConnectivityPartial = "connectivity-partial"
	alertKindConnectivityTotal   = "connectivity-total"
	connectivityPartialMessage   = "connectivity partial loss"
	connectivityTotalMessage     = "connectivity total loss"
	protoIPv4                    = "IPv4"
	protoIPv6                    = "IPv6"
	connectivityObservationTotal = "total"
	connectivityProbeSource      = "hypervisor"
	connectivityProbeSucceeded   = "succeeded"
	connectivityProbeFailed      = "failed"

	connectivityTotalReason          = "The hypervisor IPv4 and IPv6 probes failed. The watchdog did not find a recent configuration change."
	connectivityFailoverNotAttempted = "The watchdog did not attempt failover."
	connectivityFailoverNotTriggered = "The watchdog attempted failover, but failover did not trigger."
)

func (w *watchdog) sendPartialAlert(ctx context.Context, proto string) {
	log := w.tracedLogger(ctx)
	if !w.limiter.TrySendPartial(proto, w.now()) {
		remaining := w.limiter.PartialCooldownRemaining(proto, w.now())
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
	w.notifierOrNull().Notify(ctx, notify.Event{
		Now:        time.Time{},
		Level:      slog.LevelError,
		Kind:       alertKindConnectivityPartial,
		Key:        w.cfg.MwanVMID + "/" + proto,
		Message:    connectivityPartialMessage,
		Fields:     w.connectivityPartialFields(proto),
		IsRecovery: false,
	})
}

func (w *watchdog) connectivityPartialFields(proto string) []slog.Attr {
	target := w.cfg.Network.PingTargetIPv6
	if proto == protoIPv4 {
		target = w.cfg.Network.PingTargetIPv4
	}
	return []slog.Attr{
		slog.String("probe_source", connectivityProbeSource),
		slog.String("proto", proto),
		slog.String("target", target),
		slog.String("first_failed_probe_observed_at", formatObservedTime(w.connectivityLossObservedAt[proto])),
		slog.String("vm_id", w.cfg.MwanVMID),
		slog.String("node", w.cfg.PVE.Node),
	}
}

func (w *watchdog) connectivityTotalFields() []slog.Attr {
	return []slog.Attr{
		slog.String("probe_source", connectivityProbeSource),
		slog.String("target_ipv4", w.cfg.Network.PingTargetIPv4),
		slog.String("target_ipv6", w.cfg.Network.PingTargetIPv6),
		slog.String("first_failed_probe_observed_at", formatObservedTime(w.connectivityLossObservedAt[connectivityObservationTotal])),
		slog.String("vm_id", w.cfg.MwanVMID),
		slog.String("node", w.cfg.PVE.Node),
	}
}

func formatObservedTime(observedAt time.Time) string {
	return observedAt.UTC().Format(time.RFC3339)
}

func (w *watchdog) recoveryObservationFields(key string, recoveredAt time.Time) []slog.Attr {
	return []slog.Attr{
		slog.String("recovery_probe_observed_at", formatObservedTime(recoveredAt)),
		slog.Duration("observed_elapsed", recoveredAt.Sub(w.connectivityLossObservedAt[key]).Round(time.Second)),
	}
}

func (w *watchdog) observeConnectivityLoss(key string, observedAt time.Time) {
	if w.connectivityLossObservedAt == nil {
		w.connectivityLossObservedAt = map[string]time.Time{}
	}
	if _, observed := w.connectivityLossObservedAt[key]; !observed {
		w.connectivityLossObservedAt[key] = observedAt
	}
}

func (w *watchdog) resolvePartialConnectivityAlert(ctx context.Context, proto string, recoveredAt time.Time) {
	w.limiter.ResetPartialCooldown(proto)
	if _, observed := w.connectivityLossObservedAt[proto]; !observed {
		return
	}
	fields := append(w.connectivityPartialFields(proto), w.recoveryObservationFields(proto, recoveredAt)...)
	w.notifierOrNull().Resolve(ctx, alertKindConnectivityPartial, w.cfg.MwanVMID+"/"+proto,
		connectivityPartialMessage, fields...)
	delete(w.connectivityLossObservedAt, proto)
}

func (w *watchdog) resolveTotalConnectivityAlert(ctx context.Context, recoveredAt time.Time) {
	w.limiter.ResetTotalCooldown()
	if _, observed := w.connectivityLossObservedAt[connectivityObservationTotal]; !observed {
		return
	}
	fields := append(w.connectivityTotalFields(), w.recoveryObservationFields(connectivityObservationTotal, recoveredAt)...)
	if w.lastTotalLossReason != "" {
		fields = append(fields,
			slog.String("last_loss_reason", w.lastTotalLossReason),
			slog.String("last_loss_detail", w.lastTotalLossDetail),
		)
	}
	w.notifierOrNull().Resolve(ctx, alertKindConnectivityTotal, w.cfg.MwanVMID, connectivityTotalMessage, fields...)
	delete(w.connectivityLossObservedAt, connectivityObservationTotal)
	w.lastTotalLossReason = ""
	w.lastTotalLossDetail = ""
}

func (w *watchdog) resolveConnectivityAlerts(ctx context.Context, v4ok, v6ok bool) {
	observedAt := w.now()
	if v4ok {
		w.resolvePartialConnectivityAlert(ctx, protoIPv4, observedAt)
	} else {
		w.observeConnectivityLoss(protoIPv4, observedAt)
	}
	if v6ok {
		w.resolvePartialConnectivityAlert(ctx, protoIPv6, observedAt)
	} else {
		w.observeConnectivityLoss(protoIPv6, observedAt)
	}
	if v4ok || v6ok {
		w.resolveTotalConnectivityAlert(ctx, observedAt)
	} else {
		w.observeConnectivityLoss(connectivityObservationTotal, observedAt)
	}
}

func (w *watchdog) sendTotalAlert(ctx context.Context, reason, detail string) {
	log := w.tracedLogger(ctx)
	w.lastTotalLossReason = reason
	w.lastTotalLossDetail = detail
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
	w.notifierOrNull().Notify(ctx, notify.Event{
		Now:     time.Time{},
		Level:   slog.LevelError,
		Kind:    alertKindConnectivityTotal,
		Key:     w.cfg.MwanVMID,
		Message: connectivityTotalMessage,
		Fields: append(w.connectivityTotalFields(),
			slog.String("reason", reason),
			slog.String("detail", detail),
		),
		IsRecovery: false,
	})
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

	gatewayProbe := connectivityProbeFailed
	failover := connectivityFailoverNotAttempted
	if vmOK {
		gatewayProbe = connectivityProbeSucceeded
	} else if w.cfg.Failover.LXCID != "" {
		if w.tryFailover(ctx, w.cfg, "Total connectivity loss on primary, no recent config change") {
			return true
		}
		failover = connectivityFailoverNotTriggered
	}
	reason := connectivityTotalReason
	detail := fmt.Sprintf("Gateway default-route probe: %s\nFailover: %s", gatewayProbe, failover)

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
