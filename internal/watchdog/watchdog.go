package watchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"goodkind.io/mwan/internal/alert"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/rollback"
	"goodkind.io/mwan/internal/statuspush"
	"goodkind.io/mwan/internal/tracing"
)

// statusSource is the gateway verdict a diagnosis reads. The watchdog depends
// on the interface so a test seeds a verdict without a socket; the production
// implementation is the statuspush listener run starts.
type statusSource interface {
	Latest() (statuspush.Status, time.Time, bool)
}

type connectivityState string

const (
	stateUnknown   connectivityState = "unknown"
	stateHealthy   connectivityState = "healthy"
	statePartial   connectivityState = "partial"
	stateDown      connectivityState = "down"
	stateVMStopped connectivityState = "vm_stopped"

	// heartbeatInterval controls how often we log "still healthy" in steady state.
	heartbeatInterval = 5 * time.Minute
)

type watchdog struct {
	cfg     *config.Config
	ops     ops.SysOps
	notify  notify.Notifier
	coord   *alert.Coord
	limiter *alert.Limiter
	log     *slog.Logger
	runID   string

	// exitFn, if non-nil, replaces os.Exit when rollback defers shutdown after SIGTERM.
	exitFn func(code int)
	// testHeartbeatInterval overrides heartbeatInterval in run() when > 0 (tests only).
	testHeartbeatInterval time.Duration
	nowFn                 func() time.Time

	lastState             connectivityState
	vmStoppedLogged       bool
	recoveredFromRollback bool // set by recoverInterrupted when VM was stopped due to rollback
	consecutiveTotalFails int
	totalDownStartUnix    int64
	lastHeartbeat         time.Time

	// probeLog accumulates per-cycle probe results for inclusion in emails.
	probeLog []string

	// tracker may be nil when ops is not realOps (e.g. mock in tests).
	tracker *ops.ChannelTracker

	// status holds the gateway's last pushed provider verdict. Nil on a host
	// with no listener port configured, which is every host but the two
	// hypervisors.
	status statusSource

	lastConfigHash        string
	lastManifest          map[string]string // path -> sha256hex from previous run
	hashChangeWindowStart int64
	consecutiveHealthy    int
	lastSnapshotAt        time.Time
	healthyCyclesForHash  int

	// Snapshot failure state. See snapshot_recovery.go for how a failed
	// snapshot cycle is paced and how its leftovers are cleaned up.
	consecutiveSnapshotFails int
	snapshotBackoffUntil     time.Time
	forcedDeletes            int

	postRollbackGraceUntil time.Time
	lastHashCheckOK        bool
	totalFailStart         time.Time

	// failoverMu guards failoverActive, failoverStartedAt, and failoverReason.
	// These track BGP failover state so the recovery hook can fire exactly
	// once when the primary returns.
	failoverMu        sync.Mutex
	failoverActive    bool
	failoverStartedAt time.Time
	failoverReason    string
}

// newWatchdog builds a watchdog with every field set, so the zero values
// the loop relies on are stated rather than implied. Both the daemon and
// the manual failover command construct through here.
func newWatchdog(
	cfg *config.Config,
	sysOps ops.SysOps,
	notifier notify.Notifier,
	coord *alert.Coord,
	logger *slog.Logger,
	runID string,
) *watchdog {
	return &watchdog{
		cfg:     cfg,
		ops:     sysOps,
		notify:  notifier,
		coord:   coord,
		limiter: alert.NewLimiter(cfg.Watchdog.AlertCooldownSeconds),
		log:     logger,
		runID:   runID,

		exitFn:                nil,
		testHeartbeatInterval: 0,
		nowFn:                 time.Now,

		lastState:             "",
		vmStoppedLogged:       false,
		recoveredFromRollback: false,
		consecutiveTotalFails: 0,
		totalDownStartUnix:    0,
		lastHeartbeat:         time.Time{},

		probeLog: nil,
		tracker:  nil,
		status:   nil,

		lastConfigHash:        "",
		lastManifest:          nil,
		hashChangeWindowStart: 0,
		consecutiveHealthy:    0,
		lastSnapshotAt:        time.Time{},
		healthyCyclesForHash:  0,

		consecutiveSnapshotFails: 0,
		snapshotBackoffUntil:     time.Time{},
		forcedDeletes:            0,

		postRollbackGraceUntil: time.Time{},
		lastHashCheckOK:        false,
		totalFailStart:         time.Time{},

		failoverMu:        sync.Mutex{},
		failoverActive:    false,
		failoverStartedAt: time.Time{},
		failoverReason:    "",
	}
}

