package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mdlayher/vsock"
	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/tracing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Each timeout bounds one `qm` invocation or one RPC. They differ because the
// underlying operations differ in cost: a status query answers from the
// hypervisor's own state, while stopping or starting a guest waits on the
// guest itself.
const (
	// TimeoutQmStatus bounds `qm status`, which reads hypervisor state and
	// returns without touching the guest.
	TimeoutQmStatus = 10 * time.Second
	// timeoutQmGuestExec is the --timeout `qm guest exec` waits for the guest
	// command to exit, and timeoutQmGuestExecWait bounds the qm process
	// itself. The second is longer so qm reports the command's fate before
	// the process is killed.
	timeoutQmGuestExec     = 30 * time.Second
	timeoutQmGuestExecWait = 45 * time.Second
	// TimeoutQmStop bounds `qm stop`, which waits for the guest to halt.
	TimeoutQmStop = 60 * time.Second
	// TimeoutQmStart bounds `qm start`, which returns once the hypervisor has
	// started the guest rather than once the guest has booted.
	TimeoutQmStart        = 60 * time.Second
	timeoutQmListSnapshot = 10 * time.Second
	timeoutQmAgentFreeze  = 30 * time.Second
	timeoutHostProbe      = 20 * time.Second
	timeoutVsockRPC       = 15 * time.Second
	timeoutTCPRPC         = 15 * time.Second
)

// guestCmd enumerates the argv[0] commands the in-guest gRPC adapter
// translates from `GuestExec` argv into typed RPCs.
type guestCmd string

const (
	guestCmdPing  guestCmd = "ping"
	guestCmdPing6 guestCmd = "ping6"
	guestCmdCat   guestCmd = "cat"
)

// GuestExecResult includes stderr only from the hypervisor command channel.
type GuestExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// LifecycleOps starts, stops and reports on a guest as a whole.
type LifecycleOps interface {
	VMStatus(ctx context.Context, vmid string) (bool, error)
	VMStart(ctx context.Context, vmid string) error
	VMStop(ctx context.Context, vmid string) error
}

// SnapshotOps lists, takes, deletes and rolls back to guest snapshots. A
// rollback belongs here rather than with the lifecycle because it restores a
// snapshot; the stop and start around it are the caller's to sequence.
type SnapshotOps interface {
	VMSnapshots(ctx context.Context, vmid string) ([]byte, error)
	VMSnapshot(ctx context.Context, vmid, snapName string) error
	VMDelSnapshot(ctx context.Context, vmid, snapName string) error
	VMDelSnapshotForce(ctx context.Context, vmid, snapName string) error
	VMRollback(ctx context.Context, vmid, snap string) error
}

// GatewayOps reaches the running gateway: commands and queries through its
// agent, and route announcement for failover. Ping is here although it probes
// from the hypervisor rather than through the agent, because it answers the
// same question as the rest, whether the gateway is reachable, and the
// failover path calls it beside them.
type GatewayOps interface {
	GuestExec(
		ctx context.Context, vmid string, args ...string,
	) (GuestExecResult, error)
	Ping(ctx context.Context, bin, target string) bool
	GetConfigState(
		ctx context.Context, vmid string,
	) (*mwanv1.GetConfigStateResponse, string, error)
	GetBGPStatus(
		ctx context.Context, vmid string,
	) (*mwanv1.GetBGPStatusResponse, error)
	AnnounceRoutes(ctx context.Context, vmid string) error
	WithdrawRoutes(ctx context.Context, vmid string) error
}

// LockOps reads and clears the Proxmox configuration lock and the guest
// agent's filesystem freeze, which a failed snapshot can leave set.
// VMHasRunningTask is here because clearing a lock while its task still runs
// corrupts that task, so every recovery that clears one checks it first.
type LockOps interface {
	VMLock(ctx context.Context, vmid string) (string, error)
	VMUnlock(ctx context.Context, vmid string) error
	VMHasRunningTask(ctx context.Context, vmid string) (bool, error)
	VMFSFreezeStatus(ctx context.Context, vmid string) (string, error)
	VMFSFreezeThaw(ctx context.Context, vmid string) error
}

// SysOps is every external dependency the watchdog has: the hypervisor, the
// guest, and the Proxmox API. The watchdog depends on this interface rather
// than on the implementations so the red-team wrapper can inject faults and
// the dry-run wrapper can suppress destructive calls, both without the
// watchdog knowing.
//
// It is composed from four parts because the watchdog uses all four. A caller
// that needs one part should depend on that part, so a method added to one
// surface does not widen what an unrelated caller must implement.
type SysOps interface {
	LifecycleOps
	SnapshotOps
	GatewayOps
	LockOps
}

