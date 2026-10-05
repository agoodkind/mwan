// Package redteam wraps an ops.SysOps implementation and makes selected calls
// return injected faults instead of reaching the real host and guest. The
// watchdog's failure handling then runs against a reproducible failure rather
// than a broken network or a stopped guest. The watchdog builds this wrapper
// only when its -red-team flag names a preset; without that flag the wrapper is
// never constructed.
package redteam

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/ops"
)

// Preset holds the fault-injection configuration for one scenario. Each field
// turns on one deviation in the Ops wrapper, so a zero Preset leaves every call
// untouched. Description is the operator-facing summary the watchdog logs when
// it selects the scenario and the text `-list-scenarios` prints.
type Preset struct {
	Description         string
	HostV4Fail          bool
	HostV6Fail          bool
	VMStopped           bool
	GuestExecFail       bool
	GuestDefaultFail    bool
	GuestIfaceFail      bool
	GuestIfaceSucceed   bool // force per-interface ISP pings to succeed (simulate ISP up)
	DeployTSMode        deployTSMode
	InjectSnapshot      bool
	InjectChangeMarker  bool
	InjectKnownGoodSnap bool
	OmitDeployMarker    bool
}

type deployTSMode string

const (
	deployTSModeNone            deployTSMode = "none"
	deployTSModeAlwaysRecent    deployTSMode = "always_recent"
	deployTSModeRecentThenStale deployTSMode = "recent_then_stale"
)

// Presets maps each scenario name to the fault set it injects. The watchdog's
// -red-team flag looks its argument up here and refuses to start when the name
// is absent, so this map is the full set of runnable scenarios.
var Presets = map[string]Preset{
	"ipv4-loss": {
		Description:         "IPv4 fails, IPv6 passes -> partial alert",
		HostV4Fail:          true,
		HostV6Fail:          false,
		VMStopped:           false,
		GuestExecFail:       false,
		GuestDefaultFail:    false,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   false,
		DeployTSMode:        "",
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    false,
	},
	"ipv6-loss": {
		Description:         "IPv6 fails, IPv4 passes -> partial alert",
		HostV4Fail:          false,
		HostV6Fail:          true,
		VMStopped:           false,
		GuestExecFail:       false,
		GuestDefaultFail:    false,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   false,
		DeployTSMode:        "",
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    false,
	},
	"total-loss-mwan": {
		Description:         "Both fail, ISP up -> MWAN routing failure -> rollback",
		HostV4Fail:          true,
		HostV6Fail:          true,
		VMStopped:           false,
		GuestExecFail:       false,
		GuestDefaultFail:    true,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   true,
		DeployTSMode:        deployTSModeRecentThenStale,
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    false,
	},
	"total-loss-isp": {
		Description:         "Both fail, no recent config change -> real outage, no rollback",
		HostV4Fail:          true,
		HostV6Fail:          true,
		VMStopped:           false,
		GuestExecFail:       false,
		GuestDefaultFail:    true,
		GuestIfaceFail:      true,
		GuestIfaceSucceed:   false,
		DeployTSMode:        "",
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    true,
	},
	"vm-crash": {
		Description:         "VM appears stopped -> watchdog waits",
		HostV4Fail:          false,
		HostV6Fail:          false,
		VMStopped:           true,
		GuestExecFail:       false,
		GuestDefaultFail:    false,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   false,
		DeployTSMode:        "",
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    false,
	},
	"guest-agent-down": {
		Description:         "Guest agent fails -> diagnosis degraded",
		HostV4Fail:          true,
		HostV6Fail:          true,
		VMStopped:           false,
		GuestExecFail:       true,
		GuestDefaultFail:    false,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   false,
		DeployTSMode:        "",
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    false,
	},
	"proxmox-routing": {
		Description:         "Host fails, VM has internet, no config change -> Proxmox-side issue",
		HostV4Fail:          true,
		HostV6Fail:          true,
		VMStopped:           false,
		GuestExecFail:       false,
		GuestDefaultFail:    false,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   false,
		DeployTSMode:        "",
		InjectSnapshot:      false,
		InjectChangeMarker:  false,
		InjectKnownGoodSnap: false,
		OmitDeployMarker:    true,
	},
	"config-drift": {
		Description:         "No deploy marker; change marker + known-good snapshot -> rollback",
		HostV4Fail:          true,
		HostV6Fail:          true,
		VMStopped:           false,
		GuestExecFail:       false,
		GuestDefaultFail:    true,
		GuestIfaceFail:      false,
		GuestIfaceSucceed:   true,
		DeployTSMode:        "",
		InjectSnapshot:      true,
		InjectChangeMarker:  true,
		InjectKnownGoodSnap: true,
		OmitDeployMarker:    true,
	},
}

