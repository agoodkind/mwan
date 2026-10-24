package config

import (
	"fmt"
	"math"
	"net/netip"
	"path/filepath"
	"time"
)

type ReportSection struct {
	StateDirectory      string  `toml:"state_directory"`
	RRDToolPath         string  `toml:"rrdtool_path"`
	ProcDirectory       string  `toml:"proc_directory"`
	TimeZone            string  `toml:"time_zone"`
	SummaryHour         int     `toml:"summary_hour"`
	CloudflareHost      string  `toml:"cloudflare_host"`
	DNSv4               string  `toml:"dns_v4"`
	DNSv6               string  `toml:"dns_v6"`
	ProbeInterval       string  `toml:"probe_interval"`
	SampleInterval      string  `toml:"sample_interval"`
	ProbeTimeout        string  `toml:"probe_timeout"`
	PersistentRenewal   string  `toml:"persistent_renewal"`
	BreachWindow        string  `toml:"breach_window"`
	ReminderInterval    string  `toml:"reminder_interval"`
	RecoveryInterval    string  `toml:"recovery_interval"`
	MailTimeout         string  `toml:"mail_timeout"`
	MinProbeSeconds     int     `toml:"min_probe_seconds"`
	LossPercent         float64 `toml:"loss_percent"`
	DNSFailurePercent   float64 `toml:"dns_failure_percent"`
	LatencyMilliseconds float64 `toml:"latency_milliseconds"`
	DropPercent         float64 `toml:"drop_percent"`
	MinDrops            uint64  `toml:"min_drops"`
}

func DefaultReportSection() ReportSection {
	return ReportSection{
		ProcDirectory: "/proc", TimeZone: "America/Los_Angeles", SummaryHour: 20,
		CloudflareHost: "one.one.one.one", ProbeInterval: "1s", SampleInterval: "1m",
		DNSv4: "1.1.1.1:53", DNSv6: "[2606:4700:4700::1111]:53",
		ProbeTimeout: "5s", PersistentRenewal: "15m", BreachWindow: "5m",
		ReminderInterval: "15m", RecoveryInterval: "15m", MailTimeout: "45s",
		MinProbeSeconds: 54, LossPercent: 1, DNSFailurePercent: 1,
		LatencyMilliseconds: 200, DropPercent: 0.1, MinDrops: 50,
	}
}

func (settings ReportSection) Validate() error {
	for key, path := range map[string]string{
		"state_directory": settings.StateDirectory,
		"rrdtool_path":    settings.RRDToolPath,
		"proc_directory":  settings.ProcDirectory,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("[report] %s must be an absolute path", key)
		}
	}
	if _, err := time.LoadLocation(settings.TimeZone); err != nil {
		return fmt.Errorf("[report] time_zone: %w", err)
	}
	if settings.SummaryHour < 0 || settings.SummaryHour > 23 {
		return fmt.Errorf("[report] summary_hour must be between 0 and 23")
	}
	if settings.CloudflareHost == "" {
		return fmt.Errorf("[report] cloudflare_host is required")
	}
	for key, text := range map[string]string{"dns_v4": settings.DNSv4, "dns_v6": settings.DNSv6} {
		address, err := netip.ParseAddrPort(text)
		if err != nil || address.Port() == 0 || address.Addr().Is4() != (key == "dns_v4") {
			return fmt.Errorf("[report] %s requires a resolver address and port in its address family", key)
		}
	}
	durations := make(map[string]time.Duration)
	for key, text := range map[string]string{
		"probe_interval": settings.ProbeInterval, "sample_interval": settings.SampleInterval,
		"probe_timeout": settings.ProbeTimeout, "persistent_renewal": settings.PersistentRenewal,
		"breach_window": settings.BreachWindow, "reminder_interval": settings.ReminderInterval,
		"recovery_interval": settings.RecoveryInterval, "mail_timeout": settings.MailTimeout,
	} {
		value, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("[report] %s: %w", key, err)
		}
		if value <= 0 {
			return fmt.Errorf("[report] %s must be positive", key)
		}
		durations[key] = value
	}
	sample := durations["sample_interval"]
	probe := durations["probe_interval"]
	if sample%time.Second != 0 || sample%probe != 0 {
		return fmt.Errorf("[report] sample_interval must use whole seconds and divide into probe intervals")
	}
	for _, key := range []string{"breach_window", "reminder_interval", "recovery_interval"} {
		if durations[key]%sample != 0 {
			return fmt.Errorf("[report] %s must divide into sample intervals", key)
		}
	}
	if settings.MinProbeSeconds <= 0 || time.Duration(settings.MinProbeSeconds)*time.Second > sample {
		return fmt.Errorf("[report] min_probe_seconds must fit sample_interval")
	}
	for key, value := range map[string]float64{
		"loss_percent": settings.LossPercent, "dns_failure_percent": settings.DNSFailurePercent,
		"drop_percent": settings.DropPercent,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return fmt.Errorf("[report] %s must be between 0 and 100", key)
		}
	}
	if math.IsNaN(settings.LatencyMilliseconds) || math.IsInf(settings.LatencyMilliseconds, 0) || settings.LatencyMilliseconds <= 0 {
		return fmt.Errorf("[report] latency_milliseconds must be positive")
	}
	return nil
}
