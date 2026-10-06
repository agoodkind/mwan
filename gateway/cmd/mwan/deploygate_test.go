package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
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
		probeDownstream: func(context.Context, downstreamProbeConfig, requiredEgressFamilies) (bool, string) {
			return true, "ipv6=yes ipv4=yes"
		},
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
	deps.runGuestOwnedCheck = func(context.Context, int) (ops.GuestCommandResult, error) {
		return guestResult(exitDeployGateOK, "owned addresses: 2 present, 0 missing\n"), nil
	}
	deps.ping6 = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		t.Fatal("host IPv6 ping ran during wait-deploy")
		return 0, nil
	}
	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		t.Fatal("host IPv4 ping ran during wait-deploy")
		return 0, nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: time.Minute,
		egressBudget: time.Minute,
		traceID:      "trace-123",
		verdictPath:  verdictPath, families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
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
	deps.probeDownstream = func(context.Context, downstreamProbeConfig, requiredEgressFamilies) (bool, string) {
		t.Fatal("downstream egress probe ran after definitive reboot failure")
		return false, ""
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: 3 * time.Second,
		egressBudget: time.Minute,
		traceID:      "trace-123",
		verdictPath:  verdictPath, families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
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
	deps.probeDownstream = func(context.Context, downstreamProbeConfig, requiredEgressFamilies) (bool, string) {
		egressProbes++
		return false, `ipv6=no ipv4=yes ipv6_error="no route to host"`
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid:         113,
		oldBootID:    testOldBootID,
		rebootBudget: 3 * time.Second,
		egressBudget: 3 * time.Second,
		traceID:      "trace-123",
		verdictPath:  verdictPath, families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
	})

	if code != exitDeployGateOK {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateOK, out.String())
	}
	if egressProbes == 0 {
		t.Fatalf("egress probe count = %d, want greater than zero", egressProbes)
	}
	if !strings.Contains(out.String(), "required egress not restored") {
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
	deps.runGuestOwnedCheck = func(context.Context, int) (ops.GuestCommandResult, error) {
		return guestResult(exitDeployGateOK, "owned addresses: 2 present, 0 missing\n"), nil
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
		verdictPath:  filepath.Join(blocker, "verdict.json"), families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
	})

	if code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d", code, exitDeployGateFailed)
	}
}

