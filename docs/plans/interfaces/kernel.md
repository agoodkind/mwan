# Implement owned links, addresses, and route repair

The [coordinator](../2026-09-26-link-ownership.md) defines sequencing, Graphite
boundaries, and execution evidence. The [interface specification](../../interfaces.md)
defines the approved ownership and acquisition behavior.

## Assign agents and preserve boundaries

The design reviewer approves exact module placement, interfaces, and removal
semantics before implementation. A code-implementer agent verifies each
settled brief against current source and stops for review on contradictions.
An independent reviewer reproduces behavior with real dependencies. The
implementer has no live deployment or connection-transfer authority.

Merge the link PR before the static address PR. Follow it with a separate
mapped/NPT writer PR. DHCPv4 acquisition follows both address PRs.
MWAN-505 remains an independent repair PR.
Parallel work is permitted when files and runtime objects have separate
writers. Serialize shared edits to the monitor, kernel operations, daemon,
and WAN routing module.

Preserve existing providers, OOB and failover roles, translation, and
steering. Keep AT&T under its current owner. Generic VLAN support remains
required; 802.1X integration is excluded. MWAN-341 protection must precede
network operations that require it.

## 397-links: Reconcile owned links

MWAN must apply assigned physical settings and VLAN dependencies while an
absent provider leaves other connections operational.

### Verify the starting point

| Source to modify | Verified behavior |
| --- | --- |
| [Link identities](../../../internal/netif/links.go) | `ListLinkIdentities` reads names, MAC addresses, and sysfs drivers without applying configuration. |
| [Kernel operations](../../../internal/netif/state.go) | `linkByName` resolves devices for address and route operations. The file does not implement the complete planned link lifecycle. |
| [Daemon startup](../../../cmd/mwan/ifmgr.go) | `bootstrapWANFirewall` installs and inspects protective policy before `parseNetworkConfig`. Daemon construction validates module settings before `writeNetworkConfig` writes units and reloads networkd. |
| [Rename diagnostics](../../../cmd/mwan/ifmgr_links.go) | `warnRenamedLinks` logs rendered-name mismatches without renaming active devices. |
| [The daemon](../../../internal/ifmgr/daemon.go) | `Daemon.Run` starts a monitor and optional clients before module initialization. Ordinary initialization errors stop startup. |
| [Role composition](../../../internal/ifmgr/roles.go) | Existing roles select registered modules. |

The guest transit interface `enmwanbr0` is a virtio device attached to a
hypervisor bridge. Its name does not require a Linux bridge inside the guest.

### Implement link dependencies

Admit only MWAN-owned link-only connections with an explicit connection ID.
Reject family settings, provider settings, networkd files, and the internal
and management roles for that owner. Assign its link claim to the MWAN link
writer only when the same PR installs the runtime writer. Keep all live
providers under networkd. The reviewer must settle dependency cancellation
and boot naming before implementation.

1. Match actual identity unambiguously before mutation. Reject absent or
   multiple matches as explicit connection state. Validate parent references,
   cycles, VLAN settings, incompatible device types, and duplicate writers.
2. Apply configured name, MAC, MTU, and enabled state only for MWAN-owned
   links. Preserve udev behavior for legacy links. Apply the reviewed boot
   naming procedure when a live device cannot safely be renamed.
3. Create VLANs after parents exist. Apply only explicit bridge membership.
   Preserve actual guest link types and shared parents with active children.
   Remove only owned virtual links and membership; retain unrelated objects.
4. Start connections independently and recover when a missing device appears.
   Preserve existing role behavior. On restart, retain matching link state
   and recover subscriptions without resetting interfaces.
5. Preserve protective firewall bootstrap before full configuration validation.
   Complete module validation before writing networkd units or starting direct
   link operations. Keep `parseNetworkConfig` separate from `writeNetworkConfig`.

Require a configured permanent MAC address for MWAN-owned physical links.
Reject driver-only matching until a boot-stable single-device selector exists.
Stage a udev name rule for each accepted physical link. Do not rename an active
physical device. Verify the observed permanent MAC before reporting the link
ready.

Persist and sync a creation record with a random alias token before adding a
virtual link with a random temporary name. The kernel does not retain an alias
supplied with VLAN or bridge creation. Verify the new link's type, parent, and
tag; then set and verify the recorded alias before assigning the configured
name. On restart, recover an untagged temporary link only when the record,
boot ID, temporary name, type, parent, and tag match. Recover or remove a
tagged link only when its recorded alias and identity match. Do not adopt or
remove an ambiguous link. A kernel index alone does not establish ownership.
A missing parent must delay only dependent children; it must not stop other
connections.

Keep the configured ownership state file after removing the final owned
connection. The reconciler must read that file and prune the final link before
the state file configuration can be removed. A matching alias without a
durable record is a conflict, not proof of ownership.