// Ops wraps an ops.SysOps implementation and answers from its Preset instead of
// the wrapped ops for the calls that preset selects. Every other call reaches
// the wrapped ops unchanged, so a scenario breaks exactly the paths it names.
// The now field supplies the clock the injected timestamps read, and
// deployTSInjected records that the last-deploy marker has already been served
// once so deployTSModeRecentThenStale can go stale on the next read.
type Ops struct {
	inner  ops.SysOps
	preset Preset
	log    *slog.Logger
	now    func() time.Time

	deployTSInjected bool
}

// NewOps creates a new red-team Ops wrapper around a SysOps implementation.
func NewOps(inner ops.SysOps, preset Preset, log *slog.Logger) *Ops {
	return NewOpsWithClock(inner, preset, log, time.Now)
}

// NewOpsWithClock creates the wrapper with an explicit clock. The injected
// deploy and change timestamps are computed from now, so a test can pin them to
// a fixed instant. A nil now falls back to [time.Now].
func NewOpsWithClock(
	inner ops.SysOps,
	preset Preset,
	log *slog.Logger,
	now func() time.Time,
) *Ops {
	if now == nil {
		now = time.Now
	}
	return &Ops{
		inner:            inner,
		preset:           preset,
		log:              log,
		now:              now,
		deployTSInjected: false,
	}
}

// VMStatus reports the guest as not running when the preset sets VMStopped,
// which stands in for a crashed or halted guest. Otherwise it returns the
// wrapped ops' answer.
func (r *Ops) VMStatus(ctx context.Context, vmid string) (bool, error) {
	if r.preset.VMStopped {
		r.log.InfoContext(
			ctx,
			"[RED TEAM] injecting fault",
			"fault", "vm_stopped",
			"vmid", vmid,
		)
		return false, nil
	}
	running, err := r.inner.VMStatus(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "read vm status failed", "vmid", vmid, "err", err)
		return running, fmt.Errorf("redteam VMStatus: %w", err)
	}
	return running, nil
}

// VMStop passes the stop through to the wrapped ops. No preset changes it.
func (r *Ops) VMStop(ctx context.Context, vmid string) error {
	if err := r.inner.VMStop(ctx, vmid); err != nil {
		r.log.WarnContext(ctx, "stop vm failed", "vmid", vmid, "err", err)
		return fmt.Errorf("redteam VMStop: %w", err)
	}
	return nil
}

// VMRollback passes the rollback through to the wrapped ops. No preset changes
// it, so a scenario that drives the watchdog to roll back exercises the real
// rollback call.
func (r *Ops) VMRollback(ctx context.Context, vmid, snap string) error {
	if err := r.inner.VMRollback(ctx, vmid, snap); err != nil {
		r.log.WarnContext(ctx, "rollback vm failed",
			"vmid", vmid, "snapshot", snap, "err", err)
		return fmt.Errorf("redteam VMRollback: %w", err)
	}
	return nil
}

// VMStart passes the start through to the wrapped ops. No preset changes it.
func (r *Ops) VMStart(ctx context.Context, vmid string) error {
	if err := r.inner.VMStart(ctx, vmid); err != nil {
		r.log.WarnContext(ctx, "start vm failed", "vmid", vmid, "err", err)
		return fmt.Errorf("redteam VMStart: %w", err)
	}
	return nil
}

