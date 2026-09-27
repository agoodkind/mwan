# Implement interface configuration and operational state

## Scope and agent ownership

The completed loader must accept provider, parent, internal, and management
interface definitions. The served configuration must preserve validated
intent and explicit ownership. Connections must have stable identities
independent of provider names and ASNs.

Follow the [coordinator](../2026-09-26-link-ownership.md) for sequencing and
execution evidence. The [interface specification](../../interfaces.md)
defines the approved behavior.

The independent design reviewer approves the exact schema and API contracts
before implementation. The code-implementer agent verifies each
settled brief against current source, implements only that brief, and stops
for review on contradictions. An independent reviewer verifies behavior
through public boundaries. The implementer has no live deployment authority.

Stack the model PR, observation PR, and state PR in that order, then merge
the short stack before dependent kernel and protocol work. Independent work
may proceed in parallel when files and runtime objects have separate writers.

## 516-model: Define shared interface intent

### Verify the starting point

| Source to modify | Verified behavior |
| --- | --- |
| [The network loader](../../../internal/networkjson/networkjson.go) | `build` skips entries with no `WAN`, keys `Config.WAN` by provider name, rejects duplicate provider names, and calls `buildLink` only for accepted providers. |
| [The networkd specification](../../../internal/networkd/spec.go) | `Spec`, `Family`, and `Delegation` combine shared intent with rendering data. `Validate` rejects free-form keys that overlap typed values. |
| [The active schema](../../../internal/yangpub/schema/goodkind-mwan-steering@2026-09-26.yang) | Production validation uses this schema for the network document. |
| [The daemon configuration](../../../internal/config/config.go) | `IfMgrSection.Links` stores `[]networkd.Spec`; provider settings use the separate `WAN` map. |
| [The served tree](../../../internal/wanconfig/tree.go) | `Gateway` uses `InternalIface` for the internal interface and `Member.Link` for provider link specifications. |
| [Daemon startup](../../../cmd/mwan/ifmgr.go) | `bootstrapWANFirewall` installs and inspects protective policy. `parseNetworkConfig` loads and applies validated configuration. After daemon construction validates module settings, `writeNetworkConfig` writes units and reloads networkd when files change. |

The loader preserves provider-local rejection after schema validation and
treats shared routing conflicts as fatal. A document with no accepted provider
currently fails. Preserve that steering-role requirement when adding roles.

### Establish dependencies and design prerequisites

This slice precedes other shared-model work. MWAN-516 state publication
follows this slice and MWAN-523. MWAN-505 may repair current events
independently if shared-file edits are serialized.

A high-intelligence design and review agent must approve exact shared types,
schema paths, package placement, legacy conversion, and validation semantics
before implementation. These are unresolved design prerequisites. The
implementer must not invent an API from this plan.

The model implementer owns the approved shared types, loader, schema, and
configuration projection. Keep this work in a focused PR before observation
and state-publication PRs. The coordinator defines the Graphite boundaries.

### Approve exact contracts

1. Define interface intent independently of provider membership, including
   matching, configured name, MAC, MTU, enabled state, VLAN parent and tag,
   and bridge membership. Preserve actual guest device types.
2. Separate connection identity from provider display name and ASN. Preserve
   existing routing tables, rule priorities, translation identities, and
   health policy during conversion.
3. Define ownership, read-only observation, release, and removal per link,
   address, route, rule, and acquisition lifecycle. Reject conflicting writers
   before rendering units or changing the kernel.
   Include NPT's external prefix `::1/128` assignment for configured and
   delegated prefixes. Keep NPT as its writer until exclusive transfer, then
   make NPT consume the address manager's installation and removal results.
4. Define assignment records with identity, family, kind, source, address or
   prefix, protocol identity, renewal and rebinding deadlines, preferred and
   valid deadlines, and validity. Keep observation and apply results separate.
5. Define required DHCPv4, DHCPv6, kernel RA/SLAAC, resolver, metric, lifetime,
   and lease-storage settings. Preserve DUID and IAID. Initial transfer uses
   fresh negotiation; MWAN-518 recovers persisted MWAN leases on restart.
6. Represent local assignment separately from ISP-routed mappings. Permit
   routed IPv6 prefixes without delegation and separately configured physical
   links, tunnels, and BGP sessions for later MWAN-507 extension.
7. Define assignment purpose for local IA_NA addresses and intentional
   forwarding addresses. In [NPT reconciliation](../../../internal/ifmgr/modules/npt/npt.go),
   replace `extraGlobal128s` inference with the approved classification for
   transferred connections. Exclude local assignments from `ExtraDNAT` while
   preserving them in `PrefixPair.DestinationExceptions` when a translated
   prefix includes them. Preserve the explicit edge mapping and other
   intentional forwarding mappings with their destination exceptions.
   Preserve legacy classification until transfer. Require the DHCPv6 address
   PR to prove local delivery and intentional forwarding with real inbound
   packets, including a local assignment inside the translated prefix.

