package ops

import (
	"context"
	"errors"
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
	"goodkind.io/mwan/pkg/pveapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	TimeoutQmStatus       = 10 * time.Second
	timeoutQmGuestExec    = 30 * time.Second
	TimeoutQmStop         = 60 * time.Second
	TimeoutQmStart        = 60 * time.Second
	timeoutQmListSnapshot = 10 * time.Second
	timeoutQmAgentFreeze  = 30 * time.Second
	timeoutHostProbe      = 20 * time.Second
	timeoutVsockRPC       = 15 * time.Second
	timeoutTCPRPC         = 15 * time.Second
	timeoutPVEExec        = 45 * time.Second
)

// ErrGuestExecUnavailable is returned by pveExec when the PVE client is
// not configured (missing token). Callers can distinguish this from a
// command that ran and returned a non-zero exit code.
var ErrGuestExecUnavailable = errors.New("pve client not configured (no PVE_TOKEN_ID)")

// guestCmd enumerates the argv[0] commands the in-guest gRPC adapter
// translates from `GuestExec` argv into typed RPCs.
type guestCmd string

const (
	guestCmdPing  guestCmd = "ping"
	guestCmdPing6 guestCmd = "ping6"
	guestCmdCat   guestCmd = "cat"
)

// ---------------------------------------------------------------------------
// SysOps: interface for all external dependencies
// ---------------------------------------------------------------------------

type GuestExecResult struct {
	ExitCode int
	Stdout   string
}

type SysOps interface {
	VMStatus(ctx context.Context, vmid string) (bool, error)
	VMStop(ctx context.Context, vmid string) error
	VMRollback(ctx context.Context, vmid, snap string) error
	VMStart(ctx context.Context, vmid string) error
	VMSnapshots(ctx context.Context, vmid string) ([]byte, error)
	VMSnapshot(ctx context.Context, vmid, snapName string) error
	VMDelSnapshot(ctx context.Context, vmid, snapName string) error
	VMDelSnapshotForce(ctx context.Context, vmid, snapName string) error
	VMLock(ctx context.Context, vmid string) (string, error)
	VMUnlock(ctx context.Context, vmid string) error
	VMHasRunningTask(ctx context.Context, vmid string) (bool, error)
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
	VMFSFreezeStatus(ctx context.Context, vmid string) (string, error)
	VMFSFreezeThaw(ctx context.Context, vmid string) error
}

// ---------------------------------------------------------------------------
// RealOps: gRPC-over-vsock primary, PVE REST fallback, qm lifecycle
// ---------------------------------------------------------------------------

type RealOps struct {
	log       *slog.Logger
	pve       *pveapi.Client
	vsockCID  uint32
	vsockPort uint32
	pveNode   string
	nc        config.NetworkConfig

	// testVsockOverride, if set, replaces vsockExec inside GuestExec (unit tests only).
	testVsockOverride func(
		ctx context.Context, args ...string,
	) (GuestExecResult, error)

	// testGrpcDialer, if set, replaces vsock.Dial in vsockExec (unit tests only).
	testGrpcDialer func(ctx context.Context, addr string) (net.Conn, error)

	tcpAddr string
	tracker *ChannelTracker

	// testTCPDialer, if set, replaces net.Dial in tcpExec (unit tests only).
	testTCPDialer func(ctx context.Context, addr string) (net.Conn, error)
}