// VMSnapshots returns a single fabricated snapshot line when the preset sets
// InjectSnapshot, which gives a scenario a rollback target the guest does not
// really have. The fabricated name is known-good-<timestamp> when
// InjectKnownGoodSnap is set and pre-deploy-<timestamp> otherwise, and the
// timestamp comes from the wrapper's clock. Without InjectSnapshot it returns
// the wrapped ops' output.
func (r *Ops) VMSnapshots(ctx context.Context, vmid string) ([]byte, error) {
	if r.preset.InjectSnapshot {
		r.log.InfoContext(
			ctx,
			"[RED TEAM] injecting fault",
			"fault", "fake_snapshot",
			"vmid", vmid,
		)
		suffix := r.now().Format("20060102-150405")
		var fake string
		if r.preset.InjectKnownGoodSnap {
			fake = fmt.Sprintf("`-> known-good-%s\n", suffix)
		} else {
			fake = fmt.Sprintf("`-> pre-deploy-%s\n", suffix)
		}
		return []byte(fake), nil
	}
	out, err := r.inner.VMSnapshots(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "list snapshots failed", "vmid", vmid, "err", err)
		return out, fmt.Errorf("redteam VMSnapshots: %w", err)
	}
	return out, nil
}

// VMSnapshot passes the snapshot creation through to the wrapped ops. No preset
// changes it; InjectSnapshot fabricates listings only, not new snapshots.
func (r *Ops) VMSnapshot(
	ctx context.Context, vmid, snapName string,
) error {
	if err := r.inner.VMSnapshot(ctx, vmid, snapName); err != nil {
		r.log.WarnContext(ctx, "create snapshot failed",
			"vmid", vmid, "snapshot", snapName, "err", err)
		return fmt.Errorf("redteam VMSnapshot: %w", err)
	}
	return nil
}

// VMDelSnapshot passes the snapshot delete through to the wrapped ops. No
// preset changes it.
func (r *Ops) VMDelSnapshot(
	ctx context.Context, vmid, snapName string,
) error {
	if err := r.inner.VMDelSnapshot(ctx, vmid, snapName); err != nil {
		r.log.WarnContext(ctx, "delete snapshot failed",
			"vmid", vmid, "snapshot", snapName, "err", err)
		return fmt.Errorf("redteam VMDelSnapshot: %w", err)
	}
	return nil
}

// VMDelSnapshotForce passes the forced delete through to the wrapped ops;
// no red-team preset injects snapshot-cleanup faults.
func (r *Ops) VMDelSnapshotForce(
	ctx context.Context, vmid, snapName string,
) error {
	if err := r.inner.VMDelSnapshotForce(ctx, vmid, snapName); err != nil {
		r.log.WarnContext(ctx, "delsnapshot force failed",
			"vmid", vmid, "snapshot", snapName, "err", err)
		return fmt.Errorf("delsnapshot force: %w", err)
	}
	return nil
}

// VMLock passes the guest-lock query through to the wrapped ops; no
// red-team preset injects guest-lock faults.
func (r *Ops) VMLock(ctx context.Context, vmid string) (string, error) {
	lock, err := r.inner.VMLock(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "read guest lock failed", "vmid", vmid, "err", err)
		return "", fmt.Errorf("read guest lock: %w", err)
	}
	return lock, nil
}

// VMUnlock passes the unlock through to the wrapped ops; no red-team
// preset injects guest-lock faults.
func (r *Ops) VMUnlock(ctx context.Context, vmid string) error {
	if err := r.inner.VMUnlock(ctx, vmid); err != nil {
		r.log.WarnContext(ctx, "clear guest lock failed", "vmid", vmid, "err", err)
		return fmt.Errorf("clear guest lock: %w", err)
	}
	return nil
}

// VMHasRunningTask passes the task-liveness query through to the wrapped
// ops; no red-team preset injects task faults.
func (r *Ops) VMHasRunningTask(
	ctx context.Context, vmid string,
) (bool, error) {
	running, err := r.inner.VMHasRunningTask(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "task liveness check failed", "vmid", vmid, "err", err)
		return false, fmt.Errorf("task liveness: %w", err)
	}
	return running, nil
}

// VMFSFreezeStatus passes the freeze-state query through to the wrapped ops;
// no red-team preset injects freeze faults.
func (r *Ops) VMFSFreezeStatus(ctx context.Context, vmid string) (string, error) {
	status, err := r.inner.VMFSFreezeStatus(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "fsfreeze-status query failed", "vmid", vmid, "err", err)
		return "", fmt.Errorf("fsfreeze-status: %w", err)
	}
	return status, nil
}

