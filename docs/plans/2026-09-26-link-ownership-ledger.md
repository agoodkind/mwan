# MWAN-305 execution ledger

Record implementation and deployment evidence against the
[coordination plan](2026-09-26-link-ownership.md). Update this file after each
completed PR or deployment phase and before a context handoff. Preserve exact
commands, commit and configuration revisions, observed results, failed
checks, and remaining work. Never mark a runtime result complete from a
documentation change.

## Current work

September 26, 2026: The specification records the approved migration design.
The coordination plan groups 15 migration tickets into six work plans. Their
tasks have implementation or operational instructions and explicit review requirements.
Tack contains the parent and ticket dependency relationships. Slice ordering
comes from the coordination plan. This planning work changed no runtime code
and performed no deployment.

The subsequent audit created urgent MWAN-524 for the operator's required
Astound baseline restoration. At audit time the epic had 16 Todo children,
22 Done children, and nine Cancelled children. MWAN-519 depends on MWAN-524. Historical
MWAN-331 and MWAN-491 remain Done. The audit changed no running service.

The operator subsequently confirmed AT&T will retire and delegated the
migration choice. Omit 802.1X integration from the new manager. Preserve the
existing AT&T setup until retirement is confirmed. Generic VLAN support
remains required. MWAN-400 must verify that AT&T no longer requires networkd
before removing it globally. No live service was changed.