func TestWaitEgressRequiresEveryConfiguredFamily(t *testing.T) {
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

	code := waitEgress(context.Background(), deps, 5*time.Second,
		requiredEgressFamilies{ipv4: true, ipv6: true}, 1)

	if code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
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

	code := waitEgress(context.Background(), deps, 5*time.Second,
		requiredEgressFamilies{ipv4: true, ipv6: true}, 1)

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
				TranslationV4: &config.IPv4Translation{
					Mode: config.TranslationNAPT44,
					StaticMappings: []config.StaticMapping{
						{External: netip.MustParseAddr("198.51.100.193"), Internal: netip.MustParseAddr("192.0.2.2")},
					},
				},
			},
			"webpass": {
				Iface: "enwebpass0",
				TranslationV4: &config.IPv4Translation{
					Mode: config.TranslationNAPT44,
					StaticMappings: []config.StaticMapping{
						{External: netip.MustParseAddr("203.0.113.2"), Internal: netip.MustParseAddr("192.0.2.2")},
						{External: netip.MustParseAddr("203.0.113.3"), Internal: netip.MustParseAddr("192.0.2.3")},
						{External: netip.MustParseAddr("203.0.113.4"), Internal: netip.MustParseAddr("192.0.2.4")},
					},
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
	deps.runGuestOwnedCheck = func(context.Context, int) (ops.GuestCommandResult, error) {
		checks++
		if checks < 3 {
			return guestResult(exitDeployGateFailed, "owned addresses: 1 present, 1 missing\n"), nil
		}
		return guestResult(exitDeployGateOK, "owned addresses: 2 present, 0 missing\n"), nil
	}
	deps.alertOwnedMissing = func(context.Context, ownedMissingAlert) error {
		t.Fatal("owned-address alert sent although the addresses were held within the budget")
		return nil
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid: 113, oldBootID: testOldBootID, rebootBudget: time.Minute, egressBudget: time.Minute,
		traceID: "trace-123", verdictPath: verdictPath, families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
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
	deps.runGuestOwnedCheck = func(context.Context, int) (ops.GuestCommandResult, error) {
		return guestResult(exitDeployGateFailed,
			"owned address 203.0.113.4 on enwebpass0: missing\n"), nil
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
		traceID: "trace-123", verdictPath: verdictPath, families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
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
	deps.runGuestOwnedCheck = func(context.Context, int) (ops.GuestCommandResult, error) {
		return guestResult(exitDeployGateFailed, "owned addresses: 0 present, 1 missing\n"), nil
	}
	deps.alertOwnedMissing = func(context.Context, ownedMissingAlert) error {
		return errors.New("email unconfigured")
	}
	verdictPath := filepath.Join(t.TempDir(), "verdict.json")

	code := waitDeploy(context.Background(), deps, waitDeployInputs{
		vmid: 113, oldBootID: testOldBootID, rebootBudget: time.Minute, egressBudget: time.Minute,
		traceID: "trace-123", verdictPath: verdictPath, families: requiredEgressFamilies{ipv4: true, ipv6: true}, rounds: 1,
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

func TestOwnedMissingAlertEmailExplainsRecovery(t *testing.T) {
	alert := ownedMissingAlert{
		VMID:    213,
		TraceID: "deploy-123",
		Report: "owned address 10.241.204.3 on enwebpass0: missing\n" +
			"owned addresses: 0 present, 4 missing",
	}
	event := ownedMissingEvent(alert)
	if event.Message != "VM 213: 4 mapped IPv4 addresses missing" {
		t.Fatalf("email subject message = %q", event.Message)
	}
	record := slog.NewRecord(event.Now, event.Level, event.Message, 0)
	record.AddAttrs(event.Fields...)
	body := notify.BuildEmailBody(record, nil)
	for _, want := range []string{
		"postdeploy check failed after reboot",
		"gateway deploy is unhealthy",
		"No rollback occurred",
		"deployed configuration remains active",
		"On gateway VM 213, run: mwan deploy-gate check-owned-addresses",
		"On gateway VM 213, run: journalctl -u mwan-ifmgr@wan -b --no-pager",
		"Repair the cause, redeploy, then repeat the check",
		"If a fresh check reports 0 missing after a later deploy, the current gateway state passes",
		"10.241.204.3 on enwebpass0: missing",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("email body lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "On gateway VM 213") > strings.Index(body, "10.241.204.3") {
		t.Fatalf("email puts raw report before action:\n%s", body)
	}
}

func TestWaitEgressRequiresEveryConfiguredFamilyInOneRound(t *testing.T) {
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

	if code := waitEgress(context.Background(), deps, time.Second,
		requiredEgressFamilies{ipv4: true, ipv6: true}, 1); code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
	}

	deps.ping4 = func(
		context.Context, string, netip.Addr, time.Duration,
	) (time.Duration, error) {
		return 0, errors.New("v4 down")
	}
	if code := waitEgress(context.Background(), deps, time.Second,
		requiredEgressFamilies{ipv4: true, ipv6: true}, 1); code != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", code, exitDeployGateFailed, out.String())
	}
}

func guestResult(exitCode int, stdout string) ops.GuestCommandResult {
	return ops.GuestCommandResult{
		GuestExecResult: ops.GuestExecResult{ExitCode: exitCode, Stdout: stdout},
		Stderr:          "",
	}
}

func buildMwanBinary(t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "mwan")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binaryPath
}

func installFakeHypervisorTool(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runMwanBinary(
	t *testing.T, binaryPath, toolDir, configPath string, args ...string,
) (string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binaryPath, args...)
	command.Env = append(os.Environ(),
		"PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MWAN_CONFIG="+configPath)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run %v: %v", args, err)
	}
	return string(output), exitErr.ExitCode()
}

func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeployGateWaitRebootReadsTheBootIDThroughTheGuestDriver(t *testing.T) {
	binaryPath := buildMwanBinary(t)
	qemuConfig := writeTestFile(t, "config.toml", "guest_type = \"qemu\"\n")
	lxcConfig := writeTestFile(t, "config.toml", "guest_type = \"lxc\"\n")
	cases := []struct {
		name       string
		tool       string
		configPath string
		reply      string
		wantExit   int
		wantOutput string
	}{
		{
			name: "qemu new boot id", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":0,"exited":1,"out-data":"` + testNewBootID + `\n","out-truncated":0}`,
			wantExit: exitDeployGateOK, wantOutput: "rebooted",
		},
		{
			name: "qemu same boot id", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":0,"exited":1,"out-data":"` + testOldBootID + `\n","out-truncated":0}`,
			wantExit: exitDeployGateFailed, wantOutput: "reboot never fired",
		},
		{
			name: "qemu non-zero exit", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":1,"exited":1,"out-data":"boom","out-truncated":0}`,
			wantExit: exitDeployGateUnobservable, wantOutput: "guest command exited 1",
		},
		{
			name: "qemu stdout is not a boot id", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":0,"exited":1,"out-data":"hello","out-truncated":0}`,
			wantExit: exitDeployGateUnobservable, wantOutput: "not a boot_id UUID",
		},
		{
			name: "qemu empty stdout", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":0,"exited":1}`,
			wantExit: exitDeployGateUnobservable, wantOutput: "not a boot_id UUID",
		},
		{
			name: "qemu partial boot id", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":0,"exited":1,"out-data":"aaaaaaaa-bbbb","out-truncated":0}`,
			wantExit: exitDeployGateUnobservable, wantOutput: "not a boot_id UUID",
		},
		{
			name: "qemu upper-case boot id", tool: "qm", configPath: qemuConfig,
			reply: `{"exitcode":0,"exited":1,` +
				`"out-data":"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE","out-truncated":0}`,
			wantExit: exitDeployGateUnobservable, wantOutput: "not a boot_id UUID",
		},
		{
			name: "qemu truncated reply", tool: "qm", configPath: qemuConfig,
			reply:    `{"exitcode":0,"exited":1,"out-data":"` + testNewBootID + `","out-truncated":1}`,
			wantExit: exitDeployGateUnobservable, wantOutput: "truncated",
		},
		{
			name: "lxc new boot id", tool: "pct", configPath: lxcConfig,
			reply:    testNewBootID + "\n",
			wantExit: exitDeployGateOK, wantOutput: "rebooted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			replyPath := writeTestFile(t, "reply", tc.reply)
			toolDir := installFakeHypervisorTool(t, tc.tool, "#!/bin/sh\ncat '"+replyPath+"'\n")

			output, exitCode := runMwanBinary(t, binaryPath, toolDir, tc.configPath,
				"deploy-gate", "wait-reboot", "113", testOldBootID, "1")

			if exitCode != tc.wantExit {
				t.Fatalf("exit code = %d, want %d\noutput: %s", exitCode, tc.wantExit, output)
			}
			if !strings.Contains(output, tc.wantOutput) {
				t.Fatalf("output does not contain %q: %s", tc.wantOutput, output)
			}
		})
	}
}

func TestDeployGateWaitRebootFailsWhenTheHostConfigurationIsUnreadable(t *testing.T) {
	binaryPath := buildMwanBinary(t)
	missingConfig := filepath.Join(t.TempDir(), "absent.toml")

	output, exitCode := runMwanBinary(t, binaryPath, t.TempDir(), missingConfig,
		"deploy-gate", "wait-reboot", "113", testOldBootID, "1")

	if exitCode != exitDeployGateFailed {
		t.Fatalf("exit code = %d, want %d\noutput: %s", exitCode, exitDeployGateFailed, output)
	}
	if !strings.Contains(output, "load configuration") {
		t.Fatalf("output does not name the configuration failure: %s", output)
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
