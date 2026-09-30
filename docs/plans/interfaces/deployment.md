# Implement deployment and acceptance infrastructure

This plan prepares Configs integration and repeatable acceptance under the
[migration coordinator](../2026-09-26-link-ownership.md) and approved
[interface specification](../../interfaces.md). The coordinator defines the
authoritative PR dependencies and parallel work assignments.

Code implementers write settled code, configuration, and local tests only.
Independent reviewers verify changes, results, required checks, and approvals.
Only the cutover agent assigned by the coordinator activates shared testbed
infrastructure, runs deployment commands, and performs live recovery after
merge and current authorization. Check-mode deployment can copy files and
run validation on remote hosts; it is not a local implementation check.

Merge preparation with default ownership unchanged. Complete the application
release, deployment mechanism, acceptance harness, and recovery checks before
the first live transfer. Preserve AT&T's existing authentication, addressing,
VLAN configuration, and networkd services until the circuit retires. Add no
802.1X integration. Preserve generic VLAN and DHCP support.

## 521-configuration: Render every required interface

### Establish the contract

Depend on merged MWAN-516 configuration and ownership contracts. Use its
compatible released loader and embedded schema. The current Configs template
loops over providers and appends internal and management entries with names
and types. Extend those entries with the required ownership configuration.
Runtime management and transit MAC discovery currently follow rendering.

### Implement the render

Modify these Configs sources:

| File | Required change |
| --- | --- |
| [Network template](https://github.com/agoodkind/configs/blob/main/mwan/config/network.json.j2) | Render all interface roles through MWAN-516's shared model. |
| [Production inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_servers.yml) | Declare ownership, identities, addresses, and family behavior. |
| [Testbed inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_suburban_servers.yml) | Declare equivalent testbed roles and protocol identities. |
| [Runtime discovery](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/mwan-vm/discover-runtime-network.yml) | Supply management and transit identities before rendering. |
| [Gateway playbook](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-mwan.yml) | Reorder discovery before rendering and preserve validation of installed bytes. |
| [Management template](https://github.com/agoodkind/configs/blob/main/ansible/templates/vm/10-mgmt.network.j2) | Preserve its address, DNS, and domain behavior until management transfer. |
| [Transit template](https://github.com/agoodkind/configs/blob/main/mwan/networkd/40-mwanbr.network.j2) | Preserve both addresses, forwarding, and the internal return route until transit transfer. |
| [Gateway sysctl template](https://github.com/agoodkind/configs/blob/main/mwan/config/sysctl-mwan.conf.j2) | Preserve the active owner's IPv6 acquisition policy until exclusive transfer. |

1. Render physical parents, providers, internal interfaces, and management
   independently of provider membership. Preserve stable connection identity
   and repeated provider names or ASNs supported by MWAN-516.
2. Render client identifiers, supported route and resolver options, kernel
   IPv6 policy, and persistent lease configuration through the shared types.
   Reject required unsupported networkd settings before activation.
3. Discover MAC addresses after VM identity resolution and before the first
   network render. Render once, validate those bytes with the pinned loader,
   and install those validated bytes.
4. Preserve the actual virtio transit device. Do not create a guest bridge
   based on the name `enmwanbr0`. Account for adjacent udev naming files
   separately from networkd address configuration.
5. Keep unmigrated connections under explicit networkd ownership. Preserve
   management DNS/domain settings and the transit return route through the
   OPNsense edge when converting their configuration.
6. Preserve networkd's userspace IPv6 acquisition during preparation. Its
   current Webpass policy disables kernel router advertisements and
   autoconfiguration. Render the future owner's policy without activating
   it before transfer. Preserve MWAN-341 startup protection.

### Verify and review the configuration boundary

Extend the real render and loader test in
[the installation spec](https://github.com/agoodkind/configs/blob/main/spec/ansible/mwan_install_spec.rb)
and its
[testbed fixture](https://github.com/agoodkind/configs/blob/main/spec/fixtures/ansible/render_mwan_network.yml)
and [production fixture](https://github.com/agoodkind/configs/blob/main/spec/fixtures/ansible/render_mwan_prod_network.yml).
Set `MWAN_TRANSLATION_TEST_BINARY` to the compatible Linux executable and
require both environments' network and firewall validators to run without
skipping. Accept complete non-provider roles through the production loader.
Reject conflicting owners and unsupported required settings through that
boundary.

The existing validation uses `mwan install --print-schema`,
`mwan deploy-gate check-network`, and `mwan deploy-gate check-firewall` with
temporary schema and rendered-document arguments. The firewall check requires
a privileged Linux environment. Record the actual executable, complete
arguments, and results in the acceptance command manifest. Do not substitute
string snapshots for validation or later packet acceptance.

The reviewer compares rendered behavior with the management, transit,
provider, and sysctl inputs. Stop on a missing required MWAN-516 field and
report the exact contract gap before changing the design. Hand off the
Configs commit, compatible MWAN commit, rendered hashes, and local results.

### Pair the NPT journal with the compatible release

The inspected Configs baseline `3f28f3bf` supplies no address journal in the
VM runtime template. Add this section inside its WAN-enabled condition in
[the VM template](https://github.com/agoodkind/configs/blob/main/mwan/config/config-vm.toml.j2):

```toml
[ifmgr.modules.addresses]
state_file = "/var/lib/mwan/owned-addresses.json"
```

1. Preserve the existing conditional `lease_directory` in
   [the shared runtime template](https://github.com/agoodkind/configs/blob/main/mwan/config/_ifmgr_common.toml.j2).
   Do not add link or kernel-policy journals merely to manage a legacy NPT
   edge. Networkd and external acquisition retain their configured owners.
2. Pin the compatible published merged application release in both
   [testbed release inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_testbed_all.yml)
   and [production release inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_prod_all.yml)
   before merging this shared section. The older address module rejects a
   supplied journal when legacy startup publishes no `OwnedLinks` result;
   successful TOML parsing does not prove startup compatibility.
3. Run both real installation renders with the compatible published Linux
   executable through this command:

   ```sh
   env MWAN_TRANSLATION_TEST_BINARY="$MWAN_RELEASE_EXECUTABLE" bundle exec rspec spec/ansible/mwan_install_spec.rb
   ```

   Also start the actual daemon from rendered TOML with all-networkd input.
   Require the loader, firewall checks, and startup case to run without skips.
4. Prove the same-boot deployment sequence before relying on the planned
   reboot. Binary installation, role activation, handler flush, and firewall
   inspection precede that reboot. Verify backup forwarding and every
   intervening gate with the existing unjournaled edge present. Do not adopt
   that edge, delete it manually, or assume an aborted play will reboot later.
5. Use the planned baseline reboot, then verify old edge absence before the
   new authority creates and journals it. Record the actual producer result
   and kernel readback; a changed boot identity alone does not prove absence.
   Preserve ordinary historical journal records without reclassification.
6. Deploy the merged Configs and compatible release pair to the testbed.
   Require the full NPT regression and isolated and testbed baseline reboot
   acceptance before physical dual-stack transfer acceptance. Require that
   physical acceptance before production promotion. An older executable
   restores the unjournaled writer and is not a compatible same-boot recovery
   target.

The NPT implementation contract and public regression matrix belong to the
[acquisition plan](acquisition.md#npt-address-authority-journal-edges-across-connection-owners).
The existing cutover workflow supplies downstream observation and recovery;
this companion does not change connection ownership.

## 521-deployment: Implement exclusive transfer

### Preserve the deployment boundary

Depend on the configuration slice and settled acquisition, restart,
observation, and startup contracts. Implement transfer mechanics before live
activation. The current gateway playbook verifies a merged checkout and a
pinned release, validates a render, installs configuration, and reboots. Its
hypervisor-local gate starts before reboot and publishes the verdict afterward.

Modify the gateway playbook and its existing task boundaries. Preserve
[merged-checkout enforcement](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/require-merged-checkout.yml),
[release verification](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/verify-mwan-release.yml),
and [verdict collection](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/files/collect-deploy-verdict.sh).
Preserve [asynchronous restart and reconnection](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/restart-mwan-ifmgr.yml):
start the restart asynchronously, wait for controller access after the job
deadline, then verify completion.
Coordinate capabilities and persistent storage with
[the installed daemon unit](../../../cmd/mwan/mwan-ifmgr@.service).

### Implement forward and reverse transfer

1. Stage the compatible release, validated configuration, and recovery pair
   before stopping any owner. Preserve private-namespace firewall validation,
   startup protection, and active-firewall inspection in both environments.
   Preserve installed rules during nftables service retirement.
2. Exclude the selected connection from new traffic. Stop its previous
   manager's writes and protocol clients. Verify ownership release;
   removing a networkd file alone is insufficient.
3. Activate the complete replacement connection: links, addresses, assigned
   routes, and acquisition. Enable its kernel router discovery and SLAAC only
   after networkd's writes and protocol clients have stopped and ownership
   release has passed verification. Preserve client identity and negotiate
   fresh leases without importing networkd lease files.
4. Require assignments, routes, translation, readiness, and downstream packets
   before restoring selection. Preserve independent connections and shared
   parents required by another interface.
5. Implement the reverse sequence with one writer at each step. Restore a
   compatible release and configuration together. Stop MWAN acquisition and
   release its ownership, then restore the previous kernel IPv6 policy before
   restarting networkd's userspace acquisition. Preserve kernel ownership of
   automatic-address lifetimes and unrelated host networking.
6. Keep networkd enabled for remaining interfaces. Preserve the separate
   failover LXC manager and its continuing service.
7. Preserve Ansible reconnection around access interruptions and hypervisor-local
   recovery when guest access fails. Distinguish lost egress, missing verdict,
   unchanged boot identity, and failed mapped-address checks. Do not convert
   every failure into an automatic rollback.

### Verify and review transfer mechanics

Run local render and loader verification. Exercise the real transfer mechanism
in the isolated MWAN-522 Linux environment with actual networkd and MWAN
processes, real protocol services, and downstream packets. Verify initial and
reverse transfer, interruption, repeated deployment, and restart. Assert
active clients, kernel state, and served ownership. Mock owners and task-order
assertions do not establish runtime acceptance.

The reviewer verifies the patch, local results, recovery pair, and unchanged
default ownership. Reject unmerged deployment, new shell wrappers, global
networkd disablement, and authentication integration. Stop on ambiguous
ownership, unavailable recovery, invalid render, or missing packet evidence.

Hand off merged commits, release identity, rendered hashes, exact targets,
forward and reverse commands, interruption limits, and recovery evidence.
Missing executable acceptance commands block transfer until MWAN-522 supplies
them. The operational plan defines the verified deployment entry points.

## 522-acceptance: Implement repeatable protocol and packet checks

### Prepare the simulator and harness changes

Bootstrap the independent MWAN runner after MWAN-516 merges, before protocol
PR acceptance. Establish real daemon startup, Linux namespaces, Kea and radvd
processes, packet observation, and cleanup without waiting for every acquisition
feature. Each feature PR adds and runs its scenarios through this runner.

Prepare simulator configuration independently. Build the Configs downstream
harness after deployment and simulator changes merge. Assemble and run the
final MWAN-522 suite after all required feature changes merge. Follow the
coordinator's PR map and preserve existing provider scenarios.

Modify the existing Configs sources:

| Source | Required behavior |
| --- | --- |
| [Kea DHCPv4 configuration](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/kea-dhcp4.conf.j2) | Exercise acquisition, renewal, rebinding, expiry, and changed addresses or gateways. |
| [Kea DHCPv6 configuration](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/kea-dhcp6.conf.j2) | Exercise interface addresses and delegation together, changed prefixes, and assignment deadlines. |
| [Router advertisements](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/radvd.conf.j2) | Exercise router expiry, prefix deprecation, and kernel SLAAC with forwarding enabled. |
| [Simulator deployment](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/tasks/deploy-testbed-isp-lxc.yml) | Activate reviewed simulator configuration through the existing playbook. |
| [Delegation return routing](https://github.com/agoodkind/configs/blob/main/testbed/isp-lxc/pd-route.service.j2) | Preserve return routing through the gateway's stable link-local next hop. |
| [Downstream client declarations](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/mwan_test_clients.tf) | Preserve clients 225 and 226 and their single downstream interface. |
| [Gateway and router declarations](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/vms.tf) | Preserve the infrastructure used by the downstream clients. |

### Implement realistic scenarios

1. Add configurable short lifetimes and explicit client identities. Assert
   identities in actual packets or Kea observations. Preserve the existing
   routed static-block simulator scenario.
2. Preserve or update `isp.mwan_vm_ll` with identity changes. Verify return
   traffic to delegated prefixes. DHCP success without a working return
   route does not establish forwarding acceptance.
3. Run the production daemon and loader in connected Linux namespaces with
   real Kea and radvd processes, VLAN devices, addresses, routes, and packets.
   Do not use mocks, recorded replies, or fake protocol dependencies.
4. Exercise concurrent DHCPv6 address and prefix assignments, duplicate-address
   detection, prefix length changes, lease rejection, server loss, renewal,
   rebinding, expiry, restart, and reboot.
5. Exercise absent-provider startup and interface recreation with a different
   index. Delete an owned return route during downstream traffic and verify
   prompt event-driven repair. Preserve unrelated routes and another provider.
6. Access clients 225 and 226 directly over SSH through Suburban using their
   downstream IPv4 addresses. Verify each client's interfaces and IPv4 and
   IPv6 routes before testing. Prove that test traffic uses OPNsense and the
   selected MWAN gateway without an out-of-band bypass. Generate both
   families' traffic from the clients. Verify inbound mapping replies and
   translation after assignment changes.
7. Vary new connections according to the configured hash mode. Observe packets
   at simulator ingress and compare provider use with eligibility, tiers,
   and weights. Gateway-originated probes alone do not prove balancing.
8. Exercise forward and reverse transfer, repeated deployment, ordinary
   restart, and cold boot. Preserve generic VLAN tests and exclude 802.1X
   acceptance.

### Publish and verify executable commands

Create the acceptance command manifest as an implementation deliverable before
MWAN-519. Record exact entry points, repository revisions, privileged Linux
environment, daemon/server versions, setup and teardown commands, scenario
inputs, packet artifacts, expected observations, timing bounds, and recovery.
Identify every destructive fault injection and its isolated target. Review
the manifest before authorizing transfer.

Run `make docker-make TARGETS="check test"` for MWAN code changes on macOS.
Run every required privileged case explicitly. Missing privileges, skipped
tests, and absent commands block acceptance.

Run `make test-protocol` for the assembled isolated acquisition, process
recovery, autoconfiguration, resolver, and ordered-startup cases. Its
`test-protocol-namespace` and `test-protocol-systemd` subtargets use separate
privileged containers. The systemd container starts actual networkd,
resolved, and udev. Set `MWAN_PROTOCOL_TEST_BINARY` to an absolute published
executable when validating a release. Every selected daemon case uses that
read-only executable. Commands and complete test events are stored under
`bin/protocol-results`; change `PROTOCOL_RESULTS_DIR` to retain another
artifact directory. The aggregate does not establish physical forward and
reverse transfer, reboot, balancing, or shared testbed acceptance.

The shared simulator activation command from the Configs root is:

```sh
./configsctl deploy deploy-testbed --limit suburban
```

Only a cutover agent runs this command after merge and current authorization.
OpenTofu changes use `./configsctl tofu` with the reviewed target plan; record
the exact arguments before applying it.

The reviewer verifies actual process, packet, kernel, and served-state
evidence. Stop on missing return routing, OOB bypass, conflicting owners,
unbounded interruptions, or failures hidden by skipped tests. Restore ordinary
simulator traffic after each fault before accepting the next result.

Hand off the merged harness and configuration revisions, complete manifest,
measured baseline and interruption bounds, scenario results, and packet
artifacts. Separate isolated tests, shared testbed acceptance, and production
acceptance in the execution ledger.
