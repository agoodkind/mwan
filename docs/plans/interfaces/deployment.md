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

## Correct legacy preparation and deployment recovery

Complete MWAN-535 through MWAN-544 before another production preparation.
Keep the recovered production pair unchanged during implementation and
testbed validation. Apply the coordinator's production authorization gate.

1. Stage the merged candidate separately from the installed original binary.
   Capture legacy NPT evidence while the original producer runs, before
   replacing its executable or configuration. Stop and verify that producer,
   adopt the verified surviving objects through the candidate's one-shot
   command, then start the ordinary service. Retain no boot-bound adoption
   flag in its persistent service command.
2. Require the dedicated original-release upgrade case to execute its original
   binary and hash through real networkd and daemon startup. Keep ordinary
   ownership cases independent. Require positive translation and inbound
   replies after preparation; neither a skip nor translation removal passes.
3. Verify the snapshot and baseline application replies, then arm one recovery
   operation before the first network-affecting change. Register the exact
   hypervisor watch unit, invocation and PID. Require fresh passing observations
   and mutation-ready status before granting a mutation lease. During a
   planned interruption, verify the exact armed operation, live watch and
   remaining lease budget before each write under that lease.
4. Acquire a bounded mutation lease for each risky persistent-write group.
   Preserve remote asynchronous jobs and reconnect verification. Release the
   exact lease after verified remote completion, including during recovery.
   Set the recovery timeout to cover the maximum remaining lease wait plus
   restoration and verification. After reconnect, reject writes from an
   operation that started recovery. Lease expiration alone does not prove that
   remote jobs stopped; require VM stop and stopped-state readback before
   snapshot restoration.
5. Serialize automatic and manual recovery with the watchdog. Resume the exact
   operation if its watch disappears or its deadline expires. Reject new
   writes during recovery. Verify the restored machine, executable, network
   and runtime pair, record its new boot identity, and require restored
   application replies before success. Permit exact recovery retry after a
   measured recovery failure without granting new deployment leases.
6. Include required inbound replies independently of backup outbound success.
   Configure each planned provider interruption with an exact lease phase,
   selected-provider inbound check IDs and maximum duration. Keep failed
   replies in the health results. Permit those failures only while the exact
   matching lease remains live. Keep downstream traffic, other providers,
   missing or stale observations and commitment checks strict. Grant no
   interruption exception by default. Keep failed target replies
   separate from missing, stale or inaccessible observations. Commit only
   after independently repeating the required checks and target identity.
7. Run failed original-release preparation and repaired preparation on the
   physical testbed with both downstream guests and both families observed.
   Do not require testbed backups or preservation of its current state before
   fault injection. The testbed has no production clients. Prove automatic recovery
   after controller disconnection, successful commit, restart, reboot, reverse
   transfer and failover. Keep historical and current attempts separate.

Implement the shared typed observations and one-shot public command in MWAN.
Keep downstream applications, inbound services, provider egress, ping paths,
external public addresses and connection distribution independent. Refresh
assignment and path evidence. Testbed outer NAT can give different simulated
providers one public address; verify their distribution at simulator ingress.
Use the maintained Cloudflare SDK for current pool health and its protected
credential file. Preserve existing Cloudflare configuration during state
adoption. Implement playbook orchestration in Configs without a new application
harness or shell wrapper.

## 522-acceptance: Implement repeatable protocol and packet checks

### Prepare the simulator and harness changes

Bootstrap the independent MWAN runner after MWAN-516 merges, before protocol
PR acceptance. Establish real daemon startup, Linux namespaces, Kea and radvd
processes, packet observation, and cleanup without waiting for every acquisition
feature. Each feature PR adds and runs its scenarios through this runner.

Prepare simulator configuration independently. Keep downstream acceptance
code in MWAN after deployment and simulator changes merge. Assemble and run the
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

#### Prepare the reusable execution manifest

Use existing public daemon commands, protocol runners and Linux utilities.
Keep new acceptance application code in MWAN. Do not restore the deleted
Configs Ruby harness or require a replacement harness for these operations.

1. Record clean merged MWAN and Configs commits, published executable/archive
   hashes, native architecture and runner image identity. Record actual daemon,
   systemd, Kea, radvd, iproute2 and tcpdump versions. Preserve a compatible
   recovery release/configuration pair and verified hypervisor console access.
2. Record the authorized gateway, hypervisor, OPNsense edge, both downstream
   guests, simulator hosts and interfaces. Supply machine and boot IDs,
   installed executable/service/configuration hashes, owners, protocol
   identities, assignments and deadlines. Refresh these before execution.
3. Supply each guest's source addresses, probe destinations, primary/backup
   gateways, eligible providers, tiers, weights and hash mode. Supply mapped
   destinations, ports, response hashes and translation prefixes. Production
   values are separate reviewed inputs, not testbed defaults.
4. Assign separate observation transports and new outputs. Record every owned
   capture unit, PID, executable and host. Do not stop a shared SSH master.
   Retain complete setup, fault, restoration and teardown arguments, timestamps,
   stdout, stderr and exit statuses. Identify each destructive target before
   authorizing the operation.

