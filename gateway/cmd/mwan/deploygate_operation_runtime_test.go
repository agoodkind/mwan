//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/renameio/v2"
	"goodkind.io/mwan/internal/deployoperation"
	"goodkind.io/mwan/internal/observation"
)

func TestDeployOperationWatchRuntime(t *testing.T) {
	binary, runtimePath, record, _ := startDeployWatchFixture(t, nil)
	commandArgs := []string{"deploy-gate", "status", record.OperationID, record.Generation, "--config", runtimePath}
	unit := record.WatchUnit
	leaseOutput, err := deployWatchCommand(binary, "deploy-gate", "lease", record.OperationID, record.Generation, "fixture-phase", "10", "--config", runtimePath)
	if err != nil {
		t.Fatalf("fresh real application replies did not permit lease: %v: %s", err, leaseOutput)
	}
	var lease deployoperation.Lease
	if err := json.Unmarshal(leaseOutput, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.ID == "" || lease.Phase != "fixture-phase" {
		t.Fatalf("lease response is invalid: %+v", lease)
	}
	for _, generation := range []string{record.Generation, "different-generation"} {
		if output, err := deployWatchCommand(binary, "deploy-gate", "lease", record.OperationID, generation, "second-phase", "10", "--config", runtimePath); err == nil {
			t.Fatalf("outstanding or wrong-generation lease was accepted: %s", output)
		}
	}
	if output, err := deployWatchCommand(binary, "deploy-gate", "release", record.OperationID, record.Generation, lease.ID, "--config", runtimePath); err != nil {
		t.Fatalf("release exact lease: %v: %s", err, output)
	}
	runResolverCommand(t, "systemctl", "stop", unit)
	stopped := deployWatchStatus(t, binary, commandArgs)
	if stopped.MutationReady {
		t.Fatal("stopped watch permits mutation")
	}
	if output, err := deployWatchCommand(binary, "deploy-gate", "lease", record.OperationID, record.Generation, "after-stop", "10", "--config", runtimePath); err == nil {
		t.Fatalf("lease accepted after actual watch exit: %s", output)
	}
	t.Run("expected-interruption", deployWatchExpectedInterruption)
	t.Run("commit-during-observation", deployWatchCommitDuringObservation)
	t.Run("commit-waits-for-observations", deployWatchCommitWaitsForObservations)
	t.Run("commit-ignores-observations-before-start", deployWatchCommitIgnoresEarlierObservations)
}

func deployWatchCommitIgnoresEarlierObservations(t *testing.T) {
	t.Helper()
	binary, runtimePath, record, failed := startDeployWatchFixture(t, nil)
	type commitResult struct {
		output []byte
		err    error
	}
	done := make(chan commitResult, 1)
	go func() {
		output, err := deployWatchCommand(binary, "deploy-gate", "commit", record.OperationID, record.Generation, "--config", runtimePath)
		done <- commitResult{output: output, err: err}
	}()
	time.Sleep(200 * time.Millisecond)
	failed["ipv4-inbound_application"].Store(true)
	result := <-done
	if result.err == nil {
		t.Fatalf("commit succeeded after a failed application observation: %s", result.output)
	}
	if !strings.Contains(result.err.Error(), "deploy operation is not armed") {
		t.Fatalf("commit used observations taken before it started: %v", result.err)
	}
}

func deployWatchCommitWaitsForObservations(t *testing.T) {
	t.Helper()
	failedID := "ipv4-inbound_application"
	binary, runtimePath, record, failed := startDeployWatchFixture(t, nil)
	args := []string{"deploy-gate", "status", record.OperationID, record.Generation, "--config", runtimePath}
	failed[failedID].Store(true)
	observed := false
	for deadline := time.Now().Add(5 * time.Second); !observed && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, result := range deployWatchStatus(t, binary, args).Results {
			if result.CheckID == failedID && result.Outcome == observation.OutcomeFail {
				observed = true
			}
		}
	}
	if !observed {
		t.Fatal("watch did not record the failed application observation")
	}
	output, err := deployWatchCommand(binary, "deploy-gate", "commit", record.OperationID, record.Generation, "--config", runtimePath)
	if err == nil {
		t.Fatalf("commit succeeded with a failed application observation: %s", output)
	}
	if !strings.Contains(err.Error(), "deploy operation is not armed") {
		t.Fatalf("commit did not wait for the watch to start recovery: %v", err)
	}
}