The reviewer must approve this concrete contract before assigning code.
Preserve AT&T under its existing owner. Generic VLAN support remains required;
802.1X integration is excluded.

### Compile and publish complete intent

1. Extract shared intent from rendering-specific types according to the
   approved contract. Make the networkd renderer consume that representation
   without independently maintained copies of configuration fields.
2. Compile interface definitions before provider consumers. Validate parent
   references, cycles, ownership, and unsupported direct-owner settings before
   mutation. Preserve independent provider rejection and fatal shared errors.
3. Update `buildLink`, `Config.Apply`, `IfMgrSection`, `Gateway`, and projection
   to reuse the shared representation. Retain the legacy renderer and all
   current provider configurations. Loading new intent must not transfer
   ownership or enable direct writers for legacy connections.
4. Preserve firewall bootstrap before full configuration validation and
   preserve module validation before unit writes or direct interface changes.
   Extend `parseNetworkConfig` and `writeNetworkConfig` without recombining
   validation and mutation into one operation.

### Verify production boundaries

Extend [public validation tests](../../../cmd/mwan/deploygate_checknetwork_test.go)
and [real datastore tests](../../../cmd/mwan/wanconfig_selftest_test.go).
Use the production loader, actual libyang, and private sysrepo repositories.

1. Run the existing command
   `mwan deploy-gate check-network yang/instances/network-min.json internal/yangpub/schema`.
   Preserve the existing three-provider acceptance result.
2. Add tests for repeated provider names, repeated ASNs, and non-provider
   parent, internal, and management interfaces. Read published configuration
   through a second real datastore connection and compare accepted intent.
3. Submit conflicting writers, missing parents, cycles, unsupported options,
   and invalid identity. Assert the approved rejection and no kernel or unit
   mutation. Preserve provider-local rejection beside an accepted provider.
4. Test legacy AT&T, generic VLANs, routed prefixes without delegation, and
   ordinary IPv4 without external IPv6 BGP configuration.
5. Validate distinct local and forwarding assignment purposes through the
   loader and datastore. Reserve kernel translation and packet assertions
   for the address and DHCPv6 feature PRs.

### Review adversarial cases

The independent reviewer must vary identities and interface names, collide
routing slots, share parents, and break a non-provider dependency beside a
valid provider. Reject inferred local assignment, lost legacy options,
parallel configuration models, and any configuration that enables two writers.

## 523-observation: Track actual device state

The daemon must attribute kernel state to the correct device through absence,
appearance, and recreation. Consumers must distinguish address validity and
route ownership without changing observed objects.

### Verify the starting point

| Source to modify | Verified behavior |
| --- | --- |
| [The netlink monitor](../../../internal/netif/monitor.go) | `NewMonitor` resolves `ifIndex` once. An absent link rejects unrelated address and route events by checking the event index against the configured name. A deleted or recreated link can leave the stored index stale. |
| [Kernel snapshots](../../../internal/netif/state.go) | `CurrentAddr` stores CIDR, family, and flags without lifetimes. `CurrentRoute` omits table and protocol, and route conversion retains only the first multipath hop. |
| [Link identities](../../../internal/netif/links.go) | `ListLinkIdentities` returns names, MAC addresses, and drivers without the current kernel index. |

The monitor requests existing addresses, routes, and links when subscribing.
It drops events when its public channel is full. Route events already include
non-default destinations, table, and protocol. A separate namespace-wide
monitor subscribes to policy-rule deletion.

### Implement identity and validity observation

This focused PR follows the model contract. The observation implementer owns
the monitor and kernel snapshots. The design reviewer must first settle
identity matching, event attributes, resubscription, and snapshot consistency.
Serialize monitor edits with MWAN-505 and reuse one route-event contract.

1. Reject address and route attribution until a configured device has a
   resolved index. Resolve appearance through actual link identity and a
   refreshed kernel snapshot without admitting unrelated device events.
2. Invalidate deleted devices. Validate a replacement before accepting its
   index. Handle rename and index reuse without assigning stale events to
   another device. Synchronize identity updates across subscriptions.
3. Recover subscriptions after restart or failure. Close workers on
   cancellation. Mark dropped or failed observations and resynchronize from
   a complete kernel snapshot.
4. Preserve address flags, preferred and valid lifetimes, and origin evidence.
   Keep unknown origin explicit. Preserve route family, table, protocol,
   destination, next hop, device, metric, and required ownership attributes.
   Preserve relevant non-default routes. Reuse the namespace-wide policy-rule
   monitor for MWAN-505 without assigning rules to one interface.
