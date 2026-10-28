//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/offload"
)

const counterSampleMinimum uint8 = 2

func (module *Module) observeConnection(ctx context.Context, event offload.ConntrackEvent) {
	identity := flowIdentity(event.Flow)
	if event.Destroyed {
		delete(module.resumeCandidates, identity)
	}
	entry, installed := module.flows[identity]
	if !installed {
		return
	}
	if event.Destroyed || !sameConnectionPolicy(entry.flow, event.Flow) {
		if err := module.retire(ctx, identity, "connection state changed"); err != nil {
			_ = module.fail(ctx, err)
		}
		delete(module.resumeCandidates, identity)
	}
}

func (module *Module) retire(ctx context.Context, identity connectionIdentity, reason string) error {
	entry, installed := module.flows[identity]
	if !installed {
		return nil
	}
	if err := module.removeRules(ctx, identity, entry, reason); err != nil {
		return err
	}
	delete(module.flows, identity)
	module.rememberCandidate(identity, entry)
	module.stats.Retired++
	module.Log.DebugContext(ctx, "native rule retired", "connection_id", identity.id, "wan", entry.wan, "reason", reason)
	return nil
}

func (module *Module) removeRules(ctx context.Context, identity connectionIdentity, entry *admittedFlow, reason string) error {
	requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
	defer cancel()
	result, err := module.runtime.Remove(requestContext, entry.result.Handle)
	if result.Handle == entry.result.Handle {
		entry.result = result
		entry.uncertain = result.CleanupFailedDirections != 0
	} else if err != nil {
		entry.uncertain = true
	}
	if err != nil {
		module.Log.WarnContext(ctx, "native connection rule removal failed", "connection_id", identity.id, "reason", reason, "err", err)
		return fmt.Errorf("remove native connection %d for %s: %w", identity.id, reason, err)
	}
	if result.InstalledDirections != 0 || result.CleanupFailedDirections != 0 || result.State != offload.NativePairDraining {
		return fmt.Errorf("native removal did not confirm retained restoration for connection %d", identity.id)
	}
	return nil
}

func (module *Module) retireAll(ctx context.Context, reason string) error {
	var failures error
	for identity := range module.flows {
		failures = errors.Join(failures, module.retire(ctx, identity, reason))
	}
	return failures
}

func (module *Module) sweep(ctx context.Context) {
	generation := module.forwarding.Generation()
	for identity, entry := range module.flows {
		if ctx.Err() != nil {
			return
		}
		if entry.result.InstalledDirections == 0 {
			continue
		}
		if !module.sweepEntry(ctx, generation, identity, entry) {
			return
		}
	}
}

func (module *Module) sweepEntry(ctx context.Context, generation uint64, identity connectionIdentity, entry *admittedFlow) bool {
	requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
	mapping, _, observed, _, decision, mappingErr := module.candidate(requestContext, entry.observation)
	if mappingErr != nil || decision != EligibilityReady || flowIdentity(observed) != identity || mapping != entry.spec {
		cancel()
		if err := module.retire(ctx, identity, "forwarding evidence changed"); err != nil {
			_ = module.fail(ctx, err)
		}
		return true
	}
	current, currentErr := offload.FreshConntrackFlow(requestContext, entry.flow)
	stats, counterErr := module.runtime.Query(requestContext, entry.result.Handle)
	if stats.Result.Handle == entry.result.Handle {
		entry.result = stats.Result
	}
	cancel()
	if module.forwarding.Generation() != generation || module.forwarding.Err() != nil || module.events.Err() != nil {
		module.checkEvidence(ctx)
		return false
	}
	if currentErr != nil || !sameConnectionPolicy(entry.flow, current) {
		if err := module.retire(ctx, identity, "kernel connection identity became unavailable"); err != nil {
			_ = module.fail(ctx, err)
		}
		return true
	}
	sampledAt := clock.Real{}.Now()
	active, valid := observeCounters(entry, stats, counterErr, sampledAt)
	if !valid {
		module.stats.CounterFailures++
		if err := module.retire(ctx, identity, "hardware counter evidence became unavailable"); err != nil {
			_ = module.fail(ctx, err)
		}
		return true
	}
	if active {
		refreshContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
		err := module.updater.Refresh(refreshContext, current, entry.timeout)
		cancel()
		if err != nil {
			module.Log.WarnContext(ctx, "native conntrack timeout refresh failed", "connection_id", identity.id, "err", err)
			if retireErr := module.retire(ctx, identity, "kernel timeout refresh failed"); retireErr != nil {
				_ = module.fail(ctx, errors.Join(err, retireErr))
			}
			return true
		}
	}
	if sampledAt.Sub(entry.lastActive) >= module.cfg.Settings.IdleTimeout || !active && time.Duration(current.Timeout)*time.Second <= module.cfg.Settings.CounterInterval {
		if err := module.retire(ctx, identity, "valid counters confirmed inactivity"); err != nil {
			_ = module.fail(ctx, err)
		} else {
			module.stats.Expired++
		}
	}
	return true
}

