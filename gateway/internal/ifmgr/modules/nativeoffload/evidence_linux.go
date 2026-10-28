//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"

	"goodkind.io/mwan/internal/offload"
)

type evidenceEpoch struct {
	policy     uint64
	routing    uint64
	npt        uint64
	forwarding uint64
}

func (module *Module) invalidateEvidence(ctx context.Context) {
	if err := module.suspendAll(ctx, "forwarding evidence changed"); err != nil {
		_ = module.fail(ctx, err)
		return
	}
	module.evidenceGeneration = module.forwarding.Generation()
	module.policyGeneration.Add(1)
	module.requestResumeReconcile()
}

type evidenceCheck struct {
	epoch evidenceEpoch
	guard offload.NativeEvidenceGuard
}

func (module *Module) currentEvidenceEpoch() evidenceEpoch {
	routing, npt := module.Env.LiveState.ForwardingGenerations()
	return evidenceEpoch{
		policy: module.policyGeneration.Load(), routing: routing,
		npt: npt, forwarding: module.forwarding.Generation(),
	}
}

func (module *Module) evidenceFor(epoch evidenceEpoch) *evidenceCheck {
	if module.cachedEvidence != nil && module.cachedEvidence.epoch == epoch {
		return module.cachedEvidence
	}
	check := &evidenceCheck{epoch: epoch, guard: offload.NativeEvidenceGuard{Check: nil}}
	check.guard.Check = func() error {
		if err := errors.Join(module.events.Err(), module.forwarding.Err()); err != nil {
			slog.Warn("native kernel evidence became unavailable", "err", err)
			return fmt.Errorf("read native kernel evidence: %w", err)
		}
		if module.currentEvidenceEpoch() != epoch {
			slog.Warn("native forwarding evidence changed", "expected", epoch)
			return fmt.Errorf("native forwarding evidence changed: %w", syscall.ESTALE)
		}
		return nil
	}
	module.cachedEvidence = check
	return check
}
