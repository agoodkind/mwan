package reporting_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/send-email/mailer"
)

func reportSettings(t *testing.T) config.ReportSection {
	t.Helper()
	settings := config.DefaultReportSection()
	settings.StateDirectory = t.TempDir()
	path, err := exec.LookPath("rrdtool")
	if err != nil {
		t.Fatalf("install RRDtool for reporting tests: %v", err)
	}
	settings.RRDToolPath = path
	return settings
}

func reportNetwork() *networkjson.Config {
	return &networkjson.Config{WAN: map[string]config.IfMgrWANEntry{"sonic": {Iface: "wan0", ProviderName: "Sonic"}, "etheric": {Iface: "wan1", ProviderName: "Etheric"}}, ConnectionIDs: map[string]connectionid.ID{"sonic": "stable-sonic", "etheric": "stable-etheric"}}
}

func recordHistory(t *testing.T, settings config.ReportSection, network *networkjson.Config, start time.Time, count int) *reporting.Store {
	t.Helper()
	store, err := reporting.OpenStore(settings, network)
	if err != nil {
		t.Fatal(err)
	}
	for index := range count {
		minute := reporting.Minute{Start: start.Add(time.Duration(index) * time.Minute), End: start.Add(time.Duration(index+1) * time.Minute), Host: reporting.HostUsage{CPU: []reporting.CPUUsage{{Name: "cpu", Percent: float64(15 + index), Available: index > 0}, {Name: "cpu0", Percent: float64(10 + index), Available: index > 0}, {Name: "cpu1", Percent: float64(20 + index), Available: index > 0}}, MemoryPercent: 42, MemoryAvailable: true}}
		for _, id := range []connectionid.ID{"stable-sonic", "stable-etheric"} {
			minute.Traffic = append(minute.Traffic, reporting.WANTraffic{WAN: id, Downloaded: uint64(1000000 + index*1000), Uploaded: 200000, Available: true})
			minute.Attribution = append(minute.Attribution, reporting.AttributionCoverage{WAN: id, Complete: id == "stable-sonic", Reason: "An observed connection did not have byte counters."})
			minute.Talkers = append(minute.Talkers, reporting.TalkerUsage{WAN: id, Client: netip.MustParseAddr("fd06:fab::5"), Label: "fd06:fab::5", Downloaded: 500000, Uploaded: 100000, Destinations: []reporting.DestinationUsage{{Address: netip.MustParseAddr("2001:db8::8"), Downloaded: 500000, Uploaded: 100000}}})
			for _, family := range []reporting.Family{reporting.IPv4, reporting.IPv6} {
				for _, target := range []reporting.Target{reporting.Gateway, reporting.Cloudflare} {
					mean := time.Duration(8+index%3) * time.Millisecond
					if id == "stable-etheric" {
						mean += 15 * time.Millisecond
					}
					if target == reporting.Cloudflare {
						mean += 4 * time.Millisecond
					}
					if family == reporting.IPv6 {
						mean += time.Millisecond
					}
					minute.Probes = append(minute.Probes, reporting.ProbeMinute{Key: reporting.ProbeKey{WAN: id, Family: family, Target: target, Mode: reporting.Fresh}, Sent: 60, Replies: 60, LatencySum: 60 * mean, LatencyMin: mean - 2*time.Millisecond, LatencyMax: mean + 4*time.Millisecond, Available: !(id == "stable-etheric" && family == reporting.IPv6)})
				}
			}
		}
		if err := store.Record(t.Context(), minute); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestMessageRendersAllWANSeriesWithUnits(t *testing.T) {
	t.Parallel()
	settings, network := reportSettings(t), reportNetwork()
	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	store := recordHistory(t, settings, network, start, 12)
	defer store.Close()
	renderer, err := reporting.NewRenderer(settings, network, store)
	if err != nil {
		t.Fatal(err)
	}
	message, err := renderer.Message(t.Context(), reporting.Daily, reporting.IncidentView{}, start.Add(12*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Inlines) != 5 {
		t.Fatalf("graph count = %d, want 5", len(message.Inlines))
	}
	for _, inline := range message.Inlines {
		configuration, err := png.DecodeConfig(bytes.NewReader(inline.Data))
		if err != nil {
			t.Fatal(err)
		}
		if configuration.Width != 2000 {
			t.Fatalf("%s width = %d, want 2000", inline.Filename, configuration.Width)
		}
		if inline.Filename == "cpu-ram.png" && configuration.Height != 4*(278+12*4) {
			t.Fatalf("CPU/RAM graph height = %d, want overall CPU, CPU0, CPU1, and RAM legend rows", configuration.Height)
		}
		if err := os.WriteFile(filepath.Join(settings.StateDirectory, inline.Filename), inline.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server := testsmtp.Start(t)
	manager, err := notify.New(notify.EmailConfig{MsmtprcPath: server.Path, AlertEmail: "recipient@example.com"}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SendReport(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	html := testsmtp.HTML(t, server.Next(t))
	for _, text := range []string{"Overall CPU", "RAM", "Etheric cloudflare IPv6 [unavailable]", "Top talkers on Sonic", "Top talkers on Etheric", "attribution is incomplete", ">Memory<", ">Load<"} {
		if !strings.Contains(html, text) {
			t.Fatalf("report omitted %q", text)
		}
	}
	if !strings.Contains(html, "CPU0") || !strings.Contains(html, "CPU1") {
		t.Fatal("report omitted per-core CPU lines")
	}
	if directory := os.Getenv("MWAN_REPORT_ARTIFACTS"); directory != "" {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		message.To = "alex@goodkind.io"
		message.Subject = "[SAMPLE] MWAN report with per-core CPU and RAM"
		message.Preheader = "Fictional measurements for report review."
		message.Content = append([]mailer.ContentBlock{{Kind: mailer.ContentText, Text: "SAMPLE REPORT\nThe graphs and top talkers use fictional measurements for layout review. The standard footer measures the rendering environment."}}, message.Content...)
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "message.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, inline := range message.Inlines {
			if err := os.WriteFile(filepath.Join(directory, inline.Filename), inline.Data, 0o600); err != nil {
				t.Fatal(err)
			}
			uri := "data:" + inline.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(inline.Data)
			html = strings.ReplaceAll(html, "cid:"+inline.Filename, uri)
		}
		if err := os.WriteFile(filepath.Join(directory, "preview.html"), []byte(html), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStorePreservesWANHistoryAndUnknownSamples(t *testing.T) {
	t.Parallel()
	settings, network := reportSettings(t), reportNetwork()
	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	store := recordHistory(t, settings, network, start, 3)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	settings.StateDirectory = filepath.Clean(settings.StateDirectory)
	store, err := reporting.OpenStore(settings, network)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	digest := sha256.Sum256([]byte("stable-etheric"))
	path := filepath.Join(settings.StateDirectory, "rrd", fmt.Sprintf("%x.rrd", digest))
	output, err := exec.CommandContext(t.Context(), settings.RRDToolPath, "fetch", path, "AVERAGE", "--start", fmt.Sprint(start.Unix()), "--end", fmt.Sprint(start.Add(3*time.Minute).Unix()), "--resolution", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("RRD fetch: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "nan") || !strings.Contains(string(output), "g4f_mean") {
		t.Fatalf("RRD did not retain unknown samples and separate probe sources: %s", output)
	}
	network.WAN["sonic"] = config.IfMgrWANEntry{Iface: "renamed0", ProviderName: "Sonic"}
	renderer, err := reporting.NewRenderer(settings, network, store)
	if err != nil {
		t.Fatal(err)
	}
	message, err := renderer.Message(t.Context(), reporting.Daily, reporting.IncidentView{}, start.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Inlines) != 5 {
		t.Fatal("reopening history or renaming an interface discarded report history")
	}
}
