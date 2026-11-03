# MWAN-341 firewall implementation plan

## Goal

Make the gateway daemon install and maintain its firewall rules from the
shared configuration. Preserve the deployed translation and steering
behavior. Permit MWAN-507 to add direct BGP and tunnel connections through
additional configuration and rule inputs.

Implement the behavior in the [firewall specification](../firewall.md).
This plan covers MWAN-325, MWAN-330, MWAN-335, MWAN-354, and MWAN-355.

Before execution or resuming after context loss, read the
[epic goal and standing rules](2026-09-26-mwan-341-goal.md).
Follow its mandatory language rules and ledger procedure throughout the epic.

## Current behavior

The starting point is MWAN-340 and MWAN-333 deployed to both gateways.
This plan was checked against mwan commit ac1a835 and configs commit
6213f094 on September 26, 2026.

Configs renders three groups of rules: filtering, IPv4 translation, and
packet marking. Its ruleset also creates empty IPv6 NAT chains. NPTv6
prefix translation already uses TC/eBPF, a kernel packet program. The
translation module writes only the IPv6 edge exceptions into nftables.
The steering module creates and maintains its own table.

The daemon starts before device discovery and networkd, but uses
`Type=simple`. Startup renders network files and can reload networkd before
the first module reconciliation. Management interface and service-port
configuration must become available to the daemon before it can replace the
early firewall service.

The deployment gate still accepts its first successful IPv6 probe. IPv4 is
reported but does not determine that result. This behavior must change before
the firewall file is removed.

## Constraints

1. Preserve MWAN-340's translation fields, TC/eBPF programs, IPv6 edge
   exceptions, and per-family readiness. Preserve MWAN-333's live-prefix
   source rules.
2. Keep one rule writer per table. The new firewall module owns `inet filter`,
   `ip nat`, and `inet mangle`. NPT owns `ip6 nat` and TC attachments.
   Steering owns `inet mwan_steer`. The destination refresher owns only its
   set elements.
3. Keep startup independent of ISP readiness and optional management
   publication. Preserve the existing early boot ordering.
4. Use real packet and command boundaries for new tests. Reuse the existing
   namespace suites. Add no test suite that merely inspects builder calls or
   copies implementation constants.
5. Finish the implementation before deployment. Keep PRs limited to one
   coherent change. Deploy merged release commits through Configs, first to
   the testbed and then to production after the required acceptance passes.
6. Keep provider names, ASNs, public prefixes, and tunnel protocols out of
   firewall branching. Production service details are configuration values.
7. Preserve the current forwarding policy during this ownership change.
   Ordinary Sonic and Astound service remains independent of MWAN-507.

## Interfaces for MWAN-507

Define these typed inputs in the new firewall package. Project the existing
configuration into them once, before generating rules.

| Input | Required data and meaning |
| --- | --- |
| `TransportPermit` | It specifies the input interface, packet address family, optional source and destination prefixes, IP protocol, and optional TCP or UDP ports for traffic addressed to the gateway. |
| `ForwardingPath` | It specifies internal and external interfaces, enabled address families, and the existing forwarding policy. It does not require a physical device type. |
| Translation policy | It reuses the current typed IPv4 and IPv6 policies on each connection. It does not contain BGP or tunnel settings. |
| Eligibility | It reuses the existing per-family routing and translation result. The firewall does not maintain another health state. |

Use `TransportPermit` for today's management services, DHCP, and internal
routing traffic. Match interface names before the interfaces exist. Keep
transport family separate from forwarded family. Protocols without ports
must be representable without inventing a TCP or UDP port.

MWAN-507 will project its shared peer and tunnel configuration into these
inputs. It will add route and session dependencies to the existing eligibility
decision. It must not introduce a second firewall file, table writer,
translation mode, or startup sequence. The rule compiler must not query BGP
or tunnel health before permitting the packets that establish them.

Peer and tunnel settings remain authoritative in the routing model.
Do not create a second user-configured copy of those settings under firewall
configuration. The typed inputs above are internal projections, not a second
configuration format.

The integrated direct BGP and tunnel tests remain part of MWAN-507.
MWAN-341 proves the reusable packet matching and logical-interface behavior
with real namespace traffic. It does not choose a production tunnel protocol.

