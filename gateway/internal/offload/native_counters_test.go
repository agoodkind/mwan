package offload_test

import (
	"fmt"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/internal/offload"
)

func TestNativeCounterInitialReadiness(t *testing.T) {
	started := time.Unix(1, 0)
	history := offload.NewNativeCounterHistory(started, 3*time.Second)
	pending := [2]offload.NativeCounter{{Error: syscall.EAGAIN}, {Error: syscall.EAGAIN}}
	for _, elapsed := range []time.Duration{time.Second, 2 * time.Second} {
		now := started.Add(elapsed)
		if got := history.Observe(3, pending, fmt.Errorf("native query: %w", syscall.EAGAIN), now); got != offload.NativeCounterPending {
			t.Fatalf("initial async counters did not remain pending: %d", got)
		}
		if history.LastActive() != started || history.Evictable(now, time.Second) {
			t.Fatal("missing counters changed activity or permitted eviction")
		}
	}
	if got := history.Observe(3, pending, syscall.EAGAIN, started.Add(3*time.Second)); got != offload.NativeCounterReadinessExpired {
		t.Fatalf("pending counters survived the fixed readiness deadline: %d", got)
	}
	if history.LastActive() != started {
		t.Fatal("a failed readiness deadline changed activity")
	}
}

func TestNativeCountersRequireCompleteObservations(t *testing.T) {
	started := time.Unix(1, 0)
	history := offload.NewNativeCounterHistory(started, 3*time.Second)
	partial := [2]offload.NativeCounter{nativeCounter(50, 500, 3), {Error: syscall.EAGAIN}}
	if got := history.Observe(3, partial, syscall.EAGAIN, started.Add(time.Second)); got != offload.NativeCounterPending {
		t.Fatalf("one initial pending direction rejected the installed pair: %d", got)
	}
	baseline := [2]offload.NativeCounter{nativeCounter(0, 0, 1), nativeCounter(0, 0, 1)}
	if got := history.Observe(3, baseline, nil, started.Add(2*time.Second)); got != offload.NativeCounterIdle {
		t.Fatalf("partial counters contaminated the first complete baseline: %d", got)
	}
	if history.Evictable(started.Add(2*time.Second), time.Second) {
		t.Fatal("one complete sample permitted eviction")
	}
	failed := [2]offload.NativeCounter{nativeCounter(50, 500, 3), {Error: syscall.EIO}}
	if got := history.Observe(3, failed, syscall.EIO, started.Add(3*time.Second)); got != offload.NativeCounterInvalid {
		t.Fatalf("a permanent error did not reject the observation: %d", got)
	}
	activity := [2]offload.NativeCounter{nativeCounter(1, 10, 2), nativeCounter(1, 10, 2)}
	activeAt := started.Add(4 * time.Second)
	if got := history.Observe(3, activity, nil, activeAt); got != offload.NativeCounterActive {
		t.Fatalf("a rejected partial observation changed the complete baseline: %d", got)
	}
	if history.LastActive() != activeAt || !history.Evictable(activeAt, time.Second) {
		t.Fatal("complete counter increments did not establish recent activity")
	}
	if history.Evictable(activeAt.Add(time.Second+time.Nanosecond), time.Second) {
		t.Fatal("stale counter evidence permitted eviction")
	}
	idle := [2]offload.NativeCounter{nativeCounter(1, 10, 3), nativeCounter(1, 10, 3)}
	if got := history.Observe(3, idle, nil, activeAt.Add(time.Second)); got != offload.NativeCounterIdle || history.LastActive() != activeAt {
		t.Fatalf("valid unchanged counters refreshed activity: observation=%d active=%s", got, history.LastActive())
	}
	if got := history.Observe(3, partial, syscall.EAGAIN, activeAt.Add(2*time.Second)); got != offload.NativeCounterInvalid {
		t.Fatalf("EAGAIN after valid counter evidence became initial readiness: %d", got)
	}
}

