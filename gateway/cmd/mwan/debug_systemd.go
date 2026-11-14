package main

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
)

// debugSystemdFocusUnits are the units whose startup timing prints
// individually, the same set the shell's critical-chain loop covered.
func debugSystemdFocusUnits() []string {
	return []string{
		"mwan-agent.service",
		"mwan-ifmgr@wan.service",
		"systemd-networkd.service",
		"cloudflared.service",
		"nftables.service",
	}
}

// unitTiming is one unit's startup duration derived from its monotonic
// activation timestamps.
type unitTiming struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration_ns"`
}

type debugFocusTiming struct {
	unitTiming
	Present bool `json:"present"`
}

type debugSystemdResult struct {
	Units   []systemddbus.UnitStatus `json:"units"`
	Timings []unitTiming             `json:"timings"`
	Focus   []debugFocusTiming       `json:"focus"`
}

func collectDebugSystemd(
	ctx context.Context,
	logger *slog.Logger,
) (debugSystemdResult, error) {
	conn, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return debugSystemdResult{}, debugWrappedError(logger, "connect to systemd dbus", err)
	}
	defer conn.Close()

	units, err := conn.ListUnitsContext(ctx)
	if err != nil {
		return debugSystemdResult{}, debugWrappedError(logger, "list systemd units", err)
	}

	timings := unitTimings(ctx, conn, units, logger)
	focusUnits := debugSystemdFocusUnits()

	result := debugSystemdResult{
		Units:   units,
		Timings: timings,
		Focus:   make([]debugFocusTiming, 0, len(focusUnits)),
	}
	for _, name := range focusUnits {
		focus := debugFocusTiming{unitTiming: unitTiming{Name: name}}

		for _, timing := range timings {
			if timing.Name == name {
				focus.unitTiming = timing
				focus.Present = true
				break
			}
		}

		result.Focus = append(result.Focus, focus)
	}

	return result, nil
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
			ctx,
			conn,
			logger,
			unit.Name,
			"InactiveExitTimestampMonotonic",
		)
		enterMono, enterOK := monotonicProperty(
			ctx,
			conn,
			logger,
			unit.Name,
			"ActiveEnterTimestampMonotonic",
		)
		if !exitOK || !enterOK || enterMono <= exitMono {
			continue
		}

		spanMicros := enterMono - exitMono
		if spanMicros > uint64(math.MaxInt64/int64(time.Microsecond)) {
			continue
		}

		timings = append(
			timings,
			unitTiming{
				Name:     unit.Name,
				Duration: time.Duration(spanMicros) * time.Microsecond,
			},
		)
	}

	sort.Slice(
		timings,
		func(i, j int) bool {
			return timings[i].Duration > timings[j].Duration
		},
	)

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
			ctx,
			"read unit property failed",
			"unit",
			unitName,
			"property",
			propertyName,
			"err",
			err,
		)

		return 0, false
	}

	var monotonic uint64
	if storeErr := property.Value.Store(&monotonic); storeErr != nil {
		logger.DebugContext(
			ctx,
			"unit property is not a monotonic timestamp",
			"unit",
			unitName,
			"property",
			propertyName,
			"err",
			storeErr,
		)

		return 0, false
	}

	return monotonic, true
}