## Tasks

### 1. Require both address families in the deploy gate

This task implements MWAN-354 and can be reviewed independently.

Files:

- Modify [deploygate.go](../../gateway/cmd/mwan/deploygate.go).
- Create `gateway/cmd/mwan/deploygate_egress_netns_test.go` in the MWAN repository.
- Modify [the MWAN Makefile](../../gateway/Makefile).
- Modify [deploy-mwan.yml](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-mwan.yml) to pass the required families and consecutive-success count.

Behavior:

The gate must require every address family configured for the deployment.
A disabled family does not become an invented requirement. Both existing
gateways require IPv4 and IPv6. A failed round resets the success count.
Use three consecutive rounds for the initial Configs setting.

Steps:

1. Change `checkEgress`, `waitEgress`, and their callers to use one
   explicit family requirement. Preserve their existing timeout budget and
   hypervisor-local execution.
2. Make each round test every required family. Accept the deployment only
   after the configured number of complete successful rounds.
3. Preserve the trace identifier, reboot verdict, mapped-address checks,
   result collection, and existing rollback decisions.
4. Execute `mwan deploy-gate wait-egress <seconds>` in an isolated Linux
   network namespace. Route its probe addresses to a second namespace
   that answers real ICMP requests. Block only IPv4 echo, restore it, and
   use packet captures or per-round logs to verify the failed verdict and
   reset of the consecutive-success count. Keep both namespaces isolated
   from the public Internet.
5. Add `make -C gateway test-firewall` using the existing native Linux builder with
   namespace privileges and the real schema libraries. Run this CLI test
   there. Task 2 extends the target for schema-dependent firewall checks.

Verification:

1. Run `make -C gateway test`, `make -C gateway check`, and `make -C gateway test-firewall` in MWAN.
2. During Task 5, deliberately break IPv4 translation on the testbed.
   Expect gate failure even while IPv6 probes succeed.
3. Restore translation and rerun the gate. Expect success only after three
   complete successful rounds.

### 2. Generate firewall rules from typed configuration

This task implements the rule generator and MWAN-355. It depends on the
completed translation model.

Files:

- Modify [networkjson.go](../../gateway/internal/networkjson/networkjson.go) and [ifmgr_modules.go](../../gateway/internal/config/ifmgr_modules.go).
- Revise [the steering schema](../../gateway/internal/yangpub/schema/goodkind-mwan-steering@2026-09-26.yang) and [schema.go](../../gateway/internal/yangpub/schema.go).
- Modify [ifmgr_module_configs.go](../../gateway/cmd/mwan/ifmgr_module_configs.go) and [wanconfig_publish.go](../../gateway/cmd/mwan/wanconfig_publish.go).
- Extend the published `Gateway`, `GroupSettings`, and `ConfigItems` in [tree.go](../../gateway/internal/wanconfig/tree.go).
- Extend [the publication roundtrip test](../../gateway/cmd/mwan/wanconfig_roundtrip_test.go) and [the public selftest](../../gateway/cmd/mwan/wanconfig_selftest.go).
- Modify [the valid model instances](../../gateway/yang/instances) and [the public loader test](../../gateway/cmd/mwan/deploygate_checknetwork_test.go).
- Create `gateway/internal/firewall/config.go`, `rules.go`, `apply_linux.go`, and `inspect_linux.go` in the MWAN repository.
- Create `gateway/cmd/mwan/deploygate_firewall.go` and `gateway/cmd/mwan/deploygate_firewall_netns_test.go` in the MWAN repository.

Behavior:

Keep parsing, rule generation, kernel application, and inspection separate.
Use the same generator and applier in startup, reconciliation, and the offline
check. Kernel inspection must compare actual expressions and chain properties,
not just rule comments.

Steps:

1. Add a typed firewall presence container under the existing steering group.
   Its presence requests gateway firewall ownership. Add the missing
   management interface and permitted management service ports. Permit
   optional allowed-source prefixes on management services; migrated values
   retain their current interface restriction. Add the remaining firewall-only
   pin and destination-set settings currently supplied
   to the Configs template.
2. Reuse the existing internal-interface, internal-network, edge-address,
   provider-mark, DSCP, and translation fields. Give each setting one source.
   Preserve the translation schema and existing JSON documents.