func deployWatchCommitDuringObservation(t *testing.T) {
	t.Helper()
	var blockNext atomic.Bool
	started := make(chan struct{})
	resume := make(chan struct{})
	var release sync.Once
	binary, runtimePath, record, _ := startDeployWatchFixtureWithResponse(t, nil, func(id string) {
		if id == "ipv4-inbound_application" && blockNext.CompareAndSwap(true, false) {
			close(started)
			<-resume
		}
	})
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	blockNext.Store(true)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not start the blocked application observation")
	}
	args := []string{"deploy-gate", "status", record.OperationID, record.Generation, "--config", runtimePath}
	committed := deployWatchStatus(t, binary, args).Record
	committed.Status = deployoperation.Committed
	data, err := json.Marshal(committed)
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(filepath.Dir(runtimePath), "rollback.operation")
	if err := renameio.WriteFile(recordPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(resume) })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("systemctl", "show", record.WatchUnit, "--property=ActiveState", "--property=Result", "--property=ExecMainStatus").CombinedOutput()
		if err != nil {
			t.Fatalf("read actual watch exit: %v: %s", err, output)
		}
		if strings.Contains(string(output), "ActiveState=inactive\n") {
			if !strings.Contains(string(output), "Result=success\n") || !strings.Contains(string(output), "ExecMainStatus=0\n") {
				t.Fatalf("committed watch did not exit successfully: %s", output)
			}
			after, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, after) {
				t.Fatalf("watch changed the committed record: before=%s after=%s", data, after)
			}
			return
		}
		if strings.Contains(string(output), "ActiveState=failed\n") {
			t.Fatalf("committed watch failed: %s", output)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("committed watch did not exit after the observation completed")
}

