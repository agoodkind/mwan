// Package deployoperation fences mutations and restores exact deployment recovery pairs.
package deployoperation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/observation"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/rollback"
)

// Engine composes actual observations and VM operations under the recovery coordinator.
type Engine struct {
	Store        Store
	RollbackLock string
	Operations   *ops.RealOps
	Log          *slog.Logger
	Notify       notify.Notifier
	RuntimePath  string
}

// Arm verifies the baseline and registers its autonomous watch before allowing mutations.
func (engine Engine) Arm(ctx context.Context, manifest Manifest) (resultErr error) {
	defer func() {
		if resultErr != nil {
			engine.Log.WarnContext(ctx, "deployment operation arm failed")
		}
	}()
	if err := manifest.validate(); err != nil {
		return err
	}
	coordinator, err := rollback.Acquire(ctx, engine.RollbackLock, engine.Store.PollInterval)
	if err != nil {
		return fmt.Errorf("coordinate deployment recovery: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, coordinator.Close()) }()
	identity, err := ReadIdentity(ctx, engine.Operations, manifest.VMID, manifest.Paths)
	if err != nil {
		return err
	}
	if identity != manifest.Baseline {
		return fmt.Errorf("actual deployment baseline identity differs from the manifest")
	}
	snapshots, err := engine.Operations.VMSnapshots(ctx, manifest.VMID)
	if err != nil {
		return fmt.Errorf("read deployment baseline snapshots: %w", err)
	}
	if !snapshotExists(snapshots, manifest.Snapshot) {
		return fmt.Errorf("exact deployment baseline snapshot is absent")
	}
	results := engine.check(ctx, manifest, manifest.RequiredChecks)
	if !checksPassed(manifest.RequiredChecks, results, engine.Store.Clock.Now()) {
		return fmt.Errorf("deployment baseline application checks did not pass")
	}
	record := Record{Manifest: manifest, Status: Armed, Results: results, ObservedAt: engine.Store.Clock.Now().UTC(), Lease: nil, Watch: WatchIdentity{Unit: "", InvocationID: "", PID: 0}, Reason: "", UpdatedAt: time.Time{}, RestoredIdentity: nil}
	if err := engine.Store.Create(ctx, record); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), engine.Store.PollInterval)
			defer cancel()
			resultErr = errors.Join(resultErr, engine.Store.BeginRecovery(persistContext, manifest.OperationID, manifest.Generation, "deploy watch setup failed"))
			engine.transition(persistContext, record, "deploy_operation_watch_failed", "Deployment watch setup failed", resultErr.Error())
		}
	}()
	if err := StartWatch(ctx, record, engine.RuntimePath, engine.Store.Clock); err != nil {
		return err
	}
	for {
		registered, err := engine.Store.Read(ctx)
		if err != nil {
			return err
		}
		if registered.Status != Armed {
			return fmt.Errorf("deploy watch setup no longer permits mutations")
		}
		if registered.MutationReady(engine.Store.Clock.Now()) && registered.Watch.Verify(ctx) == nil {
			return nil
		}
		if err := wait(ctx, engine.Store.PollInterval); err != nil {
			return fmt.Errorf("wait for exact deploy watch registration: %w", err)
		}
	}
}

func (engine Engine) check(ctx context.Context, manifest Manifest, checks []observation.CheckSpec) []observation.Result {
	runtime := manifest.Observation
	runtime.GuestOps = engine.Operations
	executor := observation.NewExecutor(runtime, engine.Log)
	results := make([]observation.Result, 0, len(checks))
	for _, check := range checks {
		checkContext, cancel := context.WithTimeout(ctx, time.Duration(manifest.ObservationTimeoutSeconds)*time.Second)
		results = append(results, executor.Run(checkContext, check))
		cancel()
	}
	return results
}

// Watch persists application observations independently of the deployment controller.
func (engine Engine) Watch(ctx context.Context, operationID, generation string) error {
	record, err := engine.exact(ctx, operationID, generation)
	if err != nil {
		return err
	}
	if err := engine.Store.RegisterWatch(ctx, operationID, generation, record.WatchUnit); err != nil {
		return err
	}
	failures := 0
	for {
		record, err = engine.exact(ctx, operationID, generation)
		if err != nil {
			return err
		}
		if record.Status == Committed || record.Status == Recovered {
			return nil
		}
		if record.Status != Armed || !record.Deadline.After(engine.Store.Clock.Now()) {
			return engine.Recover(ctx, operationID, generation, "operation deadline or recovery state requires snapshot restoration")
		}
		results := engine.check(ctx, record.Manifest, record.RequiredChecks)
		status, err := engine.Store.observe(ctx, operationID, generation, results)
		if err != nil {
			return err
		}
		if status == Committed || status == Recovered {
			return nil
		}
		now := engine.Store.Clock.Now()
		if !checksPassed(record.RequiredChecks, results, now) {
			engine.Notify.Notify(ctx, notify.Event{Now: now.UTC(), Level: slog.LevelWarn, Kind: "deploy_operation_observation_failed", Key: operationID + "/" + generation, Message: "Deployment application observations did not pass", Fields: []slog.Attr{slog.Any("results", results)}, IsRecovery: false})
		}
		current, err := engine.exact(ctx, operationID, generation)
		if err != nil {
			return err
		}
		if current.Status == Committed || current.Status == Recovered {
			return nil
		}
		if watchObservationsAllowed(record, current, results, engine.Store.Clock.Now()) {
			failures = 0
		} else {
			failures++
		}
		if failures >= record.FailureThreshold {
			return engine.Recover(ctx, operationID, generation, "required application observations failed")
		}
		if err := wait(ctx, time.Duration(record.PollSeconds)*time.Second); err != nil {
			return err
		}
	}
}

