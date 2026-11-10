// Package vpp reconciles the router's optional VPP process.
package vpp

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/vpp"
)

const moduleName = "vpp"

// Config joins process resources with one validated router network policy.
type Config struct {
	Settings *vpp.Settings
	Policy   *vpp.Policy
}

// ModuleConfigName binds this configuration to the router's registered module.
func (Config) ModuleConfigName() string { return moduleName }

// Module retries VPP reconciliation without changing WAN provider selection.
type Module struct {
	ifmgr.BaseModule
	controller *vpp.Controller
	policy     *vpp.Policy
}

// New rejects an incomplete resource and policy join before the daemon writes state.
func New(settings ifmgr.ModuleConfig) (result ifmgr.Module, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("configure VPP interface manager module failed", "error", failure)
		}
	}()
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), controller: nil, policy: nil}
	if settings == nil {
		return module, nil
	}
	configuration, ok := settings.(Config)
	if !ok {
		return nil, fmt.Errorf("vpp: invalid config type %T", settings)
	}
	if configuration.Settings == nil {
		return module, nil
	}
	if configuration.Policy == nil {
		return nil, fmt.Errorf("vpp: router requires a goodkind-mwan-vpp:vpp network policy")
	}
	if err := configuration.Policy.Validate(); err != nil {
		return nil, fmt.Errorf("validate router VPP policy: %w", err)
	}
	controller, err := vpp.NewController(*configuration.Settings)
	if err != nil {
		return nil, fmt.Errorf("configure router VPP controller: %w", err)
	}
	if err := controller.Validate(*configuration.Policy); err != nil {
		return nil, fmt.Errorf("validate router VPP resources: %w", err)
	}
	module.controller = controller
	module.policy = configuration.Policy
	return module, nil
}

// Init disables VPP when the shared TOML file omits its module section.
func (module *Module) Init(_ context.Context, environment *ifmgr.Env) error {
	module.InitBase(environment, "module", moduleName)
	if module.controller == nil {
		return ifmgr.ErrModuleDisabled
	}
	return nil
}

// Reconcile records VPP failures in the shared operational snapshot.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	err := module.controller.Reconcile(ctx, log, *module.policy)
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetOwnedIntendedRuleset(moduleName, "Configure router VPP through its local binary API.", err)
	}
	if err != nil {
		log.ErrorContext(ctx, "VPP reconciliation failed", "error", err)
		return fmt.Errorf("reconcile optional router VPP: %w", err)
	}
	return nil
}

func init() { ifmgr.Register(moduleName, New) }
