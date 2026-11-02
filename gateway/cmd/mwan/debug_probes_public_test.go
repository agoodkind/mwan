package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/netif"
)

func TestCLIActiveProbeFailuresRemainResultData(t *testing.T) {
	binary := buildReportCommand(t)
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"ping4", "ping6"} {
		t.Run(view, func(t *testing.T) {
			output, diagnostics, code := runCLIProcess(t, binary, "debug", view, "mwan-no-iface", "--config", config, "--output-format", "json")
			var attempts []netif.PingAttempt
			if code != 0 || json.Unmarshal([]byte(output), &attempts) != nil || len(attempts) != 6 || strings.Count(output, "\n") != 1 {
				t.Fatalf("exit=%d output=%s diagnostics=%s", code, output, diagnostics)
			}
			family, targets := "inet", []string{"1.1.1.1", "8.8.8.8"}
			if view == "ping6" {
				family, targets = "inet6", []string{"2606:4700:4700::1111", "2620:fe::9"}
			}
			for index, attempt := range attempts {
				if attempt.Interface != "mwan-no-iface" || attempt.Family != family || attempt.Target != targets[index/3] || attempt.Attempt != index%3+1 || attempt.Timeout != 5*time.Second || attempt.RTT != 0 || attempt.Error == "" {
					t.Fatalf("attempt=%+v", attempt)
				}
			}
			output, diagnostics, code = runCLIProcess(t, binary, "debug", view, "mwan-no-iface", "--config", config, "--output-format", "text")
			if code != 0 || strings.Count(output, "FAIL: ") != 6 || strings.Count(output, "3 transmitted, 0 received") != 2 {
				t.Fatalf("exit=%d output=%s diagnostics=%s", code, output, diagnostics)
			}
			for _, attempt := range attempts {
				if !strings.Contains(output, attempt.Error) {
					t.Fatalf("text omitted raw error %q: %s", attempt.Error, output)
				}
			}
		})
	}
}

func TestCLIActiveProbeOutputWriteErrors(t *testing.T) {
	binary := buildReportCommand(t)
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"ping4", "ping6"} {
		for _, format := range []string{"text", "json"} {
			t.Run(view+"/"+format, func(t *testing.T) {
				full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer full.Close()
				arguments := []string{"debug", view, "--config", config, "--output-format", format}
				arguments = append(arguments, "mwan-no-iface")
				command := exec.CommandContext(t.Context(), binary, arguments...)
				command.Stdout = full
				var diagnostics bytes.Buffer
				command.Stderr = &diagnostics
				err = command.Run()
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) || exitError.ExitCode() != 1 || !strings.Contains(diagnostics.String(), "no space left on device") {
					t.Fatalf("output write error=%v diagnostics=%s", err, diagnostics.String())
				}
			})
		}
	}
}
