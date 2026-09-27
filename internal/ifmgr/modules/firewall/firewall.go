// Package firewall maintains the gateway-owned nftables tables.
package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
)

const moduleName = "firewall"

const destinationRefreshService = "mwan-update-att-pinned-dests.service"

// Module applies and inspects the configured gateway policy on each pass.
type Module struct {
	ifmgr.BaseModule
	cfg firewall.Config
}

// Init validates the policy before the daemon starts reconciliation.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	if !m.cfg.Enabled {
		return fmt.Errorf("%w: firewall ownership is absent", ifmgr.ErrModuleDisabled)
	}
	if _, err := firewall.Compile(m.cfg); err != nil {
		return fmt.Errorf("compile firewall policy: %w", err)
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "firewall nft monitor panicked", "err", fmt.Sprint(recovered))
			}
		}()
		m.watchNFTChanges(ctx, log)
	}()
	return nil
}

// Reconcile installs the full policy and confirms the kernel accepted it.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	desired, err := firewall.Compile(m.cfg)
	if err != nil {
		m.publish(desired.String(), err)
		return fmt.Errorf("compile firewall policy: %w", err)
	}
	result, err := firewall.ApplyWithReport(ctx, desired)
	if err != nil {
		m.publish(desired.String(), err)
		log.WarnContext(ctx, "firewall policy apply failed", "err", err)
		return fmt.Errorf("apply firewall policy: %w", err)
	}
	if len(result.CreatedSets) > 0 {
		requestDestinationRefresh(ctx, log)
	}
	if _, err := firewall.Inspect(ctx, desired); err != nil {
		m.publish(desired.String(), err)
		log.WarnContext(ctx, "firewall policy inspection failed", "err", err)
		return fmt.Errorf("inspect firewall policy: %w", err)
	}
	m.publish(desired.String(), nil)
	log.DebugContext(ctx, "firewall policy reconciled")
	return nil
}

func requestDestinationRefresh(ctx context.Context, log *slog.Logger) {
	// The refresher orders itself after gateway readiness. Blocking here would
	// make initial reconciliation wait for the service that waits for this daemon.
	command := exec.CommandContext(ctx, "systemctl", "--no-block", "start", destinationRefreshService)
	output, err := command.CombinedOutput()
	if err != nil {
		log.WarnContext(ctx, "destination refresh request failed",
			"service", destinationRefreshService, "err", err, "output", strings.TrimSpace(string(output)))
	}
}

func (m *Module) publish(rules string, err error) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	m.Env.LiveState.SetOwnedIntendedRuleset(moduleName, rules, err)
}

// New constructs the gateway module from the shared typed policy.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	var policy firewall.Config
	if cfg != nil {
		var ok bool
		policy, ok = cfg.(firewall.Config)
		if !ok {
			return nil, fmt.Errorf("firewall: invalid config type %T", cfg)
		}
	}
	return &Module{BaseModule: ifmgr.NewBaseModule(moduleName), cfg: policy}, nil
}

func init() { ifmgr.Register(moduleName, New) }
