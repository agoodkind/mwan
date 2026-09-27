# Implement interface address acquisition

Implement DHCPv4, delegated prefixes, kernel IPv6 autoconfiguration, and
DHCPv6 interface addresses as four focused PRs. Each numbered ticket section
defines one PR boundary. Follow the
[coordinator](../2026-09-26-link-ownership.md) for the authoritative Graphite
stack, parallel work, and common gates. Apply the
[approved specification](../../interfaces.md).

## Shared implementation and verification contract

Use MWAN-516's shared model and assignment contracts, MWAN-397's links, and
MWAN-523's observations. Each section identifies additional dependencies.
The code implementer verifies the source premise and approved dependency APIs
before editing, then implements only the reviewed brief. The reviewer owns
unresolved protocol and API decisions and independent runtime verification.
Return unsupported behavior to the reviewer. Do not improvise a design,
deploy, or transfer live ownership from these implementation PRs.

Merge MWAN-522's standalone daemon runner bootstrap after the shared model
and before accepting these protocol PRs. The runner must execute the
production loader and daemon with real servers, isolated Linux networking,
and downstream packets. Record its executable privileged commands.

Each feature PR adds its own tests and server scenarios to that runner.
Run and independently verify those scenarios before merging the feature.
Run MWAN-522's final assembled suite after all required feature PRs merge.
Do not defer individual feature acceptance until that final run.

No inspected existing command runs the proposed protocol tests. The existing
`make test-netns` checks routing, NPT, and steering regressions. Skipped
protocol tests do not satisfy acceptance. Record exact commands, revisions,
server configuration, kernel version, and results.

## Verified current behavior

In [the DHCPv4 client](../../../internal/netif/dhcp.go), `DHCPConfig` has
interface and timeout settings but no configured client identifier.
`acquire` performs discovery and request. `bound` schedules renewal at half
the lease duration and emits `LeaseExpired` after any renewal error.
`LeaseInfo` and `leaseToInfo` expose an address, mask, router, server, lease
duration, and acquisition time; they do not expose rebinding deadlines.

[Daemon startup](../../../internal/ifmgr/daemon.go) starts this client from
`Daemon.Run` and optionally constructs an RA client. This lifecycle has no
DHCPv6 address client.

[The OOB consumer](../../../internal/ifmgr/modules/oobv4/oobv4.go) uses
`OnDHCPLease`, `applyBound`, and `applyExpired`. Expiration clears the OOB
default route and leaves the previous address installed.

[The failover consumer](../../../internal/ifmgr/modules/mainv4/mainv4.go)
uses the same lease events to apply addresses and a main-table default.
Its `applyExpired` also leaves the address installed. The failover role
selects this module, which remains inactive when DHCPv4 is disabled.

[The prefix source](../../../internal/pd/source.go) defines `Source.Prefix`
as a prefix, presence flag, and error. `DefaultSource.Prefix` tries networkd
D-Bus, networkctl, and kernel routes in order. This interface returns no
renewal, preferred, or valid deadlines and performs no DHCP exchange.

`Family.DHCP` and `FamilyV6.AcceptRA` in
[the link specification](../../../internal/networkd/spec.go) configure
networkd behavior. `Delegation` includes `Hint`, `DUIDType`, `DUID`,
`WithoutRA`, `UseDelegatedPrefix`, and `RouterLifetimeSeconds`. The typed
delegation structure has no IAID field. These fields configure networkd;
they do not implement a MWAN DHCPv6 client.

[Router solicitation support](../../../internal/netif/ra.go) implements
`NewRAClient` and `RAClient.SolicitRA`. It sends a solicitation and returns
the first parsed router advertisement. It does not apply automatic addresses
or manage their lifetimes.

[Host IPv6 policy](../../../internal/ifmgr/modules/hostipv6policy/hostipv6policy.go)
uses `InterfacePolicy`, `reconcilePolicySysctls`, and `reconcileSysctl` to
configure `accept_ra`, `autoconf`, and `accept_ra_defrtr`. It also checks or
removes RA defaults and can solicit advertisements. This is an existing
host-role policy boundary, not the complete provider acquisition contract.

The [service unit](../../../cmd/mwan/mwan-ifmgr@.service) defaults to
`ProtectKernelTunables=true`. Existing deployments that write IPv6 sysctls
require a reviewed drop-in with appropriate writable paths.

## 398-dhcpv4: Implement DHCPv4 acquisition