func watchObservationsAllowed(previous, current Record, results []observation.Result, now time.Time) bool {
	if current.Status != Armed || !current.Deadline.After(now) || len(results) != len(current.RequiredChecks) {
		return false
	}
	exemptIDs := expectedInterruptionIDs(previous, current, now)
	for index, check := range current.RequiredChecks {
		result := results[index]
		if observation.RequiredPassed(check, result, now) {
			continue
		}
		if !slices.Contains(exemptIDs, check.ID) || !interruptedObservationFresh(check, result, current.Lease.StartedAt, now) {
			return false
		}
	}
	return true
}

func expectedInterruptionIDs(previous, current Record, now time.Time) []string {
	lease := current.Lease
	if lease != nil && previous.Lease != nil && lease.ID == previous.Lease.ID && lease.Phase == previous.Lease.Phase &&
		!lease.StartedAt.IsZero() && !lease.StartedAt.After(now) && lease.ExpiresAt.After(now) {
		for _, interruption := range current.ExpectedInterruptions {
			if interruption.Phase == lease.Phase && now.Before(lease.StartedAt.Add(time.Duration(interruption.MaxSeconds)*time.Second)) {
				return interruption.CheckIDs
			}
		}
	}
	return nil
}

func interruptedObservationFresh(check observation.CheckSpec, result observation.Result, startedAt, now time.Time) bool {
	if check.Dimension != observation.DimensionInboundApplication ||
		result.CheckID != check.ID || result.Dimension != check.Dimension || result.Operation != check.Operation ||
		result.Target != check.Target || result.PublicIPPolicy != check.PublicIPPolicy || result.Family != check.Family || result.Observer != check.Observer ||
		result.Availability != observation.AvailabilityComplete || result.Outcome != observation.OutcomeFail ||
		result.ObservedAt.IsZero() || result.ObservedAt.Before(startedAt) || result.ObservedAt.After(now) {
		return false
	}
	return now.Sub(result.ObservedAt) <= time.Duration(check.MaxAgeSeconds)*time.Second
}