// RealOps tries vsock, then management TCP, then a hypervisor command.
// Vsock does not require guest network access. The guest driver selects qm
// for QEMU and pct for LXC operations.
type RealOps struct {
	log       *slog.Logger
	vsockCID  uint32
	vsockPort uint32
	pveNode   string
	nc        config.NetworkConfig
	guest     guestDriver

	// testVsockOverride, if set, replaces vsockExec inside GuestExec (unit tests only).
	testVsockOverride func(
		ctx context.Context, args ...string,
	) (GuestExecResult, error)

	// testGrpcDialer, if set, replaces vsock.Dial in vsockExec (unit tests only).
	testGrpcDialer func(ctx context.Context, addr string) (net.Conn, error)

	tcpAddr string
	tracker *ChannelTracker

	// Route operations must select a gateway by its configured ID.
	primaryVMID     string
	failoverVMID    string
	failoverTCPAddr string

	// testTCPDialer, if set, replaces net.Dial in tcpExec (unit tests only).
	testTCPDialer func(ctx context.Context, addr string) (net.Conn, error)
}

// NewRealOps builds the production SysOps from the daemon's configuration. A
// nil logger becomes the slog default, so a caller that has not set one up
// still gets output.
func NewRealOps(
	cfg *config.Config,
	logger *slog.Logger,
) *RealOps {
	if logger == nil {
		logger = slog.Default()
	}
	return &RealOps{
		log:       logger.With("component", "ops"),
		vsockCID:  cfg.Watchdog.VsockCID,
		vsockPort: cfg.Watchdog.VsockPort,
		pveNode:   cfg.PVE.Node,
		nc:        cfg.Network,
		guest:     newGuestDriver(cfg.GuestType),
		tcpAddr:   cfg.Watchdog.MwanAgentTCPAddr,
		tracker:   NewChannelTracker(),

		primaryVMID:     cfg.MwanVMID,
		failoverVMID:    cfg.Failover.LXCID,
		failoverTCPAddr: cfg.Failover.AgentTCPAddr,

		// Production never overrides a dial or an exec; only tests do.
		testVsockOverride: nil,
		testGrpcDialer:    nil,
		testTCPDialer:     nil,
	}
}

func runHypervisor(
	ctx context.Context,
	binary string,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	slog.DebugContext(ctx, "ops: runHypervisor",
		"binary", binary, "args", args, "timeout", timeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, binary, args...).CombinedOutput()
	if err != nil {
		// The caller decides whether a command failure requires an Error log.
		slog.WarnContext(ctx, "ops: hypervisor command failed",
			"binary", binary, "args", args, "err", err,
			"output", strings.TrimSpace(string(out)))
		// The output is returned alongside the error because callers read the
		// combined output to decide what failed.
		return out, fmt.Errorf("%s %s: %w", binary, strings.Join(args, " "), err)
	}
	return out, nil
}

func runQm(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	return runHypervisor(ctx, qmBinary, timeout, args...)
}

func (r *RealOps) runGuest(
	ctx context.Context, timeout time.Duration, args ...string,
) ([]byte, error) {
	return runHypervisor(ctx, r.guest.binary(), timeout, args...)
}

// VMStatus checks whether qm status or pct status reports a running guest.
func (r *RealOps) VMStatus(ctx context.Context, vmid string) (bool, error) {
	out, err := r.runGuest(ctx, TimeoutQmStatus, r.guest.statusArgs(vmid)...)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "running"), nil
}

// VMStop gives qm stop a 30 second timeout. pct stop does not accept a timeout option.
func (r *RealOps) VMStop(ctx context.Context, vmid string) error {
	_, err := r.runGuest(ctx, TimeoutQmStop, r.guest.stopArgs(vmid)...)
	return err
}

// VMStart selects qm start or pct start according to the guest type.
func (r *RealOps) VMStart(ctx context.Context, vmid string) error {
	_, err := r.runGuest(ctx, TimeoutQmStart, r.guest.startArgs(vmid)...)
	return err
}

// VMSnapshots selects qm listsnapshot or pct listsnapshot according to the guest type.
func (r *RealOps) VMSnapshots(ctx context.Context, vmid string) ([]byte, error) {
	return r.runGuest(ctx, timeoutQmListSnapshot, r.guest.listSnapshotsArgs(vmid)...)
}