### Verify links through the daemon

Add public daemon scenarios beside
[the existing command namespace suite](../../../cmd/mwan/deploygate_egress_netns_test.go).
Start the production `mwan ifmgr` command with isolated configuration and real
Linux namespaces, VLANs, and packet exchange. Add link-only owned connections
beside a valid legacy provider in the test configuration. Record the
privileged test invocation when these cases exist; helper-only tests are
insufficient. Do not transfer a live provider in MWAN-397.

1. Start two connections with one absent provider. Verify traffic through the
   available connection, then create the missing device and verify recovery.
2. Configure shared-parent VLANs with varied names and tags. Verify packets,
   configured MAC and MTU, and actual kernel device types.
3. Remove one child and restart. Verify the parent, surviving child, device
   indices, and traffic persist. Present ambiguous matches and incompatible
   devices and assert failure without candidate mutation.
4. Place a legacy-owned connection beside an MWAN-owned connection. Verify
   no new writer or protocol client starts for the legacy connection.
5. Start with invalid connection or module configuration. Verify protective
   firewall policy remains installed and no unit or link mutation occurs.
   Repeat with invalid firewall policy and verify prior rules remain intact.

The independent reviewer races appearance with startup, recreates parents,
and restarts while sibling traffic continues. Inspect kernel state and
packets rather than relying on a successful reconciliation return value.

## 398-static-addresses: Apply local addresses and main routes

MWAN must reconcile static local addresses and optional main-table default
routes on MWAN-owned non-provider links through a runtime assignment writer.
The writer must report source validity and kernel application results
separately. The [acquisition plan](acquisition.md) owns DHCPv4 client behavior,
protocol timers, lease options, and OOB consumer updates.

### Verify the starting point

| Source to modify | Verified behavior |
| --- | --- |
| The kernel operations cited above | `ReconcileAddrs` only adds missing addresses. `ReconcileTableDefault` compares gateway and device but ignores changed metrics when both match. |
| [WAN routing](../../../internal/ifmgr/modules/wanroutes/wanroutes.go) | `Module` reconciles mapped external addresses classified as on-link and owns provider-table routes and policy rules. |
| [NPT reconciliation](../../../internal/ifmgr/modules/npt/npt.go) | `buildWANDesired` constructs the external prefix's `::1/128` for configured and delegated prefixes. `Module.Reconcile` installs it through `reconcileAddrs` before publishing translation readiness. |

### Implement static ownership and the assignment writer

This PR follows links and the merged model and observation contracts. Admit
static local addresses and an optional main-table default route only on
MWAN-owned non-provider links. Install the runtime writer in the same PR.
Provider addressing, mapped/NPT addresses, and DHCP acquisition belong to
later PRs. The reviewer must settle consumer types, journal identity, removal,
and apply-result semantics before implementation.

1. Persist exact owned address and route identities before kernel writes.
   Reconcile the journal with observed identity on restart.
   Apply configured IPv4 and IPv6 local addresses without deleting foreign
   addresses, kernel SLAAC, or kernel-generated connected routes. Manage the
   per-link IPv4 secondary-address promotion setting while owned IPv4
   addresses require it. This prevents deletion of a foreign secondary
   address when MWAN deletes an owned primary address in the same subnet.
   Record the prior setting and restore it by verified link identity after
   removing the owned addresses.
2. Apply only the configured optional main-table default route, including its
   metric. Replace a route when only its metric changes. WAN routing continues
   to write provider-table routes, policy routes, and rules.
3. Expose the approved assignment-consumer API for later protocol clients.
   The source controls assignment validity. Apply only valid owned
   assignments and remove withdrawn owned objects. Health probes and
   installed addresses do not establish lease validity.
4. Report source validity separately from each kernel installation or
   removal result. Record failures and dependencies without changing source
   validity. Report configured intent separately from observed state.

### Verify static assignments through the daemon

Extend the public daemon namespace suite with real kernel operations and
packet delivery. Add executable privileged commands when adding the tests.
The static configuration must invoke production admission and the runtime
writer. Private reconciliation helpers alone do not satisfy acceptance.

1. Start the production daemon in namespaces with a legacy provider and an
   MWAN-owned non-provider link. Apply static IPv4 and IPv6 local addresses
   and an optional main-table default. Verify downstream packets, foreign
   addresses, SLAAC, connected routes, and no provider address writes.
2. Remove one owned address and change only the default-route metric. Verify
   exact journal entries and kernel changes. Restart with a changed address
   or gateway. Verify removal of stale owned objects and retention of
   unrelated objects. Hot reload is not required.
3. Remove a required namespace link during apply. Verify that source validity
   remains distinct from the reported kernel failure. Recreate the link and
   verify recovery and downstream packets through the public daemon.

