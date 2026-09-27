# Migrate interface ownership from networkd to MWAN

Implement MWAN-305 against the [interface ownership specification](../interfaces.md).
That specification defines behavior, scope, and unresolved design choices.
This plan defines ticket order, source changes, and verification.

The inspected baseline is `03dd43a`. MWAN-491's renderer is an intermediate
stage. MWAN-208 and MWAN-379 are completed prerequisites. Preserve completed
work and cancelled scope when updating the epic.

## Sequence the tickets

| Stage | Ticket | Required result |
| --- | --- | --- |
| Define the shared contract | MWAN-516 | Validate ownership and publish acquisition state and detailed failure history. |
| Implement links | MWAN-397 | Apply physical settings and VLAN dependencies. |
| Implement addresses and IPv4 acquisition | MWAN-398 | Reconcile static addresses, DHCPv4, and main-table routes. |
| Implement delegation | MWAN-227 | Acquire and renew delegated prefixes with explicit lifetimes. |
| Complete IPv6 acquisition | MWAN-517 | Manage router discovery, automatic addresses, and DHCPv6 interface assignments. |
| Recover leases | MWAN-518 | Recover valid assignments after daemon restart. |
| Repair deleted routes | MWAN-505 | Restore owned routes after deletion and verify downstream forwarding. |
| Transfer the first connection | MWAN-519 | Prove exclusive handover and reverse transfer on the testbed. |
| Transfer remaining interfaces | MWAN-399 | Migrate other providers, the internal bridge, and management last. |
| Retire networkd | MWAN-400 | Remove obsolete dependencies after every replacement passes acceptance. |
| Verify the final testbed | MWAN-401 | Prove operation with networkd disabled, including reboot and failure recovery. |
| Verify production | MWAN-520 | Record production acceptance for every promoted phase and the final system. |

MWAN-397, MWAN-398, MWAN-227, and MWAN-517 use MWAN-516's contract.
MWAN-398 also needs MWAN-397. Coordinate MWAN-227 and MWAN-517 around one
DHCPv6 lifecycle. MWAN-518 follows their acquisition implementations.
MWAN-505 can proceed independently of the new model.

MWAN-519 requires the applicable acquisition components, restart recovery,
route repair, and MWAN-341 startup protection. MWAN-399 follows the first
connection's acceptance. MWAN-400 follows the remaining interface transfers.
MWAN-401 follows retirement. MWAN-520 records production promotion after each
phase's testbed acceptance; its final closure depends on MWAN-401. Do not
delay all production evidence until the final retirement phase.

## Define the contract before implementations

1. Inventory interfaces and current writers on the testbed and production.
   Include shared physical parents, VLANs, bridges, management, udev,
   networkd, generated configuration, and existing daemon modules.
2. Extend the existing schema, loader, and served state under MWAN-516.
   Convert required networkd-only settings before transferring an interface.
3. Define assignment deadlines and ownership transitions with the acquisition
   implementers. Publish enough state to explain a failed apply or lease.
4. Verify valid configuration, unsupported-setting rejection before mutation,
   and history through the actual loader and operational surface.

## Build and validate the replacement components

1. Implement MWAN-397 and the static portions of MWAN-398. Use connected
   Linux network namespaces and the real daemon to verify creation, removal,
   restart, and independent connections.
2. Correct DHCPv4 renewal handling under MWAN-398. The current client expires
   a lease after a failed renewal. Apply the lifecycle in
   [RFC 2131, section 4.4.5](https://www.rfc-editor.org/rfc/rfc2131.html#section-4.4.5).
   Verify existing OOB consumers of the shared client.
3. Implement MWAN-227 and MWAN-517 together at their shared DHCPv6 boundary.
   Select the router-discovery implementation after checking the installed
   kernel against the specification.
4. Complete MWAN-518 using actual short leases and process restarts.
   Verify temporary server absence, expiration, and changed assignments.
5. Complete MWAN-505 with a real owned-route deletion while downstream
   packets are transmitted. Verify recovery without waiting for the periodic
   reconcile interval.

Run `make docker-make TARGETS="check test"` for code changes on macOS.
Run the required privileged Linux tests explicitly. Do not replace missing
privileges with fake link lists or treat skipped tests as acceptance.

## Transfer and verify each phase

1. Resolve MWAN-519's handover choices before its first live transfer. Build
   all components required by the selected connection before deployment.
2. Use the existing Webpass simulator for the proposed first transfer.
   Exercise DHCPv4 separately with the existing dynamic-provider simulator.
3. Deploy a merged release and matching Configs revision. Execute the
   specification's exclusive transfer, reverse transfer, and restart checks.
   Record exact revisions, observed state, packet results, and interruption.
4. Promote each accepted phase through MWAN-520 using
   `./configsctl deploy <name>` from the Configs checkout. Use the existing
   reconnecting Ansible workflow and deployment checks.
5. Repeat through MWAN-399 for the remaining providers. Verify AT&T's actual
   authentication signal before making acquisition depend on it.
6. Transfer internal networking, then management with verified console
   recovery. Inventory any remaining networkd dependencies.
7. Complete MWAN-400, then MWAN-401 and final MWAN-520 acceptance.

Use downstream guests without an OOB bypass throughout the deployment.
Verify load balancing with new connections and observed provider traffic.
Check both families, mapping replies, internal BGP, and recovery using the
acceptance requirements in the specification.

## Extend the existing source boundaries

| Work | Source to inspect and extend |
| --- | --- |
| Validate ownership and acquisition configuration | Extend [the network loader](../../internal/networkjson/networkjson.go). |
| Publish operational state and history | Extend [the served model](../../internal/wanconfig/tree.go) and [the active schema](../../internal/yangpub/schema/goodkind-mwan-steering@2026-09-26.yang). |
| Separate configuration from rendering | Refactor [the link specification](../../internal/networkd/spec.go) and [the renderer](../../internal/networkd/networkd.go) without duplicating types. |
| Apply addresses and acquire assignments | Reuse [address reconciliation](../../internal/netif/state.go), [DHCPv4](../../internal/netif/dhcp.go), and [router solicitation](../../internal/netif/ra.go). |
| Replace delegated-prefix observation | Extend [the prefix source](../../internal/pd/source.go) without rewriting its translation consumers. |
| Transfer mapped addresses and remove overlapping route writers | Update [the WAN routing module](../../internal/ifmgr/modules/wanroutes/wanroutes.go). |
| Coordinate startup and manager selection | Integrate with [daemon startup](../../cmd/mwan/ifmgr.go) and the completed MWAN-341 boundary. |

Keep MWAN-507's BGP and tunnel implementation in its own tickets. Agree on
shared interface ownership and dependency contracts under MWAN-516 before
either epic implements dependent features. Global networkd retirement is
not a prerequisite for that epic.
