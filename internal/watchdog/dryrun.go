package watchdog

import (
	"context"
	"fmt"
	"log/slog"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/ops"
)

// dryRunOps wraps an ops.SysOps and logs destructive operations without executing them.
type dryRunOps struct {
	inner ops.SysOps
	log   *slog.Logger
}

func (d *dryRunOps) VMStatus(ctx context.Context, vmid string) (bool, error) {
	running, err := d.inner.VMStatus(ctx, vmid)
	if err != nil {
		d.log.WarnContext(ctx, "read vm status failed", "vmid", vmid, "err", err)
		return running, fmt.Errorf("read vm status: %w", err)
	}
	return running, nil
}

func (d *dryRunOps) VMStop(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would stop VM", "vmid", vmid)
	return nil
}

func (d *dryRunOps) VMRollback(ctx context.Context, vmid, snap string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would rollback VM", "vmid", vmid, "snapshot", snap)
	return nil
}

func (d *dryRunOps) VMStart(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would start VM", "vmid", vmid)
	return nil
}

func (d *dryRunOps) VMSnapshots(ctx context.Context, vmid string) ([]byte, error) {
	out, err := d.inner.VMSnapshots(ctx, vmid)
	if err != nil {
		d.log.WarnContext(ctx, "list snapshots failed", "vmid", vmid, "err", err)
		return out, fmt.Errorf("list snapshots: %w", err)
	}
	return out, nil
}

func (d *dryRunOps) VMSnapshot(ctx context.Context, vmid, snapName string) error {
	d.log.InfoContext(
		ctx,
		"[DRY-RUN] would snapshot VM",
		"vmid", vmid,
		"snapshot", snapName,
	)
	return nil
}

func (d *dryRunOps) VMDelSnapshot(ctx context.Context, vmid, snapName string) error {
	d.log.InfoContext(
		ctx,
		"[DRY-RUN] would delete snapshot",
		"vmid", vmid,
		"snapshot", snapName,
	)
	return nil
}

func (d *dryRunOps) VMDelSnapshotForce(
	ctx context.Context, vmid, snapName string,
) error {
	d.log.InfoContext(
		ctx,
		"[DRY-RUN] would force-delete snapshot",
		"vmid", vmid,
		"snapshot", snapName,
	)
	return nil
}

// VMLock reads state and changes nothing, so it passes through.
func (d *dryRunOps) VMLock(ctx context.Context, vmid string) (string, error) {
	lock, err := d.inner.VMLock(ctx, vmid)
	if err != nil {
		d.log.WarnContext(ctx, "read guest lock failed", "vmid", vmid, "err", err)
		return "", fmt.Errorf("read guest lock: %w", err)
	}
	return lock, nil
}

func (d *dryRunOps) VMUnlock(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would clear the guest lock", "vmid", vmid)
	return nil
}

// VMHasRunningTask reads state and changes nothing, so it passes through.
func (d *dryRunOps) VMHasRunningTask(
	ctx context.Context, vmid string,
) (bool, error) {
	running, err := d.inner.VMHasRunningTask(ctx, vmid)
	if err != nil {
		d.log.WarnContext(ctx, "task liveness check failed", "vmid", vmid, "err", err)
		return false, fmt.Errorf("task liveness: %w", err)
	}
	return running, nil
}

func (d *dryRunOps) GuestExec(
	ctx context.Context, vmid string, args ...string,
) (ops.GuestExecResult, error) {
	// The error path returns the inner result unchanged, so it keeps the
	// exit code the wrapped ops reported.
	result, err := d.inner.GuestExec(ctx, vmid, args...)
	if err != nil {
		d.log.WarnContext(ctx, "guest exec failed", "vmid", vmid, "err", err)
		return result, fmt.Errorf("guest exec: %w", err)
	}
	return result, nil
}

func (d *dryRunOps) Ping(ctx context.Context, bin, target string) bool {
	return d.inner.Ping(ctx, bin, target)
}

func (d *dryRunOps) GetConfigState(
	ctx context.Context, vmid string,
) (*mwanv1.GetConfigStateResponse, string, error) {
	state, channel, err := d.inner.GetConfigState(ctx, vmid)
	if err != nil {
		d.log.WarnContext(ctx, "get config state failed", "vmid", vmid, "err", err)
		return state, channel, fmt.Errorf("get config state: %w", err)
	}
	return state, channel, nil
}

func (d *dryRunOps) GetBGPStatus(
	ctx context.Context, vmid string,
) (*mwanv1.GetBGPStatusResponse, error) {
	d.log.InfoContext(ctx, "[DRY-RUN] would get BGP status", "vmid", vmid)
	return &mwanv1.GetBGPStatusResponse{}, nil
}

func (d *dryRunOps) AnnounceRoutes(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would announce BGP routes", "vmid", vmid)
	return nil
}

func (d *dryRunOps) WithdrawRoutes(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would withdraw BGP routes", "vmid", vmid)
	return nil
}

func (d *dryRunOps) VMFSFreezeStatus(ctx context.Context, vmid string) (string, error) {
	status, err := d.inner.VMFSFreezeStatus(ctx, vmid)
	if err != nil {
		d.log.WarnContext(ctx, "fsfreeze-status query failed", "vmid", vmid, "err", err)
		return "", fmt.Errorf("fsfreeze-status: %w", err)
	}
	return status, nil
}

func (d *dryRunOps) VMFSFreezeThaw(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would thaw guest filesystems", "vmid", vmid)
	return nil
}