Preserve a valid DHCPv4 assignment through temporary renewal failure, rebind
when required, and withdraw owned addresses and routes after rejection or
expiration.

### Dependencies and contracts

Complete MWAN-398's static address ownership before this PR. Reuse the
shared contracts for identity, owner, deadlines, routes, and withdrawal.
The protocol client publishes assignments. The address manager applies
addresses and assigned main-table routes. WAN routing retains policy tables
and rules. Preserve OOB table selection, failover main-table routing, and
existing translation behavior.

The reviewer must approve supported client identifier encoding, accepted
lease options, invalid timer handling, and both existing consumer interfaces
before implementation. Inventory configured DNS, route, and identity options
through the existing `Spec`, `Family`, and free-form entries.
Reject unsupported required options before starting acquisition.

### 1. Implement the reviewed client lifecycle

Modify `DHCPConfig`, `LeaseInfo`, `LeaseState`, `acquire`, `bound`, and
`leaseToInfo` in the existing client, plus client construction in
`Daemon.Run`.

1. Read identity and acquisition settings from the shared model. Start one
   client only after the connection has an available link and MWAN ownership.
2. Decode the server's renewal time T1, rebinding time T2, and expiration.
   Apply the reviewed protocol defaults and validation rules when options
   are absent or invalid. Preserve the absolute deadlines during retries.
3. Retain the assignment after a renewal timeout while its validity remains.
   Rebind at T2, accept a valid new assignment, and withdraw an assignment
   when the server rejects it with a NAK or its validity expires.
4. Publish changed address, router, metric, and accepted options through the
   shared assignment contract. Keep probe failure separate from validity.
5. Preserve configured identifiers during fresh acquisition. Leave persistent
   restart recovery to MWAN-518.

### 2. Update the assignment consumers

Modify the existing OOB and failover consumers. Extend the address manager
introduced by MWAN-398 through its reviewed assignment interface. Give each
owned address and route one writer during this integration.

1. Remove obsolete owned addresses and routes after replacement or withdrawal.
   Preserve unrelated addresses, OOB table selection, and failover main-table
   routing. Apply changed gateways even when the address remains unchanged.
2. Keep a valid assignment installed during renewing and rebinding states.
3. Prevent delayed events from a stopped client from overwriting assignments
   for a recreated interface or replacement client.
4. Preserve the failover consumer's inactive behavior when DHCPv4 is disabled.

### 3. Verify real DHCP behavior

Create the proposed test file
[cmd/mwan/dhcpv4_netns_test.go](../../../cmd/mwan/dhcpv4_netns_test.go).
This file does not exist at the inspected baseline. Use the MWAN-522 runner
with the production configuration loader, daemon, isolated Linux links, a
real DHCPv4 server, and a downstream packet sender.

1. Acquire a short lease and observe the configured identifier in server
   records or packet capture. Check served assignment state, kernel address,
   default route, and downstream packet delivery.
2. Silence the original server at T1. Expect the assignment and packets to
   remain valid before expiry. Enable a second server for T2 and observe
   rebinding and renewed deadlines.
3. Exercise NAK, complete server absence through expiry, a changed address,
   and a changed gateway. Expect obsolete owned state to disappear and
   unrelated state to remain.
4. Repeat through the OOB role. Expect its selected table and valid-lease
   connectivity to remain compatible while expired addresses are removed.
5. Repeat through the failover role with DHCPv4 enabled. Verify changed
   addresses and gateways, retained connectivity during renewal and rebind,
   and removal of owned addresses and main-table defaults after NAK or expiry.
   Confirm unrelated state survives. Start the role with DHCPv4 disabled and
   verify that it starts no client and performs no DHCP address or route writes.

### Review and handoff

The reviewer independently exercises early renewal timeout, omitted and
invalid T1/T2, NAK, changed gateway without address change, event backlog,
interface recreation, and expiry in both OOB and failover roles. The reviewer
compares server packets, original deadlines, served state, kernel state,
and downstream traffic.

Give MWAN-518 the client recovery metadata and both updated consumer contracts.

## 227-delegation: Implement delegated prefixes

Acquire and renew DHCPv6 delegated prefixes with explicit validity deadlines
and update existing translation consumers when delegation changes.

### Dependencies and contracts

Complete MWAN-398's address application contract before this PR.
MWAN-227 establishes the single DHCPv6 client lifecycle per connection.
MWAN-517's DHCPv6 address work extends this lifecycle after it is reviewed;
delegation must not depend on that later implementation.

