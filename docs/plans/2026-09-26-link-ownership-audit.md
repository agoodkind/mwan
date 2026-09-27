# MWAN-305 plan validation, September 26, 2026

The plans are NOT-READY for unattended execution. The approved design agrees
with prior decisions, but eight plan corrections remain. The operator also
requires the managed Astound testbed connection to be restored first under
[MWAN-524](https://tack.home.goodkind.io/browse/MWAN-524). This audit changed
no running service and performed no deployment.

## Reviewed scope

The review covered the interface specification, coordinator, ledger, six work
plans, linked provider specifications, and every existing MWAN-305 child.
Independent agents reviewed MWAN source, Configs source, and Tack/Clyde
history. The coordinator inspected live guests and combined the findings.

| Source | Reviewed identity |
| --- | --- |
| Documentation | The branch was `codex/mwan-network-model` at `3abbafa16ddabe309d7fd76e52f13d16415e035d`. |
| MWAN remote main | The revision was `03dd43a392068403eabc2e00c4e048fc632331ed`. |
| Configs remote main | The revision was `18f2a71176ed4a95306d122df780e25a4b9601c8`. |
| Configs checkout | The revision was `52056eccbcf6122191b9c8e7af7bb00b4558c51b`; source findings used remote main. |
| Live binaries | Production VM 113 and testbed VM 213 reported clean commit `ac1a835`, binary hash `5e97a31479c8`. |
| Tack | The original 46 children comprised 15 Todo, 22 Done, and nine Cancelled. MWAN-524 increased Todo to 16. |

The branch changes documentation only. A merge-tree check against fetched
MWAN main completed without conflicts. This establishes merge compatibility
at those revisions, not implementation readiness.

## Findings

### F1: Astound requires baseline restoration

[BLOCKER for cutover] VM 213's deployed provider configuration excludes
Astound. Its device, MAC `bc:24:11:a5:70:06`, is `ens23`, DOWN and unmanaged,
with no matching networkd unit. Simulator LXC 903 is running.

[MWAN-331](https://tack.home.goodkind.io/browse/MWAN-331) deliberately removed
Astound after proving configuration-only addition, tier changes, and removal.
Its completion evidence records Configs PR #469 at `ab0d4723`, deleted
generated units, and an unmanaged device after reboot. This explains the
current state. Preserve MWAN-331 and MWAN-491 as historical Done work.

The operator now requires Astound to remain managed. MWAN-524 requires
restoration through the existing provider model and merged configuration,
with real downstream IPv4, selection, balancing, and restart acceptance.
MWAN-519 now depends on this repair.

### F2: DHCPv4 migration omits the failover consumer

[SHOULD-FIX before DHCPv4 implementation] The acquisition plan accounts for
OOB, the out-of-band management connection, but omits `mainv4`. The failover
role selects this module. It consumes shared DHCP events, installs an address
and default route, and removes only the default on expiry. The plan needs
changed and expired lease coverage through the real failover daemon role.

Evidence: [acquisition plan](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/plans/interfaces/acquisition.md#L118-L147),
[role selection](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/roles.go#L66-L71),
and [mainv4 acquisition and expiry](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/mainv4/mainv4.go#L78-L164).

### F3: Address transfer omits NPT's IPv6 writer

[SHOULD-FIX before address implementation] NPT, IPv6 network prefix
translation, constructs and reconciles each external prefix's `::1/128`
address. The kernel plan explicitly transfers only mapped addresses from
WAN routing. The plan must also transfer this writer while preserving
configured-prefix and delegated-prefix behavior.

Evidence: [kernel plan](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/plans/interfaces/kernel.md#L110-L124),
[NPT reconciliation](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/npt/npt.go#L217),
and [address construction](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/npt/npt.go#L442-L466).

### F4: DHCPv6 interface addresses need explicit translation treatment

[SHOULD-FIX before DHCPv6 address implementation] The plan separates IA_NA,
DHCPv6 interface addresses, from delegated prefixes. Existing NPT code
classifies every other global IPv6 `/128` as an inbound forwarding address
and translates its destination to the OPNsense edge. That rule can redirect
traffic intended for a new local assignment. Contract review must distinguish
local assignments from intentional forwarding addresses. Real inbound packet
tests must cover both without removing existing forwarding behavior.

Evidence: [DHCPv6 plan](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/plans/interfaces/acquisition.md#L380-L407),
[address classification](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/npt/npt.go#L507-L533),
and [destination translation](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/npt/rules.go#L122-L130).

### F5: Kernel IPv6 settings must follow the active owner

[SHOULD-FIX before Configs implementation] The deployment plan calls current
Webpass settings a conflict and places their replacement in configuration
preparation. Both live gateways disable kernel RA and autoconfiguration while
networkd processes router advertisements in userspace. Live networkd logs
show router discovery and address configuration. The zero kernel settings
alone do not establish a current defect. Preparation must preserve that owner.
Kernel RA/SLAAC activation must follow networkd acquisition shutdown during
exclusive transfer.

Evidence: [deployment plan](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/plans/interfaces/deployment.md#L43-L62),
[sysctl template](https://github.com/agoodkind/configs/blob/18f2a71176ed4a95306d122df780e25a4b9601c8/mwan/config/sysctl-mwan.conf.j2#L11-L17),
and live observations below.

### F6: The downstream-client source reference is wrong

[SHOULD-FIX before harness implementation] The deployment plan assigns
reusable downstream clients to the VM declaration file. Existing clients
are declared in
[mwan_test_clients.tf](https://github.com/agoodkind/configs/blob/18f2a71176ed4a95306d122df780e25a4b9601c8/opentofu/suburban/mwan_test_clients.tf#L8-L41).
The source map needs this file. The VM declaration remains relevant for
gateway and router infrastructure.

### F7: Restart acceptance omits the required OOB case

[SHOULD-FIX before restart implementation] MWAN-518 requires compatibility
with existing OOB users of the shared DHCPv4 client. The
[restart tests](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/plans/interfaces/restart.md#L96)
do not explicitly exercise that role. Acceptance needs OOB restart and expiry
through the production daemon and a real DHCP server.

### F8: Ticket completion prevents the first transfer

[SHOULD-FIX before execution] The
[cutover prerequisites](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/plans/interfaces/cutover.md#L41)
require completed MWAN-521. That ticket requires a merged testbed deployment
proving transfer and reversal, which MWAN-519 performs. The prerequisite must
require merged implementation and isolated mechanism acceptance. MWAN-521's
live acceptance must be recorded during MWAN-519.

### F9: Linked specifications contain superseded current claims

[SHOULD-FIX before delegation] The linked
[translation model](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/superpowers/wanconfig/model.md#L279-L283)
asserts fixed-length, stateful prefix translation. The
[provider specification](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/superpowers/wanconfig/providers.md#L520-L524)
requires configured prefixes for IPv6 source rules. The
[configuration specification](https://github.com/agoodkind/mwan/blob/3abbafa16ddabe309d7fd76e52f13d16415e035d/docs/superpowers/wanconfig/spec.md#L5-L11)
asserts that no model exists and a fourth provider is rejected.

Current code uses the BPF translator, live translation prefixes for source
rules, and an unrestricted provider inventory with collision checks. These
pages need explicit historical context or corrected current claims.
Evidence: [translator initialization](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/npt/npt.go#L109-L115),
[source rules](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/ifmgr/modules/wanroutes/wanroutes.go#L655-L663),
and [provider inventory](https://github.com/agoodkind/mwan/blob/03dd43a392068403eabc2e00c4e048fc632331ed/internal/networkjson/networkjson.go#L340-L377).

## Verified design and prerequisites

The approved design remains complete-connection transfer, fresh initial DHCP
with preserved identity, persistence of MWAN leases, and kernel router
discovery and SLAAC. New 802.1X integration remains excluded. Existing AT&T
service remains until retirement. MWAN-507 remains separate and additive,
with external IPv6 BGP and ordinary IPv4 unaffected.

The plans preserve real public-boundary tests, focused PRs, four short Graphite
stacks, merged-only deployment, testbed before production, downstream
observation without OOB, balancing, and reconnect. The runner bootstrap
precedes protocol acceptance without an observed dependency cycle. Exact
interfaces still require the planned contract review.

At inspection, MWAN PRs #38 through #44 remained open. This audit established
no merged or deployed proof for daemon firewall runtime and startup. The
plans correctly require MWAN-341 protection before transfer. Current Configs
main already includes reconnection around network operations.

## Live observations and limits

Read-only observations were collected September 26, 2026, approximately
21:53 through 22:00 PDT. Both gateways reported active interface manager,
agent, and networkd services, with zero recorded service restarts. Networkd
and direct daemon manipulation still coexist.

Both Webpass interfaces had their IPv4 blocks and preserved IPv6 edge
addresses. Production also showed acquired IPv6 addresses. Both reported
kernel `accept_ra=0`, `autoconf=0`, and IPv6 forwarding enabled. Networkd logs
showed advertisement processing. Source rules used active provider prefixes,
consistent with completed MWAN-333.

| Observation | Result |
| --- | --- |
| Production downstream `debianct` | Three IPv6 probes to `2606:4700:4700::1111` succeeded. The guest had one active network interface and its default via OPNsense. |
| Testbed downstream guest 224 | Three IPv6 probes to that target succeeded. Its default used OPNsense. |
| Test clients 225 and 226 | Both were running. Client 225 had downstream IPv4 and IPv6 configuration; direct SSH attempts timed out. |
| Simulators | LXCs 900 through 904 were running, including Astound 903. |
| Production AT&T | Authentication and VLAN setup services were active. |
| Production configuration SHA-256 | The network configuration hash was `7a58263b9795f949dd2c65a16c338b9c1ac1a993a4785d45e1c8457f37340e50`. |
| Testbed configuration SHA-256 | The network configuration hash was `2d658685b82d4fa8e8c63246f02055d1f59615248b95244860f41ba7ca284c78`. |

These probes establish limited downstream IPv6 availability. They do not
prove IPv4, balancing, failover, the selected primary gateway, or migration
acceptance. This audit ran no packet captures, injected failures, restarts,
builds, or implementation tests. Agent Gate rejected nested `pct exec`
access; direct guest SSH provided the limited observations instead. The
successful sysctl query used `/usr/sbin/sysctl` after `/usr/bin/sysctl` was
absent. Configuration hashes do not establish a Configs Git commit.

## Conversation and ticket evidence

Clyde searches used `load_rules: "v1;"`. Conversation A is
`codex:01a0e0c4-eb44-7d82-a19f-071c890c7e3f`; B is
`codex:01a0cbc7-f6a2-7fe3-ac07-f73aca57436c`; C is
`claude:0f864a72-308d-4cdf-9f6f-f3e7989d10c8`.

| Requirement | Clyde evidence |
| --- | --- |
| Whole-connection transfer, fresh DHCP, persistence, and kernel SLAAC were approved. | A104 proposed the design; A105 approved it. |
| AT&T retirement permits omitting new authentication integration. | A138 delegated the choice; A139 selected omission. |
| Tests use public boundaries and real behavior. | B282 states this preference. |
| Detailed failure history remains within focused scope. | B206 and B218 establish both requirements. |
| External BGP is IPv6-only and policy is configurable. | B84, B1, and B179 establish these constraints. |
| MWAN-507 adds capabilities without reworking firewall ownership. | B8143 requests this; B8154 and B8155 identify MWAN-341. |
| Source routing follows live prefixes and removes stale rules. | B7655 approves this behavior. |
| Code precedes deployment; agents and focused PRs have explicit responsibilities. | B434, A147, and A163 establish this ordering. |
| Deployment requires merged commits, testbed before production, and reconnect. | B4189, B3589, B7418, and B7420 establish these gates. |
| New ISP production exercises remain deferred until after October. | B52 states the limitation. |
| Astound was removed after acceptance. | C6617 agrees with MWAN-331. |

All 46 original epic children were read, including completed and cancelled
work. Historical dual-write proposals do not override approved exclusive
ownership. Tack search reported temporary unavailability; this audit cannot
exclude a duplicate Astound ticket outside the inspected epic and known
provider tickets. MWAN-524 records the new restoration requirement while
preserving earlier completion history.
