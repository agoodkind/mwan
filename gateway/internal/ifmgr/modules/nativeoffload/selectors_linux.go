//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/offload"
)

func (module *Module) observeTrackedSelectors(ctx, requestContext context.Context, identity connectionIdentity, observation packetObservation, guards [2]offload.NativePacketGuard, evidence *evidenceCheck) bool {
	entry, exists := module.flows[identity]
	if !exists {
		return false
	}
	if guards != entry.guards || entry.selectorsPending {
		module.reobserveEntry(ctx, requestContext, identity, entry, observation, evidence)
	}
	return true
}

func (module *Module) selectorCandidate(ctx context.Context, entry *admittedFlow, observation packetObservation, evidence *evidenceCheck) *admittedFlow {
	now := clock.Real{}.Now()
	if age := now.Sub(observation.at); age < 0 || age > module.cfg.Settings.ObservationMaxAge || !observation.Eligible {
		return nil
	}
	spec, guards, current, wan, decision, err := module.candidate(ctx, observation)
	if err != nil || decision != EligibilityReady || !sameConnectionPolicy(entry.flow, current) || !sameNativeMapping(entry.spec, spec) || wan.Key() != entry.wan {
		return nil
	}
	if evidence.guard.Check() != nil || (clock.Real{}).Now().Sub(observation.at) > module.cfg.Settings.ObservationMaxAge {
		return nil
	}
	return newAdmittedFlow(current, spec, guards, wan, observation, now)
}

func (module *Module) reobserveEntry(ctx, requestContext context.Context, identity connectionIdentity, entry *admittedFlow, observation packetObservation, evidence *evidenceCheck) {
	now := clock.Real{}.Now()
	if !entry.selectorAttemptAt.IsZero() && now.Sub(entry.selectorAttemptAt) < module.cfg.Settings.CounterInterval {
		return
	}
	entry.selectorAttemptAt = now
	candidate := module.selectorCandidate(requestContext, entry, observation, evidence)
	if candidate == nil {
		module.stats.Fallback++
		return
	}
	wasInstalled := entry.result.InstalledDirections != 0
	if err := module.removeRules(ctx, identity, entry, "packet selectors changed"); err != nil {
		_ = module.fail(ctx, err)
		return
	}
	if wasInstalled {
		module.stats.Suspended++
		module.logRuleCounters(ctx, slog.LevelDebug, "native rule suspended", entry, "packet selectors changed", nil)
	}
	// Post-install evidence failure can leave different guards in DRAINING.
	// Only another fresh observation may confirm selectors for that pair.
	entry.selectorsPending = true
	candidate = module.selectorCandidate(requestContext, entry, observation, evidence)
	if candidate == nil {
		module.stats.Fallback++
		return
	}
	started := clock.Real{}.Now()
	result, err := module.runtime.ReobserveWithEvidence(requestContext, candidate.flow, entry.result.Handle, candidate.spec, candidate.guards, &evidence.guard)
	confirmed := retainSelectorResult(entry, result, candidate.spec.RequestedDirections)
	if err != nil || !confirmed {
		if err == nil {
			err = fmt.Errorf("native selector replacement did not confirm connection %d and every requested direction", identity.id)
		}
		module.rejectSelectorReplacement(ctx, identity, entry, err)
		return
	}
	module.completeSelectorReplacement(ctx, identity, entry, candidate, started)
	if evidence.guard.Check() != nil {
		module.invalidateEvidence(ctx)
	}
}

func retainSelectorResult(entry *admittedFlow, result offload.NativePairResult, requested uint32) bool {
	if result.Handle != entry.result.Handle {
		entry.uncertain = true
		return false
	}
	entry.result = result
	entry.uncertain = result.CleanupFailedDirections != 0
	return result.State == offload.NativePairInstalled && result.RequestedDirections == requested && result.InstalledDirections == requested && result.RestorationDirections == requested && result.CleanupFailedDirections == 0
}

func (module *Module) completeSelectorReplacement(ctx context.Context, identity connectionIdentity, entry, candidate *admittedFlow, started time.Time) {
	previous := entry.guards
	entry.flow, entry.spec, entry.guards, entry.observation = candidate.flow, candidate.spec, candidate.guards, candidate.observation
	entry.selectorsPending = false
	module.startCounters(entry, started)
	module.stats.Resumed++
	module.Log.DebugContext(ctx, "native packet selectors refreshed", "connection_id", identity.id, "wan", entry.wan,
		"handle", entry.result.Handle, "policy_generation", entry.spec.PolicyGeneration, "result", entry.result,
		"previous_guards", previous, "current_guards", entry.guards,
		"observed_traffic_class", entry.observation.TrafficClass, "observed_flow_label", entry.observation.FlowLabel)
}

func (module *Module) rejectSelectorReplacement(ctx context.Context, identity connectionIdentity, entry *admittedFlow, failure error) {
	module.stats.Fallback++
	module.Log.WarnContext(ctx, "native packet selector replacement did not complete", "connection_id", identity.id,
		"handle", entry.result.Handle, "result", entry.result, "err", failure)
	if entry.result.CleanupFailedDirections != 0 || entry.result.State == offload.NativePairFaulted {
		_ = module.fail(ctx, failure)
		return
	}
	var nativeFailure *offload.NativeError
	if errors.As(failure, &nativeFailure) && nativeFailure.ObjectKind == offload.NativeObjectPair && errors.Is(nativeFailure, syscall.ESTALE) {
		if err := module.retire(ctx, identity, "packet selector handle became stale"); err != nil {
			_ = module.fail(ctx, errors.Join(failure, err))
		} else {
			delete(module.resumeCandidates, identity)
		}
		return
	}
	if err := module.removeRules(ctx, identity, entry, "packet selector replacement did not complete"); err != nil {
		_ = module.fail(ctx, errors.Join(failure, err))
	}
}
