//go:build linux

package reporting_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
)

func TestWatchWaitsForWriterAndResumesCollection(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.SampleInterval = "1s"
	settings.BreachWindow = "1s"
	settings.RecoveryInterval = "1s"
	settings.MinProbeSeconds = 1
	network := reportNetwork()
	server := testsmtp.Start(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifications, err := notify.New(notify.EmailConfig{MsmtprcPath: server.Path, AlertEmail: "recipient@example.com"}, notify.Config{}, logger, "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	service, err := reporting.NewService(reporting.Options{Config: settings, Network: network, Notifications: notifications, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	collector, err := reporting.NewCollector(settings, network, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- service.Watch(ctx, collector) }()
	waitForReportingState(t, settings.StateDirectory, finished)
	received, release := server.PauseDelivery()
	defer release()
	delivery := make(chan error, 1)
	go func() { delivery <- service.SendDaily(ctx) }()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP did not receive the summary")
	}
	// Delay the actual mail response until collection fills the writer queue.
	time.Sleep(11 * time.Second)
	releasedAt := time.Now()
	release()
	if err := <-delivery; err != nil {
		t.Fatalf("summary delivery failed: %v", err)
	}
	resumed := waitForReportingRecovery(t, settings.StateDirectory, finished, releasedAt)
	if duration := resumed.End.Sub(resumed.Start); duration <= 0 || duration >= time.Second {
		t.Fatalf("resumed interval must contain only the observed part of the second: %+v", resumed)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("monitor cancellation returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
}

func waitForReportingRecovery(t *testing.T, directory string, finished <-chan error, releasedAt time.Time) reporting.Minute {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-finished:
			t.Fatalf("monitor stopped after writer contention: %v", err)
		case <-timeout.C:
			t.Fatal("monitor did not persist a partial interval collected after SMTP release")
		case <-ticker.C:
			data, err := os.ReadFile(filepath.Join(directory, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var state struct{ LastMinute time.Time }
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if !state.LastMinute.After(releasedAt) {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(directory, "history"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(directory, "history", entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				var minute reporting.Minute
				if err := json.Unmarshal(data, &minute); err != nil {
					t.Fatal(err)
				}
				if minute.Partial && !minute.Start.Before(releasedAt) && !minute.End.After(state.LastMinute) {
					return minute
				}
			}
		}
	}
}

func waitForReportingState(t *testing.T, directory string, finished <-chan error) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-finished:
			t.Fatalf("monitor stopped before recording: %v", err)
		case <-timeout.C:
			t.Fatal("monitor did not record an interval")
		case <-ticker.C:
			_, err := os.Stat(filepath.Join(directory, "state.json"))
			if err == nil {
				return
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
}
