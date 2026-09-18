package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"text/tabwriter"
	"time"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
)

// debugSystemdSlowestCount bounds the slowest-starters table, matching the
// shell's systemd-analyze blame | head -20.
const debugSystemdSlowestCount = 20

// debugSystemdFocusUnits are the units whose startup timing prints
// individually, the same set the shell's critical-chain loop covered.
var debugSystemdFocusUnits = []string{
	"mwan-agent.service",
	"mwan-ifmgr@wan.service",
	"systemd-networkd.service",
	"cloudflared.service",
	"nftables.service",
}

// unitTiming is one unit's startup duration derived from its monotonic
// activation timestamps.
type unitTiming struct {
	Name     string
	Duration time.Duration
}

// showDebugSystemd renders failed units, activating units, the slowest
// starters, and the mwan-relevant unit timings over the systemd D-Bus API,
// with no systemctl or systemd-analyze subprocess.
func showDebugSystemd(
	ctx context.Context,
	output io.Writer,
	logger *slog.Logger,
) error {
	conn, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return debugWrappedError(logger, "connect to systemd dbus", err)
	}
	defer conn.Close()

	units, err := conn.ListUnitsContext(ctx)
	if err != nil {
		return debugWrappedError(logger, "list systemd units", err)
	}

	fmt.Fprintln(output, "== failed units ==")
	printUnitsInState(output, units, "failed")
	fmt.Fprintln(output, "\n== units stuck in activating ==")
	printUnitsInState(output, units, "activating")

	timings := unitTimings(ctx, conn, units, logger)
	fmt.Fprintf(output, "\n== slowest starters (top %d) ==\n", debugSystemdSlowestCount)
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for i, timing := range timings {
		if i >= debugSystemdSlowestCount {
			break
		}
		fmt.Fprintf(writer, "%s\t%s\n", timing.Duration.Round(time.Millisecond), timing.Name)
	}
	_ = writer.Flush()

	fmt.Fprintln(output, "\n== mwan unit timings ==")
	byName := make(map[string]time.Duration, len(timings))
	for _, timing := range timings {
		byName[timing.Name] = timing.Duration
	}
	focusWriter := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, name := range debugSystemdFocusUnits {
		duration, ok := byName[name]
		value := "not measured"
		if ok {
			value = duration.Round(time.Millisecond).String()
		}
		fmt.Fprintf(focusWriter, "%s\t%s\n", name, value)
	}
	_ = focusWriter.Flush()
	return nil
}

func printUnitsInState(
	output io.Writer,
	units []systemddbus.UnitStatus,
	state string,
) {
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

// unitTimings derives each active unit's startup duration from the monotonic
// span between leaving inactive and entering active, the same quantity
// systemd-analyze blame reports, sorted slowest first.
func unitTimings(
	ctx context.Context,
	conn *systemddbus.Conn,
	units []systemddbus.UnitStatus,
	logger *slog.Logger,
) []unitTiming {
	timings := make([]unitTiming, 0, len(units))
	for _, unit := range units {
		if unit.ActiveState != "active" {
			continue
		}
		exitMono, exitOK := monotonicProperty(
			ctx, conn, logger, unit.Name, "InactiveExitTimestampMonotonic",
		)
		enterMono, enterOK := monotonicProperty(
			ctx, conn, logger, unit.Name, "ActiveEnterTimestampMonotonic",
		)
		if !exitOK || !enterOK || enterMono <= exitMono {
			continue
		}
		spanMicros := enterMono - exitMono
		if spanMicros > uint64(math.MaxInt64/int64(time.Microsecond)) {
			continue
		}
		timings = append(timings, unitTiming{
			Name:     unit.Name,
			Duration: time.Duration(spanMicros) * time.Microsecond,
		})
	}
	sort.Slice(timings, func(i, j int) bool {
		return timings[i].Duration > timings[j].Duration
	})
	return timings
}

// monotonicProperty reads one CLOCK_MONOTONIC microsecond timestamp property
// through the typed D-Bus variant store, so no empty-interface map crosses
// this package's boundary.
func monotonicProperty(
	ctx context.Context,
	conn *systemddbus.Conn,
	logger *slog.Logger,
	unitName string,
	propertyName string,
) (uint64, bool) {
	property, err := conn.GetUnitPropertyContext(ctx, unitName, propertyName)
	if err != nil || property == nil {
		logger.DebugContext(
			ctx, "read unit property failed",
			"unit", unitName, "property", propertyName, "err", err,
		)
		return 0, false
	}
	var monotonic uint64
	if storeErr := property.Value.Store(&monotonic); storeErr != nil {
		logger.DebugContext(
			ctx, "unit property is not a monotonic timestamp",
			"unit", unitName, "property", propertyName, "err", storeErr,
		)
		return 0, false
	}
	return monotonic, true
}
