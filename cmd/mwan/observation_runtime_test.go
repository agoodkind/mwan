//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/internal/observation"
)

func TestObservationDaemonRuntime(t *testing.T) {
	binary := protocolTestBinary(t)
	machineID := "runtime-observation-machine"
	machineIDPath := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(machineIDPath, []byte(machineID), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
		t.Run(string(family), func(t *testing.T) {
			network, address := "tcp4", "127.0.0.1:0"
			if family == observation.FamilyIPv6 {
				network, address = "tcp6", "[::1]:0"
			}
			listener, err := net.Listen(network, address)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/timeout" {
					<-request.Context().Done()
					return
				}
				if request.URL.Path == "/fail" {
					writer.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = writer.Write([]byte("healthy"))
			}))
			server.Listener = listener
			server.Start()
			defer server.Close()
			source := listener.Addr().(*net.TCPAddr).AddrPort().Addr().Unmap()
			spec := observation.CheckSpec{
				ID: "downstream-application", Dimension: observation.DimensionDownstreamApplication,
				Operation: observation.OperationHTTP, Observer: observation.Endpoint{Kind: observation.EndpointLocal, MachineID: machineID},
				Family: family, Source: source, Target: server.URL, TimeoutSeconds: 2, MaxAgeSeconds: 30,
				ExpectedHTTPStatus: []int{http.StatusOK}, ExpectedBody: "healthy",
			}
			exerciseContinuousObservation(t, binary, machineIDPath, spec)
			passed := runObservationProcess(t, binary, machineIDPath, spec)
			if !observation.RequiredPassed(spec, passed, time.Now()) || passed.Path.Source != source || passed.Path.Interface != "lo" {
				t.Fatalf("source-bound application reply did not pass: %+v", passed)
			}
			spec.Target = server.URL + "/fail"
			failed := runObservationProcess(t, binary, machineIDPath, spec)
			if failed.Availability != observation.AvailabilityComplete || failed.Outcome != observation.OutcomeFail || failed.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("unhealthy application response did not fail: %+v", failed)
			}
			spec.Target = server.URL + "/timeout"
			timedOut := runObservationProcess(t, binary, machineIDPath, spec)
			if timedOut.Availability != observation.AvailabilityComplete || timedOut.Outcome != observation.OutcomeFail || timedOut.Reason != "target reply timed out" {
				t.Fatalf("target timeout became missing observation: %+v", timedOut)
			}
			spec.Observer.MachineID = "another-machine"
			missing := runObservationProcess(t, binary, machineIDPath, spec)
			if missing.Availability != observation.AvailabilityError || missing.Outcome != observation.OutcomeUnknown {
				t.Fatalf("wrong observer identity became application failure: %+v", missing)
			}
			spec.Observer.MachineID = machineID
			spec.Operation, spec.Dimension, spec.Target = observation.OperationPing, observation.DimensionPingPath, source.String()
			spec.ExpectedHTTPStatus, spec.ExpectedBody = nil, ""
			ping := runObservationProcess(t, binary, machineIDPath, spec)
			if !observation.RequiredPassed(spec, ping, time.Now()) || ping.Path.Source != source || ping.Path.Destination != source {
				t.Fatalf("ICMP reply omitted its actual source: %+v", ping)
			}
			if observation.RequiredPassed(spec, ping, ping.ObservedAt.Add(31*time.Second)) {
				t.Fatal("expired observation passed acceptance")
			}
		})
	}
}

