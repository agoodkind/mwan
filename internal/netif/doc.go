// Package netif provides Go-native implementations of low-level Linux
// network state operations: address and route reconciliation, policy-rule
// reconciliation, kernel event monitoring, DHCPv4 client, Router
// Solicitation, sysctl access, and ICMPv6 connectivity probes.
//
// It is intentionally a leaf package: it does not depend on any other mwan
// internal package, and exposes a small surface (Monitor, DHCPClient,
// RAClient, V6Probe, ProcSysctlRunner, plus the package-level reconcile
// helpers) that the higher-level ifmgr daemon and its modules consume.
// Splitting netif out of internal/oob lets multiple roles (oob,
// failover, future NPT/policy-routing modules) share the same
// primitives without duplicating glue.
//
// All operations are in-process via:
//
//   - github.com/vishvananda/netlink for addr/route/rule and event subscribe
//   - github.com/mdlayher/ndp for Router Solicitation/Advertisement
//   - github.com/insomniacslk/dhcp/dhcpv4/nclient4 for DHCPv4 (DORA + renew)
//   - golang.org/x/net/icmp + ipv6 for connectivity probes
//   - [os.ReadFile] and [os.WriteFile] on /proc/sys for sysctl
//
// No /sbin/ip, rdisc6, dhclient, sysctl, or ping shellouts. Capabilities
// required at the systemd unit level: CAP_NET_ADMIN (route/rule/addr
// writes), CAP_NET_RAW (ICMPv6 sockets for NDP and probes). For sysctl
// writes additionally need ReadWritePaths=/proc/sys/net/... or
// ProtectKernelTunables=false.
//
// All implementations log every boundary at [slog.LevelDebug] with op name,
// parameters, duration, and error.
//
// Package is Linux-only.
package netif
