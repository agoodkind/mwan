package reporting

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/vpp"
	"goodkind.io/send-email/mailer"
)

// ConfigureVPP requires complete settings before optional VPP measurements can start.
func (collector *Collector) ConfigureVPP(section *config.VPPSection) error {
	if section == nil {
		return nil
	}
	if err := section.Validate(); err != nil {
		failure := fmt.Errorf("validate VPP reporting settings: %w", err)
		collector.log.Error("VPP reporting settings rejected", "error", failure)
		return failure
	}
	if section.Profile {
		duration, _ := time.ParseDuration(section.ProfileDuration)
		interval, _ := time.ParseDuration(collector.settings.SampleInterval)
		if duration > interval {
			failure := fmt.Errorf("configure VPP reporting: profile_duration must not exceed the reporting sample interval")
			collector.log.Error("VPP reporting profile duration rejected", "error", failure)
			return failure
		}
	}
	collector.vpp = section
	return nil
}

func (collector *Collector) profileVPP(ctx context.Context) <-chan vpp.ProfileResult {
	if collector.vpp == nil || !collector.vpp.Profile {
		return nil
	}
	results := make(chan vpp.ProfileResult, 1)
	go func() {
		var result vpp.ProfileResult
		defer func() {
			if recovered := recover(); recovered != nil {
				failure := fmt.Errorf("collect VPP CPU profile: panic: %v", recovered)
				collector.log.ErrorContext(ctx, "VPP CPU profiling panicked", "error", failure)
				result.Available = false
				result.Reason = failure.Error()
			}
			results <- result
		}()
		settings, err := collector.vpp.Endpoints()
		if err != nil {
			failure := fmt.Errorf("configure VPP CPU profile connection: %w", err)
			collector.log.ErrorContext(ctx, "VPP CPU profiling configuration failed", "error", failure)
			result.Reason = failure.Error()
			return
		}
		duration, _ := time.ParseDuration(collector.vpp.ProfileDuration)
		result, err = vpp.ProfileWithOptions(ctx, settings, collector.vpp.ProfileExecutable, duration, vpp.ProfileOptions{QueuePolling: collector.vpp.ProfileQueuePolling})
		if err != nil {
			failure := fmt.Errorf("collect VPP CPU profile: %w", err)
			collector.log.WarnContext(ctx, "VPP CPU profile unavailable", "error", failure)
			result.Available = false
			result.Reason = failure.Error()
		}
	}()
	return results
}

func (collector *Collector) vppCounters(ctx context.Context) map[string]vpp.InterfaceCounters {
	if collector.vpp == nil {
		return nil
	}
	settings, err := collector.vpp.Endpoints()
	if err != nil {
		failure := fmt.Errorf("configure VPP interface counter connection: %w", err)
		collector.log.ErrorContext(ctx, "VPP reporting configuration failed", "error", failure)
		return nil
	}
	counters, err := vpp.ReadInterfaceCounters(ctx, settings)
	if err != nil {
		failure := fmt.Errorf("collect VPP interface counters: %w", err)
		collector.log.WarnContext(ctx, "VPP reporting counters unavailable", "error", failure)
		return nil
	}
	return counters
}

func (collector *Collector) vppTraffic(previous, next map[string]vpp.InterfaceCounters) []WANTraffic {
	if collector.vpp == nil || collector.network.VPP == nil {
		return nil
	}
	var result []WANTraffic
	for _, resource := range collector.vpp.Interface {
		for _, connection := range collector.network.VPP.Connections {
			if connection.ID.String() != resource.ConnectionID {
				continue
			}
			var traffic WANTraffic
			traffic.WAN = connection.ID
			before, beforeExists := previous[connection.Name]
			after, afterExists := next[connection.Name]
			complete := beforeExists && afterExists && before.Generation == after.Generation && before.Counters.InterfaceIndex == after.Counters.InterfaceIndex && before.RXAvailable && before.TXAvailable && before.DropsAvailable && after.RXAvailable && after.TXAvailable && after.DropsAvailable
			old, current := before.Counters, after.Counters
			if complete && current.Rx.Bytes >= old.Rx.Bytes && current.Tx.Bytes >= old.Tx.Bytes && current.Rx.Packets >= old.Rx.Packets && current.Tx.Packets >= old.Tx.Packets && current.Drops >= old.Drops {
				traffic.Downloaded = current.Rx.Bytes - old.Rx.Bytes
				traffic.Uploaded = current.Tx.Bytes - old.Tx.Bytes
				traffic.Packets = current.Rx.Packets - old.Rx.Packets + current.Tx.Packets - old.Tx.Packets
				traffic.Drops = current.Drops - old.Drops
				traffic.Available = true
			}
			result = append(result, traffic)
		}
	}
	return result
}