// Commit repeats target identity and application checks before ending recovery protection.
func (engine Engine) Commit(ctx context.Context, operationID, generation string) (resultErr error) {
	coordinator, err := rollback.Acquire(ctx, engine.RollbackLock, engine.Store.PollInterval)
	if err != nil {
		engine.Log.WarnContext(ctx, "deployment commit coordination failed")
		return fmt.Errorf("coordinate deployment commit: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, coordinator.Close()) }()
	return engine.Store.Commit(ctx, operationID, generation, func(record Record) ([]observation.Result, error) {
		identity, err := ReadIdentity(ctx, engine.Operations, record.VMID, record.Paths)
		if err != nil {
			return nil, err
		}
		if identity.MachineID != record.Target.MachineID || identity.ExecutableSHA256 != record.Target.ExecutableSHA256 || identity.NetworkSHA256 != record.Target.NetworkSHA256 || identity.RuntimeSHA256 != record.Target.RuntimeSHA256 {
			return nil, fmt.Errorf("actual deployment target identity differs from the manifest")
		}
		return engine.check(ctx, record.Manifest, record.RequiredChecks), nil
	})
}

// Recover owns the hypervisor coordinator throughout mutation drain and restoration.
func (engine Engine) Recover(ctx context.Context, operationID, generation, reason string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			engine.Log.WarnContext(ctx, "deployment recovery coordination failed")
		}
	}()
	coordinator, err := rollback.Acquire(ctx, engine.RollbackLock, engine.Store.PollInterval)
	if err != nil {
		return fmt.Errorf("coordinate deployment recovery: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, coordinator.Close()) }()
	return engine.RecoverCoordinated(ctx, operationID, generation, reason, coordinator)
}

// RecoverCoordinated requires the matching coordinator already owned by the watchdog.
func (engine Engine) RecoverCoordinated(ctx context.Context, operationID, generation, reason string, coordinator *rollback.Coordinator) (resultErr error) {
	if !coordinator.Owns(engine.RollbackLock) {
		return fmt.Errorf("deployment recovery requires the hypervisor coordinator")
	}
	record, err := engine.exact(ctx, operationID, generation)
	if err != nil {
		return err
	}
	if record.Status == Recovered {
		return nil
	}
	recoveryContext, cancel := context.WithTimeout(ctx, time.Duration(record.RecoveryTimeoutSeconds)*time.Second)
	defer cancel()
	if err := engine.Store.BeginRecovery(recoveryContext, operationID, generation, reason); err != nil {
		return err
	}
	if record.Status == Armed {
		watchContext, watchCancel := context.WithTimeout(recoveryContext, time.Duration(record.ObservationTimeoutSeconds)*time.Second)
		watchError := record.Watch.Verify(watchContext)
		watchCancel()
		if watchError != nil {
			engine.transition(recoveryContext, record, "deploy_operation_watch_failed", "Deployment watch identity could not be verified", watchError.Error())
		}
	}
	engine.transition(recoveryContext, record, "deploy_operation_recovery", "Deployment snapshot recovery started", reason)
	defer func() {
		if resultErr != nil {
			engine.Log.WarnContext(ctx, "deployment snapshot recovery failed")
			persistContext, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), engine.Store.PollInterval)
			defer persistCancel()
			resultErr = errors.Join(resultErr, engine.Store.finishRecovery(persistContext, operationID, generation, nil, resultErr.Error()))
			engine.transition(persistContext, record, "deploy_operation_recovery_failed", "Deployment snapshot recovery failed", resultErr.Error())
		}
	}()
	if err := engine.Store.WaitForLease(recoveryContext, operationID, generation); err != nil {
		return err
	}
	record, err = engine.exact(recoveryContext, operationID, generation)
	if err != nil {
		return err
	}
	if err := Restore(recoveryContext, engine.Operations, record, engine.Store.Clock.Now()); err != nil {
		return err
	}
	identity, err := engine.awaitRestored(recoveryContext, record)
	if err != nil {
		return err
	}
	if err := engine.Store.finishRecovery(recoveryContext, operationID, generation, &identity, "baseline identity and application responses restored"); err != nil {
		return err
	}
	engine.Notify.Resolve(recoveryContext, "deploy_operation_observation_failed", operationID+"/"+generation, "Deployment baseline application responses restored")
	for _, kind := range []string{"deploy_operation_watch_failed", "deploy_operation_recovery", "deploy_operation_recovery_failed"} {
		engine.Notify.Resolve(recoveryContext, kind, operationID+"/"+generation, "Deployment baseline identity and application responses restored")
	}
	return nil
}

func (engine Engine) awaitRestored(ctx context.Context, record Record) (Identity, error) {
	for {
		identity, identityErr := ReadIdentity(ctx, engine.Operations, record.VMID, record.Paths)
		if identityErr == nil && identity.verifyRestored(record.Baseline) == nil {
			results := engine.check(ctx, record.Manifest, record.RestoredRequiredChecks)
			status, err := engine.Store.observe(ctx, record.OperationID, record.Generation, results)
			if err != nil {
				return Identity{}, err
			}
			if status != Armed && status != Recovering {
				return Identity{}, fmt.Errorf("terminal deploy operation rejects restoration observations")
			}
			if checksPassed(record.RestoredRequiredChecks, results, engine.Store.Clock.Now()) {
				return identity, nil
			}
		}
		if err := wait(ctx, time.Duration(record.PollSeconds)*time.Second); err != nil {
			engine.Log.WarnContext(ctx, "restored deployment application wait interrupted")
			return Identity{}, fmt.Errorf("wait for restored deployment identity and applications: %w", err)
		}
	}
}

func (engine Engine) transition(ctx context.Context, record Record, kind, message, reason string) {
	alertContext, cancel := context.WithTimeout(ctx, time.Duration(record.ObservationTimeoutSeconds)*time.Second)
	defer cancel()
	engine.Notify.Notify(alertContext, notify.Event{
		Now:        engine.Store.Clock.Now().UTC(),
		Level:      slog.LevelWarn,
		Kind:       kind,
		Key:        record.OperationID + "/" + record.Generation,
		Message:    message,
		Fields:     []slog.Attr{slog.String("vmid", record.VMID), slog.String("snapshot", record.Snapshot), slog.String("reason", reason)},
		IsRecovery: false,
	})
}

func (engine Engine) exact(ctx context.Context, operationID, generation string) (Record, error) {
	record, err := engine.Store.Read(ctx)
	if err != nil {
		return Record{}, err
	}
	if record.OperationID != operationID || record.Generation != generation {
		return Record{}, fmt.Errorf("deployment operation identity differs from the request")
	}
	return record, nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		slog.WarnContext(ctx, "deployment operation wait interrupted")
		return fmt.Errorf("deployment operation wait interrupted: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
