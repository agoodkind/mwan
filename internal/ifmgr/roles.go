package ifmgr

import (
	"fmt"
	"log/slog"
)

// roleModules maps each known role to the ordered list of module names
// that should run for it. Order matters: modules execute in the listed
// sequence on each Reconcile pass. Choose order so that a module's
// preconditions are likely to be in place by the time it runs (e.g.
// policy_rules before oobv6, since oobv6 may already need egress to
// solicit RA from the OOB tunnel side).
//
// Adding a new role: append the entry here and ensure the named modules
// are registered (their package is imported by main.go).
//
// Per-host opt-in modules: cloudflared_tap, host_ipv6_policy, and wg
// self-disable via ifmgr.ErrModuleDisabled when their TOML section is
// absent from the rendered config. That lets the single oob role list
// every OOB-style module while each host enables only the subset it
// actually configures.
var roleModules = map[string][]string{
	// oob is the unified OOB role for vault and the suburban hypervisor.
	// Module list is the union of every OOB-style module. Per-host
	// selection is driven by which [ifmgr.modules.X] sections render in
	// /etc/mwan/config.toml; modules whose section is absent self-disable
	// at Init time via ifmgr.ErrModuleDisabled and are dropped from the
	// daemon's dispatch list.
	//
	// Vault enables policy_rules, oobv6, oobv4, ra_lost, cloudflared_tap.
	// Suburban enables policy_rules, host_ipv6_policy, wg (local-exec).
	"oob": {
		"policy_rules",
		"host_ipv6_policy",
		"oobv6",
		"oobv4",
		"ra_lost",
		// cloudflared_tap is a log forwarder. It tails a configured
		// systemd unit (cloudflared-oob) and re-emits each entry through
		// the daemon's slog logger so cloudflared events flow through
		// the same JSON log file and email pipeline as everything else.
		// Pure log forwarder: no kernel state.
		"cloudflared_tap",
		// wg polls a WireGuard interface and alerts when any peer's
		// handshake age crosses configured thresholds. Two modes: remote
		// SSH (vault polls OPNsense) or local exec (suburban polls its
		// own wg0). Read-only observer; no kernel state.
		"wg",
	},
	// host is the hypervisor role for a Proxmox host with no OOB tunnel.
	// It omits oobv6, oobv4 and ra_lost, which require an OOB interface
	// and address and fail Init without them. Suburban runs this.
	"host": {
		"policy_rules",
		"host_ipv6_policy",
		"wg",
	},
	// failover is the iface-monitor role for prod LXC 116 and testbed
	// LXC 100. mainv4 is included so that when dhcp_v4 is enabled for
	// the iface, the daemon's DHCP client also drives kernel addr and
	// the main-table default route. If dhcp_v4 is disabled, mainv4's
	// Init returns inert and the role falls back to the no-mainv4
	// modules (this is intentional; prod LXC 116 today runs without
	// mainv4).
	"failover": {
		"slaac_health",
		"bridge_probe",
		"connectivity_probe",
		"ra_lost",
		"mainv4",
	},
	// wan owns the MWAN VM policy-routing inventory. It runs as a
	// separate instance from any OOB role.
	"wan": {
		// health writes the state consumed by later WAN-role modules.
		"health",
		"wan.routes",
		// steering assigns each new connection to a provider of the active
		// tier. It runs after wan.routes so the policy rules its marks select
		// are installed before any mark is set. Self-disables when the network
		// configuration lists no providers.
		"steering",
		// npt programs the ip6 nat NPT chains from the live DHCPv6-PD.
		// Self-disables when the network configuration lists no providers.
		"npt",
	},
}

// modulesForRole returns the module name list for the named role, or an
// error if the role is not known.
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

// ModulesForRole is the exported accessor for modulesForRole, used by the
// daemon entrypoint (package main) to build only the active role's module
// configs. Returns an error for an unknown role.
func ModulesForRole(role string) ([]string, error) {
	return modulesForRole(role)
}

// KnownRoles returns the sorted list of role names this binary supports.
// Used by the CLI --help and config validation.
func KnownRoles() []string {
	out := make([]string, 0, len(roleModules))
	for k := range roleModules {
		out = append(out, k)
	}
	return out
}
