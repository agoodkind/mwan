//go:build linux

package netif

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"golang.org/x/sys/unix"
)

func leaseBootTime() (time.Duration, error) {
	var stamp unix.Timespec
	// Lease validity must include time spent suspended.
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &stamp); err != nil {
		slog.Warn("read boot clock for lease recovery failed", "err", err)
		return 0, fmt.Errorf("read boot clock: %w", err)
	}
	seconds, nanoseconds := stamp.Unix()
	if seconds < 0 || nanoseconds < 0 || nanoseconds >= int64(time.Second) || seconds > (math.MaxInt64-nanoseconds)/int64(time.Second) {
		return 0, errors.New("invalid boot clock")
	}
	return time.Duration(seconds)*time.Second + time.Duration(nanoseconds), nil
}
