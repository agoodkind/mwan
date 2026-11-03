package watchdog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/alert"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/statuspush"
	"goodkind.io/mwan/internal/watchdogconfig"
)

// ---------------------------------------------------------------------------
// mockOps
// ---------------------------------------------------------------------------

type vmRollbackCall struct {
	VMID string
	Snap string
}

type mockOps struct {
	mu sync.Mutex

	vmRunning     bool
	vmStartErr    error
	vmStopErr     error
	vmStatusErr   error
	vmSnapErr     error
	vmRollbackErr error
	pingResults   map[string]bool
	guestResults  map[string]ops.GuestExecResult
	snapshotsOut  []byte

	// bgpStatusByVMID seeds GetBGPStatus responses keyed by the vmid argument.
	// Defaults to an empty response (not established) if the key is missing.
	bgpStatusByVMID map[string]*mwanv1.GetBGPStatusResponse

	// freezeStatus seeds VMFSFreezeStatus; VMFSFreezeThaw flips it to
	// "thawed" so a guard that thaws observes the recovery on recheck.
	// thawErr fails the thaw call; thawKeepsFrozen makes the thaw return
	// success while the guest still reports frozen on recheck.
	freezeStatus    string
	freezeStatusErr error
	thawErr         error
	thawKeepsFrozen bool

	vmStatusCalls       int
	vmStartCalls        int
	vmStopCalls         int
	vmSnapshotsCalls    int
	freezeStatusCalls   int
	thawCalls           int
	pingCalls           []string
	guestCalls          []string
	vmRollbackCalls     []vmRollbackCall
	announceRoutesCalls []string
	withdrawRoutesCalls []string
	getBGPStatusCalls   []string

	// Snapshot cleanup surface. delSnapshotErr fails every plain delete;
	// forceDelSnapshotErr fails every forced one. guestLock is the lock
	// the guest reports, cleared by a successful VMUnlock.
	delSnapshotCalls      []string
	delSnapshotErr        error
	forceDelSnapshotCalls []string
	forceDelSnapshotErr   error
	guestLock             string
	guestLockErr          error
	unlockCalls           int
	unlockErr             error
	taskRunning           bool
	taskRunningErr        error
}

func (m *mockOps) VMFSFreezeStatus(ctx context.Context, vmid string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.freezeStatusCalls++
	if m.freezeStatusErr != nil {
		return "", m.freezeStatusErr
	}
	if m.freezeStatus == "" {
		return "thawed", nil
	}
	return m.freezeStatus, nil
}

func (m *mockOps) VMFSFreezeThaw(ctx context.Context, vmid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thawCalls++
	if m.thawErr != nil {
		return m.thawErr
	}
	if !m.thawKeepsFrozen {
		m.freezeStatus = "thawed"
	}
	return nil
}

func (m *mockOps) VMStatus(ctx context.Context, vmid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vmStatusCalls++
	if m.vmStatusErr != nil {
		return false, m.vmStatusErr
	}
	return m.vmRunning, nil
}

func (m *mockOps) GetConfigState(ctx context.Context, vmid string) (*mwanv1.GetConfigStateResponse, string, error) {
	return &mwanv1.GetConfigStateResponse{}, "", nil
}

func (m *mockOps) GetBGPStatus(ctx context.Context, vmid string) (*mwanv1.GetBGPStatusResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getBGPStatusCalls = append(m.getBGPStatusCalls, vmid)
	if m.bgpStatusByVMID != nil {
		if resp, ok := m.bgpStatusByVMID[vmid]; ok {
			return resp, nil
		}
	}
	return &mwanv1.GetBGPStatusResponse{}, nil
}

func (m *mockOps) AnnounceRoutes(ctx context.Context, vmid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.announceRoutesCalls = append(m.announceRoutesCalls, vmid)
	return nil
}

func (m *mockOps) WithdrawRoutes(ctx context.Context, vmid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.withdrawRoutesCalls = append(m.withdrawRoutesCalls, vmid)
	return nil
}

