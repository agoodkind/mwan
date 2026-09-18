package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Proxmox guest snapshot operations, and the recovery surface for the state
// they leave behind when one of them fails.
//
// Proxmox takes a configuration lock on the guest for the whole of a
// snapshot, a snapshot delete, or a rollback. It writes the snapshot into
// the guest configuration first, copies the disk second, and releases the
// lock in a final block that runs only once every earlier step succeeded. A
// failure that skips that block leaves the lock set and the snapshot entry
// half written, and Proxmox then refuses every later operation on that
// guest until an operator intervenes.
//
// Proxmox cleans up after its own failures, but only while its process
// lives. Killing it partway is therefore what turns a recoverable failure
// into a stranded guest, and the two operations below exist to keep that
// from happening: runQmDetached removes our ability to kill it, and the
// budget outlasts Proxmox's own limits so it always resolves first.

const (
	timeoutQmConfig   = 10 * time.Second
	timeoutQmUnlock   = 30 * time.Second
	timeoutPveshTasks = 20 * time.Second
)

// TimeoutQmLockHolding is how long the caller waits on an operation that
// holds a Proxmox guest lock. It bounds the wait, not the operation: a
// detached operation keeps running after the wait expires, which is what
// keeps a slow operation from turning into a stranded lock.
//
// Proxmox allows a guest filesystem freeze 60 minutes before failing it and
// unwinding its own lock and thaw. Snapshot deletion and rollback are
// bounded by storage work with no comparable ceiling. The budget sits above
// the freeze ceiling so the ordinary slow case still returns a normal
// command error rather than a silent wait.
const TimeoutQmLockHolding = 75 * time.Minute

// minLockHoldingTimeout is the floor the budget above must clear. A test
// enforces it so a later edit cannot quietly drop it back under the Proxmox
// ceiling.
const minLockHoldingTimeout = 65 * time.Minute

// pveTaskListLimit bounds how many tasks the liveness check reads for one
// guest. Proxmox returns the newest first, so a short window answers the
// question.
const pveTaskListLimit = 50

// scopeRunner starts a transient systemd scope. A scope is its own unit, so
// the process it starts runs outside the control group of whatever started
// it.
const scopeRunner = "systemd-run"

// pveshRunner is the Proxmox API command-line client.
const pveshRunner = "pvesh"

// runQmDetached runs qm inside a transient systemd scope, so that stopping
// the calling service cannot interrupt it.
//
// The watchdog runs as a systemd service, and stopping a service signals
// its whole control group. Every watchdog restart, and therefore every
// watchdog deploy, would otherwise be able to kill a snapshot in flight and
// leave the guest locked with a half-written snapshot entry. Running the
// operation in its own scope takes it out of that control group and removes
// the entire class of interruption. The Proxmox worker then finishes or
// fails on its own terms, and Proxmox's own cleanup runs either way.
//
// Hosts without systemd-run fall back to a direct call. Only the Proxmox
// hypervisors run this code and they all run systemd, so the fallback keeps
// unit tests and non-systemd hosts working rather than describing a
// supported deployment.
func runQmDetached(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(scopeRunner); err != nil {
		slog.WarnContext(ctx,
			"ops: systemd-run not found; running qm in this control group",
			"args", args, "err", err)
		return runQm(ctx, TimeoutQmLockHolding, args...)
	}
	slog.DebugContext(ctx, "ops: runQmDetached",
		"args", args, "wait", TimeoutQmLockHolding)
	cctx, cancel := context.WithTimeout(ctx, TimeoutQmLockHolding)
	defer cancel()
	scopeArgs := append(
		[]string{"--scope", "--quiet", "--collect", "qm"}, args...,
	)
	out, err := exec.CommandContext(cctx, scopeRunner, scopeArgs...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("systemd-run scope qm %s: %w", args[0], err)
	}
	return out, nil
}

// VMRollback rolls the VM back to the named snapshot via `qm rollback`.
func (r *RealOps) VMRollback(ctx context.Context, vmid, snap string) error {
	out, err := runQmDetached(ctx, "rollback", vmid, snap)
	if err != nil {
		r.log.ErrorContext(ctx, "qm rollback failed",
			"vmid", vmid, "snapshot", snap, "err", err,
			"output", strings.TrimSpace(string(out)))
		return fmt.Errorf(
			"qm rollback %s %s: %w: %s",
			vmid, snap, err, strings.TrimSpace(string(out)),
		)
	}
	return nil
}

// VMSnapshot creates a new snapshot named snapName on the given VM via
// `qm snapshot`.
func (r *RealOps) VMSnapshot(ctx context.Context, vmid, snapName string) error {
	out, err := runQmDetached(ctx, "snapshot", vmid, snapName)
	if err != nil {
		r.log.ErrorContext(ctx, "qm snapshot failed",
			"vmid", vmid, "snapshot", snapName, "err", err,
			"output", strings.TrimSpace(string(out)))
		return fmt.Errorf(
			"qm snapshot %s %s: %w: %s",
			vmid, snapName, err, strings.TrimSpace(string(out)),
		)
	}
	return nil
}

// VMDelSnapshot deletes the snapshot named snapName via `qm delsnapshot`.
// A failure carries the command output in the error, because Proxmox
// reports why the delete failed there rather than in the exit status, and
// callers decide what to do next from that reason.
func (r *RealOps) VMDelSnapshot(ctx context.Context, vmid, snapName string) error {
	out, err := runQmDetached(ctx, "delsnapshot", vmid, snapName)
	if err != nil {
		r.log.ErrorContext(ctx, "qm delsnapshot failed",
			"vmid", vmid, "snapshot", snapName, "err", err,
			"output", strings.TrimSpace(string(out)))
		return fmt.Errorf(
			"qm delsnapshot %s %s: %w: %s",
			vmid, snapName, err, strings.TrimSpace(string(out)),
		)
	}
	return nil
}