func observeCounters(entry *admittedFlow, stats offload.NativePairStats, queryError error, now time.Time) (bool, bool) {
	if queryError != nil || stats.Result.State != offload.NativePairInstalled || stats.Result.InstalledDirections != entry.spec.RequestedDirections {
		return false, false
	}
	active := false
	for index, mask := range [2]uint32{1, 2} {
		if entry.spec.RequestedDirections&mask == 0 {
			continue
		}
		counter := stats.Directions[index]
		previous := entry.previous[index]
		if counter.Error != nil || !counter.PacketsValid || !counter.BytesValid || counter.ObservedMonotonicNS == 0 {
			return false, false
		}
		if entry.samples != 0 {
			if counter.ObservedMonotonicNS <= previous.ObservedMonotonicNS || counter.Packets < previous.Packets || counter.Bytes < previous.Bytes {
				return false, false
			}
			active = active || counter.Packets > previous.Packets || counter.Bytes > previous.Bytes
		}
		entry.previous[index] = counter
	}
	if entry.samples == 0 || active {
		entry.lastActive = now
	}
	entry.lastSample = now
	if entry.samples < counterSampleMinimum {
		entry.samples++
	}
	return active, true
}

func (module *Module) evictLRU(ctx context.Context) bool {
	now := clock.Real{}.Now()
	if module.stats.Failure != nil {
		return false
	}
	if len(module.flows) < int(module.cfg.Settings.PairCapacity) {
		return true
	}
	var oldest connectionIdentity
	var lastActive time.Time
	for identity, entry := range module.flows {
		if entry.result.InstalledDirections == 0 || entry.samples < counterSampleMinimum || now.Sub(entry.lastSample) > module.cfg.Settings.CounterInterval {
			continue
		}
		if lastActive.IsZero() || entry.lastActive.Before(lastActive) {
			oldest, lastActive = identity, entry.lastActive
		}
	}
	if lastActive.IsZero() {
		return false
	}
	if err := module.retire(ctx, oldest, "least recently active connection"); err != nil {
		if ctx.Err() != nil {
			return false
		}
		_ = module.fail(ctx, err)
		return false
	}
	module.stats.Evicted++
	return true
}

func (module *Module) logCounters(ctx context.Context) {
	stats, err := module.runtime.Stats()
	if err != nil {
		module.handleFailure(ctx, fmt.Errorf("native packet worker failed: %w", err))
		return
	}
	control := module.Stats()
	module.Log.DebugContext(ctx, "native admission counters",
		"active_pairs", control.Active, "tracked_pairs", control.Tracked, "installed", control.Installed,
		"resume_candidates", control.ResumeCandidates,
		"cleanup_uncertain_pairs", control.CleanupUncertain, "paused", control.Paused,
		"suspended", control.Suspended, "resumed", control.Resumed, "observation_frames_skipped", control.ObservationSkipped,
		"retired", control.Retired, "evicted", control.Evicted,
		"expired", control.Expired, "fallback", control.Fallback,
		"counter_failures", control.CounterFailures,
		"received", stats.Received, "forwarded", stats.Forwarded,
		"returned_to_kernel", stats.ReturnedToKernel, "pending", stats.Pending,
		"quarantined", stats.Quarantined)
}