// VMFSFreezeStatus queries the QEMU guest agent for the filesystem freeze state.
// It returns an error for LXC guests.
func (r *RealOps) VMFSFreezeStatus(ctx context.Context, vmid string) (string, error) {
	args, err := r.guest.freezeStatusArgs(vmid)
	if err != nil {
		return "", err
	}
	out, err := r.runGuest(ctx, timeoutQmAgentFreeze, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// VMFSFreezeThaw asks the QEMU guest agent to thaw the filesystem.
// It returns an error for LXC guests.
func (r *RealOps) VMFSFreezeThaw(ctx context.Context, vmid string) error {
	args, err := r.guest.thawArgs(vmid)
	if err != nil {
		return err
	}
	_, err = r.runGuest(ctx, timeoutQmAgentFreeze, args...)
	return err
}

// GuestExec tries vsock, then management TCP, then a hypervisor command.
// ChannelTracker records whether each attempted transport succeeded.
// An LXC guest runs the hypervisor command only.
func (r *RealOps) GuestExec(
	ctx context.Context, vmid string, args ...string,
) (GuestExecResult, error) {
	if !r.guest.hasGuestAgentChannels() {
		return r.trackedHypervisorExec(ctx, 1, vmid, args...)
	}

	// Allow unit test overrides to bypass the real transport layer.
	if r.testVsockOverride != nil {
		res, err := r.testVsockOverride(ctx, args...)
		if err == nil {
			return res, nil
		}
		return r.hypervisorExec(ctx, vmid, args...)
	}

	// Channel 1: vsock
	r.logAttemptStart(ctx, "guest_exec", ChanVsock, 1, vmid)
	vsockRes, vsockErr := r.vsockExec(ctx, args...)
	if vsockErr == nil {
		r.tracker.recordSuccess(ChanVsock)
		r.logAttemptResult(ctx, "guest_exec", ChanVsock, 1, vmid, nil)
		return vsockRes, nil
	}
	r.tracker.recordFailure(ChanVsock, vsockErr)
	r.logAttemptResult(ctx, "guest_exec", ChanVsock, 1, vmid, vsockErr)

	// Channel 2: TCP management interface
	r.logAttemptStart(ctx, "guest_exec", ChanTCP, 2, vmid)
	tcpRes, tcpErr := r.tcpExec(ctx, args...)
	if tcpErr == nil {
		r.tracker.recordSuccess(ChanTCP)
		r.logAttemptResult(ctx, "guest_exec", ChanTCP, 2, vmid, nil)
		return tcpRes, nil
	}
	r.tracker.recordFailure(ChanTCP, tcpErr)
	r.logAttemptResult(ctx, "guest_exec", ChanTCP, 2, vmid, tcpErr)

	return r.trackedHypervisorExec(ctx, 3, vmid, args...)
}

func (r *RealOps) trackedHypervisorExec(
	ctx context.Context, attempt int, vmid string, args ...string,
) (GuestExecResult, error) {
	r.logAttemptStart(ctx, "guest_exec", ChanPVE, attempt, vmid)
	result, err := r.hypervisorExec(ctx, vmid, args...)
	if err == nil {
		r.tracker.recordSuccess(ChanPVE)
	} else {
		r.tracker.recordFailure(ChanPVE, err)
	}
	r.logAttemptResult(ctx, "guest_exec", ChanPVE, attempt, vmid, err)
	return result, err
}

func (r *RealOps) hypervisorExec(
	ctx context.Context, vmid string, args ...string,
) (GuestExecResult, error) {
	result, err := r.guest.execGuest(
		ctx, r.log, vmid, timeoutQmGuestExecWait, timeoutQmGuestExec, args)
	return result, err
}

func (r *RealOps) vsockExec(
	ctx context.Context, args ...string,
) (GuestExecResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutVsockRPC)
	defer cancel()
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		return vsock.Dial(r.vsockCID, r.vsockPort, nil)
	}
	if r.testGrpcDialer != nil {
		dialer = r.testGrpcDialer
	}
	conn, err := grpc.NewClient(
		"passthrough:///mwan",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		r.log.WarnContext(ctx, "vsock grpc client failed", "err", err)
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
			fmt.Errorf("vsock grpc client: %w", err)
	}
	defer func() { _ = conn.Close() }()

	cli := mwanv1.NewMWANAgentClient(conn)

	if len(args) == 0 {
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""}, fmt.Errorf("vsockExec: no args")
	}
	switch guestCmd(args[0]) {
	case guestCmdPing, guestCmdPing6:
		req := &mwanv1.PingRequest{
			Target:         pingTarget(args),
			BindInterface:  pingIface(args),
			Count:          pingCount(args, 2),
			TimeoutSeconds: 3,
		}
		resp, err := cli.Ping(cctx, req)
		if err != nil {
			r.log.WarnContext(ctx, "vsock ping failed", "err", err)
			return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
				fmt.Errorf("vsock ping: %w", err)
		}
		if resp.GetSuccess() {
			return GuestExecResult{ExitCode: 0, Stdout: "", Stderr: ""}, nil
		}
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""}, nil
	case guestCmdCat:
		if len(args) >= 2 && isLastDeployPath(args[1]) {
			resp, err := cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
			if err != nil {
				r.log.WarnContext(ctx, "vsock get config state failed", "err", err)
				return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
					fmt.Errorf("vsock get config state: %w", err)
			}
			ts := strconv.FormatInt(resp.GetLastDeployEpoch(), 10)
			return GuestExecResult{ExitCode: 0, Stdout: ts, Stderr: ""}, nil
		}
	}
	return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
		fmt.Errorf("vsockExec: unhandled command %q", args[0])
}

