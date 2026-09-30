# Recover MWAN assignments after restart

## 518-restart: Recover persisted assignments

Recover MWAN-owned DHCP assignments after daemon restart and guest reboot
without extending a saved lease from the restart alone or resetting matching
interfaces. A new server assignment may set new deadlines.
Follow the [coordinator](../2026-09-26-link-ownership.md) and
[approved specification](../../interfaces.md).

## Current behavior

[The DHCPv4 client](../../../internal/netif/dhcp.go) keeps `last` and the
active lease in process memory. `StartDHCPClient` starts `run`, which begins
with `acquire`. `LeaseInfo` stores acquisition time and duration but does
not implement persisted restart validation.

[The DHCPv6 client](../../../internal/netif/dhcpv6.go) manages IA_NA and IA_PD
in one process. It records each association's deadlines in memory, but starts
a fresh negotiation after restart. The legacy prefix source still observes
networkd delegation on connections that have not transferred.

[The daemon service](../../../cmd/mwan/mwan-ifmgr@.service) already permits
writes to `/var/lib/mwan` through `ReadWritePaths`. It starts before udev
coldplug and networkd, uses `ProtectSystem=strict`, and deliberately omits
`StateDirectory` to avoid boot ordering through local filesystem setup.
Preserve that existing constraint when configuring lease storage.

## Dependencies and contracts

Start recovery after the acquisition code has merged, including MWAN-398
DHCPv4, MWAN-227 delegation, and MWAN-517 interface addresses.
Use MWAN-516's identities and assignment contract and MWAN-523's kernel
observations. The shared DHCPv6 lifecycle remains the sole owner of IA_NA
and IA_PD recovery.

The reviewed recovery contract uses a versioned, bounded, checksummed record
per connection and protocol. Replace records with a synced temporary file,
atomic rename, and parent-directory sync. Compare stable connection, link,
client, and association identities before attempting recovery. A route metric
change does not invalidate a protocol binding; rebuild local routes from the
current policy after validation. A surviving address or prefix is not proof
of protocol validity.

