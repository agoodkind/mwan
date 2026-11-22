> Read the [repository context](../../README.md) before using copied commands or historical plans.

# BGP route control and graceful restart

The WAN gateway uses GoBGP to peer with OPNsense FRR over iBGP. It advertises default routes for IPv4 and IPv6 when forwarding is ready. OPNsense applies its route policy to select the preferred gateway.

## Control route announcements

The gateway API exposes `GetBGPStatus`, `AnnounceRoutes`, and `WithdrawRoutes`. `GetBGPStatus` reports peer state and which address families the gateway advertises. `WithdrawRoutes` removes the default routes. When forwarding readiness is configured, a manual withdrawal stays in effect until `AnnounceRoutes` passes the readiness checks and resumes announcements.

## Use BGP Graceful Restart

BGP Graceful Restart (RFC 4724) lets OPNsense FRR retain the gateway's routes while `mwan-agent` restarts. The gateway advertises a restart time of 30 seconds by default. FRR removes the retained routes if the BGP session does not return before that interval expires.

The `[bgp.graceful_restart]` settings enable Graceful Restart by default. During shutdown, `mwan-agent` leaves its default routes announced when Graceful Restart is enabled. When Graceful Restart is disabled, it withdraws those routes before stopping GoBGP.

OPNsense FRR must also enable Graceful Restart. In the OPNsense GUI, open **Routing → BGP → General** and enable Graceful Restart. Reload the BGP configuration with:

```bash
configctl quagga reload bgp
```

Check the FRR configuration over SSH:

```bash
sudo vtysh -c 'show running-config router bgp' | grep 'bgp graceful-restart'
```