// isLastDeployPath reports whether p names the last-deploy timestamp
// file. Matching the suffix "last-deploy" is safe because mwan-last-change
// uses a different suffix.
func isLastDeployPath(p string) bool {
	return strings.Contains(p, "last-deploy")
}

func (r *RealOps) tcpExec(
	ctx context.Context, args ...string,
) (GuestExecResult, error) {
	if r.tcpAddr == "" {
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""}, fmt.Errorf("tcpExec: no tcp addr configured")
	}
	cctx, cancel := context.WithTimeout(ctx, timeoutTCPRPC)
	defer cancel()
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", r.tcpAddr)
	}
	if r.testTCPDialer != nil {
		dialer = r.testTCPDialer
	}
	conn, err := grpc.NewClient(
		"passthrough:///mwan-tcp",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		r.log.WarnContext(ctx, "tcp grpc client failed", "err", err)
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
			fmt.Errorf("tcp grpc client: %w", err)
	}
	defer func() { _ = conn.Close() }()

	cli := mwanv1.NewMWANAgentClient(conn)

	if len(args) == 0 {
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""}, fmt.Errorf("tcpExec: no args")
	}
	switch guestCmd(args[0]) {
	case guestCmdPing, guestCmdPing6:
		req := &mwanv1.PingRequest{
			Target:         pingTarget(args),
			BindInterface:  pingIface(args),
			Count:          pingCount(args, 2),
			TimeoutSeconds: 3,
		}
		resp, err := cli.Ping(cctx, req)
		if err != nil {
			r.log.WarnContext(ctx, "tcp ping failed", "err", err)
			return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
				fmt.Errorf("tcp ping: %w", err)
		}
		if resp.GetSuccess() {
			return GuestExecResult{ExitCode: 0, Stdout: "", Stderr: ""}, nil
		}
		return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""}, nil
	case guestCmdCat:
		if len(args) >= 2 && isLastDeployPath(args[1]) {
			resp, err := cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
			if err != nil {
				r.log.WarnContext(ctx, "tcp get config state failed", "err", err)
				return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
					fmt.Errorf("tcp get config state: %w", err)
			}
			ts := strconv.FormatInt(resp.GetLastDeployEpoch(), 10)
			return GuestExecResult{ExitCode: 0, Stdout: ts, Stderr: ""}, nil
		}
	}
	return GuestExecResult{ExitCode: 1, Stdout: "", Stderr: ""},
		fmt.Errorf("tcpExec: unhandled command %q", args[0])
}

func (r *RealOps) vsockGetConfigState(
	ctx context.Context,
) (*mwanv1.GetConfigStateResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutVsockRPC)
	defer cancel()
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		return vsock.Dial(r.vsockCID, r.vsockPort, nil)
	}
	if r.testGrpcDialer != nil {
		dialer = r.testGrpcDialer
	}
	conn, err := grpc.NewClient(
		"passthrough:///mwan",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		r.log.WarnContext(ctx, "vsock grpc client failed", "err", err)
		return nil, fmt.Errorf("vsock grpc client: %w", err)
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	res, err := cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
	if err != nil {
		r.log.WarnContext(ctx, "vsock get config state failed", "err", err)
		return nil, fmt.Errorf("vsock get config state: %w", err)
	}
	return res, nil
}

