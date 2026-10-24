package reporting_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/reporting"
)

func TestCollectorCountsGuestCPUTimeOnce(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.ProcDirectory = t.TempDir()
	if err := os.Mkdir(filepath.Join(settings.ProcDirectory, "net"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"stat":     "cpu 100 20 30 850 0 0 0 0 50 10\ncpu0 100 20 30 850 0 0 0 0 50 10\n",
		"meminfo":  "MemTotal: 1000 kB\nMemAvailable: 500 kB\n",
		"net/snmp": "Udp: SndbufErrors RcvbufErrors\nUdp: 0 0\n",
	} {
		if err := os.WriteFile(filepath.Join(settings.ProcDirectory, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	collector, err := reporting.NewCollector(settings, &networkjson.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	start := time.Now()
	if _, err := collector.Sample(t.Context(), start, start.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// Busy and idle time each increase equally; guest counters repeat part of busy time.
	next := "cpu 160 30 40 930 0 0 0 0 80 15\ncpu0 160 30 40 930 0 0 0 0 80 15\n"
	if err := os.WriteFile(filepath.Join(settings.ProcDirectory, "stat"), []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	minute, err := collector.Sample(t.Context(), start, start.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(minute.Host.CPU) != 2 {
		t.Fatalf("collector omitted overall or per-core CPU: %+v", minute.Host.CPU)
	}
	for _, cpu := range minute.Host.CPU {
		if !cpu.Available || cpu.Percent != 50 {
			t.Fatalf("guest time changed measured CPU usage: %+v", cpu)
		}
	}
}
