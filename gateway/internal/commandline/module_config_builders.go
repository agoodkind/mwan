package commandline

import (
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ifmgr/moduleconfig"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/health"
)

func buildIfMgrModuleConfigs(
	ifmgrCfg config.IfMgrSection,
	role string,
) (ifmgr.ModuleConfigSet, error) {
	result, err := moduleconfig.Build(ifmgrCfg, role)
	if err != nil {
		slog.Warn("Interface manager role configuration failed.", "role", role, "err", err)

		return result, fmt.Errorf("configuring ifmgr role %q: %w", role, err)
	}

	return result, nil
}

func buildWANRefs(settings config.IfMgrSection) moduleconfig.WANInputs {
	return moduleconfig.WANRefs(settings)
}

func buildHealthConfig(
	shared moduleconfig.WANInputs,
	section *config.IfMgrHealthSection,
) (health.Config, error) {
	result, err := moduleconfig.HealthConfig(shared, section)
	if err != nil {
		slog.Warn("Gateway status health configuration failed.", "err", err)

		return result, fmt.Errorf("configuring health state for gateway status: %w", err)
	}

	return result, nil
}

func buildHealthProviderConfig(
	shared moduleconfig.WANInputs,
	section *config.IfMgrHealthSection,
) (health.Config, error) {
	result, err := moduleconfig.HealthProviderConfig(shared, section)
	if err != nil {
		slog.Warn("Operator health probe configuration failed.", "err", err)

		return result, fmt.Errorf("configuring operator health probes: %w", err)
	}

	return result, nil
}

func parseAddrList(values []string, fieldName string) ([]netip.Addr, error) {
	result, err := config.ParseAddrList(values, fieldName)
	if err != nil {
		slog.Warn("Published probe target configuration failed.", "field", fieldName, "err", err)

		return result, fmt.Errorf("configuring published probe targets: %w", err)
	}

	return result, nil
}
