//go:build linux

package reporting_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/mwan/internal/vpp"
)

func TestCollectorRetainsVPPProfileSettings(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector, err := reporting.NewCollector(settings, &networkjson.Config{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	directory := t.TempDir()
	section := &config.VPPSection{
		APISocket: filepath.Join(directory, "api.sock"), StatsSocket: filepath.Join(directory, "stats.sock"),
		ExpectedVersion: vpp.BindingVersion, RequestTimeout: "1s", StateFile: filepath.Join(directory, "state.json"),
		Profile: true, ProfileExecutable: filepath.Join(directory, "vpp"), ProfileDuration: "1ms",
	}
	if err := collector.ConfigureVPP(section); err != nil {
		t.Fatal(err)
	}
	section.RequestTimeout = "invalid"
	section.ProfileDuration = "invalid"
	section.ProfileExecutable = "relative"
	start := time.Now()
	minute, err := collector.Sample(t.Context(), start, start.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	profile := minute.VPPProfile
	if profile == nil || profile.Available || profile.Duration != time.Millisecond {
		t.Fatalf("unavailable VPP process did not preserve the configured profile duration: %+v", profile)
	}
	if !strings.Contains(profile.Reason, section.APISocket) {
		t.Fatalf("VPP profiling did not attempt the configured socket: %q", profile.Reason)
	}
}

func TestWatchWaitsForWriterAndResumesCollection(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.SampleInterval = "1s"
	settings.BreachWindow = "1s"
	settings.RecoveryInterval = "1s"
	settings.MinProbeSeconds = 1
	network := reportNetwork()
	server := testsmtp.Start(t)
	logger, writerWaiting, logErrors := reportingWatchLogger(t)
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
	var testContext context.Context
	var finishTest context.CancelFunc
	if deadline, limited := t.Deadline(); limited {
		testContext, finishTest = context.WithDeadline(t.Context(), deadline)
	} else {
		testContext, finishTest = context.WithCancel(t.Context())
	}
	defer finishTest()
	ctx, cancel := context.WithCancel(testContext)
	defer cancel()
	finished := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { finished <- service.Watch(ctx, collector) })
	defer func() {
		cancel()
		workers.Wait()
	}()
	waitForReportingState(t, testContext, settings.StateDirectory, finished)
	received, release := server.PauseDelivery()
	defer release()
	delivery := make(chan error, 1)
	workers.Go(func() { delivery <- service.SendDaily(ctx) })
	select {
	case <-received:
	case err := <-delivery:
		t.Fatalf("summary completed before SMTP paused delivery: %v", err)
	case err := <-finished:
		t.Fatalf("monitor stopped before SMTP received the summary: %v", err)
	case err := <-logErrors:
		t.Fatalf("reporting log capture stopped before SMTP received the summary: %v", err)
	case <-testContext.Done():
		t.Fatalf("SMTP did not receive the summary before the test deadline: %v", testContext.Err())
	}
	// The warning follows a failed nonblocking write to the real writer queue.
	select {
	case <-writerWaiting:
	case err := <-delivery:
		t.Fatalf("summary completed before collection waited for the writer: %v", err)
	case err := <-finished:
		t.Fatalf("monitor stopped before collection waited for the writer: %v", err)
	case err := <-logErrors:
		t.Fatalf("reporting log capture stopped before collection waited for the writer: %v", err)
	case <-testContext.Done():
		t.Fatalf("collection did not wait for the writer before the test deadline: %v", testContext.Err())
	}
	releasedAt := time.Now()
	release()
	select {
	case err := <-delivery:
		if err != nil {
			t.Fatalf("summary delivery failed: %v", err)
		}
	case <-testContext.Done():
		t.Fatalf("summary did not complete before the test deadline: %v", testContext.Err())
	}
	resumed := waitForReportingRecovery(t, testContext, settings.StateDirectory, finished, releasedAt)
	if duration := resumed.End.Sub(resumed.Start); duration <= 0 || duration >= time.Second {
		t.Fatalf("resumed interval must contain only the observed part of the second: %+v", resumed)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("monitor cancellation returned %v", err)
		}
	case <-testContext.Done():
		t.Fatalf("monitor did not stop after cancellation before the test deadline: %v", testContext.Err())
	}
}

func reportingWatchLogger(t *testing.T) (*slog.Logger, <-chan struct{}, <-chan error) {
	t.Helper()
	reader, writer := io.Pipe()
	waiting := make(chan struct{}, 1)
	failures := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `msg="reporting collection is waiting for the writer queue"`) {
				select {
				case waiting <- struct{}{}:
				default:
				}
			}
		}
		failures <- scanner.Err()
	})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
		workers.Wait()
	})
	return slog.New(slog.NewTextHandler(writer, nil)), waiting, failures
}

func waitForReportingRecovery(t *testing.T, ctx context.Context, directory string, finished <-chan error, releasedAt time.Time) reporting.Minute {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-finished:
			t.Fatalf("monitor stopped after writer contention: %v", err)
		case <-ctx.Done():
			t.Fatalf("monitor did not persist a partial interval collected after SMTP release before the test deadline: %v", ctx.Err())
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

func waitForReportingState(t *testing.T, ctx context.Context, directory string, finished <-chan error) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-finished:
			t.Fatalf("monitor stopped before recording: %v", err)
		case <-ctx.Done():
			t.Fatalf("monitor did not record an interval before the test deadline: %v", ctx.Err())
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
