package ifmgr

import (
	"fmt"
	"log/slog"
)

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
		"wan.routes",
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

// ModulesForRole returns module identifiers in reconciliation order.
func ModulesForRole(role string) ([]string, error) {
	return modulesForRole(role)
}

// KnownRoles returns each registered ifmgr role.
func KnownRoles() []string {
	out := make([]string, 0, len(roleModules))
	for k := range roleModules {
		out = append(out, k)
	}

	return out
}
