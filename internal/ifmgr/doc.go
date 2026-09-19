// Package ifmgr implements the mwan interface-manager daemon.
//
// One daemon binary, one subcommand (`mwan ifmgr`), serves multiple
// deployment roles via a small composition pattern:
//
//   - The daemon owns the reconcile loop, the kernel event monitor (via
//     internal/netif), the optional DHCP client, and an AlertManager.
//   - Each "module" is a small Init/Reconcile/OnEvent struct that the
//     daemon dispatches into. Modules are registered at package init time
//     under a name; the role-to-modules map in roles.go decides which set
//     to instantiate at startup.
//
// Today's roles:
//
//   - "oob": vault Proxmox host and the suburban hypervisor. Modules:
//     policy_rules (ip rules for cloudflared-uid and OOB source),
//     host_ipv6_policy (bridge RA sysctl reconciliation; opt-in via
//     [ifmgr.modules.host_ipv6_policy]), oobv6 (static OOB v6 addr,
//     RA-default sync into oob table), oobv4 (DHCP-learned default into
//     oob table), ra_lost (alert when RA stops arriving), cloudflared_tap
//     (log forwarder; opt-in via [ifmgr.modules.cloudflared_tap]), wg
//     (WireGuard peer-handshake observer; opt-in via [ifmgr.modules.wg],
//     remote-SSH mode on vault, local-exec mode on the suburban
//     hypervisor against its own wg0 endpoint).
//
//   - "failover": prod LXC 116, testbed LXC 100. Modules: slaac_health
//     (detect deprecated SLAAC, send RS, fall back to disable_ipv6
//     toggle), bridge_probe (alert when no NDP/DHCP signal arrives,
//     suspecting a host-side veth dangling), ra_lost, connectivity_probe
//     (active ping of upstream and configured targets), mainv4 (inert
//     unless dhcp_v4 is enabled on the iface).
//
// Opt-in modules return ifmgr.ErrModuleDisabled from Init when their
// TOML section is absent; the daemon drops them from its dispatch list
// so a single role definition covers every host even when the host
// renders only a subset of the available modules.
//
// Future roles (not implemented in this iteration):
//
//   - "wan" wrapping NPT and per-WAN policy routing on VM 113 (slice 2).
//
// All kernel state operations go through internal/netif (Go-native via
// vishvananda/netlink, mdlayher/ndp, golang.org/x/net/icmp+ipv6, and
// /proc/sys file I/O). Zero shellouts.
//
// Boundary log discipline: every module Init/Reconcile/OnEvent logs at
// DEBUG with op name, parameters, and result. Daemon dispatch logs
// per-iteration trace IDs so a single grep reconstructs one full
// reconcile lifecycle.
//
// Package is Linux-only.
package ifmgr
