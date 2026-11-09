package reporting

import (
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

func intervalWANValues(minute Minute, connectionID connectionid.ID) map[string]string {
	values := make(map[string]string)

	for _, traffic := range minute.Traffic {
		if traffic.WAN != connectionID {
			continue
		}

		values["down"] = sampleValue(
			float64(traffic.Downloaded)/minute.End.Sub(minute.Start).Seconds(),
			traffic.Available,
		)
		values["up"] = sampleValue(
			float64(traffic.Uploaded)/minute.End.Sub(minute.Start).Seconds(),
			traffic.Available,
		)
	}

	for _, probe := range minute.Probes {
		if probe.Key.WAN != connectionID {
			continue
		}

		name := probeName(probe.Key)
		values[name+"_mean"] = sampleValue(
			float64(probe.LatencySum)/float64(time.Millisecond)/float64(probe.Replies),
			probe.Available && probe.Replies > 0,
		)
		values[name+"_min"] = sampleValue(
			float64(probe.LatencyMin)/float64(time.Millisecond),
			probe.Available && probe.Replies > 0,
		)
		values[name+"_peak"] = sampleValue(
			float64(probe.LatencyMax)/float64(time.Millisecond),
			probe.Available && probe.Replies > 0,
		)
		values[name+"_loss"] = sampleValue(
			100*float64(probe.Lost)/float64(probe.Sent),
			probe.Available && probe.Sent > 0,
		)
		values[name+"_dns"] = sampleValue(
			100*float64(probe.DNSFailures)/float64(probe.Sent),
			probe.Available && probe.Sent > 0,
		)
	}

	return values
}
