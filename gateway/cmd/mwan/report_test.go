package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
)

func reportCommandConfig(t *testing.T, server *testsmtp.Server) (*config.Config, []string) {
	t.Helper()
	state := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	text := fmt.Sprintf("[report]\nstate_directory = %q\nrrdtool_path = %q\n[email]\nmsmtprc_path = %q\nalert_email = %q\n", state, "/usr/local/bin/rrdtool", server.Path, "recipient@example.com")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.LoadArguments([]string{"mwan", "--config", path})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, []string{"send", "--network", "../../yang/instances/network-min.json", "--schema-dir", "../../internal/yangpub/schema"}
}

func TestReportCommandSendsPersistedSummary(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	cfg, arguments := reportCommandConfig(t, server)
	network, err := networkload.Load(arguments[2], arguments[4])
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	var output, diagnostics bytes.Buffer
	if code := runReport(t.Context(), arguments, cfg, &output, &diagnostics); code != 0 {
		t.Fatalf("report command failed: %s", diagnostics.String())
	}
	html := testsmtp.HTML(t, server.Next(t))
	if !strings.Contains(html, "cpu-ram.png") || !strings.Contains(html, "Top talkers on") || !strings.Contains(output.String(), "accepted") {
		t.Fatal("public command did not send the persisted report")
	}
}

func TestReportCommandRejectsInvalidAction(t *testing.T) {
	t.Parallel()
	var output, diagnostics bytes.Buffer
	if code := runReport(t.Context(), []string{"unknown"}, nil, &output, &diagnostics); code == 0 || !strings.Contains(diagnostics.String(), "usage:") {
		t.Fatal("unknown report action did not return usage and failure")
	}
}

func TestReportCommandReportsMissingHistory(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	cfg, arguments := reportCommandConfig(t, server)
	var output, diagnostics bytes.Buffer
	if code := runReport(t.Context(), arguments, cfg, &output, &diagnostics); code == 0 || !strings.Contains(diagnostics.String(), "history is empty") {
		t.Fatalf("missing history did not fail clearly: %s", diagnostics.String())
	}
}