func (w *watchdog) heartbeatTick() time.Duration {
	if w.testHeartbeatInterval > 0 {
		return w.testHeartbeatInterval
	}
	return heartbeatInterval
}

func (w *watchdog) now() time.Time {
	if w.nowFn == nil {
		now := time.Now
		return now()
	}
	return w.nowFn()
}

func (w *watchdog) since(start time.Time) time.Duration {
	return w.now().Sub(start)
}

func (w *watchdog) appendProbe(msg string) {
	w.probeLog = append(w.probeLog, msg)
}

func (w *watchdog) flushProbeLog() string {
	s := strings.Join(w.probeLog, "\n")
	w.probeLog = w.probeLog[:0]
	return s
}

func (w *watchdog) tracedLogger(ctx context.Context) *slog.Logger {
	return tracing.Logger(ctx, w.log)
}

// notifierOrNull returns w.notify when configured, or a NullNotifier
// otherwise. Tests construct watchdog instances without wiring notify so
// the call sites stay safe without conditional guards.
func (w *watchdog) notifierOrNull() notify.Notifier {
	if w.notify == nil {
		return notify.NullNotifier{}
	}
	return w.notify
}

func (w *watchdog) findSnapshot(ctx context.Context) (string, error) {
	log := w.tracedLogger(ctx)
	log.InfoContext(ctx, "Listing snapshots for VM", "vmid", w.cfg.MwanVMID)
	out, err := w.ops.VMSnapshots(ctx, w.cfg.MwanVMID)
	if err != nil {
		return "", err
	}
	snap := rollback.ExtractLatestSnapshot(out)
	if snap == "" {
		log.InfoContext(ctx,
			"No rollback snapshot (pre-deploy-* or known-good-*)",
			"listsnapshot_output", string(out),
		)
	} else {
		log.InfoContext(ctx, "Found rollback snapshot", "snapshot", snap)
	}
	return snap, nil
}

func (w *watchdog) maybeSnapshot(ctx context.Context) {
	log := w.tracedLogger(ctx)
	if w.cfg.Watchdog.SnapshotHealthyThreshold <= 0 {
		return
	}
	if w.consecutiveHealthy < w.cfg.Watchdog.SnapshotHealthyThreshold {
		return
	}
	if w.snapshotBackoffActive() {
		return
	}
	windowSec := int64(w.cfg.Watchdog.DeployWindowMinutes) * 60
	if w.hashChangeWindowStart > 0 {
		elapsed := w.now().Unix() - w.hashChangeWindowStart
		if elapsed < windowSec {
			return
		}
	}
	deployTS, dOK := w.readGuestUnix(ctx, w.cfg.Network.LastDeployPath)
	if dOK && (w.now().Unix()-deployTS) < windowSec {
		return
	}
	minGap := time.Duration(w.cfg.Watchdog.MinSnapshotIntervalSeconds) * time.Second
	if !w.lastSnapshotAt.IsZero() && w.since(w.lastSnapshotAt) < minGap {
		return
	}
	if w.cfg.Watchdog.HashCheckEveryNHealthy > 0 && !w.lastHashCheckOK {
		return
	}
	name := "known-good-" + w.now().Format("20060102-150405")
	snapErr := w.ops.VMSnapshot(ctx, w.cfg.MwanVMID, name)
	// The snapshot freezes guest filesystems through the guest agent, and a
	// thaw that never lands leaves the guest wedged with guest-exec disabled
	// and new logins hanging. Verify the thaw after every attempt, failed
	// ones included, because an aborted snapshot is exactly when the thaw
	// goes missing.
	w.ensureGuestThawed(ctx, "post-snapshot")
	if snapErr != nil {
		w.noteSnapshotFailure(ctx, name, snapErr)
		w.clearStaleGuestLock(ctx, "post-snapshot")
		return
	}
	log.InfoContext(ctx, "created known-good snapshot", "snapshot", name)
	w.lastSnapshotAt = w.now()
	w.consecutiveHealthy = 0
	w.noteSnapshotSuccess(ctx)
	if err := w.pruneSnapshots(ctx); err != nil {
		log.ErrorContext(ctx, "pruneSnapshots failed", "err", err)
		w.clearStaleGuestLock(ctx, "post-prune")
	}
}

