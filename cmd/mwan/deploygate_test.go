package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	testOldBootID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testNewBootID = "11111111-2222-3333-4444-555555555555"
)

// fakeClock advances only when the gate sleeps, so the polling loops run
// deterministically without wall-clock time.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func newTestDeps(out *strings.Builder, clock *fakeClock) deployGateDeps {
	return deployGateDeps{
		out:   out,
		now:   clock.Now,
		sleep: clock.Sleep,
	}
}

func readTestVerdict(t *testing.T, path string) gateVerdict {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	var verdict gateVerdict
	if err := json.Unmarshal(payload, &verdict); err != nil {
		t.Fatalf("unmarshal verdict: %v", err)
	}
	return verdict
}

func TestWaitRebootDetectsBootIDChange(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	reads := 0
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		reads++
		if reads < 3 {
			return testOldBootID, nil
		}
		return testNewBootID, nil
	}

	code := waitReboot(context.Background(), deps, 113, testOldBootID, time.Minute)

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if !strings.Contains(out.String(), testNewBootID) {
		t.Fatalf("output does not name the new boot_id: %s", out.String())
	}
}

func TestWaitRebootAgentSilentUntilNewBootID(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	reads := 0
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		reads++
		if reads == 1 {
			return testOldBootID, nil
		}
		if reads < 5 {
			return "", errors.New("guest agent not running")
		}
		return testNewBootID, nil
	}

	code := waitReboot(context.Background(), deps, 113, testOldBootID, time.Minute)

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
}

func TestWaitRebootNeverFiredFailsDefinitively(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		return testOldBootID, nil
	}

	code := waitReboot(context.Background(), deps, 113, testOldBootID, 10*time.Second)

	if code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
	}
	if !strings.Contains(out.String(), "never fired") {
		t.Fatalf("output does not state the reboot never fired: %s", out.String())
	}
}

func TestWaitRebootUnobservableDefersToEgressGate(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		return "", errors.New("guest agent not running")
	}

	code := waitReboot(context.Background(), deps, 113, testOldBootID, 10*time.Second)

	if code != exitDeployGateUnobservable {
		t.Fatalf("exit code = %d, want %d\noutput: %s",
			code, exitDeployGateUnobservable, out.String())
	}
	if !strings.Contains(out.String(), "deferring") {
		t.Fatalf("output does not defer the verdict: %s", out.String())
	}
}

func TestWaitRebootRecoveryOnFinalRead(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	reads := 0
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		reads++
		if reads <= 4 {
			return "", errors.New("guest agent not running")
		}
		// Only the post-deadline final read succeeds, with a new boot_id.
		return testNewBootID, nil
	}

	code := waitReboot(context.Background(), deps, 113, testOldBootID, 10*time.Second)

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
}

func TestWaitDeployRecordsSuccessfulVerdict(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		return testNewBootID, nil
	}
	deps.runGuestOwnedCheck = func(context.Context, int) (guestExecResponse, error) {
		return guestExecResponse{ExitCode: exitDeployGateOK, OutData: "owned addresses: 2 present, 0 missing\n"}, nil
	}
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: time.Minute,
		egressBudget: time.Minute,
		traceID:      "trace-123",
		verdictPath:  verdictPath,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if !strings.Contains(out.String(), "verdict recorded") {
		t.Fatalf("output does not confirm verdict recording: %s", out.String())
	}
	verdict := readTestVerdict(t, verdictPath)
	if verdict.TraceID != "trace-123" {
		t.Fatalf("trace_id = %q, want %q", verdict.TraceID, "trace-123")
	}
	if verdict.OldBootID != testOldBootID {
		t.Fatalf("old_boot_id = %q, want %q", verdict.OldBootID, testOldBootID)
	}
	if verdict.RebootRC != exitDeployGateOK {
		t.Fatalf("reboot_rc = %d, want %d", verdict.RebootRC, exitDeployGateOK)
	}
	if verdict.EgressRC != exitDeployGateOK {
		t.Fatalf("egress_rc = %d, want %d", verdict.EgressRC, exitDeployGateOK)
	}
	if verdict.OwnedRC != exitDeployGateOK {
		t.Fatalf("owned_rc = %d, want %d", verdict.OwnedRC, exitDeployGateOK)
	}
	if verdict.StartedAt == "" || verdict.FinishedAt == "" {
		t.Fatalf("timestamps are not populated: started_at=%q finished_at=%q",
			verdict.StartedAt, verdict.FinishedAt)
	}

	entries, err := os.ReadDir(filepath.Dir(verdictPath))
	if err != nil {
		t.Fatalf("read verdict directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(verdictPath) {
		t.Fatalf("verdict directory entries = %v, want only %q", entries, filepath.Base(verdictPath))
	}
}

