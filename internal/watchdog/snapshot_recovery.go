package watchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/rollback"
)

// Recovery from a snapshot cycle that fails partway.
//
// Proxmox takes a configuration lock on the guest for the whole of a
// snapshot, a snapshot delete, or a rollback, and releases it in a final
// block that runs only when every step succeeded. A delete that the
// storage layer rejects skips that block, so the lock and the half-removed
// snapshot entry both survive the failure and every later operation on the
// guest is refused, including the deploy playbook's own snapshot.
//
// The snapshot cycle repeats on each probe, so without the pacing below a
// failure that cannot resolve itself is retried every few seconds for as
// long as it lasts.

const (
	// snapshotFailureBackoffBase is how long the watchdog waits after one
	// failed snapshot. Each further failure in a row doubles the wait, up
	// to snapshotFailureBackoffCap.
	snapshotFailureBackoffBase = 5 * time.Minute
	// snapshotFailureBackoffCap bounds the growing wait so the cycle still
	// recovers on its own within the hour once the cause clears.
	snapshotFailureBackoffCap = time.Hour
	// snapshotFailureAlertThreshold is how many failures in a row must
	// pass before the watchdog raises an alert. One failure is routine and
	// resolves itself; a run of them needs a person.
	snapshotFailureAlertThreshold = 3
	// maxConsecutiveForcedDeletes caps how many times in a row the
	// watchdog escalates a delete. Past that the guest is left to an
	// operator rather than forced repeatedly.
	maxConsecutiveForcedDeletes = 3

	alertKindSnapshotFailed = "snapshot-failed"
	alertKindGuestLocked    = "guest-locked"
	alertKindSnapshotForced = "snapshot-forced-delete"

	// storageSnapshotMissingMarker is what the storage layer prints when
	// the guest configuration lists a snapshot whose disk snapshot is
	// already gone. The MWAN guests live on ZFS, and this is the one
	// reason a delete fails with nothing left to remove.
	storageSnapshotMissingMarker = "could not find any snapshots to destroy"
)

// recoverableLocks are the guest locks the watchdog's own snapshot work
// takes. A lock outside this set was set by something else and is left
// alone. Rollback is excluded deliberately: a stranded rollback lock means
// a rollback stopped partway, and resuming that is not a cleanup decision
// the watchdog can make on its own.
var recoverableLocks = []string{"snapshot", "snapshot-delete"}

// snapshotBackoffActive reports whether a recent failure is still spacing
// out the next snapshot attempt.
func (w *watchdog) snapshotBackoffActive() bool {
	return !w.snapshotBackoffUntil.IsZero() &&
		w.now().Before(w.snapshotBackoffUntil)
}

// snapshotFailureBackoff returns the wait after the given number of
// failures in a row, doubling from the base and stopping at the cap.
func snapshotFailureBackoff(failures int) time.Duration {
	if failures <= 1 {
		return snapshotFailureBackoffBase
	}
	backoff := snapshotFailureBackoffBase
	for range failures - 1 {
		backoff *= 2
		if backoff >= snapshotFailureBackoffCap {
			return snapshotFailureBackoffCap
		}
	}
	return backoff
}

// noteSnapshotFailure records a failed snapshot, spaces the next attempt
// out, and alerts once the run of failures stops looking transient.
//
// Resetting the healthy count matters as much as the wait: the cycle only
// snapshots after a run of healthy probes, and leaving that count intact
// lets the next probe qualify immediately.
func (w *watchdog) noteSnapshotFailure(
	ctx context.Context, name string, cause error,
) {
	log := w.tracedLogger(ctx)
	w.consecutiveSnapshotFails++
	backoff := snapshotFailureBackoff(w.consecutiveSnapshotFails)
	w.snapshotBackoffUntil = w.now().Add(backoff)
	w.consecutiveHealthy = 0
	log.ErrorContext(ctx, "vmSnapshot failed",
		"err", cause,
		"snapshot", name,
		"consecutive_failures", w.consecutiveSnapshotFails,
		"next_attempt_after", backoff,
	)
	if w.consecutiveSnapshotFails < snapshotFailureAlertThreshold {
		return
	}
	w.notifierOrNull().Notify(ctx, notify.Event{
		Now:     w.now(),
		Level:   slog.LevelError,
		Kind:    alertKindSnapshotFailed,
		Key:     w.cfg.MwanVMID,
		Message: "known-good snapshots keep failing on this guest",
		Fields: []slog.Attr{
			slog.String("snapshot", name),
			slog.Int("consecutive_failures", w.consecutiveSnapshotFails),
			slog.String("err", cause.Error()),
		},
		IsRecovery: false,
	})
}

// noteSnapshotSuccess clears the failure state after a snapshot lands and
// closes any alert the failures raised.
func (w *watchdog) noteSnapshotSuccess(ctx context.Context) {
	if w.consecutiveSnapshotFails == 0 {
		return
	}
	w.consecutiveSnapshotFails = 0
	w.snapshotBackoffUntil = time.Time{}
	w.forcedDeletes = 0
	w.notifierOrNull().Resolve(
		ctx, alertKindSnapshotFailed, w.cfg.MwanVMID,
		"known-good snapshot succeeded",
	)
}

