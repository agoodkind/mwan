package reporting

import (
	"context"
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goodkind.io/send-email/mailer"
)

const graphWidth = 500
const graphScale = 4

type graphTrace struct {
	path, source, minimum, peak, label string
	scale                              float64
}
type graphDefinition struct {
	filename, title, unit string
	traces                []graphTrace
	start, end            time.Time
}

func seriesColor(index int) string {
	approved := []string{"4F9DE0", "5BC88A", "F2A03D", "C084FC"}
	if index < len(approved) {
		return approved[index]
	}
	hue := math.Mod(float64(index-4)*137.508+15, 360) / 60
	chroma := 0.55
	intermediate := chroma * (1 - math.Abs(math.Mod(hue, 2)-1))
	red, green, blue := 0.0, 0.0, 0.0
	switch int(hue) {
	case 0:
		red, green = chroma, intermediate
	case 1:
		red, green = intermediate, chroma
	case 2:
		green, blue = chroma, intermediate
	case 3:
		green, blue = intermediate, chroma
	case 4:
		red, blue = intermediate, chroma
	default:
		red, blue = chroma, intermediate
	}
	return fmt.Sprintf("%02X%02X%02X", int((red+0.3)*255), int((green+0.3)*255), int((blue+0.3)*255))
}

func rrdEscape(text string) string {
	return strings.NewReplacer("\\", "\\\\", ":", "\\:", "\n", " ", "\r", " ", "\t", " ").Replace(text)
}

func (renderer *Renderer) renderGraph(ctx context.Context, directory string, definition graphDefinition) (mailer.InlineImage, mailer.ContentBlock, error) {
	height := 278 + 12*len(definition.traces)
	path := filepath.Join(directory, definition.filename)
	axisFormat, statFormat := "%3.0lf "+definition.unit, "%5.1lf "+definition.unit
	if definition.unit == "%" {
		axisFormat, statFormat = "%3.1lf%%", "%5.2lf%%"
	}
	arguments := []string{"graph", path, "--start", strconv.FormatInt(definition.start.Unix(), 10), "--end", strconv.FormatInt(definition.end.Unix(), 10),
		"--full-size-mode", "--width", strconv.Itoa(graphWidth), "--height", strconv.Itoa(height), "--zoom", strconv.Itoa(graphScale),
		"--title", definition.title, "--font", "DEFAULT:9:Liberation Mono", "--font", "TITLE:11:Liberation Mono Bold", "--font", "LEGEND:7:Liberation Mono",
		"--color", "BACK#1E1E1E", "--color", "CANVAS#1E1E1E", "--color", "FONT#EEEEEE", "--color", "GRID#3A3A3A", "--color", "MGRID#555555",
		"--color", "AXIS#999999", "--color", "ARROW#1E1E1E", "--color", "FRAME#1E1E1E", "--border", "0", "--slope-mode", "--graph-render-mode", "normal",
		"--alt-autoscale-max", "--lower-limit", "0", "--units-exponent", "0", "--left-axis-format", axisFormat, "--units-length", "9", "--dynamic-labels", "--disable-rrdtool-tag"}
	if definition.filename == "cpu-ram.png" || definition.filename == "loss.png" {
		arguments = append(arguments, "--upper-limit", "100", "--rigid")
	}
	labelWidth := 17
	for _, trace := range definition.traces {
		labelWidth = max(labelWidth, len([]rune(trace.label)))
	}
	var legends []string
	for index, trace := range definition.traces {
		name := fmt.Sprintf("s%d", index)
		arguments = append(arguments, fmt.Sprintf("DEF:%s_raw=%s:%s:AVERAGE", name, rrdEscape(trace.path), trace.source), fmt.Sprintf("CDEF:%s=%s_raw,%g,*", name, name, trace.scale))
		minimum, peak := name, name
		if trace.minimum != "" {
			minimum = name + "_observed_min"
			arguments = append(arguments, fmt.Sprintf("DEF:%s=%s:%s:MIN", minimum, rrdEscape(trace.path), trace.minimum))
		}
		if trace.peak != "" {
			peak = name + "_observed_peak"
			arguments = append(arguments, fmt.Sprintf("DEF:%s=%s:%s:MAX", peak, rrdEscape(trace.path), trace.peak))
		}
		arguments = append(arguments, "VDEF:"+name+"_min="+minimum+",MINIMUM", "VDEF:"+name+"_max="+peak+",MAXIMUM", "VDEF:"+name+"_avg="+name+",AVERAGE", "VDEF:"+name+"_last="+name+",LAST")
		arguments = append(arguments, fmt.Sprintf("LINE1.7:%s#%s:%s", name, seriesColor(index), rrdEscape(fmt.Sprintf("%-*s", labelWidth, trace.label))))
		if definition.filename == "usage.png" {
			arguments = append(arguments, "GPRINT:"+name+"_last:  total "+statFormat+"\\l")
		} else {
			arguments = append(arguments, "GPRINT:"+name+"_min:  min "+statFormat, "GPRINT:"+name+"_max:  max "+statFormat, "GPRINT:"+name+"_avg:  avg "+statFormat+"\\l")
		}
		legends = append(legends, trace.label)
	}
	if len(definition.traces) == 0 {
		arguments = append(arguments, "CDEF:unknown=UNKN", "LINE1:unknown#4F9DE0:Measurements unavailable\\l")
	}
	if _, err := renderer.store.rrd(ctx, arguments...); err != nil {
		return mailer.InlineImage{}, mailer.ContentBlock{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return mailer.InlineImage{}, mailer.ContentBlock{}, err
	}
	alternative := definition.title + ". " + strings.Join(legends, ", ") + ". Gaps indicate unavailable measurements."
	markup := fmt.Sprintf(`<img src="cid:%s" width="%d" height="%d" alt="%s" style="display:block;width:%dpx;max-width:100%%;height:auto;margin:16px 0 8px">`, definition.filename, graphWidth, height, html.EscapeString(alternative), graphWidth)
	return mailer.InlineImage{Filename: definition.filename, MIMEType: "image/png", Data: data}, mailer.ContentBlock{Kind: mailer.ContentHTML, Text: alternative, HTML: markup}, nil
}
