//go:build linux

package reporting_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
)

func TestWatchWaitsForWriterAndResumesCollection(t *testing.T) {
	t.Setenv("RRD_LOCKING", "block")
	settings := config.DefaultReportSection()
	settings.StateDirectory = t.TempDir()
	path, err := exec.LookPath("rrdtool")
	if err != nil {
		t.Fatal(err)
	}
	settings.RRDToolPath = path
	settings.SampleInterval = "1s"
	settings.BreachWindow = "1s"
	settings.RecoveryInterval = "1s"
	settings.MinProbeSeconds = 1
	network := &networkjson.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifications, err := notify.New(notify.EmailConfig{}, notify.Config{}, logger, "mwan-report")
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
	digest := sha256.Sum256([]byte("memory"))
	locked, err := os.OpenFile(filepath.Join(settings.StateDirectory, "rrd", fmt.Sprintf("host-%x.rrd", digest)), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	// Delay actual RRD writes with a filesystem lock until the collection queue fills.
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0, Pid: 0}
	if err := unix.FcntlFlock(locked.Fd(), unix.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
	time.Sleep(11 * time.Second)
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		t.Fatalf("monitor stopped after writer contention: %v", err)
	case <-time.After(2 * time.Second):
	}
	data, err := os.ReadFile(filepath.Join(settings.StateDirectory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct{ LastMinute time.Time }
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if time.Since(state.LastMinute) > 2*time.Second {
		t.Fatalf("monitor did not resume collection: last observation %s", state.LastMinute)
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