func (w *watchdog) pruneSnapshots(ctx context.Context) error {
	log := w.tracedLogger(ctx)
	out, err := w.ops.VMSnapshots(ctx, w.cfg.MwanVMID)
	if err != nil {
		return err
	}
	s := string(out)
	knownGoods := rollback.KnownGoodSnapRE.FindAllString(s, -1)
	sort.Strings(knownGoods)
	preDeploys := rollback.PreDeploySnapRE.FindAllString(s, -1)
	total := len(knownGoods) + len(preDeploys)

	var firstErr error
	if w.cfg.Watchdog.MaxKnownGoodSnapshots > 0 &&
		len(knownGoods) > w.cfg.Watchdog.MaxKnownGoodSnapshots {
		toDrop := len(knownGoods) - w.cfg.Watchdog.MaxKnownGoodSnapshots
		for i := range toDrop {
			// One snapshot that refuses to go must not hold back the
			// rest of the rotation, so the pass keeps going and reports
			// the first failure at the end.
			if err := w.deleteSnapshot(ctx, knownGoods[i]); err != nil {
				log.ErrorContext(ctx,
					"vmDelSnapshot",
					"snapshot", knownGoods[i],
					"err", err,
				)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		out, err = w.ops.VMSnapshots(ctx, w.cfg.MwanVMID)
		if err != nil {
			return err
		}
		s = string(out)
		knownGoods = rollback.KnownGoodSnapRE.FindAllString(s, -1)
		sort.Strings(knownGoods)
		preDeploys = rollback.PreDeploySnapRE.FindAllString(s, -1)
		total = len(knownGoods) + len(preDeploys)
	}

	if w.cfg.Watchdog.MaxTotalSnapshots <= 0 ||
		total <= w.cfg.Watchdog.MaxTotalSnapshots || len(knownGoods) == 0 {
		return firstErr
	}
	excess := min(total-w.cfg.Watchdog.MaxTotalSnapshots, len(knownGoods))
	for i := range excess {
		if err := w.deleteSnapshot(ctx, knownGoods[i]); err != nil {
			log.ErrorContext(ctx,
				"vmDelSnapshot max total",
				"snapshot", knownGoods[i],
				"err", err,
			)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// executeRollbackVM performs the stop-rollback-start cycle on the MWAN VM.
// It deletes intermediate snapshots that are children of the target (Proxmox/ZFS
// requires the target to be a leaf), then runs qm rollback and qm start.
// Returns a non-nil error if the qm rollback command itself failed.
func (w *watchdog) executeRollbackVM(ctx context.Context, snap string) error {
	log := w.tracedLogger(ctx)
	stopStart := w.now()
	log.InfoContext(ctx,
		"Stopping VM",
		"vmid", w.cfg.MwanVMID,
		"timeout", ops.TimeoutQmStop,
	)
	if err := w.ops.VMStop(ctx, w.cfg.MwanVMID); err != nil {
		log.ErrorContext(ctx,
			"vmStop error (continuing to rollback)",
			"vmid", w.cfg.MwanVMID,
			"err", err,
		)
	} else {
		log.DebugContext(ctx,
			"VM stopped",
			"vmid", w.cfg.MwanVMID,
			"elapsed", w.since(stopStart).Round(time.Millisecond),
		)
	}

	// Delete any watchdog-managed snapshots that are children of the target.
	// Proxmox/ZFS only allows rollback to the leaf snapshot in the chain.
	if listOut, lErr := w.ops.VMSnapshots(ctx, w.cfg.MwanVMID); lErr == nil {
		toDelete := rollback.SnapshotsAfter(listOut, snap)
		for _, child := range slices.Backward(toDelete) {
			log.DebugContext(ctx, "Deleting intermediate snapshot before rollback",
				"snapshot", child, "target", snap)
			if dErr := w.ops.VMDelSnapshot(ctx, w.cfg.MwanVMID, child); dErr != nil {
				log.ErrorContext(ctx, "Failed to delete intermediate snapshot",
					"snapshot", child, "err", dErr)
			}
		}
	} else {
		log.WarnContext(ctx, "Could not list snapshots before rollback", "err", lErr)
	}

	var rollbackErr error
	rollbackStart := w.now()
	log.InfoContext(ctx,
		"Running qm rollback",
		"vmid", w.cfg.MwanVMID,
		"snapshot", snap,
		"timeout", ops.TimeoutQmLockHolding,
	)
	if err := w.ops.VMRollback(ctx, w.cfg.MwanVMID, snap); err != nil {
		rollbackErr = err
		log.ErrorContext(ctx,
			"qm rollback FAILED; attempting qm start anyway",
			"vmid", w.cfg.MwanVMID,
			"snapshot", snap,
			"elapsed", w.since(rollbackStart).Round(time.Millisecond),
			"err", err,
		)
	} else {
		log.InfoContext(ctx,
			"qm rollback completed",
			"elapsed", w.since(rollbackStart).Round(time.Millisecond),
		)
	}

	startTime := w.now()
	log.InfoContext(ctx,
		"Starting VM",
		"vmid", w.cfg.MwanVMID,
		"timeout", ops.TimeoutQmStart,
	)
	if err := w.ops.VMStart(ctx, w.cfg.MwanVMID); err != nil {
		log.ErrorContext(ctx,
			"qm start FAILED; VM may remain stopped",
			"vmid", w.cfg.MwanVMID,
			"elapsed", w.since(startTime).Round(time.Millisecond),
			"err", err,
		)
	} else {
		log.InfoContext(ctx,
			"VM started",
			"vmid", w.cfg.MwanVMID,
			"elapsed", w.since(startTime).Round(time.Millisecond),
		)
	}
	return rollbackErr
}

// recordRollbackResult persists rollback state, removes the lock file, and
// logs the outcome. Called after executeRollbackVM completes.
func (w *watchdog) recordRollbackResult(
	ctx context.Context,
	deployTS int64,
	snap string,
	rollbackErr error,
) {
	log := w.tracedLogger(ctx)
	if err := os.Remove(w.cfg.Watchdog.RollbackLockFile); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		log.ErrorContext(ctx, "remove rollback lock", "err", err)
	} else {
		log.InfoContext(ctx, "Removed rollback lock file")
	}

	rollbackAttempts := 1
	if existing, att, _ := rollback.AlreadyDone(
		w.cfg.Watchdog.RollbackStateFile, deployTS,
	); !existing {
		rollbackAttempts = att + 1
	}
	rollbackSucceeded := rollbackErr == nil
	if writeErr := rollback.WriteState(
		w.cfg.Watchdog.RollbackStateFile, deployTS, snap,
		rollbackAttempts, rollbackSucceeded, w.now(),
	); writeErr != nil {
		log.ErrorContext(ctx, "write rollback state", "err", writeErr)
	} else {
		log.InfoContext(ctx,
			"Wrote rollback state",
			"path", w.cfg.Watchdog.RollbackStateFile,
			"deploy_ts", deployTS,
			"snapshot", snap,
			"success", rollbackSucceeded,
			"attempts", rollbackAttempts,
		)
	}

	log.WarnContext(ctx,
		"auto-rollback completed",
		"vm_id", w.cfg.MwanVMID,
		"snapshot", snap,
		"node", w.cfg.PVE.Node,
	)
}

func (w *watchdog) rollback(ctx context.Context, deployTS int64, snap string) {
	log := w.tracedLogger(ctx)
	w.hashChangeWindowStart = 0
	w.coord.SetRollingBack(true)
	w.totalFailStart = time.Time{}
	w.consecutiveHealthy = 0
	defer w.coord.SetRollingBack(false)

	_ = w.flushProbeLog()

	lockContent := fmt.Sprintf(
		"deploy_ts=%d snapshot=%s ts=%d\n",
		deployTS, snap, w.now().Unix(),
	)
	if err := os.WriteFile(
		w.cfg.Watchdog.RollbackLockFile, []byte(lockContent), 0o644,
	); err != nil {
		log.ErrorContext(ctx, "write rollback lock", "err", err)
	} else {
		log.InfoContext(ctx, "Wrote rollback lock", "path", w.cfg.Watchdog.RollbackLockFile)
	}

	log.InfoContext(ctx,
		"INITIATING ROLLBACK",
		"vmid", w.cfg.MwanVMID,
		"snapshot", snap,
		"deploy_ts", deployTS,
		"deploy_age_seconds", w.now().Unix()-deployTS,
	)

	rollbackErr := w.executeRollbackVM(ctx, snap)
	w.recordRollbackResult(ctx, deployTS, snap, rollbackErr)

	log.InfoContext(ctx,
		"ROLLBACK COMPLETE; waiting for routes to converge",
		"vmid", w.cfg.MwanVMID,
		"snapshot", snap,
		"grace", w.cfg.Watchdog.PostRollbackGraceSeconds,
	)
	w.postRollbackGraceUntil = w.now().Add(
		time.Duration(w.cfg.Watchdog.DeployWindowMinutes) * time.Minute,
	)
	if w.coord.TakeShutdownAfterRollback() {
		log.InfoContext(ctx, "Deferred shutdown now executing after rollback")
		if w.exitFn != nil {
			w.exitFn(0)
		}
	}
}

func (w *watchdog) recoverInterrupted(ctx context.Context) {
	log := w.tracedLogger(ctx)
	data, err := os.ReadFile(w.cfg.Watchdog.RollbackLockFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.ErrorContext(ctx, "read rollback lock", "err", err)
		return
	}
	log.InfoContext(ctx,
		"Found rollback lock from previous instance",
		"lock_content", strings.TrimSpace(string(data)),
	)
	running, statusErr := w.ops.VMStatus(ctx, w.cfg.MwanVMID)
	if statusErr != nil {
		log.ErrorContext(ctx, "qm status during recovery", "err", statusErr)
		return
	}
	if running {
		log.InfoContext(ctx,
			"VM is running; previous rollback completed. Removing lock.",
			"vmid", w.cfg.MwanVMID,
		)
		if err := os.Remove(w.cfg.Watchdog.RollbackLockFile); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			log.ErrorContext(ctx, "remove stale rollback lock", "err", err)
		}
		return
	}
	log.InfoContext(ctx,
		"VM is STOPPED and rollback lock exists; attempting to start VM to complete interrupted rollback",
		"vmid", w.cfg.MwanVMID,
	)
	if startErr := w.ops.VMStart(ctx, w.cfg.MwanVMID); startErr != nil {
		log.ErrorContext(ctx,
			"VM start after interrupted rollback FAILED; manual intervention needed",
			"vmid", w.cfg.MwanVMID,
			"err", startErr,
		)
	} else {
		log.InfoContext(ctx,
			"VM started successfully after interrupted rollback",
			"vmid", w.cfg.MwanVMID,
		)
	}
	if err := os.Remove(w.cfg.Watchdog.RollbackLockFile); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		log.ErrorContext(ctx, "remove rollback lock after recovery", "err", err)
	}
	w.recoveredFromRollback = true
	log.InfoContext(ctx,
		"Waiting for VM to boot and routes to converge after interrupted rollback recovery",
		"grace", w.cfg.Watchdog.PostRollbackGraceSeconds,
	)
	if !sleepOrDone(ctx, time.Duration(w.cfg.Watchdog.PostRollbackGraceSeconds)*time.Second) {
		log.InfoContext(ctx, "Context cancelled during interrupted rollback recovery")
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// handleVMStopped handles the case when the MWAN VM is not running.
// It logs the event, attempts auto-start if appropriate, and returns true
// if the caller should continue to the next loop iteration.
func (w *watchdog) handleVMStopped(ctx context.Context) {
	log := w.tracedLogger(ctx)
	if w.vmStoppedLogged {
		return
	}
	log.InfoContext(ctx,
		"VM is not running; pausing checks",
		"vmid", w.cfg.MwanVMID,
		"recheck_interval", w.cfg.Watchdog.DegradedInterval(),
	)
	w.vmStoppedLogged = true
	w.lastState = stateVMStopped

	// Only alert and auto-start when no rollback is in progress.
	// If the rollback lock exists, the watchdog itself stopped the VM
	// intentionally; do not interfere. Also skip if recoverInterrupted
	// already handled this episode (lock was present at startup and removed).
	_, lockErr := os.Stat(w.cfg.Watchdog.RollbackLockFile)
	rollbackInProgress := lockErr == nil || w.recoveredFromRollback
	if rollbackInProgress {
		log.InfoContext(ctx,
			"VM stopped but rollback lock present; skipping alert and auto-start",
			"lock_file", w.cfg.Watchdog.RollbackLockFile,
		)
		return
	}
	log.ErrorContext(ctx,
		"VM stopped unexpectedly",
		"vm_id", w.cfg.MwanVMID,
		"node", w.cfg.PVE.Node,
		"err", "vm transitioned to stopped state outside of mwan control",
	)

	log.InfoContext(ctx, "Attempting to start stopped VM", "vmid", w.cfg.MwanVMID)
	if startErr := w.ops.VMStart(ctx, w.cfg.MwanVMID); startErr != nil {
		log.ErrorContext(ctx,
			"vmStart failed for stopped VM",
			"vmid", w.cfg.MwanVMID,
			"err", startErr,
		)
	} else {
		log.InfoContext(ctx, "vmStart issued for stopped VM", "vmid", w.cfg.MwanVMID)
	}
}

// handleHealthyProbe processes a fully-healthy probe result (both v4 and v6 OK).
// It updates counters, checks config hash periodically, manages snapshots, and
// emits heartbeat logs.
func (w *watchdog) handleHealthyProbe(ctx context.Context, iteration int) {
	log := w.tracedLogger(ctx)
	w.consecutiveHealthy++
	w.healthyCyclesForHash++
	if w.cfg.Watchdog.HashCheckEveryNHealthy > 0 &&
		w.healthyCyclesForHash >= w.cfg.Watchdog.HashCheckEveryNHealthy {
		w.healthyCyclesForHash = 0
		w.checkConfigHash(ctx)
	}
	w.maybeSnapshot(ctx)

	if w.lastState != stateHealthy {
		log.InfoContext(ctx,
			"Connectivity OK: IPv4 and IPv6",
			"previous_state", w.lastState,
		)
	} else if w.since(w.lastHeartbeat) >= w.heartbeatTick() {
		log.InfoContext(ctx,
			"Heartbeat: connectivity healthy",
			"ping_target_ipv4", w.cfg.Network.PingTargetIPv4,
			"ping_target_ipv6", w.cfg.Network.PingTargetIPv6,
			"iteration", iteration,
		)
		if w.tracker != nil {
			w.tracker.LogAll(ctx, log)
		}
		w.lastHeartbeat = w.now()
	}
	w.lastState = stateHealthy
	w.consecutiveTotalFails = 0
	w.totalDownStartUnix = 0
	w.limiter.ResetCooldowns()
	w.probeLog = w.probeLog[:0]
}

// handlePartialProbe processes a probe where one protocol is up and the other
// is down.
func (w *watchdog) handlePartialProbe(ctx context.Context, v6ok bool) {
	log := w.tracedLogger(ctx)
	downProto := "IPv6"
	if v6ok {
		downProto = "IPv4"
	}
	if w.lastState != statePartial {
		log.InfoContext(ctx,
			"Partial degradation: one protocol DOWN, other OK",
			"protocol_down", downProto,
			"previous_state", w.lastState,
		)
	} else {
		log.InfoContext(ctx,
			"Still in partial degradation: one protocol DOWN, other OK",
			"protocol_down", downProto,
		)
	}
	w.lastState = statePartial
	w.consecutiveTotalFails = 0
	w.totalDownStartUnix = 0
	w.sendPartialAlert(ctx, downProto)
}

// handleTotalLoss processes a probe where both protocols are down.
// It tracks downtime and returns true when the connectivity timeout has been
// exceeded (meaning the caller should invoke handleTimeoutExceeded).
func (w *watchdog) handleTotalLoss(ctx context.Context) bool {
	log := w.tracedLogger(ctx)
	w.consecutiveTotalFails++
	now := w.now().Unix()
	if w.totalDownStartUnix == 0 {
		w.totalDownStartUnix = now
		log.InfoContext(ctx,
			"TOTAL connectivity loss (IPv4 and IPv6 both FAILED); starting timeout",
			"timeout_seconds", w.cfg.Watchdog.ConnectivityTimeoutSeconds,
			"fail_count", w.consecutiveTotalFails,
		)
	}
	w.lastState = stateDown
	downDuration := int(now - w.totalDownStartUnix)
	remaining := w.cfg.Watchdog.ConnectivityTimeoutSeconds - downDuration
	if downDuration < w.cfg.Watchdog.ConnectivityTimeoutSeconds {
		log.InfoContext(ctx,
			"Still down before timeout threshold",
			"elapsed_seconds", downDuration,
			"remaining_seconds", remaining,
			"fail_count", w.consecutiveTotalFails,
		)
		return false
	}
	log.InfoContext(ctx,
		"Timeout exceeded; entering diagnosis",
		"down_seconds", downDuration,
		"threshold_seconds", w.cfg.Watchdog.ConnectivityTimeoutSeconds,
	)
	return true
}

// sleepOrShutdown sleeps for the given duration, returning false if the
// context was cancelled (indicating the caller should return from run).
func (w *watchdog) sleepOrShutdown(ctx context.Context, d time.Duration) bool {
	log := w.tracedLogger(ctx)
	if !sleepOrDone(ctx, d) {
		log.InfoContext(ctx, "Context cancelled during sleep; watchdog shutting down")
		return false
	}
	return true
}

// runIteration executes one iteration of the watchdog loop.
// Returns false if the loop should exit (context cancelled).
func (w *watchdog) runIteration(ctx context.Context, iteration int) bool {
	iterCtx := tracing.WithOperation(ctx, "watchdog_iteration")
	iterCtx = tracing.WithAttempt(iterCtx, iteration)
	iterCtx, _ = tracing.StartTrace(iterCtx, "", "watchdog_iteration")
	log := w.tracedLogger(iterCtx)
	select {
	case <-ctx.Done():
		log.InfoContext(ctx, "Context cancelled; watchdog shutting down")
		return false
	default:
	}

	running, err := w.ops.VMStatus(iterCtx, w.cfg.MwanVMID)
	if err != nil {
		log.ErrorContext(ctx, "qm status error", "vmid", w.cfg.MwanVMID, "err", err)
		return w.sleepOrShutdown(iterCtx, w.cfg.Watchdog.DegradedInterval())
	}
	if !running {
		w.handleVMStopped(iterCtx)
		return w.sleepOrShutdown(iterCtx, w.cfg.Watchdog.DegradedInterval())
	}
	if w.vmStoppedLogged {
		log.DebugContext(ctx, "VM is running again", "vmid", w.cfg.MwanVMID)
	}
	w.vmStoppedLogged = false

	v4ok, v6ok := w.probeConnectivity(iterCtx)
	if !v4ok || !v6ok {
		w.consecutiveHealthy = 0
		w.healthyCyclesForHash = 0
	}

	switch {
	case v4ok && v6ok:
		w.handleHealthyProbe(iterCtx, iteration)
		w.maybeTriggerRecovery(iterCtx, w.cfg)
		return w.sleepOrShutdown(iterCtx, w.cfg.Watchdog.HealthyInterval())
	case v4ok || v6ok:
		w.handlePartialProbe(iterCtx, v6ok)
		return w.sleepOrShutdown(iterCtx, w.cfg.Watchdog.DegradedInterval())
	default:
		if w.handleTotalLoss(iterCtx) {
			w.handleTimeoutExceeded(iterCtx)
		}
		return w.sleepOrShutdown(iterCtx, w.cfg.Watchdog.DegradedInterval())
	}
}

func (w *watchdog) run(ctx context.Context) {
	log := w.tracedLogger(ctx)
	w.logStartupConfig(ctx)
	w.runStartupChecks(ctx)
	w.lastState = stateUnknown
	w.lastHeartbeat = w.now()
	iteration := 0

	// The gateway pushes its provider verdict here. A listener that cannot bind
	// leaves w.status reporting nothing received, which a diagnosis says out
	// loud; it never fabricates a verdict and never stops the connectivity loop.
	if w.cfg.Watchdog.StatusListenPort != 0 {
		statusListener := statuspush.NewListener(
			w.cfg.Watchdog.StatusListenPort,
			w.log.With("component", "statuspush"),
		)
		w.status = statusListener
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.ErrorContext(ctx, "status listener panic",
						"err", fmt.Errorf("panic: %v", recovered))
				}
			}()
			// Run loops until it fails; it has no success return, so its
			// error is always non-nil and always worth a log line.
			runErr := statusListener.Run(ctx)
			log.WarnContext(ctx, "status listener stopped", "err", runErr)
		}()
	}

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "interface monitor panic", "err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		w.runIfaceMonitor(ctx)
	}()

	for w.cfg.Watchdog.MaxIterations <= 0 ||
		iteration < w.cfg.Watchdog.MaxIterations {
		iteration++
		if !w.runIteration(ctx, iteration) {
			return
		}
	}
	log.InfoContext(ctx, "Reached max iterations; exiting", "max", w.cfg.Watchdog.MaxIterations)
}

