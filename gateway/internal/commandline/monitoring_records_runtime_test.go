//go:build linux && protocol

package commandline

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/observation"
)

func TestObservationExecutorRuntime(t *testing.T) {
	binary := protocolTestBinary(t)
	machineID := "runtime-observation-machine"
	machineIDPath := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(machineIDPath, []byte(machineID), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
		t.Run(
			string(family),
			func(t *testing.T) {
				network, address := "tcp4", "127.0.0.1:0"
				if family == observation.FamilyIPv6 {
					network, address = "tcp6", "[::1]:0"
				}
				listener, err := net.Listen(network, address)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewUnstartedServer(
					http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						if request.URL.Path == "/timeout" {
							<-request.Context().Done()
							return
						}
						if request.URL.Path == "/fail" {
							writer.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						_, _ = writer.Write([]byte("healthy"))
					}),
				)
				server.Listener = listener
				server.Start()
				defer server.Close()
				source := listener.Addr().(*net.TCPAddr).AddrPort().Addr().Unmap()
				spec := observation.CheckSpec{
					ID:        "downstream-application",
					Dimension: observation.DimensionDownstreamApplication,
					Operation: observation.OperationHTTP,
					Observer: observation.Endpoint{
						Kind:      observation.EndpointLocal,
						MachineID: machineID,
					},
					Family:             family,
					Source:             source,
					Target:             server.URL,
					TimeoutSeconds:     2,
					MaxAgeSeconds:      30,
					ExpectedHTTPStatus: []int{http.StatusOK},
					ExpectedBody:       "healthy",
				}
				exerciseContinuousObservation(t, binary, machineIDPath, spec)
				passed := runLocalObservation(t, machineIDPath, spec)
				if !observation.RequiredPassed(spec, passed, time.Now()) ||
					passed.Path.Source != source ||
					passed.Path.Interface != "lo" {
					t.Fatalf("source-bound application reply did not pass: %+v", passed)
				}
				spec.Target = server.URL + "/fail"
				failed := runLocalObservation(t, machineIDPath, spec)
				if failed.Availability != observation.AvailabilityComplete ||
					failed.Outcome != observation.OutcomeFail ||
					failed.HTTPStatus != http.StatusServiceUnavailable {
					t.Fatalf("unhealthy application response did not fail: %+v", failed)
				}
				spec.Target = server.URL + "/timeout"
				timedOut := runLocalObservation(t, machineIDPath, spec)
				if timedOut.Availability != observation.AvailabilityComplete ||
					timedOut.Outcome != observation.OutcomeFail ||
					timedOut.Reason != "target reply timed out" {
					t.Fatalf("target timeout became missing observation: %+v", timedOut)
				}
				spec.Observer.MachineID = "another-machine"
				missing := runLocalObservation(t, machineIDPath, spec)
				if missing.Availability != observation.AvailabilityError ||
					missing.Outcome != observation.OutcomeUnknown {
					t.Fatalf("wrong observer identity became application failure: %+v", missing)
				}
				spec.Observer.MachineID = machineID
				spec.Operation, spec.Dimension, spec.Target = observation.OperationPing, observation.DimensionPingPath, source.String()
				spec.ExpectedHTTPStatus, spec.ExpectedBody = nil, ""
				ping := runLocalObservation(t, machineIDPath, spec)
				if !observation.RequiredPassed(
					spec,
					ping,
					time.Now(),
				) || ping.Path.Source != source ||
					ping.Path.Destination != source {
					t.Fatalf("ICMP reply omitted its actual source: %+v", ping)
				}
				if observation.RequiredPassed(spec, ping, ping.ObservedAt.Add(31*time.Second)) {
					t.Fatal("expired observation passed acceptance")
				}
			},
		)
	}
}

func exerciseContinuousObservation(
	t *testing.T,
	binary string,
	machineIDPath string,
	original observation.CheckSpec,
) {
	t.Helper()
	var failing atomic.Bool
	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/slow" {
				<-request.Context().Done()
				return
			}
			if failing.Load() {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = writer.Write([]byte("healthy"))
		}),
	)
	defer server.Close()
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
	settings := observation.ContinuousSettings{
		Runtime: observation.RuntimeConfig{MachineIDPath: machineIDPath, ProbeBinary: binary},
		Checks: []observation.ScheduledCheck{
			{Check: fast, IntervalSeconds: 1},
			{Check: slow, IntervalSeconds: 1},
			{Check: unknown, IntervalSeconds: 1},
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	results := make(chan observation.Result, 64)
	type completion struct {
		summary observation.Summary
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		summary, err := observation.RunContinuous(
			ctx,
			settings,
			notify.NullNotifier{},
			slog.Default(),
			func(_ observation.CheckSpec, result observation.Result) error {
				results <- result
				return nil
			},
		)
		done <- completion{summary: summary, err: err}
	}()
	fastPasses := 0
	unknownSeen, slowSeen := false, false
	await := func(outcome observation.Outcome) {
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			select {
			case result := <-results:
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
			case <-timer.C:
				t.Fatalf("continuous application verdict %s did not arrive", outcome)
			case completed := <-done:
				t.Fatalf("continuous observations stopped early: %v", completed.err)
			case <-ctx.Done():
				t.Fatalf("continuous observations stopped early: %v", ctx.Err())
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
	cancel()
	completed := <-done
	if completed.err != nil {
		t.Fatalf("continuous observations: %v", completed.err)
	}
	counts := completed.summary.Checks[fast.ID]
	if counts.Pass < 3 || counts.Fail == 0 || counts.Failures == 0 || counts.Recoveries == 0 ||
		completed.summary.Checks[unknown.ID].Unknown == 0 {
		t.Fatalf(
			"summary omitted application failure, recovery, or unknown evidence: %+v",
			completed.summary,
		)
	}
}

func runLocalObservation(
	t *testing.T,
	machineIDPath string,
	spec observation.CheckSpec,
) observation.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	executor := observation.NewExecutor(
		observation.RuntimeConfig{
			MachineIDPath: machineIDPath,
		},
		nil,
	)
	result := executor.Run(ctx, spec)
	if result.Path.Source.IsValid() &&
		result.Path.Source.Is4() != (spec.Family == observation.FamilyIPv4) {
		t.Fatalf("observed source has the wrong family: %s", result.Path.Source)
	}
	return result
}
