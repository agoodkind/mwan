package reporting

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

const (
	gibibyteBytes  = 1073741824
	rrdStartOption = "--start"
)

func (renderer *Renderer) writeUsageSeries(
	ctx context.Context,
	directory string,
	index int,
	connectionID connectionid.ID,
	history []Minute,
	start time.Time,
	end time.Time,
) (string, uint64, uint64, error) {
	path := filepath.Join(directory, fmt.Sprintf("usage-%d.rrd", index))
	arguments := []string{
		"create",
		path,
		rrdStartOption,
		strconv.FormatInt(start.Unix()-1, 10),
		"--step",
		strconv.FormatInt(renderer.store.step, 10),
		"DS:down:GAUGE:172800:0:U",
		"DS:up:GAUGE:172800:0:U",
		"RRA:AVERAGE:0.5:1:45000",
	}
	if _, err := renderer.store.rrd(ctx, arguments...); err != nil {
		return path, 0, 0, err
	}

	var down, uploaded uint64
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
				if traffic.WAN == connectionID && traffic.Available {
					down += traffic.Downloaded
					uploaded += traffic.Uploaded
					observed = true
				}
			}

			cursor++
		}

		updates = append(
			updates,
			fmt.Sprintf(
				"%d:%s:%s",
				stop.Unix(),
				sampleValue(float64(down)/gibibyteBytes, observed),
				sampleValue(float64(uploaded)/gibibyteBytes, observed),
			),
		)
		if !stop.Before(end) {
			break
		}
	}

	if _, err := renderer.store.rrd(ctx, updates...); err != nil {
		return path, 0, 0, err
	}

	return path, down, uploaded, nil
}
