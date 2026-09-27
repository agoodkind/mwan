# Transfer interfaces and verify retirement

This operational plan implements the
[migration coordinator](../2026-09-26-link-ownership.md) and approved
[interface specification](../../interfaces.md). High intelligence cutover agents
own live execution and recovery. Code implementers write settled changes and
run local validation. Independent reviewers verify changes, required checks,
approvals, and acceptance evidence before activation.

Every live phase requires current authorization for its exact target, merged
application and Configs revisions, a compatible release and configuration,
the executable MWAN-522 command manifest, and tested recovery. Operational
phases are not placeholder PRs. Prepare a separate reviewed activation
configuration PR when a phase requires an ownership change.

Use [the gateway playbook](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-mwan.yml)
with [testbed inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_suburban_servers.yml)
and [production inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_servers.yml).
The verified Configs root commands are:

```sh
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
./configsctl deploy deploy-mwan --limit mwan_servers
```

Run these as separate testbed and production operations. Complete each phase's
testbed acceptance and then MWAN-520 production acceptance before starting
the next live phase. Do not invoke Ansible directly or deploy an unmerged fix.
Check-mode deployment also belongs to the cutover agent.

Preserve Ansible reconnection and hypervisor-local verdict collection and
recovery. Record a healthy downstream baseline, exact target identity,
compatible recovery pair, and working hypervisor console before each transfer.
A missing verdict does not prove rollback is safe; inspect the guest and
watchdog through the verified recovery channel.

## 519-first-connection: Prove complete and reverse transfer

### Establish the prerequisites

Require live acceptance of MWAN-524's Astound baseline repair before any
migration cutover. Preserve its managed IPv4-only connection during transfer.

Require completed MWAN-516, MWAN-523, MWAN-397, MWAN-398, MWAN-227, MWAN-517,
MWAN-518, MWAN-505, MWAN-521, MWAN-522, and MWAN-341 protection. Build every
required component before transfer. Require successful privileged acceptance
and independent verification of the command manifest and recovery pair.

Use the existing Webpass simulator for the first connection. Validate DHCPv4
with the dynamic-provider simulator before production promotion. Verify
another usable provider and baseline downstream traffic.

### Execute the testbed transfer

1. Resolve connection, guest, hypervisor, owner, and release identity. Capture
   addresses, routes, clients, ownership, downstream IPv4 and IPv6 packets,
   and the manifest's accepted interruption bound.
2. Stage the merged release and validated configuration using MWAN-521's
   procedure. Exclude the selected connection from new traffic.
3. Stop the previous owner's writes and clients. Verify exclusive release of
   link, address, route, and acquisition responsibilities before replacement.
   Preserve shared parents and unrelated interfaces.
4. Activate the complete connection. Preserve DHCP identities and negotiate
   assignments afresh. Do not import networkd leases.
5. Require correct assignments, lifetime state, routes, translation, readiness,
   downstream packets, and inbound mapping replies before restoring selection.
6. Execute reverse transfer with one active owner at each step. Prove the
   previous owner restores downstream traffic with compatible configuration.
7. Repeat forward transfer, daemon restart, and guest reboot. Record actual
   interruption and renumbering. Verify persisted recovery separately from
   fresh negotiation at initial transfer.

### Stop, recover, and hand off

Stop on conflicting writers, failed ownership release, missing protection,
invalid assignments, packet failure, interruption beyond the accepted bound,
or unavailable recovery access. Keep the failed connection excluded from new
traffic. The cutover agent performs the reviewed reverse procedure or
hypervisor recovery appropriate to the observed failure.

Record exact commits, release and configuration identity, commands, owner
transitions, client identities, packet results, unaffected-provider traffic,
failure history, interruption, and recovery. Complete this phase's MWAN-520
production acceptance before any next live migration phase.

## 399-remaining-connections: Transfer providers, transit, and management

### Establish the prerequisites

Require accepted first transfer, reverse transfer, and its production phase.
Reuse the MWAN-521 mechanism and MWAN-522 manifest. The reviewer verifies each
phase's release compatibility and previous phase evidence. Future ISP circuits
and unknown production BGP values do not block implementation of approved
contracts.

### Execute ordered phases

1. Inventory each continuing provider's device, parent, owner, client identity,
   options, and recovery requirements. Verify another usable provider and
   downstream baseline before each transfer.
2. Transfer each complete provider using the accepted exclude, release,
   acquire, verify, and restore-selection sequence. Run the required reverse
   and recovery checks. Accept that phase in testbed and production.