func (renderer *Renderer) vppContent(history []Minute) []mailer.ContentBlock {
	if renderer.network.VPP == nil {
		return nil
	}
	var rows [][]string
	for _, connection := range renderer.network.VPP.Connections {
		var received, transmitted, drops uint64
		var measured, unavailable int
		for _, minute := range history {
			for _, traffic := range minute.VPPTraffic {
				if traffic.WAN != connection.ID {
					continue
				}
				if !traffic.Available {
					unavailable++
					continue
				}
				measured++
				received += traffic.Downloaded
				transmitted += traffic.Uploaded
				drops += traffic.Drops
			}
		}
		if measured+unavailable == 0 {
			continue
		}
		rows = append(rows, []string{connection.Name, strconv.FormatUint(received, 10), strconv.FormatUint(transmitted, 10), strconv.FormatUint(drops, 10), strconv.Itoa(measured), strconv.Itoa(unavailable)})
	}
	blocks := []mailer.ContentBlock{tableBlock("VPP interface measurements", []string{"Interface", "Received bytes", "Transmitted bytes", "Drops", "Measured intervals", "Unavailable intervals"}, rows)}
	for _, minute := range slices.Backward(history) {
		profile := minute.VPPProfile
		if profile == nil {
			continue
		}
		status := "Available"
		if !profile.Available {
			status = "Unavailable"
		}
		blocks = append(blocks, tableBlock("Latest VPP CPU profile", []string{"Status", "Duration", "Threads", "Measurement limit"}, [][]string{{status, profile.Duration.String(), strconv.Itoa(len(profile.Threads)), profile.Reason}}))
		var samples [][]string
		for _, thread := range profile.Threads {
			clock := "Unavailable"
			if thread.CPUClockAvailable {
				clock = thread.CPUClockTime.String()
			}
			threadStatus := "Available"
			if !thread.Available {
				threadStatus = "Unavailable: " + thread.Reason
			}
			samples = append(samples, []string{thread.Thread.Name, clock, strconv.FormatUint(thread.Counts.PacketWork, 10), strconv.FormatUint(thread.Counts.Other, 10), strconv.FormatUint(thread.Counts.Unresolved, 10), strconv.FormatUint(thread.Counts.Lost, 10), threadStatus})
		}
		blocks = append(blocks, tableBlock("Native VPP instruction samples", []string{"Thread", "CPU clock", "Recognized packet function samples", "Other samples", "Unresolved samples", "Lost samples", "Status"}, samples))
		if polling := profile.QueuePolling; polling != nil {
			status := "Available"
			if !polling.Available {
				status = "Unavailable: " + polling.Reason
			}
			blocks = append(blocks, tableBlock("VPP receive queue intervals", []string{"Status", "Duration", "Measurement limit"}, [][]string{{status, polling.Duration.String(), "Elapsed receive intervals include preemption. They do not measure consumed CPU time."}}))
			if polling.Available {
				var queues [][]string
				for _, queue := range polling.Queues {
					queues = append(queues, []string{strconv.FormatUint(uint64(queue.ThreadID), 10), strconv.FormatUint(uint64(queue.InterfaceIndex), 10), strconv.FormatUint(uint64(queue.QueueID), 10), strconv.FormatUint(queue.EmptyCalls, 10), queue.EmptyElapsed.String(), strconv.FormatUint(queue.NonemptyCalls, 10), queue.NonemptyElapsed.String(), strconv.FormatUint(queue.Packets, 10), queue.RefillElapsed.String()})
				}
				blocks = append(blocks, tableBlock("VPP receive queue checks", []string{"Thread", "Interface index", "Queue", "Empty checks", "Empty elapsed", "Nonempty checks", "Nonempty elapsed", "Packets", "Refill elapsed"}, queues))
			}
		}
		break
	}
	return blocks
}