func TestWaitDeploySkipsEgressAfterDefinitiveRebootFailure(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		return testOldBootID, nil
	}
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		t.Fatalf("IPv6 egress probe ran after definitive reboot failure")
		return 0, nil
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		t.Fatalf("IPv4 egress probe ran after definitive reboot failure")
		return 0, nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: 3 * time.Second,
		egressBudget: time.Minute,
		traceID:      "trace-123",
		verdictPath:  verdictPath,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	verdict := readTestVerdict(t, verdictPath)
	if verdict.RebootRC != exitDeployGateFailed {
		t.Fatalf("reboot_rc = %d, want %d", verdict.RebootRC, exitDeployGateFailed)
	}
	if verdict.EgressRC != gateNotRun {
		t.Fatalf("egress_rc = %d, want %d", verdict.EgressRC, gateNotRun)
	}
	if verdict.OwnedRC != gateNotRun {
		t.Fatalf("owned_rc = %d, want %d", verdict.OwnedRC, gateNotRun)
	}
}

func TestWaitDeployRunsEgressAfterUnobservableReboot(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		return "", errors.New("guest agent not running")
	}
	egressProbes := 0
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		egressProbes++
		return 0, errors.New("no route to host")
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: 3 * time.Second,
		egressBudget: 3 * time.Second,
		traceID:      "trace-123",
		verdictPath:  verdictPath,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if egressProbes == 0 {
		t.Fatalf("egress probe count = %d, want greater than zero", egressProbes)
	}
	if !strings.Contains(out.String(), "no IPv6 egress") {
		t.Fatalf("output does not confirm the egress verdict: %s", out.String())
	}
	verdict := readTestVerdict(t, verdictPath)
	if verdict.RebootRC != exitDeployGateUnobservable {
		t.Fatalf("reboot_rc = %d, want %d", verdict.RebootRC, exitDeployGateUnobservable)
	}
	if verdict.EgressRC != exitDeployGateFailed {
		t.Fatalf("egress_rc = %d, want %d", verdict.EgressRC, exitDeployGateFailed)
	}
	// The owned-address check needs a guest that is back on the network, so a
	// failed egress verdict leaves it unrun rather than failing it twice.
	if verdict.OwnedRC != gateNotRun {
		t.Fatalf("owned_rc = %d, want %d", verdict.OwnedRC, gateNotRun)
	}
}

func TestWaitDeployReturnsFailureWhenVerdictWriteFails(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(_ context.Context, _ int) (string, error) {
		return testNewBootID, nil
	}
	deps.runGuestOwnedCheck = func(context.Context, int) (guestExecResponse, error) {
		return guestExecResponse{ExitCode: exitDeployGateOK, OutData: "owned addresses: 2 present, 0 missing\n"}, nil
	}
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: time.Minute,
		egressBudget: time.Minute,
		traceID:      "trace-123",
		verdictPath:  filepath.Join(blocker, "verdict.json"),
	})

	if code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d", code, exitDeployGateFailed)
	}
}

func TestWaitEgressV6Decides(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	probes := 0
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		probes++
		if probes < 3 {
			return 0, errors.New("no route to host")
		}
		return time.Millisecond, nil
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return 0, errors.New("still down")
	}

	code := waitEgress(context.Background(), deps, time.Minute)

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if !strings.Contains(out.String(), "ipv6=yes ipv4=no") {
		t.Fatalf("output does not report both families: %s", out.String())
	}
}

func TestWaitEgressTimesOut(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return 0, errors.New("no route to host")
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}

	code := waitEgress(context.Background(), deps, 5*time.Second)

	if code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
	}
}

// ownedTestNetwork is the part of a gateway's network tree the owned-address
// check reads: an on-link provider whose first mapping is its own link address,
// and a routed provider whose block sits outside its lease.
func ownedTestNetwork() (*networkjson.Config, error) {
	return &networkjson.Config{
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface: "enatt0",
				StaticMappings: []config.StaticMapping{
					{External: netip.MustParseAddr("198.51.100.193"), Internal: netip.MustParseAddr("192.0.2.2")},
				},
			},
			"webpass": {
				Iface: "enwebpass0",
				StaticMappings: []config.StaticMapping{
					{External: netip.MustParseAddr("203.0.113.2"), Internal: netip.MustParseAddr("192.0.2.2")},
					{External: netip.MustParseAddr("203.0.113.3"), Internal: netip.MustParseAddr("192.0.2.3")},
					{External: netip.MustParseAddr("203.0.113.4"), Internal: netip.MustParseAddr("192.0.2.4")},
				},
			},
		},
	}, nil
}