The shared lifecycle uses one configured DUID, the DHCPv6 client identifier,
and separate IAIDs for assignment associations. Keep delegated prefix
associations, called IA_PD, distinct from address associations, called IA_NA.
Use the shared model for each association's identity and deadlines.

The reviewer must approve the lifecycle API, protocol library operations,
solicitation and retransmission rules, supported response status codes, and
consumer withdrawal semantics before implementation. The review must also
resolve required on-link delegated-prefix use and downstream announcements
from the actual configuration. Keep ordinary routed prefixes independent
of delegation and preserve existing translation and source-rule behavior.

### 1. Establish the shared DHCPv6 lifecycle

Create the proposed
[internal/netif/dhcpv6.go](../../../internal/netif/dhcpv6.go). This is a new
file, not an existing client. Integrate its lifecycle with
`Daemon.Run` using the shared configuration
and assignment interfaces.

1. Start one client for the connection after link readiness and exclusive
   MWAN ownership. Preserve configured DUID and IAID values and prefix hints.
2. Implement solicitation, selection, request, renewal at T1, rebinding at T2,
   and expiration. Support configured operation without an initial router
   advertisement. Apply the reviewed protocol defaults for omitted timers.
3. Publish each prefix with its preferred and valid deadlines. Mark a prefix
   deprecated after preferred expiry and unusable after valid expiry.
   Do not infer lease validity from a surviving kernel route.
4. Define the association extension required by MWAN-517 without starting
   a second DHCPv6 client. Keep independent association results and deadlines
   within the common client lifecycle.
5. Define the metadata required by MWAN-518 without implementing networkd
   lease import. Initial transfer always negotiates with preserved identity.

### 2. Replace observation for MWAN-owned connections

Modify `Source` and `DefaultSource.Prefix` through a compatible adapter in the
existing prefix package. Create the proposed
[internal/pd/assignment.go](../../../internal/pd/assignment.go) for the
assignment-backed source if the reviewed API requires a separate adapter.

1. Select the source from explicit connection ownership. Keep networkd
   observation only for connections networkd still owns.
2. Publish validated delegation to existing translation and routing consumers.
   Withdraw expired state and apply changed prefixes and prefix lengths.
3. Implement reviewed delegated-prefix link use and announcement behavior
   without duplicating the address manager's writes.
4. Preserve non-DHCP routed-prefix configurations and completed translation
   behavior. Keep default-router discovery under kernel RA policy.

### 3. Verify actual delegation and translation

Create the proposed
[cmd/mwan/dhcpv6pd_netns_test.go](../../../cmd/mwan/dhcpv6pd_netns_test.go).
Run the production daemon with a real DHCPv6 server, isolated Linux links,
the assignment-backed source, and downstream IPv6 traffic.

1. Observe the configured DUID, IAID, and prefix hint on actual requests.
   Exercise both configured solicitation without RA and RA-dependent start.
2. Renew a short delegation, suppress replies until rebinding, then accept
   a new prefix with a different length. Compare published deadlines with
   server replies and confirm translated downstream traffic.
3. Deprecate and expire the delegation. Expect consumer withdrawal at the
   valid deadline even if an old route remains visible.
4. Repeat with a routed static IPv6 prefix and a networkd-owned connection.
   Expect independent operation and no competing MWAN DHCPv6 client.

### Review and handoff

The reviewer independently exercises absent RA, changed DUID or IAID,
server rejection, zero and invalid lifetimes, different prefix lengths,
renewal loss, rebind, stale kernel routes, and ownership changes. Confirm
that expired delegation cannot remain usable through a cached prefix.

Give MWAN-517 the reviewed client API and association contract. Give
MWAN-518 the required recovery metadata.

## 517-autoconfiguration: Configure kernel IPv6 autoconfiguration

Configure and observe Linux router discovery and automatic IPv6 addresses
for MWAN-owned interfaces. Linux must manage address and router lifetimes.

### Dependencies and contracts

This PR can proceed after the shared prerequisites without waiting for
DHCPv6 delegation or address acquisition.

The approved implementation uses kernel router advertisement processing and
SLAAC, the kernel's automatic address configuration. MWAN configures policy
and observes results. The address manager must recognize kernel automatic
addresses and must not recreate addresses the kernel legitimately expires.
DHCPv6 does not supply default-router discovery.

