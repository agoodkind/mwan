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
	observation := observeCounters(entry, stats, counterErr, sampledAt)
	if observation == offload.NativeCounterPending && time.Duration(current.Timeout)*time.Second > module.cfg.Settings.CounterInterval {
		module.stats.CounterPendingQueries++
		return true
	}
	if observation == offload.NativeCounterInvalid || observation == offload.NativeCounterPending || observation == offload.NativeCounterReadinessExpired {
		module.stats.CounterFailures++
		reason := "hardware counter evidence became unavailable"
		if observation == offload.NativeCounterReadinessExpired {
			reason = "initial hardware counter readiness deadline elapsed"
		}
		if observation == offload.NativeCounterPending {
			reason = "kernel timeout cannot wait for initial hardware counters"
		}
		module.Log.WarnContext(ctx, "native counter evidence rejected", "connection_id", identity.id, "wan", entry.wan,
			"reason", reason, "err", counterErr, "result", stats.Result,
			"original_counter", stats.Directions[0], "reply_counter", stats.Directions[1])
		if err := module.retire(ctx, identity, reason); err != nil {
			_ = module.fail(ctx, err)
		}
		return true
	}
	active := observation == offload.NativeCounterActive
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
	if sampledAt.Sub(entry.counters.LastActive()) >= module.cfg.Settings.IdleTimeout || !active && time.Duration(current.Timeout)*time.Second <= module.cfg.Settings.CounterInterval {
		if err := module.retire(ctx, identity, "valid counters confirmed inactivity"); err != nil {
			_ = module.fail(ctx, err)
		} else {
			module.stats.Expired++
		}
	}
	return true
}

func (module *Module) startCounters(entry *admittedFlow, started time.Time) {
	// Leave one sweep interval before the observed kernel timeout expires.
	readiness := min(module.cfg.Settings.IdleTimeout, time.Duration(entry.flow.Timeout)*time.Second-module.cfg.Settings.CounterInterval)
	entry.counters.Restart(started, readiness)
}

func observeCounters(entry *admittedFlow, stats offload.NativePairStats, queryError error, now time.Time) offload.NativeCounterObservation {
	if stats.Result.Handle != entry.result.Handle || stats.Result.State != offload.NativePairInstalled ||
		stats.Result.RequestedDirections != entry.spec.RequestedDirections || stats.Result.InstalledDirections != entry.spec.RequestedDirections ||
		stats.Result.RestorationDirections != entry.spec.RequestedDirections || stats.Result.CleanupFailedDirections != 0 {
		return offload.NativeCounterInvalid
	}
	return entry.counters.Observe(entry.spec.RequestedDirections, stats.Directions, queryError, now)
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
		if entry.result.InstalledDirections == 0 || !entry.counters.Evictable(now, module.cfg.Settings.CounterInterval) {
			continue
		}
		if lastActive.IsZero() || entry.counters.LastActive().Before(lastActive) {
			oldest, lastActive = identity, entry.counters.LastActive()
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
		"counter_failures", control.CounterFailures, "counter_pending_queries", control.CounterPendingQueries,
		"received", stats.Received, "forwarded", stats.Forwarded,
		"returned_to_kernel", stats.ReturnedToKernel, "pending", stats.Pending,
		"quarantined", stats.Quarantined)
}
