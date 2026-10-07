# MWAN documentation

Maintain MWAN specifications, plans, runbooks, goals, and execution records in
this repository. The documents copied from Configs preserve their original
requirements, commands, historical paths, and recorded results.

## Repository context

Run `configsctl`, Ansible deployment, inventory, and OpenTofu commands from
the [Configs checkout](https://github.com/agoodkind/configs). Copying these
documents does not relocate deployment code. References to those files use
explicit Configs links.

The copied documents use Configs revision
`70894173c07328a91de1d6236b0d02cbf81a0186`. The MWAN-341 specification, plan,
goal, and ledger were imported earlier from `e3143788`. Historical plans
retain the paths and commands used at the time, including paths that predate
the separate MWAN repository. Check current implementation and deployment
state before executing a historical plan or recovery procedure.

Configs removes the migrated implementation plans and retains the other
copied documents as migration snapshots.
Maintain subsequent MWAN documentation changes here. General Configs
documentation and deployment code remain in Configs.

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
firewall projections from network.json.

## Implementation plans

Follow the [MWAN-341 plan](plans/2026-09-26-mwan-341-firewall.md) and its
adjacent goal and ledger for firewall ownership. Follow the
[interface migration coordination plan](plans/2026-09-26-link-ownership.md)
for MWAN-305's work plans, agent assignments, and execution order. Earlier work
includes the
[translation plan](plans/2026-09-22-mwan-340-typed-translation.md),
[provider-model completion plan](plans/2026-09-21-mwan-506-completion.md),
[live validation plan](plans/2026-09-21-mwan-506-live-validation.md), and
[application installation plan](plans/2026-09-16-application-file-seam.md).

Historical design and implementation records cover
[deployment checks](superpowers/deploygate/plan.md),
[router reboot coordination](superpowers/routerguards/plan.md), and
[management-channel recovery](superpowers/wedgeproof/plan.md).

## Operation and testbed

Use the [deployment guide](ops/README.md) for release installation and
verification. The [testbed guide](ops/mwan/testbed.md) covers simulated
providers. The [packet diagnosis guide](ops/mwan/dataplane.md) and
[failover guide](ops/mwan/failover.md) cover traffic checks.

Read the [OPNsense operations guide](ops/opnsense/operations.md) for downstream
router requirements and the [VM configuration reference](../testbed/mwan-vm/qm-config.md)
for the MWAN testbed guest.