func (m *mockOps) VMStart(ctx context.Context, vmid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vmStartCalls++
	return m.vmStartErr
}

func (m *mockOps) VMStop(ctx context.Context, vmid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vmStopCalls++
	return m.vmStopErr
}

func (m *mockOps) VMSnapshots(ctx context.Context, vmid string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vmSnapshotsCalls++
	if m.vmSnapErr != nil {
		return nil, m.vmSnapErr
	}
	return m.snapshotsOut, nil
}

func (m *mockOps) VMSnapshot(ctx context.Context, vmid, snapname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vmSnapErr != nil {
		return m.vmSnapErr
	}
	return nil
}

func (m *mockOps) VMDelSnapshot(ctx context.Context, vmid, snapname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delSnapshotCalls = append(m.delSnapshotCalls, snapname)
	return m.delSnapshotErr
}

func (m *mockOps) VMDelSnapshotForce(ctx context.Context, vmid, snapname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forceDelSnapshotCalls = append(m.forceDelSnapshotCalls, snapname)
	return m.forceDelSnapshotErr
}

func (m *mockOps) VMLock(ctx context.Context, vmid string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.guestLockErr != nil {
		return "", m.guestLockErr
	}
	return m.guestLock, nil
}

func (m *mockOps) VMUnlock(ctx context.Context, vmid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unlockCalls++
	if m.unlockErr != nil {
		return m.unlockErr
	}
	m.guestLock = ""
	return nil
}

func (m *mockOps) VMHasRunningTask(ctx context.Context, vmid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.taskRunningErr != nil {
		return false, m.taskRunningErr
	}
	return m.taskRunning, nil
}

func (m *mockOps) VMRollback(ctx context.Context, vmid, snapname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vmRollbackCalls = append(m.vmRollbackCalls, vmRollbackCall{VMID: vmid, Snap: snapname})
	return m.vmRollbackErr
}

func (m *mockOps) Ping(ctx context.Context, cmd string, target string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := cmd + ":" + target
	m.pingCalls = append(m.pingCalls, key)
	if m.pingResults != nil {
		if res, ok := m.pingResults[key]; ok {
			return res
		}
	}
	return false
}

