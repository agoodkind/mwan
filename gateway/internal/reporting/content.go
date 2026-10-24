package reporting

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/send-email/mailer"
)

type Renderer struct {
	settings config.ReportSection
	network  *networkjson.Config
	store    *Store
}

func NewRenderer(settings config.ReportSection, network *networkjson.Config, store *Store) (*Renderer, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if network == nil || store == nil {
		return nil, errors.New("report rendering requires network configuration and history")
	}
	return &Renderer{settings: settings, network: network, store: store}, nil
}

func tableBlock(caption string, headers []string, rows [][]string) mailer.ContentBlock {
	return mailer.ContentBlock{Kind: mailer.ContentTable, Table: &mailer.Table{Caption: caption, Headers: headers, Rows: rows}}
}

func (renderer *Renderer) label(id connectionid.ID) string {
	for key, wan := range renderer.network.WAN {
		if connectionid.Resolve(renderer.network.ConnectionIDs[key].String(), key, wan.Iface) == id {
			if wan.ProviderName != "" {
				return wan.ProviderName
			}
		}
	}
	return id.String()
}

func (renderer *Renderer) Message(ctx context.Context, kind Kind, incident IncidentView, end time.Time) (mailer.Message, error) {
	boundary := renderer.store.periodEnd(end.Add(-time.Nanosecond))
	start := boundary.AddDate(0, 0, -1)
	if end.Before(boundary) {
		start = renderer.store.periodEnd(end).AddDate(0, 0, -1)
	}
	return renderer.messagePeriod(ctx, kind, incident, start, end)
}

func (renderer *Renderer) messagePeriod(ctx context.Context, kind Kind, incident IncidentView, start, end time.Time) (mailer.Message, error) {
	minuteHistory, err := renderer.store.history(start, end)
	if err != nil {
		return mailer.Message{}, err
	}
	if len(minuteHistory) == 0 {
		return mailer.Message{}, errors.New("reporting history is empty for this report period")
	}
	if minuteHistory[0].Start.After(start) {
		start = minuteHistory[0].Start
	}
	message := mailer.Message{Subject: "MWAN " + string(kind) + " report", Caller: "mwan-report"}
	if kind != Daily {
		message.Content = append(message.Content, renderer.incidentContent(incident)...)
	}
	directory, err := os.MkdirTemp(renderer.settings.StateDirectory, "render-")
	if err != nil {
		return message, err
	}
	defer os.RemoveAll(directory)
	usage, totals, err := renderer.usageGraph(ctx, directory, end)
	if err != nil {
		return message, err
	}
	definitions := renderer.graphDefinitions(minuteHistory, incident, start, end)
	definitions = append([]graphDefinition{usage}, definitions...)
	for _, definition := range definitions {
		inline, block, err := renderer.renderGraph(ctx, directory, definition)
		if err != nil {
			return message, err
		}
		message.Inlines = append(message.Inlines, inline)
		message.Content = append(message.Content, block)
		if definition.filename == "usage.png" {
			message.Content = append(message.Content, tableBlock("Cumulative usage over the displayed period", []string{"WAN", "Downloaded (GiB)", "Uploaded (GiB)"}, totals))
		}
		if definition.filename == "speed.png" {
			period := "Complete"
			if minuteHistory[0].Partial || start.After(renderer.store.periodEnd(end.Add(-time.Nanosecond)).AddDate(0, 0, -1)) {
				period = "Partial"
			}
			message.Content = append(message.Content, tableBlock("Traffic measurement intervals", []string{"Period start", "Period end", "Average interval", "Coverage"}, [][]string{{start.In(boundaryLocation(renderer.settings)).Format(time.RFC1123), end.In(boundaryLocation(renderer.settings)).Format(time.RFC1123), renderer.settings.SampleInterval, period}}))
		}
	}
	message.Content = append(message.Content, renderer.talkerContent(minuteHistory)...)
	return message, nil
}

func boundaryLocation(settings config.ReportSection) *time.Location {
	location, _ := time.LoadLocation(settings.TimeZone)
	return location
}