func (r *RealOps) tcpGetConfigState(
	ctx context.Context,
) (*mwanv1.GetConfigStateResponse, error) {
	if r.tcpAddr == "" {
		return nil, fmt.Errorf("tcpGetConfigState: no tcp addr configured")
	}
	cctx, cancel := context.WithTimeout(ctx, timeoutTCPRPC)
	defer cancel()
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", r.tcpAddr)
	}
	if r.testTCPDialer != nil {
		dialer = r.testTCPDialer
	}
	conn, err := grpc.NewClient(
		"passthrough:///mwan-tcp",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		r.log.ErrorContext(ctx, "tcp grpc client failed", "err", err)
		return nil, fmt.Errorf("tcp grpc client: %w", err)
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	res, err := cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
	if err != nil {
		r.log.ErrorContext(ctx, "tcp get config state failed", "err", err)
		return nil, fmt.Errorf("tcp get config state: %w", err)
	}
	return res, nil
}

// GetConfigState asks the guest agent what configuration it is running. It
// tries vsock first and falls back to the management TCP address, and the
// second return value names the channel that answered, so the caller can
// report which path is still working. Both attempts are recorded against the
// channel tracker whether they succeed or fail.
//
// vmid is accepted for symmetry with the rest of SysOps and is not used: both
// channels address the one guest this daemon watches.
func (r *RealOps) GetConfigState(
	ctx context.Context, vmid string,
) (*mwanv1.GetConfigStateResponse, string, error) {
	_ = vmid
	r.logAttemptStart(ctx, "get_config_state", ChanVsock, 1, vmid)
	res, err := r.vsockGetConfigState(ctx)
	if err == nil {
		r.tracker.recordSuccess(ChanVsock)
		r.logAttemptResult(ctx, "get_config_state", ChanVsock, 1, vmid, nil)
		return res, "vsock", nil
	}
	r.tracker.recordFailure(ChanVsock, err)
	r.logAttemptResult(ctx, "get_config_state", ChanVsock, 1, vmid, err)
	r.logAttemptStart(ctx, "get_config_state", ChanTCP, 2, vmid)
	res, err = r.tcpGetConfigState(ctx)
	if err == nil {
		r.tracker.recordSuccess(ChanTCP)
		r.logAttemptResult(ctx, "get_config_state", ChanTCP, 2, vmid, nil)
		return res, "tcp", nil
	}
	r.tracker.recordFailure(ChanTCP, err)
	r.logAttemptResult(ctx, "get_config_state", ChanTCP, 2, vmid, err)
	return nil, "", fmt.Errorf("GetConfigState: all channels failed")
}

// ---------------------------------------------------------------------------
// BGP route control: vsock -> TCP fallback (same pattern as GetConfigState)
// ---------------------------------------------------------------------------

// vsockAgent supports only the primary gateway because the failover container
// has no vsock endpoint.
func (r *RealOps) vsockAgent(
	ctx context.Context,
) (mwanv1.MWANAgentClient, func(), error) {
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		return vsock.Dial(r.vsockCID, r.vsockPort, nil)
	}
	if r.testGrpcDialer != nil {
		dialer = r.testGrpcDialer
	}
	conn, err := grpc.NewClient(
		"passthrough:///mwan",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		r.log.WarnContext(ctx, "vsock grpc client failed", "err", err)
		return nil, nil, fmt.Errorf("vsock grpc client: %w", err)
	}
	return mwanv1.NewMWANAgentClient(conn), func() { _ = conn.Close() }, nil
}

// tcpAgent requires an explicit address for the selected gateway.
func (r *RealOps) tcpAgent(
	ctx context.Context, addr string,
) (mwanv1.MWANAgentClient, func(), error) {
	if addr == "" {
		return nil, nil, fmt.Errorf("tcp agent: no tcp addr configured")
	}
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if r.testTCPDialer != nil {
		dialer = r.testTCPDialer
	}
	conn, err := grpc.NewClient(
		"passthrough:///mwan-tcp",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		r.log.ErrorContext(ctx, "tcp grpc client failed", "err", err)
		return nil, nil, fmt.Errorf("tcp grpc client: %w", err)
	}
	return mwanv1.NewMWANAgentClient(conn), func() { _ = conn.Close() }, nil
}

