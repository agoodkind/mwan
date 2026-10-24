package reporting_test

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
)

func reportService(t *testing.T, settings config.ReportSection, server *testsmtp.Server) *reporting.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := notify.New(notify.EmailConfig{MsmtprcPath: server.Path, AlertEmail: "recipient@example.com"}, notify.Config{}, logger, "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	service, err := reporting.NewService(reporting.Options{Config: settings, Network: reportNetwork(), Notifications: manager, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func incidentMinute(start time.Time, index int, bad ...connectionid.ID) reporting.Minute {
	minute := reporting.Minute{Start: start.Add(time.Duration(index) * time.Minute), End: start.Add(time.Duration(index+1) * time.Minute), Host: reporting.HostUsage{CPU: []reporting.CPUUsage{{Name: "cpu", Percent: 12, Available: true}}, MemoryPercent: 42, MemoryAvailable: true}}
	for _, id := range []connectionid.ID{"stable-sonic", "stable-etheric"} {
		probe := reporting.ProbeMinute{Key: reporting.ProbeKey{WAN: id, Family: reporting.IPv4, Target: reporting.Gateway, Mode: reporting.Fresh}, Available: true, Sent: 60, Replies: 60, LatencySum: 600 * time.Millisecond, LatencyMin: 10 * time.Millisecond, LatencyMax: 10 * time.Millisecond}
		for _, failed := range bad {
			if id == failed {
				probe.Lost = 60
				probe.Replies = 0
				probe.LatencySum = 0
			}
		}
		minute.Probes = append(minute.Probes, probe)
		minute.Traffic = append(minute.Traffic, reporting.WANTraffic{WAN: id, Available: true, Downloaded: 1000000, Uploaded: 100000})
	}
	return minute
}

func TestServiceCombinesWANIncidentsAndWaitsForRecovery(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.BreachWindow = "2m"
	settings.ReminderInterval = "3m"
	settings.RecoveryInterval = "3m"
	server := testsmtp.Start(t)
	service := reportService(t, settings, server)
	defer service.Close()
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for index := range 12 {
		var bad []connectionid.ID
		if index < 3 {
			bad = append(bad, "stable-sonic")
		}
		if index < 7 {
			bad = append(bad, "stable-etheric")
		}
		if err := service.RecordMinute(t.Context(), incidentMinute(start, index, bad...)); err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			opening := testsmtp.HTML(t, server.Next(t))
			if !strings.Contains(opening, "Sonic") || !strings.Contains(opening, "Etheric") || !strings.Contains(opening, "Current incident") || !strings.Contains(opening, "Other WANs for comparison") {
				t.Fatal("combined opening omitted a WAN or comparison section")
			}
		}
		if index == 4 || index == 7 {
			reminder := string(server.Next(t))
			if !strings.Contains(reminder, "MWAN reminder report") {
				t.Fatal("partial recovery closed the combined incident")
			}
		}
		if index == 10 {
			recovery := string(server.Next(t))
			if !strings.Contains(recovery, "MWAN recovery report") {
				t.Fatal("recovery did not wait for every WAN")
			}
		}
	}
	select {
	case <-server.Messages:
		t.Fatal("service sent an extra opening or recovery email")
	default:
	}
}

func TestServiceIgnoresPartialStartupMinute(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.BreachWindow = "1m"
	server := testsmtp.Start(t)
	service := reportService(t, settings, server)
	defer service.Close()
	minute := incidentMinute(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), 0, "stable-sonic")
	minute.Partial = true
	minute.Start = minute.Start.Add(30 * time.Second)
	if err := service.RecordMinute(t.Context(), minute); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Messages:
		t.Fatal("partial startup interval opened an incident")
	default:
	}
}

func TestServiceRetriesAfterRestart(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.BreachWindow = "1m"
	server := testsmtp.Start(t)
	server.Reject(true)
	service := reportService(t, settings, server)
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 0, "stable-sonic")); err == nil {
		t.Fatal("rejected report did not return a delivery error")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	server.Reject(false)
	service = reportService(t, settings, server)
	defer service.Close()
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 1, "stable-sonic")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(server.Next(t)), "MWAN incident report") {
		t.Fatal("restart did not retry the pending incident")
	}
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 2, "stable-sonic")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Messages:
		t.Fatal("service repeated an acknowledged report")
	default:
	}
}

func TestManualSummaryDoesNotCloseReportDay(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	server := testsmtp.Start(t)
	service := reportService(t, settings, server)
	defer service.Close()
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 0)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(settings.StateDirectory, "state.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SendDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	server.Next(t)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("manual summary changed the scheduled report state")
	}
}
