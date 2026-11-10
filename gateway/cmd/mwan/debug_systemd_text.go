package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func writeDebugSystemdText(logger *slog.Logger, output io.Writer, result debugSystemdResult) error {
	var text strings.Builder
	fmt.Fprintf(&text, "== units ==\nunits: %d\n", len(result.Units))

	for _, unit := range result.Units {
		fmt.Fprintf(
			&text,
			"  name: %q\n  load state: %q\n  active state: %q\n  substate: %q\n  followed: %q\n  job ID: %d\n  job type: %q\n",
			unit.Name,
			unit.LoadState,
			unit.ActiveState,
			unit.SubState,
			unit.Followed,
			unit.JobId,
			unit.JobType,
		)
	}

	fmt.Fprintf(&text, "\n== startup timings ==\ntimings: %d\n", len(result.Timings))

	for _, timing := range result.Timings {
		fmt.Fprintf(&text, "  name: %q\n  duration (ns): %d\n", timing.Name, timing.Duration)
	}

	fmt.Fprintf(&text, "\n== mwan unit timings ==\ntimings: %d\n", len(result.Focus))

	for _, timing := range result.Focus {
		fmt.Fprintf(&text, "  name: %q\n  present: %t\n", timing.Name, timing.Present)
		if timing.Present {
			fmt.Fprintf(&text, "  duration (ns): %d\n", timing.Duration)
		}
	}

	return WriteDebugText(logger, output, text.String())
}
