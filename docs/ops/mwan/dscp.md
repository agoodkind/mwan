> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Pinning a device or app to one WAN via DSCP

Status: the mwan half ships as the per-provider forced DSCP value, with AT&T on
CS1. The OPNsense scrub half is not deployed. Captured 2026-06-15.

## Why this exists

Hulu shows a "location changed" notification when its flows load-balance across
WANs, because each WAN egresses a different ISP IP (AT&T `104.57.226.x`, Webpass
`136.25.91.x`, Monkeybrains `158.247.70.x`) and Hulu reads the account as hopping
networks. The first fix was destination-IP pinning via the `att_pinned_v4/v6`
nftables sets. A live capture on 2026-06-15
showed that approach loses to Hulu's CDN: Hulu sprays video segments across
Akamai, Fastly, and CloudFront `/24`s (and v6 `/64`s) faster than the 6h refresh
can track, so new rotated IPs keep leaking to the load-balanced WAN.

The durable fix is to stop classifying by destination and instead carry a
classification signal from OPNsense (which sees the device and the DNS) across
the routed hop to mwan, then pin on that signal. DSCP is the one piece of
metadata that survives a plain routed hop.

## What mwan can and cannot see (verified live)

Checked against `/proc/net/nf_conntrack` and `conntrack -L` on the mwan VM:

- IPv4: mwan sees `src=10.250.250.2` for every LAN flow. OPNsense source-NATs all
  IPv4 LAN clients to its `10.250.250.2` edge before handing traffic to mwan
  (consistent with the masquerade rule matching only `ip saddr 10.250.250.0/29`).
  So mwan cannot tell v4 devices apart by source, and a v4 source pin at mwan
  would pin all LAN v4 traffic.
- IPv6: mwan sees the real client `/128` (e.g. `3d06:bad:b01:2:9121:f8:be06:8f4a`)
  because the internal `3d06:bad:b01::/60` is routed end to end with no NAT. A v6
  source pin at mwan works, though SLAAC privacy addresses rotate, so pin a
  device `/64` (its own VLAN) rather than a single `/128`.

This split is why OPNsense-side tagging is required for v4: OPNsense is the only
node that sees the real `10.250.2.x` client.

## How to set DSCP on OPNsense (verified in source)

OPNsense can only SET DSCP through legacy scrub/normalization rules:

- The filter generator emits `set-tos <value>` from the scrub rules in the
  router config, and the GUI exposes the value as the "TOS / DSCP" field on a
  Normalization rule. Verified in the OPNsense core source on 25.7 and live on
  26.1.9.
- Allowed values are the named classes (`lowdelay`, `ef`, ...), `af11`-`af43`,
  `cs0`-`cs7`, and raw hex `0x00`-`0xFF`.
- The MVC firewall filter-rule `tos` field and the Traffic Shaper `dscp` field
  are MATCH-only, not set. There is no ipfw `setdscp`. So scrub `set-tos` is
  the only set primitive.

Scrub runs before OPNsense's outbound NAT, so a scrub rule can match the real
v4/v6 client source, stamp a codepoint, and the bits ride through the NAT to
mwan.

## Automation reach (verified in source)

- The `opnsense-go` REST client cannot do this: its firewall filter model has
  no `tos` or `dscp` field, and it implements no traffic-shaper or scrub
  resource.
- The configs stack does not use that REST client. It drives OPNsense through
  the `mwan-opnsense` gRPC daemon over virtio-serial, which mutates the router
  config via XPath set, get, and delete calls plus command execution. Scrub
  rules sit under `filter/scrub/rule` in that config, so the daemon can
  program the tag with an XPath write and a filter reload. It currently only
  touches BGP, gateways, and upgrades, so this would be a new use of an
  existing mechanism.

## Design

Chosen codepoint: CS1 (DSCP 8, ToS byte `0x20`), a scavenger-class value nothing
else here uses. nft accepts `ip dscp cs1`; OPNsense scrub stores `set-tos` as
`cs1`.

### OPNsense half (scrub set-tos)

One Normalization rule per family (or one rule with an alias holding both
addresses): match the streaming device source, set TOS/DSCP to `cs1`. Config
shape of the scrub rule:

```
interface = lan
proto     = any
src       = <streaming-device-ip-or-alias>
dst       = any
set-tos   = cs1
descr     = Tag streaming device for AT&T pin
```

### MWAN half (forced DSCP)

The AT&T provider's `forced-dscp` value in the typed network configuration is
8. The gateway daemon installs IPv4 and IPv6 marking rules in the internal
interface's `inet mangle` prerouting chain. New CS1-tagged connections receive
AT&T's firewall mark before load balancing. Connection tracking stores that
mark for later packets. The daemon maintains the rules, including after a
missing table or chain is recreated.

Do not edit the retired gateway nftables template or add a durable rule with
`nft`. Change the provider's `forced-dscp` configuration and deploy it to
change this policy. No other provider may use the same DSCP value.

## Test OPNsense tagging

This procedure verifies that OPNsense CS1 tagging causes MWAN to assign
`mark=1` to new connections.

1. Identify the streaming device LAN addresses. On the mwan VM during playback,
   the device's v6 appears as the conntrack source for Hulu flows (for example
   `3d06:bad:b01:2:9121:f8:be06:8f4a`); its v4 is its DHCP lease on OPNsense.
2. OPNsense, optional alias: Firewall > Aliases > Add, type Host(s), name
   `streaming_device`, add the device v4 and v6. Apply.
3. OPNsense scrub rule: Firewall > Settings > Normalization > Add.
   - Interface: the LAN the device is on.
   - Protocol: any.
   - Source: `streaming_device` (or the device IP). Leave invert off.
   - Destination: any.
   - TOS / DSCP: `cs1`.
   - Description: `Tag streaming device for AT&T pin`.
   - Save, then Apply changes.
4. Inspect the installed MWAN policy without changing kernel state:
   ```bash
   mwan deploy-gate inspect-firewall /etc/mwan/network.json /usr/local/share/wanconfig/yang
   ```
   The command fails when the configured rules differ from the current kernel.
5. Restart Hulu on the device to create new connections. Check `conntrack -L`
   on MWAN for `mark=1` across the changing CDN destinations. Confirm that
   untagged traffic still load-balances.

Keep the successful scrub rule in the router configuration.

## Verification

- Confirm the tag survives to mwan: on the mwan VM, `conntrack -L` Hulu flows
  show `mark=1`; before the change they split between `mark=1` and `mark=2`.
- Confirm the device egresses AT&T: its public IP as seen by an IP-echo service
  while streaming should be in the AT&T `104.57.226.x` range.
- Confirm no collateral: only the tagged device should change WAN; other LAN
  traffic still load-balances.

## Caveats

- It is the router's on-disk config, not the REST API. Automating it means the
  `mwan-opnsense` XPath path, which has not yet been used for firewall rules;
  verify the daemon can insert a rule subtree, not just scalar values, before
  relying on it.
- Verify nothing else re-normalizes ToS to 0 between OPNsense and mwan (a quick
  live capture settles this).
- Pick a codepoint not otherwise in use. CS1 (`0x20`) is the assumed free value
  here; confirm no Traffic Shaper or other consumer keys on it.
- This pins the whole device, which is the intended "blunt" behavior. For
  app-only granularity on a shared device, OPNsense would still need a per-app
  classifier, which inherits the same shared-CDN problem.
