package reporting_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/reporting"
)

func TestHostSamplerReadsLinuxUsage(t *testing.T) {
	t.Parallel()
	settings := config.DefaultReportSection()
	settings.StateDirectory = t.TempDir()
	settings.RRDToolPath = "/usr/local/bin/rrdtool"
	collector, err := reporting.NewCollector(settings, &networkjson.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	start := time.Now()
	first, err := collector.Sample(t.Context(), start, start.Add(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Host.CPU) < 2 || !first.Host.MemoryAvailable {
		t.Fatal("collector omitted visible CPUs or RAM")
	}
	for _, cpu := range first.Host.CPU {
		if cpu.Available {
			t.Fatal("first CPU reading invented a usage percentage")
		}
	}
	start = time.Now()
	second, err := collector.Sample(t.Context(), start, start.Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	for _, cpu := range second.Host.CPU {
		if !cpu.Available || cpu.Percent < 0 || cpu.Percent > 100 {
			t.Fatalf("invalid CPU measurement: %+v", cpu)
		}
	}
	if second.Host.MemoryPercent < 0 || second.Host.MemoryPercent > 100 {
		t.Fatal("RAM measurement exceeds its percentage range")
	}
}