func NewRealOps(
	cfg *config.Config,
	logger *slog.Logger,
) *RealOps {
	var pveClient *pveapi.Client
	if cfg.PVE.TokenID != "" && cfg.PVE.TokenSecret != "" {
		pveClient = pveapi.NewClient(
			cfg.PVE.BaseURL,
			cfg.PVE.TokenID,
			cfg.PVE.TokenSecret,
		)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RealOps{
		log:       logger.With("component", "ops"),
		pve:       pveClient,
		vsockCID:  cfg.Watchdog.VsockCID,
		vsockPort: cfg.Watchdog.VsockPort,
		pveNode:   cfg.PVE.Node,
		nc:        cfg.Network,
		tcpAddr:   cfg.Watchdog.MwanAgentTCPAddr,
		tracker:   NewChannelTracker(),
	}
}

// runQm wraps qm with a context-bound timeout.
func runQm(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	slog.DebugContext(ctx, "ops: runQm", "args", args, "timeout", timeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return exec.CommandContext(cctx, "qm", args...).CombinedOutput()
}

// VMStatus reports whether the VM with the given vmid is currently running
// according to `qm status`.
func (r *RealOps) VMStatus(ctx context.Context, vmid string) (bool, error) {
	out, err := runQm(ctx, TimeoutQmStatus, "status", vmid)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "running"), nil
}

// VMStop stops the VM with the given vmid via `qm stop --timeout 30`.
func (r *RealOps) VMStop(ctx context.Context, vmid string) error {
	_, err := runQm(ctx, TimeoutQmStop, "stop", vmid, "--timeout", "30")
	return err
}

// VMStart starts the VM with the given vmid via `qm start`.
func (r *RealOps) VMStart(ctx context.Context, vmid string) error {
	_, err := runQm(ctx, TimeoutQmStart, "start", vmid)
	return err
}

// VMSnapshots returns the raw output of `qm listsnapshot` for the given vmid.
func (r *RealOps) VMSnapshots(ctx context.Context, vmid string) ([]byte, error) {
	return runQm(ctx, timeoutQmListSnapshot, "listsnapshot", vmid)
}

// VMFSFreezeStatus reports the guest agent's filesystem freeze state via
// `qm agent fsfreeze-status` ("thawed" or "frozen"). Snapshots freeze the
// guest through the agent, and a thaw that never lands leaves the guest
// wedged with guest-exec disabled, so the watchdog checks this after every
// snapshot and at startup.
func (r *RealOps) VMFSFreezeStatus(ctx context.Context, vmid string) (string, error) {
	out, err := runQm(ctx, timeoutQmAgentFreeze, "agent", vmid, "fsfreeze-status")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// VMFSFreezeThaw releases a stuck filesystem freeze via
// `qm agent fsfreeze-thaw`.
func (r *RealOps) VMFSFreezeThaw(ctx context.Context, vmid string) error {
	_, err := runQm(ctx, timeoutQmAgentFreeze, "agent", vmid, "fsfreeze-thaw")
	return err
}

// GuestExec tries all three channels in order: vsock -> TCP/mgmt -> PVE REST.
// Each channel's result is recorded in the channelTracker regardless of outcome.
func (r *RealOps) GuestExec(
	ctx context.Context, vmid string, args ...string,
) (GuestExecResult, error) {
	// Allow unit test overrides to bypass the real transport layer.
	if r.testVsockOverride != nil {
		res, err := r.testVsockOverride(ctx, args...)
		if err == nil {
			return res, nil
		}
		return r.pveExec(ctx, vmid, args...)
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

	// Channel 3: PVE REST API fallback
	r.logAttemptStart(ctx, "guest_exec", ChanPVE, 3, vmid)
	pveRes, pveErr := r.pveExec(ctx, vmid, args...)
	if pveErr == nil {
		r.tracker.recordSuccess(ChanPVE)
		r.logAttemptResult(ctx, "guest_exec", ChanPVE, 3, vmid, nil)
	} else {
		r.tracker.recordFailure(ChanPVE, pveErr)
		r.logAttemptResult(ctx, "guest_exec", ChanPVE, 3, vmid, pveErr)
	}
	return pveRes, pveErr
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
		return GuestExecResult{ExitCode: 1}, err
	}
	defer func() { _ = conn.Close() }()

	cli := mwanv1.NewMWANAgentClient(conn)

	if len(args) == 0 {
		return GuestExecResult{ExitCode: 1}, fmt.Errorf("vsockExec: no args")
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
			return GuestExecResult{ExitCode: 1}, err
		}
		if resp.GetSuccess() {
			return GuestExecResult{ExitCode: 0}, nil
		}
		return GuestExecResult{ExitCode: 1}, nil
	case guestCmdCat:
		if len(args) >= 2 && isLastDeployPath(args[1]) {
			resp, err := cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
			if err != nil {
				return GuestExecResult{ExitCode: 1}, err
			}
			ts := strconv.FormatInt(resp.GetLastDeployEpoch(), 10)
			return GuestExecResult{ExitCode: 0, Stdout: ts}, nil
		}
	}
	return GuestExecResult{ExitCode: 1},
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
		return GuestExecResult{ExitCode: 1}, fmt.Errorf("tcpExec: no tcp addr configured")
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
		return GuestExecResult{ExitCode: 1}, err
	}
	defer func() { _ = conn.Close() }()

	cli := mwanv1.NewMWANAgentClient(conn)

	if len(args) == 0 {
		return GuestExecResult{ExitCode: 1}, fmt.Errorf("tcpExec: no args")
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
			return GuestExecResult{ExitCode: 1}, err
		}
		if resp.GetSuccess() {
			return GuestExecResult{ExitCode: 0}, nil
		}
		return GuestExecResult{ExitCode: 1}, nil
	case guestCmdCat:
		if len(args) >= 2 && isLastDeployPath(args[1]) {
			resp, err := cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
			if err != nil {
				return GuestExecResult{ExitCode: 1}, err
			}
			ts := strconv.FormatInt(resp.GetLastDeployEpoch(), 10)
			return GuestExecResult{ExitCode: 0, Stdout: ts}, nil
		}
	}
	return GuestExecResult{ExitCode: 1},
		fmt.Errorf("tcpExec: unhandled command %q", args[0])
}