func (renderer *Renderer) incidentContent(incident IncidentView) []mailer.ContentBlock {
	var affected []string
	for _, id := range incident.Affected {
		affected = append(affected, renderer.label(id))
	}
	if incident.RouterAffected {
		affected = append(affected, "Local gateway UDP/DNS counters")
	}
	text := "Current incident\n" + strings.Join(affected, "\n")
	if !incident.Open {
		text += "\nEvery affected WAN and local gateway signal has satisfied the recovery interval."
	}
	var other []string
	for _, id := range renderer.store.wanIDs() {
		if !slices.Contains(incident.Affected, id) {
			other = append(other, renderer.label(id))
		}
	}
	comparison := "Other WANs for comparison"
	if len(other) > 0 {
		comparison += "\n" + strings.Join(other, "\n")
	} else {
		comparison += "\nEvery monitored WAN belongs to the current incident."
	}
	return []mailer.ContentBlock{{Kind: mailer.ContentText, Text: text}, {Kind: mailer.ContentHTML, Text: "Comparison section", HTML: `<hr style="border:0;border-top:4px solid #999;margin:24px 0">`}, {Kind: mailer.ContentText, Text: comparison}}
}

func (renderer *Renderer) graphDefinitions(history []Minute, incident IncidentView, start, end time.Time) []graphDefinition {
	definitions := []graphDefinition{{filename: "speed.png", title: "Average traffic rate", unit: "Mbps", start: start, end: end}, {filename: "latency.png", title: "Gateway and Cloudflare latency", unit: "ms", start: start, end: end}, {filename: "loss.png", title: "Gateway and Cloudflare packet loss", unit: "%", start: start, end: end}, {filename: "cpu-ram.png", title: "CPU and RAM", unit: "%", start: start, end: end}}
	for _, id := range renderer.store.wanIDs() {
		label := renderer.label(id)
		if slices.Contains(incident.Affected, id) {
			label += " [incident]"
		}
		path := renderer.store.wanPath(id)
		definitions[0].traces = append(definitions[0].traces, graphTrace{path: path, source: "down", label: label + " download", scale: 8.0 / 1e6}, graphTrace{path: path, source: "up", label: label + " upload", scale: 8.0 / 1e6})
		for _, family := range []Family{IPv4, IPv6} {
			for _, target := range []Target{Gateway, Cloudflare} {
				key := ProbeKey{WAN: id, Family: family, Target: target, Mode: Fresh}
				available := false
				for _, minute := range history {
					for _, probe := range minute.Probes {
						if probe.Key == key && probe.Available && probe.Sent > 0 {
							available = true
						}
					}
				}
				traceLabel := fmt.Sprintf("%s %s IPv%d", label, target, family)
				if !available {
					traceLabel += " [unavailable]"
				}
				name := probeName(key)
				definitions[1].traces = append(definitions[1].traces, graphTrace{path: path, source: name + "_mean", minimum: name + "_min", peak: name + "_peak", label: traceLabel, scale: 1})
				definitions[2].traces = append(definitions[2].traces, graphTrace{path: path, source: name + "_loss", label: traceLabel, scale: 1})
			}
		}
	}
	cpuNames := make(map[string]bool)
	for _, minute := range history {
		for _, cpu := range minute.Host.CPU {
			cpuNames[cpu.Name] = true
		}
	}
	names := make([]string, 0, len(cpuNames))
	for name := range cpuNames {
		names = append(names, name)
	}
	slices.SortFunc(names, func(first, second string) int {
		if first == "cpu" {
			return -1
		}
		if second == "cpu" {
			return 1
		}
		firstIndex, _ := strconv.Atoi(strings.TrimPrefix(first, "cpu"))
		secondIndex, _ := strconv.Atoi(strings.TrimPrefix(second, "cpu"))
		return firstIndex - secondIndex
	})
	definitions[3].traces = append(definitions[3].traces, graphTrace{path: renderer.store.hostPath("memory"), source: "usage", label: "RAM", scale: 1})
	for _, name := range names {
		label := strings.ToUpper(name)
		if name == "cpu" {
			label = "Overall CPU"
		}
		definitions[3].traces = append(definitions[3].traces, graphTrace{path: renderer.store.hostPath(name), source: "usage", label: label, scale: 1})
	}
	return definitions
}

