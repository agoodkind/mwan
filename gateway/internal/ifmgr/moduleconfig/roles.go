// Package moduleconfig builds configuration for the modules in an ifmgr role.
//
// Dataplane selection determines which modules receive settings. An OVS
// role keeps host services such as the firewall and health probes while OVS
// takes responsibility for WAN packet forwarding.
package moduleconfig

import (
	"fmt"
	"log/slog"

	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr"
)

// Build returns settings for each module selected by a supported role.
//
// Build validates the role before it creates any module settings. The returned
// set contains only the modules that the interface manager starts for that
// role.
//
// Example:
//
//	moduleConfigs, err := moduleconfig.Build(settings.IfMgr, "wan")
//	if err != nil {
//	    return err
//	}
//	_ = moduleConfigs
func Build(
	ifmgrCfg config.IfMgrSection,
	role string,
) (ifmgr.ModuleConfigSet, error) {
	logger := slog.Default().With("component", "ifmgr")
	selectModules := ifmgr.ModulesForRole
	if ifmgrCfg.OVSDataplane {
		selectModules = ifmgr.ModulesForOVS
	}

	names, err := selectModules(role)
	if err != nil {
		logger.Warn("ifmgr: ModulesForRole failed", "role", role, "err", err)

		return nil, fmt.Errorf("ModulesForRole(%q): %w", role, err)
	}

	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}

	moduleConfigs := make(ifmgr.ModuleConfigSet)
	if err := addWANRoleConfigs(moduleConfigs, want, ifmgrCfg); err != nil {
		return nil, err
	}

	return moduleConfigs, nil
}