func (r *RealOps) vsockGetBGPStatus(
	ctx context.Context,
) (*mwanv1.GetBGPStatusResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutVsockRPC)
	defer cancel()
	cli, closeConn, err := r.vsockAgent(ctx)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	res, err := cli.GetBGPStatus(cctx, &mwanv1.GetBGPStatusRequest{})
	if err != nil {
		r.log.WarnContext(ctx, "vsock get bgp status failed", "err", err)
		return nil, fmt.Errorf("vsock get bgp status: %w", err)
	}
	return res, nil
}

func (r *RealOps) tcpGetBGPStatus(
	ctx context.Context, addr string,
) (*mwanv1.GetBGPStatusResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutTCPRPC)
	defer cancel()
	cli, closeConn, err := r.tcpAgent(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	res, err := cli.GetBGPStatus(cctx, &mwanv1.GetBGPStatusRequest{})
	if err != nil {
		r.log.ErrorContext(ctx, "tcp get bgp status failed", "err", err)
		return nil, fmt.Errorf("tcp get bgp status: %w", err)
	}
	return res, nil
}

// routeTarget rejects IDs outside the configured gateway pair to prevent
// calls to an unintended gateway.
func (r *RealOps) routeTarget(vmid string) (bool, error) {
	if vmid != "" && vmid == r.failoverVMID {
		return true, nil
	}
	if vmid != "" && vmid == r.primaryVMID {
		return false, nil
	}
	return false, fmt.Errorf(
		"vmid %q is neither the primary gateway %q nor the failover container %q",
		vmid, r.primaryVMID, r.failoverVMID,
	)
}

// routeResponse groups responses for operations that share gateway selection
// and channel handling.
type routeResponse interface {
	*mwanv1.GetBGPStatusResponse |
		*mwanv1.AnnounceRoutesResponse |
		*mwanv1.WithdrawRoutesResponse
}

// callFailoverAgent excludes container calls from the primary gateway channel
// tracker because container responses do not establish primary channel status.
func callFailoverAgent[T routeResponse](
	ctx context.Context,
	r *RealOps,
	operation, vmid string,
	call func(ctx context.Context, addr string) (T, error),
) (T, error) {
	r.logAttemptStart(ctx, operation, ChanTCP, 1, vmid)
	res, err := call(ctx, r.failoverTCPAddr)
	r.logAttemptResult(ctx, operation, ChanTCP, 1, vmid, err)
	return res, err
}

// GetBGPStatus omits the answering channel because no caller reports it.
func (r *RealOps) GetBGPStatus(
	ctx context.Context, vmid string,
) (*mwanv1.GetBGPStatusResponse, error) {
	isFailover, targetErr := r.routeTarget(vmid)
	if targetErr != nil {
		return nil, targetErr
	}
	if isFailover {
		return callFailoverAgent(ctx, r, "get_bgp_status", vmid, r.tcpGetBGPStatus)
	}
	r.logAttemptStart(ctx, "get_bgp_status", ChanVsock, 1, vmid)
	res, err := r.vsockGetBGPStatus(ctx)
	if err == nil {
		r.tracker.recordSuccess(ChanVsock)
		r.logAttemptResult(ctx, "get_bgp_status", ChanVsock, 1, vmid, nil)
		return res, nil
	}
	r.tracker.recordFailure(ChanVsock, err)
	r.logAttemptResult(ctx, "get_bgp_status", ChanVsock, 1, vmid, err)
	r.logAttemptStart(ctx, "get_bgp_status", ChanTCP, 2, vmid)
	res, err = r.tcpGetBGPStatus(ctx, r.tcpAddr)
	if err == nil {
		r.tracker.recordSuccess(ChanTCP)
		r.logAttemptResult(ctx, "get_bgp_status", ChanTCP, 2, vmid, nil)
		return res, nil
	}
	r.tracker.recordFailure(ChanTCP, err)
	r.logAttemptResult(ctx, "get_bgp_status", ChanTCP, 2, vmid, err)
	return nil, fmt.Errorf("GetBGPStatus: all channels failed")
}

func (r *RealOps) vsockAnnounceRoutes(
	ctx context.Context,
) (*mwanv1.AnnounceRoutesResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutVsockRPC)
	defer cancel()
	cli, closeConn, err := r.vsockAgent(ctx)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	res, err := cli.AnnounceRoutes(cctx, &mwanv1.AnnounceRoutesRequest{})
	if err != nil {
		r.log.WarnContext(ctx, "vsock announce routes failed", "err", err)
		return nil, fmt.Errorf("vsock announce routes: %w", err)
	}
	return res, nil
}