Run the assembled protocol suite from clean merged MWAN with its published
native executable, a new output directory and the compatible Configs checkout:

```sh
make test-protocol MWAN_PROTOCOL_TEST_BINARY="$PUBLISHED_NATIVE_EXECUTABLE" PROTOCOL_RESULTS_DIR="$PROTOCOL_EVIDENCE" MWAN_OWNED_ROLE_CONFIGS="$CONFIGS_CHECKOUT"
```

Require every selected case's passing terminal event and zero skips. Preserve
failed attempts separately. The accepted release execution selected 27 namespace
and six systemd cases; review any changed selection. Preserve the twenty-minute
package deadline and existing fixture deadlines. Require the runner's exact
owned container to be absent after cleanup. Native ARM64 protocol evidence
does not establish AMD64 execution or physical ownership transfer.

#### Deploy the authorized phase

1. Merge compatible release pins and configuration before deployment. Start
   source-bound observation on both downstream guests before the first network
   change and continue through primary recovery. Preserve Configs checkout and
   release enforcement, reconnection and hypervisor-local verdict collection.
2. From clean merged Configs, run the complete authorized testbed operation:

   ```sh
   ./configsctl deploy deploy-mwan --limit mwan_suburban_servers
   ```

3. Compare installed identity, owner release/acquisition, selection, protection,
   BGP, mappings, translation, management/transit and the reboot verdict. Compare
   every nonselected connection with its baseline. Execute the reviewed reverse
   configuration through the same play, then repeat forward transfer, ordinary
   restart and reboot. Preserve fresh initial acquisition separately from
   persisted restart recovery.
4. For the first Webpass production phase, prepare with every connection
   networkd-owned. Production is authorized after complete incident repair and
   the required testbed acceptance pass for the exact merged release and
   compatible configuration. Run its separate
   operation:

   ```sh
   ./configsctl deploy deploy-mwan --limit mwan_servers
   ```

   Accept preparation and capture installed runtime/network documents before
   merging and deploying Webpass activation. Preserve strict comparison of
   nonselected records. Apply that conditional authorization to activation. Keep
   AT&T and networkd coexistence; other interfaces and retirement are later phases.

#### Observe downstream packets and provider attribution

Run the following commands on the indicated host through its reviewed,
authenticated SSH transport. Bind variables to reviewed environment inputs
before execution. Retain the complete transport arguments. The accepted packet
commands used BatchMode=yes and ConnectTimeout=15; continuous observers used
ConnectTimeout=5. Do not infer packet health from SSH success.

| Host | Existing commands | Required observation |
| --- | --- | --- |
| Each downstream guest | `ip -j -d link`; `ip -j address`; `ip -4 -j route show table all`; `ip -6 -j route show table all`; `ip -4 -j rule`; `ip -6 -j rule` | Compare actual interfaces, addresses and routes with the declared guest. Reject an OOB bypass. |
| Each downstream guest | `ip -4 -j route get "$PROBE4" from "$SOURCE4"`; `ip -6 -j route get "$PROBE6" from "$SOURCE6"` | Verify source and downstream gateway before requests. |
| Gateway | `sha256sum /usr/local/bin/mwan`; `sha256sum /etc/mwan/network.json`; `cat /etc/machine-id`; `cat /proc/sys/kernel/random/boot_id`; `systemctl show mwan-ifmgr@wan.service --property=MainPID,ActiveState` | Match the installed pair and machine. Compare boot ID after reboot and MainPID after restart. |
| Gateway | `sysrepocfg -X -d operational -m ietf-interfaces -f json`; `ip -j -d link`; `ip -j address`; `ip -4 -j route show table all`; `ip -6 -j route show table all`; `nft list ruleset` | Compare owner, acquisition/deadlines, family readiness, routes, mappings and translation. |
| Each downstream guest | `ping -4 -D -O -n -I "$SOURCE4" -i 1.0 -w "$OBSERVATION_SECONDS" "$PROBE4"`; `ping -6 -D -O -n -I "$SOURCE6" -i 1.0 -w "$OBSERVATION_SECONDS" "$PROBE6"` | Run all four streams concurrently. Preserve timestamps, sequences and terminal transmission totals. |

Query OPNsense concurrently with `/sbin/route -n get -inet "$PROBE4"` and
`/sbin/route -n get -inet6 "$PROBE6"`. Record backup selection and primary
recovery separately from packet replies. Route-query failures and SSH gaps
do not establish packet loss. Adjacent reply intervals do not establish a
continuous outage. The observation duration is not an outage limit. Preserve
measured interruption without inventing a new bound.

Start captures on gateway transit, each participating gateway WAN and each
participating simulator ingress. Use simulator eth0, not its management
interface. The recorded capture operation is:

```sh
systemd-run --quiet --collect --pipe --wait --unit="$CAPTURE_UNIT" --property=RuntimeMaxSec=601 --property=KillSignal=SIGINT --property=TimeoutStopSec=3 -- tcpdump --immediate-mode -U -nn -s 0 -i "$CAPTURE_INTERFACE" -w - tcp port "$CAPTURE_PORT"
```

