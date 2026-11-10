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
	// NativeCounterInvalid rejects incomplete or nonmonotonic evidence and errors
	// other than a supported initial EAGAIN. Callers must stop using the rules.
	NativeCounterInvalid NativeCounterObservation = iota
	// NativeCounterPending permits initial EAGAIN before the fixed deadline.
	// It does not establish a counter baseline or confirm inactivity.
	NativeCounterPending
	// NativeCounterIdle reports a complete first baseline or unchanged counts.
	// A first baseline does not establish activity before that observation.
	NativeCounterIdle
	// NativeCounterActive confirms an increase after a complete counter baseline.
	NativeCounterActive
	// NativeCounterReadinessExpired rejects initial EAGAIN at or after the fixed
	// deadline. Callers must remove rules that still lack complete counters.
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

// NewNativeCounterHistory uses now as the initial activity baseline and fixes
// the initial EAGAIN deadline at now plus readinessTimeout. A nonpositive
// readinessTimeout prevents waiting for initial EAGAIN.
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

// Observe updates history only after every requested direction supplies valid,
// nondecreasing counts and a newer sample time. Initial EAGAIN can remain pending
// until the fixed deadline. Only increases after a complete baseline report activity.
func (history *NativeCounterHistory) Observe(requested uint32, counters [2]NativeCounter, queryError error, now time.Time) NativeCounterObservation {
	if requested == 0 || requested > 3 || now.Before(history.started) || queryError != nil && !errors.Is(queryError, syscall.EAGAIN) {
		return NativeCounterInvalid
	}
	pending, active := false, false
	for index, mask := range [2]uint32{1, 2} {
		if requested&mask == 0 {
			continue
		}
		observation := history.directionObservation(index, counters[index])
		if observation == NativeCounterInvalid {
			return NativeCounterInvalid
		}
		pending = pending || observation == NativeCounterPending
		active = active || observation == NativeCounterActive
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

func (history *NativeCounterHistory) directionObservation(index int, counter NativeCounter) NativeCounterObservation {
	if counter.Error != nil {
		if !errors.Is(counter.Error, syscall.EAGAIN) || counter.PacketsValid || counter.BytesValid || counter.ObservedMonotonicNS != 0 {
			return NativeCounterInvalid
		}
		return NativeCounterPending
	}
	if !counter.PacketsValid || !counter.BytesValid || counter.ObservedMonotonicNS == 0 {
		return NativeCounterInvalid
	}
	if history.samples == 0 {
		return NativeCounterIdle
	}
	previous := history.previous[index]
	if counter.ObservedMonotonicNS <= previous.ObservedMonotonicNS || counter.Packets < previous.Packets || counter.Bytes < previous.Bytes {
		return NativeCounterInvalid
	}
	if counter.Packets > previous.Packets || counter.Bytes > previous.Bytes {
		return NativeCounterActive
	}
	return NativeCounterIdle
}

// LastActive returns the initial activity baseline or the time of the latest
// complete counter baseline or increase. Pending and rejected reads do not change it.
func (history *NativeCounterHistory) LastActive() time.Time {
	return history.lastActive
}

// Latest returns the last complete hardware observation and its collection time
// for the current rule set. The boolean is false before a valid observation and
// after Restart. Pending and rejected reads do not replace this evidence.
func (history *NativeCounterHistory) Latest() ([2]NativeCounter, time.Time, bool) {
	return history.previous, history.lastSample, history.samples != 0
}

// Evictable requires two complete samples and a last sample no older than
// counterInterval. Pending and rejected reads do not supply new eviction evidence.
func (history *NativeCounterHistory) Evictable(now time.Time, counterInterval time.Duration) bool {
	return history.samples >= nativeCounterSampleMinimum && !now.Before(history.lastSample) && now.Sub(history.lastSample) <= counterInterval
}
