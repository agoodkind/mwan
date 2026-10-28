package offload

import (
	"errors"
	"syscall"
	"time"
)

const nativeCounterSampleMinimum uint8 = 2

// NativeCounter reports cumulative hardware counts only when its validity bits are true.
type NativeCounter struct {
	Packets             uint64
	Bytes               uint64
	ObservedMonotonicNS uint64
	PacketsValid        bool
	BytesValid          bool
	Error               error
}

// NativeCounterObservation separates missing evidence from confirmed inactivity.
type NativeCounterObservation uint8

const (
	NativeCounterInvalid NativeCounterObservation = iota
	NativeCounterPending
	NativeCounterIdle
	NativeCounterActive
	NativeCounterReadinessExpired
)

// NativeCounterHistory commits complete observations across the requested directions.
type NativeCounterHistory struct {
	previous          [2]NativeCounter
	samples           uint8
	started           time.Time
	readinessDeadline time.Time
	lastActive        time.Time
	lastSample        time.Time
}

func NewNativeCounterHistory(now time.Time, readinessTimeout time.Duration) NativeCounterHistory {
	return NativeCounterHistory{
		previous: [2]NativeCounter{}, samples: 0, started: now,
		readinessDeadline: now.Add(readinessTimeout), lastActive: now, lastSample: time.Time{},
	}
}

// Restart discards counters for replaced hardware rules without inventing activity.
func (history *NativeCounterHistory) Restart(now time.Time, readinessTimeout time.Duration) {
	lastActive := history.lastActive
	*history = NewNativeCounterHistory(now, readinessTimeout)
	history.lastActive = lastActive
}

func (history *NativeCounterHistory) Observe(requested uint32, counters [2]NativeCounter, queryError error, now time.Time) NativeCounterObservation {
	if requested == 0 || requested > 3 || now.Before(history.started) || queryError != nil && !errors.Is(queryError, syscall.EAGAIN) {
		return NativeCounterInvalid
	}
	pending, active := false, false
	for index, mask := range [2]uint32{1, 2} {
		if requested&mask == 0 {
			continue
		}
		counter := counters[index]
		if counter.Error != nil {
			if !errors.Is(counter.Error, syscall.EAGAIN) || counter.PacketsValid || counter.BytesValid || counter.ObservedMonotonicNS != 0 {
				return NativeCounterInvalid
			}
			pending = true
			continue
		}
		if !counter.PacketsValid || !counter.BytesValid || counter.ObservedMonotonicNS == 0 {
			return NativeCounterInvalid
		}
		if history.samples != 0 {
			previous := history.previous[index]
			if counter.ObservedMonotonicNS <= previous.ObservedMonotonicNS || counter.Packets < previous.Packets || counter.Bytes < previous.Bytes {
				return NativeCounterInvalid
			}
			active = active || counter.Packets > previous.Packets || counter.Bytes > previous.Bytes
		}
	}
	if pending {
		if queryError == nil || history.samples != 0 {
			return NativeCounterInvalid
		}
		if !now.Before(history.readinessDeadline) {
			return NativeCounterReadinessExpired
		}
		return NativeCounterPending
	}
	if queryError != nil {
		return NativeCounterInvalid
	}
	history.previous = counters
	if history.samples == 0 || active {
		history.lastActive = now
	}
	history.lastSample = now
	if history.samples < nativeCounterSampleMinimum {
		history.samples++
	}
	if active {
		return NativeCounterActive
	}
	return NativeCounterIdle
}

func (history *NativeCounterHistory) LastActive() time.Time {
	return history.lastActive
}

func (history *NativeCounterHistory) Evictable(now time.Time, counterInterval time.Duration) bool {
	return history.samples >= nativeCounterSampleMinimum && !now.Before(history.lastSample) && now.Sub(history.lastSample) <= counterInterval
}
