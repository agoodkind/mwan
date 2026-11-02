//go:build linux && firewallnetns

package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
	"goodkind.io/mwan/internal/installspec"
)

func TestDebugSystemdPublicRuntime(t *testing.T) {
	if os.Getenv("MWAN_NETWORKD_RESOLVER_SYSTEMD_TEST") != "1" {
		t.Skip("requires the isolated systemd protocol runner")
	}
	binary := buildReportCommand(t)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unitName := "mwan-cli-inspection.service"
	unitPath := filepath.Join("/run/systemd/system", unitName)
	unit := "[Unit]\nDescription=CLI inspection timing fixture\n[Service]\nType=oneshot\nExecStart=/usr/bin/sleep 0.051\nRemainAfterExit=yes\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"daemon-reload"}, {"start", unitName}} {
		if output, err := exec.CommandContext(t.Context(), "systemctl", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("systemctl %v: %v: %s", arguments, err, output)
		}
	}
	t.Cleanup(func() {
		if output, err := exec.Command("systemctl", "stop", unitName).CombinedOutput(); err != nil {
			t.Errorf("stop fixture: %v: %s", err, output)
		}
		if err := os.Remove(unitPath); err != nil {
			t.Error(err)
		}
		if output, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			t.Errorf("reload units: %v: %s", err, output)
		}
	})
	connection, err := systemddbus.NewSystemConnectionContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	units, err := connection.ListUnitsContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var expected systemddbus.UnitStatus
	for _, value := range units {
		if value.Name == unitName {
			expected = value
			break
		}
	}
	if expected.Name == "" || expected.ActiveState != "active" {
		t.Fatalf("fixture unit=%+v", expected)
	}
	var timestamps [2]uint64
	for index, property := range []string{"InactiveExitTimestampMonotonic", "ActiveEnterTimestampMonotonic"} {
		value, err := connection.GetUnitPropertyContext(t.Context(), unitName, property)
		if err != nil {
			t.Fatal(err)
		}
		if err := value.Value.Store(&timestamps[index]); err != nil {
			t.Fatal(err)
		}
	}
	duration := time.Duration(timestamps[1]-timestamps[0]) * time.Microsecond
	if duration <= 0 {
		t.Fatalf("fixture duration=%s", duration)
	}
	output, diagnostics, code := runCLIProcess(t, binary, "debug", "systemd", "--config", configPath, "--output-format", "json")
	if code != 0 {
		t.Fatalf("systemd exit=%d stdout=%s stderr=%s", code, output, diagnostics)
	}
	var result struct {
		Units   []systemddbus.UnitStatus `json:"units"`
		Timings []struct {
			Name     string        `json:"name"`
			Duration time.Duration `json:"duration_ns"`
		} `json:"timings"`
		Focus []struct {
			Name     string        `json:"name"`
			Duration time.Duration `json:"duration_ns"`
			Present  bool          `json:"present"`
		} `json:"focus"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range result.Units {
		if value.Name == unitName {
			found = reflect.DeepEqual(value, expected)
		}
	}
	if !found {
		t.Fatalf("source unit record changed: expected=%+v actual=%+v", expected, result.Units)
	}
	timingFound := false
	for _, timing := range result.Timings {
		if timing.Name == unitName {
			timingFound = timing.Duration == duration
		}
	}
	if !timingFound || len(result.Focus) != 5 {
		t.Fatalf("timings=%+v focus=%+v expected duration=%s", result.Timings, result.Focus, duration)
	}
	specification, known := installspec.For(installspec.RoleWAN)
	if !known {
		t.Fatal("installed WAN role does not exist")
	}
	for _, name := range specification.Enable {
		if name == "mwan-trace-boot.service" || name == "rousette.service" || name == "nghttpx-wanconfig.service" {
			continue
		}
		found := false
		for _, focus := range result.Focus {
			if focus.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("systemd inspection omits installed WAN unit %s", name)
		}
	}
	for _, focus := range result.Focus {
		present := false
		var observed time.Duration
		for _, timing := range result.Timings {
			if timing.Name == focus.Name {
				present, observed = true, timing.Duration
			}
		}
		if focus.Present != present || focus.Duration != observed {
			t.Fatalf("focus timing changed: %+v", focus)
		}
	}
	text, diagnostics, code := runCLIProcess(t, binary, "debug", "systemd", "--config", configPath, "--output-format", "text")
	if code != 0 || !strings.Contains(text, "== failed units ==") || !strings.Contains(text, "== units stuck in activating ==") || !strings.Contains(text, "== slowest starters (top 20) ==") || !strings.Contains(text, "== mwan unit timings ==") {
		t.Fatalf("systemd text exit=%d stdout=%s stderr=%s", code, text, diagnostics)
	}
	for _, format := range []string{"text", "json"} {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), binary, "debug", "systemd", "--config", configPath, "--output-format", format)
		var diagnostics bytes.Buffer
		command.Stdout, command.Stderr = full, &diagnostics
		err = command.Run()
		if closeErr := full.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err == nil || !strings.Contains(diagnostics.String(), "no space left on device") {
			t.Fatalf("%s write error=%v stderr=%s", format, err, diagnostics.String())
		}
	}
}
