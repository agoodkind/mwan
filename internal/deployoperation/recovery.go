package deployoperation

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/rollback"
)

// Recovery acquires the hypervisor coordinator before short record transactions.
// Each poll releases the record lock before waiting because lease release needs
// that record lock but never needs the hypervisor coordinator.
func (store Store) WaitForLease(ctx context.Context, operationID, generation string) error {
	for {
		record, err := store.Read(ctx)
		if err != nil {
			return err
		}
		if record.OperationID != operationID || record.Generation != generation || record.Status != Recovering {
			return fmt.Errorf("deploy recovery operation identity or state has changed")
		}
		if record.Lease == nil || !record.Lease.ExpiresAt.After(time.Now()) {
			return nil
		}
		delay := min(store.PollInterval, time.Until(record.Lease.ExpiresAt))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Restore requires the caller to own the shared rollback coordinator and to
// drain the mutation lease before stopping the guest.
func Restore(ctx context.Context, operations *ops.RealOps, record Record) error {
	if record.Status != Recovering || (record.Lease != nil && record.Lease.ExpiresAt.After(time.Now())) {
		return fmt.Errorf("snapshot restoration requires recovery state and an expired or released mutation lease")
	}
	snapshots, err := operations.VMSnapshots(ctx, record.VMID)
	if err != nil {
		return fmt.Errorf("verify deploy recovery snapshot: %w", err)
	}
	if !snapshotExists(snapshots, record.Snapshot) {
		return fmt.Errorf("exact deploy recovery snapshot %s is absent", record.Snapshot)
	}
	if err := operations.VMStop(ctx, record.VMID); err != nil {
		slog.WarnContext(ctx, "deploy recovery VM stop failed")
		return fmt.Errorf("stop VM for deploy recovery: %w", err)
	}
	running, err := operations.VMStatus(ctx, record.VMID)
	if err != nil {
		return fmt.Errorf("verify stopped deploy recovery VM: %w", err)
	}
	if running {
		return fmt.Errorf("deploy recovery VM remains running after stop")
	}
	for _, snapshot := range slices.Backward(rollback.SnapshotsAfter(snapshots, record.Snapshot)) {
		if err := operations.VMDelSnapshot(ctx, record.VMID, snapshot); err != nil {
			return fmt.Errorf("remove deploy recovery snapshot descendant %s: %w", snapshot, err)
		}
	}
	if err := operations.VMRollback(ctx, record.VMID, record.Snapshot); err != nil {
		slog.WarnContext(ctx, "deploy recovery snapshot restore failed")
		return fmt.Errorf("restore deploy recovery snapshot: %w", err)
	}
	if err := operations.VMStart(ctx, record.VMID); err != nil {
		slog.WarnContext(ctx, "deploy recovery VM start failed")
		return fmt.Errorf("start restored deploy recovery VM: %w", err)
	}
	return nil
}

func snapshotExists(output []byte, name string) bool {
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(strings.TrimLeft(line, " `->|"))
		if len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}