func (m *mockOps) GuestExec(ctx context.Context, vmid string, cmd ...string) (ops.GuestExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.Join(append([]string{vmid}, cmd...), "|")
	m.guestCalls = append(m.guestCalls, key)
	if m.guestResults != nil {
		if res, ok := m.guestResults[key]; ok {
			return res, nil
		}
	}
	return ops.GuestExecResult{}, nil
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func testNC() watchdogconfig.NetworkConfig {
	return watchdogconfig.NetworkConfig{
		PingTargetIPv4: "1.1.1.1",
		PingTargetIPv6: "2606:4700:4700::1111",
		PingTargets:    []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
		CurlTarget:     "https://ifconfig.co/ip",
		LastDeployPath: "/var/lib/mwan/last-deploy",
		LastChangePath: "/var/run/mwan-last-change",
	}
}

func newTestWatchdog(
	t *testing.T, ops ops.SysOps, cfgOverrides ...func(*watchdogconfig.Config),
) *watchdog {
	t.Helper()
	tmp := t.TempDir()
	cfg := &watchdogconfig.Config{
		MwanVMID: "113",
		Email: notify.EmailConfig{
			AlertEmail: "test@test.com",
		},
		Network: testNC(),
		Watchdog: watchdogconfig.WatchdogSection{
			DeployWindowMinutes:        30,
			ConnectivityTimeoutSeconds: 0,
			CheckIntervalHealthy:       0,
			CheckIntervalDegraded:      0,
			PostRollbackGraceSeconds:   0,
			LogFile:                    filepath.Join(tmp, "watchdog.log"),
			RollbackStateFile:          filepath.Join(tmp, "rollback.state"),
			RollbackLockFile:           filepath.Join(tmp, "rollback.lock"),
			AlertCooldownSeconds:       0,
			MaxIterations:              5,
		},
	}
	for _, fn := range cfgOverrides {
		fn(cfg)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := &watchdog{
		cfg:         cfg,
		ops:         ops,
		notify:      notify.NullNotifier{},
		log:         logger,
		exitFn:      os.Exit,
		coord:       &alert.Coord{},
		configState: newConfigStateSource(cfg, ops, logger),

		connectivityLossObservedAt: map[string]time.Time{},
	}
	return w
}

// fixedStatus is a gateway verdict already received. The listener's own
// round-trip is proven in the statuspush package; here the question is what a
// diagnosis does with a verdict it holds.
type fixedStatus struct {
	status     statuspush.Status
	receivedAt time.Time
	seen       bool
}

func (f fixedStatus) Latest() (statuspush.Status, time.Time, bool) {
	return f.status, f.receivedAt, f.seen
}

// TestDiagnosisLogsThePushedStatus is what replaces the per-interface pings: a
// diagnosis reports every provider's verdict and the age of that report, from
// what the gateway pushed, and it names AT&T, which the hand-typed interface
// list never did.
func TestDiagnosisLogsThePushedStatus(t *testing.T) {
	var logged bytes.Buffer
	mock := &mockOps{}
	w := newTestWatchdog(t, mock)
	w.log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	now := time.Date(2026, 9, 2, 12, 0, 30, 0, time.UTC)
	w.nowFn = func() time.Time { return now }
	w.status = fixedStatus{
		status: statuspush.Status{
			SentAt:     now.Add(-20 * time.Second),
			ActiveTier: 1,
			Providers: map[string]string{
				"att":          "unhealthy",
				"webpass":      "unhealthy",
				"monkeybrains": "healthy",
			},
		},
		receivedAt: now.Add(-20 * time.Second),
		seen:       true,
	}

	w.logPushedStatus(context.Background())

	output := logged.String()
	for _, want := range []string{
		"Gateway provider status",
		"att=unhealthy",
		"webpass=unhealthy",
		"monkeybrains=healthy",
		"active_tier=1",
		"age=20s",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diagnosis log missing %q\nfull log:\n%s", want, output)
		}
	}
}

// TestDiagnosisSaysSoWithNoPushedStatus keeps the silent case legible: a
// gateway that has never pushed must read as silent, not as healthy.
func TestDiagnosisSaysSoWithNoPushedStatus(t *testing.T) {
	var logged bytes.Buffer
	w := newTestWatchdog(t, &mockOps{})
	w.log = slog.New(slog.NewTextHandler(&logged, nil))
	w.status = fixedStatus{
		status:     statuspush.Status{},
		receivedAt: time.Time{},
		seen:       false,
	}

	w.logPushedStatus(context.Background())

	if !strings.Contains(logged.String(), "No gateway status received yet") {
		t.Fatalf("silent gateway not reported\nfull log:\n%s", logged.String())
	}
}

func TestEnsureGuestThawedRecoversStuckFreeze(t *testing.T) {
	mock := &mockOps{freezeStatus: "frozen"}
	w := newTestWatchdog(t, mock)

	w.ensureGuestThawed(context.Background(), "test")

	if mock.thawCalls != 1 {
		t.Fatalf("thawCalls = %d, want 1", mock.thawCalls)
	}
	if mock.freezeStatus != "thawed" {
		t.Fatalf("freezeStatus = %q, want thawed", mock.freezeStatus)
	}
}

func TestEnsureGuestThawedNoopWhenThawed(t *testing.T) {
	mock := &mockOps{freezeStatus: "thawed"}
	w := newTestWatchdog(t, mock)

	w.ensureGuestThawed(context.Background(), "test")

	if mock.thawCalls != 0 {
		t.Fatalf("thawCalls = %d, want 0", mock.thawCalls)
	}
}

func TestEnsureGuestThawedToleratesUnreachableAgent(t *testing.T) {
	mock := &mockOps{freezeStatusErr: errors.New("guest agent is not running")}
	w := newTestWatchdog(t, mock)

	w.ensureGuestThawed(context.Background(), "test")

	if mock.thawCalls != 0 {
		t.Fatalf("thawCalls = %d, want 0", mock.thawCalls)
	}
}

func TestEnsureGuestThawedSkipsRecheckWhenThawFails(t *testing.T) {
	mock := &mockOps{
		freezeStatus: "frozen",
		thawErr:      errors.New("guest agent timeout"),
	}
	w := newTestWatchdog(t, mock)

	w.ensureGuestThawed(context.Background(), "test")

	if mock.thawCalls != 1 {
		t.Fatalf("thawCalls = %d, want 1", mock.thawCalls)
	}
	if mock.freezeStatusCalls != 1 {
		t.Fatalf("freezeStatusCalls = %d, want 1: no recheck after a failed thaw", mock.freezeStatusCalls)
	}
	if mock.freezeStatus != "frozen" {
		t.Fatalf("freezeStatus = %q, want frozen", mock.freezeStatus)
	}
}

func TestEnsureGuestThawedRechecksWhenThawIsIneffective(t *testing.T) {
	mock := &mockOps{
		freezeStatus:    "frozen",
		thawKeepsFrozen: true,
	}
	w := newTestWatchdog(t, mock)

	w.ensureGuestThawed(context.Background(), "test")

	if mock.thawCalls != 1 {
		t.Fatalf("thawCalls = %d, want 1", mock.thawCalls)
	}
	if mock.freezeStatusCalls != 2 {
		t.Fatalf("freezeStatusCalls = %d, want 2: the recheck must observe the surviving freeze", mock.freezeStatusCalls)
	}
	if mock.freezeStatus != "frozen" {
		t.Fatalf("freezeStatus = %q, want frozen", mock.freezeStatus)
	}
}

func TestEnsureGuestThawedRunsWithCancelledCaller(t *testing.T) {
	mock := &mockOps{freezeStatus: "frozen"}
	w := newTestWatchdog(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.ensureGuestThawed(ctx, "test")

	if mock.thawCalls != 1 {
		t.Fatalf("thawCalls = %d, want 1: the guard must survive a cancelled snapshot context", mock.thawCalls)
	}
	if mock.freezeStatus != "thawed" {
		t.Fatalf("freezeStatus = %q, want thawed", mock.freezeStatus)
	}
}

func TestMaybeSnapshotThawsAfterFailedSnapshot(t *testing.T) {
	mock := &mockOps{
		freezeStatus: "frozen",
		vmSnapErr:    errors.New("snapshot task aborted"),
	}
	w := newTestWatchdog(t, mock, func(cfg *watchdogconfig.Config) {
		cfg.Watchdog.SnapshotHealthyThreshold = 1
	})
	w.consecutiveHealthy = 1
	w.lastHashCheckOK = true

	w.maybeSnapshot(context.Background())

	if mock.thawCalls != 1 {
		t.Fatalf("thawCalls = %d, want 1: the guard must run on the failed-snapshot path", mock.thawCalls)
	}
}

// ---------------------------------------------------------------------------
// Test Placeholder - minimal tests to allow compilation
// ---------------------------------------------------------------------------

func TestWatchdogCompiles(t *testing.T) {
	m := &mockOps{vmRunning: true}
	w := newTestWatchdog(t, m)
	if w.cfg.MwanVMID != "113" {
		t.Fatal("watchdog not initialized correctly")
	}
}

func TestMockOpsBasics(t *testing.T) {
	m := &mockOps{
		vmRunning:   true,
		pingResults: map[string]bool{"ping:1.1.1.1": true},
	}
	running, err := m.VMStatus(context.Background(), "113")
	if !running || err != nil {
		t.Fatalf("vmstatus failed: %v", err)
	}
	if !m.Ping(context.Background(), "ping", "1.1.1.1") {
		t.Fatal("ping failed")
	}
}
