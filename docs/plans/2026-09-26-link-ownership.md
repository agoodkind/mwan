# Migrate interface ownership from networkd to MWAN

Implement all remaining MWAN-305 work against the
[interface ownership specification](../interfaces.md). The approved design
uses complete-connection transfers, fresh DHCP negotiation during migration,
persisted MWAN leases for restart, and kernel router discovery and SLAAC.
SLAAC is automatic IPv6 address configuration from router advertisements.

This plan covers implementation in MWAN, integration in Configs, test
infrastructure, staged deployment, reverse transfer, and final acceptance.
Implementation completion alone does not close the epic.

## Establish the starting point

The inspected MWAN baseline is `03dd43a`; the Configs baseline is `52056ec`.
MWAN-491's renderer is an intermediate stage. MWAN-208 and MWAN-379 are
completed prerequisites.

The network loader compiles link settings only for provider entries. The
served configuration represents the internal interface principally by name.
The model must also configure physical parents, internal interfaces, and
management interfaces before networkd can retire.

The existing DHCPv4 client starts acquisition on process startup and expires
a lease after a failed renewal. It needs configured identity, renewal and
rebinding behavior, and persisted restart recovery. The prefix source
observes networkd or kernel state and returns a prefix without its deadlines.
Router solicitation support alone does not implement the approved kernel
autoconfiguration contract.

## Sequence the work

| Stage | Ticket | Required result |
| --- | --- | --- |
| Define shared configuration and state | MWAN-516 | Compile all interface roles and publish ownership, acquisition, and history. |
| Observe kernel state | MWAN-523 | Track actual device identities, address validity, and route ownership. |
| Implement links | MWAN-397 | Apply physical settings and VLAN dependencies. |
| Implement addresses and IPv4 acquisition | MWAN-398 | Reconcile static addresses, DHCPv4, and assigned routes. |
| Implement delegation | MWAN-227 | Acquire and renew delegated prefixes with explicit lifetimes. |
| Complete IPv6 acquisition | MWAN-517 | Configure kernel autoconfiguration and manage DHCPv6 interface assignments. |
| Recover leases | MWAN-518 | Recover valid assignments after daemon restart. |
| Repair deleted routes | MWAN-505 | Restore owned routes promptly and verify downstream forwarding. |
| Integrate deployment | MWAN-521 | Render complete interface configuration and enforce exclusive transfer. |
| Prepare repeatable acceptance | MWAN-522 | Exercise real protocols and downstream traffic before handover. |
| Transfer the first connection | MWAN-519 | Prove exclusive handover and reverse transfer. |
| Transfer remaining interfaces | MWAN-399 | Migrate other providers, internal networking, and management last. |
| Retire networkd | MWAN-400 | Remove obsolete dependencies after replacement acceptance. |
| Verify the final testbed | MWAN-401 | Prove operation without networkd after restart, reboot, and failure. |
| Verify production | MWAN-520 | Record each promoted phase and final production acceptance. |

MWAN-397, MWAN-398, MWAN-227, MWAN-517, MWAN-521, MWAN-522, and MWAN-523 use
MWAN-516's contracts. MWAN-398 also needs MWAN-397. MWAN-227 and MWAN-517
share one DHCPv6 lifecycle. MWAN-518 follows the acquisition implementations.
MWAN-505 can proceed independently.

MWAN-519 requires completed acquisition, restart recovery, route repair,
deployment integration, acceptance tests, reliable observation, and MWAN-341
startup protection.
MWAN-399 follows first-connection acceptance. MWAN-400 follows all interface
transfers. MWAN-401 follows retirement. MWAN-520 records production results
after every phase and closes only after final production acceptance.

## Implement the shared model under MWAN-516

Modify [the network loader](../../internal/networkjson/networkjson.go),
[the served configuration](../../internal/wanconfig/tree.go),
[the active schema](../../internal/yangpub/schema/goodkind-mwan-steering@2026-09-26.yang),
and [the link specification](../../internal/networkd/spec.go).

1. Compile interface definitions independently of provider membership.
   Preserve the provider list as consumers of shared interface definitions.
   Model required non-provider parents, internal interfaces, and management.
2. Separate configuration from unit rendering without copying the same fields
   into parallel types. Add explicit ownership for the migration period.
