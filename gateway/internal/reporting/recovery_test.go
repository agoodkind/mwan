package reporting_test

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
)

func TestServiceRecoveryWaitsForEveryExpectedProbeAndWANCounters(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	settings.BreachWindow = "1m"
	settings.RecoveryInterval = "3m"
	settings.ReminderInterval = "1h"
	network := reportNetwork()
	for key, wan := range network.WAN {
		wan.TranslationV4 = &config.IPv4Translation{Mode: config.TranslationNative}
		wan.TranslationV6 = &config.IPv6Translation{Mode: config.TranslationNative}
		network.WAN[key] = wan
	}
	server := testsmtp.Start(t)
	logger, closer := logging.New(logging.Config{BuildVersion: "test"})
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
	})
	manager, err := notify.New(notify.EmailConfig{MsmtprcPath: server.Path, AlertEmail: "recipient@example.com"}, notify.Config{}, logger, "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	service, err := reporting.NewService(reporting.Options{Config: settings, Network: network, Notifications: manager, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	minuteAt := func(index int) reporting.Minute {
		minute := incidentMinute(start, index)
		for _, probe := range minute.Probes {
			probe.Key.Target = reporting.Cloudflare
			minute.Probes = append(minute.Probes, probe)
			probe.Key.Family = reporting.IPv6
			minute.Probes = append(minute.Probes, probe)
			probe.Key.Target = reporting.Gateway
			minute.Probes = append(minute.Probes, probe)
		}
		return minute
	}
	opening := minuteAt(0)
	opening.Probes[0].Lost = opening.Probes[0].Sent
	opening.Probes[0].Replies = 0
	opening.Probes[0].LatencySum = 0
	if err := service.RecordMinute(t.Context(), opening); err != nil {
		t.Fatal(err)
	}
	accepted := server.Next(t)
	if !strings.Contains(string(accepted), "Subject: MWAN incident report") || !strings.Contains(testsmtp.HTML(t, accepted), "Sonic gateway IPv4 probes exceeded the packet-loss threshold") {
		t.Fatal("service did not send an incident for the failed Sonic IPv4 probe")
	}
	for index := 1; index < 12; index++ {
		minute := minuteAt(index)
		if index < 5 {
			for probeIndex := range minute.Probes {
				probe := &minute.Probes[probeIndex]
				if probe.Key.WAN == "stable-sonic" && probe.Key.Family == reporting.IPv6 && probe.Key.Target == reporting.Cloudflare {
					probe.Available = false
					probe.Sent = 0
					probe.Replies = 0
					probe.LatencySum = 0
				}
			}
		}
		if index >= 5 && index < 9 {
			minute.Traffic[0].Available = false
		}
		if err := service.RecordMinute(t.Context(), minute); err != nil {
			t.Fatal(err)
		}
		if index < 11 {
			select {
			case <-server.Messages:
				t.Fatalf("service sent a report at minute %d before every expected probe and WAN counter completed the recovery interval", index)
			default:
			}
		}
	}
	recovered := server.Next(t)
	if !strings.Contains(string(recovered), "Subject: MWAN recovery report") || !strings.Contains(testsmtp.HTML(t, recovered), "Sonic") {
		t.Fatal("service did not send recovery after three complete healthy intervals")
	}
}