func (r *RealOps) pveExec(
	ctx context.Context, vmid string, args ...string,
) (GuestExecResult, error) {
	if r.pve == nil {
		return GuestExecResult{ExitCode: 1}, ErrGuestExecUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, timeoutPVEExec)
	defer cancel()
	pid, err := r.pve.GuestExec(cctx, r.pveNode, vmid, args)
	if err != nil {
		return GuestExecResult{ExitCode: 1}, err
	}
	code, stdout, _, err := r.pve.GuestExecStatus(cctx, r.pveNode, vmid, pid)
	if err != nil {
		return GuestExecResult{ExitCode: 1}, err
	}
	return GuestExecResult{ExitCode: code, Stdout: stdout}, nil
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.GetConfigState(cctx, &mwanv1.GetConfigStateRequest{})
}

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

func (r *RealOps) vsockGetBGPStatus(
	ctx context.Context,
) (*mwanv1.GetBGPStatusResponse, error) {
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.GetBGPStatus(cctx, &mwanv1.GetBGPStatusRequest{})
}

func (r *RealOps) tcpGetBGPStatus(
	ctx context.Context,
) (*mwanv1.GetBGPStatusResponse, error) {
	if r.tcpAddr == "" {
		return nil, fmt.Errorf("tcpGetBGPStatus: no tcp addr configured")
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.GetBGPStatus(cctx, &mwanv1.GetBGPStatusRequest{})
}

func (r *RealOps) GetBGPStatus(
	ctx context.Context, vmid string,
) (*mwanv1.GetBGPStatusResponse, error) {
	_ = vmid
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
	res, err = r.tcpGetBGPStatus(ctx)
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.AnnounceRoutes(cctx, &mwanv1.AnnounceRoutesRequest{})
}

func (r *RealOps) tcpAnnounceRoutes(
	ctx context.Context,
) (*mwanv1.AnnounceRoutesResponse, error) {
	if r.tcpAddr == "" {
		return nil, fmt.Errorf("tcpAnnounceRoutes: no tcp addr configured")
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.AnnounceRoutes(cctx, &mwanv1.AnnounceRoutesRequest{})
}

func (r *RealOps) AnnounceRoutes(ctx context.Context, vmid string) error {
	_ = vmid
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
	res, err = r.tcpAnnounceRoutes(ctx)
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.WithdrawRoutes(cctx, &mwanv1.WithdrawRoutesRequest{})
}

func (r *RealOps) tcpWithdrawRoutes(
	ctx context.Context,
) (*mwanv1.WithdrawRoutesResponse, error) {
	if r.tcpAddr == "" {
		return nil, fmt.Errorf("tcpWithdrawRoutes: no tcp addr configured")
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
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cli := mwanv1.NewMWANAgentClient(conn)
	return cli.WithdrawRoutes(cctx, &mwanv1.WithdrawRoutesRequest{})
}

func (r *RealOps) WithdrawRoutes(ctx context.Context, vmid string) error {
	_ = vmid
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
	res, err = r.tcpWithdrawRoutes(ctx)
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

func pingCount(args []string, def int32) int32 {
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			n, err := strconv.Atoi(args[i+1])
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

// ---------------------------------------------------------------------------
// DryRunOps: wraps SysOps, logs destructive operations instead of executing
// ---------------------------------------------------------------------------

type DryRunOps struct {
	inner SysOps
	log   *slog.Logger
}

// NewDryRunOps creates a new dry-run wrapper.
func NewDryRunOps(inner SysOps, log *slog.Logger) *DryRunOps {
	return &DryRunOps{inner: inner, log: log}
}

func (d *DryRunOps) VMStatus(ctx context.Context, vmid string) (bool, error) {
	return d.inner.VMStatus(ctx, vmid)
}

func (d *DryRunOps) VMStop(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would stop VM", "vmid", vmid)
	return nil
}

func (d *DryRunOps) VMRollback(ctx context.Context, vmid, snap string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would rollback VM", "vmid", vmid, "snapshot", snap)
	return nil
}

func (d *DryRunOps) VMStart(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would start VM", "vmid", vmid)
	return nil
}

func (d *DryRunOps) VMSnapshots(ctx context.Context, vmid string) ([]byte, error) {
	return d.inner.VMSnapshots(ctx, vmid)
}

func (d *DryRunOps) VMSnapshot(ctx context.Context, vmid, snapName string) error {
	d.log.InfoContext(
		ctx,
		"[DRY-RUN] would snapshot VM",
		"vmid", vmid,
		"snapshot", snapName,
	)
	return nil
}

func (d *DryRunOps) VMDelSnapshot(ctx context.Context, vmid, snapName string) error {
	d.log.InfoContext(
		ctx,
		"[DRY-RUN] would delete snapshot",
		"vmid", vmid,
		"snapshot", snapName,
	)
	return nil
}

func (d *DryRunOps) GuestExec(
	ctx context.Context, vmid string, args ...string,
) (GuestExecResult, error) {
	return d.inner.GuestExec(ctx, vmid, args...)
}

func (d *DryRunOps) Ping(ctx context.Context, bin, target string) bool {
	return d.inner.Ping(ctx, bin, target)
}

func (d *DryRunOps) GetConfigState(
	ctx context.Context, vmid string,
) (*mwanv1.GetConfigStateResponse, string, error) {
	return d.inner.GetConfigState(ctx, vmid)
}

func (d *DryRunOps) GetBGPStatus(
	ctx context.Context, vmid string,
) (*mwanv1.GetBGPStatusResponse, error) {
	d.log.InfoContext(ctx, "[DRY-RUN] would get BGP status", "vmid", vmid)
	return &mwanv1.GetBGPStatusResponse{}, nil
}

func (d *DryRunOps) AnnounceRoutes(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would announce BGP routes", "vmid", vmid)
	return nil
}

func (d *DryRunOps) WithdrawRoutes(ctx context.Context, vmid string) error {
	d.log.InfoContext(ctx, "[DRY-RUN] would withdraw BGP routes", "vmid", vmid)
	return nil
}
