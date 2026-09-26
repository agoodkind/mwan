> Read the [repository context](../../../README.md) before using copied commands or historical plans.

# WireGuard endpoint roaming across multiple WANs

WireGuard learns a peer's endpoint from authenticated traffic, so a peer behind
several public WAN addresses can move a tunnel onto whichever address sent the
latest valid packet.

## How endpoint roaming works

An endpoint is the remote IP address and UDP port that WireGuard uses for a
peer. A configured hostname or address provides the initial endpoint. After
traffic starts, WireGuard replaces that value with the source address and port
of the latest correctly authenticated packet.

The [`wg(8)` configuration reference](https://git.zx2c4.com/wireguard-tools/tree/src/man/wg.8)
defines this update as standard endpoint behavior. Authentication makes roaming
safe when a peer changes address, but it does not show whether the new address
uses the intended WAN.

## Why several WANs create path drift

A multi-WAN gateway normally keeps one connection symmetric. It records the
provider that received a new inbound connection and sends replies through that
provider.

WireGuard control traffic can open another connection later. A rekey, a peer
address change, or lost connection-tracking state can make the next packet new
to the gateway. If that packet leaves through another provider, the remote peer
authenticates it and learns the other provider's public address. The tunnel has
then drifted even though each individual connection followed a valid route.

Path drift becomes a failure when the two providers do not offer equivalent
reachability. One path can drop packets, retain stale firewall state, or lose
the peer's new prefix while the other path remains healthy. WireGuard continues
to use the last authenticated source until later traffic teaches it another
endpoint.

## The suburban and OPNsense case

The suburban hypervisor reaches the OPNsense WireGuard listener through
`home.goodkind.io:51820`. Public DNS can return either the AT&T address or the
Webpass address, and the MWAN gateway forwards both addresses to OPNsense.

When suburban starts the exchange, the path remains consistent:

1. Suburban resolves the name to the AT&T address and sends from its current
   Comcast address.
2. MWAN records AT&T as the ingress provider and forwards the packet to
   OPNsense.
3. OPNsense replies through the recorded provider.
4. Suburban authenticates the reply and keeps the AT&T address as the OPNsense
   endpoint.

Without the WireGuard pin, three later events can create a separate path:

- An OPNsense rekey could start a new connection that generic steering assigned
  to Webpass. Suburban then learned the Webpass address from the authenticated
  packet.
- A Comcast prefix change gave suburban a new source address. OPNsense learned
  that address, while stale provider or firewall state could still affect the
  reverse path.
- An MWAN restart or connection-tracking flush removed the provider choice for
  existing traffic. The next new connection could select a different provider
  and move suburban's learned endpoint again.

The current MWAN policy pins OPNsense-initiated WireGuard control traffic to the
configured provider. Its fixed port match survives suburban address changes,
and generic steering does not replace the selected provider. Inbound traffic
still keeps the provider on which it arrived, so DNS failover retains symmetric
replies.

## Limits of broader fixes

Stock `wg` and `wg-quick` do not expose a setting that disables authenticated
endpoint roaming. Disabling that behavior would also remove the address-change
handling that roaming provides.

A static suburban endpoint would prevent DNS from selecting another provider.
It would therefore replace multi-WAN failover with a single fixed path.

Forcing every inbound packet onto one provider would discard the ingress choice
and break reply symmetry for other services that use the same public addresses.

## Current visibility

The interface manager on suburban reads `wg0`, records each peer's learned
endpoint and handshake age, and alerts when a previously active handshake
becomes stale. It does not poll OPNsense or correlate the two directions, so an
operator must inspect both peers to reconstruct the path.

To diagnose and reconcile a stalled tunnel, follow
[Recover a WireGuard roaming failure](recovery.md).
