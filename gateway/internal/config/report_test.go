package config_test

import (
	"fmt"
	"testing"

	"goodkind.io/mwan/internal/config"
)

func TestReportConfigLoadsAndValidates(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	text := fmt.Sprintf("[report]\nstate_directory = %q\nrrdtool_path = %q\n", directory, "/usr/local/bin/rrdtool")
	cfg, _, err := config.LoadArguments([]string{"mwan", "--config", writeSharedFile(t, text)})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Report.SummaryHour != 20 || cfg.Report.SampleInterval != "1m" || cfg.Report.ProcDirectory != "/proc" {
		t.Fatalf("unexpected defaults: %+v", cfg.Report)
	}
	if err := cfg.Report.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name string
		edit func(*config.ReportSection)
	}{
		{"zone", func(settings *config.ReportSection) { settings.TimeZone = "invalid/zone" }},
		{"path", func(settings *config.ReportSection) { settings.StateDirectory = "relative" }},
		{"window", func(settings *config.ReportSection) { settings.BreachWindow = "61s" }},
		{"cadence", func(settings *config.ReportSection) { settings.ProbeInterval = "7s" }},
		{"completeness", func(settings *config.ReportSection) { settings.MinProbeSeconds = 61 }},
		{"overflowing completeness", func(settings *config.ReportSection) { settings.MinProbeSeconds = 1 << 34 }},
		{"percent", func(settings *config.ReportSection) { settings.LossPercent = 101 }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			settings := cfg.Report
			entry.edit(&settings)
			if err := settings.Validate(); err == nil {
				t.Fatal("invalid report settings passed validation")
			}
		})
	}
	if _, _, err := config.LoadArguments([]string{"mwan", "--config", writeSharedFile(t, sharedFileText)}); err != nil {
		t.Fatalf("existing configuration requires reporting settings: %v", err)
	}
}
