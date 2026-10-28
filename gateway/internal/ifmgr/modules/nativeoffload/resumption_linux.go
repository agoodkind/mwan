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

type resumption struct {
	keys     []connectionIdentity
	index    int
	evidence *evidenceCheck
}

func (module *Module) suspendAll(ctx context.Context, reason string) error {
	module.admissionPaused.Store(true)
	module.resumePass = nil
	module.cachedEvidence = nil
	var failures error
	for identity, entry := range module.flows {
		wasInstalled := entry.result.InstalledDirections != 0
		if err := module.removeRules(ctx, identity, entry, reason); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if wasInstalled {
			module.stats.Suspended++
			module.logRuleCounters(ctx, slog.LevelDebug, "native rule suspended", entry, reason, nil)
		}
	}
	return failures
}

// OnFamilyReadiness permits resumption only after the daemon publishes complete family readiness.
func (module *Module) OnFamilyReadiness(ctx context.Context, _ *slog.Logger) {
	module.Lock()
	defer module.Unlock()
	if module.runtime == nil || module.stats.Failure != nil || ctx.Err() != nil {
		return
	}
	if !module.refreshPolicy(ctx) {
		return
	}
	if !module.checkEvidence(ctx) {
		return
	}
	pass := &resumption{
		keys:     make([]connectionIdentity, 0, len(module.flows)),
		index:    0,
		evidence: module.evidenceFor(module.currentEvidenceEpoch()),
	}
	for identity, entry := range module.flows {
		if entry.result.State == offload.NativePairDraining && entry.result.InstalledDirections == 0 {
			pass.keys = append(pass.keys, identity)
		}
	}
	module.resumePass = pass
	if len(pass.keys) == 0 {
		module.finishResumption(ctx, pass)
		return
	}
	module.admissionPaused.Store(true)
	module.scheduleResumption()
}

func (module *Module) scheduleResumption() {
	select {
	case module.resumeWake <- struct{}{}:
	default:
	}
}

func (module *Module) requestResumeReconcile() {
	if module.Env.RequestReconcile != nil {
		module.Env.RequestReconcile("native rules require fresh family readiness")
	}
}

func (module *Module) resumeEvidence(ctx context.Context, pass *resumption) bool {
	if err := errors.Join(module.events.Err(), module.forwarding.Err(), module.rules.Err()); err != nil {
		_ = module.fail(ctx, err)
		return false
	}
	if pass.evidence.guard.Check() != nil {
		if err := module.suspendAll(ctx, "resumption evidence changed"); err != nil {
			_ = module.fail(ctx, err)
		}
		module.requestResumeReconcile()
		return false
	}
	return true
}

func (module *Module) finishResumption(ctx context.Context, pass *resumption) {
	if !module.resumeEvidence(ctx, pass) {
		return
	}
	module.evidenceGeneration = pass.evidence.epoch.forwarding
	module.resumePass = nil
	module.admissionPaused.Store(false)
}

func (module *Module) resumeOne(ctx context.Context) {
	pass := module.resumePass
	if pass == nil || module.stats.Failure != nil || ctx.Err() != nil {
		return
	}
	if !module.resumeEvidence(ctx, pass) {
		return
	}
	identity := pass.keys[pass.index]
	pass.index++
	entry, exists := module.flows[identity]
	if exists {
		requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
		module.resumeEntry(ctx, requestContext, identity, entry, pass.evidence)
		cancel()
	}
	if module.resumePass != pass || ctx.Err() != nil {
		return
	}
	if pass.index == len(pass.keys) {
		module.finishResumption(ctx, pass)
		return
	}
	module.scheduleResumption()
}

func (module *Module) resumeEntry(ctx, requestContext context.Context, identity connectionIdentity, entry *admittedFlow, evidence *evidenceCheck) {
	counterStarted := clock.Real{}.Now()
	spec, guards, current, _, decision, err := module.candidate(requestContext, entry.observation)
	if err != nil || decision != EligibilityReady || !sameConnectionPolicy(entry.flow, current) || !sameNativeMapping(entry.spec, spec) || guards != entry.guards {
		module.stats.Fallback++
		if ctx.Err() == nil {
			if removeErr := module.retire(ctx, identity, "suspended connection mapping changed"); removeErr != nil {
				_ = module.fail(ctx, removeErr)
			}
		}
		return
	}
	if evidence.guard.Check() != nil {
		return
	}
	result, err := module.runtime.ResumeObservedWithEvidence(requestContext, current, entry.result.Handle, spec, guards, &evidence.guard)
	if result.Handle == entry.result.Handle {
		entry.result = result
		entry.uncertain = result.CleanupFailedDirections != 0
	} else if err != nil {
		entry.uncertain = true
	}
	if err != nil || result.State != offload.NativePairInstalled || result.InstalledDirections != spec.RequestedDirections {
		if err == nil {
			err = fmt.Errorf("native resumption did not confirm every direction for connection %d", identity.id)
		}
		module.Log.WarnContext(ctx, "native resumption failed", "connection_id", identity.id, "err", err)
		module.stats.Fallback++
		if removeErr := module.retire(ctx, identity, "native resumption did not complete"); removeErr != nil {
			_ = module.fail(ctx, errors.Join(err, removeErr))
		} else if errors.Is(err, syscall.ESTALE) {
			delete(module.resumeCandidates, identity)
		}
		return
	}
	entry.flow, entry.spec, entry.guards = current, spec, guards
	module.startCounters(entry, counterStarted)
	module.stats.Resumed++
	module.logAdmittedFlow(ctx, "native rule resumed", entry)
}

func (module *Module) rememberCandidate(identity connectionIdentity, entry *admittedFlow) {
	if len(module.resumeCandidates) >= int(module.cfg.Settings.FlowCapacity) {
		var oldest connectionIdentity
		var observed time.Time
		for key, candidate := range module.resumeCandidates {
			if observed.IsZero() || candidate.counters.LastActive().Before(observed) {
				oldest, observed = key, candidate.counters.LastActive()
			}
		}
		delete(module.resumeCandidates, oldest)
	}
	module.resumeCandidates[identity] = entry
}

func sameNativeMapping(previous, current offload.NativePairSpec) bool {
	if current.PolicyGeneration < previous.PolicyGeneration {
		return false
	}
	current.PolicyGeneration = previous.PolicyGeneration
	return current == previous
}
