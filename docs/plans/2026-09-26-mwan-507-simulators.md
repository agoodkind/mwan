# Persistent tunnel and BGP simulator implementation plan

## Goal

Provide persistent thin LXCs for Astound 6in4 without BGP, native Etheric BGP,
and all three VPS tunnel arrangements. Give each variant its own endpoint
and each BGP scenario its own upstream router. Select the active MWAN
configuration without rebuilding remote simulators.

The [routing specification](../superpowers/wanconfig/model.md#persistent-tunnel-and-bgp-simulators)
defines approved behavior. This plan covers simulator prerequisites, not the
entire MWAN-507 application implementation. No deployment has occurred.

## Current behavior

The specification revision is MWAN `b9693a7`; inspected Configs main is
`94848bff5be240e69f8ebd1e5ea171a8aca5ef42`. Recheck both before implementation.
Search found the specification and related migration plans, but no simulator
implementation plan.

Configs already declares ISP LXCs, provider bridges, and downstream clients.
Astound is IPv4-only. Its ordinary uplink masquerades IPv4. The separate
router-2 LXC runs FRR for internal BGP. Reuse its deployment pattern without
changing its existing purpose or sessions. Service mapping supplies shared
guest identities. The testbed deployment already checks for a merged checkout.

## Constraints

- Complete MWAN-524 before live Astound tunnel acceptance. Simulator preparation
  must not wait for MWAN tunnel or BGP tickets to close.
- Use 6in4 for testbed transport. Keep production protocol selection open.
  Exclude WireGuard. Preserve ordinary IPv4 independently of IPv6 BGP.
- Keep all variants provisioned concurrently with separate BGP upstream LXCs.
  Preserve existing ISP simulators, clients, and internal BGP.
- Use OpenTofu for guests and bridges and Ansible for guest settings. Use
  networkd for simulator links and configured addresses and FRR for BGP routes.
  Assign one writer per object. MWAN-305's gateway migration does not require
  removing networkd from simulator guests.
- Preserve MWAN-340 translation, MWAN-333 source routing, and MWAN-341 firewall
  boundaries. Reuse applicable MWAN-522 client and packet infrastructure.
- Test public behavior with real dependencies. Add no mocks, template-string
  tests, or shell wrappers. Use merged revisions and supported deployment
  commands. Reserve shared testbed activation for a cutover agent.
- Perform no production deployment as part of simulator construction.

## Tasks

### 1. Define inventory and reserve identities

Files in Configs:

- Modify [service mapping](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/all/service_mapping.yml).
- Modify [testbed inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/suburban_servers.yml).

Behavior:

Define `testbed_routing_scenarios` with keys `astound_static`, `etheric_native`,
`tunnel_static`, `tunnel_upstream`, and `tunnel_vps`. Define
`testbed_routing_nodes` for node roles and service-mapping references. Guest
VMIDs, MACs, hostnames, and addresses must not be redefined in scenario data.

Each scenario specifies transport connections, node references, tunnel
protocol and MTU where applicable, outer endpoint pairs, inner addresses,
home and remote prefixes, configured routes, peer pairs, local and remote
ASNs, import/export prefixes, and probe targets. Keep peer addresses separate
from tunnel endpoints. Permit repeated ASNs and reject conflicting identities.

Steps:

1. Inspect service mapping, OpenTofu state, and live guests read-only. Reserve
   unused VMIDs, MACs, bridge names, and address ranges before resource creation.
2. Declare a plain 6in4 endpoint and remote client for `astound_static`, with no
   BGP process. Declare an Etheric router, upstream, and remote client for
   `etheric_native`. Declare a VPS, upstream, and remote client for each tunnel
   BGP variant. Each upstream belongs to exactly one scenario.
3. Declare a dedicated ordinary Sonic transport simulator. Preserve Webpass,
   Astound, Monkeybrains, the routed simulator, and router-2. Represent a second
   Sonic circuit as a separate connection with independent identity.
4. Validate required fields and references before changing guests. Reject
   unsupported scenario kinds, missing peer policy, and duplicate endpoint
   tuples. For several transports to one VPS, predeclare separate tunnel
   interfaces and endpoint pairs instead of rewriting the VPS between tests.

Verification:

Run `./configsctl lint` from Configs. Inspect inventory consumed by OpenTofu
and Ansible. Expect one value per identity and independent scenario references.
Tasks 3 through 5 provide runtime assertions.

### 2. Declare persistent guests and networks

Dependency: task 1's reviewed inventory contract.

Files in Configs:

- Create [routing simulator resources](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/routing_simulators.tf).
- Modify [provider containers](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/containers.tf)
  for Astound's additional test transport interface.
- Modify [gateway VM resources](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/vms.tf)
  for new Sonic and Etheric provider attachments when required.
- Reuse [network declarations](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/networks.tf),
  [shared identities](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/services.tf),
  and [downstream clients](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/mwan_test_clients.tf).

Steps:

1. Key guest and bridge resources by the reviewed inventory. Use small Debian
   LXCs. Start FRR nodes with one CPU and router-2's current memory allocation;
   measure before reducing it. Scenario selection must not alter resource keys.
2. Add an isolated outer IPv4 network connecting Astound's additional interface,
   Sonic transport, and remote endpoints. MWAN must access it through the
   selected provider, never by a direct attachment.
3. Preserve Astound's existing provider and ordinary uplink interfaces. Route
   test endpoint addresses over the added interface without masquerade. Return
   outer packets through the corresponding provider to the declared MWAN
   endpoint. Preserve masquerade on the ordinary internet uplink.
4. Give each VPS/upstream pair a separate transit network and each upstream a
   separate remote-client network. Give Etheric provider and upstream links
   without a tunnel. Keep management interfaces outside test forwarding.
5. Verify host support for SIT, Linux's IPv6-over-IPv4 tunnel device, and required
   container permissions. Configure required host modules through Ansible.
   Do not copy privileged `features.nesting` settings into token-managed
   OpenTofu resources. Use the established root deployment boundary only when
   a required capability needs it.

Verification:

Run from Configs after normal backend initialization:

```sh
./configsctl tofu -chdir=opentofu/suburban fmt -check
./configsctl tofu -chdir=opentofu/suburban validate
./configsctl tofu -chdir=opentofu/suburban plan -out=routing-simulators.tfplan
./configsctl tofu -chdir=opentofu/suburban show routing-simulators.tfplan
```

Expect declared additions and reviewed interface attachments only. Reject
replacement or deletion of existing guests and bridges. A required gateway
reboot belongs to cutover, not local verification.

### 3. Configure Astound 6in4 without BGP

Dependencies: tasks 1 and 2. Live MWAN acceptance additionally requires
MWAN-524 and MWAN-510/511 implementation.

Files in Configs:

- Modify [testbed deployment](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-testbed.yml)
  to include simulator tasks under a `routing-simulators` tag.
- Create [simulator deployment tasks](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/deploy-routing-simulators.yml).
- Create [network unit template](https://github.com/agoodkind/configs/blob/main/testbed/routing-simulators/interface.network.j2),
  [tunnel device template](https://github.com/agoodkind/configs/blob/main/testbed/routing-simulators/tunnel.netdev.j2),
  and [firewall template](https://github.com/agoodkind/configs/blob/main/testbed/routing-simulators/nftables.conf.j2).
- Modify [ISP deployment tasks](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/deploy-testbed-isp-lxc.yml)
  for the additional routed Astound interface.

Steps:

1. Deploy only declared nodes through the hypervisor's existing guest-management
   boundary. Assert Suburban as the target and require a merged checkout.
   Use and clean up distinct temporary files per node.
2. Configure outer addresses and return routes, SIT devices, inner IPv6
   addresses, and configured home/remote routes. Enable forwarding on the
   endpoint LXC. Keep FRR disabled for `astound_static`.
3. Permit IPv4 protocol 41, required ICMP, and declared test forwarding. Do not
   translate inner IPv6 or forward test packets through management interfaces.
4. Preserve unrelated variants on repeat deployment. Reconnect around network
   changes with the established Ansible tasks.

Verification:

Run `./configsctl lint`. After authorized merged activation, inspect
`ip -d link show`, IPv4/IPv6 routes, and active services inside the endpoint.
Expect the configured SIT endpoint pair and MTU, valid return routes, and no
BGP process. Task 5 verifies packets through the provider.

### 4. Configure native and tunneled BGP

Dependency: the shared inventory, network templates, and deployment boundary.
Preserve the plain Astound scenario unchanged.

Files in Configs:

- Extend task 3's simulator deployment and network templates.
- Create [FRR configuration](https://github.com/agoodkind/configs/blob/main/testbed/routing-simulators/frr.conf.j2)
  and [FRR daemon settings](https://github.com/agoodkind/configs/blob/main/testbed/routing-simulators/daemons.j2).
- Reuse [router-2 deployment](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-router2-sim.yml)
  as a package/service reference without editing its sessions.

Steps:

1. Install FRR. Enable zebra, its kernel-route manager, and bgpd, its BGP
   daemon, on BGP nodes only. Record versions and validate rendered
   configuration before restart. Report the failed node and scenario.
2. Configure IPv6 unicast sessions with explicit import and export policies.
   Announce real connected or learned routes. Do not use a permanent discard
   route to conceal loss of a usable home path.
3. Apply the scenario contracts below. Configure multihop peering for scenario
   2 and next-hop behavior that resolves announced routes to actual forwarding
   paths. Keep peer and tunnel endpoint addresses independently configurable.
4. Scope each restart and failure to its scenario. Keep distinct upstream
   instances even when their configured ASNs repeat.

| Scenario | Required remote configuration |
| --- | --- |
| `etheric_native` | Etheric peers directly with MWAN and separately with its upstream. Its home route depends on the MWAN announcement. |
| `tunnel_static` | The VPS uses a configured home route through its tunnel and BGP with its own upstream. This is scenario 1. |
| `tunnel_upstream` | The VPS forwards between tunnel and upstream without terminating the home BGP session. The upstream peers with MWAN across the VPS. This is scenario 2. |
| `tunnel_vps` | The VPS peers with MWAN and separately with its upstream. Its upstream home announcement depends on a usable learned home route. This is scenario 3. |

Verification:

Run `./configsctl lint`. After deployment, inspect
`vtysh -c 'show bgp ipv6 unicast summary json'`,
`vtysh -c 'show bgp ipv6 unicast json'`, and `ip -6 route show`.
Expect declared peers and installed routes to agree. Check remote sessions
before MWAN features exist where both peers are implemented. An absent MWAN
peer at that stage means feature acceptance remains incomplete.

### 5. Implement public-boundary acceptance

Dependency: tasks 3 and 4 for the applicable scenarios. Reuse available
MWAN-522 tooling without making MWAN-305 depend on external BGP.

Files in Configs:

- Create [acceptance playbook](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/check-routing-simulators.yml).
- Create [scenario assertions](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/check-routing-scenario.yml).
- Create [fault and recovery tasks](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/check-routing-recovery.yml).

Steps:

1. Require `routing_scenario` and `routing_check_mode` inputs. Accept
   `infrastructure` and `mwan` modes. Read all identities and expected
   observations from inventory. Assert merged checkout and testbed scope.
2. In infrastructure mode, check declared nodes, applied configuration, outer
   transport, and remote sessions with implemented peers. Emit separate
   results per scenario and no claim of MWAN acceptance.
3. In MWAN mode, exercise the real gateway and downstream clients. Verify
   ICMP and a real TCP transfer in both directions. Observe protocol 41 before
   and after the ISP router and inner IPv6 at the remote endpoint. For native
   Etheric, observe IPv6 directly. Assert source addresses and return paths.
4. Test packets below the tunnel MTU and oversized packets. Verify successful
   delivery or the expected packet-too-big response. Verify ordinary IPv4
   throughout and reject silent packet loss as success.
5. Assert permitted BGP announcements and rejection of an undeclared test
   prefix. Verify route installation and withdrawal against packet results.
   Test upstream loss separately from tunnel loss and verify another scenario
   retains its configuration and service.
6. Bound captures and transfer timeouts. Restore injected changes in Ansible
   `always` tasks and record recovery. Restrict faults to selected testbed
   nodes. Missing privileges and skipped required checks fail acceptance.
7. Record revisions, configuration hashes, commands, route/session observations,
   packets, interruption, and recovery. MWAN-513 owns combined selection and
   balancing acceptance after the individual features pass.

Verification:

After the new playbook is implemented and merged, run from Configs:

```sh
./configsctl deploy check-routing-simulators --limit suburban -e routing_scenario=all -e routing_check_mode=infrastructure
./configsctl deploy check-routing-simulators --limit suburban -e routing_scenario=astound_static -e routing_check_mode=mwan
```

Accept the other four scenario keys for individual MWAN checks. Infrastructure
mode must verify every persistent instance. MWAN mode must fail when the
selected gateway path or required packet assertions fail. These are planned
entry points, not existing checks. Add the tunnel checks with task 3 and the
BGP checks with task 4 rather than postponing tests to a final PR.

### 6. Merge and activate in stages

Files: Update this plan's execution record with exact PRs, commits, commands,
results, and outstanding acceptance. Update Tack states only from evidence.

Steps:

1. Review the inventory contract and exclusive file ownership before assigning
   implementers. Create a Configs PR for inventory/resources, a dependent PR
   for Astound tunnel behavior and tests, and a dependent PR for BGP variants
   and tests. Use Graphite for this stack. Preserve required AI reviews.
2. Permit independent infrastructure, remote configuration, and test work only
   after shared contracts are settled. The coordinator owns shared inventory
   and integration. An independent reviewer verifies each patch and its
   public behavior. A cutover agent alone performs live activation.
3. Finish required code before activation. From the merged Configs revision,
   apply the reviewed saved plan with
   `./configsctl tofu -chdir=opentofu/suburban apply routing-simulators.tfplan`.
   Regenerate and review a stale plan before applying it.
4. Deploy with
   `./configsctl deploy deploy-testbed --limit suburban --tags routing-simulators`.
   Include merged-checkout verification and simulator prerequisites in the
   tag. Exclude unrelated host and ISP service restarts from that invocation.
5. Verify infrastructure first. After compatible merged MWAN features exist,
   activate the complete scenario configuration through
   `./configsctl deploy deploy-mwan --limit mwan_suburban_servers` and run
   the scenario's MWAN checks. Keep every other variant provisioned.
6. Restore the previous accepted gateway configuration after a failed
   activation. Record Astound acceptance under MWAN-510/511, direct BGP under
   MWAN-509, and tunnel BGP under MWAN-512. Run MWAN-513's combined recovery
   and balancing afterward. Keep production activation separate.

Verification:

Expect repeat deployment and guest restart to preserve each variant's
identity and configuration. Verify another scenario before and after an
isolated upstream failure. Distinguish infrastructure readiness, remote
protocol success, MWAN acceptance, and combined failover acceptance.

## Execution record

September 26, 2026: The plan is written against the revisions above. No code
implementation, infrastructure apply, deployment, or packet acceptance has
occurred. The first task is inventory review and identity reservation.
MWAN-524 remains the prerequisite for live Astound tunnel acceptance.
