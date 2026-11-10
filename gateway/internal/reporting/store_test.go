package reporting_test

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/reporting"
)

func fetchDownloadRates(t *testing.T, settings config.ReportSection, id connectionid.ID, start, end time.Time) map[int64]float64 {
	t.Helper()
	digest := sha256.Sum256([]byte(id))
	path := filepath.Join(settings.StateDirectory, "rrd", fmt.Sprintf("%x.rrd", digest))
	command := exec.CommandContext(t.Context(), settings.RRDToolPath, "fetch", path, "AVERAGE", "--start", strconv.FormatInt(start.Unix(), 10), "--end", strconv.FormatInt(end.Unix(), 10), "--resolution", "60")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch WAN download rates: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	column := -1
	for index, source := range strings.Fields(lines[0]) {
		if source == "down" {
			column = index + 1
		}
	}
	if column < 0 {
		t.Fatalf("RRDtool fetch omitted the download source: %s", output)
	}
	rates := make(map[int64]float64)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) <= column {
			t.Fatalf("RRDtool fetch omitted a download value: %q", line)
		}
		timestamp, err := strconv.ParseInt(strings.TrimSuffix(fields[0], ":"), 10, 64)
		if err != nil {
			t.Fatalf("parse RRD interval timestamp %q: %v", fields[0], err)
		}
		value := math.NaN()
		if !strings.Contains(strings.ToLower(fields[column]), "nan") {
			value, err = strconv.ParseFloat(fields[column], 64)
			if err != nil {
				t.Fatalf("parse RRD download rate %q: %v", fields[column], err)
			}
		}
		rates[timestamp] = value
	}
	return rates
}

func TestStoreLeavesUnobservedIntervalUnknownAfterNextGaugeUpdate(t *testing.T) {
	t.Parallel()
	settings, network := reportSettings(t), reportNetwork()
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	store := recordHistory(t, settings, network, start, 1)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	resumed := incidentMinute(start, 2)
	resumed.Traffic[0].Downloaded = 2400000
	if err := store.Record(t.Context(), resumed); err != nil {
		t.Fatal(err)
	}
	rates := fetchDownloadRates(t, settings, "stable-sonic", start, resumed.End)
	for _, expected := range []struct {
		end  time.Time
		rate float64
	}{
		{end: start.Add(time.Minute), rate: 1000000.0 / 60},
		{end: resumed.End, rate: 2400000.0 / 60},
	} {
		actual, exists := rates[expected.end.Unix()]
		if !exists || math.IsNaN(actual) || math.Abs(actual-expected.rate) > 0.001 {
			t.Fatalf("download rate at %s = %v, present = %v, want %v", expected.end, actual, exists, expected.rate)
		}
	}
	missingEnd := start.Add(2 * time.Minute)
	missing, exists := rates[missingEnd.Unix()]
	if !exists || !math.IsNaN(missing) {
		t.Fatalf("unobserved interval ending %s = %v, present = %v, want NaN", missingEnd, missing, exists)
	}
}

func TestStoreReportCoverageDetectsGapsAndUnavailableWANCounters(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name        string
		gap         bool
		unavailable bool
		coverage    string
	}{
		{name: "complete", coverage: "Complete"},
		{name: "gap", gap: true, coverage: "Partial"},
		{name: "unavailable WAN counters", unavailable: true, coverage: "Partial"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			settings, network := reportSettings(t), reportNetwork()
			location, err := time.LoadLocation(settings.TimeZone)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 10, 20, settings.SummaryHour, 0, 0, 0, location)
			store := recordHistory(t, settings, network, start, 1)
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			for index := 1; index < 3; index++ {
				if scenario.gap && index == 1 {
					continue
				}
				minute := incidentMinute(start, index)
				if scenario.unavailable && index == 1 {
					minute.Traffic[1].Available = false
				}
				if err := store.Record(t.Context(), minute); err != nil {
					t.Fatal(err)
				}
			}
			renderer, err := reporting.NewRenderer(settings, network, store)
			if err != nil {
				t.Fatal(err)
			}
			message, err := renderer.Message(t.Context(), reporting.Daily, reporting.IncidentView{}, start.Add(3*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			for _, block := range message.Content {
				if block.Table == nil || block.Table.Caption != "Traffic measurement intervals" {
					continue
				}
				if len(block.Table.Rows) != 1 || len(block.Table.Rows[0]) != 4 {
					t.Fatalf("report returned invalid interval rows: %v", block.Table.Rows)
				}
				if actual := block.Table.Rows[0][3]; actual != scenario.coverage {
					t.Fatalf("report coverage = %q, want %q", actual, scenario.coverage)
				}
				return
			}
			t.Fatal("report omitted traffic interval coverage")
		})
	}
}