Retain binary stdout as the capture and stderr as its terminal counters.
Verify active unit, MainPID and `/proc` executable before requests. Use TCP
port 443 for HTTPS and the reviewed mapping port for mapping cohorts. The
tested mapping port was 1406; do not assign it to production.

Run the recorded HTTPS operation on each guest with family 4 or 6, its source,
the family-specific resolve argument and a distinct request label:

```sh
curl -"$FAMILY" --interface "$SOURCE" --silent --show-error --fail --noproxy '*' --http1.1 --connect-timeout 15 --max-time 14 --header 'Connection: close' --header "X-Mwan-Acceptance: $REQUEST_LABEL" --resolve "$RESOLVE_ARGUMENT" --output /dev/null --write-out '%{json}' https://one.one.one.one/cdn-cgi/trace
```

Use 20 new connections per guest and family, for 40 per family. Require HTTP
200 and actual ingress attribution. Decode with
`tcpdump -nn -tt -S -r "$CAPTURE_FILE"`. Correlate TCP tuples and sequence
numbers across transit and provider ingress, including translated addresses.
Compare provider counts with eligibility, tiers, weights and hash mode. The
accepted equal-weight random calibration permits 13 through 27 of 40 per
provider. Review bounds for different weights or hash modes before execution;
do not reuse that range for a different policy.

Run each mapping operation from its provider side:

```sh
ip -4 -j route get "$MAPPED_ADDRESS" from "$PROVIDER_SOURCE4"
curl -4 --interface "$PROVIDER_SOURCE4" --noproxy '*' --silent --show-error --fail --http1.1 --header 'Connection: close' --max-time 14 --write-out '\nMWAN-RESULT:%{json}\n' "$MAPPING_URL"
```

Require HTTP 200 and the expected response hash. Correlate external request,
mapped internal request and reply. Preserve IPv4 independence and IPv6
translation after assignment changes. Gateway-only probes, requests without
attribution and missing capture counters cannot establish complete acceptance.

#### Verify route repair, persistent history and cleanup

1. Refresh the exact owned route before its authorized testbed fault. Keep all
   four downstream streams active. Record the complete numeric route identity
   and deletion arguments. The accepted main-table Webpass example was:

   ```sh
   ip -4 route del default via 10.241.204.1 dev enwebpass0 table 254 proto static metric 10
   ```

   Treat this as a testbed example, not a production default. The later approved
   provider-table fault used its separately verified identity. Do not select a
   different route merely to force a particular readiness transition.
2. Compare kernel absence and restoration with served state and actual product
   history. Read current history with `cat -- /var/log/mwan-ifmgr.jsonl`. Preserve
   event IDs, timestamps, connection, family, dependency, reason and state before
   restart. Require detailed failure/repair history, not an unobserved IPv4
   not-ready transition that prompt repair never produced. Preserve the approved
   90-second history window; it is not an outage limit.
3. Run `systemctl restart mwan-ifmgr@wan.service`. Require changed MainPID,
   active service, recovered packets and identical recorded history events after
   restart. A ready status or successful exit does not prove history retention.
4. If automatic repair fails, restore only the refreshed deleted object. For the
   preceding example, the recorded restoration command was:

   ```sh
   ip -4 route add default via 10.241.204.1 dev enwebpass0 table 254 proto static metric 10
   ```

   Stop further faults and use the reviewed reverse configuration or hypervisor
   recovery appropriate to actual guest/verdict state. Do not restore an already
   repaired route or change unrelated provider objects.
5. Stop each exact owned capture with `systemctl stop "$CAPTURE_UNIT"`, reap its
   waiting SSH process and require a successful terminal result and zero kernel
   drops. Check its recorded PID on the capture host with
   `find /proc -maxdepth 1 -mindepth 1 -name "$CAPTURE_PID" -print`; require empty
   output. Unit collection alone does not prove process absence. On each guest,
   run `ps -p "$OWNED_PING4_PID,$OWNED_PING6_PID" -o pid,args`. Require both exact
   PIDs and their recorded arguments before running
   `kill -INT "$OWNED_PING4_PID" "$OWNED_PING6_PID"`. Retain transmission
   summaries and require each exact PID to be absent with the same `/proc` check.
   Signal only the recorded local observer process with
   `kill -INT "$OWNED_OBSERVER_PID"` and reap its transport. Refresh every PID
   before execution.
6. Restore ordinary simulator traffic and compare final owners, assignments,
   routes, translation, mappings, BGP, management and both downstream families.
   Record passed, failed and unperformed results separately. Preserve command
   records, captures/counters, event logs, recovery and final identity as the
   execution's manifest evidence.

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

Hand off the merged protocol runner and configuration revisions, complete manifest,
measured baseline and interruption bounds, scenario results, and packet
artifacts. Separate isolated tests, shared testbed acceptance, and production
acceptance in the execution ledger.
