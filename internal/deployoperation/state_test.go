package deployoperation_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/deployoperation"
	"goodkind.io/mwan/internal/observation"
)

const (
	testMachineID = "0123456789abcdef0123456789abcdef"
	testBootID    = "01234567-89ab-4def-8123-456789abcdef"
	testDigest    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func armedRecord(generation string) deployoperation.Record {
	identity := deployoperation.Identity{
		MachineID: testMachineID, BootID: testBootID,
		ExecutableSHA256: testDigest, NetworkSHA256: testDigest, RuntimeSHA256: testDigest,
	}
	var checks []observation.CheckSpec
	for _, dimension := range []observation.Dimension{observation.DimensionInboundApplication, observation.DimensionDownstreamApplication} {
		checks = append(checks, observation.CheckSpec{
			ID: "ipv4-" + string(dimension), Dimension: dimension, Operation: observation.OperationHTTP,
			Observer: observation.Endpoint{Kind: observation.EndpointLocal, MachineID: testMachineID},
			Family:   observation.FamilyIPv4, Target: "http://192.0.2.10/" + string(dimension),
			TimeoutSeconds: 2, MaxAgeSeconds: 10, ExpectedHTTPStatus: []int{http.StatusOK},
		})
	}
	return deployoperation.Record{
		Manifest: deployoperation.Manifest{
			OperationID: "disarm-" + generation, Generation: generation, VMID: "999999", Snapshot: "baseline",
			Baseline: identity, Target: identity,
			Paths:    deployoperation.Paths{Executable: "/usr/local/bin/mwan", Network: "/etc/mwan/network.json", Runtime: "/etc/mwan/config.toml"},
			Deadline: time.Now().Add(time.Minute), WatchUnit: "mwan-deploy-watch-" + generation + ".service",
			PollSeconds: 1, ObservationTimeoutSeconds: 5, RecoveryTimeoutSeconds: 10, FailureThreshold: 3,
			RequiredFamilies: []observation.Family{observation.FamilyIPv4},
			RequiredChecks:   checks, RestoredRequiredChecks: checks,
			Observation: observation.RuntimeConfig{MachineIDPath: "/etc/machine-id", ProbeBinary: "/usr/local/bin/mwan"},
		},
		Status: deployoperation.Armed,
	}
}

func TestDisarmEndsAnOperationWithoutRecovery(t *testing.T) {
	t.Parallel()
	store := deployoperation.Store{
		Path:         filepath.Join(t.TempDir(), "rollback.operation"),
		PollInterval: 10 * time.Millisecond, Clock: clock.Real{},
	}
	first := armedRecord("first")
	if err := store.Create(t.Context(), first); err != nil {
		t.Fatalf("create the armed operation: %v", err)
	}
	if err := store.Disarm(t.Context(), first.OperationID, first.Generation, "deploy watch setup failed"); err != nil {
		t.Fatalf("disarm the operation that has no lease: %v", err)
	}
	disarmed, err := store.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if disarmed.Status != deployoperation.Disarmed || disarmed.Reason != "deploy watch setup failed" {
		t.Fatalf("record after disarm: status=%s reason=%q", disarmed.Status, disarmed.Reason)
	}
	err = store.BeginRecovery(t.Context(), first.OperationID, first.Generation, "deployment watch is absent")
	if err == nil || !strings.Contains(err.Error(), "rejects recovery") {
		t.Fatalf("recovery of the disarmed operation returned %v, want a rejection", err)
	}
	if err := store.Create(t.Context(), armedRecord("second")); err != nil {
		t.Fatalf("the disarmed operation blocked the next operation: %v", err)
	}
}