// VMFSFreezeThaw passes the thaw through to the wrapped ops; no red-team
// preset injects freeze faults.
func (r *Ops) VMFSFreezeThaw(ctx context.Context, vmid string) error {
	if err := r.inner.VMFSFreezeThaw(ctx, vmid); err != nil {
		r.log.WarnContext(ctx, "fsfreeze-thaw failed", "vmid", vmid, "err", err)
		return fmt.Errorf("fsfreeze-thaw: %w", err)
	}
	return nil
}

// GuestExec fails every command when the preset sets GuestExecFail, which
// stands in for a dead guest agent. Otherwise the ping, last-deploy and
// last-change handlers each get a chance to answer from the preset, and any
// command none of them claims reaches the wrapped ops.
func (r *Ops) GuestExec(
	ctx context.Context, vmid string, args ...string,
) (ops.GuestExecResult, error) {
	if r.preset.GuestExecFail {
		r.logFault(ctx, "guest_exec_fail", vmid, args)
		return ops.GuestExecResult{ExitCode: 1, Stdout: ""},
			fmt.Errorf("red-team: guest agent down")
	}
	if res, handled := r.handlePingFault(ctx, vmid, args); handled {
		return res, nil
	}
	if res, handled := r.handleDeployFault(ctx, vmid, args); handled {
		return res, nil
	}
	if res, handled := r.handleChangeFault(ctx, vmid, args); handled {
		return res, nil
	}
	res, err := r.inner.GuestExec(ctx, vmid, args...)
	if err != nil {
		r.log.WarnContext(ctx, "guest exec failed",
			"vmid", vmid, "args", strings.Join(args, " "), "err", err)
		return res, fmt.Errorf("redteam GuestExec: %w", err)
	}
	return res, nil
}

func (r *Ops) logFault(ctx context.Context, fault, vmid string, args []string) {
	r.log.InfoContext(
		ctx,
		"[RED TEAM] injecting fault",
		"fault", fault,
		"vmid", vmid,
		"args", strings.Join(args, " "),
	)
}

// isPingArgs reports whether args invokes one of the two ping binaries.
func isPingArgs(args []string) bool {
	return len(args) > 0 && (args[0] == "ping" || args[0] == "ping6")
}

// hasIfaceArg reports whether args binds the probe to one interface, which is
// what separates a per-ISP probe from a default-route probe.
func hasIfaceArg(args []string) bool {
	return slices.Contains(args, "-I")
}

// isCatDeployArgs reports whether args reads the last-deploy timestamp marker.
func isCatDeployArgs(args []string) bool {
	return len(args) >= 2 && args[0] == "cat" &&
		strings.Contains(args[1], "last-deploy")
}

// isCatChangeArgs reports whether args reads the last-change timestamp marker.
func isCatChangeArgs(args []string) bool {
	return len(args) >= 2 && args[0] == "cat" &&
		strings.Contains(args[1], "mwan-last-change")
}

func (r *Ops) handlePingFault(
	ctx context.Context,
	vmid string,
	args []string,
) (ops.GuestExecResult, bool) {
	isPing := isPingArgs(args)
	hasIface := hasIfaceArg(args)
	if isPing && hasIface && r.preset.GuestIfaceFail {
		r.logFault(ctx, "guest_iface_fail", vmid, args)
		return ops.GuestExecResult{ExitCode: 1, Stdout: ""}, true
	}
	if isPing && hasIface && r.preset.GuestIfaceSucceed {
		r.logFault(ctx, "guest_iface_succeed", vmid, args)
		return ops.GuestExecResult{ExitCode: 0, Stdout: ""}, true
	}
	if isPing && !hasIface && r.preset.GuestDefaultFail {
		r.logFault(ctx, "guest_default_route_fail", vmid, args)
		return ops.GuestExecResult{ExitCode: 1, Stdout: ""}, true
	}
	return ops.GuestExecResult{ExitCode: 0, Stdout: ""}, false
}