5. Expose observations to state publication and existing role consumers.
   Preserve kernel RA/SLAAC lifetime ownership. Do not add or repair kernel
   objects in the observer or infer lease validity from installed state.

### Verify observation through public events

Add real-kernel cases to [the monitor suite](../../../internal/netif/monitor_test.go)
through `NewMonitor` and its events. Add public daemon scenarios beside
[the existing command namespace suite](../../../cmd/mwan/deploygate_egress_netns_test.go).
Record the actual privileged invocation after creating these tests.

1. Start with the configured device absent and change an unrelated device's
   addresses, routes, and carrier state. Assert no event is attributed to the
   absent device. Preserve this existing behavior.
2. Create the intended device, exchange packets, delete it, and recreate it
   with a different index. Verify refreshed events and packet delivery without
   daemon restart. Reproduce the stale-index defect before the fix. Repeat
   with different interface names and index reuse.
3. Send actual router advertisements. Exercise duplicate-address detection,
   deprecation, and expiry. Compare flags and lifetimes with the kernel and
   verify the observer never resets them.
4. Create identical route destinations in different tables and protocols.
   Delete one and assert complete event identity. Pressure the event channel
   and verify snapshot recovery. Restart with existing state and verify its
   remaining lifetimes.

The independent reviewer repeats these operations and compares kernel state
with public events and, after the next slice, operational reads. Parser-only
tests and fabricated events do not establish acceptance.

## 516-state: Publish operation results and history

Operational reads must distinguish ownership, acquired assignments, observed
state, apply failures, and forwarding readiness for every interface and
family. Persistent history must explain failures across ordinary restart.

### Verify the starting point

| Source to modify | Verified behavior |
| --- | --- |
| [The live store](../../../internal/wanstate/wanstate.go) | `Store`, `Snapshot`, `SetHealth`, `SetRouting`, and `SetTranslation` store member decisions. General interface assignment and observation records are absent. |
| [Operational publication](../../../cmd/mwan/wanconfig_livestate.go) | `registerLiveStateProviders` reads snapshots. `interfacesLiveItems` iterates `Gateway.Members` and publishes provider, rejection, and group state. |

`runIfMgr` creates the store independently of the optional management surface.
`MemberRouting.V4Ready` and `V6Ready` already distinguish families. The health
and tier observer does not provide complete interface history.

### Store and publish independent facts

This PR follows the model and observation PRs. The state implementer owns
store updates, operational schema additions, and publication. The design
reviewer must settle exact schema paths, history retention bounds, timestamps,
and transition records. Protocol slices publish actual assignments later;
configuration and kernel presence cannot establish acquisition.

1. Extend `Store` and `Snapshot` with stable identity, owner, acquisition,
   assignments and original deadlines, actual index, installed addresses and
   routes, and last apply result. Copy mutable records at publication.
2. Keep family validity, firewall protection, routing, and forwarding
   readiness distinct. Preserve usable IPv4 when IPv6 fails. Represent stale
   or failed observations without publishing empty success or reviving leases.
3. Record each transition's identity, family, previous and new state,
   operation, timestamp, failed dependency, and plain reason. Bound served
   recent history and retain detailed history through persistent logging.
4. Extend `interfacesLiveItems` to enumerate parents and non-provider roles.
   Keep stable identity across index changes. Preserve existing rejection,
   health, translation, and group state. Use the approved installed schema.
5. Continue state collection without sysrepo. Keep operational reads
   independent of reconciliation locks. Add no writes or hot reload.

### Verify operational reads and history

Extend the real datastore tests cited in the model slice and
[the operational publication suite](../../../cmd/mwan/wanconfig_livestate_test.go).
The private selftest accepts `mwan wanconfig-selftest --repository` and
`--models-dir` together. Use temporary directories and the release schema.
The unqualified command temporarily publishes a host marker; use the private
form for these tests.

1. Before merging this PR, run the daemon with real namespace links and its
   production monitor. Add and remove kernel addresses externally and read
   through a second sysrepo connection. Assert distinct configured and
   observed state without inventing assignments from kernel presence.
2. Remove and recreate a namespace link to verify observation transitions,
   actual device indices, timestamps, and persistent history across restart.
   This foundation does not require future address managers or clients.
3. In the later address-manager PR, remove a required link during apply.
   Read the failed operation, dependency, reason, and timestamp. Restore the
   link and packet delivery; assert recovery and preserved failure history.
4. In protocol feature PRs, use actual short-lived DHCP assignments and
   router advertisements. Let IPv6 expire while IPv4 traffic succeeds.
   Verify independent deadlines and readiness. Restart and inspect original
   assignment deadlines. These tests gate the feature PRs, not this foundation.

The independent reviewer queries during updates, removes devices during
publication, stops the management service, and compares expired leases with
surviving addresses. Reject fabricated observations, hidden failures, lost
restart history, and false health transitions caused by startup.
