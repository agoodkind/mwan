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
	"testing"
	"time"

	"goodkind.io/mwan/internal/deployoperation"
	"goodkind.io/mwan/internal/observation"
)

func TestDeployOperationWatchRuntime(t *testing.T) {
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
	checks := deployWatchApplicationChecks(t, identity.MachineID)
	record := deployoperation.Record{
		Manifest: deployoperation.Manifest{
			OperationID: "watch-runtime", Generation: rand.Text(), VMID: "999999", Snapshot: "unexecuted-watch-fixture",
			Baseline: identity, Target: identity,
			Paths:    deployoperation.Paths{Executable: binary, Network: networkPath, Runtime: runtimePath},
			Deadline: time.Now().Add(2 * time.Minute), WatchUnit: unit,
			PollSeconds: 1, ObservationTimeoutSeconds: 5, RecoveryTimeoutSeconds: 10, FailureThreshold: 3,
			RequiredFamilies: []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6},
			RequiredChecks:   checks, RestoredRequiredChecks: checks,
			Observation: observation.RuntimeConfig{MachineIDPath: "/etc/machine-id", ProbeBinary: binary},
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
		if output, err := exec.Command("systemctl", "stop", unit).CombinedOutput(); err != nil {
			t.Errorf("stop owned watch: %v: %s", err, output)
		}
	})
	ready := waitDeployWatchReady(t, binary, commandArgs)
	if ready.Watch.PID == 0 || ready.Watch.InvocationID == "" || ready.Watch.Unit != unit {
		t.Fatalf("watch registration lacks actual service identity: %+v", ready.Watch)
	}
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
}

func deployWatchApplicationChecks(t *testing.T, machineID string) []observation.CheckSpec {
	t.Helper()
	var checks []observation.CheckSpec
	for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
		network, address := "tcp4", "127.0.0.1:0"
		if family == observation.FamilyIPv6 {
			network, address = "tcp6", "[::1]:0"
		}
		listener, err := net.Listen(network, address)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte("healthy"))
		}))
		if err := server.Listener.Close(); err != nil {
			t.Fatal(err)
		}
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		source := listener.Addr().(*net.TCPAddr).AddrPort().Addr().Unmap()
		for _, dimension := range []observation.Dimension{observation.DimensionInboundApplication, observation.DimensionDownstreamApplication} {
			checks = append(checks, observation.CheckSpec{
				ID: string(family) + "-" + string(dimension), Dimension: dimension,
				Operation: observation.OperationHTTP, Observer: observation.Endpoint{Kind: observation.EndpointLocal, MachineID: machineID},
				Family: family, Interface: "lo", Source: source, Target: server.URL,
				TimeoutSeconds: 2, MaxAgeSeconds: 10, ExpectedHTTPStatus: []int{http.StatusOK}, ExpectedBody: "healthy",
			})
		}
	}
	return checks
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