3. Define `BaselineConfig` from the fields needed for protective rules.
   Decode this subset from valid JSON and validate it without requiring
   unrelated provider settings. Full schema and semantic validation still
   apply before network changes or the full firewall policy.
4. Define `TransportPermit`, `ForwardingPath`, and a typed desired ruleset.
   Project current configuration into these inputs. Validate family and
   address combinations, interface references, and port/protocol combinations
   before kernel application.
5. Translate the current Configs policy into the generator. Preserve rule
   order, source translation by outgoing interface, static mappings before
   masquerade, connection marks, destination and DSCP pins, and the NPT
   hairpin guard. Preserve steering's actual priority after the IPv4
   destination-NAT pin; the template's old priority comment is stale.
6. Create missing owned tables, chains, and sets before replacing owned
   chain contents. Commit each update atomically. Preserve existing
   destination-set elements and interval/automatic-merging metadata.
7. Add the public command
   `mwan deploy-gate check-firewall <network.json> <schema-dir>`.
   The command validates the document, creates a private network namespace,
   uses the production firewall writer, reads back the result, and reports
   any mismatch. It must never apply rules in the caller's namespace.
   Its output includes a normalized ruleset for review.
8. Keep offline validation explicit about dynamic state. It proves the
   configured firewall and table definitions without inventing a delegated
   prefix or a healthy provider. Runtime NPT and steering state receive their
   existing packet tests and the live checks in Task 5.
9. Extend `make -C gateway test-firewall` from Task 1 with the new CLI test.
   Do not add the schema-dependent CLI to the current
   `CGO_ENABLED=0` namespace runner.

Verification:

1. Run the existing `mwan deploy-gate check-network <network.json> <schema-dir>`
   against both rendered environments.
2. Run `make -C gateway test-firewall`. Expect the public check to reject invalid
   configuration and accept valid rules in its private namespace. Verify
   that the caller's rules remain unchanged. The private namespace ends
   when the command exits.
3. Keep a reviewed normalized reference for each environment alongside these
   tests. Compare it with the command's kernel readback, excluding handles,
   counters, and refresher-owned elements. Complete packet acceptance in
   Task 3 after the daemon can apply the new configuration.

### 3. Install protective rules before configuring links

This task implements MWAN-325 and daemon support for MWAN-330. It depends
on Task 2.

Files:

- Modify [main.go](../../gateway/cmd/mwan/main.go), [ifmgr.go](../../gateway/cmd/mwan/ifmgr.go), and [roles.go](../../gateway/internal/ifmgr/roles.go).
- Modify [the daemon service](../../gateway/cmd/mwan/mwan-ifmgr@.service) and [install.go](../../gateway/cmd/mwan/install.go).
- Create `gateway/internal/ifmgr/modules/firewall/firewall.go` and `nftwatch.go` in the MWAN repository.
- Modify [the NPT applier](../../gateway/internal/ifmgr/modules/npt/applier.go).
- Modify [the live state store](../../gateway/internal/wanstate/wanstate.go) and [live publication](../../gateway/cmd/mwan/wanconfig_livestate.go) for each writer's intended rules.
- Extend [the existing namespace tests](../../gateway/internal/ifmgr/modules) through their production packet paths.

Behavior:

Each module retains exclusive ownership of its tables. Startup applies the
protective filter before any link-file rendering or networkd reload.
Reconciliation orders firewall, NPT, routes, and steering correctly.

Steps:

1. Add a gateway-only bootstrap before full TOML/BGP validation and optional
   sysrepo startup. Resolve the command's configured network-document path,
   decode the validated baseline subset, and apply and inspect protective
   rules when firewall ownership is enabled.
2. If the JSON or baseline fields are invalid, report the error without
   replacing existing rules. If later validation fails, leave the newly
   applied protective rules installed and fail startup. Do not substitute
   guessed management settings.
3. Separate `loadNetworkConfig` parsing from its network-file writes and
   reload. Complete full validation and protective rule application before
   those side effects.
4. Change the gateway instance to `Type=notify` and send readiness after
   protective rules pass inspection and required network files are written.
   Apply this service change only to the gateway instance. Other roles must
   retain their existing startup behavior.