3. Represent supported matching, address acquisition, kernel IPv6 policy,
   bridge dependencies, client identities, and lease storage configuration.
   Validate unsupported free-form networkd settings before any mutation.
4. Define the assignment interface used by clients and consumers. Include
   interface identity, family, assignment kind, address or prefix, source,
   renewal and expiration deadlines, and validity. Keep configured intent
   separate from acquired and installed state.
5. Publish owner, acquisition state, apply result, and existing family
   readiness. Record transition history with timestamp, failed operation or
   dependency, and plain reason through the existing logging and served
   state. Preserve history across ordinary daemon restart using existing
   persistent logging; keep any served recent-history collection bounded.
6. Define removal for each owned object. Define observation mode as read-only.
   Preserve schema errors and provider-local rejection behavior.

Verify through the production loader and operational interface. Accept
multiple providers plus non-provider interfaces. Reject conflicting ownership
and unsupported settings before kernel mutation. Demonstrate a recorded
failure and recovery. Do not introduce management writes or hot reload.

## Implement link management under MWAN-397

Extend [kernel state operations](../../internal/netif/state.go) and
[daemon startup](../../cmd/mwan/ifmgr.go). Add the link-management module under
the existing interface-manager module structure.

1. Match devices unambiguously and apply configured name, MAC, MTU, and enabled
   state. Reject ambiguous matches before changing a device.
2. Create VLANs after their parents exist. Preserve parents used by other
   interfaces. Preserve actual guest link types. The current `enmwanbr0` is
   a virtio transit interface attached to a hypervisor bridge. Its name does
   not require creating a Linux bridge inside the guest.
3. Start and stop per-connection work independently. Extend current role
   plumbing without restricting the already supported provider set.
4. Reconcile creation, changes, and removal only for owned objects. On restart,
   retain matching state and recover observation subscriptions.
5. Keep migrated-interface naming compatible with boot ordering and udev.
   Do not enable direct writes while networkd owns that connection.

Verify through the daemon with connected Linux namespaces, real VLANs,
packet exchange, removal, and restart. An absent provider must not
prevent another provider from working. Repeat with different interface names.

## Observe actual kernel state under MWAN-523

Extend [the interface monitor](../../internal/netif/monitor.go),
[the shared state store](../../internal/wanstate/wanstate.go), and
[live-state publication](../../cmd/mwan/wanconfig_livestate.go).

1. Resolve and track the actual device index through appearance, removal,
   and recreation. Never attribute another interface's event to an absent
   configured interface.
2. Include the address flags, origins, and lifetimes needed to distinguish
   tentative, usable, deprecated, and expired state.
3. Include route table and protocol information needed to attribute owned
   routes and distinguish acquisition from policy routing.
4. Recover observations after restart and preserve existing role consumers.
   Coordinate route deletion events with MWAN-505.

Start the real daemon with a configured device absent. Modify an unrelated
device, then create and replace the intended device with a different index.
Verify the operational interface, kernel state, and downstream packets agree.

## Implement addressing and DHCPv4 under MWAN-398

Extend [the DHCPv4 client](../../internal/netif/dhcp.go) and
[WAN routing](../../internal/ifmgr/modules/wanroutes/wanroutes.go), using the
shared model and kernel operations from the preceding tasks.

1. Apply configured addresses and main-table routes. Distinguish locally
   assigned mapping addresses from prefixes routed by the ISP.
2. Transfer on-link mapped IPv4 address installation from WAN routing to the
   address manager at the exclusive ownership boundary.
3. Preserve configured DHCP identity. Implement server renewal and rebinding
   timers, rejection, expiration, changed addresses, and changed gateways.
   A renewal timeout must not revoke a valid lease.
4. Apply configured route metrics and required accepted lease options.
   Preserve the configured resolver policy; inventory existing DNS and route
   options before enabling a replacement client.
5. Remove overlapping provider-default writers. Keep policy tables and rules
   under WAN routing, and recognize kernel-generated connected routes.
6. Remove expired or withdrawn owned assignments and update their consumers.
   Preserve current translation behavior and OOB users of the shared client.
   Update [the OOB lease consumer](../../internal/ifmgr/modules/oobv4/oobv4.go)
   so changed or expired leases remove obsolete owned addresses as well as
   their routes.

