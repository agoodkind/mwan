package main_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredHealthArguments(t *testing.T) {
	binary := buildReportCommand(t)
	missing := filepath.Join(t.TempDir(), "absent.toml")
	output, diagnostics, code := runCLIProcess(
		t,
		binary,
		"health",
		"--config",
		missing,
		"--watch",
		"--help",
	)
	if code != 0 || diagnostics != "" || !strings.Contains(output, "--output-format") ||
		!strings.Contains(output, "--watch") {
		t.Fatalf("health help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}

	for _, arguments := range [][]string{
		{"health", "--config", missing, "--output-format", "invalid"},
		{"health", "extra"},
		{"health", "--config", ""},
	} {
		output, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 1 || output != "" || strings.Count(diagnostics, "Usage:") != 1 ||
			strings.Contains(diagnostics, "read config") {
			t.Fatalf(
				"health arguments=%v exit=%d output=%s diagnostics=%s",
				arguments,
				code,
				output,
				diagnostics,
			)
		}
	}

	output, diagnostics, code = runCLIProcess(t, binary, "health", "--config", missing)
	if code != 1 || output != "" || !strings.Contains(diagnostics, missing) ||
		strings.Contains(diagnostics, "Usage:") {
		t.Fatalf("health configuration exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
}
