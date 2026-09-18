package watchdog

import (
	"context"
	"errors"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
)

// snapshotTestWatchdog returns a watchdog whose snapshot cycle fires on the
// first healthy probe, so a test drives one attempt per maybeSnapshot call.
func snapshotTestWatchdog(t *testing.T, mock *mockOps) *watchdog {
	t.Helper()
	w := newTestWatchdog(t, mock, func(cfg *config.Config) {
		cfg.Watchdog.SnapshotHealthyThreshold = 1
	})
	w.consecutiveHealthy = 1
	w.lastHashCheckOK = true
	return w
}

// TestFailedSnapshotSpacesOutTheNextAttempt covers the retry storm: the
// cycle qualifies again on the very next probe unless a failure records the
// attempt, so a failure that cannot resolve itself was retried every few
// seconds for as long as it lasted.
func TestFailedSnapshotSpacesOutTheNextAttempt(t *testing.T) {
	mock := &mockOps{vmSnapErr: errors.New("snapshot task aborted")}
	w := snapshotTestWatchdog(t, mock)
	now := time.Unix(1_700_000_000, 0)
	w.nowFn = func() time.Time { return now }

	w.maybeSnapshot(context.Background())

	if w.consecutiveSnapshotFails != 1 {
		t.Fatalf("consecutiveSnapshotFails = %d, want 1", w.consecutiveSnapshotFails)
	}
	if !w.snapshotBackoffActive() {
		t.Fatal("a failed snapshot must space out the next attempt")
	}
	if w.consecutiveHealthy != 0 {
		t.Fatalf("consecutiveHealthy = %d, want 0: a failure must not leave the"+
			" cycle qualified to retry immediately", w.consecutiveHealthy)
	}

	// The next probe must not attempt another snapshot while the wait holds.
	w.consecutiveHealthy = 1
	before := mock.vmSnapshotsCalls
	w.maybeSnapshot(context.Background())
	if mock.vmSnapshotsCalls != before {
		t.Fatal("the cycle attempted another snapshot during the wait")
	}
}

// TestSnapshotBackoffGrowsThenStops covers the wait after repeated failures.
func TestSnapshotBackoffGrowsThenStops(t *testing.T) {
	t.Parallel()

	if got := snapshotFailureBackoff(1); got != snapshotFailureBackoffBase {
		t.Fatalf("first backoff = %s, want %s", got, snapshotFailureBackoffBase)
	}
	if got := snapshotFailureBackoff(2); got != 2*snapshotFailureBackoffBase {
		t.Fatalf("second backoff = %s, want %s", got, 2*snapshotFailureBackoffBase)
	}
	if got := snapshotFailureBackoff(20); got != snapshotFailureBackoffCap {
		t.Fatalf("backoff after many failures = %s, want the %s cap",
			got, snapshotFailureBackoffCap)
	}
}

// TestSuccessClearsTheFailureState covers recovery: once a snapshot lands,
// the cycle must return to its normal cadence.
func TestSuccessClearsTheFailureState(t *testing.T) {
	mock := &mockOps{}
	w := snapshotTestWatchdog(t, mock)
	now := time.Unix(1_700_000_000, 0)
	w.nowFn = func() time.Time { return now }
	w.consecutiveSnapshotFails = 2
	// An active wait, so clearing it is observable rather than assumed.
	w.snapshotBackoffUntil = now.Add(10 * time.Minute)
	w.forcedDeletes = 1

	w.noteSnapshotSuccess(context.Background())

	if w.consecutiveSnapshotFails != 0 {
		t.Fatalf("consecutiveSnapshotFails = %d, want 0", w.consecutiveSnapshotFails)
	}
	if !w.snapshotBackoffUntil.IsZero() {
		t.Fatalf("snapshotBackoffUntil = %s, want the zero time",
			w.snapshotBackoffUntil)
	}
	if w.snapshotBackoffActive() {
		t.Fatal("a successful snapshot must clear the wait")
	}
	if w.forcedDeletes != 0 {
		t.Fatalf("forcedDeletes = %d, want 0", w.forcedDeletes)
	}
}