3. Keep AT&T under existing Configs and networkd management until circuit
   retirement. Verify generic VLAN support in testbed without adding 802.1X
   integration or transferring the authentication-dependent connection.
4. Transfer internal networking after continuing providers pass. Preserve
   both transit addresses, forwarding, and the return route through the
   OPNsense edge. Preserve the actual virtio link type.
5. Verify internal BGP and downstream forwarding separately from management.
   Delete an owned return route during traffic and verify repair.
6. Transfer management last. Verify hypervisor console recovery immediately
   before activation. Preserve its static address, DNS/domain configuration,
   udev naming, and deployment reconnection and recovery behavior.
7. Verify the separate failover LXC continues operating after every phase.
   Keep its required network manager outside the gateway retirement.

### Stop, recover, and hand off

Stop on conflicting ownership, changed unrelated interfaces, missing return
routes, packet failure, unavailable console recovery, or excessive interruption.
Apply the reviewed reverse transfer and verify the restored owner and packets
before resuming. An SSH-only result does not establish internal-network
acceptance or authorize management transfer.

Record merged commits, configuration, owner transitions, commands, packets,
balancing, interruption, and recovery for each phase. Inventory every remaining
networkd-owned object and dependency before MWAN-400. Keep global networkd
operation while AT&T or another required interface depends on it.

## 400-retirement: Classify, remove, review, and activate

### Inventory before assigning deletion

A high intelligence agent performs read-only inventory after MWAN-399.
Require confirmed AT&T circuit retirement and accepted replacement ownership
for every required interface before global networkd retirement. Do not assign
an open-ended deletion task to a code implementer.

Inspect [the networkd renderer](../../../internal/networkd/spec.go),
[the prefix source](../../../internal/pd/source.go),
[the daemon unit](../../../cmd/mwan/mwan-ifmgr@.service), and current callers.
Inspect the Configs deployment and inventories identified above and
[AT&T deployment](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/mwan-vm/att-8021x.yml).
These are inspection starting points, not unconditional deletion targets.

1. Record exact code, generated files, units, processes, lease readers,
   naming rules, and remaining consumers in each environment. Identify each
   candidate's replacement owner and acceptance evidence.
2. Confirm AT&T is no longer required and no active interface depends on its
   authentication or networkd configuration. Preserve legacy behavior until
   that confirmation.
3. Classify udev naming separately from networkd startup. Exclude required
   failover LXC and unrelated host-network dependencies.
4. Produce a bounded deletion brief with exact files, symbols, artifacts,
   service dependencies, and replacement edits. Preserve reusable credentials.
   ISP service cancellation is outside this task.

### Implement and review bounded removal

Assign only the settled brief to a code implementer. Prepare focused MWAN and
Configs retirement PRs according to the coordinator. Remove obsolete
renderers, lease readers, generated-unit installation, service dependencies,
temporary ownership configuration, and retired AT&T setup only where the
inventory proves replacement or retirement.

Preserve generic VLAN and DHCP support, tests for their behavior, and a
compatible recovery release and configuration. Read affected operator
instructions and update the actual replacement procedure. Delete tests for
removed behavior. Keep completed MWAN-491 and cancelled historical tickets
unchanged.

Run `make docker-make TARGETS="check test"` for MWAN code changes on macOS.
Run real daemon startup, restart, acquisition, and packet tests without
networkd in the privileged MWAN-522 environment. Skipped cases do not prove
replacement acceptance.

The independent reviewer checks every deletion against inventory and remaining
consumers, verifies the recovery pair, and confirms checks and approvals.
Stop on an unclassified candidate, a live AT&T dependency, missing naming
replacement, or an interface without accepted ownership.

### Activate and hand off retirement

Only the high intelligence cutover agent activates merged retirement changes
after current authorization and confirmed AT&T retirement. Run final testbed
acceptance under MWAN-401 before production activation under MWAN-520. On loss
of required service, stop promotion and restore the reviewed compatible pair
through the proven recovery procedure.

Record the classified inventory, deletion brief, reviewed merged revisions,
AT&T retirement confirmation, active-unit and client observations, packets,
and recovery evidence. Separate implementation from activation and acceptance.

## 401-testbed: Verify the final gateway without networkd

### Establish the prerequisites

Require accepted MWAN-399 transfers, reviewed MWAN-400 retirement, and
MWAN-522's complete command manifest and successful privileged tests. The
reviewer verifies exact revisions, retirement inventory, approvals, and
recovery compatibility. Every required testbed interface must have an
accepted owner without necessary networkd dependencies.

