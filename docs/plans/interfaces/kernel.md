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

Stack the link PR before the address PR and merge that short stack before
protocol work depends on it. MWAN-505 remains an independent repair PR.
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

This PR uses the merged model and observation contracts. The link implementer
owns the approved new module, kernel link operations, and startup wiring.
The reviewer must first settle dependency cancellation and boot naming.

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

### Verify links through the daemon

Add public daemon scenarios beside
[the existing command namespace suite](../../../cmd/mwan/deploygate_egress_netns_test.go).
Start the production `mwan ifmgr` command with isolated configuration and real
Linux namespaces, VLANs, and packet exchange. Record the privileged test
invocation when these cases exist; helper-only tests are insufficient.

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

## 398-addresses: Apply owned addresses and routes

MWAN must reconcile static addresses and routes through a shared assignment
consumer. Removal must delete stale owned objects without deleting unrelated
state. The consumer must preserve separate configuration and apply results.

### Verify the starting point

| Source to modify | Verified behavior |
| --- | --- |
| The kernel operations cited above | `ReconcileAddrs` only adds missing addresses. `ReconcileTableDefault` compares gateway and device but ignores changed metrics when both match. |
| [WAN routing](../../../internal/ifmgr/modules/wanroutes/wanroutes.go) | `Module` reconciles mapped external addresses classified as on-link and owns provider-table routes and policy rules. |
| [NPT reconciliation](../../../internal/ifmgr/modules/npt/npt.go) | `buildWANDesired` constructs the external prefix's `::1/128` for configured and delegated prefixes. `Module.Reconcile` installs it through `reconcileAddrs` before publishing translation readiness. |

The [acquisition plan](acquisition.md) owns DHCPv4 client behavior, protocol
timers, lease options, and OOB consumer updates. Those feature PRs integrate
dynamic assignments with the consumer established here.

### Implement static ownership and the assignment consumer

This PR follows links and the merged model and observation contracts. The
address implementer owns the static manager, shared assignment consumer,
route metrics, and mapped-address ownership. The reviewer must settle exact
consumer types, object identity, removal, and apply-result semantics first,
including NPT's dependency on successful address installation.

1. Add removal using explicit ownership records and observed identity. Apply
   configured IPv4 and IPv6 addresses while preserving foreign addresses,
   kernel SLAAC, and kernel-generated connected routes.
2. Apply configured main-table routes and metrics with complete identity.
   Repair a metric-only change. Preserve WAN ownership of provider-table
   policy routes and rules.
3. Consume explicit local-assignment versus ISP-routed mapping intent.
   Transfer on-link mapped address installation from WAN routing to the
   address manager only at exclusive ownership transfer. At that boundary,
   transfer NPT's external prefix `::1/128` address writer to the same manager.
   Keep its intentional forwarding purpose and edge translation behavior.
   Make NPT consume actual installation results before reporting readiness.
   Remove overlapping provider-default writers for transferred connections
   and preserve both legacy address writers elsewhere.
4. Expose the approved assignment-consumer API for later protocol clients.
   Keep assignment validity under the source's control. Apply only valid
   owned assignments and remove withdrawn owned objects. Do not infer lease
   validity from health probes or installed addresses.
   Derive the NPT edge assignment from the selected configured or valid
   delegated prefix. Remove its obsolete owned address after replacement or
   withdrawal. Integrate delegated-prefix lifetimes in the delegation PR.
5. Publish actual installation and removal results, including failed
   operations and their dependencies. Preserve configured intent separately
   from observed state. Dynamic client integration remains in its feature PR.

### Verify static assignments through the daemon

Extend the public daemon namespace suite with real kernel operations and
packet delivery. Add executable privileged commands when adding the tests.
The static configuration must invoke the production assignment consumer;
tests that call private reconciliation helpers alone are insufficient.

1. Apply static addresses and routes, an on-link mapping, and an ISP-routed
   mapping. Verify downstream replies and address resolution where required.
   Preserve foreign addresses and connected routes. Remove one owned address
   and change only a metric; verify exact kernel changes.
2. Restart with changed static address or gateway configuration. Verify stale
   owned objects disappear, unrelated objects persist, and request/reply
   traffic uses the new configuration. Do not require hot reload.
3. Remove a required namespace link during apply, inspect the reported
   failure, recreate the link, and verify recovery through the public daemon.
   Protocol feature PRs later exercise expiry and dynamic assignment changes
   through this consumer with real servers.
4. Configure an NPT external prefix and transfer address ownership. Verify
   one writer installs its `::1/128`, inbound edge translation and replies
   succeed, and a failed installation prevents translation readiness.
   Restart with a changed configured prefix and verify removal of the old
   owned address while unrelated addresses and mappings persist.
5. In the delegation PR, acquire a real prefix, change it, and let it expire.
   Verify the corresponding edge address, translation, and readiness follow
   the valid assignment. Repeat beside a legacy-owned connection and verify
   its existing NPT writer remains responsible. These cases gate delegation,
   not the static address PR.

The independent reviewer tests unchanged addresses with changed gateways,
metric-only changes, both mapping kinds, foreign addresses, and SLAAC beside
static IPv6. Reject stale owned objects, inferred local assignment, competing
kernel lifetime management, or a second writer.

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