func deployWatchExpectedInterruption(t *testing.T) {
	t.Helper()
	selectedID := "ipv4-inbound_application"
	policy := []deployoperation.ExpectedInterruption{{Phase: "connection-handover-selected", CheckIDs: []string{selectedID}, MaxSeconds: 8}}
	for _, scenario := range []struct {
		name          string
		failedID      string
		interruptions []deployoperation.ExpectedInterruption
		expected      bool
	}{
		{name: "selected-until-expiry", failedID: selectedID, interruptions: policy, expected: true},
		{name: "unaffected-provider", failedID: "ipv4-inbound_application-unaffected", interruptions: policy},
		{name: "downstream", failedID: "ipv4-downstream_application", interruptions: policy},
		{name: "no-exemption", failedID: selectedID},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			binary, runtimePath, record, failed := startDeployWatchFixture(t, scenario.interruptions)
			args := []string{"deploy-gate", "status", record.OperationID, record.Generation, "--config", runtimePath}
			if len(scenario.interruptions) > 0 {
				if output, err := deployWatchCommand(binary, "deploy-gate", "lease", record.OperationID, record.Generation, policy[0].Phase, "9", "--config", runtimePath); err == nil {
					t.Fatalf("lease exceeds declared interruption bound: %s", output)
				}
			}
			output, err := deployWatchCommand(binary, "deploy-gate", "lease", record.OperationID, record.Generation, policy[0].Phase, "8", "--config", runtimePath)
			if err != nil {
				t.Fatal(err)
			}
			var lease deployoperation.Lease
			if err := json.Unmarshal(output, &lease); err != nil {
				t.Fatal(err)
			}
			failed[scenario.failedID].Store(true)
			deadline := lease.ExpiresAt.Add(5 * time.Second)
			failedObservations := 0
			var observedAt time.Time
			for time.Now().Before(deadline) {
				status := deployWatchStatus(t, binary, args)
				if status.Status == deployoperation.Recovering || status.Status == deployoperation.RecoveryFailed {
					if scenario.expected && time.Now().Before(lease.ExpiresAt) {
						t.Fatal("selected-provider interruption triggered recovery before lease expiry")
					}
					if scenario.expected && failedObservations < record.FailureThreshold {
						t.Fatal("expected interruption did not persist enough actual failed observations")
					}
					if !scenario.expected && !time.Now().Before(lease.ExpiresAt) {
						t.Fatal("strict check did not trigger recovery during the live lease")
					}
					if output, err := deployWatchCommand(binary, "deploy-gate", "lease", record.OperationID, record.Generation, "after-failure", "1", "--config", runtimePath); err == nil {
						t.Fatalf("recovery permits a new lease: %s", output)
					}
					return
				}
				for _, result := range status.Results {
					if result.CheckID == scenario.failedID && result.Outcome == observation.OutcomeFail && result.Availability == observation.AvailabilityComplete && result.ObservedAt.After(observedAt) {
						observedAt = result.ObservedAt
						failedObservations++
						if status.MutationReady {
							t.Fatal("failed application observation permits mutation")
						}
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatal("required application failure did not enter recovery")
		})
	}
}

func startDeployWatchFixture(t *testing.T, interruptions []deployoperation.ExpectedInterruption) (string, string, deployoperation.Record, map[string]*atomic.Bool) {
	t.Helper()
	return startDeployWatchFixtureWithResponse(t, interruptions, nil)
}

func startDeployWatchFixtureWithResponse(t *testing.T, interruptions []deployoperation.ExpectedInterruption, beforeResponse func(string)) (string, string, deployoperation.Record, map[string]*atomic.Bool) {
	t.Helper()
	if os.Getenv("MWAN_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires the existing dedicated systemd container")
	}
	runResolverCommand(t, "systemctl", "is-active", "dbus")
	binary := protocolTestBinary(t)
	root := t.TempDir()
	unit := "mwan-deploy-watch-" + rand.Text() + ".service"
	runtimePath := filepath.Join(root, "config.toml")
	networkPath := filepath.Join(root, "network.json")
	lockPath := filepath.Join(root, "rollback")
	configuration := fmt.Sprintf("mwan_vmid = '999999'\n[watchdog]\nrollback_lock_file = '%s'\nrollback_state_file = '%s'\ncheck_interval_degraded_seconds = 1\nconnectivity_timeout_seconds = 5\n", lockPath, filepath.Join(root, "rollback-state"))
	for path, data := range map[string][]byte{runtimePath: []byte(configuration), networkPath: []byte("{}\n")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	identity := deployWatchFixtureIdentity(t, binary, networkPath, runtimePath)
	checks, failed := deployWatchApplicationChecks(t, identity.MachineID, beforeResponse)
	record := deployoperation.Record{
		Manifest: deployoperation.Manifest{
			OperationID: "watch-runtime", Generation: rand.Text(), VMID: "999999", Snapshot: "unexecuted-watch-fixture",
			Baseline: identity, Target: identity,
			Paths:    deployoperation.Paths{Executable: binary, Network: networkPath, Runtime: runtimePath},
			Deadline: time.Now().Add(2 * time.Minute), WatchUnit: unit,
			PollSeconds: 1, ObservationTimeoutSeconds: 5, RecoveryTimeoutSeconds: 10, FailureThreshold: 3,
			RequiredFamilies: []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6},
			RequiredChecks:   checks, RestoredRequiredChecks: checks,
			ExpectedInterruptions: interruptions,
			Observation:           observation.RuntimeConfig{MachineIDPath: "/etc/machine-id", ProbeBinary: binary},
		},
		Status: deployoperation.Armed, UpdatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath+".operation", data, 0o600); err != nil {
		t.Fatal(err)
	}
	commandArgs := []string{"deploy-gate", "status", record.OperationID, record.Generation, "--config", runtimePath}
	before := deployWatchStatus(t, binary, commandArgs)
	if before.MutationReady {
		t.Fatal("unregistered watch permits mutation")
	}
	runResolverCommand(t, "systemd-run", "--unit="+unit, "--property=Type=exec", binary, "deploy-gate", "watch", record.OperationID, record.Generation, "--config", runtimePath)
	t.Cleanup(func() {
		loadState, err := exec.Command("systemctl", "show", unit, "--property=LoadState", "--value").Output()
		if err != nil {
			t.Errorf("read owned watch load state: %v", err)
			return
		}
		if strings.TrimSpace(string(loadState)) == "not-found" {
			return
		}
		if output, err := exec.Command("systemctl", "stop", unit).CombinedOutput(); err != nil {
			t.Errorf("stop owned watch: %v: %s", err, output)
		}
	})
	ready := waitDeployWatchReady(t, binary, commandArgs)
	if ready.Watch.PID == 0 || ready.Watch.InvocationID == "" || ready.Watch.Unit != unit {
		t.Fatalf("watch registration lacks actual service identity: %+v", ready.Watch)
	}
	return binary, runtimePath, record, failed
}

