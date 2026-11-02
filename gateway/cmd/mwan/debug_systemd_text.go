package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
)

func writeDebugSystemdText(logger *slog.Logger, output io.Writer, result debugSystemdResult) error {
	var text strings.Builder
	fmt.Fprintln(&text, "== failed units ==")
	printUnitsInState(&text, result.Units, "failed")
	fmt.Fprintln(&text, "\n== units stuck in activating ==")
	printUnitsInState(&text, result.Units, "activating")

	fmt.Fprintf(&text, "\n== slowest starters (top %d) ==\n", debugSystemdSlowestCount)
	writer := tabwriter.NewWriter(&text, 0, 4, 2, ' ', 0)
	for i, timing := range result.Timings {
		if i >= debugSystemdSlowestCount {
			break
		}
		fmt.Fprintf(writer, "%s\t%s\n", timing.Duration.Round(time.Millisecond), timing.Name)
	}
	if err := writer.Flush(); err != nil {
		logger.Warn("flush systemd timings failed", "err", err)
		return fmt.Errorf("flush systemd timings: %w", err)
	}

	fmt.Fprintln(&text, "\n== mwan unit timings ==")
	focusWriter := tabwriter.NewWriter(&text, 0, 4, 2, ' ', 0)
	for _, timing := range result.Focus {
		value := "not measured"
		if timing.Present {
			value = timing.Duration.Round(time.Millisecond).String()
		}
		fmt.Fprintf(focusWriter, "%s\t%s\n", timing.Name, value)
	}
	if err := focusWriter.Flush(); err != nil {
		logger.Warn("flush systemd focus timings failed", "err", err)
		return fmt.Errorf("flush systemd focus timings: %w", err)
	}
	return WriteDebugText(logger, output, text.String())
}

func printUnitsInState(output io.Writer, units []systemddbus.UnitStatus, state string) {
	count := 0
	for _, unit := range units {
		if unit.ActiveState != state {
			continue
		}
		fmt.Fprintf(output, "%s %s (%s)\n", unit.Name, unit.ActiveState, unit.SubState)
		count++
	}
	if count == 0 {
		fmt.Fprintln(output, "none")
	}
}