5. Keep `DefaultDependencies=no`, the journal/remount dependencies, and
   ordering before coldplug and networkd. Do not introduce
   `local-fs.target`, `PrivateTmp`, or `StateDirectory` into this early
   service; the recorded boot tests show their dependency cycle.
6. Add the firewall module before NPT in the gateway role. NPT must read
   the installed IPv4 translation rules before publishing translation
   readiness. Preserve the subsequent route and steering reconciliations.
7. Give NPT's applier its own table and base-chain creation. Preserve its
   TC/eBPF application, kernel readback, edge exceptions, and readiness
   publication. The new firewall module must never clear NPT or steering
   tables.
8. Watch deletions of the new module's owned tables and chains. Reuse
   `Env.RequestReconcile` and existing periodic repair. Ignore ordinary
   rule replacement and destination-set element updates as event triggers.
9. Preserve steering's tested atomic chain-priority migration. For other
   incompatible chain definitions, reject the update without partial changes
   and report the affected chain.
10. Publish intended rules by owner in the existing state store. Preserve the
    existing NPT operational data. A failed apply must remain visible and must
    not report readiness.
11. Keep all stop paths free of ruleset deletion.

Verification:

1. Run `make -C gateway test-netns` and `make -C gateway test-firewall`.
   Run the real `mwan ifmgr --role wan` process inside persistent network
   and mount namespaces owned by the test harness. Use temporary configuration,
   schema, and network-file directories. Disable optional sysrepo publication
   through valid configuration. Use supported hand-authored provider links
   to avoid requesting a systemd reload in this namespace test. Verify actual
   systemd readiness and networkd ordering separately in Task 5.
2. Start the daemon with missing WAN links and a valid baseline. Verify
   management traffic and blocked unsolicited traffic without waiting for
   DHCP, NPT, a tunnel, or BGP.
3. Start with valid baseline data and an invalid remaining field. Verify
   failed startup with the protective rules installed. Repeat with invalid
   baseline data and verify the prior kernel rules remain unchanged.
4. Delete the ruleset while the test daemon runs. Verify recreation of all
   owned tables and real IPv4 and IPv6 forwarding without restarting it.
5. Stop and restart the daemon. Verify rule preservation on stop and no
   repeated self-triggered reconciliation on restart.
6. Reuse the existing NPT hairpin, edge-exception, IPv4-readiness, and steering
   eligibility packet tests. Expect their behavior to remain unchanged.
7. Send real packets through the daemon's rules. Verify permitted and rejected
   management traffic, IPv4 mappings and masquerade, native forwarding, and
   marks used by steering. Repeat with renamed interfaces and a logical
   interface without changing the policy.
8. Exercise address-constrained TCP permissions and portless protocol
   permissions. Verify rejection from an unpermitted source or interface.

### 4. Transfer deployment to daemon ownership

This task completes MWAN-330 and MWAN-335. It depends on Tasks 1 through 3.

Files:

