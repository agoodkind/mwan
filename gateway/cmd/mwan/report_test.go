package main_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
)

func buildReportCommand(t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "mwan")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v\n%s", err, output)
	}
	return binaryPath
}

func runReportCommand(t *testing.T, configPath string, arguments ...string) (string, int) {
	t.Helper()
	command := exec.CommandContext(t.Context(), buildReportCommand(t), arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run report command: %v\n%s", err, output)
	}
	return string(output), exitError.ExitCode()
}

func reportCommandConfig(t *testing.T, server *testsmtp.Server) (*config.Config, string, []string) {
	t.Helper()
	state := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	rrdtool, err := exec.LookPath("rrdtool")
	if err != nil {
		t.Fatalf("install RRDtool for reporting tests: %v", err)
	}
	text := fmt.Sprintf("[report]\nstate_directory = %q\nrrdtool_path = %q\n[email]\nmsmtprc_path = %q\nalert_email = %q\n", state, rrdtool, server.Path, "recipient@example.com")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.LoadArguments([]string{"mwan", "--config", path})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, path, []string{"report", "send", "--network", "../../yang/instances/network-min.json", "--schema-dir", "../../internal/yangpub/schema"}
}

func recordReportCommandHistory(t *testing.T, cfg *config.Config, arguments []string) {
	t.Helper()
	network, err := networkload.Load(arguments[3], arguments[5])
	if err != nil {
		t.Fatal(err)
	}
	logger, closer := logging.New(logging.Config{BuildVersion: "test"})
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
	})
	manager, err := notify.New(cfg.Email, cfg.NotifyConfig(), logger, "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	service, err := reporting.NewService(reporting.Options{Config: cfg.Report, Network: network, Notifications: manager, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	for index := range 3 {
		minute := reporting.Minute{Start: start.Add(time.Duration(index) * time.Minute), End: start.Add(time.Duration(index+1) * time.Minute), Host: reporting.HostUsage{CPU: []reporting.CPUUsage{{Name: "cpu", Percent: 15, Available: true}, {Name: "cpu0", Percent: 15, Available: true}}, MemoryPercent: 30, MemoryAvailable: true}}
		for key, wan := range network.WAN {
			minute.Traffic = append(minute.Traffic, reporting.WANTraffic{WAN: connectionid.Resolve(network.ConnectionIDs[key].String(), key, wan.Iface), Downloaded: 1000, Uploaded: 500, Available: true})
		}
		if err := service.RecordMinute(t.Context(), minute); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReportCommandSendsPersistedSummary(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	cfg, configPath, arguments := reportCommandConfig(t, server)
	recordReportCommandHistory(t, cfg, arguments)
	output, code := runReportCommand(t, configPath, arguments...)
	if code != 0 {
		t.Fatalf("report command failed: %s", output)
	}
	html := testsmtp.HTML(t, server.Next(t))
	if !strings.Contains(html, "cpu-ram.png") || !strings.Contains(html, "Top talkers on") || !strings.Contains(output, "accepted") {
		t.Fatal("public command did not send the persisted report")
	}
}

func TestReportCommandRejectsInvalidAction(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	_, configPath, _ := reportCommandConfig(t, server)
	output, code := runReportCommand(t, configPath, "report", "unknown")
	if code == 0 || !strings.Contains(output, "usage:") {
		t.Fatal("unknown report action did not return usage and failure")
	}
}

func TestReportCommandReportsMissingHistory(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	_, configPath, arguments := reportCommandConfig(t, server)
	output, code := runReportCommand(t, configPath, arguments...)
	if code == 0 || !strings.Contains(output, "history is empty") {
		t.Fatalf("missing history did not fail clearly: %s", output)
	}
}
