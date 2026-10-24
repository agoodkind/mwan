package reporting

import (
	"fmt"
	"slices"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

type incidentState struct {
	View         IncidentView                  `json:"View"`
	LastBad      map[connectionid.ID]time.Time `json:"LastBad"`
	LocalLastBad time.Time                     `json:"LocalLastBad"`
	LastReminder time.Time                     `json:"LastReminder"`
}

func completeBreachHistory(history []Minute, window, step time.Duration) bool {
	if len(history) != int(window/step) {
		return false
	}
	for index, minute := range history {
		if minute.Partial || minute.End.Sub(minute.Start) != step {
			return false
		}
		if index > 0 && !minute.Start.Equal(history[index-1].End) {
			return false
		}
	}
	return true
}

func (service *Service) breaches(ctxEnd time.Time) (map[connectionid.ID]bool, bool, map[connectionid.ID]bool, bool, []string, error) {
	window, _ := time.ParseDuration(service.settings.BreachWindow)
	history, err := service.store.history(ctxEnd.Add(-window), ctxEnd)
	if err != nil {
		return nil, false, nil, false, nil, err
	}
	step, _ := time.ParseDuration(service.settings.SampleInterval)
	bad, observed := make(map[connectionid.ID]bool), make(map[connectionid.ID]bool)
	if !completeBreachHistory(history, window, step) {
		return bad, false, observed, false, nil, nil
	}
	probeInterval, _ := time.ParseDuration(service.settings.ProbeInterval)
	byKey := make(map[ProbeKey]ProbeMinute)
	keyMinutes := make(map[ProbeKey]int)
	for _, key := range service.expectedProbeKeys() {
		keyMinutes[key] = 0
	}
	traffic := make(map[connectionid.ID]WANTraffic)
	trafficMinutes := make(map[connectionid.ID]int)
	localBad := false
	var signals []string
	invalidWAN := make(map[connectionid.ID]bool)
	for _, minute := range history {
		if minute.UDPSendFailures > 0 || minute.DNSReceiveDrops > 0 {
			localBad = true
			signals = append(signals, "Local gateway UDP error counters increased.")
		}
		for _, probe := range minute.Probes {
			if probe.Key.Mode != Fresh || !probe.Available || float64(probe.Sent)*probeInterval.Seconds() < float64(service.settings.MinProbeSeconds) {
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
	for key := range keyMinutes {
		if keyMinutes[key] != len(history) {
			invalidWAN[key.WAN] = true
			continue
		}
		observed[key.WAN] = true
		probe := byKey[key]
		reasons := service.probeReasons(key, probe)
		if len(reasons) > 0 {
			bad[key.WAN] = true
			signals = append(signals, reasons...)
		}
	}
	for id := range invalidWAN {
		observed[id] = false
	}
	for id, total := range traffic {
		if trafficMinutes[id] == len(history) && total.Drops >= service.settings.MinDrops && 100*float64(total.Drops)/float64(total.Packets+total.Drops) > service.settings.DropPercent {
			bad[id] = true
			signals = append(signals, service.renderer.label(id)+" interface drops exceeded the configured threshold.")
		}
	}
	for _, id := range service.store.wanIDs() {
		observed[id] = observed[id] && trafficMinutes[id] == len(history)
	}
	return bad, localBad, observed, true, signals, nil
}

func (service *Service) expectedProbeKeys() []ProbeKey {
	var keys []ProbeKey
	for name, wan := range service.store.network.WAN {
		id := connectionid.Resolve(service.store.network.ConnectionIDs[name].String(), name, wan.Iface)
		var families []Family
		if wan.TranslationV4 != nil {
			families = append(families, IPv4)
		}
		if wan.TranslationV6 != nil {
			families = append(families, IPv6)
		}
		for _, family := range families {
			for _, target := range []Target{Gateway, Cloudflare} {
				keys = append(keys, ProbeKey{WAN: id, Family: family, Target: target, Mode: Fresh})
			}
		}
	}
	return keys
}

func (service *Service) evaluateIncident(end time.Time) error {
	bad, localBad, observed, complete, signals, err := service.breaches(end)
	if err != nil {
		return err
	}
	state := &service.state.Incident
	if state.LastBad == nil {
		state.LastBad = make(map[connectionid.ID]time.Time)
	}
	previouslyOpen := state.View.Open
	for _, signal := range signals {
		if !slices.Contains(state.View.Signals, signal) {
			state.View.Signals = append(state.View.Signals, signal)
		}
	}
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
		var empty incidentState
		empty.LastBad = make(map[connectionid.ID]time.Time)
		*state = empty
		return nil
	}
	reminder, _ := time.ParseDuration(service.settings.ReminderInterval)
	if end.Sub(state.LastReminder) >= reminder {
		service.enqueue(Reminder, state.View, end)
		state.LastReminder = end
	}
	return nil
}

func (service *Service) probeReasons(key ProbeKey, probe ProbeMinute) []string {
	prefix := fmt.Sprintf("%s %s IPv%d probes", service.renderer.label(key.WAN), key.Target, key.Family)
	var reasons []string
	if 100*float64(probe.Lost)/float64(probe.Sent) > service.settings.LossPercent {
		reasons = append(reasons, prefix+" exceeded the packet-loss threshold.")
	}
	if 100*float64(probe.DNSFailures)/float64(probe.Sent) > service.settings.DNSFailurePercent {
		reasons = append(reasons, prefix+" exceeded the DNS-failure threshold.")
	}
	if probe.Replies > 0 && float64(probe.LatencySum)/float64(time.Millisecond)/float64(probe.Replies) >= service.settings.LatencyMilliseconds {
		reasons = append(reasons, prefix+" exceeded the latency threshold.")
	}
	return reasons
}