func (r *RealOps) tcpAnnounceRoutes(
	ctx context.Context, addr string,
) (*mwanv1.AnnounceRoutesResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutTCPRPC)
	defer cancel()
	cli, closeConn, err := r.tcpAgent(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	res, err := cli.AnnounceRoutes(cctx, &mwanv1.AnnounceRoutesRequest{})
	if err != nil {
		r.log.ErrorContext(ctx, "tcp announce routes failed", "err", err)
		return nil, fmt.Errorf("tcp announce routes: %w", err)
	}
	return res, nil
}

// AnnounceRoutes does not retry a reported failure on the other channel
// because both channels call the same primary gateway agent.
func (r *RealOps) AnnounceRoutes(ctx context.Context, vmid string) error {
	isFailover, targetErr := r.routeTarget(vmid)
	if targetErr != nil {
		return targetErr
	}
	if isFailover {
		res, err := callFailoverAgent(ctx, r, "announce_routes", vmid, r.tcpAnnounceRoutes)
		if err != nil {
			return err
		}
		if !res.GetSuccess() {
			return fmt.Errorf("AnnounceRoutes: agent returned error: %s", res.GetError())
		}
		return nil
	}
	r.logAttemptStart(ctx, "announce_routes", ChanVsock, 1, vmid)
	res, err := r.vsockAnnounceRoutes(ctx)
	if err == nil {
		r.tracker.recordSuccess(ChanVsock)
		r.logAttemptResult(ctx, "announce_routes", ChanVsock, 1, vmid, nil)
		if !res.GetSuccess() {
			return fmt.Errorf("AnnounceRoutes: agent returned error: %s", res.GetError())
		}
		return nil
	}
	r.tracker.recordFailure(ChanVsock, err)
	r.logAttemptResult(ctx, "announce_routes", ChanVsock, 1, vmid, err)
	r.logAttemptStart(ctx, "announce_routes", ChanTCP, 2, vmid)
	res, err = r.tcpAnnounceRoutes(ctx, r.tcpAddr)
	if err == nil {
		r.tracker.recordSuccess(ChanTCP)
		r.logAttemptResult(ctx, "announce_routes", ChanTCP, 2, vmid, nil)
		if !res.GetSuccess() {
			return fmt.Errorf("AnnounceRoutes: agent returned error: %s", res.GetError())
		}
		return nil
	}
	r.tracker.recordFailure(ChanTCP, err)
	r.logAttemptResult(ctx, "announce_routes", ChanTCP, 2, vmid, err)
	return fmt.Errorf("AnnounceRoutes: all channels failed")
}

func (r *RealOps) vsockWithdrawRoutes(
	ctx context.Context,
) (*mwanv1.WithdrawRoutesResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutVsockRPC)
	defer cancel()
	cli, closeConn, err := r.vsockAgent(ctx)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	res, err := cli.WithdrawRoutes(cctx, &mwanv1.WithdrawRoutesRequest{})
	if err != nil {
		r.log.WarnContext(ctx, "vsock withdraw routes failed", "err", err)
		return nil, fmt.Errorf("vsock withdraw routes: %w", err)
	}
	return res, nil
}

func (r *RealOps) tcpWithdrawRoutes(
	ctx context.Context, addr string,
) (*mwanv1.WithdrawRoutesResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutTCPRPC)
	defer cancel()
	cli, closeConn, err := r.tcpAgent(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	res, err := cli.WithdrawRoutes(cctx, &mwanv1.WithdrawRoutesRequest{})
	if err != nil {
		r.log.ErrorContext(ctx, "tcp withdraw routes failed", "err", err)
		return nil, fmt.Errorf("tcp withdraw routes: %w", err)
	}
	return res, nil
}

