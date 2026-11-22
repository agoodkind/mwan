# Multiple downstream routers over BGP

A MWAN gateway can serve multiple downstream routers on the same transit
network. The agent announces a default only when configuration enables it. If
forwarding readiness is enabled, the agent also requires the corresponding
address family to be ready. Each router announces the LAN prefixes it can
serve.

## Peering and route selection

1. The agent accepts dynamic BGP neighbors within the configured IPv4 and IPv6
   transit prefixes. It also supports static peers. A downstream router sets up
   its own iBGP session and announces only the prefixes it can serve.
2. The speaker selects the best path when multiple peers announce the same
   prefix. It accepts IPv4 and IPv6 unicast paths for route installation. It
   skips peer defaults, local announcements, and learned prefixes that match or
   cover a connected network.
3. When configuration specifies a learned route interface, the agent installs
   the selected paths in the main table and configured WAN tables. It marks
   those routes with the kernel's BGP protocol number. Downstream routers can
   exchange traffic through the gateway using their learned LAN routes.

MWAN can announce an internal prefix while NPTv6 translates that prefix to the
configured external prefix. The translation uses an explicit internal and
external prefix pair. The provider delegation must supply the required prefix
length.

## Withdrawal and restart

The agent removes routes associated with a peer when that session drops. It
reapplies current best paths during reconciliation, including paths that
return with the same attributes after a peer reconnects. Stale route cleanup
waits for the startup grace period to expire.

When BGP graceful restart is active, the agent shuts down without explicitly
withdrawing its default announcements. A peer that supports graceful restart
can retain those routes during reconnection.
