# Interface ownership

MWAN-305 replaces networkd management of gateway interfaces with explicit
ownership of links, addresses, leases, and routes. This specification defines
the intended behavior. It does not claim that the migration is implemented
or deployed. The [migration plan](plans/2026-09-26-link-ownership.md) assigns
the work to tickets and defines implementation and deployment checks.

At the inspected baseline, `03dd43a`, MWAN renders networkd units and also
changes kernel addresses and routes directly. Networkd still manages provider
leases. The endpoint of this epic is a gateway that starts and operates every
required interface without networkd.

## Configuration and identity

Extend the existing [interface and routing model](superpowers/wanconfig/model.md).
Keep one configuration representation from inventory through validation and
the served model. Reuse shared types; separate link intent from the networkd
renderer where required.

Give each connection a stable identity. Permit repeated provider names and
ASNs, the numbers that identify routing networks. Configure device matching,
MAC address, MTU, VLAN parent and tag, bridge membership, and family behavior.
MTU is the maximum packet size for an interface. Keep provider addresses,
prefix lengths, client identities, and route metrics configurable.

Separate configured intent, assignments learned from protocols, observed
kernel state, and the last apply result. A configured address is not proof
that the kernel installed it. An installed address is not proof that its
lease remains valid or that packets can be delivered.

## One writer per object

Assign an explicit writer to every managed link, address, route, and rule.
During migration, retain networkd ownership until a connection is transferred.
Observation may compare intended and observed state without changing either.
Never use two active managers as a comparison test.

| Object | Responsibility after transfer |
| --- | --- |
| Physical link settings, VLANs, and bridge membership | The link manager applies the configured settings and dependencies. |
| Static and acquired interface addresses | The address manager reconciles addresses assigned to MWAN. |
| DHCP assignments and delegated prefixes | The protocol client negotiates assignments and publishes their deadlines. |
| IPv6 automatic addresses and discovered routers | The selected kernel or userspace implementation manages their lifetimes. |
| Configured and acquired main-table routes | The interface manager applies its assigned routes. |
| Provider-table routes and policy rules | The existing WAN routing module applies routing policy. |
| Firewall, translation, and steering | The existing modules retain their responsibilities. |
| Future tunnel interfaces and BGP routes | MWAN-507 extends these contracts with explicit ownership. |

Identify routes by family, table, destination, and relevant route attributes.
Account for kernel-generated routes and automatic addresses. Reconcile only
owned objects; preserve unrelated state. Repair deletion of owned routes
promptly through the existing event mechanism under MWAN-505.

The current networkd renderer and WAN routing module both produce provider
defaults. Remove that overlap during a verified ownership transfer. Transfer
on-link mapped address installation to the address manager at the same
boundary. Preserve the routing and translation behavior established by
MWAN-340 and MWAN-333, including source rules based on the live delegation.

An external address may require local assignment and address-resolution
replies, or the ISP may route it to MWAN. Represent that distinction explicitly.
A translation mapping alone must not imply local assignment.

Reject an unsupported setting before changing the interface. Convert required
free-form networkd settings into supported typed behavior before transferring
that connection. Inventory required resolver, route, and lifetime options;
silently ignoring an option is not migration compatibility.

## Address acquisition and validity

Support configured IPv4 addresses and DHCPv4 leases. Support configured IPv6
addresses, router discovery, automatic addresses, DHCPv6 address assignments,
and prefix delegation where configured. A router advertisement supplies IPv6
router and prefix information. SLAAC uses that information to configure an
interface address. DHCPv6 can assign an interface address or delegate a prefix
for another network. These are distinct assignments.

Use one active protocol client and identity per connection and protocol.
Coordinate DHCPv6 address assignment and delegation in that lifecycle.
Preserve configured client identifiers, including DHCPv6 DUID and IAID.
DUID identifies the client; IAID distinguishes an assignment association.

Track renewal, rebinding, and expiration. Rebinding requests renewal from
another server after the original server stops responding. Track preferred
and valid lifetimes for IPv6 assignments. Stop treating an expired assignment
as usable and update dependent routes and translation when assignments change.

Keep lease validity independent of forwarding health. A failed traffic probe
does not revoke a lease. A temporary DHCPv4 renewal timeout does not expire
an otherwise valid lease. Apply the protocol's rejection and expiration rules.

Persist MWAN-owned lease metadata and deadlines for protocol-correct restart
recovery. Verify recovered assignments before using them. Preserve matching
kernel state where valid. Do not reset an interface solely because the daemon
restarted. Importing networkd leases during the initial handover is a separate
choice from recovering MWAN's own leases on ordinary restarts.

## Startup, readiness, and history