September 27, 2026: [MWAN PR #48](https://github.com/agoodkind/mwan/pull/48)
merged the specification, six slice plans, coordination plan, and corrected
audit as `8d26c5ab078061125bfd91806332622b35fefc26`. The review ended at
signed head `7906f5c45b901344fd71d3e8109e99e2581f3f93`. The independent
plan review found no remaining conflict after the audit correction. The
automated reviewer approved the final head. All required checks passed, and
no review thread remained open. This merge changed documentation only. No
MWAN-305 interface ownership code or live gateway configuration changed.

## Track slice execution

MWAN-524 passed implementation, review, merge, and live testbed acceptance.
MWAN-516 identity work and MWAN-505 route repair have begun in separate local
worktrees. Neither has a submitted code PR or runtime acceptance yet.

| Ticket | Slices | Current execution state |
| --- | --- | --- |
| MWAN-524 | Restore the managed Astound testbed connection first. | Configs PR #522 merged. Deployment, restart, downstream traffic, balancing, and recovery passed. |
| MWAN-516 | 516-model; 516-state | Connection identity implementation has begun in `codex/mwan-516-interface-model` from `5970f1d`; no code PR or acceptance exists. |
| MWAN-397 | 397-links | Execution has not started. |
| MWAN-523 | 523-observation | Execution has not started. |
| MWAN-398 | 398-addresses; 398-dhcpv4 | Execution has not started. |
| MWAN-227 | 227-delegation | Execution has not started. |
| MWAN-517 | 517-autoconfiguration; 517-dhcpv6 | Execution has not started. |
| MWAN-518 | 518-restart | Execution has not started. |
| MWAN-505 | 505-route-repair | Source review traced the route and rule deletion mechanism at `8d26c5a`; reproduction and implementation have begun in `codex/mwan-505-route-repair`. No code PR or acceptance exists. |
| MWAN-521 | 521-configuration; 521-deployment | Execution has not started. |
| MWAN-522 | 522-acceptance | Execution has not started. |
| MWAN-519 | 519-first-connection | Execution has not started. |
| MWAN-399 | 399-remaining-connections | Execution has not started. |
| MWAN-400 | 400-retirement | Execution has not started. |
| MWAN-401 | 401-testbed | Execution has not started. |
| MWAN-520 | 520-production | Execution has not started. |

## Resume the work

MWAN-524 restored Astound as a permanently managed testbed connection.
The September 27 plan revision corrected the eight audit findings. Continue
the 516 identity and 505 route repair slices against current source. Review
their implementation commits and public-boundary tests before merging. No
interface-owner cutover has begun.

For every handoff, record the slice, agent responsibility, exact source
revision, agreed interfaces, owned files, current PR, last passing check,
remaining acceptance, next action, and prerequisite evidence.

## Record plan review

| Date | Reviewed revision | Review scope | Verdict | Evidence |
| --- | --- | --- | --- | --- |
| September 26, 2026 | `codex/mwan-network-model` at `3abbafa16ddabe309d7fd76e52f13d16415e035d` | Specification, coordinator, six plans, linked historical specifications, all 46 existing epic children, current MWAN and Configs source, Clyde decisions, and read-only live state. | NOT-READY for unattended execution. One baseline blocker and eight plan corrections remain. | The [audit](2026-09-26-link-ownership-audit.md) distinguishes static findings, live observations, and missing proof. MWAN-524 was added after reviewing the existing children. |
| September 27, 2026 | `7906f5c45b901344fd71d3e8109e99e2581f3f93` | Corrected F2 through F9, reconciled the current MWAN and Configs source, and reviewed the plan dependencies and acceptance gates. | Plans ready for implementation review. Runtime behavior remains unproved. | MWAN PR #48 merged as `8d26c5ab078061125bfd91806332622b35fefc26` after required CI and AI review. |

## Record each implementation result

### MWAN-524 acceptance, September 26, 2026

[Configs PR #522](https://github.com/agoodkind/configs/pull/522) restored only
the testbed Astound provider entry. Signed commit
`a74c701306861c4c53b171250b5156cbcb2a82fe` merged as
`94848bff5be240e69f8ebd1e5ea171a8aca5ef42`. Independent source review found
no blocker. The automated PR reviewer approved the submitted commit.
Every required CI check passed. No review thread remained unresolved.

Local lint passed. The existing install suite reported 26 examples, zero
failures, and one pending compatible-loader case. The actual deployed
`ac1a835` loader separately accepted the rendered configuration: five providers,
zero rejected. An initial local render using translation mode `disabled`
failed validation before installation. The corrected IPv6 translation mode
is `native`. DHCPv6, RA, and IPv6 probes remain disabled for Astound.

The supported command was
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers`, run from the
clean merged Configs commit. The recap reported 212 successful tasks,
32 changes, zero unreachable hosts, and zero failures. Release
`202609262039-1e-ac1a835` remained installed with binary hash `5e97a31479c8`.
The deployed network configuration SHA-256 was
`1cf6297af00eca24c1f0f612d23f4525d7c122c17ae7f72b3bd7d718efef5a16`.

VM 213 rebooted from boot ID `abe05ae4-0c91-48da-a7e7-34ff4a0ba924` to
`5c8750fc-3adf-4fdf-ade4-4edd20a78f72`. The deployment verdict for trace
`20260926-225735-deploy-86429` reported reboot, egress, and mapped-address
return codes of zero. Snapshot `pre-deploy-20260926T225722` was retained.
Rollback was unnecessary. Production was unchanged.

After reboot, networkd managed MAC `bc:24:11:a5:70:06` as `enastound0`.
It acquired `10.240.207.2/24` from `10.240.207.1`. IPv4 table 700 contained
the default via that gateway. Mark 5 selected that table. Astound had
only link-local IPv6, no IPv6 default, and no IPv6 steering eligibility.
All five providers reported healthy. Networkd, nftables, and the interface
manager were active.

Clients 225 and 226 generated traffic through OPNsense with one network
interface each and no OOB route. Six baseline HTTPS requests passed before
deployment and six more passed after reboot. The controlled fault lowered
only simulator uplinks `veth900i1` and `veth901i1` at about 23:06 PDT. Verified
systemd timers would restore both after 600 seconds. Webpass became unhealthy
at 23:07:42 and AT&T at 23:08:20. The IPv4 selector then used Monkeybrains and
Astound at equal weight. IPv6 selected Monkeybrains alone.

The sample made 100 fresh IPv4 HTTPS connections to `1.1.1.1` from the two
clients. Every request returned HTTP 200. Captures at simulator ingress
`veth902i0` and `veth903i0` counted distinct TCP SYN sequences and matching
SYN-ACKs: 49 connections used Monkeybrains and 51 used Astound. Both counts
satisfied the planned range of 35 to 65. Neither capture dropped packets.

Both uplinks were restored by 23:11:12. The restoration timers were stopped
after verifying both links were up. All providers recovered. Twenty fresh
HTTPS connections then succeeded: 12 used AT&T, eight used Webpass, and zero
used Astound. Fresh IPv4 and IPv6 probes each passed five of five.

One-second probes from client 225 measured a 45.069-second interval between
IPv4 replies and a 43.020-second interval between IPv6 replies around the
reboot. Before reboot, the network reload had intervals of 5.084 and 6.115
seconds. Earlier observations included delayed replies and a 64.021-second
IPv6 reply gap before Astound installation. This run did not establish its
cause. Packet-loss counts account for replies received out of sequence.

During the deliberate uplink failure, an existing IPv4 ping sequence stayed
unsuccessful until 23:11:23, after uplink restoration. A fresh IPv4 ping
passed three of three while those uplinks were still down. All 100 fresh
HTTPS connections also passed during that period. IPv6 probes resumed at
23:08:52. Acceptance establishes new-connection selection and balancing.
It does not establish seamless survival of existing sessions across failure.

Gateway observations used `qm guest exec 213`. Packet captures used
`tcpdump` on simulator ingress before masquerade. Downstream commands used
SSH through Suburban to the clients' IPv4 addresses. The maintainer merged
the plan corrections in MWAN PR #48. Implementation and live cutover remain pending.

## Record future implementation results

1. Record the ticket and slice, PR, signed commit, merge result, and exact checks.
2. Record independent review and its reviewed commit separately from local checks.
3. State the observable behavior demonstrated and any missing acceptance.
4. Identify the next unfinished task and its prerequisites.

## Record each deployment result

1. Record the environment, release, configuration revision, and connection.
2. Record ownership before and after, deployment commands, and reconnect result.
3. Record downstream packet checks, balancing observations, interruption, and
   recovery times separately for each address family.
4. Record reverse-transfer evidence and the recovery release/configuration.
5. Update the relevant ticket only to the state supported by that evidence.
