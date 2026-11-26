# MWAN documentation

This guide links the gateway specifications, implementation plans, and
operator guides.

## Repository context

The [Configs repository](https://github.com/agoodkind/configs) contains MWAN
deployment code, inventory, and OpenTofu configuration. Run deployment
commands from that checkout.

## Architecture and configuration

Read the [gateway architecture](mwan.md) for routing and provider behavior.
The [configuration specification](superpowers/wanconfig/spec.md) defines the
provider model and links its detailed requirements. The
[firewall specification](firewall.md) defines daemon ownership of firewall
policy. The [interface ownership specification](interfaces.md) defines the
MWAN-305 migration from networkd and its shared contracts with MWAN-507.
The [downstream router design](superpowers/multirouter/spec.md)
defines the BGP relationship with OPNsense.
The [public address specification](downstream.md) defines untranslated
IPv6 use by downstream BGP peers alongside synthetic-address clients.
The [network planning contract](superpowers/networkplan/spec.md) requires native
plan-time validation and stable configured interface, route, policy-rule, and
firewall projections from `network.json`.

The [packet-processing backend specification](backends.md) defines runtime
selection of Linux or VPP in the same binary and optional hardware steering.

## Implementation plans

Follow the [MWAN-341 plan](plans/2026-09-26-mwan-341-firewall.md) and its
adjacent goal and ledger for firewall ownership. Follow the
[interface migration coordination plan](plans/2026-09-26-link-ownership.md)
for MWAN-305's work plans, agent assignments, and execution order. Earlier work
includes the
[translation plan](plans/2026-09-22-mwan-340-typed-translation.md) and
[application installation plan](plans/2026-09-16-application-file-seam.md).

## Operation and testbed

Use the [operations guide](ops/README.md) for release installation and
verification. The [packet diagnosis guide](ops/mwan/dataplane.md) covers
traffic checks.

Read the [OPNsense operations guide](ops/opnsense/operations.md) for downstream
router requirements.
