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
5. Keep provider names, ASNs, public prefixes, and tunnel protocols out of
   firewall branching. Production service details are configuration values.
6. Preserve the current forwarding policy during this ownership change.
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

### 1. Generate firewall rules from typed configuration

This task implements the rule generator and MWAN-355. It depends on the
completed translation model.

Files:

- Modify [networkjson.go](../../gateway/internal/networkjson/networkjson.go) and [ifmgr_modules.go](../../gateway/internal/config/ifmgr_modules.go).
- Revise [the steering schema](../../gateway/internal/yangpub/schema/goodkind-mwan-steering@2026-09-26.yang) and [schema.go](../../gateway/internal/yangpub/schema.go).
- Modify [ifmgr_module_configs.go](../../gateway/cmd/mwan/ifmgr_module_configs.go) and [wanconfig_publish.go](../../gateway/cmd/mwan/wanconfig_publish.go).
- Extend the published `Gateway`, `GroupSettings`, and `ConfigItems` in [tree.go](../../gateway/internal/wanconfig/tree.go).
- Extend [the publication roundtrip test](../../gateway/cmd/mwan/wanconfig_roundtrip_test.go) and [the public selftest](../../gateway/cmd/mwan/wanconfig_selftest.go).
- Modify [the valid model instances](../../gateway/yang/instances).
- Create `gateway/internal/firewall/config.go`, `rules.go`, `apply_linux.go`, and `inspect_linux.go` in the MWAN repository.

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
7. Add firewall validation to the existing `debug` command.
   `mwan debug check-firewall <network.json> <schema-dir>` validates the
   document, creates a private network namespace,
   uses the production firewall writer, reads back the result, and reports
   any mismatch. It must never apply rules in the caller's namespace.
   Its output includes a normalized ruleset for review.
8. Keep offline validation explicit about dynamic state. It proves the
   configured firewall and table definitions without inventing a delegated
   prefix or a healthy provider. Runtime NPT and steering state receive their
   existing packet tests and the live checks in Task 4.
9. Run the firewall check in the existing namespace suite. Do not add the
   schema-dependent command to the `CGO_ENABLED=0` namespace runner.

Verification:

1. Run `mwan debug check-network <network.json> <schema-dir>` against both
   rendered environments.
2. Run `make -C gateway test-firewall`. Expect the public check to reject invalid
   configuration and accept valid rules in its private namespace. Verify
   that the caller's rules remain unchanged. The private namespace ends
   when the command exits.
3. Keep a reviewed normalized reference for each environment alongside these
   tests. Compare it with the command's kernel readback, excluding handles,
   counters, and refresher-owned elements. Complete packet acceptance in
   Task 2 after the daemon can apply the new configuration.

### 2. Install protective rules before configuring links

This task implements MWAN-325 and daemon support for MWAN-330. It depends
on Task 1.

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
   systemd readiness and networkd ordering in the systemd-tagged suite.
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
