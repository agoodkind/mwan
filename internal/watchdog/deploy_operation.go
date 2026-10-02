package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/deployoperation"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/rollback"
)

func (w *watchdog) handleDeployOperation(ctx context.Context) bool {
	_, err := os.Stat(w.cfg.Watchdog.RollbackLockFile + ".operation")
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	log := w.tracedLogger(ctx)
	if err != nil {
		log.ErrorContext(ctx, "deployment operation inspection failed", "err", err)
		return true
	}
	coordinator, err := rollback.Acquire(ctx, w.cfg.Watchdog.RollbackLockFile, w.cfg.Watchdog.DegradedInterval())
	if err != nil {
		log.ErrorContext(ctx, "deployment operation coordination failed", "err", err)
		return true
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			log.ErrorContext(ctx, "deployment operation coordination release failed", "err", err)
		}
	}()
	return w.inspectDeployOperation(ctx, coordinator)
}

func (w *watchdog) inspectDeployOperation(ctx context.Context, coordinator *rollback.Coordinator) bool {
	log := w.tracedLogger(ctx)
	store := deployoperation.Store{Path: w.cfg.Watchdog.RollbackLockFile + ".operation", PollInterval: w.cfg.Watchdog.DegradedInterval(), Clock: clock.Real{}}
	record, err := store.Read(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		log.ErrorContext(ctx, "deployment operation read failed", "err", err)
		return true
	}
	if record.Status == deployoperation.Committed || record.Status == deployoperation.Recovered {
		return false
	}
	if record.VMID != w.cfg.MwanVMID {
		log.ErrorContext(ctx, "deployment operation VMID conflicts with watchdog configuration", "err", fmt.Errorf("operation VMID %s differs from configured VMID %s", record.VMID, w.cfg.MwanVMID))
		return true
	}
	watchContext, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.Watchdog.ConnectivityTimeoutSeconds)*time.Second)
	watchError := record.Watch.Verify(watchContext)
	cancel()
	if record.MutationReady(w.now()) && watchError == nil {
		return true
	}
	operations, ok := w.ops.(*ops.RealOps)
	if !ok {
		log.ErrorContext(ctx, "deployment recovery requires the production hypervisor operations", "err", fmt.Errorf("watchdog operations are not the configured RealOps runtime"))
		return true
	}
	engine := deployoperation.Engine{Store: store, RollbackLock: w.cfg.Watchdog.RollbackLockFile, Operations: operations, Log: log, Notify: w.notify, RuntimePath: ""}
	w.coord.SetRollingBack(true)
	defer w.coord.SetRollingBack(false)
	if err := engine.RecoverCoordinated(ctx, record.OperationID, record.Generation, "exact deployment watch is absent, expired or not healthy", coordinator); err != nil {
		log.ErrorContext(ctx, "exact deployment recovery failed", "err", err)
	}
	return true
}