The reviewer must approve the exact kernel controls, route metric behavior,
DNS option handling, and shared policy API after testing the supported
kernel. Preserve existing host-role policy and configured resolver behavior.
Report a demonstrated kernel limitation before proposing a design change.
A userspace SLAAC engine is outside this approved work.

### 1. Apply shared per-interface kernel policy

Reuse the existing host policy's sysctl boundary. Create the proposed
[internal/ifmgr/modules/autoconfiguration/autoconfiguration.go](../../../internal/ifmgr/modules/autoconfiguration/autoconfiguration.go)
for connection acquisition and register it through
[daemon startup](../../../cmd/mwan/ifmgr.go). Extend the existing host policy
only where sharing the reviewed sysctl operation requires it.

1. Apply configured advertisement acceptance, autoconfiguration, default
   router learning, forwarding interaction, and supported route metrics.
   Validate unsupported settings before changing a connection.
2. Apply controls after the link exists and before acquisition is considered
   ready. Respect MWAN-341 startup protection and exclusive ownership.
3. Reapply necessary policy when the intended device is recreated. Use
   MWAN-523's actual device identity and preserve unrelated interfaces.
4. Preserve the existing host role and coordinate the required service
   permissions with MWAN-521. Do not remove unrelated service protections.

### 2. Publish observed acquisition and expiration

Consume the observation contract from MWAN-523 and publish through the
MWAN-516 state contract. Keep existing router solicitation support only
where the reviewed policy requires active solicitation.

1. Distinguish tentative, usable, deprecated, and expired automatic addresses.
   A tentative address is undergoing duplicate-address detection and must
   not establish readiness.
2. Publish router validity separately from prefix and address lifetimes.
   Reflect kernel withdrawal without reinstalling expired objects.
3. Preserve IPv4 readiness during IPv6 failure and recovery. Keep DHCPv6
   assignments separate from kernel automatic addresses.
4. Apply the reviewed DNS and advertisement-option policy without adding a
   resolver service or silently ignoring a required option.

### 3. Verify kernel behavior through the daemon

Create the proposed
[cmd/mwan/autoconfiguration_netns_test.go](../../../cmd/mwan/autoconfiguration_netns_test.go).
Use the production loader and daemon, a real router advertisement daemon,
isolated Linux interfaces, and downstream packet exchange. The proposed
module and test do not exist at the inspected baseline.

1. Enable forwarding and emit advertisements with independent router,
   preferred-prefix, and valid-prefix lifetimes. Confirm configured sysctls,
   kernel addresses and routes, served state, and downstream packets.
2. Exercise tentative addresses and an actual duplicate on a connected peer.
   Expect no usable assignment or readiness before successful detection.
3. Deprecate and expire a prefix, then expire the router while an address
   remains valid. Expect each observed object to follow its own deadline.
4. Restore advertisements and recreate the interface with a different index.
   Expect recovery without changing unrelated interfaces or usable IPv4.
5. Run the existing host policy scenario to confirm its allowed and denied
   RA behavior remains intact.

### Review and handoff

The reviewer independently tests forwarding enabled, initial sysctl conflicts,
missing permission, duplicate detection, router expiry before prefix expiry,
prefix expiry before router expiry, interface recreation, and absent RA.
Compare actual kernel lifetimes with served state and packet delivery.

Give MWAN-521 the exact required sysctls and service permissions.

## 517-dhcpv6: Implement DHCPv6 interface addresses

Acquire DHCPv6 interface addresses alongside delegated prefixes through one
client lifecycle per connection.

### Dependencies and contracts

Complete MWAN-398's address application interface and MWAN-227's shared
DHCPv6 lifecycle before this PR. Require merged kernel autoconfiguration
from MWAN-517 as the separate prerequisite for default-router acceptance.
Extend the reviewed DHCPv6 client. Do not create an independent address-only
client alongside the delegation client.

Preserve the shared DUID and configured IAIDs established by MWAN-227.
Publish each assignment with its own preferred and valid deadlines,
association, and validity.
Keep kernel RA default routes and automatic addresses separate.

The reviewer must approve IA_NA request and reply handling, association
timer aggregation, partial-success behavior, duplicate-address handling,
and the configuration rules for starting DHCPv6. Record supported DNS and
other required options before implementation. Do not infer a default router
from a DHCPv6 server address.