Establish MWAN-341's protective firewall before enabling forwarding or starting
network operations that require that protection. Start independent connections
independently. An absent provider or unavailable address family must not stop
the others from starting.

Represent shared parents, bridge members, authentication, and acquired
assignments as dependencies. Preserve AT&T authentication in Configs. Verify
its actual readiness signal; interface link-up alone does not establish that
authentication succeeded. Preserve shared parents while another active
interface depends on them.

Use existing family eligibility and probe policy. Keep firewall protection,
lease validity, installed routes, and forwarding readiness distinguishable in
operational state. Changing an IPv6 assignment must not disable usable IPv4.

Publish the configured owner, acquisition state, assignments and deadlines,
installed addresses and routes, readiness, and last failed operation with its
timestamp. Record detailed transition history with connection identity,
address family, previous and new state, failed dependency, plain reason, and
time. Use the existing operational surface and logging facilities. This epic
requires useful history without a separate diagnostic framework.

## Transfer and recovery

Keep each required interface assigned to a working owner throughout the
migration. During handover, exclude the affected connection from new traffic,
stop the previous owner's writes and clients, verify ownership release, then
start its replacement. Restore eligibility after the required assignments,
routes, translation, and downstream forwarding succeed.

Apply the reverse sequence for rollback with one writer at a time. Removing
a networkd file does not prove ownership release. Account for networkd's
running state, udev naming, generated configuration, and startup dependencies.
Measure interruption and renumbering. Do not promise that existing sessions
survive a transfer.

Transfer management interfaces last with verified hypervisor console access.
Retire networkd only after every required provider, internal, bridge, and
management interface has a verified replacement. Remove obsolete renderers,
lease readers, units, and service dependencies after replacement acceptance.

## Migration choices

The following recommendations remain design choices to resolve in the
assigned tickets before the affected implementation or cutover.

| Choice | Recommendation | Tradeoff and decision ticket |
| --- | --- | --- |
| Transfer complete connections or individual functions | Transfer one complete connection at a time after its components are ready. | Complete connections require more preparation. Individual functions create more temporary ownership combinations. Resolve under MWAN-519. |
| Acquire assignments or import networkd leases at first transfer | Preserve client identities and acquire assignments through the protocol. | Acquisition can interrupt or renumber a connection. Import requires verified lease metadata and deadlines. Resolve under MWAN-519. |
| Use kernel or userspace IPv6 autoconfiguration | Evaluate the kernel first. | Kernel behavior must satisfy configuration and reporting requirements. A userspace implementation adds protocol and lifetime work. Resolve under MWAN-517. |

## Relationship to the saga

MWAN-305 owns this migration. Its completed and cancelled historical work
remains unchanged. Host tuning, pinned-destination machinery, and the AT&T
authentication rewrite are excluded. MWAN-341 owns firewall policy. Preserve
its boundary and completed translation and steering behavior.

MWAN-507 remains the BGP and tunnel epic. It adds capabilities to the shared
interface model and does not require global networkd retirement. It may
proceed when each affected interface and route has an explicit owner. The
existing routing model specifies its connection arrangements and provider
terms; this epic must not duplicate or narrow those requirements.

In particular, a routed IPv6 prefix must not require DHCP delegation. Ordinary
IPv4 must remain independent of external IPv6 BGP. Physical interfaces,
tunnels, and BGP sessions must remain separately configurable. Existing
providers and simulators establish migration readiness before future circuits
or production prefix values exist.

Management writes and hot reload remain MWAN-440. The optional NDP and NPT
combination remains MWAN-514. Neither is required to close this epic.

## Acceptance and language

Verify runtime behavior through public boundaries with real dependencies.
Use actual DHCP servers, router advertisements, kernel networking, and packet
exchange. Do not substitute mock protocol replies or static document checks
for runtime acceptance.

Build replacement components before live cutovers. Use focused PRs. Deploy
only merged commits through the supported Configs workflow. Each deployable
phase requires testbed acceptance before production promotion. Preserve the
existing Ansible reconnect and rollback behavior.

Prove both address families, new-connection load balancing, inbound mappings
and reply paths, internal BGP, provider failure and recovery, restart, and
reboot. Observe downstream guests without an OOB bypass during changes.
Record exact revisions and interruption and recovery times. Gateway-local
probes and OOB connectivity do not prove downstream forwarding.

Close the epic only after final testbed and production acceptance. Future ISP
activation has its own schedule and evidence requirements.

Use plain terms in specifications, tickets, plans, and operator output. Define
unfamiliar terms on first use. State observed behavior, failed operation, and
required action directly. Avoid metaphors, invented shorthand, promotional
language, and unsupported claims of readiness.