func (r *Ops) handleDeployFault(
	ctx context.Context,
	vmid string,
	args []string,
) (ops.GuestExecResult, bool) {
	if !isCatDeployArgs(args) {
		return ops.GuestExecResult{ExitCode: 0, Stdout: ""}, false
	}
	if r.preset.OmitDeployMarker {
		return ops.GuestExecResult{ExitCode: 1, Stdout: ""}, true
	}
	if r.preset.DeployTSMode == deployTSModeRecentThenStale && r.deployTSInjected {
		oldTS := r.now().Unix() - 7200
		r.logFault(ctx, "inject_deploy_ts_once", vmid, args)
		return ops.GuestExecResult{
			ExitCode: 0, Stdout: strconv.FormatInt(oldTS, 10),
		}, true
	}
	if r.preset.DeployTSMode == deployTSModeAlwaysRecent ||
		r.preset.DeployTSMode == deployTSModeRecentThenStale {
		ts := r.now().Unix() - 60
		r.logFault(ctx, "inject_deploy_ts", vmid, args)
		r.deployTSInjected = true
		return ops.GuestExecResult{
			ExitCode: 0, Stdout: strconv.FormatInt(ts, 10),
		}, true
	}
	return ops.GuestExecResult{ExitCode: 0, Stdout: ""}, false
}

func (r *Ops) handleChangeFault(
	ctx context.Context,
	vmid string,
	args []string,
) (ops.GuestExecResult, bool) {
	if !isCatChangeArgs(args) || !r.preset.InjectChangeMarker {
		return ops.GuestExecResult{ExitCode: 0, Stdout: ""}, false
	}
	ts := r.now().Unix() - 60
	r.logFault(ctx, "inject_change_ts", vmid, args)
	return ops.GuestExecResult{
		ExitCode: 0, Stdout: strconv.FormatInt(ts, 10),
	}, true
}

// Ping reports failure for the `ping` binary when the preset sets HostV4Fail
// and for `ping6` when it sets HostV6Fail. These are the host's own probes, so
// a scenario can break host reachability while the guest still answers. Any
// other binary, or an unset flag, reaches the wrapped ops.
func (r *Ops) Ping(ctx context.Context, bin, target string) bool {
	if bin == "ping" && r.preset.HostV4Fail {
		r.log.InfoContext(
			ctx,
			"[RED TEAM] injecting fault",
			"fault", "host_v4_fail",
			"target", target,
		)
		return false
	}
	if bin == "ping6" && r.preset.HostV6Fail {
		r.log.InfoContext(
			ctx,
			"[RED TEAM] injecting fault",
			"fault", "host_v6_fail",
			"target", target,
		)
		return false
	}
	return r.inner.Ping(ctx, bin, target)
}

// GetConfigState passes the config-state query through to the wrapped ops. No
// preset changes it; a scenario that needs a fabricated deploy timestamp
// injects one through the last-deploy marker read in GuestExec instead.
func (r *Ops) GetConfigState(
	ctx context.Context, vmid string,
) (*mwanv1.GetConfigStateResponse, string, error) {
	res, channel, err := r.inner.GetConfigState(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "read config state failed", "vmid", vmid, "err", err)
		return res, channel, fmt.Errorf("redteam GetConfigState: %w", err)
	}
	return res, channel, nil
}

// GetBGPStatus passes the BGP status query through to the wrapped ops. No
// preset changes it.
func (r *Ops) GetBGPStatus(
	ctx context.Context, vmid string,
) (*mwanv1.GetBGPStatusResponse, error) {
	res, err := r.inner.GetBGPStatus(ctx, vmid)
	if err != nil {
		r.log.WarnContext(ctx, "read bgp status failed", "vmid", vmid, "err", err)
		return res, fmt.Errorf("redteam GetBGPStatus: %w", err)
	}
	return res, nil
}

// AnnounceRoutes passes the route announcement through to the wrapped ops. No
// preset changes it, so a scenario that drives a recovery exercises the real
// announcement.
func (r *Ops) AnnounceRoutes(ctx context.Context, vmid string) error {
	if err := r.inner.AnnounceRoutes(ctx, vmid); err != nil {
		r.log.WarnContext(ctx, "announce routes failed", "vmid", vmid, "err", err)
		return fmt.Errorf("redteam AnnounceRoutes: %w", err)
	}
	return nil
}

// WithdrawRoutes passes the route withdrawal through to the wrapped ops. No
// preset changes it, so a scenario that drives a failover exercises the real
// withdrawal.
func (r *Ops) WithdrawRoutes(ctx context.Context, vmid string) error {
	if err := r.inner.WithdrawRoutes(ctx, vmid); err != nil {
		r.log.WarnContext(ctx, "withdraw routes failed", "vmid", vmid, "err", err)
		return fmt.Errorf("redteam WithdrawRoutes: %w", err)
	}
	return nil
}