// WithdrawRoutes does not retry a reported failure on the other channel
// because both channels call the same primary gateway agent.
func (r *RealOps) WithdrawRoutes(ctx context.Context, vmid string) error {
	isFailover, targetErr := r.routeTarget(vmid)
	if targetErr != nil {
		return targetErr
	}
	if isFailover {
		res, err := callFailoverAgent(ctx, r, "withdraw_routes", vmid, r.tcpWithdrawRoutes)
		if err != nil {
			return err
		}
		if !res.GetSuccess() {
			return fmt.Errorf("WithdrawRoutes: agent returned error: %s", res.GetError())
		}
		return nil
	}
	r.logAttemptStart(ctx, "withdraw_routes", ChanVsock, 1, vmid)
	res, err := r.vsockWithdrawRoutes(ctx)
	if err == nil {
		r.tracker.recordSuccess(ChanVsock)
		r.logAttemptResult(ctx, "withdraw_routes", ChanVsock, 1, vmid, nil)
		if !res.GetSuccess() {
			return fmt.Errorf("WithdrawRoutes: agent returned error: %s", res.GetError())
		}
		return nil
	}
	r.tracker.recordFailure(ChanVsock, err)
	r.logAttemptResult(ctx, "withdraw_routes", ChanVsock, 1, vmid, err)
	r.logAttemptStart(ctx, "withdraw_routes", ChanTCP, 2, vmid)
	res, err = r.tcpWithdrawRoutes(ctx, r.tcpAddr)
	if err == nil {
		r.tracker.recordSuccess(ChanTCP)
		r.logAttemptResult(ctx, "withdraw_routes", ChanTCP, 2, vmid, nil)
		if !res.GetSuccess() {
			return fmt.Errorf("WithdrawRoutes: agent returned error: %s", res.GetError())
		}
		return nil
	}
	r.tracker.recordFailure(ChanTCP, err)
	r.logAttemptResult(ctx, "withdraw_routes", ChanTCP, 2, vmid, err)
	return fmt.Errorf("WithdrawRoutes: all channels failed")
}

// Ping runs the host probe binary (typically `ping` or `ping6`) against
// target with a 2-packet count and 3-second per-packet timeout, capped by
// timeoutHostProbe. It returns true when the binary exits 0.
func (r *RealOps) Ping(ctx context.Context, bin, target string) bool {
	r.log.DebugContext(ctx, "ops: Ping", "bin", bin, "target", target)
	cctx, cancel := context.WithTimeout(ctx, timeoutHostProbe)
	defer cancel()
	return exec.CommandContext(cctx, bin, "-c", "2", "-W", "3", target).Run() == nil
}

func (r *RealOps) attemptLogger(
	ctx context.Context,
	operation string,
	channel ChannelName,
	attempt int,
) *slog.Logger {
	attemptCtx := tracing.WithOperation(ctx, operation)
	attemptCtx = tracing.WithAttempt(attemptCtx, attempt)
	attemptCtx = tracing.WithAttrs(attemptCtx,
		slog.String("channel", string(channel)),
	)
	return tracing.Logger(attemptCtx, r.log)
}

func (r *RealOps) logAttemptStart(
	ctx context.Context,
	operation string,
	channel ChannelName,
	attempt int,
	vmid string,
) {
	r.attemptLogger(ctx, operation, channel, attempt).Info(
		"ops transport attempt",
		"vmid", vmid,
	)
}

func (r *RealOps) logAttemptResult(
	ctx context.Context,
	operation string,
	channel ChannelName,
	attempt int,
	vmid string,
	err error,
) {
	log := r.attemptLogger(ctx, operation, channel, attempt)
	if err != nil {
		log.WarnContext(ctx, "ops transport failed", "vmid", vmid, "err", err)
		return
	}
	log.InfoContext(ctx, "ops transport succeeded", "vmid", vmid)
}

// ---------------------------------------------------------------------------
// ping arg helpers (parse argv-style ping arguments for vsock translation)
// ---------------------------------------------------------------------------

func pingTarget(args []string) string {
	for i, a := range args {
		if a == "-I" || a == "-c" || a == "-W" {
			i++
			_ = i
			continue
		}
		if !strings.HasPrefix(a, "-") && i > 0 {
			return a
		}
	}
	if len(args) > 0 {
		return args[len(args)-1]
	}
	return ""
}

func pingIface(args []string) string {
	for i, a := range args {
		if a == "-I" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// pingCount reads the count from a `-c N` argument pair, returning def when
// there is none. Parsing at 32 bits rejects a value too large for the request
// field, so an out-of-range count falls back to def instead of truncating to
// an unrelated number.
func pingCount(args []string, def int32) int32 {
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			n, err := strconv.ParseInt(args[i+1], 10, 32)
			if err == nil {
				return int32(n)
			}
		}
	}
	return def
}

// ExtractTracker returns the internal channel tracker for testing or diagnostics.
func (r *RealOps) ExtractTracker() *ChannelTracker {
	return r.tracker
}
