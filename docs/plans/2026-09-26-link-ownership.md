# Migrate interface ownership from networkd to MWAN

This proposal refines MWAN-305's interface migration for discussion. Its
recommendations are not approved implementation decisions. Build on the
existing typed configuration, then transfer one complete connection at a
time. Retire networkd after every interface it manages has a replacement.

## Establish the starting point

The inspected baseline is MWAN commit `03dd43a`, dated September 26, 2026.
MWAN-491 renders provider networkd units from the network configuration.
Networkd still manages those interfaces and their leases. MWAN-341 must
establish protective firewall rules before this migration changes startup.

The implementation already includes netlink operations, a DHCPv4 client,
router solicitation, provider routing, translation, and operational status.
Those components need integration and some corrections before they replace
networkd. In particular, the DHCPv4 client marks a lease expired after one
failed renewal, and the delegated-prefix interface returns a prefix without
lease deadlines. Existing primitives do not prove a complete WAN client.

Tack currently lists MWAN-397 through MWAN-401 and MWAN-227 as Todo.
MWAN-208 and MWAN-379, the cited discovery and repository prerequisites,
are Done. Recheck their dependencies before implementation. Replace old
TOML and template instructions in the tickets after this design is agreed.

## Define responsibility for each object

Keep the existing interface identity and per-family configuration. Separate
configured values, acquired values, desired kernel state, and observed
kernel state. Reuse the existing types where they already represent a fact.
Move shared link data out of the renderer package when necessary; do not
create a second configuration model with matching fields.

Assign each address, route, and rule one writer. A route's identity includes
its address family, table, destination, and relevant route attributes.
Separate lease validity from forwarding health: an unsuccessful traffic
probe does not revoke an unexpired lease.

| Object | Proposed responsibility after migration |
| --- | --- |
| Interface identity, MAC, MTU, VLANs, and bridge membership | The link module applies configured values. |
| Configured and acquired interface addresses | The address reconciliation code installs and removes the addresses it owns. |
| DHCP leases and delegated prefixes | The protocol clients negotiate assignments and publish their validity and deadlines. |
| IPv6 router advertisements and automatic addresses | An explicitly selected kernel or userspace implementation manages them. |
| Configured and acquired main-table routes | The link and address modules apply their assigned routes. |
| Provider tables and policy rules | The existing WAN routing module applies routing policy. |
| Firewall, translation, and steering | The existing modules retain their responsibilities. |
| Future BGP routes and tunnel interfaces | MWAN-507 uses the shared interface and routing contracts, with an explicit writer for its routes. |

The current renderer emits a provider-table default as well as a main-table
default for a configured gateway. The WAN routing module also reconciles
provider-table defaults. Remove that overlap through a verified transfer of
responsibility. Do not remove a route producer before its replacement works.

Represent externally routed addresses separately from addresses that require
local assignment and ARP replies. A static translation mapping must not
automatically assign every external address to the interface. Preserve the
current mapping behavior while making the address requirement explicit.

## Choose the migration boundaries

| Choice | Recommendation | Cost or alternative |
| --- | --- | --- |
| Transfer a whole connection or one protocol at a time | Transfer a complete connection, including its configured families and lease clients. | More code must be ready first. Protocol-by-protocol transfer provides earlier live testing but creates more temporary ownership combinations. |
| Acquire new leases or import networkd lease state | Preserve configured client identities and acquire leases through the protocol at the first cutover. | The connection can be interrupted or renumbered. Importing leases may reduce disruption but requires validated lease state and deadlines. Matching identities alone does not guarantee the same assignment. |
| Resume MWAN leases or acquire them on every restart | Persist MWAN's own lease metadata and use the protocol's restart behavior. | Persistence requires correct expiration handling. Restarting acquisition every time is simpler but makes ordinary daemon restarts more disruptive. |
| Use kernel IPv6 autoconfiguration or implement it in Go | Evaluate the kernel for router advertisements and SLAAC, the automatic configuration of IPv6 addresses. | Kernel behavior must satisfy the existing configuration and reporting requirements. A Go implementation gives more control but adds protocol and lifetime management. |
| Transfer connections individually or replace the whole gateway at once | Transfer connections individually. | Temporary coexistence requires explicit ownership checks. A simultaneous gateway-wide replacement has fewer intermediate states but interrupts more connections at once. |