// VMDelSnapshotForce deletes the snapshot named snapName via
// `qm delsnapshot --force`.
//
// Without --force Proxmox aborts the delete when the storage layer cannot
// remove a disk snapshot, and the abort skips the block that removes the
// entry from the guest configuration and releases the lock. With --force
// the storage error becomes a warning, so the delete finishes, the entry
// goes away, and the lock clears.
//
// Proxmox refuses this while a lock is already set, so a caller recovering
// from a failed delete must clear the lock first. The caller also decides
// when forcing is safe; see the watchdog, which escalates only after a
// plain delete already failed with the storage layer reporting that the
// disk snapshot is gone.
func (r *RealOps) VMDelSnapshotForce(
	ctx context.Context, vmid, snapName string,
) error {
	out, err := runQmDetached(ctx, "delsnapshot", vmid, snapName, "--force")
	if err != nil {
		r.log.ErrorContext(ctx, "qm delsnapshot --force failed",
			"vmid", vmid, "snapshot", snapName, "err", err,
			"output", strings.TrimSpace(string(out)))
		return fmt.Errorf(
			"qm delsnapshot --force %s %s: %w: %s",
			vmid, snapName, err, strings.TrimSpace(string(out)),
		)
	}
	return nil
}

// VMLock reports the configuration lock currently set on the guest, or the
// empty string when none is set.
func (r *RealOps) VMLock(ctx context.Context, vmid string) (string, error) {
	out, err := runQm(ctx, timeoutQmConfig, "config", vmid)
	if err != nil {
		r.log.ErrorContext(ctx, "qm config failed",
			"vmid", vmid, "err", err,
			"output", strings.TrimSpace(string(out)))
		return "", fmt.Errorf(
			"qm config %s: %w: %s", vmid, err, strings.TrimSpace(string(out)),
		)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		value, found := strings.CutPrefix(line, "lock:")
		if found {
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
}

// VMUnlock clears the guest's configuration lock via `qm unlock`. This
// takes no lock of its own and returns immediately.
func (r *RealOps) VMUnlock(ctx context.Context, vmid string) error {
	out, err := runQm(ctx, timeoutQmUnlock, "unlock", vmid)
	if err != nil {
		r.log.ErrorContext(ctx, "qm unlock failed",
			"vmid", vmid, "err", err,
			"output", strings.TrimSpace(string(out)))
		return fmt.Errorf(
			"qm unlock %s: %w: %s", vmid, err, strings.TrimSpace(string(out)),
		)
	}
	r.log.InfoContext(ctx, "cleared guest lock", "vmid", vmid)
	return nil
}

// pveTask is the one field of a Proxmox task record the liveness check
// reads. Proxmox omits endtime while a task is still running.
type pveTask struct {
	EndTime *int64 `json:"endtime"`
}

// VMHasRunningTask reports whether Proxmox currently has a task running
// against the given guest. Clearing a lock while its operation is still
// running corrupts that operation, so every automatic recovery checks this
// first.
//
// The task list defaults to finished tasks only, so the query asks for
// active ones explicitly.
func (r *RealOps) VMHasRunningTask(
	ctx context.Context, vmid string,
) (bool, error) {
	if r.pveNode == "" {
		err := errors.New("ops: pve node is not configured")
		r.log.ErrorContext(ctx, "task liveness check unavailable", "err", err)
		return false, err
	}
	out, err := runPvesh(ctx,
		"get", nodeTasksPath(r.pveNode),
		"--vmid", vmid,
		"--source", "active",
		"--limit", strconv.Itoa(pveTaskListLimit),
		"--output-format", "json",
	)
	if err != nil {
		r.log.ErrorContext(ctx, "pvesh task list failed",
			"vmid", vmid, "err", err)
		return false, err
	}
	var tasks []pveTask
	if err := json.Unmarshal(out, &tasks); err != nil {
		r.log.ErrorContext(ctx, "pvesh task list decode failed",
			"vmid", vmid, "err", err)
		return false, fmt.Errorf("pvesh tasks %s decode: %w", vmid, err)
	}
	running := 0
	for _, task := range tasks {
		if task.EndTime == nil {
			running++
		}
	}
	r.log.InfoContext(ctx, "read guest task liveness",
		"vmid", vmid, "running_tasks", running)
	return running > 0, nil
}

// nodeTasksPath builds the Proxmox API path for one node's task list.
func nodeTasksPath(node string) string {
	return "/nodes/" + node + "/tasks"
}

// runPvesh wraps the Proxmox API command-line client with a bounded wait,
// keeping its stderr out of the parsed output.
func runPvesh(ctx context.Context, args ...string) ([]byte, error) {
	slog.DebugContext(ctx, "ops: runPvesh", "args", args)
	cctx, cancel := context.WithTimeout(ctx, timeoutPveshTasks)
	defer cancel()
	cmd := exec.CommandContext(cctx, pveshRunner, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		slog.ErrorContext(ctx, "ops: pvesh failed",
			"args", args, "err", err,
			"stderr", strings.TrimSpace(stderr.String()))
		return nil, fmt.Errorf(
			"pvesh %s: %w: %s",
			args[0], err, strings.TrimSpace(stderr.String()),
		)
	}
	return out, nil
}