Review address purpose with the translation consumer before implementation.
The existing [NPT address classification](../../../internal/ifmgr/modules/npt/npt.go)
uses `extraGlobal128s` for intentional forwarding and BPF destination exceptions.
[The DNAT rule builder](../../../internal/ifmgr/modules/npt/rules.go) forwards
those addresses to OPNsense. The
[BPF packet processor](../../../internal/ifmgr/modules/npt/bpf/npt.c) translates
destinations inside an external prefix unless they match an exception.
The shared assignment contract must distinguish local interface assignments
from intentional forwarding addresses. The reviewer must settle classification
and update ordering across address installation and both translation mechanisms.

### 1. Extend the delegation client's association support

Modify the proposed DHCPv6 client established by MWAN-227. Modify the
existing `Daemon.Run` integration only as required by the reviewed shared
lifecycle API.

1. Add configured IA_NA requests to the same client that negotiates IA_PD.
   Support address-only, delegation-only, and combined configurations.
2. Process successful and failed associations independently while preserving
   a single protocol lifecycle and configured identities.
3. Renew and rebind using the reviewed protocol timer rules. Preserve valid
   assignments during temporary server loss. Deprecate and withdraw each
   address according to its own preferred and valid deadlines.
4. Publish new, changed, rejected, and expired assignments to the shared
   address manager. Integrate actual duplicate-address detection and the
   reviewed protocol response without treating tentative addresses as ready.
5. Preserve required accepted options and resolver policy. Provide MWAN-518
   the complete metadata required to validate these assignments after restart.

### 2. Keep route and family state independent

Use the MWAN-516 assignment and readiness APIs and the MWAN-523 kernel
observation API. Reuse the address manager from MWAN-398.
Modify NPT address classification, DNAT rule selection, and BPF exception
inputs through the reviewed address-purpose contract.

1. Apply only MWAN-owned DHCPv6 interface addresses. Preserve static and
   kernel automatic addresses with different owners.
2. Keep delegation validity independent of interface-address validity.
   Update prefix mappings from valid delegation changes. Update local address
   classification and BPF destination exceptions when interface assignments
   change.
3. Preserve usable IPv4 when DHCPv6 fails. Record the failed association and
   operation without reporting unrelated families as unusable.
4. Require an independently observed or configured IPv6 route for forwarding
   readiness. A DHCPv6 address alone does not establish a default route.
5. Exclude local IA_NA assignments from DNAT to OPNsense. Preserve local
   destination exceptions in BPF when an assignment is inside a translated
   external prefix. Preserve DNAT and BPF exceptions for intentional forwarding
   addresses. Remove obsolete local exceptions when assignments expire or change.
   Apply the reviewed update ordering without exposing a usable local assignment
   to forwarding translation.

### 3. Verify combined real-server assignments

Create the proposed
[cmd/mwan/dhcpv6ia_netns_test.go](../../../cmd/mwan/dhcpv6ia_netns_test.go).
Use the production daemon and loader, a real DHCPv6 server, a real RA source,
isolated Linux links, and downstream traffic. This test is new work.

1. Run address-only, delegation-only, and combined configurations. Observe
   requests at the server and prove one DUID with the configured IAIDs and
   one client lifecycle per connection.
2. Assign different preferred and valid lifetimes to the address and prefix.
   Deprecate or expire one association while the other remains valid.
   Confirm independent served state and correct kernel and translation state.
3. Reject one association, change an address, cause duplicate detection,
   and suppress renewal replies through rebinding. Verify protocol recovery
   and removal of obsolete owned state.
4. Stop router advertisements while DHCPv6 remains successful. Expect the
   default route to expire according to RA lifetime. Restore RA and verify
   downstream recovery without disrupting IPv4.
5. Send actual inbound packets to a local IA_NA address and an intentional
   forwarding address with NPT enabled. Include a local assignment inside the
   translated external prefix. Expect local service replies for the assignment
   and OPNsense replies for the forwarding address. Repeat after renewal,
   address replacement, and expiry. Verify that expired assignments stop
   accepting local traffic, obsolete exceptions disappear, and intentional
   forwarding remains operational.

### Review and handoff

The reviewer independently tests partial replies, association-specific
status errors, unequal deadlines, renewal and rebind loss, changed IAID,
duplicate addresses, and successful DHCPv6 without a usable default router.
Confirm that IA_NA expiry cannot revoke valid IA_PD and that an expired
IA_PD cannot remain active through translation state.
Verify local delivery and intentional forwarding independently through DNAT
and BPF processing, including assignment changes during packet exchange.

Give MWAN-518 the completed IA_NA recovery metadata.