func heldAddresses(
	byIface map[string][]string,
) func(context.Context, *slog.Logger, string) ([]netif.CurrentAddr, error) {
	return func(_ context.Context, _ *slog.Logger, iface string) ([]netif.CurrentAddr, error) {
		held := make([]netif.CurrentAddr, 0, len(byIface[iface]))
		for _, cidr := range byIface[iface] {
			held = append(held, netif.CurrentAddr{CIDR: cidr, Family: "inet", Flags: 0})
		}
		return held, nil
	}
}

func TestCheckOwnedAddressesPassesWhenEveryOnLinkAddressIsHeld(t *testing.T) {
	var out strings.Builder
	deps := newTestDeps(&out, &fakeClock{now: time.Unix(1000, 0)})
	deps.loadNetwork = ownedTestNetwork
	deps.listAddrs = heldAddresses(map[string][]string{
		"enatt0":     {"192.0.2.10/24"},
		"enwebpass0": {"203.0.113.2/29", "203.0.113.3/32", "203.0.113.4/32"},
	})

	code := checkOwnedAddresses(context.Background(), deps)

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if !strings.Contains(out.String(), "2 present, 0 missing") {
		t.Fatalf("output does not count the two owned addresses: %s", out.String())
	}
}

func TestCheckOwnedAddressesFailsWhenAnAddressIsMissing(t *testing.T) {
	var out strings.Builder
	deps := newTestDeps(&out, &fakeClock{now: time.Unix(1000, 0)})
	deps.loadNetwork = ownedTestNetwork
	deps.listAddrs = heldAddresses(map[string][]string{
		"enatt0":     {"192.0.2.10/24"},
		"enwebpass0": {"203.0.113.2/29", "203.0.113.3/32"},
	})

	code := checkOwnedAddresses(context.Background(), deps)

	if code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
	}
	if !strings.Contains(out.String(), "203.0.113.4 on enwebpass0: missing") {
		t.Fatalf("output does not name the missing address: %s", out.String())
	}
}