- Modify [the JSON renderer](https://github.com/agoodkind/configs/blob/main/mwan/config/network.json.j2).
- Modify [production inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_servers.yml) and [testbed inventory](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_suburban_servers.yml).
- Remove [the gateway ruleset template](https://github.com/agoodkind/configs/blob/main/mwan/config/nftables.conf.j2) after its complete policy is generated by the daemon.
- Modify [the destination refresher service](https://github.com/agoodkind/configs/blob/main/mwan/services/mwan-update-att-pinned-dests.service), [its timer](https://github.com/agoodkind/configs/blob/main/mwan/timers/mwan-update-att-pinned-dests.timer), and [its existing implementation](https://github.com/agoodkind/configs/blob/main/mwan/scripts/update-att-pinned-dests.sh) only as needed for set recovery.
- Modify [the Configs render contract](https://github.com/agoodkind/configs/blob/main/spec/ansible/mwan_install_spec.rb).
- Modify [the agent's critical paths](../../gateway/internal/agent/server.go).
- Remove installation of [the gateway nftables drop-in](../../gateway/cmd/mwan/nftables-override.conf).
- Update [the MWAN operator reference](../mwan.md) and affected procedures under [operations](../ops/README.md).

Behavior:

The compatible release, firewall configuration, and service transition deploy
together. The destination refresher remains the only external writer of set
elements. The gateway is the only role that changes ownership.

Steps:

1. Render the new firewall fields from existing inventory values. Keep the
   existing network and translation values authoritative. Preserve the
   refresher's existing set identities during this cutover.
2. Run the released network and firewall checks before replacing the live
   configuration. A schema check alone does not replace kernel validation.
3. Declare the required nftables and connection-tracking kernel modules for
   boot loading. Verify module load order before the gateway daemon starts;
   retain the daemon's existing capabilities and module-loading restriction.
4. Implement a handoff that preserves installed rules. Before stopping
   nftables, install a temporary service drop-in that clears its inherited
   `ExecStop` command, then reload systemd's unit definitions. Stop and
   disable the service without flushing rules. Start the configured daemon,
   inspect its replacement rules, and mask the retired service. Remove the
   temporary drop-in only after the service is masked. Keep the existing
   snapshot and recovery workflow for a failed handoff.
5. Remove the installed ruleset, obsolete gateway drop-in, deployment task,
   and reload handler. Preserve separate failover-container firewall files.
6. Retain the playbook's merged-commit enforcement and bounded reconnect
   handling around network changes. Add no shell deployment wrapper.
7. Order the destination refresher after gateway readiness and add retry on
   failure. When its sets are recreated, request a refresh through its
   existing service. Keep the daemon from rewriting existing set contents.
8. Update drift detection to hash the authoritative network and daemon
   configuration. Remove the retired ruleset from the expected path list.
9. Replace operator instructions for durable manual rule edits with the
   configuration and deployment procedure. Keep temporary inspection and
   recovery instructions separate from the specification.

Verification:

1. Run `./configsctl lint` and
   `bundle exec rspec spec/ansible/mwan_install_spec.rb`.
2. The render contract must run the released public validators on the
   rendered document. Do not replace this with template-text assertions.
3. Inspect the rendered service dependencies and the handoff task order.
   Expect no gateway reload or stop action after daemon ownership starts.
4. Verify that the failover and hypervisor deployments remain outside the
   new firewall module.

### 5. Deploy and validate the testbed

This task depends on all implementation tasks. Build and merge the code
before starting deployment.

Files:

- Modify [the testbed release pin](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_testbed_all.yml).
- Create `docs/mwan-341-acceptance.md` in MWAN for testbed and production evidence.

Steps:

1. Run `make -C gateway check`, `make -C gateway test`, `make -C gateway test-netns`, and
   `make -C gateway test-firewall` in MWAN. Merge the reviewed changes and verify the
   resulting release and attestations.
2. Merge the compatible Configs changes and testbed release pin. Run
   `./configsctl deploy deploy-mwan --limit mwan_suburban_servers`
   from a clean merged commit.
3. Compare the generated static rules with `nft -j list ruleset` after
   reboot. Compare NPT and steering rules with their published current
   intent. Preserve semantic rule order while excluding kernel handles,
   counters, and changing destination-set elements.
4. Verify the installed service's readiness ordering, actual capabilities,
   kernel modules, mapped addresses, and the masked firewall service.
5. Use the existing downstream test clients. Verify native and translated
   traffic, inbound replies, DSCP and destination pins, fallback, NPT hairpin
   behavior, and independent family exclusion.
6. Send 100 fresh flows per address family with equal-weight providers.
   Require a reply for every flow and 35 to 65 captured selections per
   provider. Capture before simulator masquerade and account for every
   command and flow.
7. Run the IPv4 gate-failure case from Task 1, invalid-configuration cases,
   daemon stop/restart, missing link at boot, and complete ruleset deletion.
   Restore every deliberate failure before the next case.
8. Verify destination-set definitions, retained elements during reconciliation,
   and repopulation after deletion.
9. Record each command, deployed commit, observation, and failure duration.
   Store continuous probe output on the observing testbed host. Losing the
   controller's SSH session must not destroy the measurement.

Expected result:

The testbed passes the firewall, translation, steering, startup, recovery,
and load-balancing checks. The gateway no longer requires the generated
ruleset file. These results do not claim external BGP or tunnel acceptance.

### 6. Promote the accepted release to production

This task depends on completed testbed acceptance.

Files:

- Modify [the production release pin](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_prod_all.yml).
- Add production observations to the Task 5 acceptance record.

Steps:

1. Pin the exact accepted release in a separate reviewed change. Merge it
   before deployment.
2. Start persistent observation from a downstream guest without an
   out-of-band route. Record IPv4 and IPv6 service, the watchdog, and the
   existing load-balancer monitors. Use the hypervisor for management
   observation, not as the sole proof of client connectivity.
3. Run `./configsctl deploy deploy-mwan --limit mwan_servers`
   from a clean merged commit. Preserve the accepted snapshot and rollback
   workflow.
4. Verify the post-reboot rules, installed release, management access,
   downstream IPv4 and IPv6, mapped addresses, provider health, and observed
   balancing. Confirm that the retired service cannot reload a ruleset.
5. Report the measured interruption and recovery using the persistent
   observer's timestamps. Record any missing measurement explicitly.
6. Close MWAN-341 only after its production acceptance is recorded.
   Track MWAN-507's direct BGP and tunnel acceptance separately. New-circuit
   production exercises remain after October 2026.

### 7. Complete configurable firewall and steering policy

The operator reopened this epic for this follow-up. Preserve the accepted
ownership work and prior acceptance records. Keep production settings unchanged
while implementing the configuration contract.

Files:

- Modify [the current steering schema](../../internal/yangpub/schema/goodkind-mwan-steering@2026-10-01.yang), [JSON decoding](../../internal/networkjson/firewall.go), and [shared runtime configuration](../../internal/config/ifmgr_modules.go).
- Modify [firewall compilation](../../internal/firewall/rules.go), [firewall input validation](../../internal/firewall/config.go), and [steering compilation](../../internal/ifmgr/modules/steering/applier.go).
- Extend the configuration publication and public command tests identified in Task 2.

Behavior:

The network document defines operator policy. Go validates that policy and
compiles it into kernel rules. Keep the selection algorithm independent of
whether selection applies to a packet or a connection. Define a connection
as one transport session with stable endpoint addresses and ports. Do not
label the older established-only mark restoration as true per-packet selection.

The proposed configuration separates the assignment algorithm from the
selection unit:

```json
{
  "hash-mode": "random",
  "selection-unit": "connection"
}
```

The proposed selection-unit values are connection and packet. Connection
selection reuses one ISP choice for a transport session. Packet selection
chooses an ISP for each packet. Packet selection requires endpoint addressing
that remains valid through each selected ISP. The new field is not implemented.
Settle its omission behavior and supported translation combinations in Step 2.

Steps:

1. Classify each hardcoded choice as operator policy, protocol requirement or
   compiler behavior. Expose operator policy through typed configuration;
   preserve required protocol handling and kernel implementation details.
2. Settle supported selection modes, omitted-field compatibility and translation
   constraints. Preserve the shared configuration source and avoid raw nftables
   expressions or a second rule language in the network document.
3. Add the settled contract to the schema, loader, shared types, publication,
   firewall writer and steering writer. Validate the complete combination before
   changing kernel state. Preserve DSCP and destination policies, inbound return
   paths, family readiness, weights and provider exclusion.
4. Add the smallest public command and real packet regression for each changed
   behavior. Use the existing namespace and testbed clients; add no mocks,
   static-content checks or private builder-call tests.
5. Implement and review the coherent policy change before deployment. Use one
   focused MWAN PR unless an actual dependency requires a stack. Prepare the
   compatible Configs inputs separately and deploy only merged releases.
6. Validate the accepted modes on testbed with both guest families, unreplied
   TCP requests, established TCP, UDP, balancing, translation, inbound replies
   and recovery. Record unsupported or unperformed combinations separately.
7. Promote only after the required testbed proof. Record any production policy
   change explicitly instead of assuming that omitted fields authorize it.

Verification:

Run the existing public network loader and isolated firewall validator against
accepted and rejected configuration combinations. Require rejected settings to
preserve the prior kernel policy. Verify actual provider selections and endpoint
addresses with packet observations, then repeat the affected cutover acceptance.
Close the reopened work only after its implementation and acceptance are recorded.