## 398-mapped-addresses: Transfer mapped and NPT writers

This additive PR extends the assignment writer after static ownership passes
review. It must not transfer a live provider before MWAN-519 or MWAN-399.
Networkd, WAN routing, and NPT continue to write addresses for connections
without exclusive transfer.

1. Consume explicit local-assignment versus ISP-routed mapping intent.
   Transfer on-link mapped address installation from WAN routing only for an
   exclusively transferred connection. WAN routing continues to write
   provider-table routes and policy rules.
2. Transfer NPT's external prefix `::1/128` address writer to the assignment
   writer at the same exclusive boundary. Classify this address as an
   intentional forwarding address for edge translation. Derive it from the
   selected configured or valid delegated prefix. Remove obsolete owned
   addresses after replacement or withdrawal. The delegation PR integrates
   delegated-prefix lifetimes.
3. Require an actual address installation result for translation readiness.
   Report source validity separately. Remove overlapping provider-default
   writers only for exclusively transferred connections.

### Verify mapped assignments through the daemon

Extend the public daemon namespace suite with an exclusively owned synthetic
connection and a legacy-owned provider. Do not activate a live provider in
this PR.

1. Apply on-link and ISP-routed mappings. Verify downstream replies, address
   resolution where required, and one writer per mapped address. Verify the
   legacy provider's existing writer still installs its addresses.
2. Apply a configured NPT external prefix. Verify one writer installs its
   `::1/128`, inbound edge translation and replies succeed, and failed
   installation prevents translation readiness. Restart with a changed
   prefix and verify exact removal without changing foreign addresses.
3. In the delegation PR, acquire a real prefix, change it, and let it expire.
   Verify the edge address, translation, and readiness follow the valid
   assignment. Repeat beside a legacy-owned connection and verify its
   existing NPT writer remains responsible. These cases gate delegation.

The independent reviewer tests unchanged addresses with changed gateways,
metric-only changes, both mapping kinds, foreign addresses, and SLAAC beside
static IPv6 across the relevant PRs. Reject stale owned objects, inferred
local assignment, competing kernel lifetime management, or a second writer.

## 505-route-repair: Restore deleted owned routes

Deleted owned routes and rules must recover through events before the next
periodic reconciliation. Downstream replies and unrelated routing state
must remain verifiable.

### Verify the starting point

[The monitor](../../../internal/netif/monitor.go) rejects non-default routes
in `routeUpdateToEvent`, omits table and protocol from `Event`, and has no rule
subscription. WAN routing's `watchedIfaces` already includes `InternalIface`
and providers. Its `onMonitorEvent` accepts only `isDefaultRouteEvent`.
`ReconcileTableRoute` can restore prefix routes when requested; the daemon
exposes `Env.RequestReconcile` and periodic reconciliation.

This standalone PR can repair current behavior without new protocol clients
or global networkd retirement. The route implementer owns event conversion
and WAN repair triggers. The reviewer must approve exact ownership matching
and event scheduling. Serialize monitor edits with MWAN-523.

### Implement owned-object event repair

1. Preserve family, table, protocol, destination, next hop, device, metric,
   and required ownership attributes in route events. Deliver non-default
   deletions, including internal return routes. Retain existing interface
   coverage without adding a duplicate internal monitor.
2. Observe policy-rule deletion with family, priority, selectors, and target
   table. Do not assign rule events to arbitrary interfaces. Match events to
   desired owned objects and preserve unrelated or kernel-owned state.
3. Replace the default-only callback condition with ownership-aware triggers.
   Use bounded, serialized reconciliation and coalesce bursts. Prevent
   self-generated add/replace events from causing continuous reconfiguration.
   Preserve genuine upstream gateway-change handling.
4. Rebuild desired objects from current intent and valid assignments. Do not
   resurrect expired, withdrawn, or ineligible policy. Record failed repairs
   and retry through the existing mechanism.

### Verify event timing and downstream recovery

Add public daemon tests with real gateway, downstream, and upstream namespaces
in the existing command suite. Use actual routes, rules, and request/reply
packets. Set the periodic interval long enough to distinguish event repair.

1. Reproduce baseline failure by deleting an internal return route during
   both-family traffic. After the fix, measure deletion, restoration, and
   lost packets. Require replies before the next periodic reconciliation.
2. Repeat for an owned provider default and a policy rule without a link
   event. Preserve an unrelated same-destination route in another table or
   protocol. Delete several owned objects and verify bounded reconciliation
   after repair rather than a continuous self-generated event sequence.
3. Withdraw an assignment or change eligibility before deleting its old
   route. Assert that repair does not restore obsolete policy.

The independent reviewer varies interface names, tables, metrics, and
families. A route listing or manual reconciliation is insufficient proof.
Measure downstream interruption and recheck identity recovery after shared
monitor changes.
