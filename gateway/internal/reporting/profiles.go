//go:build linux

package reporting

import (
	"slices"
	"strconv"

	"goodkind.io/send-email/mailer"
)

const (
	availableProfileStatus = "Available"
	profileStatusHeading   = "Status"
)

func appendLatestVPPProfile(blocks []mailer.ContentBlock, history []Minute) []mailer.ContentBlock {
	for _, minute := range slices.Backward(history) {
		profile := minute.VPPProfile
		if profile == nil {
			continue
		}

		status := availableProfileStatus
		if !profile.Available {
			status = "Unavailable"
		}

		blocks = append(
			blocks,
			tableBlock(
				"Latest VPP CPU profile",
				[]string{profileStatusHeading, "Duration", "Threads", "Measurement limit"},
				[][]string{
					{
						status,
						profile.Duration.String(),
						strconv.Itoa(len(profile.Threads)),
						profile.Reason,
					},
				},
			),
		)
		samples := make([][]string, 0, len(profile.Threads))
		if len(profile.Threads) == 0 {
			samples = nil
		}

		for _, thread := range profile.Threads {
			clock := "Unavailable"
			if thread.CPUClockAvailable {
				clock = thread.CPUClockTime.String()
			}

			threadStatus := availableProfileStatus
			if !thread.Available {
				threadStatus = "Unavailable: " + thread.Reason
			}

			samples = append(
				samples,
				[]string{
					thread.Thread.Name,
					clock,
					strconv.FormatUint(thread.Counts.PacketWork, 10),
					strconv.FormatUint(thread.Counts.Other, 10),
					strconv.FormatUint(thread.Counts.Unresolved, 10),
					strconv.FormatUint(thread.Counts.Lost, 10),
					threadStatus,
				},
			)
		}

		blocks = append(
			blocks,
			tableBlock(
				"Native VPP instruction samples",
				[]string{
					"Thread",
					"CPU clock",
					"Recognized packet function samples",
					"Other samples",
					"Unresolved samples",
					"Lost samples",
					profileStatusHeading,
				},
				samples,
			),
		)
		blocks = append(blocks, vppQueueContent(profile.QueuePolling)...)

		break
	}

	return blocks
}
