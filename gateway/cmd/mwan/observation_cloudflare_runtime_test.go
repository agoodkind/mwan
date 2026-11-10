//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/observation"
)

func TestCloudflareObservationDaemonRuntime(t *testing.T) {
	settingsPath := os.Getenv("MWAN_OBSERVATION_RUNTIME_SETTINGS")
	checksPath := os.Getenv("MWAN_OBSERVATION_CLOUDFLARE_CHECKS")
	if settingsPath == "" || checksPath == "" {
		t.Skip("live Cloudflare runtime settings and explicit expected health checks are required")
	}
	var cases []struct {
		Check   observation.CheckSpec `json:"check"`
		Outcome observation.Outcome   `json:"outcome"`
	}
	input, err := os.ReadFile(checksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(input, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("live Cloudflare acceptance cases are empty")
	}
	binary := protocolTestBinary(t)
	identity, err := os.ReadFile("/etc/machine-id")
	if os.IsNotExist(err) {
		if output, setupErr := exec.Command("systemd-machine-id-setup").CombinedOutput(); setupErr != nil {
			t.Fatalf("initialize observer identity: %v: %s", setupErr, output)
		}
		identity, err = os.ReadFile("/etc/machine-id")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, acceptance := range cases {
		t.Run(acceptance.Check.ID, func(t *testing.T) {
			spec := acceptance.Check
			spec.Observer = observation.Endpoint{Kind: observation.EndpointLocal, MachineID: strings.TrimSpace(string(identity))}
			result := runCloudflareObservationProcess(t, binary, settingsPath, spec)
			if result.Availability != observation.AvailabilityComplete || result.Outcome != acceptance.Outcome || result.CloudflarePool == nil || len(result.CloudflarePool.Regions) == 0 {
				t.Fatalf("actual pool health differs from expected verdict: %+v", result)
			}
			if observation.RequiredPassed(spec, result, time.Now()) != (acceptance.Outcome == observation.OutcomePass) {
				t.Fatalf("actual health acceptance differs from expected verdict: %+v", result)
			}
			spec.CloudflareExpectedOrigins = []string{"nonexistent-observation-origin.invalid"}
			missing := runCloudflareObservationProcess(t, binary, settingsPath, spec)
			if missing.Availability != observation.AvailabilityMissing || missing.Outcome != observation.OutcomeUnknown || observation.RequiredPassed(spec, missing, time.Now()) {
				t.Fatalf("omitted required origin became pool success or target failure: %+v", missing)
			}
			settings, loadErr := loadObservationRuntime(settingsPath)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			settings.CloudflareTokenFile = filepath.Join(t.TempDir(), "absent-token")
			encoded, encodeErr := json.Marshal(settings)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			absentSettings := filepath.Join(t.TempDir(), "runtime.json")
			if writeErr := os.WriteFile(absentSettings, encoded, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			unavailable := runCloudflareObservationProcess(t, binary, absentSettings, spec)
			if unavailable.Availability != observation.AvailabilityError || unavailable.Outcome != observation.OutcomeUnknown {
				t.Fatalf("unavailable credential became target failure: %+v", unavailable)
			}
			t.Logf("observed %d real Cloudflare regions with %s verdict", len(result.CloudflarePool.Regions), result.Outcome)
		})
	}
}

func runCloudflareObservationProcess(t *testing.T, binary, settingsPath string, spec observation.CheckSpec) observation.Result {
	t.Helper()
	input, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(spec.TimeoutSeconds+5)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "observe", "--runtime-settings", settingsPath, "--check", string(input))
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("public Cloudflare observation failed: %v: %s", err, stderr.String())
	}
	var result observation.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("public Cloudflare observation returned invalid JSON: %v", err)
	}
	return result
}
