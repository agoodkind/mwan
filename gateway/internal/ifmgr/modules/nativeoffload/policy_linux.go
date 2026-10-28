//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"fmt"
	"syscall"

	"goodkind.io/mwan/internal/firewall"
)

type policyCertificate struct {
	rules      uint64
	configured uint64
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
	if module.policyCached(before, configured) {
		return module.policyCertificate.failure
	}
	desired, err := firewall.Compile(module.cfg.Firewall)
	if err != nil {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy compilation failed", "err", err)
		return fmt.Errorf("compile expected native forwarding policy: %w", err)
	}
	inspection := firewall.InspectNative(requestContext, desired)
	after, err := module.rules.Barrier(requestContext)
	if err != nil {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy checkpoint failed", "phase", "after inspection", "err", err)
		return fmt.Errorf("checkpoint nftables after native policy inspection: %w", err)
	}
	if before != after || configured != module.policyGeneration.Load() {
		module.policyCertificate = nil
		module.Log.WarnContext(ctx, "native policy changed during certification", "rules_before", before, "rules_after", after, "configured", configured)
		return fmt.Errorf("nftables policy changed during native certification: %w", syscall.ESTALE)
	}
	if inspection != nil {
		module.Log.WarnContext(ctx, "native forwarding policy certification failed", "err", inspection)
		inspection = fmt.Errorf("inspect native forwarding policy: %w", inspection)
	}
	module.policyCertificate = &policyCertificate{rules: after, configured: configured, failure: inspection}
	return inspection
}

func (module *Module) policyCached(rules, configured uint64) bool {
	return module.policyCertificate != nil && module.policyCertificate.rules == rules && module.policyCertificate.configured == configured
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
		if err := module.suspendAll(ctx, "nftables policy became uncertified"); err != nil {
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