func TestNativeCountersRejectInvalidEvidence(t *testing.T) {
	started := time.Unix(1, 0)
	valid := [2]offload.NativeCounter{nativeCounter(10, 100, 2), nativeCounter(10, 100, 2)}
	for _, failure := range []syscall.Errno{syscall.ESTALE, syscall.ECANCELED, syscall.ENOTSUP, syscall.ENOENT, syscall.EBUSY, syscall.EIO} {
		t.Run(failure.Error(), func(t *testing.T) {
			history := offload.NewNativeCounterHistory(started, time.Minute)
			counters := valid
			counters[1] = offload.NativeCounter{Error: failure}
			if got := history.Observe(3, counters, fmt.Errorf("native query: %w", failure), started.Add(time.Second)); got != offload.NativeCounterInvalid {
				t.Fatalf("permanent query error became pending: %d", got)
			}
			if history.LastActive() != started || history.Evictable(started.Add(time.Second), time.Second) {
				t.Fatal("invalid counters changed activity or permitted eviction")
			}
		})
	}
	for _, test := range []struct {
		name       string
		requested  uint32
		counters   [2]offload.NativeCounter
		queryError error
	}{
		{"missing-mask", 0, valid, nil},
		{"unknown-direction", 4, valid, nil},
		{"missing-leg", 3, [2]offload.NativeCounter{valid[0], {}}, nil},
		{"missing-validity", 3, [2]offload.NativeCounter{valid[0], {Packets: 10, Bytes: 100, ObservedMonotonicNS: 2, PacketsValid: true}}, nil},
		{"missing-timestamp", 3, [2]offload.NativeCounter{valid[0], nativeCounter(10, 100, 0)}, nil},
		{"unreported-EAGAIN", 3, [2]offload.NativeCounter{valid[0], {Error: syscall.EAGAIN}}, nil},
		{"unexplained-EAGAIN", 3, valid, syscall.EAGAIN},
		{"EAGAIN-with-validity", 3, [2]offload.NativeCounter{valid[0], {Error: syscall.EAGAIN, PacketsValid: true}}, syscall.EAGAIN},
		{"EAGAIN-with-permanent-leg", 3, [2]offload.NativeCounter{{Error: syscall.EAGAIN}, {Error: syscall.EIO}}, syscall.EAGAIN},
	} {
		t.Run(test.name, func(t *testing.T) {
			history := offload.NewNativeCounterHistory(started, time.Minute)
			if got := history.Observe(test.requested, test.counters, test.queryError, started.Add(time.Second)); got != offload.NativeCounterInvalid {
				t.Fatalf("invalid requested evidence did not reject: %d", got)
			}
		})
	}
	for _, counter := range []offload.NativeCounter{
		nativeCounter(11, 110, 2), nativeCounter(9, 110, 3), nativeCounter(11, 99, 3),
	} {
		history := offload.NewNativeCounterHistory(started, time.Minute)
		if got := history.Observe(3, valid, nil, started.Add(time.Second)); got != offload.NativeCounterIdle {
			t.Fatalf("complete baseline did not initialize: %d", got)
		}
		if got := history.Observe(3, [2]offload.NativeCounter{nativeCounter(11, 110, 3), counter}, nil, started.Add(2*time.Second)); got != offload.NativeCounterInvalid {
			t.Fatalf("nonmonotonic evidence did not reject: %d", got)
		}
		if history.LastActive() != started.Add(time.Second) {
			t.Fatal("nonmonotonic evidence refreshed activity")
		}
	}
}

func TestNativeCountersSingleDirectionAndResumption(t *testing.T) {
	started := time.Unix(1, 0)
	for index, mask := range [2]uint32{1, 2} {
		history := offload.NewNativeCounterHistory(started, time.Second)
		counters := [2]offload.NativeCounter{{Error: syscall.ENOTSUP}, {Error: syscall.ENOTSUP}}
		counters[index] = nativeCounter(0, 0, 1)
		if got := history.Observe(mask, counters, nil, started); got != offload.NativeCounterIdle {
			t.Fatalf("unrequested direction invalidated mask %d: %d", mask, got)
		}
		resumedAt := started.Add(2 * time.Second)
		history.Restart(resumedAt, time.Second)
		counters[index] = offload.NativeCounter{Error: syscall.EAGAIN}
		if got := history.Observe(mask, counters, syscall.EAGAIN, resumedAt); got != offload.NativeCounterPending || history.Evictable(resumedAt, time.Second) {
			t.Fatalf("resumed hardware inherited its earlier counter baseline: %d", got)
		}
		if history.LastActive() != started {
			t.Fatal("resumption refreshed activity without a complete counter observation")
		}
		if got := history.Observe(mask, counters, syscall.EAGAIN, resumedAt.Add(time.Second)); got != offload.NativeCounterReadinessExpired {
			t.Fatalf("resumption did not receive a bounded readiness period: %d", got)
		}
	}
}

func nativeCounter(packets, bytes, observed uint64) offload.NativeCounter {
	return offload.NativeCounter{Packets: packets, Bytes: bytes, ObservedMonotonicNS: observed, PacketsValid: true, BytesValid: true}
}
