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

MWAN-524 passed live testbed acceptance. MWAN-505 route repair, the first
MWAN-521 deployment preparation, and MWAN-516 identity work merged. The
merged MWAN-505 and MWAN-516 release passed testbed and production
deployments with downstream traffic checks. The MWAN-523 observer foundation
merged and passed a separate testbed deployment and downstream battery.
Its production pin merged and passed production deployment and downstream
checks.
MWAN-523 observation and overflow recovery passed the final testbed and
production checkpoints. Networkd still owns the provider interfaces.
MWAN-530 passed controlled restart checks on testbed and production after
the primary stopped advertising Graceful Restart and waited for forwarding
readiness before announcing either default.
MWAN-516 state publication merged and passed testbed and production acceptance.
The served ownership state remains observational while networkd owns the links.
No interface-owner cutover has begun.

| Ticket | Slices | Current execution state |
| --- | --- | --- |
| MWAN-524 | Restore the managed Astound testbed connection first. | Configs PR #522 merged. Deployment, restart, downstream traffic, balancing, and recovery passed. |
| MWAN-516 | 516-model; 516-state | [MWAN PR #51](https://github.com/agoodkind/mwan/pull/51) merged standalone identity as `959fbd3a65955e8156f2ea6c9bf2c90febef762c`. [MWAN PR #55](https://github.com/agoodkind/mwan/pull/55) merged shared interface intent as `2c6df538fbb1174a9189f3098d6ac458256857f4`. [MWAN PR #84](https://github.com/agoodkind/mwan/pull/84) merged state publication as `b601ab8a5f4f44d09b949ab6132c4287ef9dbbef`. The shared model and state publication passed testbed and production checkpoints. |
| MWAN-397 | 397-links | [MWAN PR #86](https://github.com/agoodkind/mwan/pull/86) merged as `446e76fe590e5dc9bbbcfe6fd875a8752f4e404c`. Configs PRs #548 and #549 passed testbed and production deployment with downstream packets, balancing, failover, and recovery. Tack records Done. |
| MWAN-523 | 523-observation | [MWAN PR #60](https://github.com/agoodkind/mwan/pull/60) merged the observer foundation as `13183ea9d6264beac7b85bfd4e7946b15f7da601`. Configs PRs #533 and #535 passed its first testbed and production checkpoints. MWAN PRs #66, #71, #73, #75, #77, and #79 verified route identity, bridge rebinding, router advertisement lifetimes, index reuse, daemon packet recovery, and duplicate-address failure in privileged kernel tests. PR #81 repaired snapshot replay overflow. Configs PRs #544 and #545 passed final testbed and production acceptance. |
| MWAN-530 | Restart handover | [MWAN PR #64](https://github.com/agoodkind/mwan/pull/64) merged forwarding readiness as `798ee6a8dcb57ef91e7d9656e8a368f3f4bd4412`. OPNsense selected the backup during controlled testbed and production reboots, then restored the primary while downstream replies continued. Tack records Done. |
| MWAN-398 | 398-static; 398-mapped-addresses; 398-dhcpv4 | MWAN PRs #89, #91, and #95 through #99 merged static, mapped/NPT, and DHCPv4 ownership code. The combined release passed testbed and production deployment. Live providers remain under networkd, so owned acquisition and transfer acceptance remain. Tack records In Progress. |
| MWAN-227 | 227-delegation | The DHCPv6 client contract review identified the existing library and required assignment, deadline, and translation boundaries. Runtime implementation and acceptance remain. Tack records Todo. |
| MWAN-517 | 517-autoconfiguration; 517-dhcpv6 | [MWAN PR #102](https://github.com/agoodkind/mwan/pull/102) merged kernel RA policy, [#103](https://github.com/agoodkind/mwan/pull/103) merged its service permission, and [#106](https://github.com/agoodkind/mwan/pull/106) merged observed IPv6 address phases and router validity. Release `202609291301-67-acb81bb` passed testbed and production deployment. DHCPv6 IA_NA and live ownership acceptance remain. Tack records In Progress. |
| MWAN-518 | 518-restart | Execution has not started. |
| MWAN-505 | 505-route-repair | [MWAN PR #52](https://github.com/agoodkind/mwan/pull/52) merged as `18941243f1e8fa3d5623a04440e4d90de796d1f4`. Namespace packet tests and live testbed route and rule deletion checks passed. The release passed production deployment and downstream acceptance. |
| MWAN-521 | 521-configuration; 521-deployment | [Configs PR #527](https://github.com/agoodkind/configs/pull/527) moved MAC discovery before rendering. [Configs PR #558](https://github.com/agoodkind/configs/pull/558) rendered explicit connection IDs and networkd ownership. Configs PRs #559 and #560 pinned and deployed the combined release to testbed and production. Complete role rendering and live transfer remain. |
| MWAN-522 | 522-acceptance | [MWAN PR #93](https://github.com/agoodkind/mwan/pull/93) merged the protocol test runner. [Configs PR #553](https://github.com/agoodkind/configs/pull/553) merged configurable simulator timing. Seven real Kea DHCPv4 namespace scenarios passed. DHCPv6 delegation, IA_NA, assembled protocol tests, and live ownership acceptance remain. Tack records In Progress. |
| MWAN-519 | 519-first-connection | Execution has not started. |
| MWAN-399 | 399-remaining-connections | Execution has not started. |
| MWAN-400 | 400-retirement | Execution has not started. |
| MWAN-401 | 401-testbed | Execution has not started. |
| MWAN-520 | 520-production | The shared model, route repair, observer, state publication, and release `202609291301-67-acb81bb` passed production deployment and downstream continuity checks. Live ownership transfer remains. |

## Resume the work

MWAN-524 restored Astound as a managed testbed connection. MWAN-397 link
management and the combined MWAN-398 DHCPv4 and MWAN-517 kernel-policy release
passed testbed and production deployment without transferring a live link.
MWAN PR #104 fixed the mapped IPv6 test readiness race. MWAN-517 observed
IPv6 state merged and passed testbed and production deployment. The next
protocol work is MWAN-227's shared DHCPv6 client and MWAN-517 IA_NA. Each
protocol slice still needs public daemon tests, then live ownership transfer
and downstream acceptance. Keep networkd on AT&T until retirement is
confirmed.

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

### First code merges, September 27, 2026

[MWAN PR #49](https://github.com/agoodkind/mwan/pull/49) merged the first
ledger update and identity PR boundary as
`58f6faa80bd8c2dc230e4cd36eafea0ad7b0c68b`. Its signed head was
`4cdbd5183329697a876736035256a7114c9537cb`. Required checks and automated
review passed. This change modified documentation only.

[Configs PR #527](https://github.com/agoodkind/configs/pull/527) moved runtime
MAC discovery after VMID resolution and before network document rendering.
Signed commit `e5a7abe6bed8344db40d1cbbc5ebc0a01a1e8937` merged as
`20ad40232fa62394046b1218839e60756a6b7a22`. `./configsctl lint` and
28 install examples passed; one existing optional Linux binary case remained
pending. Required CI, automated review, and independent static review passed.
The template and installed configuration did not change. No deployment ran.

[MWAN PR #52](https://github.com/agoodkind/mwan/pull/52) repaired owned route
and policy-rule deletion. Signed commit
`7f62b9d070408ef72082fe333710c5678e45a1fb` merged as
`18941243f1e8fa3d5623a04440e4d90de796d1f4`. Before the fix, a real daemon
namespace test deleted an owned return route and observed it missing after five
seconds with one-hour periodic reconciliation. After the fix, deleting that
route and an owned rule triggered repair; downstream TCP resumed and an
unrelated route remained. `make docker-make TARGETS="check test"`, four
privileged firewall tests, required CI, automated review, and independent
static review passed. No testbed or production deployment ran.

[MWAN PR #51](https://github.com/agoodkind/mwan/pull/51) assigned stable
connection IDs independently of provider names and ASNs in the served model.
Its signed commits were `b01c858145ebd44bb634a474ace448e37f99114b`,
`202e230451171fbfdd429e51a3d8ff4b55328a30`,
`13abbefe8872c935ec5050b742675824a8e9748e`, and
`0bfca014eb4c6b4b9320d3670c999e6bea047073`. It merged as
`959fbd3a65955e8156f2ea6c9bf2c90febef762c`. The full local check and
test suite passed at the final head. All required CI checks passed. The
automated reviewer approved the final head, all review threads were resolved,
and an independent reviewer found no blocker in the final interface-key fix.
The optional Govulncheck job repeated the GoBGP database result below. This
PR changed no interface owner and ran no testbed or production deployment.

The optional `go / Quality / Govulncheck` job failed on GO-2026-4736 for
GoBGP v4.7.0 on PRs #51 and #52 and on clean main. The
[GitHub advisory](https://github.com/advisories/GHSA-4p9m-8gc4-rw2h) limits
affected versions to 4.3.0 and earlier. The
[upstream fix](https://github.com/osrg/gobgp/commit/583080a7258e22cc884162e15b078771aa2c2c80)
precedes the v4.7.0 tag. The Go vulnerability database currently reports all
v4 versions as affected. The repository ruleset did not require this check.
No scanner result was hidden or changed.

### Merged runtime testbed checkpoint, September 27, 2026

[Configs PR #528](https://github.com/agoodkind/configs/pull/528) pinned
testbed release `202609271802-31-959fbd3` from merged MWAN commit
`959fbd3a65955e8156f2ea6c9bf2c90febef762c`. Configs merged the
testbed-only pin as `fd65687035a8fcc2ef9fd779a9ca587002e10bb0`.
Production retained release `202609271118-2c-f61a4d7`. The release binary
SHA-256 was `0e5725d34c660e3bcf5d3969784ecbb693617ef966b54eb9a2988f42e3d7bf89`.

From clean merged Configs main, the supported check command was
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`.
It reported 176 successful tasks, 18 changes, and zero failures. The apply
command was `./configsctl deploy deploy-mwan --limit mwan_suburban_servers`.
It reported 229 successful tasks, 24 changes, zero unreachable hosts, and zero
failures. Deployment trace `20260927-113103-deploy-666670` passed reboot,
egress, and mapped-address gates without rollback. The active binary reported
`commit=959fbd3 dirty=clean`. VM 213 rebooted. A downstream one-second probe
measured about 6.1 seconds without a reply during daemon restart and about
46.1 seconds during reboot for each address family. These measurements are
separate from the later deliberate uplink fault.

Clients 225 and 226 each had one network interface and no OOB path. Direct
HTTPS requests returned HTTP 200 over IPv4 and IPv6 on both clients after
deployment. From client 225, 100 fresh IPv4 and 100 fresh IPv6 HTTP
connections returned replies with 100 distinct source ports per family.
Simulator ingress captures attributed IPv4 requests to Webpass 48 and AT&T
52, and IPv6 requests to Webpass 56 and AT&T 44. The captures had no overlap
in request indices.

The first controlled fault lowered simulator uplinks `veth900i1` and
`veth901i1` with verified 600-second restoration timers. The generator
started before health detection. Webpass became unhealthy at 18:49:05 UTC;
AT&T became unhealthy at 18:49:48 UTC. The first 40 IPv4 requests failed;
the remaining 60 succeeded after AT&T changed state. All 20 IPv6 requests
ended before AT&T became unhealthy and failed. This sample measures detection
delay, not forwarding after both primary providers became unhealthy.

The repeated fault again lowered the same two uplinks with verified
restoration timers. The health state file reported Webpass and AT&T unhealthy
and Monkeybrains and Astound healthy before traffic generation. From client
225, 100 of 100 fresh IPv4 and 100 of 100 fresh IPv6 HTTP requests received
replies between 19:01:35 and 19:01:40 UTC. Every connection used a distinct
source port. Both uplinks were restored and the timers stopped. Both clients
then returned HTTP 200 for direct IPv4 and IPv6 HTTPS. Webpass and AT&T
recovered by 19:02:16 UTC; all five health states were healthy. The repeated
sample proves forwarding after health convergence. It does not prove that
existing sessions survive a provider failure or that detection is instant.

This checkpoint changed no interface owner. It did not repeat the deployed
owned-route deletion test from MWAN-505. Complete MWAN-505 testbed acceptance
and production promotion remain separate from this traffic checkpoint.

### Shared interface model testbed checkpoint, September 27, 2026

[MWAN PR #55](https://github.com/agoodkind/mwan/pull/55) merged the shared
interface intent and ownership model as
`2c6df538fbb1174a9189f3098d6ac458256857f4`. Its final signed head was
`f9bdf4faad15bfb817ae7a770c4e37783b21b3a7`. The full local check and
test suite passed. Required CI passed, the automated reviewer approved the
final head, and all review threads were resolved. The optional Govulncheck
job repeated the GoBGP advisory database mismatch recorded above. The merge
changed the model and loader, but did not transfer live interface ownership.

The MWAN release workflow published `202609272028-35-2c6df53` from the exact
merge commit. Its `mwan_linux_amd64.tar.gz` SHA-256 was
`1d60f1a8e3dfb83fc80fdb3fecd32084089209142b58ef4e373dd16916b130dc`.
[Configs PR #529](https://github.com/agoodkind/configs/pull/529) pinned that
release only for testbed. The signed pin `a02b44b4` merged as
`770833cccf0bb5bbb933df3c9c0de309fd50c423`. Configs lint, required CI,
and automated review passed. The production release pin did not change.

From clean merged Configs main, the supported check command was
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`.
It reported 176 successful tasks, 18 changes, and zero failures. The apply
command was `./configsctl deploy deploy-mwan --limit mwan_suburban_servers`.
It reported 229 successful tasks, 24 changes, zero unreachable hosts, and zero
failures. Deployment trace `20260927-135055-deploy-679436` passed the
controller reconnect, reboot, egress, and mapped-address gates without
rollback. VM 213 reported `commit=2c6df53 dirty=clean` after the deploy.

Clients 225 and 226 each used one network interface without an OOB path.
Both clients returned HTTP 200 over IPv4 and IPv6 after deployment. Client
225 completed 100 fresh IPv4 and 100 fresh IPv6 HTTP connections, each with
a distinct source port. Simulator ingress captures attributed IPv4 requests
to Webpass 49 and AT&T 51, and IPv6 requests to Webpass 52 and AT&T 48.
All 100 indices per family appeared once across those two providers.

A controlled fault lowered the Webpass and AT&T simulator uplinks with
verified 600-second restoration timers. An early sample of 10 fresh
connections per family failed while both providers still reported healthy.
The health journal then recorded failed probes. The operator restored the
links before AT&T recorded its second failed probe. That sample does not
measure forwarding after health convergence.

The repeated fault used fresh verified restoration timers. The health file
reported Webpass and AT&T unhealthy while Monkeybrains and Astound remained
healthy. Client 225 then completed 100 of 100 fresh IPv4 and 100 of 100 fresh
IPv6 HTTP connections, with distinct source ports. Both uplinks were restored
and the timers stopped. Both clients returned HTTP 200 over IPv4 and IPv6;
all five health states returned to healthy. This proves fresh-flow failover
after health convergence and recovery on the deployed model release. It does
not measure session survival or exact interruption duration.

The deployed model still leaves interface control with networkd. MWAN-516
state publication and later interface-owner cutover remain unfinished.

### Owned route and rule repair testbed checkpoint, September 27, 2026

VM 213 ran merged MWAN commit `2c6df53` with the interface manager active.
The owned IPv4 return route was `10.240.240.0/29 dev enmwanbr0` in table 100.
The owned policy rule selected table 100 for mark `0x1` at priority 100.
Before the fault, route lookup for `10.240.240.2` with mark `0x1` selected
`enmwanbr0`, and client 225 returned IPv4 HTTP 200.

The operator scheduled and verified a 60-second route restoration timer on
VM 213, then deleted only the owned return route through the Proxmox guest
agent. The first follow-up route query found the exact route restored. The
interface manager journal recorded `owned return route removed` at
14:22:56 PDT. Client 225 returned IPv4 HTTP 200. The operator stopped the
unused timer after verification.

The operator then scheduled and verified a separate 60-second rule
restoration timer, deleted only the priority 100 rule, and queried the rules
again. The first follow-up query found the exact rule restored. The journal
recorded `owned policy rule removed` at 14:23:38 PDT. Marked route lookup
again selected `enmwanbr0`, and client 225 returned IPv4 HTTP 200. The
operator stopped the unused rule timer. Neither fallback timer executed.

These observations prove live repair on the deployed release. The first
follow-up queries found each object restored. Each guest-agent query took
about two to three seconds. Exact repair latency was not measured. The test
did not remove both objects together, delete a default route, or exercise
production.

### Shared model and route repair production checkpoint, September 27, 2026

[Configs PR #532](https://github.com/agoodkind/configs/pull/532) pinned the
testbed-accepted MWAN release `202609272028-35-2c6df53` for production. Its
signed pin merged as `c26ab7f75cc5336c950824916f12275430f855a9`. The
previous production binary was `f61a4d7`. The production network document
SHA-256 after deployment was
`a068afec1d9de228e5e9a90cd1107d5d1ebe5c91327ddf9c877c663341be2e59`.
Networkd retained provider ownership throughout this phase.

From clean merged Configs main,
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff` passed
with 187 successful tasks, 14 proposed
changes, and zero failures. The supported apply command omitted the check and
diff flags. It reported 242 successful tasks, 24 changes, zero unreachable
hosts, and zero failures. Trace `20260927-144808-deploy-27830` passed the
reboot, egress, and mapped-address gates without rollback. VM 113 reported
`commit=2c6df53 dirty=clean` after reboot and boot ID
`f9a26309-0a7d-44db-a8f9-479b2b1cd902`.

The downstream UniFi LXC 102 has one network interface and no OOB route. Its
continuous one-second IPv4 and IPv6 HTTP observer recorded 607 probes per
family during and after deployment. Each family passed 596 and timed out on
11. The longest reboot interval between successful replies was 47.069
seconds for IPv4 and 46.054 seconds for IPv6. Earlier configuration work
produced an 8 to 9 second interruption in both families and one isolated
IPv6 timeout. These are measured gaps between replies, not exact link-down
times. Both families returned HTTP 200 from the LXC after deployment.
OPNsense VM 101 separately completed source-bound IPv4 and IPv6 HTTPS through
its MWAN transit addresses.

Twenty fresh external address observations per family from LXC 102 succeeded.
IPv4 selected Webpass nine times at `136.25.91.242` and AT&T eleven times at
`104.57.226.193`. IPv6 selected Webpass ten times at
`2604:5500:c271:be00:583d::102` and AT&T ten times at
`2600:1700:2f71:c80:dac2::102`. The health file reported AT&T,
Monkeybrains, and Webpass healthy. From the independent Suburban host, two
ICMP requests each received replies at Webpass `136.25.91.242` and AT&T
`104.57.226.193`; two IPv6 requests each received replies at the provider
edge addresses `2604:5500:c271:be00::1` and
`2600:1700:2f71:c80::1`. The firewall inspection matched the configured
DNAT, SNAT, forwarding, and marking rules. The gateway retained its owned
`10.250.250.0/29` return route in table 100 and mark `0x1` policy rule.
Internal IPv4 and IPv6 BGP TCP sessions were established, and the BGP return
routes remained present. The interface manager and agent services were active.

The production playbook performed network reload, daemon restart, and reboot.
Its downstream observer found no 14 to 40 second gap during network reload,
and the final route checks found the owned route and rule present. The deletion repair
itself was deliberately exercised on testbed, not on production. Production
provider fault injection remains excluded from this checkpoint. MWAN-505 has
production acceptance; MWAN-516 still needs state publication, and MWAN-520
remains open for subsequent ownership phases.

### Observer foundation testbed checkpoint, September 27, 2026

[MWAN PR #60](https://github.com/agoodkind/mwan/pull/60) merged the focused
observer foundation as `13183ea9d6264beac7b85bfd4e7946b15f7da601`.
The full Docker check and test suite, privileged namespace tests, required CI,
and independent review passed. The release workflow published
`202609272306-3a-13183ea` from that exact merge commit. Its
`mwan_linux_amd64.tar.gz` SHA-256 was
`2d143ffabf690d06dd8c7278df92152e406af617bca011a8ad1a5e373ab98b74`.
The optional Govulncheck job reported the existing GoBGP advisory database
result recorded above.

[Configs PR #533](https://github.com/agoodkind/configs/pull/533) merged the
testbed-only release pin as `4f551e5a8181e28acc3fde45df7e44b700dbc636`.
All required checks, automated review, and review-thread resolution passed.
From clean merged Configs main,
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`
passed with 176 successful tasks, 18 proposed changes, and zero failures.
The apply command omitted the check and diff flags. It passed with 227
successful tasks, 20 changes, zero unreachable hosts, and zero failures.
Trace `20260927-162859-deploy-791806` passed its reboot, egress, and
mapped-address gates without rollback. VM 213 reported
`commit=13183ea dirty=clean`; `mwan-ifmgr@wan` was active. Networkd retained
interface ownership.

Client 225 sent one-second IPv4 and IPv6 probes during the deploy. The
network reload produced intervals of 6.143 and 6.156 seconds between replies.
The reboot produced intervals of 46.090 and 46.074 seconds. These are
reply gaps, not exact link-down times. Clients 225 and 226 each have one
network interface and no OOB route. Both clients returned HTTP 200 over
IPv4 and IPv6 after deployment.

Client 225 completed 100 fresh IPv4 and 100 fresh IPv6 HTTPS requests with
HTTP 200. Simulator ingress captures attributed IPv4 SYNs to Webpass 62 and
AT&T 38, and IPv6 SYNs to Webpass 51 and AT&T 49. An initial public-IP
sample returned a single address and did not establish balancing. The
provider-side captures supplied the acceptance evidence.

The first controlled fault lowered `veth900i1` and `veth901i1` with verified
600-second restoration timers. Early state reads still reported both primary
providers healthy. The journal later showed Webpass and AT&T becoming
unhealthy at 16:52:54 and 16:52:23 PDT. The links were restored without a
fresh-flow sample at that state. The first attempt did not prove failover.

The repeated fault used fresh verified restoration timers. The health file
reported Webpass and AT&T unhealthy while Monkeybrains and Astound remained
healthy before traffic generation. All 20 fresh IPv4 requests returned HTTP
200: simulator captures counted Monkeybrains 12 and Astound eight. All 20
fresh IPv6 requests returned HTTP 200 through Monkeybrains. Both primary
links were restored and the timers stopped after verifying the links were up.
All five health states returned to healthy, and both clients again returned
HTTP 200 over IPv4 and IPv6. Continuous ping sessions established reload and
reboot gaps but did not provide reliable loss timing during the longer fault.
Their output was truncated. The test proves fresh-flow forwarding after health
convergence. It does not prove survival of existing connections.

MWAN-523 remains In Progress. Public daemon tests of device replacement,
virtual-link rename and index reuse, actual RA lifetimes, and route identity
scenarios still need implementation and acceptance. Tack MWAN-523 records
the testbed evidence.

### Observer foundation production checkpoint, September 27, 2026

[Configs PR #535](https://github.com/agoodkind/configs/pull/535) merged the
production pin as `8d5cdd4de382e06ff5ad7d5cfedb689fcefddd87`. Required
CI and automated review passed; no review thread remained open. Production
retained the accepted `2c6df53` release until the merged pin deployed.

From clean merged Configs main,
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff` passed
with 187 successful tasks, 14 proposed changes, and zero failures. The apply
command omitted the check and diff flags. It passed with 240 successful tasks,
20 changes, zero unreachable hosts, and zero failures. Trace
`20260927-171414-deploy-676349` passed the reboot, egress, and mapped-address
gates without rollback. VM 113 reported `commit=13183ea dirty=clean`, and
`mwan-ifmgr@wan` was active. Networkd retained interface ownership.

The downstream UniFi LXC 102 has one network interface and no OOB route. It
returned HTTP 200 over IPv4 and IPv6 before and after the deployment. Its
initial SSH-hosted probes stopped when the network reload reset SSH. Transient
probe services inside LXC 102 continued across the VM reboot. They recorded
14.306 seconds between successful IPv4 replies and 46.053 seconds between
successful IPv6 replies around that reboot. These intervals are measured
reply gaps, not exact link-down times. The transient services were stopped
after inspection.

Twenty fresh external-address observations per family succeeded from LXC
102. IPv4 selected AT&T 12 times at `104.57.226.193` and Webpass eight times
at `136.25.91.242`. IPv6 selected AT&T 12 times at
`2600:1700:2f71:c80:dac2::102` and Webpass eight times at
`2604:5500:c271:be00:583d::102`. The health file reported AT&T,
Monkeybrains, and Webpass healthy. From the independent Suburban host, both
requests to each of the Webpass and AT&T mapped IPv4 addresses and IPv6 edge
addresses received replies. The IPv6 edge addresses were
`2604:5500:c271:be00::1` and `2600:1700:2f71:c80::1`.

VM 113 retained its owned `10.250.250.0/29` return route in table 100 and
the mark `0x1` policy rule. TCP connections for internal IPv4 and IPv6 BGP
were established. This checkpoint did not repeat provider fault injection or
owned-route deletion in production. MWAN-523 still requires public daemon
tests of device replacement, virtual-link rename and index reuse, actual RA
lifetimes, and route identity acceptance. MWAN-516 state publication and later
interface ownership transfers remain. MWAN-520 stays open for subsequent
production phases.

### Observer rebind testbed checkpoint and restart failure, September 27, 2026

[MWAN PR #62](https://github.com/agoodkind/mwan/pull/62) merged the
name-bound observer rebind as `0ed736e6230dc3039fd3d7b0f4658de4d2de05ec`.
Release `202609280058-3c-0ed736e` passed its workflow. Configs PR #536
merged the testbed pin as `2de1f7662bfdccb07791a13c1f49a785dfd9cff6`.
The check and apply deployments passed from merged Configs main. VM 213
reported clean commit `0ed736e`; both downstream clients passed IPv4 and
IPv6 HTTP. Fresh connections used both primary providers in each family.
When both primary uplinks were lowered and marked unhealthy, 20 fresh IPv4
requests used Monkeybrains and Astound; 20 fresh IPv6 requests used
Monkeybrains. Both links and all health states recovered.

A controlled VM 213 reboot exposed two failover defects. OPNsense kept the
disconnected primary IPv4 route marked stale and best while its Graceful
Restart timer counted down, despite an available backup route. After backup
selection, the primary announced defaults before its provider forwarding was
ready. Client 225 lost IPv4 and IPv6 replies for about 25 seconds on that
return. The release was not promoted to production. MWAN-530 tracks the
restart blocker, and MWAN-523 remains In Progress.

[Configs PR #537](https://github.com/agoodkind/configs/pull/537) merged the
testbed-only Graceful Restart disablement as `51cc98c9`. The check and apply
deployments passed from clean merged Configs main. The apply recap reported
227 successful tasks, 18 changes, zero unreachable hosts, and zero failures;
trace `20260927-192535-deploy-485198` passed its gates. The installed MWAN
release remained `0ed736e`.

On the next controlled reboot, OPNsense selected the backup IPv4 route by
Unix time `1790563328`, without retaining a stale primary path. Client 225
continued to receive IPv4 and IPv6 replies during that handoff. OPNsense
selected the returned primary route by `1790563362`; the client then had
24.542 seconds between IPv4 replies and 23.525 seconds between IPv6 replies.
These are measured reply gaps, not exact link-down durations. The remaining
failure is premature primary announcement. [MWAN PR #64](https://github.com/agoodkind/mwan/pull/64)
added per-family forwarding readiness before primary announcements. Its full
local Docker check and test gate passed. [Configs PR #538](https://github.com/agoodkind/configs/pull/538)
merged the matching inventory and rendered configuration as
`a4eccad51583159e5affdee37503766999862d9f`.

### Forwarding readiness testbed and production checkpoint, September 27, 2026

MWAN PR #64 merged as `798ee6a8dcb57ef91e7d9656e8a368f3f4bd4412` after
required checks and review-thread resolution. The signed commit passed
`make docker-make TARGETS="check test"`. Release
`202609280259-3e-798ee6a` published from that merge. The release archive
`mwan_linux_amd64.tar.gz` has SHA-256
`db9faa750ff113a9ef0e2f8e3330dda5c4d910db7c1f16fe91cdbed940337643`.
[Configs PR #539](https://github.com/agoodkind/configs/pull/539) pinned that
release on testbed as `7e240ddd3cd2f6eff2644d686e85d78943412326`.

From clean merged Configs main, the testbed check deployment passed with
176 successful tasks, 19 proposed changes, and no failures. The first apply
lost management SSH before installing the new binary. VM 213 continued to
run the old binary and downstream traffic passed. An idempotent retry passed
with 227 successful tasks, 21 changes, and no failures. Trace
`20260927-202855-deploy-674680` passed reboot, egress, and mapped-address
checks. VM 213 reported clean commit `798ee6a`.

Client 225 sent one-second IPv4 and IPv6 probes during deployment. The
network reload produced 6.165 and 6.118 second reply intervals. OPNsense
selected backup-only defaults during the reboot, then restored the primary.
The probes continued to return replies during both route transitions.
Fifty fresh IPv4 HTTPS requests all succeeded; simulator captures counted
30 Webpass and 20 AT&T SYNs. Thirty IPv6 requests all succeeded; captures
counted 14 Webpass and 16 AT&T SYNs. With the Webpass and AT&T simulator
containers stopped, 20 fresh IPv4 requests succeeded through Monkeybrains
11 times and Astound nine times. Twenty IPv6 requests succeeded through
Monkeybrains. Automatic restoration timers restarted both simulators.

AT&T did not initially reacquire its address after simulator restart. The
guest sent DHCP requests, and a provider-side capture showed no replies.
The tagged testbed playbook skipped the included service tasks because
`include_tasks` did not propagate the `isp-lxcs` tag. [Configs PR #540](https://github.com/agoodkind/configs/pull/540)
merged the tag fix as `b62a96555e799ddf89f7abc9604f7e51fd24c122`.
The tagged deploy from clean merged main passed with 94 successful tasks,
70 changes, and no failures. AT&T reacquired IPv4 and IPv6, and all five
testbed providers reported healthy. Both downstream guests completed IPv4
and IPv6 HTTPS requests. During the simulator repair, the client probes
recorded no reply interval above 2.1 seconds.

After a final controlled testbed reboot, OPNsense selected backup-only IPv4
and IPv6 defaults, then restored the primary. Client 225 returned 387 IPv4 replies
with a maximum 1.012 second interval. It returned 386 IPv6 replies with
one missed packet and a maximum 2.057 second interval. The probes covered
both transitions. All five providers remained healthy.

[Configs PR #541](https://github.com/agoodkind/configs/pull/541) pinned the
same release and disabled primary Graceful Restart in production. It merged
as `495d53b70efb6a4ff15ce76c3d8471ffc709f17b` after required checks.
The production check deployment passed with 187 successful tasks, 15
proposed changes, and no failures. The apply from clean merged main passed
with 240 successful tasks, 22 changes, and no failures. Trace
`20260927-223958-deploy-615749` passed reboot, egress, and mapped-address
checks. VM 113 reported clean commit `798ee6a`; AT&T, Monkeybrains, and
Webpass reported healthy.

The first production deployment produced 9.207 and 8.204 second IPv4 and
IPv6 reply gaps during the network manager restart. Its reboot produced
18.434 and 18.375 second gaps. The previous BGP session still advertised
Graceful Restart when the reboot disconnected it. The new IPv4 and IPv6
sessions report that the primary does not advertise Graceful Restart. The
observed gaps are consistent with stale-route retention during this
transition, but no route sample was captured during those gaps.

A second controlled production reboot tested the new session. OPNsense
selected backup-only defaults and then restored the primary. The proxy
guest, which routes both families through OPNsense, returned 600 consecutive
IPv4 replies and 600 consecutive IPv6 replies with no interval above
2.1 seconds. Both-family HTTPS passed afterward. Twenty fresh IPv4 HTTPS
requests used Webpass 13 times and AT&T seven times. Twenty IPv6 requests
used Webpass 12 times and AT&T eight times. All three providers remained
healthy. MWAN-530 is Done; MWAN-523 and interface ownership remain open.

### Route deletion identity acceptance, September 28, 2026

[MWAN PR #66](https://github.com/agoodkind/mwan/pull/66) merged as
`268f01daf02b5bee62fdd730659f13a3e816b350`. Its signed commit was
`33af2e8864494164c27c042ebed7e92094b54332`. A real-kernel test created
two routes with the same destination in different tables and protocols. The
monitor reported the deleted route's family, table, protocol, metric, scope,
interface index, and next hop. A fresh kernel snapshot retained the route in
the other table. The default privileged test target now includes the observer
package. `make test-netns` passed all six packages. The full Docker check and
test gate, required CI, and independent review passed with no findings or
unresolved threads. The optional Govulncheck job repeated the existing GoBGP
database finding recorded above.

This merge changed tests and the test target only. No testbed or production
deployment ran for this test-only merge. MWAN-523 still requires index reuse,
virtual-link rename, router advertisement lifetimes, and daemon-level packet
acceptance before state publication.

### VLAN rename observer testbed checkpoint, September 28, 2026

[MWAN PR #68](https://github.com/agoodkind/mwan/pull/68) merged as
`79d166e709c31d4860eb732c97773d21fe1ed966`. The matcher identifies a
renamed 802.1Q VLAN by its parent name, tag, and protocol. Its privileged
kernel test creates 802.1Q and 802.1ad links with the same parent and tag,
renames the 802.1Q link, and verifies that the monitor follows only that
link. `make test-netns` passed all six packages, and
`make docker-make TARGETS='check test'` passed. Required CI, the final
privileged test run, and independent review passed with no unresolved review
threads. The optional Govulncheck job repeated the existing GoBGP finding
GO-2026-4736, with no fixed version reported by the scanner.

Release `202609281014-42-79d166e` passed its publish and verification
workflow. [Configs PR #542](https://github.com/agoodkind/configs/pull/542)
merged the testbed pin as `84cd7b3058348bcc7213369185284d0a17cab4b2`.
The testbed check from clean merged Configs main passed with 176 successful
tasks, 25 proposed changes, and no failures. The apply passed with 227
successful tasks, 21 changes, no unreachable hosts, and no failures. Trace
`20260928-033732-deploy-775018` passed the reboot, egress, and mapped-address
gates. VM 213 reported clean MWAN commit `79d166e` and an active
`mwan-ifmgr@wan` service. Networkd retained interface ownership.

Client 225 has one downstream interface through OPNsense. Its existing probe
service recorded 830 successful IPv4 samples and 830 successful IPv6 samples
during deployment, with no failed samples. The largest intervals between
successful replies were 2.127 seconds for IPv4 and 2.126 seconds for IPv6.
Client 226 returned IPv4 and IPv6 HTTPS replies after deployment. Client 225
completed 100 of 100 fresh requests in each family; simulator captures saw
new connections on both Webpass and AT&T links for both families.

Both primary simulator uplinks were lowered with verified ten-minute
restoration timers. Client 225 recorded 31 failed samples per family before
health classified Webpass and AT&T unhealthy. The largest intervals between
successful replies were 133.660 seconds for IPv4 and 134.764 seconds for
IPv6. After classification, 20 of 20 fresh IPv4 requests succeeded, with
SYNs observed on both Monkeybrains and Astound links. Twenty of 20 fresh
IPv6 requests succeeded, with SYNs observed on Monkeybrains. Both uplinks
were restored; all five providers returned healthy, both families returned
successful replies, and the restoration timers were stopped.

The testbed has no live VLAN provider. The real-kernel test verifies VLAN
rename behavior. The live testbed passed deployment compatibility checks.
Production still uses its prior release. MWAN-523 still requires index reuse,
bridge rename, router advertisement lifetimes, and public daemon packet
acceptance before state publication.

### VLAN rename observer production checkpoint, September 28, 2026

[Configs PR #543](https://github.com/agoodkind/configs/pull/543) pinned release
`202609281014-42-79d166e` in production and merged as
`14b87828101871b56277fb9251c5aa263cd51d3b`. The production check ran
from a clean checkout of that merged commit and passed with 187 successful
tasks, 15 proposed changes, no unreachable hosts, and no failures. The apply
passed with 240 successful tasks, 21 changes, no unreachable hosts, and no
failures. Trace `20260928-041729-deploy-714431` passed the reboot, egress,
and mapped-address gates without rollback. VM 113 reported clean MWAN commit
`79d166e`; `mwan-ifmgr@wan` was active, and AT&T, Webpass, and Monkeybrains
reported healthy.

The existing observer on downstream UniFi LXC 102 recorded 454 successful
IPv4 samples and 454 successful IPv6 samples from 11:15:46 through 11:23:29
UTC, with no failures. The largest intervals between successful samples were
2.039 seconds for IPv4 and 2.040 seconds for IPv6. Thirty fresh connections
per family all succeeded from that guest. IPv4 selected `136.25.91.242` 16
times and `104.57.226.193` 14 times. IPv6 selected
`2600:1700:2f71:c80:dac2::102` 16 times and
`2604:5500:c271:be00:583d::102` 14 times. Proxy LXC 110 also returned
IPv4 and IPv6 HTTPS replies after deployment. The gateway's provider-bound
probe succeeded over IPv4 for all
three providers and over IPv6 to ifconfig.co for all three providers. Its
alternate api.ipify.org IPv6 probes failed; the successful ifconfig.co probes
establish IPv6 egress on each provider.

Independent probes from Suburban received both ICMP replies from mapped IPv4
addresses `104.57.226.193` and `136.25.91.242` and both ICMPv6 replies from
provider edge addresses `2600:1700:2f71:c80::1` and
`2604:5500:c271:be00::1`. Production acceptance for this observer change
passed. Networkd still owns interfaces. MWAN-523 remains In Progress for
index reuse, bridge rename, router advertisement lifetimes, and public daemon
packet acceptance before state publication.

### Typed bridge observer checkpoint, September 28, 2026

[MWAN PR #71](https://github.com/agoodkind/mwan/pull/71) merged as
`adb62401a24836b14a1f9cd33f0c228b291492b3`. Its real-kernel test
renames a configured bridge, verifies that the monitor clears the binding
and does not attribute the old bridge's address or route, then creates a
replacement with the configured name and verifies its index and address
without restarting the monitor. The existing runtime passed 20 focused
privileged runs, all six packages in `make test-netns`, and
`make docker-make TARGETS='check test'`. Independent review found no issue.
Required CI passed after one unrelated firewall packet-test timeout was
rerun; the new bridge test passed in both CI attempts.

This test-only merge does not change the deployed binary and requires no
testbed or production deployment. MWAN-523 still requires index reuse,
actual router-advertisement lifetimes, and public daemon packet acceptance.

### Router advertisement lifetime observer checkpoint, September 28, 2026

[MWAN PR #73](https://github.com/agoodkind/mwan/pull/73) merged as
`566e1d567f56af63874d6dc26b12301f725d4e4d`. Its test sends a real
router advertisement in an isolated Linux network namespace. Kernel address
reads and fresh monitor snapshots report tentative, usable, deprecated, and
expired states. The long-lived monitor reports the usable address and its
deletion. It did not emit a tentative address event on this kernel; a fresh
snapshot reported that state.

The focused privileged test passed five consecutive runs. All six packages
in `make test-netns` and `make docker-make TARGETS='check test'` passed.
Independent review found no blocking defect. Required CI and the automated
review passed with no unresolved threads. Optional Govulncheck reported the
existing GoBGP advisory GO-2026-4736, which has no fixed version in that
scanner result. This test-only merge requires no gateway release or deploy.
MWAN-523 remains In Progress for index reuse, duplicate-address failure,
observation-gap recovery, and public daemon packet acceptance.

### Index reuse observer checkpoint, September 28, 2026

[MWAN PR #75](https://github.com/agoodkind/mwan/pull/75) merged as
`dd288e117435039245024438eb0e7e890c6b01f2`. Its isolated real-kernel
test deletes a configured physical link and creates an unrelated link with
the same kernel index. The monitor stays unbound and does not attribute the
unrelated address. A matching replacement then reuses that index. The
monitor reports the configured connection ID, replacement name, and current
address; a fresh snapshot excludes the old address.

The final test passed 20 focused privileged runs, all six packages in
`make test-netns`, and `make docker-make TARGETS='check test'`. Independent
review verified the unrelated-link rejection. Required CI and automated
review passed with no unresolved threads. Optional Govulncheck repeated the
existing GoBGP advisory GO-2026-4736. This test-only merge requires no
gateway release or deploy. MWAN-523 remains In Progress for duplicate-address
failure and public daemon packet acceptance after link replacement and
observation recovery.

### Daemon packet recovery checkpoint, September 28, 2026

[MWAN PR #77](https://github.com/agoodkind/mwan/pull/77) merged as
`f398dbada5bce6ee3d11e34a83ad7830a8e368e7`. Its signed commit was
`69871e221b1166ee511f50c9fcceada77c5211ac`. A public
`mwan ifmgr --role wan` test replaced a configured physical link with a new
kernel index and the configured MAC. The daemon rebuilt its owned IPv4 and
IPv6 defaults on the replacement index without restarting. Fresh TCP
connections succeeded in both families. TCP connections failed while the
link was absent.

Focused privileged runs, `make test-firewall`, and
`make docker-make TARGETS='check test'` passed. Independent review found no
blocking defect. Required CI and automated review passed with no unresolved
threads. Vet passed on rerun after the first attempt failed during a Go
module proxy download. Optional Govulncheck repeated the existing GoBGP
advisory. This test-only merge requires no gateway release or deploy.
MWAN-523 remains In Progress for duplicate-address failure and
observation-gap acceptance.

### Duplicate-address failure checkpoint, September 28, 2026

[MWAN PR #79](https://github.com/agoodkind/mwan/pull/79) merged as
`368a5a1d3c4579cc30c2ce57582aa9c94c5fb574`. Its signed commit was
`270b5cfd759e020539775a5bdc18514550a39bd0`. In an isolated Linux
network namespace, a peer owned `2001:db8:523::d/128` and the configured
client attempted the same address with duplicate-address detection enabled.
The kernel, a public `NewMonitor` event, and a fresh monitor snapshot all
reported `DADFailed` and `Tentative` for the client address.

The focused privileged test passed ten runs. `make test-netns`,
`make docker-make TARGETS='check test'`, required CI, automated review, and
independent review passed. Optional Govulncheck repeated the existing
GoBGP advisory. This test-only merge requires no gateway release or deploy.
MWAN-523 remains In Progress for complete observation-gap snapshot acceptance.

### Snapshot replay overflow repair, September 28, 2026

[MWAN PR #81](https://github.com/agoodkind/mwan/pull/81) merged as
`1cc8e2d1712d5ac95c44ece621b2deabccdafbab`. Its signed commit was
`b6464c669920087c669f620318cc0abc13c2cbea`. Real kernel changes filled
the monitor's 64-event channel. Snapshot replay of 160 addresses filled the
channel again and repeatedly requested another snapshot. In a five-second
pre-fix test, overflow warnings increased from one to 1,207 after the burst,
and the monitor did not report a later address delta.

Snapshot replay now waits for a draining consumer. Real kernel-event
overflow still requests a complete snapshot. The public real-kernel test
requires an overflow warning, one snapshot matching all 160 kernel
addresses, all 160 replayed additions, and a normal delta for address 161.
The test failed before the fix and passed ten focused runs afterward.
Existing replay-order tests, `make test-netns`,
`make docker-make TARGETS='check test'`, required CI, automated review, and
independent review passed. Optional Govulncheck repeated the existing
GoBGP advisory. Release and live acceptance followed this repair.

### Snapshot recovery testbed acceptance, September 28, 2026

Release `202609281333-4f-1cc8e2d` passed publication and verification and
resolves to merged MWAN commit `1cc8e2d1712d5ac95c44ece621b2deabccdafbab`.
[Configs PR #544](https://github.com/agoodkind/configs/pull/544) pinned it in
testbed and merged as `d521fcbae35df40f5eb962b28792650b2c707930`.
The clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`,
then the same command without `--check --diff`. Check mode reported 176
successful tasks and 25 proposed changes. Apply reported 227 successful tasks
and 20 changes. Both reported zero unreachable hosts and zero failures.
Trace `20260928-065654-deploy-432649` passed reboot, egress, and mapped-address
gates without rollback. VM 213 reported clean MWAN commit `1cc8e2d` and an
active interface manager. All five providers reported healthy.

Client 225 received 600 of 600 IPv4 and 600 of 600 IPv6 replies in each of
two deployment windows. A controlled primary reboot selected backup
`10.240.240.4`, then restored primary `10.240.240.3`. The same client received
180 of 180 replies in each family during that reboot. Clients 225 and 226
completed 100 fresh connections per family to an unpinned destination.
Provider ingress captures counted 44 Webpass and 56 AT&T IPv4 connections,
and 45 Webpass and 55 AT&T IPv6 connections. The remote endpoint returned a
valid HTTP 404 for each connection. The testbed's configured `1.0.0.1` pin
selected AT&T for a separate 100-request check; that pin was not used for
the balancing count.

### Snapshot recovery production acceptance, September 28, 2026

[Configs PR #545](https://github.com/agoodkind/configs/pull/545) pinned the
same release in production and merged as
`60e605ca506da9918e2d6adfc83b0e68c1136cd1`. Its published binary
checksum matched the inventory pin. The clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff`, then
the same command without `--check --diff`. Check mode reported 187 successful
tasks and 15 proposed changes. Apply reported 240 successful tasks and 20
changes. Both reported zero unreachable hosts and zero failures. Trace
`20260928-073213-deploy-243564` passed reboot, egress, and mapped-address
gates without rollback. VM 113 reported clean MWAN commit `1cc8e2d`, an
active interface manager, and healthy AT&T, Webpass, and Monkeybrains.

Downstream UniFi LXC 102 received 900 of 900 IPv4 and 900 of 900 IPv6
replies across the deployment and reboot. The largest intervals between
successful replies were 1.030 and 1.004 seconds. UniFi LXC 102 received
another 900 of 900 replies per family in an overlapping probe. Its largest
intervals were 1.310 and 1.335 seconds. During reboot, OPNsense selected backup
`10.250.250.4`, then restored primary `10.250.250.3`. Twenty fresh requests
per family succeeded from the downstream guest. IPv4 selected external
addresses `104.57.226.193` nine times and `136.25.91.242` eleven times.
IPv6 selected
`2600:1700:2f71:c80:dac2::102` thirteen times and
`2604:5500:c271:be00:583d::102` seven times. An earlier ifconfig.co
sample received HTTP 429 after successful connections; the subsequent
api.ipify.org sample completed without a rate-limit response.

Independent Suburban probes received three of three replies from each
mapped IPv4 address and from provider edge addresses
`2600:1700:2f71:c80::1` and `2604:5500:c271:be00::1`. The gateway had
established internal IPv4 and IPv6 BGP TCP sessions. This release passed
production acceptance. MWAN-516 state publication is the next prerequisite
for interface-owner cutover.

### MWAN-516 state publication, September 28, 2026

[MWAN PR #84](https://github.com/agoodkind/mwan/pull/84) merged as
`b601ab8a5f4f44d09b949ab6132c4287ef9dbbef`. Its public daemon test
queried a second sysrepo connection after link replacement, address
deprecation, observation overflow and recovery, and restart. It verified
stable object IDs, bounded recent transitions, and persistent detailed
history. The privileged namespace suite, Docker check and test gate,
required CI, automated review, and independent review passed. No review
thread remained open. Optional Govulncheck repeated the existing GoBGP
database finding. The release workflow published
`202609281827-52-b601ab8` from the exact merged commit. The amd64 binary
archive SHA-256 was
`b59e9308e50ecf73d206fa5624aa9f551f6980dcd690f32749cabc3a700ac5dd`.

[Configs PR #546](https://github.com/agoodkind/configs/pull/546) pinned the
release in testbed and merged as
`5de3e6607f89b0a3e6857957125848f57a976dc7`. A clean checkout at that
merged commit ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`,
then the same command without
`--check --diff`. Check mode reported 176 successful tasks and 18 proposed
changes. Apply reported 229 successful tasks and 24 changes. Both had zero
unreachable hosts and zero failures. Trace
`20260928-115246-deploy-290454` passed reboot, egress, and mapped-address
gates without rollback. VM 213 reported clean commit `b601ab8`, an active
interface manager, and five healthy provider states.

Client 225 received 900 of 900 IPv4 and 898 of 900 IPv6 replies during the
testbed deployment and reboot. One IPv6 packet was missed before reboot and
one during reboot. The largest reply gaps were 1.658 and 2.039 seconds.
Client 226 returned five of five replies in each family afterward. During a
controlled primary speaker restart, FRR selected backup
`3d06:bad:b01:201::4` for IPv6, then restored primary
`3d06:bad:b01:201::3`. Client 225 received 100 of 100 replies in each
family during a second speaker restart. Fresh downstream connections used
AT&T and Webpass: 10 and 10 IPv4 SYNs, then 12 and eight IPv6 SYNs.

The testbed RESTCONF query returned fresh, up observations for all seven
configured interfaces. Each provider retained configured owner `networkd`.
The first 600-packet probe launched at reboot stopped returning SSH output
after 257 IPv4 and 251 IPv6 replies, so its incomplete files were excluded
from acceptance. The separate completed 900-packet and 100-packet probes
provide the packet results above. A gateway-local diagnostic reported failed
IPv6 connectivity on the routed simulator; this phase did not claim routed
simulator IPv6 acceptance.

[Configs PR #547](https://github.com/agoodkind/configs/pull/547) pinned the
same release in production and merged as
`05f5799e2624f1cdb6a26fc6e6002cc40dff594c`. Its required checks and
automated review passed with no unresolved threads. A clean checkout at
that merged commit ran
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff`,
then the same command without `--check --diff`. Check mode
reported 187 successful tasks and 14 proposed changes. Apply reported 242
successful tasks and 24 changes. Both had zero unreachable hosts and zero
failures. Trace `20260928-123532-deploy-395029` passed reboot, egress, and
mapped-address gates without rollback. Ansible reconnected after the
interface-manager restart. VM 113 reported clean commit `b601ab8`, an
active interface manager, and healthy AT&T, Webpass, and Monkeybrains.

Downstream UniFi LXC 102 received 1,000 of 1,000 IPv4 and 1,000 of 1,000
IPv6 replies across the restart and reboot. The largest gaps between replies
were 1.205 and 1.020 seconds. Replies used TTL 45 during 65 IPv4 and 66
IPv6 samples; this matched the backup path's earlier observed TTL. FRR then selected the
primary default and reported established IPv4 and IPv6 sessions with both
speakers. Twenty fresh downstream connections per family used AT&T ten
times and Webpass ten times. Independent Suburban probes received three of
three replies from each mapped IPv4 address, `104.57.226.193` and
`136.25.91.242`, and each IPv6 edge address,
`2600:1700:2f71:c80::1` and `2604:5500:c271:be00::1`.

The production RESTCONF query reported fresh, up observations for all five
configured interfaces. AT&T, Webpass, and Monkeybrains still use networkd.
The served family records separate observed addresses and routes from
acquisition and readiness. Acquisition and readiness remain `unknown` until
a component reports them. AT&T served 28 recent transitions; the persistent
interface-manager JSON log contained 1,725 lines after reboot. No live
interface ownership transfer or reverse transfer occurred. MWAN-516 passed
its state-publication acceptance; MWAN-397 is the next implementation slice.

### MWAN-397 link implementation, September 28, 2026

Tack marks MWAN-397 In Progress. [MWAN PR #86](https://github.com/agoodkind/mwan/pull/86)
merged as `446e76fe590e5dc9bbbcfe6fd875a8752f4e404c`. The release
`202609282224-55-446e76f` resolves to that commit. Its amd64 binary and
stack archives passed checksum and source-attestation verification. The change
admits only link-only MWAN connections. Existing provider connections retain
their networkd owner. A privileged Linux namespace test found that netlink
`LinkAdd` did not retain the requested alias for either a VLAN or a bridge.
The link writer now syncs a creation record with a random alias token before
it creates a virtual link under a reserved temporary name. It verifies the
device before setting the recorded alias and assigning the configured name.
Focused kernel namespace tests pass for creation, restart adoption, removal,
and preservation of foreign links.
The parser, boot-name writer, and configuration-tree tests pass. The public
daemon namespace test passed with VLAN packet delivery, restart adoption,
final-link removal, and preservation of legacy and external links. Independent
review found and verified fixes for journal and bridge identity defects. Lint,
schema validation, the full test target, privileged namespace tests, required
CI, and automated review passed on the rebased change. The optional
Govulncheck job reported the existing GoBGP advisory.

### September 28, 2026: Repair the monitor test fixture

The Linux namespace job for PR #86 failed on a monitor test that created
duplicate current MAC addresses after veth creation. The resolver checks the
permanent MAC when the kernel reports one. The test did not verify that both
permanent MACs matched the configured address. The production resolver did
not change. [MWAN PR #87](https://github.com/agoodkind/mwan/pull/87) set the
requested MAC at veth creation and verified the kernel identity before
starting the monitor.

The affected tests passed 20 local repetitions. The full privileged netif
suite passed three repetitions. `make docker-make TARGETS='check test'`
passed. Independent review found no remaining defect. The Linux namespace,
firewall, arm64, build, required lint, and automated review checks passed.
PR #87 merged as `6796dc74352a4c9d3823a58fc5eeb6b59757fc3e`. The optional
Govulncheck job still reports the preexisting GoBGP advisory.

PR #86 was rebased onto that merged commit. Its final signed source revision
was `b58cd73d2c3b206d0377de947ba67324de82aa65`. Required CI and automated
review passed on that revision before merge.

### MWAN-397 testbed acceptance, September 28, 2026

[Configs PR #548](https://github.com/agoodkind/configs/pull/548) pinned the
release in testbed and merged as `52db9c6c46b24a236da9d20adf1455eded333ba0`.
A clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`,
then the same command without `--check --diff`. Check mode reported 176
successful tasks and 18 proposed changes. Apply reported 227 successful tasks
and 21 changes. Both reported zero unreachable hosts and zero failures. Trace
`20260928-155235-deploy-296953` passed reboot, egress, and mapped-address
gates without rollback. The controller reconnected after the interface-manager
restart. VM 213 reported clean commit `446e76f` and an active interface manager.
The network configuration and networkd provider files remained unchanged.
This deployment transferred no live interface ownership.

Downstream client 225 received 1,221 of 1,221 IPv4 and 1,221 of 1,221 IPv6
replies during the deployment and reboot. Its default routes selected OPNsense
for both families. Twenty fresh IPv6 connections used Webpass eight times and
AT&T 12 times, measured by provider-interface SYN captures. IPv4 captures
also showed new client connections on both providers. Client HTTP requests
returned success in both families before the request burst triggered
`ifconfig.co` rate limiting. The testbed `api.ipify.org` IPv6 endpoint returned
a mismatched certificate or connection failure; `ifconfig.co` succeeded over
IPv6. These endpoint results do not indicate a gateway forwarding failure.

OPNsense initially preferred the primary BGP defaults and had the backup
routes available. A controlled stop of `mwan-agent` selected the backup
`10.240.240.4` and `3d06:bad:b01:201::4`. Starting the agent restored the
primary `10.240.240.3` and `3d06:bad:b01:201::3`. Client 225 received 120
of 120 IPv4 and 119 of 120 IPv6 replies during that test. IPv6 missed sequence
26; the gap between successful replies was 2.036 seconds. After recovery,
five of five downstream probes passed in each family. The gateway reported
the expected addresses and delegations for all configured providers.
Direct pings from Suburban to the private mapped addresses failed. The host
routed those destinations through Comcast instead of the isolated ISP bridges,
so that probe did not test inbound mapping. The deploy gate verified address
presence on the gateway.

### MWAN-397 production acceptance, September 28, 2026

[Configs PR #549](https://github.com/agoodkind/configs/pull/549) pinned the
same release in production and merged as
`d99305044149a77a7eab7eaf8b3456771c5970db`. Its required checks,
Graphite AI review, and PR-Agent review passed with no unresolved threads.
A clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff`, then
the same command without `--check --diff`. Check mode reported 187 successful
tasks and 14 proposed changes. Apply reported 240 successful tasks and 21
changes. Both reported zero unreachable hosts and zero failures. The
predeploy egress check passed and Vault created a rollback snapshot. Trace
`20260928-162923-deploy-667237` passed reboot, egress, and mapped-address
gates without rollback. Ansible reconnected after the interface-manager
restart. VM 113 reported clean commit `446e76f`, an active interface manager,
and the expected AT&T, Webpass, and Monkeybrains addresses and delegations.
All live provider links remain under networkd. No reverse transfer applied
to this link-only release.

Downstream UniFi LXC 102 received 1,125 of 1,126 IPv4 and 1,126 of 1,126
IPv6 replies across deployment and reboot. IPv4 missed sequence 764 during
the change to the backup path; successful replies were 2.026 seconds apart.
The reply TTL changed from 49 to 45 for IPv4 and from 55 to 45 for IPv6
during the backup interval. OPNsense subsequently installed the primary
defaults through `10.250.250.3` and `3d06:bad:b01:fe::3`. Twenty fresh
downstream HTTP connections per family all succeeded. IPv4 selected Webpass
12 times and AT&T eight times; IPv6 selected Webpass 12 times and AT&T eight
times. Independent Suburban probes received three of three replies from each
mapped IPv4 address, `104.57.226.193` and `136.25.91.242`, and each IPv6
edge address, `2600:1700:2f71:c80::1` and `2604:5500:c271:be00::1`.

MWAN-397 is Done in Tack. The next runtime slice is MWAN-398 owned address
and route reconciliation. Static and mapped/NPT code can share one deployment
phase before protocol acquisition work begins.

### MWAN-398 static implementation review, September 28, 2026

Tack records MWAN-398 In Progress. The uncommitted static patch on
`codex/mwan-398-static` starts from merged MWAN commit `fd8277f`. The Docker
check and test gate, privileged kernel suite, and one public daemon namespace
test passed. No merge, release, or deployment exists for this slice.

Independent review found that an owned address could not change prefix
length, family apply failures lacked a public reason, and a failed journal
save could discard the in-memory cleanup record. It also found missing
publication for removed-family cleanup failures and missing isolated metric
and failure-state cases in the public test. The review verdict is NOT-READY.
The implementation corrected those cases and added a pending-removal state
for cleanup failures after a connection disappears from configuration. The
public daemon test reproduced a read-only journal failure, read the served
failure, restored journal writes, and verified cleanup. A second review found
stale pending-removal entries after mixed cleanup results; the state now
replaces the published failure set after each pass. The final independent
verdict is MERGE-READY on the uncommitted patch. The reviewer independently
ran the public Linux daemon test. The full Docker check and test gate,
privileged kernel suite, and public daemon test passed locally on the final
patch. PR #89 is open. Merge, release, and deployment remain pending.

### MWAN-398 static merge and deferred deployment, September 28, 2026

[MWAN PR #89](https://github.com/agoodkind/mwan/pull/89) merged as
`d9e8a5a6669c5517325917a97f577d05dda04108`. Required checks, independent
review, automated review, and thread resolution passed. The optional
Govulncheck job reported the existing GoBGP advisory. Release
`202609290156-57-d9e8a5a` passed archive and source verification.
[Configs PR #550](https://github.com/agoodkind/configs/pull/550) pinned that
release in testbed and merged as `1cbedd4b3d576ccdec6f3c7238f371b89d9da360`.

The clean merged Configs checkout ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`.
Check mode reported 176 successful tasks, 18 proposed changes, zero
unreachable hosts, and zero failures. Downstream guest 225 returned 623
consecutive IPv4 and 623 consecutive IPv6 replies during the run. No apply
or production deployment ran. The operator requested batching compatible
low-risk changes instead of deploying after every merge. The next phase can
include the mapped/NPT writer transfer while existing providers remain under
networkd. The final testbed pin must identify the complete merged release
that the phase validates.

### MWAN-398 mapped and NPT writer, September 28, 2026

The signed `codex/mwan-398-mapped` patch transfers on-link IPv4 mappings
and the configured NPT external `::1/128` address to the address module for
exclusively owned connections. Legacy connections retain their existing
address writers. [MWAN PR #91](https://github.com/agoodkind/mwan/pull/91)
merged as `4b8a219e601c2444135ef5b0e39bd28dd9e08e41`.

The public daemon namespace test passed local and routed IPv4 packet replies,
inbound NPT IPv6 packet replies, legacy NPT address installation, changed
prefix cleanup, and foreign address preservation. A foreign address with the
same IPv6 address and a different prefix prevented owned installation and
the new NPT rule. The focused test passed three consecutive runs. The Docker
check and test gate and all six privileged public firewall tests passed.
Independent review found no runtime blocker and identified stale writer
comments. The corrected patch received a MERGE-READY verdict. The reviewer
did not run a red-green reversal. Required PR checks passed. The optional
Govulncheck job reported the existing GoBGP advisory, and the optional
PR-Agent service failed before reviewing a code chunk.

Release `202609290344-59-4b8a219` passed build, package, publish, and archive
verification. [Configs PR #551](https://github.com/agoodkind/configs/pull/551)
pinned that release in testbed and merged as
`5742621ad33f550d47ceef7e17d42885843036f7`. The production pin did not
change. The clean merged Configs checkout ran the check-mode
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`
command. Its first check lost the SSH
connection during a Suburban load spike. The VM remained reachable. A repeat
after host load fell passed with 174 successful tasks, 13 proposed changes,
and zero unreachable hosts or failures. The same checkout then ran the apply.
It completed with 229 successful tasks, 24 changes, and zero unreachable hosts
or failures. The asynchronous interface-manager restart and reconnect passed.

The deploy gate trace `20260928-213013-deploy-973524` recorded reboot,
OPNsense-originated IPv4 and IPv6 HTTPS egress through the MWAN next hop, and
mapped-address checks with return codes of zero. VM 213 reported release
commit `4b8a219e`, and `mwan-ifmgr@wan` was active. Downstream
clients 225 and 226 each had one network interface and default routes through
OPNsense. Each client received three of three IPv4 and three of three IPv6
ICMP replies after deployment. This sample did not measure interruption across
the reboot. Client 225 then completed ten of ten fresh IPv4 and six of six
fresh IPv6 HTTPS connections. An IPv4 SYN capture recorded three connections
on Webpass and two on AT&T before its 20-second timeout. An IPv6 packet capture
recorded connections on both providers. These samples establish selection of
both eligible providers, not a statistical weight estimate. The public Linux
namespace test validated the mapped/NPT writer. No live provider has
transferred address ownership.
MWAN-398 remains In Progress for protocol acquisition and full phase acceptance.

### MWAN-522 protocol test preparation, September 28, 2026

[MWAN PR #93](https://github.com/agoodkind/mwan/pull/93) merged as
`255875090427ad6b063b85cbef6dec5401923931`. Its public namespace test
starts the production daemon, Kea, and radvd, then verifies downstream TCP.
`make test-protocol` and the Docker check and test gate passed. This test does
not yet verify a DHCP exchange or delegated prefix. Required GitHub checks
passed. The optional firewall job timed out waiting for a mapped IPv6 UDP
reply in `TestOwnedMappedDaemonRuntime`; a local run reproduced the timeout.
A separate local run from the previous main commit passed. The failure has
not been attributed to this PR. The optional Govulncheck job reported an
existing GoBGP dependency advisory.

[Configs PR #553](https://github.com/agoodkind/configs/pull/553) merged as
`73701eb061709a007b9b24732c161e9dee852fff`. It permits configured Kea
DHCPv4 and DHCPv6 lease timing and radvd advertisement timing while retaining
the previous rendered defaults. Focused render tests and `./configsctl lint`
passed. An independent review parsed the default and short configurations
with Kea and radvd. Neither merged change has been deployed in this phase.
MWAN-522 remains In Progress for protocol assertions and live acceptance.

### MWAN-398 DHCPv4 implementation and testbed pin, September 29, 2026

The five focused DHCPv4 PRs merged in order: [#95](https://github.com/agoodkind/mwan/pull/95)
at `88a34f4` added lease renewal, rebinding, expiry, and classless routes;
[#96](https://github.com/agoodkind/mwan/pull/96) at `534ea12` added exact
address and route ownership; [#97](https://github.com/agoodkind/mwan/pull/97)
at `8912e77` updated OOB and failover assignment consumers;
[#98](https://github.com/agoodkind/mwan/pull/98) at `86ff105` added WAN-owned
acquisition and seven real Kea namespace scenarios; and
[#99](https://github.com/agoodkind/mwan/pull/99) at `3945636` permitted the
required journal and IPv4 kernel-policy writes in the WAN and failover units.
An independent review found the missing unit permissions before merge. The
Docker check and test gate and all seven Kea scenarios passed locally. CI on
the final main commit passed. The optional mapped IPv6 UDP firewall test
timed out intermittently on earlier PR runs; its cause remains unproven.

[Configs PR #556](https://github.com/agoodkind/configs/pull/556) merged as
`a852377` and added the conditional OOB DHCPv4 unit permissions on Vault.
Release `202609290823-61-3945636` passed archive and attestation verification
on the second workflow attempt. The first attempt could not query the GitHub
release API and returned HTTP 403 before checking an archive. Independently,
all four downloaded archives matched the published SHA256 file.
[Configs PR #557](https://github.com/agoodkind/configs/pull/557) merged as
`db0f3cd` and pinned the two Linux AMD64 archives to testbed only. Its
required checks passed. Production remains pinned to its previous MWAN
release. The merged Configs checkout ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`
with 176 successful tasks, 25 proposed changes, and no failures. The apply
completed with 229 successful tasks, 24 changes, and no failures. The
hypervisor verdict for `20260929-021249-deploy-626884` recorded successful
reboot, egress, and mapped-address checks from 09:22:25 to 09:23:53 UTC.
VM 213 reported build commit `3945636`, and `mwan-ifmgr@wan` was active.

Downstream client 225 recorded 600 of 600 IPv4 and 600 of 600 IPv6 ICMP
replies across the service restart and reboot. Clients 225 and 226 each
returned IPv4 and IPv6 HTTP 200 responses afterward. Client 225 completed
20 fresh IPv4 and 20 fresh IPv6 HTTPS requests. WAN packet captures recorded
ten IPv4 SYNs on Webpass and ten on AT&T, then 14 IPv6 SYNs on Webpass and
six on AT&T. These samples demonstrate selection of both eligible providers;
they do not estimate the configured weights or identify the route selected
for each probe during the reboot. No live provider transferred address
ownership. Production remains on its previous release. MWAN-398 and MWAN-522
remain In Progress until protocol transfer and full live acceptance.

### MWAN-517 kernel policy and combined release, September 29, 2026

[MWAN PR #103](https://github.com/agoodkind/mwan/pull/103) merged the
interface-manager permission for IPv6 sysctl writes as `5ce1f74`.
[MWAN PR #104](https://github.com/agoodkind/mwan/pull/104) merged as
`5214310` after correcting the mapped UDP test's readiness wait. Five
repeated mapped tests, the firewall suite, and the Docker check and test
gate passed. [MWAN PR #102](https://github.com/agoodkind/mwan/pull/102)
merged kernel RA policy as `3191615`. Its namespace and firewall tests,
required CI, and independent review passed. The optional Govulncheck job
continued to report the previously recorded GoBGP database advisory.
Release `202609291053-65-3191615` passed archive verification.

[Configs PR #558](https://github.com/agoodkind/configs/pull/558) merged
explicit connection IDs and networkd owner fields as `f6769336`.
[Configs PR #559](https://github.com/agoodkind/configs/pull/559) pinned the
release in testbed as `514d601e`. A clean checkout of that merged commit
ran `./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`,
then `./configsctl deploy deploy-mwan --limit mwan_suburban_servers`.
Check mode reported 176 successful tasks, 26 proposed changes, and no failures. Apply reported
229 successful tasks, 25 changes, and no failures. Trace
`20260929-041749-deploy-106369` passed reboot, egress, and mapped-address
gates without rollback. VM 213 reported clean commit `3191615`; the agent,
interface manager, and RESTCONF service were active. An initial 600-sample
pair and an overlapping 900-sample pair each received every IPv4 and IPv6
reply. The longer pair covered the restart and reboot. Clients 225 and 226
returned HTTP 200 in both families.
Twenty fresh HTTPS requests per family returned 200. WAN captures counted
11 Webpass and nine AT&T IPv4 SYNs, then ten Webpass and ten AT&T IPv6 SYNs.

[Configs PR #560](https://github.com/agoodkind/configs/pull/560) pinned the
same release in production as `e2de4dd4`. Its required checks and automated
reviews passed without an unresolved thread. The clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff`, then
`./configsctl deploy deploy-mwan --limit mwan_servers`.
Check mode reported 187 successful tasks, 16 proposed changes, and no failures. Apply reported 242 successful
tasks, 25 changes, and no failures. Vault created a rollback snapshot. Trace
`20260929-045726-deploy-756548` passed reboot, egress, and mapped-address
gates without rollback. VM 113 reported clean commit `3191615`; the agent,
interface manager, and RESTCONF service were active. Downstream UniFi LXC
102 returned HTTP 200 in both families. Twenty fresh HTTPS requests per
family returned 200. WAN captures counted ten Webpass and ten AT&T IPv4
SYNs, then eight Webpass and 12 AT&T IPv6 SYNs.

LXC 102 received 1,800 of 1,800 IPv4 replies and 1,797 of 1,800 IPv6
replies during the production apply and reboot. IPv6 missed sequences 698,
714, and 715 around the first path change. The largest interval between
successful IPv6 replies was 3.065 seconds; the IPv4 maximum was 1.261
seconds. Reply TTL changed to 45 during the service restart and again during
the reboot, matching the backup-path TTL observed in the earlier controlled
failover test. This run did not sample OPNsense's selected route during
either interval. Testbed and production rendered every provider as
networkd-owned. The new kernel policy and DHCPv4 ownership paths did not
transfer a live interface. MWAN-398, MWAN-517, MWAN-521, MWAN-522, and
MWAN-520 remain In Progress for the remaining protocol, render, cutover,
and acceptance work.

### MWAN-517 observed IPv6 state, September 29, 2026

[MWAN PR #105](https://github.com/agoodkind/mwan/pull/105) merged the prior
release acceptance record as `e367d9a`. [MWAN PR #106](https://github.com/agoodkind/mwan/pull/106)
merged IPv6 address phases and RA router validity as `acb81bb`. Its public
Linux namespace test exercised kernel tentative, duplicate, usable,
deprecated, removed, and stale observations, plus RA default-route addition
and withdrawal. The old-schema upgrade selftest, YANG validation, Docker
check and test gate, required CI, and independent review passed. The optional
Govulncheck job reported the previously recorded GoBGP database advisory.
Release `202609291301-67-acb81bb` passed build, archive, and attestation
verification.

[Configs PR #561](https://github.com/agoodkind/configs/pull/561) pinned the
release in testbed and merged as `37135a4b`. The clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`
and then the same command without `--check --diff`. Check mode reported 176
successful tasks and 18 proposed changes. Apply reported 229 successful tasks
and 24 changes. Neither run reported an unreachable host or failure. Deploy
trace `20260929-063116-deploy-551616` passed reboot, egress, and mapped-address
gates without rollback. VM 213 reported clean `acb81bb` and active interface
manager, agent, and RESTCONF services. Client 225 received 900 of 900 IPv4
and 900 of 900 IPv6 replies across the deployment. The largest reply gaps
were 2.367 and 2.503 seconds. Clients 225 and 226 returned HTTP 200 over
both families. Forty fresh HTTPS requests all returned 200; gateway captures
counted eight Webpass and 12 AT&T IPv4 SYNs, then ten and ten IPv6 SYNs.

[Configs PR #562](https://github.com/agoodkind/configs/pull/562) pinned the
same release in production and merged as `ea5d92b6`. Required checks passed
with no unresolved review thread. The clean merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff`, then
`./configsctl deploy deploy-mwan --limit mwan_servers`. Check mode reported
187 successful tasks and 14 proposed changes. Apply reported 242 successful
tasks and 23 changes. Neither run reported an unreachable host or failure.
Deploy trace `20260929-070359-deploy-878087` passed reboot, egress, and
mapped-address gates without rollback. VM 113 reported clean `acb81bb`.
The interface manager, agent, and RESTCONF services were active. Downstream
LXC 102 returned HTTP 200 over both families. Forty fresh HTTPS requests per
run returned 200. Gateway captures showed new IPv4 and IPv6 connections on
both Webpass and AT&T. Independent Suburban probes received three of three
replies from each provider's mapped IPv4 address and IPv6 edge address.
Internal IPv4 and IPv6 BGP TCP sessions remained established.

The exact RESTCONF IPv6 ownership subtree for `enwebpass0` returned HTTP 200
in testbed and production. Both reported a present RA router and usable
observed addresses. Address origin remained `unknown` because kernel netlink
does not establish whether an address came from SLAAC, DHCPv6, or static
configuration. A request for the entire production RESTCONF data tree made
Rousette abort while constructing an invalid stream URL. Systemd restarted
the service, and the exact ownership request then succeeded. The broad-tree
request is not required for this slice's ownership observation.

LXC 102 received 1,800 of 1,800 IPv4 replies and 1,799 of 1,800 IPv6
replies across the production apply and reboot. IPv6 missed sequence 483
at the interface-manager restart. The largest intervals between successful
replies were 1.006 seconds for IPv4 and 2.011 seconds for IPv6. Reply TTL
changed to 45 during the service restart and again during the reboot.
This run did not sample OPNsense's selected route during either interval,
so TTL changes alone do not prove which next hop forwarded the packets.
No live provider changed owner. MWAN-517 remains In Progress for IA_NA and
ownership acceptance. MWAN-227 must establish the shared DHCPv6 client first.

### MWAN-227 DHCPv6 client release, September 29, 2026

[MWAN PR #109](https://github.com/agoodkind/mwan/pull/109) merged DHCPv6
prefix acquisition as `ac1c5a7`. [MWAN PR #110](https://github.com/agoodkind/mwan/pull/110)
merged RA-gated startup as `eb03497`. Required CI, independent review,
Docker check and test, and the privileged Kea network namespace test passed.
Release `202609291633-6b-eb03497` publishes merged commit `eb03497`.
The release archives match the Configs checksums.

[Configs PR #563](https://github.com/agoodkind/configs/pull/563) merged the
testbed pin as `6be36af2`. The merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`,
then the same command without `--check --diff`. Check mode reported 176
successful tasks and 19 proposed changes. Apply reported 227 successful tasks
and 21 changes, with no failed or unreachable hosts. Trace
`20260929-095813-deploy-131902` passed reboot, egress, and mapped-address
gates without rollback. VM 213 reported clean `eb03497`. The interface
manager, agent, and RESTCONF services were active after reboot. Client 225
received 900 of 900 IPv4 and 900 of 900 IPv6 replies during the deploy.
An overlapping probe received 600 of 600 replies per family. Clients 225
and 226 returned HTTP 200 in both families. Twenty fresh IPv4 and 50 fresh
IPv6 HTTPS requests returned 200. Gateway captures recorded new client
connections on both Webpass and AT&T in both families. OPNsense reported
established primary and failover BGP peers for IPv4 and IPv6.

[Configs PR #564](https://github.com/agoodkind/configs/pull/564) merged the
production pin as `180fbf5a`. The merged checkout ran
`./configsctl deploy deploy-mwan --limit mwan_servers --check --diff`, then
the same command without `--check --diff`. Check mode reported 187
successful tasks and 15 proposed changes. Apply reported 240 successful
tasks and 21 changes, with no failed or unreachable hosts. Trace
`20260929-124936-deploy-183334` passed reboot, egress, and mapped-address
gates without rollback. VM 113 reported clean `eb03497`; its interface
manager, agent, and RESTCONF services were active after reboot. Downstream
LXC 102 received 899 of 900 IPv4 and 899 of 900 IPv6 replies. Both families
missed sequence 288 during the apply before the VM reboot. LXC 102 returned
HTTP 200 in both families. Twenty fresh IPv4 and 50 fresh IPv6 HTTPS
requests returned 200. Gateway captures recorded new client connections
on both Webpass and AT&T in both families. OPNsense reported established
primary and failover BGP peers and selected the primary VM for both default
routes after reboot.

The production probes did not record OPNsense's selected route during the
missed reply. Packet continuity and the later route read do not establish
which next hop forwarded traffic at that instant. This release did not
transfer DHCPv6 or interface ownership from networkd. MWAN-227 remains Todo
until real DHCPv6 server acceptance and exclusive handover pass. MWAN-517
remains In Progress for IA_NA and ownership acceptance.

### MWAN-517 IA_NA release and existing-service acceptance, September 29, 2026

[MWAN PRs #112](https://github.com/agoodkind/mwan/pull/112),
[#113](https://github.com/agoodkind/mwan/pull/113),
[#114](https://github.com/agoodkind/mwan/pull/114), and
[#115](https://github.com/agoodkind/mwan/pull/115) merged the shared DHCPv6
address client, owned address lifecycle, local NPT exception, and configuration
support. The final signed merge commit `d442ba1` produced release
`202609292152-70-d442ba1`. Required CI, Docker check and test, golangci-lint,
four real Kea namespace scenarios, and six privileged firewall tests passed.
The release archive checksum matched the published `checksums.txt`.

[Configs PR #565](https://github.com/agoodkind/configs/pull/565) merged the
testbed pin as `66927454`. The merged checkout passed the deploy check with
176 successful tasks and 18 proposed changes, then applied with 227 successful
tasks and 21 changes. Trace `20260929-151541-deploy-321500` passed reboot,
egress, and mapped-address gates without rollback. VM 213 reported clean
`d442ba1`. Client 225 completed 20 of 20 IPv4 and 50 of 50 IPv6 HTTPS
requests, and client 226 returned HTTP 200 in both families. Gateway captures
showed new client connections on Webpass and AT&T in both families. A separate
primary VM reboot made OPNsense select the failover default route in both
families; client 225 received 300 of 300 replies per family at 200 ms intervals.
OPNsense selected the primary again after recovery. Client 225 received five
of five replies from the Monkeybrains IA_NA address after reboot.

[Configs PR #566](https://github.com/agoodkind/configs/pull/566) merged the
production pin as `d53b8b97`. The merged checkout passed the deploy check with
187 successful tasks and 14 proposed changes, then applied with 240 successful
tasks and 21 changes. Trace `20260929-155123-deploy-65721` passed reboot,
egress, and mapped-address gates without rollback. The deploy created snapshot
`pre-deploy-20260929T155123`. VM 113 reported clean `d442ba1`; its interface
manager, agent, and RESTCONF services were active. Downstream LXC 102 received
900 of 900 IPv4 and 900 of 900 IPv6 replies across the deploy and reboot.
Twenty IPv4 and 50 IPv6 HTTPS requests returned HTTP 200. Captures showed new
client connections on Webpass and AT&T in both families. OPNsense selected the
failover default routes during the VM reboot and selected the primary again
after recovery. The one-second probes observed no interruption; they did not
measure exact route withdrawal or recovery time.

The first bulk HTTPS test used `ifconfig.co`, which returned HTTP 429 rate
limits after successful requests in both families. The repeated test used a
separate Cloudflare endpoint and completed without transport or HTTP failures.
Both environments still configure `systemd-networkd` with `DHCP=yes` on
Monkeybrains. The observed IA_NA address therefore does not validate MWAN's
new client on a live owned link. No owner transfer or reverse transfer occurred.
MWAN-517 remains In Progress until the later ownership cutover exercises that
client; MWAN-518 must add durable lease recovery before the cutover.

The finding counts report blockers, issues to fix, and minor issues found
during review, including findings fixed before the verdict.
The post-verdict column records defects discovered after that review verdict.

| Date | Branch | Class | Reviewer tier | Verdict | Blockers / issues to fix / minor issues | Post-verdict defects | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-09-28 | MWAN PR #86 at `cdb320b` | Link ownership and recovery | Independent adversarial | MERGE-READY | 10 / 2 / 0, fixed before verdict | None found | The public daemon namespace test and kernel namespace race tests passed. `TestModulesForRoleWAN` and `TestModulesForRoleExported` passed after their expected WAN module order included `links` before `health`. `TestLoadMWANOwnedLink` failed against `origin/main` because the old parser rejected direct MWAN link ownership, then passed with this change. The live `origin/main` merge-tree passed. Lint and schema passed before the module order fix; the full test target passed afterward. No deployment was reviewed. |
| 2026-09-28 | MWAN PR #86 follow-up after `1db34fb` | Bridge membership adoption | Independent adversarial | MERGE-READY | 0 / 0 / 0 | None found | The public kernel test failed on the old guard when a replacement bridge reused the configured name and passed with the new guard under `-race`. The test also rejects an unrecorded current membership. The public daemon namespace test and live `origin/main` merge-tree passed. The public deleted-parent test no longer exercises `LinkAdd` rollback after a dependency race; that path was reviewed statically. |
| 2026-09-28 | `codex/mwan-398-mapped` | Mapped and NPT address writer transfer | Independent adversarial | MERGE-READY | 0 / 2 / 0, stale comments corrected | None found | The reviewer independently ran all six public firewall tests and checked the corrected comments and diff. The review did not run a red-green reversal. No deployment was reviewed. |

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