func TestWaitDeployRetriesOwnedAddressesUntilHeld(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(context.Context, int) (string, error) { return testNewBootID, nil }
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.ping4 = func(context.Context, string, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	checks := 0
	deps.runGuestOwnedCheck = func(context.Context, int) (guestExecResponse, error) {
		checks++
		if checks < 3 {
			return guestExecResponse{ExitCode: exitDeployGateFailed, OutData: "owned addresses: 1 present, 1 missing\n"}, nil
		}
		return guestExecResponse{ExitCode: exitDeployGateOK, OutData: "owned addresses: 2 present, 0 missing\n"}, nil
	}
	deps.alertOwnedMissing = func(context.Context, ownedMissingAlert) error {
		t.Fatal("owned-address alert sent although the addresses were held within the budget")
		return nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid: 113, oldBootID: testOldBootID, rebootBudget: time.Minute, egressBudget: time.Minute,
		traceID: "trace-123", verdictPath: verdictPath,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if verdict := readTestVerdict(t, verdictPath); verdict.OwnedRC != exitDeployGateOK {
		t.Fatalf("owned_rc = %d, want %d\noutput: %s", verdict.OwnedRC, exitDeployGateOK, out.String())
	}
	if checks != 3 {
		t.Fatalf("owned-address checks = %d, want 3", checks)
	}
}

func TestWaitDeployFailsOwnedAddressesAfterTheBudget(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(context.Context, int) (string, error) { return testNewBootID, nil }
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.ping4 = func(context.Context, string, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.runGuestOwnedCheck = func(context.Context, int) (guestExecResponse, error) {
		return guestExecResponse{
			ExitCode: exitDeployGateFailed,
			OutData:  "owned address 203.0.113.4 on enwebpass0: missing\n",
		}, nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")
	var alerts []ownedMissingAlert
	deps.alertOwnedMissing = func(_ context.Context, alert ownedMissingAlert) error {
		// The collector must be able to read the verdict before the mail
		// transport is touched, so the file already exists when the alert goes.
		if _, err := os.Stat(verdictPath); err != nil {
			t.Fatalf("alert sent before the verdict was recorded: %v", err)
		}
		alerts = append(alerts, alert)
		return nil
	}

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid: 113, oldBootID: testOldBootID, rebootBudget: time.Minute, egressBudget: time.Minute,
		traceID: "trace-123", verdictPath: verdictPath,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if verdict := readTestVerdict(t, verdictPath); verdict.OwnedRC != exitDeployGateFailed {
		t.Fatalf("owned_rc = %d, want %d", verdict.OwnedRC, exitDeployGateFailed)
	}
	if !strings.Contains(out.String(), "203.0.113.4 on enwebpass0: missing") {
		t.Fatalf("output does not carry the guest's last report: %s", out.String())
	}
	if len(alerts) != 1 {
		t.Fatalf("owned-address alerts = %d, want 1", len(alerts))
	}
	if alerts[0].VMID != 113 || alerts[0].TraceID != "trace-123" {
		t.Fatalf("alert = %+v, want vmid 113 and trace-123", alerts[0])
	}
	if !strings.Contains(alerts[0].Report, "203.0.113.4 on enwebpass0: missing") {
		t.Fatalf("alert report does not name the missing address: %q", alerts[0].Report)
	}
}

func TestWaitDeployRecordsTheVerdictWhenTheAlertFails(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.readBootID = func(context.Context, int) (string, error) { return testNewBootID, nil }
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.ping4 = func(context.Context, string, netip.Addr, time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	deps.runGuestOwnedCheck = func(context.Context, int) (guestExecResponse, error) {
		return guestExecResponse{ExitCode: exitDeployGateFailed, OutData: "owned addresses: 0 present, 1 missing\n"}, nil
	}
	deps.alertOwnedMissing = func(context.Context, ownedMissingAlert) error {
		return errors.New("email unconfigured")
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid: 113, oldBootID: testOldBootID, rebootBudget: time.Minute, egressBudget: time.Minute,
		traceID: "trace-123", verdictPath: verdictPath,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if verdict := readTestVerdict(t, verdictPath); verdict.OwnedRC != exitDeployGateFailed {
		t.Fatalf("owned_rc = %d, want %d", verdict.OwnedRC, exitDeployGateFailed)
	}
	if !strings.Contains(out.String(), "owned-address alert not sent: email unconfigured") {
		t.Fatalf("output does not report the failed alert: %s", out.String())
	}
}

func TestCheckEgressEitherFamilySuffices(t *testing.T) {
	var out strings.Builder
	clock := &fakeClock{now: time.Unix(1000, 0)}
	deps := newTestDeps(&out, clock)
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		return 0, errors.New("v6 down")
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}

	if code := checkEgress(context.Background(), deps); code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}

	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return 0, errors.New("v4 down")
	}
	if code := checkEgress(context.Background(), deps); code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
	}
}

func TestUnmarshalGuestBootID(t *testing.T) {
	valid := fmt.Sprintf(`{"exitcode": 0, "out-data": "%s\n"}`, testOldBootID)
	bootID, err := unmarshalGuestBootID([]byte(valid))
	if err != nil {
		t.Fatalf("unmarshalGuestBootID(valid): %v", err)
	}
	if bootID != testOldBootID {
		t.Fatalf("bootID = %q, want %q", bootID, testOldBootID)
	}

	cases := map[string]string{
		"nonzero exit": `{"exitcode": 1, "out-data": "boom"}`,
		"not a uuid":   `{"exitcode": 0, "out-data": "hello"}`,
		"empty out":    `{"exitcode": 0, "out-data": ""}`,
		"malformed":    `QEMU guest agent is not running`,
		"truncated":    `{"exitcode": 0, "out-data": "aaaaaaaa-bbbb"}`,
		"upper hex":    `{"exitcode": 0, "out-data": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}`,
	}
	for name, raw := range cases {
		if _, err := unmarshalGuestBootID([]byte(raw)); err == nil {
			t.Fatalf("unmarshalGuestBootID(%s) accepted %q", name, raw)
		}
	}
}

func TestRunDeployGateUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"bogus-mode"},
		{"wait-deploy"},
		{"wait-deploy", "not-a-vmid", testOldBootID, "180", "180", "trace-123", "/tmp/verdict.json"},
		{"wait-deploy", "113", "not-a-uuid", "180", "180", "trace-123", "/tmp/verdict.json"},
		{"wait-deploy", "113", testOldBootID, "0", "180", "trace-123", "/tmp/verdict.json"},
		{"wait-deploy", "113", testOldBootID, "180", "0", "trace-123", "/tmp/verdict.json"},
		{"wait-deploy", "113", testOldBootID, "180", "180", "trace/123", "/tmp/verdict.json"},
		{"wait-deploy", "113", testOldBootID, "180", "180", "trace 123", "/tmp/verdict.json"},
		{"wait-reboot", "113", "not-a-uuid", "180"},
		{"wait-reboot", "113", testOldBootID, "0"},
		{"wait-reboot", "113; rm", testOldBootID, "180"},
		{"wait-reboot", "113", testOldBootID},
		{"wait-egress"},
		{"check-egress", "extra"},
		{"check-owned-addresses", "extra"},
	}
	for _, args := range cases {
		if code := runDeployGate(args); code != exitDeployGateUsage {
			t.Fatalf("runDeployGate(%v) = %d, want %d", args, code, exitDeployGateUsage)
		}
	}
}