// attemptRollbackForDeploy checks rollback eligibility, finds a snapshot, and
// triggers a rollback if appropriate. Returns true if a rollback was executed.
func (w *watchdog) attemptRollbackForDeploy(ctx context.Context, deployTS int64) bool {
	log := w.tracedLogger(ctx)
	log.InfoContext(ctx,
		"Step 2: checking rollback state",
		"deploy_ts", deployTS,
	)
	already, attempts, err := rollback.AlreadyDone(
		w.cfg.Watchdog.RollbackStateFile, deployTS,
	)
	if err != nil {
		log.ErrorContext(ctx,
			"read rollback state file (proceeding cautiously)",
			"path", w.cfg.Watchdog.RollbackStateFile,
			"err", err,
		)
	}
	if already {
		log.InfoContext(ctx,
			"Rollback already performed for this deploy_ts; "+
				"not rolling back again",
			"deploy_ts", deployTS,
		)
		log.InfoContext(ctx, "--- DIAGNOSIS END (rollback already done) ---")
		return false
	}
	if w.cfg.Watchdog.MaxRollbackAttempts > 0 && attempts >= w.cfg.Watchdog.MaxRollbackAttempts {
		log.ErrorContext(ctx,
			"Rollback attempt limit reached; manual intervention required",
			"deploy_ts", deployTS,
			"attempts", attempts,
			"max_attempts", w.cfg.Watchdog.MaxRollbackAttempts,
			"err", "rollback attempt budget exhausted",
		)
		log.InfoContext(ctx, "--- DIAGNOSIS END (rollback exhausted) ---")
		return false
	}

	log.InfoContext(ctx, "Step 3: finding rollback snapshot...")
	snap, snapErr := w.findSnapshot(ctx)
	if snapErr != nil {
		log.ErrorContext(ctx, "listsnapshot error", "err", snapErr)
	}
	if snap == "" {
		log.InfoContext(ctx, "No rollback snapshot found; cannot rollback")
		w.sendTotalAlert(
			ctx,
			"Config changed but no rollback snapshot exists",
			fmt.Sprintf(
				"A recent change (effective ts=%d) was "+
					"detected and connectivity is broken, "+
					"but no pre-deploy-* or known-good-* "+
					"snapshot was found.\n\n"+
					"Manual intervention required.",
				deployTS,
			),
		)
		log.InfoContext(ctx, "--- DIAGNOSIS END (no snapshot) ---")
		return false
	}

	log.InfoContext(ctx,
		"--- DIAGNOSIS END: triggering rollback ---",
		"vmid", w.cfg.MwanVMID,
		"snapshot", snap,
		"deploy_ts", deployTS,
	)
	rbCtx := tracing.WithRunID(context.Background(), w.runID)
	rbCtx = tracing.WithTraceID(rbCtx, tracing.TraceID(ctx))
	rbCtx = tracing.WithOperation(rbCtx, "rollback")
	rbCtx, _ = tracing.StartTrace(rbCtx, "", "rollback")
	w.rollback(rbCtx, deployTS, snap)
	return true
}
