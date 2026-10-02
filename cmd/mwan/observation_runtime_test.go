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
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/observation"
)

func TestObservationDaemonRuntime(t *testing.T) {
	binary := protocolTestBinary(t)
	machineID, err := os.ReadFile("/etc/machine-id")
	if os.IsNotExist(err) {
		if output, setupErr := exec.Command("systemd-machine-id-setup").CombinedOutput(); setupErr != nil {
			t.Fatalf("initialize observer OS identity: %v: %s", setupErr, output)
		}
		machineID, err = os.ReadFile("/etc/machine-id")
	}
	if err != nil {
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
				Operation: observation.OperationHTTP, Observer: observation.Endpoint{Kind: observation.EndpointLocal, MachineID: strings.TrimSpace(string(machineID))},
				Family: family, Source: source, Target: server.URL, TimeoutSeconds: 2, MaxAgeSeconds: 30,
				ExpectedHTTPStatus: []int{http.StatusOK}, ExpectedBody: "healthy",
			}
			passed := runObservationProcess(t, binary, spec)
			if !observation.RequiredPassed(spec, passed, time.Now()) || passed.Path.Source != source || passed.Path.Interface != "lo" {
				t.Fatalf("source-bound application reply did not pass: %+v", passed)
			}
			spec.Target = server.URL + "/fail"
			failed := runObservationProcess(t, binary, spec)
			if failed.Availability != observation.AvailabilityComplete || failed.Outcome != observation.OutcomeFail || failed.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("unhealthy application response did not fail: %+v", failed)
			}
			spec.Target = server.URL + "/timeout"
			timedOut := runObservationProcess(t, binary, spec)
			if timedOut.Availability != observation.AvailabilityComplete || timedOut.Outcome != observation.OutcomeFail || timedOut.Reason != "target reply timed out" {
				t.Fatalf("target timeout became missing observation: %+v", timedOut)
			}
			spec.Observer.MachineID = "another-machine"
			missing := runObservationProcess(t, binary, spec)
			if missing.Availability != observation.AvailabilityError || missing.Outcome != observation.OutcomeUnknown {
				t.Fatalf("wrong observer identity became application failure: %+v", missing)
			}
			spec.Observer.MachineID = strings.TrimSpace(string(machineID))
			spec.Operation, spec.Dimension, spec.Target = observation.OperationPing, observation.DimensionPingPath, source.String()
			spec.ExpectedHTTPStatus, spec.ExpectedBody = nil, ""
			ping := runObservationProcess(t, binary, spec)
			if !observation.RequiredPassed(spec, ping, time.Now()) || ping.Path.Source != source || ping.Path.Destination != source {
				t.Fatalf("ICMP reply omitted its actual source: %+v", ping)
			}
			if observation.RequiredPassed(spec, ping, ping.ObservedAt.Add(31*time.Second)) {
				t.Fatal("expired observation passed acceptance")
			}
		})
	}
}

func runObservationProcess(t *testing.T, binary string, spec observation.CheckSpec) observation.Result {
	t.Helper()
	request, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "observe", "--check", string(request))
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
