package ops

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/config"
)

const (
	qmBinary  = "qm"
	pctBinary = "pct"

	qmStopTimeoutSeconds = "30"
)

type guestDriver interface {
	guestLifecycleArgs
	guestSnapshotArgs
	guestLockArgs
	guestExecRunner
}

type guestExecRunner interface {
	execGuest(
		ctx context.Context,
		log *slog.Logger,
		vmid string,
		waitTimeout, agentTimeout time.Duration,
		command []string,
	) (GuestExecResult, error)
}

type guestLifecycleArgs interface {
	binary() string
	statusArgs(vmid string) []string
	startArgs(vmid string) []string
	stopArgs(vmid string) []string
}

type guestSnapshotArgs interface {
	listSnapshotsArgs(vmid string) []string
	snapshotArgs(vmid, snapName string) []string
	deleteSnapshotArgs(vmid, snapName string, force bool) []string
	rollbackArgs(vmid, snapName string) []string
}

type guestLockArgs interface {
	configArgs(vmid string) []string
	unlockArgs(vmid string) []string
	freezeStatusArgs(vmid string) ([]string, error)
	thawArgs(vmid string) ([]string, error)
}

func newGuestDriver(guestType config.GuestType) guestDriver {
	if guestType == config.GuestTypeLXC {
		return lxcGuest{}
	}
	return qemuGuest{}
}

type qemuGuest struct{}

func (qemuGuest) binary() string { return qmBinary }

func (qemuGuest) statusArgs(vmid string) []string { return []string{"status", vmid} }

func (qemuGuest) startArgs(vmid string) []string { return []string{"start", vmid} }

func (qemuGuest) stopArgs(vmid string) []string {
	return []string{"stop", vmid, "--timeout", qmStopTimeoutSeconds}
}

func (qemuGuest) listSnapshotsArgs(vmid string) []string {
	return []string{"listsnapshot", vmid}
}

func (qemuGuest) snapshotArgs(vmid, snapName string) []string {
	return []string{"snapshot", vmid, snapName}
}

func (qemuGuest) deleteSnapshotArgs(vmid, snapName string, force bool) []string {
	args := []string{"delsnapshot", vmid, snapName}
	if force {
		args = append(args, "--force")
	}
	return args
}

func (qemuGuest) rollbackArgs(vmid, snapName string) []string {
	return []string{"rollback", vmid, snapName}
}

func (qemuGuest) configArgs(vmid string) []string { return []string{"config", vmid} }

func (qemuGuest) unlockArgs(vmid string) []string { return []string{"unlock", vmid} }

func (qemuGuest) freezeStatusArgs(vmid string) ([]string, error) {
	return []string{"agent", vmid, "fsfreeze-status"}, nil
}

func (qemuGuest) thawArgs(vmid string) ([]string, error) {
	return []string{"agent", vmid, "fsfreeze-thaw"}, nil
}

type lxcGuest struct{}

func (lxcGuest) binary() string { return pctBinary }

func (lxcGuest) statusArgs(vmid string) []string { return []string{"status", vmid} }

func (lxcGuest) startArgs(vmid string) []string { return []string{"start", vmid} }

// pct stop does not accept a timeout option.
func (lxcGuest) stopArgs(vmid string) []string { return []string{"stop", vmid} }

func (lxcGuest) listSnapshotsArgs(vmid string) []string {
	return []string{"listsnapshot", vmid}
}

func (lxcGuest) snapshotArgs(vmid, snapName string) []string {
	return []string{"snapshot", vmid, snapName}
}

func (lxcGuest) deleteSnapshotArgs(vmid, snapName string, force bool) []string {
	args := []string{"delsnapshot", vmid, snapName}
	if force {
		args = append(args, "--force")
	}
	return args
}

// pct rollback does not start the container. The caller runs pct start afterward.
func (lxcGuest) rollbackArgs(vmid, snapName string) []string {
	return []string{"rollback", vmid, snapName}
}

func (lxcGuest) configArgs(vmid string) []string { return []string{"config", vmid} }

func (lxcGuest) unlockArgs(vmid string) []string { return []string{"unlock", vmid} }

// pct does not provide guest agent or freeze status queries.
func (lxcGuest) freezeStatusArgs(string) ([]string, error) {
	return nil, errors.New("lxc guest has no filesystem freeze status")
}

func (lxcGuest) thawArgs(string) ([]string, error) {
	return nil, errors.New("lxc guest has no filesystem thaw")
}