// clearStaleGuestLock releases a guest lock that outlived the operation
// that set it. It clears a lock only when Proxmox reports no task running
// against that guest, because clearing a lock whose operation is still
// running corrupts that operation.
func (w *watchdog) clearStaleGuestLock(ctx context.Context, phase string) {
	log := w.tracedLogger(ctx)
	lock, err := w.ops.VMLock(ctx, w.cfg.MwanVMID)
	if err != nil {
		log.WarnContext(ctx, "read guest lock failed",
			"phase", phase, "err", err)
		return
	}
	if lock == "" {
		return
	}
	if !slices.Contains(recoverableLocks, lock) {
		log.WarnContext(ctx,
			"guest is locked by an operation the watchdog does not own",
			"phase", phase, "lock", lock)
		return
	}
	running, err := w.ops.VMHasRunningTask(ctx, w.cfg.MwanVMID)
	if err != nil {
		log.WarnContext(ctx,
			"task liveness check failed; leaving the guest lock in place",
			"phase", phase, "lock", lock, "err", err)
		return
	}
	if running {
		log.InfoContext(ctx,
			"guest lock belongs to a running task; leaving it in place",
			"phase", phase, "lock", lock)
		return
	}
	if err := w.ops.VMUnlock(ctx, w.cfg.MwanVMID); err != nil {
		log.ErrorContext(ctx, "clearing the stale guest lock failed",
			"phase", phase, "lock", lock, "err", err)
		return
	}
	log.WarnContext(ctx, "cleared a stale guest lock",
		"phase", phase, "lock", lock)
	w.notifierOrNull().Notify(ctx, notify.Event{
		Now:     w.now(),
		Level:   slog.LevelWarn,
		Kind:    alertKindGuestLocked,
		Key:     w.cfg.MwanVMID,
		Message: "cleared a guest lock left behind by a failed snapshot operation",
		Fields: []slog.Attr{
			slog.String("lock", lock),
			slog.String("phase", phase),
		},
		IsRecovery: false,
	})
}

// deleteSnapshot removes one watchdog-owned snapshot. When the plain
// delete fails because the disk snapshot is already gone, it escalates to
// a forced delete, which is the only way to remove the leftover entry and
// release the lock Proxmox took for the delete.
func (w *watchdog) deleteSnapshot(ctx context.Context, name string) error {
	err := w.ops.VMDelSnapshot(ctx, w.cfg.MwanVMID, name)
	if err == nil {
		return nil
	}
	if !w.canForceDelete(ctx, name, err) {
		return fmt.Errorf("delete snapshot %s: %w", name, err)
	}
	log := w.tracedLogger(ctx)
	// Count the attempt before making it. Counting only successes would let
	// a forced delete that keeps failing retry without ever reaching the
	// limit, which is the case the limit exists for.
	w.forcedDeletes++
	if forceErr := w.ops.VMDelSnapshotForce(
		ctx, w.cfg.MwanVMID, name,
	); forceErr != nil {
		log.ErrorContext(ctx, "forced snapshot delete failed",
			"snapshot", name, "forced_deletes", w.forcedDeletes,
			"err", forceErr)
		return errors.Join(err, forceErr)
	}
	log.WarnContext(ctx,
		"removed a snapshot entry whose disk snapshot was already gone",
		"snapshot", name, "forced_deletes", w.forcedDeletes)
	w.notifierOrNull().Notify(ctx, notify.Event{
		Now:     w.now(),
		Level:   slog.LevelWarn,
		Kind:    alertKindSnapshotForced,
		Key:     w.cfg.MwanVMID,
		Message: "removed a snapshot entry whose disk snapshot was already gone",
		Fields: []slog.Attr{
			slog.String("snapshot", name),
			slog.Int("forced_deletes", w.forcedDeletes),
		},
		IsRecovery: false,
	})
	return nil
}

// canForceDelete reports whether escalating this delete is safe. Every
// condition must hold, and each one narrows what a forced delete can
// possibly destroy.
func (w *watchdog) canForceDelete(
	ctx context.Context, name string, cause error,
) bool {
	log := w.tracedLogger(ctx)
	// Only snapshots the watchdog itself creates. A pre-deploy snapshot
	// belongs to the deploy playbook and an operator-named one belongs to
	// a person; neither is the watchdog's to force.
	if !rollback.KnownGoodSnapRE.MatchString(name) {
		return false
	}
	// Only when the plain delete already failed for the one reason a
	// forced delete addresses. Any other failure is a live problem, and
	// forcing past it would destroy a snapshot that still exists.
	if !strings.Contains(cause.Error(), storageSnapshotMissingMarker) {
		return false
	}
	if w.forcedDeletes >= maxConsecutiveForcedDeletes {
		log.WarnContext(ctx,
			"forced-delete limit reached; leaving this guest to an operator",
			"snapshot", name, "forced_deletes", w.forcedDeletes)
		return false
	}
	running, err := w.ops.VMHasRunningTask(ctx, w.cfg.MwanVMID)
	if err != nil {
		log.WarnContext(ctx,
			"task liveness check failed; not escalating this delete",
			"snapshot", name, "err", err)
		return false
	}
	if running {
		log.InfoContext(ctx,
			"a task is running against this guest; not escalating this delete",
			"snapshot", name)
		return false
	}
	return true
}