func (renderer *Renderer) usageGraph(ctx context.Context, directory string, end time.Time) (graphDefinition, [][]string, error) {
	utc := end.UTC()
	start := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -29)
	history, err := renderer.store.history(start, end)
	if err != nil {
		return graphDefinition{}, nil, err
	}
	ids := renderer.store.wanIDs()
	definition := graphDefinition{filename: "usage.png", title: "Cumulative data usage (UTC days)", unit: "GiB", start: start, end: end}
	var totals [][]string
	for index, id := range ids {
		path := filepath.Join(directory, fmt.Sprintf("usage-%d.rrd", index))
		arguments := []string{"create", path, "--start", strconv.FormatInt(start.Unix()-1, 10), "--step", strconv.FormatInt(renderer.store.step, 10), "DS:down:GAUGE:172800:0:U", "DS:up:GAUGE:172800:0:U", "RRA:AVERAGE:0.5:1:45000"}
		if _, err := renderer.store.rrd(ctx, arguments...); err != nil {
			return definition, nil, err
		}
		var down, up uint64
		updates := []string{"update", path, fmt.Sprintf("%d:0:0", start.Unix())}
		cursor := 0
		for dayEnd := start.AddDate(0, 0, 1); ; dayEnd = dayEnd.AddDate(0, 0, 1) {
			stop := dayEnd
			if stop.After(end) {
				stop = end
			}
			observed := false
			for cursor < len(history) && !history[cursor].End.After(stop) {
				for _, traffic := range history[cursor].Traffic {
					if traffic.WAN == id && traffic.Available {
						down += traffic.Downloaded
						up += traffic.Uploaded
						observed = true
					}
				}
				cursor++
			}
			updates = append(updates, fmt.Sprintf("%d:%s:%s", stop.Unix(), sampleValue(float64(down)/1073741824, observed), sampleValue(float64(up)/1073741824, observed)))
			if !stop.Before(end) {
				break
			}
		}
		if _, err := renderer.store.rrd(ctx, updates...); err != nil {
			return definition, nil, err
		}
		definition.traces = append(definition.traces, graphTrace{path: path, source: "down", label: renderer.label(id) + " download", scale: 1}, graphTrace{path: path, source: "up", label: renderer.label(id) + " upload", scale: 1})
		totals = append(totals, []string{renderer.label(id), fmt.Sprintf("%.2f", float64(down)/1073741824), fmt.Sprintf("%.2f", float64(up)/1073741824)})
	}
	return definition, totals, nil
}

func (renderer *Renderer) talkerContent(history []Minute) []mailer.ContentBlock {
	var blocks []mailer.ContentBlock
	for _, id := range renderer.store.wanIDs() {
		clients := make(map[netip.Addr]*TalkerUsage)
		gaps := make(map[string]bool)
		for _, minute := range history {
			for _, coverage := range minute.Attribution {
				if coverage.WAN == id && !coverage.Complete {
					gaps[coverage.Reason] = true
				}
			}
			for _, usage := range minute.Talkers {
				if usage.WAN != id {
					continue
				}
				client := clients[usage.Client]
				if client == nil {
					client = &TalkerUsage{WAN: id, Client: usage.Client, Label: usage.Label}
					clients[usage.Client] = client
				}
				client.Downloaded += usage.Downloaded
				client.Uploaded += usage.Uploaded
				client.Destinations = append(client.Destinations, usage.Destinations...)
			}
		}
		var ordered []*TalkerUsage
		for _, client := range clients {
			ordered = append(ordered, client)
		}
		slices.SortFunc(ordered, func(first, second *TalkerUsage) int {
			return intCompare(second.Downloaded+second.Uploaded, first.Downloaded+first.Uploaded)
		})
		var rows [][]string
		for _, client := range ordered[:min(10, len(ordered))] {
			destinationCounts := make(map[netip.Addr]uint64)
			for _, destination := range client.Destinations {
				destinationCounts[destination.Address] += destination.Downloaded + destination.Uploaded
			}
			var destinations []netip.Addr
			for address := range destinationCounts {
				destinations = append(destinations, address)
			}
			slices.SortFunc(destinations, func(first, second netip.Addr) int {
				return intCompare(destinationCounts[second], destinationCounts[first])
			})
			var labels []string
			for _, address := range destinations[:min(3, len(destinations))] {
				labels = append(labels, address.String())
			}
			rows = append(rows, []string{client.Label, fmt.Sprintf("%.3f", float64(client.Downloaded)/1073741824), fmt.Sprintf("%.3f", float64(client.Uploaded)/1073741824), strings.Join(labels, ", ")})
		}
		blocks = append(blocks, tableBlock("Top talkers on "+renderer.label(id)+", report day", []string{"Client", "Downloaded (GiB)", "Uploaded (GiB)", "Top destinations"}, rows))
		if len(gaps) > 0 {
			var reasons []string
			for reason := range gaps {
				reasons = append(reasons, reason)
			}
			slices.Sort(reasons)
			text := renderer.label(id) + " client attribution is incomplete. " + strings.Join(reasons, " ")
			blocks = append(blocks, mailer.ContentBlock{Kind: mailer.ContentHTML, Text: text, HTML: "<p>" + html.EscapeString(text) + "</p>"})
		}
	}
	return blocks
}

func intCompare(first, second uint64) int {
	if first < second {
		return -1
	}
	if first > second {
		return 1
	}
	return 0
}
