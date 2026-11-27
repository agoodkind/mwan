package ifmgr

// Role modules run in reconciliation order. When a WAN gateway selects OVS,
// the role retains host services and inserts OVS immediately after health.
//
//	health verdict -> OVS selects OpenFlow flows and group buckets

import (
	"fmt"
	"log/slog"
)

const wanRoutesModule = "wan.routes"

// The daemon reconciles modules in the order listed for each role.
var roleModules = map[string][]string{
	"wan": {
		"links",
		"autoconfiguration",
		"addresses",
		"resolver",
		// The WAN route module uses the results of the health and NPT checks.
		"health",
		"firewall",
		"npt",
		wanRoutesModule,
		"bgp_sessions",
		// The WAN route module installs policy rules before steering sets packet marks.
		"steering",
	},
}

func modulesForRole(role string) ([]string, error) {
	logger := slog.Default().With("component", "ifmgr")
	names, ok := roleModules[role]
	if !ok {
		valid := make([]string, 0, len(roleModules))
		for k := range roleModules {
			valid = append(valid, k)
		}

		logger.Warn("ifmgr: unknown role requested", "role", role, "valid", valid)

		return nil, fmt.Errorf("unknown ifmgr role %q (valid: %v)", role, valid)
	}

	return names, nil
}

// ModulesForOVS returns modules that configure OVS and host services.
//
// OVS reads connection health before installing conditional rules and groups.
func ModulesForOVS(role string) ([]string, error) {
	names, err := modulesForRole(role)
	if err != nil {
		return nil, err
	}

	var active []string

	for _, name := range names {
		if name == "npt" || name == "steering" || name == wanRoutesModule {
			continue
		}

		active = append(active, name)
		if name == "health" {
			active = append(active, "ovs")
		}
	}

	return active, nil
}

// ModulesForRole returns module identifiers in reconciliation order.
func ModulesForRole(role string) ([]string, error) {
	return modulesForRole(role)
}

func (cfg DaemonConfig) moduleNames() ([]string, error) {
	if _, selected := cfg.ModuleConfigs["ovs"]; selected {
		return ModulesForOVS(cfg.Role)
	}

	return modulesForRole(cfg.Role)
}

// KnownRoles returns each registered ifmgr role.
func KnownRoles() []string {
	out := make([]string, 0, len(roleModules))
	for k := range roleModules {
		out = append(out, k)
	}

	return out
}
