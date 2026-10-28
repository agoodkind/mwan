//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/ifmgr/modules/steering"
)

type policyCertificate struct {
	rules      uint64
	configured uint64
	forwarding uint64
	npt        uint64
	failure    error
}

func (module *Module) inspectPolicy(ctx context.Context) error {
	return module.certifyPolicy(ctx, false)
}

func (module *Module) certifyPolicy(ctx context.Context, checkpoint bool) error {
	if err := module.rules.Err(); err != nil {
		module.Log.WarnContext(ctx, "native policy observations unavailable", "err", err)
		return fmt.Errorf("observe nftables policy for native certification: %w", err)
	}
	configured := module.policyGeneration.Load()
	forwarding := module.forwarding.Generation()
	_, npt := module.Env.LiveState.ForwardingGenerations()
	if !checkpoint && module.policyCached(module.rules.Generation(), configured) {
		return module.policyCertificate.failure
	}
	requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
	defer cancel()
	before, err := module.rules.Barrier(requestContext)
	if err != nil {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy checkpoint failed", "phase", "before inspection", "err", err)
		return fmt.Errorf("checkpoint nftables before native policy inspection: %w", err)
	}
	if !checkpoint && module.policyCached(before, configured) {
		return module.policyCertificate.failure
	}
	desired, err := module.expectedPacketPolicy()
	if err != nil {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy compilation failed", "err", err)
		return fmt.Errorf("compile expected native forwarding policy: %w", err)
	}
	inspection := errors.Join(firewall.InspectNative(requestContext, desired), module.inspectTrafficPolicy(requestContext))
	after, err := module.rules.Barrier(requestContext)
	if err != nil {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy checkpoint failed", "phase", "after inspection", "err", err)
		return fmt.Errorf("checkpoint nftables after native policy inspection: %w", err)
	}
	_, currentNPT := module.Env.LiveState.ForwardingGenerations()
	if before != after || configured != module.policyGeneration.Load() || forwarding != module.forwarding.Generation() || npt != currentNPT {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy changed during certification", "rules_before", before, "rules_after", after, "configured", configured)
		return fmt.Errorf("packet policy changed during native certification: %w", syscall.ESTALE)
	}
	if inspection != nil {
		module.Log.WarnContext(ctx, "native forwarding policy certification failed", "err", inspection)
		inspection = fmt.Errorf("inspect native forwarding policy: %w", inspection)
	}
	module.policyCertificate = &policyCertificate{rules: after, configured: configured, forwarding: forwarding, npt: npt, failure: inspection}
	return inspection
}

func (module *Module) expectedPacketPolicy() (firewall.Ruleset, error) {
	desired, err := firewall.Compile(module.cfg.Firewall)
	if err != nil || module.cfg.Steering == nil {
		return desired, err
	}
	projection, err := steering.CompilePolicy(*module.cfg.Steering, module.Env.LiveState.Snapshot())
	if err != nil {
		return desired, err
	}
	desired.Chains = append(desired.Chains, projection.Chains...)
	return desired, nil
}

func (module *Module) policyCached(rules, configured uint64) bool {
	_, npt := module.Env.LiveState.ForwardingGenerations()
	certificate := module.policyCertificate
	return certificate != nil && certificate.rules == rules && certificate.configured == configured && certificate.forwarding == module.forwarding.Generation() && certificate.npt == npt
}

func (module *Module) inspectTrafficPolicy(ctx context.Context) error {
	indices := make([]int, 0, len(module.links))
	for index := range module.links {
		indices = append(indices, index)
	}
	if module.Env.InspectNativePacketPolicy != nil {
		return module.Env.InspectNativePacketPolicy(ctx, indices)
	}
	if len(module.Env.LiveState.Snapshot().NPTPolicies) != 0 {
		return fmt.Errorf("native admission requires the applied NPT policy inspector")
	}
	return bpf.InspectNativePolicy(ctx, indices)
}

func (module *Module) refreshPolicy(ctx context.Context) bool {
	if module.stats.Failure != nil {
		return false
	}
	failure := module.certifyPolicy(ctx, true)
	blocked := failure != nil
	if blocked == module.policyBlocked {
		return !blocked
	}
	module.policyBlocked = blocked
	generation := module.policyGeneration.Add(1)
	if module.policyCertificate != nil {
		module.policyCertificate.configured = generation
	}
	if blocked {
		if err := module.suspendAll(ctx, "packet policy became uncertified"); err != nil {
			_ = module.fail(ctx, err)
			return false
		}
		module.Log.DebugContext(ctx, "native policy became ineligible", "err", failure)
	} else {
		module.Log.DebugContext(ctx, "native policy certification restored")
		module.requestResumeReconcile()
	}
	return !blocked
}

func (module *Module) observePolicyChange(ctx context.Context) {
	if module.stats.Failure != nil {
		return
	}
	if module.runtime == nil {
		module.policyCertificate = nil
	} else if !module.policyCached(module.rules.Generation(), module.policyGeneration.Load()) {
		module.refreshPolicy(ctx)
	}
	module.checkEvidence(ctx)
}
