// Package netif provides Linux networking operations for the interface manager.
//
// The package reconciles addresses, routes, and policy rules. It also monitors
// kernel events and provides DHCPv4, Router Solicitation, sysctl, and ICMPv6
// operations.
//
// The systemd unit needs CAP_NET_ADMIN for address, route, and policy-rule
// changes. It needs CAP_NET_RAW for ICMPv6 probes and Neighbor Discovery.
// Sysctl writes require access to the relevant /proc/sys paths or disabled
// kernel-tunable protection.
package netif