// TestAlertFiresOnlyAfterRepeatedFailures covers the alert threshold. One
// failure is routine; a run of them is what needs a person.
func TestAlertFiresOnlyAfterRepeatedFailures(t *testing.T) {
	mock := &mockOps{vmSnapErr: errors.New("snapshot task aborted")}
	w := snapshotTestWatchdog(t, mock)
	now := time.Unix(1_700_000_000, 0)
	w.nowFn = func() time.Time { return now }
	ctx := context.Background()
	fn := fakeNotifierFrom(t, w)

	for i := 1; i < snapshotFailureAlertThreshold; i++ {
		w.noteSnapshotFailure(ctx, "known-good-x", errors.New("boom"))
		if fn.Active(alertKindSnapshotFailed, w.cfg.MwanVMID) {
			t.Fatalf("alert fired after %d failures, before the threshold of %d",
				i, snapshotFailureAlertThreshold)
		}
	}

	w.noteSnapshotFailure(ctx, "known-good-x", errors.New("boom"))

	if !fn.Active(alertKindSnapshotFailed, w.cfg.MwanVMID) {
		t.Fatalf("no alert after %d failures in a row",
			snapshotFailureAlertThreshold)
	}
}

// TestStaleGuestLockIsClearedOnlyWhenNoTaskRuns covers the guard on the one
// step that can damage a live operation.
func TestStaleGuestLockIsClearedOnlyWhenNoTaskRuns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		lock        string
		taskRunning bool
		taskErr     error
		wantUnlock  int
	}{
		{
			name: "no lock leaves the guest alone", lock: "",
			taskRunning: false, taskErr: nil, wantUnlock: 0,
		},
		{
			name: "a stale snapshot-delete lock is cleared", lock: "snapshot-delete",
			taskRunning: false, taskErr: nil, wantUnlock: 1,
		},
		{
			name: "a lock with a running task is left in place", lock: "snapshot-delete",
			taskRunning: true, taskErr: nil, wantUnlock: 0,
		},
		{
			name: "a lock the watchdog does not own is left in place", lock: "backup",
			taskRunning: false, taskErr: nil, wantUnlock: 0,
		},
		{
			name: "an unreadable task list leaves the lock in place", lock: "snapshot",
			taskRunning: false, taskErr: errors.New("pvesh unavailable"), wantUnlock: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockOps{
				guestLock:      tc.lock,
				taskRunning:    tc.taskRunning,
				taskRunningErr: tc.taskErr,
			}
			w := newTestWatchdog(t, mock)

			w.clearStaleGuestLock(context.Background(), "test")

			if mock.unlockCalls != tc.wantUnlock {
				t.Fatalf("unlockCalls = %d, want %d", mock.unlockCalls, tc.wantUnlock)
			}
		})
	}
}

