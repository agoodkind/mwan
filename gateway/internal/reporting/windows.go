package reporting

import "goodkind.io/mwan/internal/connectionid"

func (service *Service) appendDropBreachSignals(
	traffic map[connectionid.ID]WANTraffic,
	trafficMinutes map[connectionid.ID]int,
	history []Minute,
	bad map[connectionid.ID]bool,
	signals []string,
) []string {
	for connectionID, total := range traffic {
		if trafficMinutes[connectionID] == len(history) &&
			total.Drops >= service.settings.MinDrops &&
			100*float64(total.Drops)/float64(
				total.Packets+total.Drops,
			) > service.settings.DropPercent {
			bad[connectionID] = true
			signals = append(
				signals,
				service.renderer.label(
					connectionID,
				)+" interface drops exceeded the configured threshold.",
			)
		}
	}

	return signals
}
