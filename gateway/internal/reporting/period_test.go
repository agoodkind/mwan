package reporting_test

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"goodkind.io/mwan/internal/testsmtp"
)

func reportCellText(node *html.Node) string {
	var text strings.Builder
	var collect func(*html.Node)
	collect = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)
	return strings.TrimSpace(text.String())
}

func deliveredReportPeriod(t *testing.T, server *testsmtp.Server, location *time.Location, interval string) (time.Time, time.Time, string) {
	t.Helper()
	message := server.Next(t)
	if !strings.Contains(string(message), "Subject: MWAN daily report") {
		t.Fatalf("expected a daily report, received: %s", message)
	}
	document, err := html.Parse(strings.NewReader(testsmtp.HTML(t, message)))
	if err != nil {
		t.Fatal(err)
	}
	var start, end time.Time
	var coverage string
	var inspect func(*html.Node)
	inspect = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "tr" {
			var cells []string
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && child.Data == "td" {
					cells = append(cells, reportCellText(child))
				}
			}
			if len(cells) == 4 && cells[2] == interval {
				parsedStart, startError := time.ParseInLocation(time.RFC1123, cells[0], location)
				parsedEnd, endError := time.ParseInLocation(time.RFC1123, cells[1], location)
				if startError == nil && endError == nil {
					start, end, coverage = parsedStart, parsedEnd, cells[3]
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			inspect(child)
		}
	}
	inspect(document)
	if start.IsZero() || end.IsZero() {
		t.Fatal("delivered report omitted readable period boundaries")
	}
	return start, end, coverage
}

func TestReportPeriodPreservesLocalSummaryHourAcrossDST(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		month    time.Month
		day      int
		duration time.Duration
	}{
		{name: "spring", month: time.March, day: 8, duration: 23 * time.Hour},
		{name: "fall", month: time.November, day: 1, duration: 25 * time.Hour},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			settings := reportSettings(t)
			location, err := time.LoadLocation(settings.TimeZone)
			if err != nil {
				t.Fatal(err)
			}
			server := testsmtp.Start(t)
			service := reportService(t, settings, server)
			t.Cleanup(func() {
				if err := service.Close(); err != nil {
					t.Error(err)
				}
			})
			start := time.Date(2025, scenario.month, scenario.day, settings.SummaryHour, 0, 0, 0, location)
			end := start.AddDate(0, 0, 1)
			if err := service.RecordMinute(t.Context(), incidentMinute(start, 0)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-server.Messages:
				t.Fatal("service sent a daily report before the local summary hour")
			default:
			}
			if err := service.RecordMinute(t.Context(), incidentMinute(end.Add(-time.Minute), 0)); err != nil {
				t.Fatal(err)
			}
			actualStart, actualEnd, coverage := deliveredReportPeriod(t, server, location, settings.SampleInterval)
			if !actualStart.Equal(start) || !actualEnd.Equal(end) || actualStart.Hour() != 20 || actualEnd.Hour() != 20 {
				t.Fatalf("scheduled period = %s to %s, want %s to %s at local 20:00", actualStart, actualEnd, start, end)
			}
			if actual := actualEnd.Sub(actualStart); actual != scenario.duration {
				t.Fatalf("scheduled period duration = %s, want %s", actual, scenario.duration)
			}
			if coverage != "Partial" {
				t.Fatalf("scheduled report coverage = %q after an unobserved interval, want Partial", coverage)
			}
		})
	}
}

func TestReportPeriodManualSummaryPreservesScheduledDeliveryAfterRestart(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	location, err := time.LoadLocation(settings.TimeZone)
	if err != nil {
		t.Fatal(err)
	}
	server := testsmtp.Start(t)
	service := reportService(t, settings, server)
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	boundary := time.Date(2026, 10, 20, settings.SummaryHour, 0, 0, 0, location)
	start := boundary.Add(-2 * time.Minute)
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 0)); err != nil {
		t.Fatal(err)
	}
	if err := service.SendDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	manualStart, manualEnd, _ := deliveredReportPeriod(t, server, location, settings.SampleInterval)
	if !manualStart.Equal(start) || !manualEnd.Equal(boundary.Add(-time.Minute)) {
		t.Fatalf("manual summary period = %s to %s, want %s to %s", manualStart, manualEnd, start, boundary.Add(-time.Minute))
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service = reportService(t, settings, server)
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 1)); err != nil {
		t.Fatal(err)
	}
	scheduledStart, scheduledEnd, _ := deliveredReportPeriod(t, server, location, settings.SampleInterval)
	if !scheduledStart.Equal(start) || !scheduledEnd.Equal(boundary) {
		t.Fatalf("scheduled summary period = %s to %s, want %s to %s", scheduledStart, scheduledEnd, start, boundary)
	}
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 2)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Messages:
		t.Fatal("service repeated the scheduled report after its boundary")
	default:
	}
}

func TestReportPeriodSkipsUnobservedDaysAfterRestart(t *testing.T) {
	t.Parallel()
	settings := reportSettings(t)
	location, err := time.LoadLocation(settings.TimeZone)
	if err != nil {
		t.Fatal(err)
	}
	server := testsmtp.Start(t)
	service := reportService(t, settings, server)
	start := time.Date(2026, 10, 20, settings.SummaryHour, 0, 0, 0, location).Add(time.Minute)
	if err := service.RecordMinute(t.Context(), incidentMinute(start, 0)); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service = reportService(t, settings, server)
	defer service.Close()
	resumed := start.AddDate(0, 0, 4)
	if err := service.RecordMinute(t.Context(), incidentMinute(resumed, 0)); err != nil {
		t.Fatalf("unobserved days blocked delivery after restart: %v", err)
	}
	deliveredStart, _, _ := deliveredReportPeriod(t, server, location, settings.SampleInterval)
	if !deliveredStart.Equal(start) {
		t.Fatalf("saved observed period started at %s, want %s", deliveredStart, start)
	}
	if err := service.SendDaily(t.Context()); err != nil {
		t.Fatalf("a valid current report remained blocked: %v", err)
	}
	deliveredStart, _, _ = deliveredReportPeriod(t, server, location, settings.SampleInterval)
	if !deliveredStart.Equal(resumed) {
		t.Fatalf("current report started at %s, want %s", deliveredStart, resumed)
	}
	select {
	case <-server.Messages:
		t.Fatal("unobserved days produced extra reports")
	default:
	}
}
