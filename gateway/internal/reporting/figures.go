package reporting

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/send-email/mailer"
)

const (
	usageGraphFilename     = "usage.png"
	inlineGraphLabelRunes  = 24
	wrappedGraphLabelRunes = 60
)

func (renderer *Renderer) appendMessageGraphs(
	ctx context.Context,
	directory string,
	definitions []graphDefinition,
	minuteHistory []Minute,
	start time.Time,
	end time.Time,
	message *mailer.Message,
	totals [][]string,
) error {
	for _, definition := range definitions {
		inline, block, err := renderer.renderGraph(ctx, directory, definition)
		if err != nil {
			return err
		}

		message.Inlines = append(message.Inlines, inline)
		message.Content = append(message.Content, block)
		if definition.filename == usageGraphFilename {
			message.Content = append(
				message.Content,
				tableBlock(
					"Cumulative usage over the displayed period",
					[]string{"WAN", "Downloaded (GiB)", "Uploaded (GiB)"},
					totals,
				),
			)
		}

		if definition.filename == "speed.png" {
			period := "Complete"
			if !renderer.periodComplete(minuteHistory, start, end) ||
				start.After(renderer.store.periodEnd(end.Add(-time.Nanosecond)).AddDate(0, 0, -1)) {
				period = "Partial"
			}

			message.Content = append(
				message.Content,
				tableBlock(
					"Traffic measurement intervals",
					[]string{"Period start", "Period end", "Average interval", "Coverage"},
					[][]string{
						{
							start.In(boundaryLocation(renderer.settings)).Format(time.RFC1123),
							end.In(boundaryLocation(renderer.settings)).Format(time.RFC1123),
							renderer.settings.SampleInterval,
							period,
						},
					},
				),
			)
		}
	}

	return nil
}

func appendProbeGraphTraces(
	definitions []graphDefinition,
	history []Minute,
	connectionID connectionid.ID,
	label string,
	path string,
) {
	for _, family := range []Family{IPv4, IPv6} {
		for _, target := range []Target{Gateway, Cloudflare} {
			key := ProbeKey{WAN: connectionID, Family: family, Target: target, Mode: Fresh}
			traceLabel := fmt.Sprintf("%s %s IPv%d", label, target, family)
			if !probeAvailable(history, key) {
				traceLabel += " [unavailable]"
			}

			name := probeName(key)
			definitions[1].traces = append(
				definitions[1].traces,
				graphTrace{
					path:    path,
					source:  name + "_mean",
					minimum: name + "_min",
					peak:    name + "_peak",
					label:   traceLabel,
					scale:   1,
				},
			)
			definitions[2].traces = append(
				definitions[2].traces,
				graphTrace{
					path:    path,
					source:  name + "_loss",
					minimum: "",
					peak:    "",
					label:   traceLabel,
					scale:   1,
				},
			)
		}
	}
}

func appendTraceArguments(
	arguments []string,
	definition graphDefinition,
	statFormat string,
	labelWidth int,
	legends []string,
) ([]string, []string) {
	for index, trace := range definition.traces {
		name := fmt.Sprintf("s%d", index)
		arguments = append(
			arguments,
			graphDEF(name+"_raw", trace.path, trace.source, "AVERAGE", definition),
			fmt.Sprintf("CDEF:%s=%s_raw,%g,*", name, name, trace.scale),
		)
		minimum, peak := name, name
		if trace.minimum != "" {
			minimum = name + "_observed_min"
			arguments = append(
				arguments,
				graphDEF(minimum, trace.path, trace.minimum, "MIN", definition),
			)
		}

		if trace.peak != "" {
			peak = name + "_observed_peak"
			arguments = append(arguments, graphDEF(peak, trace.path, trace.peak, "MAX", definition))
		}

		arguments = append(
			arguments,
			"VDEF:"+name+"_min="+minimum+",MINIMUM",
			"VDEF:"+name+"_max="+peak+",MAXIMUM",
			"VDEF:"+name+"_avg="+name+",AVERAGE",
			"VDEF:"+name+"_last="+name+",LAST",
		)
		if utf8.RuneCountInString(trace.label) > inlineGraphLabelRunes {
			rows := wrapGraphLabel(trace.label, wrappedGraphLabelRunes)

			arguments = append(
				arguments,
				fmt.Sprintf("LINE1.7:%s#%s:%s\\l", name, seriesColor(index), rrdEscape(rows[0])),
			)
			for _, row := range rows[1:] {
				arguments = append(arguments, "COMMENT:"+rrdEscape(row)+"\\l")
			}

			arguments = append(arguments, "COMMENT:"+strings.Repeat(" ", labelWidth))
		} else {
			arguments = append(
				arguments,
				fmt.Sprintf(
					"LINE1.7:%s#%s:%s",
					name,
					seriesColor(index),
					rrdEscape(fmt.Sprintf("%-*s", labelWidth, trace.label)),
				),
			)
		}

		switch {
		case strings.HasSuffix(trace.label, "[unavailable]"):
			arguments = append(arguments, "COMMENT:Measurements unavailable\\l")
		case definition.filename == usageGraphFilename:
			arguments = append(arguments, "GPRINT:"+name+"_last:  total "+statFormat+"\\l")
		default:
			arguments = append(
				arguments,
				"GPRINT:"+name+"_min:  min "+statFormat,
				"GPRINT:"+name+"_max:  max "+statFormat,
				"GPRINT:"+name+"_avg:  avg "+statFormat+"\\l",
			)
		}

		legends = append(legends, trace.label)
	}

	return arguments, legends
}