Linux supports router advertisements and automatic address configuration
through per-interface controls, including operation while forwarding.
Verify the installed kernel and required behavior before selecting that
implementation. See the [kernel IPv6 controls](https://docs.kernel.org/networking/ip-sysctl.html).
Owning the configuration does not require reimplementing kernel protocols.

## Build the replacement before the first cutover

### 1. Define ownership and operational reporting

1. Inventory provider, VLAN parent, internal, bridge, and management interfaces.
   Record their actual writers, including udev naming rules, netplan or
   cloud-init output, networkd units, and existing MWAN modules where present.
2. Add a per-interface migration choice to the existing model. Keep each
   interface assigned to its current owner until explicitly migrated. Treat
   comparison as an observation mode, not a third writer.
3. Publish the selected owner, acquisition state, installed addresses and
   routes, per-family readiness, and the last failed operation with its time.
   Extend the existing management surface with dated transition history.
4. Reject settings that the selected implementation cannot honor before
   changing the interface. Convert required free-form networkd settings into
   typed behavior before transferring that interface.
5. Define removal as well as creation. Delete only objects assigned to MWAN;
   account for kernel-generated connected routes and automatic addresses.

Verify configuration acceptance and rejection through the real loader and
served model. Verify that comparison performs no link, address, route, or
lease changes. This work prepares MWAN-397 and MWAN-398.

### 2. Implement link and static address management

1. Implement device matching, configured naming, MAC and MTU application,
   VLAN creation, bridge membership, and static address reconciliation.
2. Transfer on-link mapped address installation from the WAN routing module
   to the address owner as part of the interface handover. Preserve existing
   translation and source-routing behavior.
3. Apply main-table routes under the agreed ownership map. Keep policy tables
   under the WAN routing module.
4. On restart, inspect existing objects and leave matching state installed.
   Do not cycle an interface merely because the daemon restarted.
5. Treat shared parents and bridge members as explicit dependencies. Do not
   rename, change a MAC, or disable a parent independently of its active users.
6. Start each connection independently. An absent provider or unavailable
   address family must not prevent the other connections from starting.

Verify these operations through the real daemon in Linux network namespaces,
using connected peers and actual packet exchange. Include configuration
removal and process restart. This implements the static portions of
MWAN-397 and MWAN-398 without a live ownership transfer.

### 3. Complete address acquisition and lease management

1. Reuse the DHCPv4 library and correct the client lifecycle. Respect server
   renewal and rebinding times, lease expiration, rejection, and configured
   client identity. Rebinding means requesting renewal from another server
   after the original server stops responding.
2. Implement MWAN-227 using the DHCPv6 library. Preserve the configured DUID
   and IAID, the client and assignment identifiers. Track renewal, rebinding,
   preferred lifetime, and valid lifetime for each delegated prefix.
3. Cover IPv6 router discovery, automatic addresses, and DHCPv6 address
   assignment where configured. Prefix delegation alone does not replace
   every IPv6 operation that networkd performs.
4. Publish lease deadlines separately from the existing prefix lookup used
   by translation. Keep that lookup compatible where possible. Stop reporting
   an expired delegation as usable, and update dependent routes and translation
   when the active delegation changes.
5. Persist MWAN lease state for restart handling if that recommendation is
   selected. Apply protocol-specific recovery; do not infer lease validity
   solely from an address remaining in the kernel.

DHCPv4 permits renewal attempts while a lease remains valid, followed by
rebinding and eventual expiration. A timeout is not a server rejection.
Use [RFC 2131, section 4.4.5](https://www.rfc-editor.org/rfc/rfc2131.html#section-4.4.5)
when correcting the existing early-expiration behavior.

Verify through actual DHCP servers and router advertisements on isolated
networks. Use short real leases to exercise renewal, temporary server loss,
expiration, changed assignments, and daemon restart. Assert installed state
and packet delivery. Retain the existing OOB behavior when changing shared
clients. This completes the acquisition requirements of MWAN-398 and MWAN-227.

## Transfer connections and retire networkd

### 4. Transfer the first complete connection

1. Use the Webpass testbed connection proposed by the existing tickets. Prepare
   a merged release with all protocols that connection requires. Its static
   IPv4 exercises mapped addresses; its IPv6 requires completed acquisition.
   Validate DHCPv4 independently with the existing dynamic-provider simulator.
2. Verify another connection can serve downstream traffic. Exclude the
   selected connection from new traffic before changing ownership. Measure
   interruption; do not promise established connections will survive.
3. Stop networkd's management and lease clients for that connection. Verify
   that udev rules and other configuration cannot reclaim it. Keep networkd
   running for the connections it still owns.
4. Start MWAN ownership and acquire current assignments. Require observed
   addresses, routes, translation, and forwarding before restoring eligibility.
5. Prove the reverse transfer on the testbed: stop MWAN's clients and writes,
   restore networkd ownership, and verify forwarding. Never run both sets of
   clients to accelerate recovery.
6. Reboot the testbed and repeat the downstream traffic checks. Run acceptance
   before promoting the phase to production.

Validate the handover against the installed systemd version. Removing one
file is not proof that networkd has stopped managing the interface. This
phase replaces MWAN-397's proposed simultaneous writers with observation
followed by an exclusive handover.

### 5. Transfer the remaining interfaces

1. Repeat the complete-connection transfer for the dynamic providers, then
   the VLAN and authentication-dependent connection. Preserve the AT&T
   authentication service in Configs as MWAN-305 requires. Verify its actual
   readiness signal before making lease acquisition depend on it.
2. Transfer the internal bridge and return routes after provider behavior is
   established. Prove downstream forwarding and internal BGP separately.
3. Transfer management interfaces last, with verified hypervisor console
   access and a tested reverse transfer. Management access alone does not
   prove downstream forwarding.
4. Keep networkd enabled until all interfaces it manages have migrated.

This expands MWAN-399 to include the management boundary required by
MWAN-400's planned gateway-wide shutdown of networkd.

### 6. Remove networkd dependencies and prove the final system

1. Complete MWAN-400 after every required replacement passes acceptance.
   Remove obsolete renderers, generated units, deploy tasks, networkctl and
   networkd readers, service dependencies, and unused configuration choices.
   Inventory current files; do not execute historical deletion lists blindly.
2. Disable networkd only after proving that it manages no required interface.
   Remove obsolete udev naming files separately from stopping networkd.
3. Run MWAN-401 on the testbed from a cold boot, after a daemon restart, and
   after provider failure and recovery. Prove IPv4, IPv6, load balancing,
   inbound mappings, internal BGP, management, and downstream traffic.
4. Promote the verified merged release and matching configuration to
   production. Record measured interruption and recovery, then update ticket
   states from the evidence.

Use focused PRs for each coherent change. Complete implementation before
starting live cutovers. Each deployable phase requires merged commits,
testbed deployment, acceptance, and then production deployment. Run deploys
through Configs with its reconnecting Ansible workflow and existing checks.

Use public boundaries and real dependencies for regression tests. Run
`make docker-make TARGETS="check test"` for code changes on macOS, plus the
required privileged Linux tests. Observe downstream guests without an OOB
bypass during deployment, reboot, balancing, and failover exercises.

## Preserve the later routing work

MWAN-507 adds physical and tunnel dependencies, configured routes, and BGP
sessions to the same interface and routing model. It must not require a
delegated prefix for a routed IPv6 prefix. Keep ordinary IPv4 independent of
external IPv6 BGP. A second circuit uses a separate interface identity even
when its provider and ASN match another circuit.

Define those interfaces before implementing dependent components. External
BGP does not need to wait for global networkd removal, provided every
affected interface and route has one owner. Keep provider values and tunnel
protocols configurable. New-circuit production tests remain subject to the
existing deployment schedule.

## Use the existing implementation boundaries

| Work | Existing source to extend |
| --- | --- |
| Load and publish ownership and acquisition settings | Extend [the network loader](../../internal/networkjson/networkjson.go), [the served model](../../internal/wanconfig/tree.go), and the active [schema](../../internal/yangpub/schema/goodkind-mwan-steering@2026-09-26.yang). |
| Separate link intent from unit rendering | Refactor [the link specification](../../internal/networkd/spec.go) and [the renderer](../../internal/networkd/networkd.go) without duplicating configuration types. |
| Implement kernel changes and acquisition | Reuse [address reconciliation](../../internal/netif/state.go), [DHCPv4](../../internal/netif/dhcp.go), and [router solicitation](../../internal/netif/ra.go). |
| Replace delegated-prefix observation | Extend [the prefix source](../../internal/pd/source.go) while preserving its consumers. |
| Transfer mapped addresses and route ownership | Update [the WAN routing module](../../internal/ifmgr/modules/wanroutes/wanroutes.go). |
| Apply startup ordering | Integrate with [daemon startup](../../cmd/mwan/ifmgr.go) after MWAN-341 completes. |

Keep accepted configuration behavior in the existing [model specification](../superpowers/wanconfig/model.md)
when this proposal becomes implementation work. This migration plan should
retain sequencing and verification rather than duplicate that specification.