func exerciseContinuousObservation(t *testing.T, binary, machineIDPath string, original observation.CheckSpec) {
	t.Helper()
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
			return
		}
		if failing.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("healthy"))
	}))
	defer server.Close()
	// The recurring consumer uses the requested family, including an IPv6 listener.
	if original.Family == observation.FamilyIPv6 {
		server.Close()
		server = httptest.NewUnstartedServer(server.Config.Handler)
		listener, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Fatal(err)
		}
		server.Listener = listener
		server.Start()
		defer server.Close()
	}
	source := server.Listener.Addr().(*net.TCPAddr).AddrPort().Addr().Unmap()
	fast := original
	fast.ID, fast.Target, fast.Source = "inbound", server.URL, source
	fast.Dimension = observation.DimensionInboundApplication
	slow := fast
	slow.ID, slow.Target, slow.TimeoutSeconds = "slow-downstream", server.URL+"/slow", 3
	slow.Dimension = observation.DimensionDownstreamApplication
	unknown := fast
	unknown.ID, unknown.Observer.MachineID = "unavailable-observer", "another-machine"
	settings := observation.ContinuousSettings{Runtime: observation.RuntimeConfig{MachineIDPath: machineIDPath, ProbeBinary: binary}, Checks: []observation.ScheduledCheck{
		{Check: fast, IntervalSeconds: 1}, {Check: slow, IntervalSeconds: 1}, {Check: unknown, IntervalSeconds: 1},
	}}
	root := t.TempDir()
	settingsPath, configPath := filepath.Join(root, "checks.json"), filepath.Join(root, "config.toml")
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "observe", "--continuous", "--settings", settingsPath, "--config", configPath)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	type record struct {
		Result  *observation.Result  `json:"result"`
		Summary *observation.Summary `json:"summary"`
	}
	records := make(chan record, 64)
	decodeErrors := make(chan error, 1)
	go func() {
		defer close(records)
		decoder := json.NewDecoder(stdout)
		for {
			var value record
			if err := decoder.Decode(&value); err != nil {
				decodeErrors <- err
				return
			}
			records <- value
		}
	}()
	fastPasses := 0
	unknownSeen, slowSeen := false, false
	await := func(outcome observation.Outcome) {
		deadline := time.NewTimer(8 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case value, open := <-records:
				if !open {
					t.Fatalf("continuous command stopped before cancellation: %v", <-decodeErrors)
				}
				if value.Result == nil {
					t.Fatal("continuous command returned an early terminal summary")
				}
				result := *value.Result
				if result.CheckID == unknown.ID {
					unknownSeen = true
					if result.Outcome != observation.OutcomeUnknown {
						t.Fatalf("observer failure became target health: %+v", result)
					}
				}
				if result.CheckID == slow.ID {
					slowSeen = true
				}
				if result.CheckID == fast.ID && result.Outcome == observation.OutcomePass {
					fastPasses++
				}
				if result.CheckID == fast.ID && result.Outcome == outcome {
					if fastPasses == 2 && slowSeen {
						t.Fatal("slow check delayed independent application checks")
					}
					return
				}
			case <-deadline.C:
				t.Fatal("continuous application verdict did not arrive")
			}
		}
	}
	await(observation.OutcomePass)
	await(observation.OutcomePass)
	failing.Store(true)
	await(observation.OutcomeFail)
	failing.Store(false)
	await(observation.OutcomePass)
	if !unknownSeen {
		t.Fatal("required unavailable observer was omitted")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var summary *observation.Summary
	for value := range records {
		if value.Summary != nil {
			summary = value.Summary
		}
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("continuous cancellation: %v: %s", err, stderr.String())
	}
	if summary == nil {
		t.Fatal("continuous cancellation omitted terminal counts")
	}
	counts := summary.Checks[fast.ID]
	if counts.Pass < 3 || counts.Fail == 0 || counts.Failures == 0 || counts.Recoveries == 0 || summary.Checks[unknown.ID].Unknown == 0 {
		t.Fatalf("terminal counts omitted application failure, recovery or unknown evidence: %+v", summary)
	}
}

func runObservationProcess(t *testing.T, binary, machineIDPath string, spec observation.CheckSpec) observation.Result {
	t.Helper()
	request, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtimeSettings, err := json.Marshal(observation.RuntimeConfig{MachineIDPath: machineIDPath, ProbeBinary: binary})
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(runtimePath, runtimeSettings, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "observe", "--check", string(request), "--runtime-settings", runtimePath)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("mwan observe command failed: %v: %s", err, stderr.String())
	}
	var result observation.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("mwan observe returned invalid result JSON: %v: %s", err, output)
	}
	if result.Path.Source.IsValid() && result.Path.Source.Is4() != (spec.Family == observation.FamilyIPv4) {
		t.Fatalf("observed source has the wrong family: %s", result.Path.Source)
	}
	return result
}
