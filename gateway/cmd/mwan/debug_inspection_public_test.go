package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugSystemdPublicConnectionFailure(t *testing.T) {
	binary := buildReportCommand(t)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(t.TempDir(), "missing-dbus")

	for _, format := range []string{"text", "json"} {
		command := exec.CommandContext(
			t.Context(),
			binary,
			"debug",
			"systemd",
			"--config",
			configPath,
			"--output-format",
			format,
		)
		command.Env = append(os.Environ(), "DBUS_SYSTEM_BUS_ADDRESS=unix:path="+socket)
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		err := command.Run()
		if err == nil || output.Len() != 0 ||
			!strings.Contains(diagnostics.String(), "connect to systemd dbus:") ||
			!strings.Contains(diagnostics.String(), socket) {
			t.Fatalf(
				"%s connection error=%v stdout=%s stderr=%s",
				format,
				err,
				output.String(),
				diagnostics.String(),
			)
		}
	}
}