// TestForcedDeleteRequiresTheStorageReason covers the escalation guard: a
// forced delete removes the snapshot entry regardless of what the storage
// layer holds, so it runs only for the one failure it addresses.
func TestForcedDeleteRequiresTheStorageReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		snapshot   string
		deleteErr  error
		running    bool
		wantForced int
	}{
		{
			name:     "the disk snapshot is already gone",
			snapshot: "known-good-20260809-120000",
			deleteErr: errors.New(
				"qm delsnapshot: exit status 255: " + storageSnapshotMissingMarker,
			),
			running: false, wantForced: 1,
		},
		{
			name:      "any other delete failure is left alone",
			snapshot:  "known-good-20260809-120000",
			deleteErr: errors.New("qm delsnapshot: exit status 255: storage is offline"),
			running:   false, wantForced: 0,
		},
		{
			name:     "a deploy snapshot is never forced",
			snapshot: "pre-deploy-20260809T120000",
			deleteErr: errors.New(
				"qm delsnapshot: exit status 255: " + storageSnapshotMissingMarker,
			),
			running: false, wantForced: 0,
		},
		{
			name:     "a running task blocks the escalation",
			snapshot: "known-good-20260809-120000",
			deleteErr: errors.New(
				"qm delsnapshot: exit status 255: " + storageSnapshotMissingMarker,
			),
			running: true, wantForced: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockOps{
				delSnapshotErr: tc.deleteErr,
				taskRunning:    tc.running,
			}
			w := newTestWatchdog(t, mock)

			err := w.deleteSnapshot(context.Background(), tc.snapshot)

			if len(mock.forceDelSnapshotCalls) != tc.wantForced {
				t.Fatalf("forced deletes = %d, want %d",
					len(mock.forceDelSnapshotCalls), tc.wantForced)
			}
			if tc.wantForced == 0 && err == nil {
				t.Fatal("a delete that was not escalated must report its failure")
			}
			if tc.wantForced > 0 && err != nil {
				t.Fatalf("a successful escalation must not report an error: %v", err)
			}
		})
	}
}

// TestForcedDeletesStopAfterTheLimit covers the cap that hands a guest to an
// operator rather than forcing it repeatedly.
func TestForcedDeletesStopAfterTheLimit(t *testing.T) {
	t.Parallel()

	mock := &mockOps{
		delSnapshotErr: errors.New(
			"qm delsnapshot: exit status 255: " + storageSnapshotMissingMarker,
		),
		// Every forced delete fails, which is the case that must still
		// consume the limit rather than retrying without bound.
		forceDelSnapshotErr: errors.New("qm delsnapshot --force: exit status 255"),
	}
	w := newTestWatchdog(t, mock)
	ctx := context.Background()

	for i := range maxConsecutiveForcedDeletes {
		if err := w.deleteSnapshot(ctx, "known-good-20260809-120000"); err == nil {
			t.Fatalf("attempt %d: a failed forced delete must report its failure", i+1)
		}
	}
	if len(mock.forceDelSnapshotCalls) != maxConsecutiveForcedDeletes {
		t.Fatalf("forced deletes = %d, want %d",
			len(mock.forceDelSnapshotCalls), maxConsecutiveForcedDeletes)
	}

	if err := w.deleteSnapshot(ctx, "known-good-20260809-120000"); err == nil {
		t.Fatal("past the limit the delete must report its failure")
	}
	if len(mock.forceDelSnapshotCalls) != maxConsecutiveForcedDeletes {
		t.Fatalf("forced deletes = %d, want the limit to stop further attempts at %d",
			len(mock.forceDelSnapshotCalls), maxConsecutiveForcedDeletes)
	}
}

// TestPruneContinuesPastOneFailingSnapshot covers the rotation: one entry
// that refuses to go must not hold back the others in the same pass.
func TestPruneContinuesPastOneFailingSnapshot(t *testing.T) {
	mock := &mockOps{
		snapshotsOut: []byte(
			"known-good-20260809-100000\n" +
				"known-good-20260809-110000\n" +
				"known-good-20260809-120000\n" +
				"known-good-20260809-130000\n",
		),
		delSnapshotErr: errors.New("qm delsnapshot: exit status 255: storage is offline"),
	}
	w := newTestWatchdog(t, mock, func(cfg *config.Config) {
		cfg.Watchdog.MaxKnownGoodSnapshots = 1
		cfg.Watchdog.MaxTotalSnapshots = 0
	})

	if err := w.pruneSnapshots(context.Background()); err == nil {
		t.Fatal("prune must report the failure it saw")
	}
	if len(mock.delSnapshotCalls) != 3 {
		t.Fatalf("delete attempts = %d, want 3: the pass must continue past"+
			" the first failure", len(mock.delSnapshotCalls))
	}
}
