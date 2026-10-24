package reporting

import (
	"slices"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

type incidentState struct {
	View         IncidentView
	LastBad      map[connectionid.ID]time.Time
	LocalLastBad time.Time
	LastReminder time.Time
}

func (service *Service) breaches(ctxEnd time.Time) (map[connectionid.ID]bool, bool, map[connectionid.ID]bool, bool, error) {
	window, _ := time.ParseDuration(service.settings.BreachWindow)
	history, err := service.store.history(ctxEnd.Add(-window), ctxEnd)
	if err != nil {
		return nil, false, nil, false, err
	}
	step, _ := time.ParseDuration(service.settings.SampleInterval)
	bad, observed := make(map[connectionid.ID]bool), make(map[connectionid.ID]bool)
	if len(history) != int(window/step) {
		return bad, false, observed, false, nil
	}
	for index, minute := range history {
		if minute.Partial || minute.End.Sub(minute.Start) != step {
			return bad, false, observed, false, nil
		}
		if index > 0 && !minute.Start.Equal(history[index-1].End) {
			return bad, false, observed, false, nil
		}
	}
	probeInterval, _ := time.ParseDuration(service.settings.ProbeInterval)
	byKey := make(map[ProbeKey]ProbeMinute)
	keyMinutes := make(map[ProbeKey]int)
	traffic := make(map[connectionid.ID]WANTraffic)
	trafficMinutes := make(map[connectionid.ID]int)
	localBad := false
	for _, minute := range history {
		if minute.UDPSendFailures > 0 || minute.DNSReceiveDrops > 0 {
			localBad = true
		}
		for _, probe := range minute.Probes {
			if probe.Key.Mode != Fresh || !probe.Available || time.Duration(probe.Sent)*probeInterval < time.Duration(service.settings.MinProbeSeconds)*time.Second {
				continue
			}
			total := byKey[probe.Key]
			total.Sent += probe.Sent
			total.Lost += probe.Lost
			total.DNSFailures += probe.DNSFailures
			total.Replies += probe.Replies
			total.LatencySum += probe.LatencySum
			byKey[probe.Key] = total
			keyMinutes[probe.Key]++
		}
		for _, sample := range minute.Traffic {
			if !sample.Available {
				continue
			}
			total := traffic[sample.WAN]
			total.Packets += sample.Packets
			total.Drops += sample.Drops
			traffic[sample.WAN] = total
			trafficMinutes[sample.WAN]++
		}
	}
	for key, probe := range byKey {
		if keyMinutes[key] != len(history) {
			continue
		}
		observed[key.WAN] = true
		loss := 100 * float64(probe.Lost) / float64(probe.Sent)
		dns := 100 * float64(probe.DNSFailures) / float64(probe.Sent)
		latency := float64(probe.LatencySum) / float64(time.Millisecond) / float64(probe.Replies)
		if loss > service.settings.LossPercent || dns > service.settings.DNSFailurePercent || (probe.Replies > 0 && latency >= service.settings.LatencyMilliseconds) {
			bad[key.WAN] = true
		}
	}
	for id, total := range traffic {
		if trafficMinutes[id] == len(history) && total.Drops >= service.settings.MinDrops && 100*float64(total.Drops)/float64(total.Packets+total.Drops) > service.settings.DropPercent {
			bad[id] = true
		}
	}
	return bad, localBad, observed, true, nil
}

func (service *Service) evaluateIncident(end time.Time) error {
	bad, localBad, observed, complete, err := service.breaches(end)
	if err != nil {
		return err
	}
	state := &service.state.Incident
	if state.LastBad == nil {
		state.LastBad = make(map[connectionid.ID]time.Time)
	}
	previouslyOpen := state.View.Open
	for id := range bad {
		state.LastBad[id] = end
		if !slices.Contains(state.View.Affected, id) {
			state.View.Affected = append(state.View.Affected, id)
		}
	}
	slices.Sort(state.View.Affected)
	if localBad {
		state.LocalLastBad = end
		state.View.RouterAffected = true
	}
	if len(bad) > 0 || localBad {
		if !state.View.Open {
			state.View.Open = true
			state.View.Start = end
			state.LastReminder = end
		}
	}
	if !state.View.Open {
		return nil
	}
	recovery, _ := time.ParseDuration(service.settings.RecoveryInterval)
	recovered := complete
	for _, id := range state.View.Affected {
		if !observed[id] {
			state.LastBad[id] = end
		}
		if end.Sub(state.LastBad[id]) < recovery {
			recovered = false
		}
	}
	if state.View.RouterAffected {
		if !complete {
			state.LocalLastBad = end
		}
		if end.Sub(state.LocalLastBad) < recovery {
			recovered = false
		}
	}
	if !previouslyOpen {
		service.enqueue(Incident, state.View, end)
		return nil
	}
	if recovered {
		state.View.Open = false
		service.enqueue(Recovery, state.View, end)
		*state = incidentState{LastBad: make(map[connectionid.ID]time.Time)}
		return nil
	}
	reminder, _ := time.ParseDuration(service.settings.ReminderInterval)
	if end.Sub(state.LastReminder) >= reminder {
		service.enqueue(Reminder, state.View, end)
		state.LastReminder = end
	}
	return nil
}
