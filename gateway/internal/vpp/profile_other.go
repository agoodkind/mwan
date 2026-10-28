//go:build !linux

package vpp

import (
	"context"
	"fmt"
	"time"
)

// Profile requires Linux native perf events and returns an explicit platform error.
func Profile(_ context.Context, _ Endpoints, _ string, duration time.Duration) (ProfileResult, error) {
	reason := "VPP native CPU profiling requires Linux perf_event_open"
	var counts SampleCounts
	return ProfileResult{Available: false, Reason: reason, Duration: duration, SamplePeriod: 0, EmptyPollingAvailable: false, Counts: counts, Threads: nil}, fmt.Errorf("%s", reason)
}

func ProfileWithOptions(ctx context.Context, endpoints Endpoints, executable string, duration time.Duration, _ ProfileOptions) (ProfileResult, error) {
	return Profile(ctx, endpoints, executable, duration)
}