### Execute final acceptance

1. Record baseline downstream traffic, guest and hypervisor identity, release,
   configuration, console recovery, and the existing deployment snapshot
   and verdict mechanism.
2. Activate the merged retirement configuration using the testbed deployment
   command. Verify networkd is disabled and inactive and obsolete clients
   and writers do not run.
3. Run the full MWAN-522 manifest after ordinary daemon restart and cold guest
   boot. Verify the startup firewall before dependent network operations.
4. Exercise real Kea DHCPv4, DHCPv6 addresses and delegation, radvd, kernel
   SLAAC, lifetime expiry, changed assignments, and persisted recovery.
   Verify delegated-prefix return routing and its stable link-local next hop.
5. Verify absent-provider startup, link recreation, route deletion and repair,
   independent family recovery, and another provider's continued service.
6. Verify downstream IPv4 and IPv6 without OOB bypass, inbound mapping replies,
   translation, and new-connection balancing at simulator ingress.
7. Verify internal BGP, transit return routes, management, and the separate
   failover LXC. Preserve generic VLAN and routed-static scenarios.
8. Compare served ownership, assignments, deadlines, readiness, apply failures,
   and persistent transition history with kernel and packet observations.
   Record interruption for every restart and failure.

### Stop, recover, and hand off

Stop on an obsolete active writer, missing required interface, protocol or
packet failure, incorrect balancing, missing failure history, or excessive
interruption. Missing privileges and skipped cases also block acceptance.
Restore the proven release and configuration, then verify exclusive ownership
and downstream service. Return defects to implementation and review before
activating a new merged release.

Hand off merged commits, installed identity, rendered hashes, complete command
results, captures, active-client observations, boot identity, and recovery
results. Promote only this accepted release and compatible configuration.

## 520-production: Accept every phase and the final system

### Establish phase-specific prerequisites

Run this phase after the first connection, each remaining-interface phase,
and final retirement. Require the same phase's testbed acceptance for the
exact merged release and compatible Configs revision, executable manifest,
checks, approvals, and current production authorization. Final retirement
also requires confirmed AT&T retirement and MWAN-401 acceptance.

The reviewer verifies scope, release identity, configuration differences,
testbed evidence, and recovery compatibility. The high intelligence cutover
agent owns production deployment, observation, and recovery.

### Execute production acceptance

1. Resolve the current guest, hypervisor, connections, owner state, and phase.
   Verify console recovery and the previous compatible release pair. Record
   healthy downstream and deploy-gate baselines.
2. Verify the release pin matches testbed acceptance and configuration changes
   reflect production inventory. Preserve merged-checkout and release checks.
3. Deploy through the bounded production command. Observe downstream traffic
   throughout exclusion, release, acquisition, verification, and eligibility
   restoration. Preserve reconnection and hypervisor-local recovery.
4. Verify clients, ownership, assignment deadlines, addresses, routes,
   translation, family readiness, inbound mappings, and downstream IPv4 and
   IPv6 without an OOB bypass.
5. Verify new-connection balancing with traffic variation appropriate to the
   hash mode. Record provider observations and unaffected-provider service.
   Verify internal BGP and management when their dependencies change.
6. Run production-safe restart and recovery checks from the reviewed manifest.
   Record interruption, renumbering, and failure history. Execute destructive
   simulator faults only against their approved isolated or testbed targets.
7. For final retirement, verify networkd and obsolete clients remain inactive
   after ordinary restart and cold boot. Verify every required interface,
   internal BGP, mappings, balancing, management, and the separate failover LXC.

### Stop, recover, and close only after final acceptance

Stop on release mismatch, unhealthy baseline, conflicting ownership, failed
packets or mappings, unavailable management recovery, or excessive interruption.
Keep failed connections excluded from new traffic. Execute the reviewed
reverse transfer or hypervisor recovery appropriate to the failure and verify
restored service. Preserve AT&T legacy dependencies until confirmed retirement.

Record phase, merged commits, installed release, configuration hash, commands,
owners, actual packets, readiness, lifetime state, interruption, and recovery.
Distinguish implementation, merge, testbed acceptance, and production acceptance
in the execution ledger. Complete the current phase record before the next
live phase. Keep MWAN-520 open until final production acceptance after MWAN-401.
Close the epic only after every required interface operates without networkd
and the evidence includes restart, boot, and recovery results.
