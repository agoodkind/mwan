//go:build linux && firewall

package firewall_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/internal/observation"
)

func TestCloudflareObservationExecutorRuntime(t *testing.T) {
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
	settingsJSON, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings observation.RuntimeConfig
	if err := json.Unmarshal(settingsJSON, &settings); err != nil {
		t.Fatal(err)
	}
	machineID := "cloudflare-observation-local"
	settings.MachineIDPath = filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(settings.MachineIDPath, []byte(machineID), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, acceptance := range cases {
		t.Run(
			acceptance.Check.ID,
			func(t *testing.T) {
				spec := acceptance.Check
				spec.Observer = observation.Endpoint{
					Kind:      observation.EndpointLocal,
					MachineID: machineID,
				}
				result := runCloudflareObservation(t, settings, spec)
				if result.Availability != observation.AvailabilityComplete ||
					result.Outcome != acceptance.Outcome ||
					result.CloudflarePool == nil ||
					len(result.CloudflarePool.Regions) == 0 {
					t.Fatalf("actual pool health differs from expected verdict: %+v", result)
				}
				if observation.RequiredPassed(
					spec,
					result,
					time.Now(),
				) != (acceptance.Outcome == observation.OutcomePass) {
					t.Fatalf("actual health acceptance differs from expected verdict: %+v", result)
				}
				spec.CloudflareExpectedOrigins = []string{"nonexistent-observation-origin.invalid"}
				missing := runCloudflareObservation(t, settings, spec)
				if missing.Availability != observation.AvailabilityMissing ||
					missing.Outcome != observation.OutcomeUnknown ||
					observation.RequiredPassed(spec, missing, time.Now()) {
					t.Fatalf(
						"omitted required origin became pool success or target failure: %+v",
						missing,
					)
				}
				absentSettings := settings
				absentSettings.CloudflareTokenFile = filepath.Join(t.TempDir(), "absent-token")
				unavailable := runCloudflareObservation(t, absentSettings, spec)
				if unavailable.Availability != observation.AvailabilityError ||
					unavailable.Outcome != observation.OutcomeUnknown {
					t.Fatalf("unavailable credential became target failure: %+v", unavailable)
				}
				t.Logf(
					"observed %d real Cloudflare regions with %s verdict",
					len(result.CloudflarePool.Regions),
					result.Outcome,
				)
			},
		)
	}
}

func runCloudflareObservation(
	t *testing.T,
	settings observation.RuntimeConfig,
	spec observation.CheckSpec,
) observation.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		t.Context(),
		time.Duration(spec.TimeoutSeconds+5)*time.Second,
	)
	defer cancel()
	executor := observation.NewExecutor(settings, nil)
	return executor.Run(ctx, spec)
}