Verify static addressing and actual DHCPv4 negotiation through the daemon,
with a real server and packet delivery. Exercise renewal, rebinding, expiry,
changed gateway, and mapped-address replies. Use
[RFC 2131](https://www.rfc-editor.org/rfc/rfc2131.html#section-4.4.5)
for the lifecycle implementation.

## Implement delegation under MWAN-227

Extend [the delegated-prefix source](../../internal/pd/source.go) with the
DHCPv6 client and assignment state. Preserve the translation consumer boundary.

1. Negotiate delegation using configured DUID and IAID identifiers and prefix
   hints. Support the configured solicitation behavior, including operation
   without an initial router advertisement where required.
2. Coordinate interface-address and prefix assignments through one DHCPv6
   lifecycle per connection. Keep their identities and deadlines distinct.
3. Implement renew, rebind, expiration, and prefix changes. Publish preferred
   and valid lifetimes without inferring validity from a surviving route.
4. Preserve configured use of the delegated prefix on the link and any required
   downstream announcement behavior identified in the inventory.
5. Update translation and source routing when the live delegation changes.
   Keep ordinary routed IPv6 prefixes independent of DHCP delegation.
6. Select the current owner's prefix source explicitly during migration.
   Retain networkd observation only for connections networkd still owns.

Verify real server assignments, changed prefix lengths, renewal, server loss,
expiry, and translated downstream traffic. Prove that configured prefix
values can change without a binary change.

## Complete IPv6 acquisition under MWAN-517

Integrate kernel observation with [the existing router solicitation support](../../internal/netif/ra.go)
and the shared acquisition lifecycle. Reuse the sysctl boundary in
[host IPv6 policy](../../internal/ifmgr/modules/hostipv6policy/hostipv6policy.go).

1. Configure Linux advertisement acceptance, automatic addressing, default
   router learning, metrics, and forwarding behavior per interface. Verify the
   installed kernel's controls in the test environment.
2. Observe tentative, usable, deprecated, and expired addresses and router
   lifetimes. A tentative address is still undergoing duplicate-address
   detection; it must not establish forwarding readiness.
3. Preserve kernel ownership of automatic-address lifetimes. Do not repeatedly
   recreate an address that the kernel legitimately removed.
4. Implement DHCPv6 interface-address assignment alongside delegation.
   Distinguish router discovery, SLAAC, DHCPv6 addresses, and delegated prefixes
   in published state and protocol start conditions.
5. Account for configured DNS acceptance and required advertisement options.
   Preserve existing behavior without adding a new resolver service.
6. Verify failure and recovery independently for IPv4 and IPv6. Report a
   demonstrated kernel incompatibility before proposing a design change.

Verify actual router advertisements and DHCPv6 replies. Cover forwarding
enabled, duplicate detection, router expiration, prefix deprecation and
expiration, and interface-address plus delegation assignments together.
The implementation must use kernel SLAAC; a second userspace SLAAC engine is
outside the approved scope.

## Recover leases under MWAN-518

Add persistent assignment storage beside the acquisition implementation.
Configure its writable location through the existing configuration and
service-installation mechanisms. Update the
[installed daemon service](../../cmd/mwan/mwan-ifmgr@.service) for the required
state directory and kernel controls without removing unrelated protections.

1. Persist protocol identity, assignments, server information required for
   restart, and original deadlines with atomic file replacement.
2. On daemon restart, match saved state to current interface identity and
   configuration. Apply each protocol's validation before publishing usable
   assignments. Do not extend a deadline because the process restarted.
3. Handle expired, incomplete, or incompatible saved state with explicit
   acquisition status and protocol negotiation.
4. Preserve valid matching kernel state. Retire state deliberately on
   configuration removal or ownership transfer without releasing leases
   automatically on every ordinary process stop.
5. Verify both daemon restart and guest reboot, including temporary server
   absence and changed assignments.

Use actual servers, a temporary state directory, and real process restarts.
Confirm status, packet delivery, and dependent translation. Do not implement
networkd lease-file import.

## Repair deleted routes under MWAN-505

Extend WAN routing and its route-event subscription.

1. Trigger reconciliation when an owned route or rule is deleted, including
   internal return routes in provider tables.
2. Observe all interfaces that contain those owned routes.
3. Avoid treating self-generated events as repeated full reconfiguration.

Delete an owned return route during downstream packet exchange. Verify prompt
restoration without waiting for the periodic reconcile interval. Confirm
unrelated routes remain unchanged and record packet interruption.

## Integrate Configs under MWAN-521

Update inventory rendering and deployment in the Configs repository. Preserve
one network configuration model across both repositories.

| Change | Configs source to modify |
| --- | --- |
| Render all interface roles and ownership. | Extend [the network document template](https://github.com/agoodkind/configs/blob/main/mwan/config/network.json.j2). |
| Configure both environments. | Update [production inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_servers.yml) and [testbed inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_suburban_servers.yml). |
| Discover management and transit MAC addresses before rendering. | Reorder [runtime discovery](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/mwan-vm/discover-runtime-network.yml) within [the deployment playbook](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-mwan.yml). |
| Transfer management and transit settings. | Replace applicable behavior in [management networking](https://github.com/agoodkind/configs/blob/main/ansible/templates/vm/10-mgmt.network.j2) and [transit networking](https://github.com/agoodkind/configs/blob/main/mwan/networkd/40-mwanbr.network.j2). Account for their adjacent naming files separately. |
| Replace post-authentication networkd operations. | Update [AT&T activation](https://github.com/agoodkind/configs/blob/main/mwan/scripts/bringup-att-vlan.sh) and [its service](https://github.com/agoodkind/configs/blob/main/mwan/services/bringup-att-vlan.service). |
| Preserve the existing authentication signal. | Integrate with [the supplicant action](https://github.com/agoodkind/configs/blob/main/mwan/scripts/wpa-action.sh). |
| Remove conflicting automatic-address settings. | Update [gateway sysctl configuration](https://github.com/agoodkind/configs/blob/main/mwan/config/sysctl-mwan.conf.j2). |
| Preserve release and checkout verification. | Retain [merged-checkout enforcement](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/require-merged-checkout.yml) and [release verification](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/verify-mwan-release.yml). |

1. Render complete parent, provider, internal, and management definitions.
   Include explicit ownership, client identities, and kernel IPv6 policy.
2. Validate the rendered document with the release's production loader before
   changing network state. Preserve merged-commit enforcement.
   Move management and transit discovery before rendering; the current play
   discovers those identities after rendering the network document.
3. Stage the replacement configuration and release before stopping old writes.
   Coordinate daemon startup, networkd, udev, and firewall protection.
4. Give the daemon the required capabilities and persistent lease directory.
   Preserve authentication services and unrelated host networking.
5. Implement exclusive forward and reverse transfer using the supported
   deployment workflow. Keep networkd enabled for unmigrated interfaces.
6. Preserve Ansible reconnection around operations that can interrupt access
   and hypervisor-local recovery when guest access fails.
7. Verify rerunning the deployment leaves matching state intact. Verify
   rollback restores compatible configuration, release, and one active owner.

Preserve management DNS/domain settings and the transit return route.
Translate AT&T parent addressing, VLAN creation, and post-authentication
DHCP activation. The existing supplicant signal uses an authenticated marker;
preserve its CONNECTED and DISCONNECTED behavior while replacing the consumer
that invokes networkd. Leave certificates and authentication implementation
unchanged.

Resolve the existing sysctl conflict explicitly: Webpass inventory accepts
router advertisements, while the static sysctl template disables RA and
autoconfiguration there. Kernel acquisition must apply the intended policy
after early boot settings and preserve the firewall startup requirement.

The failover LXC uses a separate networking configuration. Verify its service
continues during gateway changes. Do not retire its network manager as part
of the primary gateway migration.

Build this work before MWAN-519. Keep deployment changes in focused Configs
PRs and application changes in MWAN PRs. Do not add shell-script wrappers.

## Prepare acceptance under MWAN-522

Extend existing simulators and downstream client declarations. Keep reusable
test infrastructure in OpenTofu and simulator behavior in its existing
configuration.

Modify [DHCPv4 simulator configuration](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/kea-dhcp4.conf.j2),
[DHCPv6 simulator configuration](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/kea-dhcp6.conf.j2),
and [router advertisements](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/radvd.conf.j2).
Activate them through [the existing simulator task](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/deploy-testbed-isp-lxc.yml).
Preserve [delegation return routing](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/pd-route.service.j2)
and [declared testbed guests](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/vms.tf).

1. Preserve current provider scenarios. Add required short-lease, changed
   assignment, combined DHCPv6 address/delegation, and router-expiry scenarios
   through configuration.
   Configure explicit test client identities and assert them in actual server
   or packet observations. Preserve or update the link-local identity used
   by the simulator's delegation return route.
2. Run the real daemon with real DHCP servers, advertisements, and kernel
   networking. Exercise the production loader and served model.
3. Provide repeatable downstream packet checks without an OOB bypass.
   Exercise both families and inbound mapping replies.
4. Measure new-connection balancing using traffic variation appropriate to
   the configured hash mode. Observe provider traffic at simulator ingress.
5. Exercise absent-provider startup, route deletion, process restart, reboot,
   and exclusive reverse transfer.
6. Verify VLAN behavior with virtual links. Record that simulated AT&T
   service does not establish live authentication compatibility.

Document executable commands and expected results as part of the test work.
Required privileged tests must run explicitly. Do not replace missing
privileges with fake dependencies or count skipped tests as acceptance.

## Transfer the first connection under MWAN-519

1. Finish and merge all components required by the first connection. Use the
   existing Webpass simulator for the first transfer; validate DHCPv4 with
   the dynamic-provider simulator before promotion.
2. Establish a downstream traffic baseline and another usable provider.
3. Execute exclusive ownership transfer for the complete connection.
   Negotiate assignments with preserved client identities.
4. Verify addresses, routes, translation, readiness, and downstream packets
   before restoring selection for new traffic.
5. Execute the reverse transfer and prove one active owner at each stage.
6. Repeat forward transfer, daemon restart, and reboot. Record interruption
   and any changed assignments.
7. Complete the phase's production acceptance under MWAN-520 before starting
   the next live migration phase.

## Transfer remaining interfaces under MWAN-399

1. Repeat the accepted procedure for remaining dynamic providers.
2. Transfer the VLAN and authentication-dependent connection. Preserve the
   Configs authentication service and verify its actual ready signal.
3. Transfer internal networking and return routes. Verify internal BGP and
   downstream forwarding separately from management access.
4. Transfer management last with verified hypervisor console recovery.
5. Record every remaining networkd-owned object before requesting retirement.

Complete each phase's testbed checks and production acceptance. Do not treat
future ISP circuits or unknown production BGP values as implementation
prerequisites.

## Retire networkd under MWAN-400

1. Prove that every required interface has a replacement owner.
2. Remove obsolete rendering paths, generated units, networkd lease readers,
   service dependencies, and temporary migration configuration.
3. Account for udev naming files separately from stopping networkd.
4. Preserve a tested recovery release and matching configuration.
5. Run the final acceptance tickets before closing the epic.

Inventory current files before deletion. Keep completed MWAN-491 and
cancelled historical tickets unchanged. Update affected operator instructions
with the actual replacement procedure.

## Verify final testbed and production under MWAN-401 and MWAN-520

1. Deploy the exact merged release and configuration to the testbed.
2. Run the full MWAN-522 battery after cold boot and ordinary restart with
   networkd disabled.
3. Record protocol recovery, mappings, balancing, internal BGP, management,
   detailed failure history, and downstream interruption.
4. Promote only the accepted release and configuration to production.
5. Observe downstream traffic throughout deployment and recovery.
6. Record the result of each production phase and the final system.

Run `make docker-make TARGETS="check test"` for MWAN code changes on macOS,
plus the required privileged acceptance suite. Run Configs deployments
through `./configsctl deploy` with the applicable playbook and target limit.
Do not invoke Ansible directly or deploy an unmerged branch.

## Keep implementation and deployment evidence separate

Use focused PRs. MWAN-398 should separate static ownership from DHCPv4
lifecycle changes; MWAN-517 should separate kernel autoconfiguration from
DHCPv6 address assignment. Split Configs rendering and handover mechanics
when each change is independently reviewable. Build the complete required
code before starting live cutovers.

For each PR and deployment phase, record the ticket, commit, configuration
revision, owner before and after, commands, observed results, and remaining
work in the [execution ledger](2026-09-26-link-ownership-ledger.md).
Record implementation, merge, testbed
acceptance, and production acceptance separately. Update the ledger before
context handoff. Keep tickets open until their own acceptance is demonstrated.

MWAN-507 remains separate. Define compatible shared contracts under MWAN-516;
do not implement external BGP or tunnels in this epic. Preserve completed
translation and firewall behavior, Configs authentication, and the deferred
scope listed in the specification.