DHCPv4 sends an INIT-REBOOT DHCPREQUEST with the requested address and no
server identifier, as [RFC 2131](https://www.rfc-editor.org/rfc/rfc2131.html#section-4.4.2)
specifies. DHCPv6 sends Confirm for address-only state and Rebind for any
valid delegated prefix, as [RFC 9915](https://www.rfc-editor.org/rfc/rfc9915.html#section-18.2.12)
specifies. A Confirm success retains the original address
lifetimes. A Rebind reply validates only the associations it actually returns.
Keep an unanswered recovery pending. Do not extend an original deadline
without a new server assignment. Compare a saved boot ID and boot time when
available; an uncertain wall clock after reboot never makes cached state
usable without a server response. A changed kernel interface index alone
does not invalidate a matching stable link identity.

Persist only MWAN-owned assignments. Initial networkd handover preserves
identifiers and negotiates fresh assignments under MWAN-519. Do not import
networkd lease files. Keep this distinction in acquisition status.

## Tasks

### 1. Persist the protocol recovery record

Add a durable lease record store beside the existing DHCPv4 and DHCPv6
clients. Integrate each client after the store contract passes review.

1. Store the shared connection identity, protocol and association identity,
   assignments, server information, original deadlines, and the reviewed
   configuration compatibility data.
2. Replace saved records atomically at the reviewed persistence boundary.
   Report WAN connection write failures in family operational status and
   history. Record OOB and mainv4 role write failures in structured daemon logs;
   those roles have no WAN connection entry in the operational datastore.
3. Preserve previous valid data after an interrupted replacement. Detect
   truncated, corrupt, incompatible, and expired records explicitly.
4. Retire records deliberately on configuration removal or ownership
   transfer. Do not send a lease release merely because the daemon stops.

### 2. Validate saved state before publishing usability

Integrate owned-connection loading and recovery in the
[addresses module](../../../internal/ifmgr/modules/addresses/addresses.go)
before its DHCPv4 and DHCPv6 clients start. Integrate the OOB and mainv4
role clients with [Daemon.Run](../../../internal/ifmgr/daemon.go). Those roles
currently prune journaled addresses during initialization; defer that prune
until recovery validates or rejects the saved assignment.

1. Match saved records to current configured identity, actual connection,
   protocol settings, and ownership before using them.
2. Apply each protocol's reviewed restart exchange and status handling.
   Keep original renewal, rebinding, preferred, and valid deadlines until a
   successful server response changes them. Account for reboot and uncertain
   wall-clock time without extending an unvalidated lease.
3. Preserve matching kernel state while determining protocol validity where
   the reviewed protocol permits it. Defer owned DHCP reconciliation that
   would remove journaled addresses or routes while recovery is pending.
   Do not publish a usable assignment before validation. Apply the validated,
   rejected, or expired result, then reconcile the owned kernel state.
4. Handle temporary server absence, changed assignments, and failed
   validation with explicit acquisition states. Keep forwarding probes
   separate from protocol validity.
5. Publish validated changes through the same consumers used during ordinary
   acquisition. Update dependent translation and routing once per change.

### 3. Configure storage without changing boot dependencies

Modify the existing service unit only if storage needs a permission it does
not already grant. Use a private directory under `/var/lib/mwan`; owned
connections do not accept the existing `lease-store` configuration field.
Coordinate directory creation with MWAN-521's installation mechanism.

1. Create the lease directory with mode 0700 and records with mode 0600.
   Confirm behavior under the installed service sandbox.
2. Preserve required pre-coldplug startup and existing filesystem ordering.
   Do not add `StateDirectory` without resolving the documented dependency
   cycle through independent boot verification.
3. Preserve unrelated capabilities, sysctl protections, and service paths.

### 4. Verify process and guest recovery

Add daemon restart scenarios to the existing network namespace test suite.
Use the production daemon, real DHCPv4 and DHCPv6 servers, a temporary state
directory, real kernel networking, and downstream packet exchange.

1. Acquire assignments, stop and restart the real process, and inspect server
   packets for the required validation. Expect preserved identifiers and
   original deadlines, matching kernel state, and validated recovery.
2. Restart before T1, between T1 and T2, after preferred expiry, and after
   valid expiry. Repeat with servers absent and with changed assignments.
   Expect no restart to create additional lease lifetime.
3. Interrupt record replacement and test corrupt, expired, and incompatible
   state. Expect an explicit state error or fresh negotiation under the
   reviewed recovery policy, with no unvalidated usable assignment.
4. After the integration PR merges, deploy its release to the isolated
   testbed guest and reboot it. Confirm writable storage, boot progress,
   protocol validation, translated downstream traffic, and both address
   families independently.
5. Perform a fresh migration test with no MWAN record. Expect ordinary
   negotiation with preserved identity and no networkd file import.
6. Repeat DHCPv4 recovery through the real OOB daemon role and a real server.
   Restart with a valid assignment, with changed server replies, and with
   the server absent through expiry. Observe protocol validation, preserved
   client identity and deadlines, the configured OOB routing table, and
   packet delivery. Verify removal of obsolete owned addresses and routes
   after replacement, rejection, or expiry without changing unrelated state.

Use the coordinator's common gates and MWAN-522's merged daemon runner. Keep
the storage, DHCPv4 exchange, DHCPv6 exchange, and daemon integration in
focused PRs. Each protocol PR tests its exchange through a real server. The
integration PR adds real process recovery tests before its merge. Reboot the
installed testbed guest only from a merged revision. Record the executable
commands and independently verify every required scenario.

Run MWAN-522's final assembled suite after all required feature PRs merge.
That final run does not replace this PR's independent acceptance. Process
restart evidence does not establish reboot acceptance.

## Review and handoff

The reviewer independently tests stale identity, configuration changes,
backward and forward clock changes, server absence, interrupted replacement,
unwritable storage, partial association recovery, and expiry during downtime.
Compare saved original deadlines with server exchanges and served state.
Verify that repeated restart cannot preserve an expired assignment.

The code implementer verifies the completed clients and service premise,
then implements only the reviewed persistence and recovery brief. Return
unresolved protocol, clock, or storage decisions to the reviewer. Provide
MWAN-521 the required storage installation changes and record evidence for
the coordinator. Deploy the merged inactive release to the testbed before
production. This slice does not transfer a live provider.
