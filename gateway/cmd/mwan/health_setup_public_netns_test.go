//go:build linux && firewallnetns

package main_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestConfiguredHealthSetupNamespaceRuntime(t *testing.T) {
	runDebugProbeNamespaceTest(t, runConfiguredHealthSetupNamespaceRuntime)
}

func runConfiguredHealthSetupNamespaceRuntime(t *testing.T, binary string) {
	fixture := newHealthFixture(t)
	for _, family := range []string{"inet", "inet6"} {
		socketError := "Ping4 ListenPacket: listen ip4:icmp 0.0.0.0: socket: operation not permitted"
		bindingError := `bind socket to interface "health-missing0"`
		policy := healthPolicy([]string{})
		policy.PingCount = new(1)
		policy.TargetsV4, policy.TargetsV6 = []string{"127.0.0.1"}, []string{}
		if family == "inet6" {
			socketError = "ListenPacket: listen ip6:ipv6-icmp ::: socket: operation not permitted"
			bindingError = `bind v6 probe: setsockopt SO_BINDTODEVICE "health-missing0"`
			policy.TargetsV4, policy.TargetsV6 = []string{}, []string{"::1"}
		}
		fixture.policy(t, "webpass", "lo", policy)
		fixture.save(t, 100)
		for _, watch := range []bool{false, true} {
			for _, format := range []string{"text", "json"} {
				arguments := []string{
					"--bounding-set=-net_raw",
					"--",
					binary,
					"health",
					"--config",
					fixture.config,
					"--output-format",
					format,
				}
				if watch {
					arguments = append(arguments, "--watch")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				command := exec.CommandContext(ctx, "setpriv", arguments...)
				var output, diagnostics bytes.Buffer
				command.Stdout, command.Stderr = &output, &diagnostics
				err := command.Run()
				cancel()
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) || exitError.ExitCode() != 1 || output.Len() != 0 ||
					strings.Contains(diagnostics.String(), "Usage:") ||
					!strings.Contains(diagnostics.String(), socketError) {
					t.Fatalf(
						"raw socket setup family=%s watch=%t format=%s error=%v output=%s diagnostics=%s",
						family,
						watch,
						format,
						err,
						output.String(),
						diagnostics.String(),
					)
				}
			}
		}
		fixture.policy(t, "webpass", "health-missing0", policy)
		fixture.save(t, 100)
		command := healthCommand(t, binary, fixture, "--output-format", "json")
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		err := command.Run()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 || output.Len() != 0 ||
			!strings.Contains(diagnostics.String(), bindingError) {
			t.Fatalf(
				"socket binding family=%s error=%v output=%s diagnostics=%s",
				family,
				err,
				output.String(),
				diagnostics.String(),
			)
		}
	}
	policy := healthPolicy([]string{})
	policy.PingCount, policy.TargetsV4, policy.TargetsV6 = new(1), []string{"127.0.0.1"}, []string{}
	for _, target := range []string{"http://127.0.0.1:invalid", "fixture://127.0.0.1"} {
		policy.HTTPURLs = []string{target}
		fixture.policy(t, "webpass", "lo", policy)
		fixture.save(t, 100)
		command := healthCommand(t, binary, fixture, "--output-format", "json")
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		err := command.Run()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 || output.Len() != 0 ||
			strings.Contains(diagnostics.String(), "Usage:") ||
			!strings.Contains(diagnostics.String(), "HTTPGet:") {
			t.Fatalf(
				"HTTP setup target=%s error=%v output=%s diagnostics=%s",
				target,
				err,
				output.String(),
				diagnostics.String(),
			)
		}
	}
	server := httptest.NewServer(
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
		}),
	)
	defer server.Close()
	policy.HTTPURLs = []string{server.URL}
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 50)
	records, diagnostics := runHealthFixture(t, binary, fixture)
	if len(records) != 2 || records[1].HTTP == nil || records[1].HTTP.Error == "" ||
		diagnostics != "" {
		t.Fatalf(
			"HTTP timeout became a setup failure: records=%+v diagnostics=%s",
			records,
			diagnostics,
		)
	}
}
