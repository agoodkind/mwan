# Recover MWAN assignments after restart

## 518-restart: Recover persisted assignments

Recover MWAN-owned DHCP assignments after daemon restart and guest reboot
without extending their original validity or resetting matching interfaces.
Follow the [coordinator](../2026-09-26-link-ownership.md) and
[approved specification](../../interfaces.md).

## Current behavior

[The DHCPv4 client](../../../internal/netif/dhcp.go) keeps `last` and the
active lease in process memory. `StartDHCPClient` starts `run`, which begins
with `acquire`. `LeaseInfo` stores acquisition time and duration but does
not implement persisted restart validation.

[The prefix source](../../../internal/pd/source.go) obtains a prefix through
`DefaultSource.Prefix` without returning lease deadlines. A prefix observed
in a kernel route cannot establish protocol validity after restart.

[The daemon service](../../../cmd/mwan/mwan-ifmgr@.service) already permits
writes to `/var/lib/mwan` through `ReadWritePaths`. It starts before udev
coldplug and networkd, uses `ProtectSystem=strict`, and deliberately omits
`StateDirectory` to avoid boot ordering through local filesystem setup.
Preserve that existing constraint when configuring lease storage.

## Dependencies and contracts

Start this standalone PR after the acquisition PRs have merged, including
MWAN-398 DHCPv4, MWAN-227 delegation, and both MWAN-517 acquisition PRs.
Use MWAN-516's identities and assignment contract and MWAN-523's kernel
observations. The shared DHCPv6 lifecycle remains the sole owner of IA_NA
and IA_PD recovery.

The reviewer must approve the persisted format, atomic replacement and
durability requirements, configuration compatibility rules, clock handling,
and each protocol's restart validation. DHCPv4 and DHCPv6 recovery require
different exchanges. A surviving address or prefix is insufficient proof.

Persist only MWAN-owned assignments. Initial networkd handover preserves
identifiers and negotiates fresh assignments under MWAN-519. Do not import
networkd lease files. Keep this distinction in acquisition status.

## Tasks

### 1. Persist the protocol recovery record

Create the proposed
[internal/netif/leasestore.go](../../../internal/netif/leasestore.go).
Integrate the existing DHCPv4 client and the proposed
[internal/netif/dhcpv6.go](../../../internal/netif/dhcpv6.go) established by
MWAN-227. These new paths are implementation proposals, not current APIs.

1. Store the shared connection identity, protocol and association identity,
   assignments, server information, original deadlines, and the reviewed
   configuration compatibility data.
2. Replace saved records atomically at the reviewed persistence boundary.
   Surface write failures through acquisition status and operation history.
3. Preserve previous valid data after an interrupted replacement. Detect
   truncated, corrupt, incompatible, and expired records explicitly.
4. Retire records deliberately on configuration removal or ownership
   transfer. Do not send a lease release merely because the daemon stops.

### 2. Validate saved state before publishing usability

Integrate loading and recovery with
[Daemon.Run](../../../internal/ifmgr/daemon.go) and the completed client APIs.

1. Match saved records to current configured identity, actual connection,
   protocol settings, and ownership before using them.
2. Apply each protocol's reviewed restart exchange and status handling.
   Keep original renewal, rebinding, preferred, and valid deadlines. Account
   for reboot and uncertain wall-clock time without extending a lease.
3. Preserve matching kernel state while determining protocol validity where
   the reviewed protocol permits it. Publish usable assignments only after
   the required validation. Remove expired owned state and negotiate again.
4. Handle temporary server absence, changed assignments, and failed
   validation with explicit acquisition states. Keep forwarding probes
   separate from protocol validity.
5. Publish validated changes through the same consumers used during ordinary
   acquisition. Update dependent translation and routing once per change.

### 3. Configure storage without changing boot dependencies

Modify the existing service unit only where the reviewed storage contract
requires it. Coordinate the configured path and creation permissions with
MWAN-521's existing installation mechanism.

1. Use the approved writable state location and restrictive record
   permissions. Confirm behavior under the installed service sandbox.
2. Preserve required pre-coldplug startup and existing filesystem ordering.
   Do not add `StateDirectory` without resolving the documented dependency
   cycle through independent boot verification.
3. Preserve unrelated capabilities, sysctl protections, and service paths.

### 4. Verify process and guest recovery

Create the proposed
[cmd/mwan/lease_restart_netns_test.go](../../../cmd/mwan/lease_restart_netns_test.go).
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
4. Reboot the isolated acceptance guest under the installed service. Confirm
   writable storage, boot progress, protocol validation, translated
   downstream traffic, and both address families independently.
5. Perform a fresh migration test with no MWAN record. Expect ordinary
   negotiation with preserved identity and no networkd file import.

Use the coordinator's common gates and MWAN-522's standalone daemon runner.
Its bootstrap must merge after the shared model and before protocol feature
acceptance. Add the recovery tests and server scenarios in this PR. Record
the executable commands and independently verify these scenarios before
merging this PR. No inspected existing command covers the proposed
real-process recovery suite or guest reboot.

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
the coordinator. This slice does not authorize live deployment or migration.