func deployWatchApplicationChecks(t *testing.T, machineID string, beforeResponse func(string)) ([]observation.CheckSpec, map[string]*atomic.Bool) {
	t.Helper()
	var checks []observation.CheckSpec
	failed := make(map[string]*atomic.Bool)
	for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
		network, address := "tcp4", "127.0.0.1:0"
		if family == observation.FamilyIPv6 {
			network, address = "tcp6", "[::1]:0"
		}
		listener, err := net.Listen(network, address)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		server := httptest.NewUnstartedServer(mux)
		if err := server.Listener.Close(); err != nil {
			t.Fatal(err)
		}
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		source := listener.Addr().(*net.TCPAddr).AddrPort().Addr().Unmap()
		for _, dimension := range []observation.Dimension{observation.DimensionInboundApplication, observation.DimensionDownstreamApplication} {
			ids := []string{string(family) + "-" + string(dimension)}
			if dimension == observation.DimensionInboundApplication {
				ids = append(ids, ids[0]+"-unaffected")
			}
			for _, id := range ids {
				failure := &atomic.Bool{}
				failed[id] = failure
				mux.HandleFunc("/"+id, func(writer http.ResponseWriter, _ *http.Request) {
					if beforeResponse != nil {
						beforeResponse(id)
					}
					if failure.Load() {
						writer.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = writer.Write([]byte("healthy"))
				})
				checks = append(checks, observation.CheckSpec{
					ID: id, Dimension: dimension,
					Operation: observation.OperationHTTP, Observer: observation.Endpoint{Kind: observation.EndpointLocal, MachineID: machineID},
					Family: family, Interface: "lo", Source: source, Target: server.URL + "/" + id,
					TimeoutSeconds: 2, MaxAgeSeconds: 10, ExpectedHTTPStatus: []int{http.StatusOK}, ExpectedBody: "healthy",
				})
			}
		}
	}
	return checks, failed
}

func deployWatchFixtureIdentity(t *testing.T, binary, networkPath, runtimePath string) deployoperation.Identity {
	t.Helper()
	machineID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		t.Fatal(err)
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, path := range []string{binary, networkPath, runtimePath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, fmt.Sprintf("%x", sha256.Sum256(data)))
	}
	return deployoperation.Identity{MachineID: strings.TrimSpace(string(machineID)), BootID: strings.TrimSpace(string(bootID)), ExecutableSHA256: hashes[0], NetworkSHA256: hashes[1], RuntimeSHA256: hashes[2]}
}

func deployWatchCommand(binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("public deployment command: %w: %s", err, stderr.String())
	}
	return output, nil
}

func deployWatchStatus(t *testing.T, binary string, args []string) deployOperationStatus {
	t.Helper()
	output, err := deployWatchCommand(binary, args...)
	if err != nil {
		t.Fatalf("public status command: %v: %s", err, output)
	}
	var status deployOperationStatus
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatalf("public status JSON: %v: %s", err, output)
	}
	return status
}

func waitDeployWatchReady(t *testing.T, binary string, args []string) deployOperationStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status := deployWatchStatus(t, binary, args)
		if status.MutationReady {
			return status
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("actual systemd watch did not publish fresh passing application replies")
	return deployOperationStatus{}
}
