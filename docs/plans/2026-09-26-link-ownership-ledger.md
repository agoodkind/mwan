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
No shared testbed or production interface-owner cutover has begun. The published
`bb4d1c5` release passed isolated IPv4 forward and reverse transfer on real
virtio interfaces. IPv6 transfer and complete downstream acceptance remain.

| Ticket | Slices | Current execution state |
| --- | --- | --- |
| MWAN-524 | Restore the managed Astound testbed connection first. | Configs PR #522 merged. Deployment, restart, downstream traffic, balancing, and recovery passed. |
| MWAN-516 | 516-model; 516-state | [MWAN PR #51](https://github.com/agoodkind/mwan/pull/51) merged standalone identity as `959fbd3a65955e8156f2ea6c9bf2c90febef762c`. [MWAN PR #55](https://github.com/agoodkind/mwan/pull/55) merged shared interface intent as `2c6df538fbb1174a9189f3098d6ac458256857f4`. [MWAN PR #84](https://github.com/agoodkind/mwan/pull/84) merged state publication as `b601ab8a5f4f44d09b949ab6132c4287ef9dbbef`. The shared model and state publication passed testbed and production checkpoints. |
| MWAN-397 | 397-links | [MWAN PR #86](https://github.com/agoodkind/mwan/pull/86) merged as `446e76fe590e5dc9bbbcfe6fd875a8752f4e404c`. Configs PRs #548 and #549 passed testbed and production deployment with downstream packets, balancing, failover, and recovery. Tack records Done. |
| MWAN-523 | 523-observation | [MWAN PR #60](https://github.com/agoodkind/mwan/pull/60) merged the observer foundation as `13183ea9d6264beac7b85bfd4e7946b15f7da601`. Configs PRs #533 and #535 passed its first testbed and production checkpoints. MWAN PRs #66, #71, #73, #75, #77, and #79 verified route identity, bridge rebinding, router advertisement lifetimes, index reuse, daemon packet recovery, and duplicate-address failure in privileged kernel tests. PR #81 repaired snapshot replay overflow. Configs PRs #544 and #545 passed final testbed and production acceptance. |
| MWAN-530 | Restart handover | [MWAN PR #64](https://github.com/agoodkind/mwan/pull/64) merged forwarding readiness as `798ee6a8dcb57ef91e7d9656e8a368f3f4bd4412`. OPNsense selected the backup during controlled testbed and production reboots, then restored the primary while downstream replies continued. Tack records Done. |
| MWAN-398 | 398-static; 398-mapped-addresses; 398-dhcpv4 | MWAN PRs #89, #91, and #95 through #99 merged static, mapped/NPT, and DHCPv4 ownership code. The combined release passed testbed and production deployment. Live providers remain under networkd, so owned acquisition and transfer acceptance remain. Tack records In Progress. |
| MWAN-227 | 227-delegation | Client code merged; live acceptance pending. |
| MWAN-517 | 517-autoconfiguration; 517-dhcpv6 | DHCPv6 code and inactive release passed testbed and production deployment. Live ownership acceptance remains. Tack records In Progress. |
| MWAN-518 | 518-restart | [MWAN PR #117](https://github.com/agoodkind/mwan/pull/117) merged DHCPv4 restart validation as `c51c063060b4d1252db5c36f6578cf519d4c6e42`. [MWAN PR #118](https://github.com/agoodkind/mwan/pull/118) merged the durable lease store as `63cbe758258c97326dfff6c2533e41d58770a890`. [MWAN PR #120](https://github.com/agoodkind/mwan/pull/120) merged DHCPv6 restart validation as `eae8f3c8fb4806cc739e0e2508c86815144a3bf2`. [MWAN PR #121](https://github.com/agoodkind/mwan/pull/121) merged daemon integration as `5666b3dd`. Release `202609300154-76-5666b3d` passed testbed deployment, downstream traffic, balancing, and restart handover under the merged Configs #567 pin `23cd8f14`. The clock correction and executable selector merged; five published-release recovery cases passed in isolation on VM 213. Production promotion and live ownership acceptance remain. Tack records In Progress. |
| MWAN-505 | 505-route-repair | [MWAN PR #52](https://github.com/agoodkind/mwan/pull/52) merged as `18941243f1e8fa3d5623a04440e4d90de796d1f4`. Namespace packet tests and live testbed route and rule deletion checks passed. The release passed production deployment and downstream acceptance. |
| MWAN-521 | 521-configuration; 521-deployment | [Configs PR #527](https://github.com/agoodkind/configs/pull/527) moved MAC discovery before rendering. [Configs PR #558](https://github.com/agoodkind/configs/pull/558) rendered explicit connection IDs and networkd ownership. Configs PRs #559 and #560 passed testbed and production deployment. Complete role rendering merged in Configs #569. Configs #570 activation remains open after three corrected fixture cases passed independently. Isolated physical IPv4 transfer passed; shared testbed transfer remains. Tack records In Progress. |
| MWAN-522 | 522-acceptance | Physical IPv4 transfer passed. Aggregate protocol and shared runtime acceptance remain pending. |
| MWAN-519 | 519-first-connection | Execution has not started. |
| MWAN-399 | 399-remaining-connections | Execution has not started. |
| MWAN-400 | 400-retirement | Execution has not started. |
| MWAN-401 | 401-testbed | Execution has not started. |
| MWAN-520 | 520-production | The shared model, route repair, observer, state publication, and release `202609291301-67-acb81bb` passed production deployment and downstream continuity checks. Live ownership transfer remains. |

## Resume the work

MWAN-524 restored Astound as a managed testbed connection. Link management,
owned DHCPv4, kernel IPv6 policy, and the shared DHCPv6 client passed code
checks. Their inactive releases passed testbed and production deployment
without transferring a live provider. MWAN-518 restart recovery merged
in PR #121. Its testbed deployment passed after an SSH failure. Downstream
traffic and primary-to-backup handover passed during restart and reboot.
Released-binary recovery checks exposed insufficient recovery clock precision.
The clock correction and released-binary test harness merged, and five
published-release recovery cases passed in isolated namespaces on VM 213.
The published `bb4d1c5` release passed physical IPv4 forward and reverse
transfer in isolated QEMU guests. Complete role activation, aggregate protocol
execution, IPv6 transfer and shared downstream validation remain required
before production promotion. Keep networkd on AT&T until retirement is confirmed.

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

### MWAN-518 restart integration, September 29, 2026

PRs #117, #118, and #120 merged the DHCPv4 client, lease store, and DHCPv6
client foundations. The daemon validates saved leases for owned WAN, OOB,
and mainv4 roles. It defers removal of matching journaled addresses during
validation, records WAN lease storage status, and reports rejected recovery
separately from fresh acquisition. The optional
`lease_directory` setting now enables storage on testbed. Production retains
its previous release without that setting.

`make docker-make TARGETS='check test'` passed after the integration changes.
`make test-netns` passed for the route, firewall, steering, and netif packages.
Its first run failed one DHCPv6 test that required the next exchange within
12 seconds of the first packet. The test passed in isolation. The bounded
suite assertion now allows 16 seconds for scheduler delay. The privileged
daemon process suite passed OOB restart, late interface, expired OOB record,
and owned DHCPv6 Rebind scenarios in 101.190 seconds. A separate real Kea
test passed corrupt owned-WAN record rejection and subsequent reacquisition.
That test exposed missing YANG enum values for `rejected` lease persistence
and `waiting` address application; both are defined in the integration branch.

The privileged daemon checks ran from the integration worktree with these
commands:

```bash
docker run --rm --privileged --platform linux/arm64 -v "$PWD":/src -w /src -v mwan-wanconfig-gomod:/go/pkg/mod -v mwan-wanconfig-cache-arm64:/root/.cache -e GOWORK=off mwan-protocol-runner:arm64 go test -v -count=1 -tags 'netns firewallnetns' ./cmd/mwan -run '^Test(OOBDHCPv4Daemon(RestartRecovery|LateInterfaceRecovery|RejectedRecovery)|OwnedDHCPv6DaemonRestartRecovery)$'
docker run --rm --privileged --platform linux/arm64 -v "$PWD":/src -w /src -v mwan-wanconfig-gomod:/go/pkg/mod -v mwan-wanconfig-cache-arm64:/root/.cache -v mwan-wanconfig-gomk-arm64:/src/.make -e GOWORK=off -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src mwan-protocol-runner:arm64 go test -v -count=1 -tags 'netns firewallnetns' -run '^TestOwnedDHCPv4RejectedRecoveryRuntime$' ./cmd/mwan
```

Independent review found and the integration branch corrected DHCPv6
retransmission after a silent server, per-association expiry retention,
unconfigured WAN record pruning, recovery event ordering, OOB expiry cleanup,
and status after rejected records or failed deletion. PR #121 review found
that a startup expiry could withdraw a journaled DHCPv4 assignment before
recovery completed. Both DHCPv4 role modules now ignore that synthetic event;
their namespace lifecycle tests verify the saved address and route remain.
PR #121 merged daemon integration as `5666b3dd`. Release
`202609300154-76-5666b3d` is published and verified. [Configs PR #567](https://github.com/agoodkind/configs/pull/567)
merged the testbed pin and lease storage configuration as `23cd8f14`.
The testbed deployment and existing-service checks passed below. Production
promotion and live provider owner transfer remain pending.

### MWAN-518 testbed deployment attempts, September 29, 2026

The merged Configs checkout passed
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff`
with 176 successful tasks, 19 proposed changes, and zero failures.
The first live run was interrupted. Its final result is unavailable.
VM 213 still reported `d442ba1` after interruption. Its configuration had
no `lease_directory` setting.

The second live run failed during SSH key deployment to Suburban.
Its recap reported 32 successful tasks, zero changes, one unreachable host,
and zero failed tasks.
The Suburban SSH journal first reported `MaxStartups` at 20:25:31 PDT.
The journal subsequently reported failed-authentication penalties.
Sequential SSH resumed at 20:26:56 PDT.

The downstream battery then reused SSH connections and ran commands sequentially.
Eight baseline HTTPS requests returned HTTP 200.
Matched IPv4 connections used Webpass twice and AT&T twice.
Matched IPv6 connections used Webpass once and AT&T three times.
These baseline results precede deployment acceptance.

The third live run completed with 229 successful tasks, 24 changes, zero
unreachable hosts, zero failures, zero rescues, and zero ignored tasks.
The supported apply command was
`./configsctl deploy deploy-mwan --limit mwan_suburban_servers`.
The hypervisor verdict for trace `20260929-203535-deploy-866386` reported reboot,
egress, and mapped-address return codes of zero from 03:45:21 to 03:46:59 UTC
on September 30. Rollback tasks were skipped. VM 213 reports clean commit
`5666b3d`, binary hash `43bede7022d3`, and
`lease_directory = "/var/lib/mwan"`. The interface manager, agent, Rousette,
and wanconfig proxy services are active.

Clients 225 and 226 each completed 20 fresh IPv4 and 20 fresh IPv6 HTTPS
requests. All 80 requests returned HTTP 200. Gateway captures matched transit
and provider SYNs by destination and TCP sequence. IPv4 selected Webpass 16
times and AT&T 24 times. IPv6 selected Webpass 21 times and AT&T 19 times.
IPv4 client attribution uses the controlled request interval because OPNsense
rewrites client addresses and ports. The capture ended at its configured
timeout after the request commands completed successfully.

Client 225 transmitted 1,798 probes per family across deployment and reboot.
IPv4 received 1,797 replies and missed sequence 1363 near 03:46:06 UTC.
The adjacent replies were 2.026 seconds apart. IPv6 received all 1,798 replies.
The full observation's largest reply intervals were 2.437 seconds for IPv4
and 2.456 seconds for IPv6, both before deployment at 03:28:31 through
03:28:33 UTC. One-second probes do not
establish the exact interruption duration.

OPNsense selected backup `10.240.240.4` and `3d06:bad:b01:201::4` during
the interface-manager restart and again during reboot. It restored primary
`10.240.240.3` and `3d06:bad:b01:201::3` after both events. Restart selections
changed within 03:41:50.179 through 03:41:50.636 UTC and returned within
03:42:06.086 through 03:42:06.561 UTC. Reboot selections changed within
03:45:32.927 through 03:45:33.375 UTC and returned within 03:46:28.573
through 03:46:29.022 UTC. The observer recorded 193 failed samples before the
successful deployment and zero failed samples during restart or reboot.

All seven configured interfaces still report owner `networkd`. This deploy
proves compatibility and existing-service handover, not lease recovery on a
live MWAN-owned provider. MWAN-518 remains In Progress. Production retains
release `202609292152-70-d442ba1`.

### MWAN-518 recovery clock correction, September 30, 2026

The merged [clock correction](https://github.com/agoodkind/mwan/pull/122)
uses Linux `CLOCK_BOOTTIME` at nanosecond
precision instead of the rounded uptime value. Its public store regression
test saves and loads real DHCPv4 records and rejects any extension of the
saved expiry. The merged [protocol harness](https://github.com/agoodkind/mwan/pull/123)
accepts an explicit daemon
executable through `MWAN_PROTOCOL_TEST_BINARY` and rejects invalid supplied
paths instead of compiling another executable. The merge commits are
`9b3363a29867efac4e8aab08b8581f50183ca6fd` and
`5dd0ce00e5deaa4b3c53e4014ce34cbec1879f5a`, respectively.

The explicit corrected executable passed the real DHCPv6 daemon recovery
test three consecutive times in 96.496 seconds. This executable is not
installed on testbed or production.
Live ownership acceptance remains required.

### MWAN-518 published-release recovery, September 30, 2026

Release `202609300456-79-5dd0ce0` passed all five public process-recovery
tests on native AMD64 in testbed VM 213. The selected executable was a copy
of the published artifact, not the live service executable. The archive
SHA256 matched `7a0a0cec8fc16ffed0feb33911f394ffdeb0ed8d2f274665dcf01d3db181ff2c`.
The executable SHA256 was
`19cc23c6642f2c74bcbd676be506f7b2e382f80c8485825d1286d69fcddfff7a`.

The runner was compiled from merged commit `5dd0ce00` with
`go test -c -tags 'netns firewallnetns' ./cmd/mwan` on Linux AMD64.
Its SHA256 was
`b6fc11c6b304b3f2980637aac738b2db9acd22afbe898e39e50a068b7f12262e`.
Kea 2.6.3 and its lease-cleanup executable ran from a temporary dependency
bundle. The runner used private network and mount namespaces, a separate
sysrepo repository and shared-memory prefix, and temporary `/run` and
`/dev/shm` mounts. The tests did not access the live service sockets.

| Public test | Result | Duration |
| --- | --- | --- |
| `TestOOBDHCPv4DaemonRestartRecovery` | Passed | 57.49 seconds |
| `TestOOBDHCPv4DaemonLateInterfaceRecovery` | Passed | 2.03 seconds |
| `TestOOBDHCPv4DaemonRejectedRecovery` | Passed | 9.97 seconds |
| `TestOwnedDHCPv4RejectedRecoveryRuntime` | Passed | 17.56 seconds |
| `TestOwnedDHCPv6DaemonRestartRecovery` | Passed | 35.76 seconds |

No selected test skipped. The transient unit exited successfully after
122.905 seconds, used 16.965 CPU seconds, and peaked at 87.9 MB of memory.
The gateway service remained active. Client 225 completed fresh IPv4 and
IPv6 HTTPS requests with HTTP 200 afterward.

The earlier AMD64 Docker emulation attempt could not create namespace
subprocesses. Native client 225 passed the OOB cases but denied the eBPF
operations required by the WAN cases, including with unlimited locked
memory. VM 213 passed those cases with unlimited locked memory and the
private runtime mounts. These results establish isolated process recovery.
At that recovery checkpoint, the live gateway ran `5666b3d` with
networkd-owned providers.
Installed-service deployment, reboot validation, provider transfer, and
production promotion remain separate acceptance requirements.

The finding counts report blockers, issues to fix, and minor issues found
during review, including findings fixed before the verdict.
The post-verdict column records defects discovered after that review verdict.

| Date | Branch | Class | Reviewer tier | Verdict | Blockers / issues to fix / minor issues | Post-verdict defects | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-09-28 | MWAN PR #86 at `cdb320b` | Link ownership and recovery | Independent adversarial | MERGE-READY | 10 / 2 / 0, fixed before verdict | None found | The public daemon namespace test and kernel namespace race tests passed. `TestModulesForRoleWAN` and `TestModulesForRoleExported` passed after their expected WAN module order included `links` before `health`. `TestLoadMWANOwnedLink` failed against `origin/main` because the old parser rejected direct MWAN link ownership, then passed with this change. The live `origin/main` merge-tree passed. Lint and schema passed before the module order fix; the full test target passed afterward. No deployment was reviewed. |
| 2026-09-28 | MWAN PR #86 follow-up after `1db34fb` | Bridge membership adoption | Independent adversarial | MERGE-READY | 0 / 0 / 0 | None found | The public kernel test failed on the old guard when a replacement bridge reused the configured name and passed with the new guard under `-race`. The test also rejects an unrecorded current membership. The public daemon namespace test and live `origin/main` merge-tree passed. The public deleted-parent test no longer exercises `LinkAdd` rollback after a dependency race; that path was reviewed statically. |
| 2026-09-28 | `codex/mwan-398-mapped` | Mapped and NPT address writer transfer | Independent adversarial | MERGE-READY | 0 / 2 / 0, stale comments corrected | None found | The reviewer independently ran all six public firewall tests and checked the corrected comments and diff. The review did not run a red-green reversal. No deployment was reviewed. |
| 2026-09-29 | `codex/mwan-518-clock-recovery`, uncommitted changes over `5666b3d` | Lease recovery clock precision | Independent adversarial | MERGE-READY | 0 / 1 / 1, fixed before verdict | None found | `TestLeaseRecoveryDoesNotExtendSavedDeadline` failed against the mounted `origin/main` implementation with a 5.084375 ms extension on attempt 0. The fix passed five runs with 200 public store roundtrips. A freshly compiled test executable passed all six `TestLeaseRecovery*` tests, including backward clock, identity, corruption, and pruning checks. Review found a Linux/386 type mismatch and an incomplete duration overflow bound. Both were corrected; Linux/386 compilation passed. The reviewer independently ran the real daemon `TestOwnedDHCPv6DaemonRestartRecovery`, which builds a fresh executable and checks its modification time; it passed in 35.759 seconds. Clock capture ordering and compatibility with previous rounded snapshots were reviewed statically. The refreshed live `origin/main` merge-tree passed. Full project gates, suspension, and deployment were not repeated by this reviewer. |
| 2026-09-30 | codex/mwan-521-configured-routes | Configured routes, schema upgrade, and downstream restart | Independent adversarial | MERGE-READY for configured-route contract | 1 / 0 / 0, fixture prerequisite corrected | None found | Real sysrepo upgrade and four mixed-owner collision cases passed independent red-green checks. Removing configured-route installation caused the daemon test to fail. The final daemon fixture passed ten independent repeats in 68.246 seconds with initial, deletion repair, stopped-daemon loss, restart, and foreign-route assertions. The fixture waits for the actual IPv6 selection rule. Global forwarding readiness and deployment were not accepted by this review. |

| 2026-09-30 | `codex/mwan-518-dhcp-test-isolation` | DHCP restart fixture isolation | Independent root review | MERGED as `fde02feed` | 0 / 0 / 0 | None found | Real udev changed the fixture MAC after ACK and reproduced NAK and silent-server failures on unchanged main. Root review accepted a child network namespace with unchanged packet assertions, deadlines, and production identity checks. Twenty runs passed with host udev active; a separate instrumented copy recorded 60 valid server REQUEST packets across 20 passing runs. A host control MAC changed under the active daemon. Full Docker check/test and privileged namespace gates passed. Nonroot and missing-capability cases reported explicit skips. All required CI checks and the namespace, firewall, and ARM64 suites passed. Graphite AI Reviews and PR-Agent passed; the final thread read found no unresolved threads. |

### MWAN-521 and MWAN-522 acceptance corrections, September 30, 2026

The local evidence root is `~/.local/state/mwan305`. Artifact identifiers
below refer to retained local reports, not repository files.

[Naming transfer PR #137](https://github.com/agoodkind/mwan/pull/137)
merged as `bb4d1c5d547d4e776ec11e1f49ecca25ed2590a8` from signed
`52c06d55343262ad8a3db33716b78c1a2e934052`.
The independent production-unit test passed all four cases in 6.806 seconds.
The original-main control failed generated-file transfer in 6.717 seconds;
foreign, retained, and symlink rejection passed. The actual journal reported
rejection of `20-ownedboot0.link`. The implementation permits only obsolete
regular generated files absent from the current networkd render and repeats
strict validation after pruning. This fixture does not prove physical
acquisition or reverse transfer.

All ten required checks passed. A nonrequired IPv6 firewall packet check
failed without packet-state evidence. Ten focused repetitions passed on the
PR source and ten passed on main. The restarted namespace, firewall, and
ARM64 CI suites passed. Govulncheck reports the same advisory on main. The pinned
GoBGP v4.7.0 source includes the published correction. GitHub limits affected
versions to `<=4.3.0`, while the Go database has no fixed-version boundary.
The scanner and dependency remain unchanged. Evidence:
`20260930-link-naming-fix/independent-review.md` and
`20260930-link-naming-fix/ci-evidence-verdict.md`.

[Route expiry PR #138](https://github.com/agoodkind/mwan/pull/138)
remains open at signed `efde596b018ed2dab06d73690637180d4feb65d7`.
The earlier `18c1d346` raw-NDP and radvd daemon tests passed in 22.751 seconds.
Reverting gateway, monitor, and inspection filtering separately failed the
intended public assertions. Review found that the first inspection control
checked only a permanent provider route; the final fixture also rejects
expired main-table RA defaults. Permanent routes, multipath observation,
and live RA-default deletion passed. The full author ARM64 project gates
passed before the final assertion. Independent review accepted the final
source. After integration with merged `bb4d1c5`, both daemon tests passed
in 22.965 seconds. The complete ordered startup and naming fixture passed
in 6.577 seconds. The integrated merge tree matched the reviewed source.
One disputed Graphite thread remains open; merge and deployment remain pending.
The local report `20260930-radvd-autoconfiguration/independent-review.md`
preserves the controls and the first runner's missing-udev prerequisite failure.

[Configs PR #570](https://github.com/agoodkind/configs/pull/570)
remains open at signed `21006a9ae7f2cdfe24e37026311cc209a87a8996`.
The earlier independent 11-case run passed at `59bc0301`. Commit `dc2a4cae`
extracts duplicated fixture configuration. Two independent three-case runs
each failed first-install and restoration at unchanged 120-second whole-play
deadlines; recovery diagnosis passed. A quiet repetition still failed.
The transport control reduced first-install Docker exec calls from 214 to 98.
Commit `21006a9a` enables pipelining only in the Docker fixture inventory.
The three affected cases then passed independently in 218.2 seconds with
`ANSIBLE_PIPELINING` explicitly unset and unchanged 120-second play deadlines.
The local report `20260930-role-activation/final210-acceptance.md`
records the root session's final output; its complete transcript is unavailable.
The duplicate-configuration thread is resolved. Two disputed review threads
remain open. No merge or revised deployment occurred.

The original downstream harness used signed Configs commit
`48c51ee4c8f063366b8e1d8d1fa458f6d5fc0bb9`. Its actual Linux public suite
passed three examples in 80.99 seconds, including packet mapping, restart
history, SIGINT cleanup, and premature observer termination. Project RSpec
passed 186 examples with four explicit skips; lint passed. Independent
review passed three real public cases in 80.86 seconds and additional controls.
A later live read found 2,464 compressed product log archives. A real archive
had gzip signature `1f 8b` and passed integrity validation. The original reader
used plain `cat` and checked its history deadline only after reading all files.
The earlier plaintext rotation fixture missed this defect.

The mechanical library extraction at signed
`1e53f44b6595b318bbbb1e9729b1fd0e0a3d945f` preserved all 32 parsed declarations.
The local report `20260930-downstream-harness/refactor-static-review.md`
also verified unchanged command and public fixture bytes; its deliberate
TERM-argument change failed the comparison control.

[Configs PR #571](https://github.com/agoodkind/configs/pull/571) corrects gzip
reading, history-window selection and the overall observation budget.
Signed `c5d17e7e51d48e7e2446fd11aded1f40b83db6c4` passed three public cases
in 95.47 seconds, including actual daemon log rotation and compression after
restart and the one-second history-budget failure control. RuboCop passed
for nine files and `./configsctl lint` passed. The
local report `20260930-downstream-harness/history-c5-verification.md`
records exact commands, retained artifacts and the unavailable complete terminal
transcript. Signed follow-up `2c19eeebd8c1cbeee5750ef7e1437969f6288186`
replaces dynamic assignments with eight explicit plan-field assignments.
Its RuboCop check passed. The independent exact `c5d17e7e` suite passed all
three cases in 94.63 seconds. Restoring only the old history reader failed
in 29.86 seconds on actual product gzip. Removing only the observation budget
failed the unchanged three-second assertion with elapsed time 22.724572385
seconds. The mechanical `2c19eeeb` comparison verified the eight original
field mappings and identical remaining bytes. Both signatures passed with
status `G` and raw `gpgsig` headers. The
local report `20260930-downstream-harness/independent-c5-final-review.md`
accepts both commits. The explicit-assignment thread is resolved; two disputed
Graphite threads remain open. Actual Proxmox harness execution, unequal
provider distribution and shared downstream acceptance remain unaccepted.

### MWAN-522 physical IPv4 transfer, September 30, 2026

Published release `202609301143-84-bb4d1c5` passed forward and reverse IPv4
transfer on two fresh isolated QEMU guests with real virtio interfaces and Kea.
All four release archives matched published checksums and asset digests;
attestations included exact merged source
`bb4d1c5d547d4e776ec11e1f49ecca25ed2590a8`. The guest reported a clean source
checkout and an ARM64 binary with SHA256
`44de93576083f7cc8b73abe123b1cd55ededc553e86bbf0cadcbcf9b4fc9aaef`.

Membership withdrawal preserved the selected physical link and acquisition
while downstream requests used the second provider. Networkd release removed
the target's IPv4 addresses and routes. MWAN then acquired the same address
with the preserved DHCP client identifier. Reverse release stopped the client,
emptied address, promotion and kernel journals, and restored prior kernel
policy before networkd reacquisition. Every recorded downstream request passed.
The unrelated provider received five requests during the transition phases.

The baseline, MWAN and reacquired captures contained two, three and three
target requests with exact option 61 `01:52:54:00:52:20:01`. Each ten-second
released interval contained zero target DHCP packets. All five captures
reported zero kernel drops. Both guests synchronized, powered off gracefully
and exited with status zero. The
local report `20260930-exclusive-release-bb4d1c5/qemu-virtio/transfer-verdict.md`
preserves commands and artifacts. This result establishes this IPv4 transfer
configuration; it does not establish IPv6 transfer, load distribution or
continuous delivery between the recorded requests. No shared guest changed.

### MWAN-522 aggregate protocol gap, September 30, 2026

The source audit at merged `6f415f49e8667328f327130770e75af72e608ee3`
found that `make test-protocol` executes only bootstrap. The required source
contains 21 namespace cases and two separate opted-in systemd cases. Only
nine cases use the explicit released-binary selector. The
local report `20260930-protocol-target-audit/audit.md`
records the exact selectors, prerequisites and required execution changes.
Aggregate execution and consistent released-binary selection are under
implementation; no passing aggregate result exists at this checkpoint.

The initial assembled namespace run passed 20 cases and failed
`TestOwnedDHCPv6IAAddressOnlyRuntime` on its actual local IPv6 packet assertion.
The total was 304.680 seconds. Both real systemd cases passed in 11.261 seconds.
The local artifacts
`20260930-protocol-target-audit/implementation/namespace-1166649602/events.jsonl`
and `20260930-protocol-target-audit/implementation/systemd-4189432965/events.jsonl`
preserve the actual result. The exact-source and merged-main comparison is
active; the failure's cause is unestablished. Aggregate acceptance remains
incomplete.

The resolver Graphite stack was restacked onto `6f415f49` and published.
[Parent PR #130](https://github.com/agoodkind/mwan/pull/130) uses
`e23491b1d0c6152b06906a888332b455d5da9c3f`; [child PR #135](https://github.com/agoodkind/mwan/pull/135)
uses `fab954102da6f2ccb4570113fbb178ad10119a2a`. Both documentation checkpoints
remain after conflict resolution. All 11 rewritten commits passed signature
verification with status `G` and raw `gpgsig` headers. Independent integration
review, fresh CI and automated reviews remain pending. Neither PR merged or
deployed at this checkpoint.

MWAN-521 and MWAN-522 descriptions in Tack include this evidence. Both remain
In Progress. The attempted UUID state-field update was rejected; the
description-only retry passed without changing state or other properties.

No correction above has a revised shared deployment acceptance result.
IPv6 physical transfer, complete downstream testbed acceptance and production
promotion remain required. Production is unchanged.

### Review corrections and merged preparation, September 30, 2026

Configs PR #570 merged as `c6a9a869bc57a5bb69326681416b3d4661aa278b`.
All required checks passed and all review threads are resolved. Independent
final affected-case acceptance passed three cases in 218.2 seconds. The
Configs primary checkout now matches that merged revision. No revised shared
deployment has run.

MWAN PR #138 merged as `9baef7654c5ceb724990b392603c017a3df44c2f`.
The independent kernel route-expiry controls passed before merge. PR #141
was rebased onto that revision and published at signed
`dbcb26d86be87d6a54acf9e00fa7b22ed74ad17c`. The required manifest now includes
the real radvd case. Its child selector preserves the current test name.
Host runner compilation passed. Fresh integrated execution of all 22
namespace and two systemd cases remains required.

The resolver logging candidate passes both actual logging analyzers on the
complete Linux ARM64 packages. It records the failed operation without
repeating the error string and retains the final daemon diagnostic. It
removes intermediate warnings while preserving joined failures, connection
identifiers, leaf operation context, and readiness handling. The earlier
policy-conflict claim was an interpretation error. Implementation and runtime
verification remain pending.

The IPv6 physical fixture requires the existing BGP route installer and a
real downstream FRR peer. Attempt six passed all five owner and membership
loader validations. Its FRR peer remained Active with zero messages at the
unchanged 15-second prerequisite, before baseline packets or ownership
changes. Both guests shut down gracefully with status zero. The local report
`20260930-dualstack-physical-transfer/attempt6/fixture-verdict.md` preserves
the actual service and socket evidence. Transport diagnosis remains active.

The approved production phases preserve AT&T and networkd for legacy
connections. Only final removal waits for AT&T retirement. Shared testbed
acceptance and production promotion remain required. MWAN-521 and MWAN-522
remain In Progress in Tack.

### Integrated acceptance and transfer blockers, September 30, 2026

PR #141 at `dbcb26d86be87d6a54acf9e00fa7b22ed74ad17c` passed all 22
namespace cases in 337.235 seconds and both systemd cases in 9.905 seconds
in CI. Six separate firewall cases passed. All required Go checks passed,
and all review threads are resolved. GitHub blocks merging because the
required GitGuardian check is absent. The retained evidence is
`20260930-protocol-target-audit/integrated-dbcb26d/ci-firewall-protocol.log`.

The resolver stack is published through Graphite at parent
`825e2493ac80d2fe204bcb212476d8ca8b0d5506` and child
`c7bfb6bd34a64a2fd452136d7a7643ac6a01ce00`. The module records the failed
application without repeating the error string, then returns its contextual
wrapped error. The daemon retains the final diagnostic. Both affected
Linux ARM64 packages passed Golangci with zero issues. Both logging
analyzers and all 14 branch-local signature checks passed. The real
masked-resolved failure and recovery test is published; current CI and
runtime acceptance remain pending.

Configs PR #571 is published at
`3725a8e71c1df9b63b053672254179b61b8adc4d`. It removes nullable validator
construction and guarantees teardown after a failed injection thread.
Lint and data CI passed. Fresh public Linux acceptance remains pending
after Docker runtime operations stalled. The valid construction thread
remains open; the incorrect swallowed-error finding is resolved with
actual Ruby exception evidence.

Physical attempt seven established BGP learning in main, table 100, and
table 300. Peer withdrawal and recovery passed. Networkd client release
passed. Full transfer failed: the management DHCP default metric collides
with the target default, and the new address authority rejects the retained
NPT edge address without an ownership record. The next fixture must set
the management metric to 9000 before baseline. The product requires a
proper NPT address handover correction before another transfer attempt.

The unbound IPv6 client selected the edge exception instead of the LAN
translation source. Its short baseline capture recorded zero captured
packets. These results do not prove LAN-prefix translation. The next
fixture must bind the LAN address and capture actual packets. Both guests
powered off cleanly. The retained reports are
`20260930-dualstack-physical-transfer/verdict.md` and
`20260930-dualstack-physical-transfer/npt-edge-handover-brief.md`.

Tack descriptions for MWAN-521 and MWAN-522 include this checkpoint. Both
remain In Progress. AT&T retirement does not block earlier production
cutover phases. Networkd continues managing legacy connections until
retirement. Full shared testbed acceptance and production promotion remain
required; no new shared deployment occurred at this checkpoint.

### Protocol merge and Docker recovery, September 30, 2026

PR #141 merged as `2f547619cec6be859aca269132ec982e60b91dec` after all
ten required checks passed and all review threads were resolved. The clean
MWAN primary checkout matches the merge. The protocol manifest requires
the networkd resolver and ordered startup cases; the new static resolver
and owned-role cases require separate integration into that manifest.

Fresh Configs PR #571 acceptance passed all three public cases in 94.95
seconds using the published `bb4d1c5` ARM64 executable. The guest SHA256
matched `44de93576083f7cc8b73abe123b1cd55ededc553e86bbf0cadcbcf9b4fc9aaef`.
The real missing-file teardown control remains pending. The log is
`20261001-pr571-thread-review/fix/public-three-current.log`.

Five unused MWAN Docker fixtures were removed and individually verified
absent. The resolver and downstream harness fixtures remain available for
required tests. Docker copy requests stalled or failed guest verification;
exec stdin transfer passed the actual guest hash check after recovery.
The user authorized needed Docker restarts by other agents. Tack coordinates
shared restarts. Interrupted runs require fresh validation. The cleanup
report is `20261001-container-cleanup/status.md`.

No new shared testbed or production deployment occurred. NPT edge handover,
complete shared downstream acceptance, and production promotion remain
unfinished. AT&T and networkd retirement do not block earlier accepted
production cutover phases.

### MWAN-521 kernel and resolver acceptance, September 30, 2026

MWAN PR #125 merged configured routes as
`16739bb3d4035b8648e2a08582bfc12352329420`. MWAN PR #127 merged the
published-release recovery evidence as `fc4298be9f1409e2af394353b58a959392cc6051`.
Neither merge deployed the new source.

| Open PR | Source revision | Scope |
| --- | --- | --- |
| #128 | `5894c82d93ec122f8d3e487af006994694aa586b` | Shared kernel link identity verification. |
| #129 | `20ec15fd0a5462b73d316151d0f413b9c06488a1` | Durable per-link kernel policy and startup journal validation. |
| #130 | `6cd53df878cdf68356f1d8aed9195430e19dd74b` | Independent DNS and search-domain ownership through systemd-resolved. |

The Graphite dependency order is #128, #129, then #130. These revisions remain
unmerged and undeployed. Every commit in their branch range has a verified
signature and a raw `gpgsig` header.

Independent kernel review reproduced networkd unit writes before invalid
journal configuration was rejected. Commit `5dce337654244d6dc3f0d612ae3b9e4c91dccb05`
validates journals during construction and permits cleanup without link results
when no enabled MWAN connections remain. Its independent public daemon test
passed in 3.424 seconds. Removing constructor validation reproduced unit writes
and failed in 1.557 seconds before the real D-Bus reload completed. Restoring
the unconditional link-results prerequisite failed cleanup startup in 3.941
seconds. The final independent verdict was MERGE-READY with zero findings.
The restacked source passed the kernel daemon test in 3.672 seconds and all
17 affected protocol fixtures. Full Docker `check test` passed.

The static resolver test uses the production daemon, real systemd-resolved,
and a local authoritative DNS server. It passed in 5.119 seconds, including
mixed IPv4/IPv6 servers, an IPv6-only query, daemon and resolved restarts,
independent field removal, baseline restoration, an unrelated link, and
external domain preservation. After real VLAN pruning, the original resolver
initialization failed cleanup-only startup with `owned link results are required`
in 4.002 seconds. The correction passed the complete test in 3.953 seconds.
Physical hardware restoration remains unproved; restoration used an owned
VLAN. Kernel identity checks remain required before restoration. Full Docker
`check test` passed again after the final restack.

Independent networkd resolver commit `aaee02e4` remains unsubmitted. Its real
daemon test passed three repetitions in 10.114 seconds with networkd rendering,
systemd-resolved, and authoritative single-label resolution. Disabling resolver
rendering failed the unchanged public assertion. Its full Docker `check test`
passed. Integration into the stack and independent review remain required.

The persistent local evidence paths are recorded below for local reproduction.
These files are not GitHub artifacts.

| Acceptance | Persistent local evidence |
| --- | --- |
| Independent kernel finding and controls | `/Users/agoodkind/.local/state/mwan305/20260930-mwan518-testbed/kernel-validation/independent-review.md`, `startup-journal-red.log`, `startup-journal-repro.patch`, `5dce337-public.log`, `5dce337-constructor-red.log`, and `5dce337-cleanup-red.log` in that directory. |
| Restacked kernel and project gates | `/Users/agoodkind/.local/state/mwan305/20260930-mwan518-testbed/kernel-policy-validation/restacked-kernel-green.log` and `restacked-kernel-check-test.log` in that directory. |
| Static resolver and cleanup control | `/Users/agoodkind/.local/state/mwan305/20261001-static-resolver/final-restacked-public-daemon.log`, `final-restacked-check-test.log`, `cleanup-only-red.log`, and `cleanup-only-green.log` in that directory. |
| Networkd resolver and rendering control | `/Users/agoodkind/.local/state/mwan305/20261001-networkd-resolver/runtime-final-green.log`, `runtime-rendering-disabled-red.log`, and `check-test-final.log` in that directory. |

Testbed still runs `5666b3d` with networkd-owned interfaces. Production still
runs release `202609292152-70-d442ba1`. This checkpoint performed no deployment
or provider ownership transfer.

Complete stack review and merge, integrate and review networkd resolver
rendering, then finish Configs role rendering, exclusive ownership transfer,
and the acceptance manifest before the first owner cutover.

## Verify kernel and resolver review corrections

Kernel commit `737f24402a5cff64b14164b7c00a1da3448a1690` separates the
boot-based journal retention decision from file access. The real daemon
regression and full project checks passed. Graphite completed its refreshed
review after the kernel thread reply and resolution.

Resolver commit `f9924591fab325cfa14b426614da3081984eb7e2` saves link-retirement
state only after removing a missing or replaced link. The unchanged daemon
regression failed when empty cleanup created a journal. The corrected test
passed in 5.071 seconds after restacking onto the kernel correction. Full
project checks passed, and all ten stack commits have verified signatures
and raw `gpgsig` headers.

The resolver retains persistence before each DNS mutation. A single save at
the end of reconciliation would omit ownership records after a crash between
the resolver mutation and that save. The cleanup fixture retains unrelated
JSON fields before the production loader validates the document. A suggested
name-only struct would discard required configuration. Removing library
warnings failed the active analyzer's wrapped-error logging requirement;
the existing warnings remain. Review replies document these conflicts.

The resolver evidence is in
`/Users/agoodkind/.local/state/mwan305/20261001-static-resolver/`:
`review-side-effects-red.log`, `review-retirement-public.log`,
`review-retirement-restacked-public.log`, and `review-retirement-check-test.log`.
The kernel evidence is in
`/Users/agoodkind/.local/state/mwan305/20260930-mwan518-testbed/kernel-policy-validation/`:
`pure-decode-public.log` and `pure-decode-check-test.log`.

Independent networkd resolver review accepted commit
`e6504275bea87e89bd04371bb3108f6bcf788613`. Its real daemon test passed in
2.972 seconds. Removing resolver rendering failed the unchanged assertion in
11.460 seconds. The private kernel journal configuration is included;
integration after the kernel stack merges remains required. Its evidence is
in `/Users/agoodkind/.local/state/mwan305/20261001-networkd-resolver/independent-review.md`.

The stack remains unmerged. This checkpoint performed no deployment or live
ownership transfer. MWAN-521 remains In Progress.

## Verify the merged kernel policy and runner

Graphite merged PR #128 as `1e2aba914441a7d6c2e62aa66ccb3fb95d684953`
and PR #129 as `3424270b92d6377997bc0d7e29a8442efc206392`.
PR #131 merged as `d14a9a43ce499208f41548751ab6bb28dd195cf5`.
The ARM64 systemd runner booted actual systemd, resolved, D-Bus, and udev.
Its resolver daemon test passed in 5.201 seconds.

Networkd DNS commit `29459cdf1c14b785938985d615d8b3404bdc51ed`
includes both merged dependencies. Its real daemon resolved the single-label
hostname through the authoritative server in 4.401 seconds. Full project
checks passed. Both rewritten commits have verified signatures and raw
`gpgsig` headers. PR #132 remains open.

Resolver commit `dbc88dbc1cf7507a18b2b0695b7a3ed001a03958` includes the
merged dependencies. Its real daemon test passed in 5.815 seconds, and full
project checks passed. All six rewritten commits have verified signatures
and raw `gpgsig` headers. PR #130 remains open; the review refresh has not
published a completed result after the thread replies.

Configs commit `0420b1097dd949cc0e59de4f0946f4d8b2c86a92` renders
complete networkd-owned management and transit settings. Both environment
renders passed the combined local loader and firewall validator. All 32
render specs passed without skips. Independent validation confirmed that
the installed testbed release rejects the new configured route and typed
resolver fields. Pair this render with a compatible merged release pin.
Do not deploy the combined local validation executable.

The playbook still prunes legacy files before replacement verification.
Complete generated-file verification and runtime selected-unit checks
must precede legacy retirement. Starting the daemon can activate the
rendered transit unit before the legacy transit unit is deleted.

The next owned-role source change requires both admission restrictions
removed and current-pass transit link and family readiness checked before
forwarding announcements. Omitted forwarding preserves kernel policy;
explicitly disabled forwarding cannot establish readiness. Preserve the
existing global module-error behavior.

This checkpoint performed no deployment or live ownership transfer.
MWAN-521 and MWAN-522 remain In Progress.

## September 30: Verify owned roles and test deployment preparation

Configs PR #569 merged as `2aa7b7ab`. Both environment renders passed all
32 specs without skips against published release `202609300730-80-2f9a40a`.
Independent review verified the release digests, role settings, and signatures.
MWAN PR #133 merged as `6323b505` for read-only generated-unit verification.
PR #134 merged as `f9027a8f` for the final conditional-reboot task.

Owned-role source `a17ca0a` passed three independent real-daemon runs in
28.594 seconds. Removing the internal readiness prerequisite failed the
explicit-false assertion; removing the configured route failed IPv6 packet
delivery. Management DNS, transit packets, route repair, and restart passed.
Physical interface transfer and complete operational publication remain
unproven. The negative control retained a transient projection rejection.

The testbed deployment used the clean merged Configs checkout and stopped at
daemon restart verification with `ok=198`, `changed=28`, and `failed=1`.
The first daemon start wrote replacement management/transit units, then waited
for networkd reload until systemd's 90-second startup timeout. Networkd reloaded
after that start was terminated. The automatic second start skipped unchanged
unit reload and became active. An isolated real-systemd reproduction confirmed
the dependency cycle: the daemon waited for a reload job that systemd ordered
after the daemon's readiness notification.

OPNsense selected the backup in both families during restart and restored
the primary afterward. The before and after batteries each passed 80 downstream
HTTPS requests, observed both providers in both families, and recorded zero
kernel capture drops. The four continuous downstream probe streams received
7,199 of 7,200 replies in total. Client 226's IPv4 stream missed one reply;
the other three streams received all 1,800 replies. The missed sequence
preceded daemon restart. OPNsense used the backup for 116 seconds in both families.

MWAN PR #136 merged the direct networkd manager reload as
`6d3945abccc7c70bf5ca0c2ea4b8e57e0ca9fb2c`. Independent review ran the real
daemon under its production systemd unit. Three repetitions passed in
18.209 seconds. Restoring only the previous reload implementation reproduced
the startup timeout in 9.883 seconds. An unchanged restart passed in
6.234 seconds. Required checks and completed automated reviews passed.

Testbed runs `2f9a40a`, but its failed deployment has not passed acceptance.
Production remains on release `202609292152-70-d442ba1`. All live connections
remain networkd-owned. Deploy the merged reload fix to testbed, verify actual
generated-unit activation and restart recovery, then complete reboot and
downstream acceptance before production promotion. MWAN-521 remains In Progress.

### Resolver acceptance and scoped NPT implementation, September 30, 2026

Configs PR #571 merged as `3f28f3bf291ebd6f3374937cb4252896f4feba12`.
Its real missing-file control raised the expected error and removed the
fixture directory, all five namespaces and all six units. The evidence is
`20261001-pr571-thread-review/fix/missing-file-result.json`.

The resolver stack is published at parent
`f005b62236b921a376b358c7b36f5e5f7f12933d` and child
`fd9a89d8efbad13b05b88ace54d1eb919c0edbab`. All four source systemd cases
passed without skips in 26.409 seconds. Explicit read-only candidate
selection passed the two new cases in 12.485 seconds. Its SHA256 is
`6b4bedf71cc39648b42e6a06ff3bd4e521ad26f41f146794132b551ea8c4709d`.
The older published release failed on its unsupported resolver ownership
configuration, confirming that binary selection did not substitute a source
build. These candidate results do not prove a published feature release.

Exact parent CI passed 22 namespace and three systemd cases. Exact child CI
passed 22 namespace and four systemd cases after retrying a dependency
download failure that occurred before test execution. All review threads
are resolved. Required GitGuardian checks remain pending at this checkpoint.
The full local systemd command requires an explicit absolute Configs checkout
through `MWAN_OWNED_ROLE_CONFIGS`; the runner reads its actual bootstrap
sources. The report is
`20261001-networkd-resolver/logging-contract/implementation.md`.

Twenty bounded runs of the earlier monitor and IPv6 firewall failures passed
with unchanged deadlines and assertions. Passing identity observations and
TCP captures do not establish the earlier failures' causes. Failure-time
evidence remains unavailable. No speculative runtime correction was made.
The report is `20261001-netns-failure-evidence/evidence.md`.

PR #140 merged as `6d238595a7fd8dfe85eaecc41c596e7d0a122bea` after all
ten required checks passed. The merged protocol and expired-route worktrees
and their local and remote branches were removed after verifying current
trunk behavior and ancestry. Their ignored build files and logs were moved
intact into `20260930-terminal-cleanup` before removal.

The NPT edge authority correction is in implementation on
`codex/mwan-305-npt-edge-authority`. One scoped journal authority must
preserve unchanged edges across connection owner changes and verify relevant
managed translation removal before releasing obsolete edges. Existing
external-owned NPT configurations remain supported: acquisition stays
external while configured NPT intent authorizes only the edge producer.
Legacy networkd acquisition and AT&T authentication continue.

The paired Configs change must provide the explicit journal path and a
compatible merged published executable. Same-boot startup and pre-reboot
deployment gates require real acceptance; a future reboot does not prove
their success. No further physical guest boot or shared deployment occurred.
MWAN-519, MWAN-521 and MWAN-522 remain In Progress in Tack. IPv6 handover,
complete shared testbed acceptance and production promotion remain required.

### Resolver merge and NPT runtime controls, September 30, 2026

Graphite merged resolver PR #130 as
`422a64763bb472f86e0b84cddeddff0b252f2110` after all ten required checks
and complete source acceptance passed. The original full run timed out during
an IA_NA UDP packet assertion. Its single retry passed the unchanged case in
8.63 seconds and completed both protocol lanes. The failure remains unexplained.
Six isolated unchanged ARM64 samples passed; three captured the requested
datagram in both provider and gateway namespaces. The image lacked `ip`, so
the attempted route and address snapshots establish no kernel state. No
failed-packet capture exists. All 39 retained artifacts passed hash verification,
and the observation container was removed. The report is
`20261001-ia-na-packet-evidence/report.md`.

Graphite rebased dependent PR #135 to
`a25a37b8c40bd35b3bab12f9e4b014b0c21b848a` with unchanged accepted tree
bytes. Its full acceptance passed 22 namespace and four systemd cases.
All ten required checks passed. Graphite merged PR #135 as
`2587f140a10ac8424ef96fe00d57c40853a7fa8a` at 16:19:32 UTC.

The real NPT daemon control passed external link recreation after stale BPF
policy cleanup. A nested-prefix fault control reproduced obsolete edge
retention after the new edge completed duplicate address detection. The
candidate correction verifies actual policy values against desired policy
and rejects an obsolete edge exception. Independent source review also found
that a pure interface rename discarded a scoped receipt without removing
the address. The correction must retain the receipt on that name mismatch.
The expanded real daemon lifecycle passed in 13.82 seconds, including
rename retention/recovery, nested prefix replacement, foreign address
rejection, legacy startup/restart and final NPT withdrawal. Full Linux
ARM64 project checks passed. Feature-removal controls, real delegated-prefix
systemd proof and committed independent review remain required.

The mounted published old executable created actual prior-process NPT rules.
First candidate activation with an empty journal rejected its unjournaled
edge, preserved that address and removed the prior rules while remaining
alive. This proves neither NPT packet continuity nor deployment acceptance.
The actual deployment must prove backup traffic during this interval and
journaled recovery after reboot without adopting foreign addresses.

Configs commit `a7bfbac749e77fe2c8110f726fc21019272c1164` adds explicit
journal configuration and a real inventory runtime rendering fixture. Both
production and testbed TOML and network JSON rendered successfully, and the
fixture lint and diff checks passed. This signed branch remains unmerged;
compatible published release pins and actual first-start acceptance remain
required. The production play starts the new daemon before scheduling reboot.
Current pre-reboot gates do not inspect NPT translation or its journal.

Unchanged edge continuity requires the verified link to remain present.
The existing link authority deletes an MWAN-created VLAN during release;
whole-connection transfer must verify its recreation, assignments and packets.
MWAN-519 has the updated description and remains In Progress. No physical
guest boot, shared testbed deployment or production promotion occurred.

### Published resolver acceptance and NPT fixture prerequisites

Release `202609301619-8a-2587f14` published from merged commit
`2587f140a10ac8424ef96fe00d57c40853a7fa8a`. All four archives passed
checksum, GitHub asset digest and attestation verification. The actual merged
runner passed all four systemd cases against the published ARM64 executable
with zero skips in 22.805 seconds. Guest readback verified SHA256
`67a65dc237819a62e15d23d11baa083c8fbf926c2be469d7000f76829d21c543`
and the read-only binary mount. The runner removed its container after exit
zero. This proves resolver and owned-role runtime behavior, not NPT transfer.
The report is
`20260930-resolver-owned-role-release-2587f14/verification.md`.

The mapped NPT lifecycle now uses the public binary-selection helper.
The explicitly mounted candidate passed in 11.89 seconds. The explicitly
mounted older published executable failed the scoped receipt assertion in
4.18 seconds without rebuilding a candidate. Separate feature-removal
controls failed obsolete-edge retention and rename receipt assertions.
Final committed review and delegated-prefix acceptance remain required.

The real networkd fixture initially started Kea before its provider
link-local address completed duplicate address detection. Kea reported no
open sockets. After bounded link-local readiness, networkd sent Solicit and
Kea returned NoAddrsAvail and NoPrefixAvail because its subnet did not match
the actual client interface. These results do not test NPT authority.
The fixture must select the actual Kea server interface without changing
production code or acceptance deadlines.

The revised physical fixture binds IPv6 HTTP requests to the downstream LAN
address, requires an explicit verified release, and checks captured packet
counts. Its management DHCP metric is 9000 before baseline observation.
Helper validation proved source binding, query preservation, invalid response
rejection and a 2.094-second timeout. These are fixture results; the revised
physical transfer has not run.

Read-only inspection found testbed VM 213 running clean release `2f9a40a`.
All seven configured interfaces use networkd. The service is active, but this
inspection does not establish downstream health or balancing. No shared
testbed deployment or production promotion occurred at this checkpoint.

### Delegated NPT and native readiness regression

The real systemd/networkd, Kea and radvd fixture passed scoped NPT edge
creation and daemon restart in 3.97 seconds against the explicit read-only
candidate. The public prefix command returned the delegated /56, and restart
preserved acquired networkd addresses. Explicit server interface selection
resolved the measured Kea subnet mismatch without changing deadlines.
The log is `20261001-npt-edge-authority/networkd-npt-explicit-candidate.log`.

Independent kernel testing verified rejection of a live BPF edge exception,
rejection of an actual modified shared policy, removal of the old map key
after interface recreation, and final map cleanup. The independent mapped
daemon lifecycle passed in 12.533 seconds. Final signed source review remains
required; these results are not release or deployment acceptance.

MWAN-531 is In Progress under MWAN-305. A production module lifecycle
regression reports native IPv6 ready while an actual old prerouting rule
remains after an nftables apply error. Repairing the incompatible chain and
reconciling removes the rule and restores readiness. The defect exists on
merged main. Its focused correction uses a separate worktree and must pass
before ownership and translation-mode migration acceptance. The evidence is
`20261001-npt-edge-authority/independent-native-readiness-red.log`.

Same-boot migration of historical unscoped MWAN NPT receipts is unsupported.
Ordinary reconciliation can prune those old receipts before scoped NPT
release verification. The companion deployment requires a verified networkd
baseline, reboot, actual old-edge absence and fresh scoped creation. A changed
boot identifier alone does not prove that baseline.

### Native readiness merge and NPT authority review

PR [#142](https://github.com/agoodkind/mwan/pull/142) merged as
`8d408c2f67214e9f2f2a2dca7d295b6ea6944513`. Its signed source
`049c1b22ad1660eeb08a9ac84407f5897e54e081` passed independent real
kernel testing, the full local namespace suite, and Linux ARM64 checks.
CI passed every required check plus namespace, ARM64, firewall and protocol
tests. The nonrequired vulnerability check reported the existing GoBGP
GO-2026-4736 advisory with no fixed version. The correction verifies both
nftables chains and relevant rule absence before reporting native readiness.
This merge does not establish deployed readiness.

PR [#143](https://github.com/agoodkind/mwan/pull/143) contains the scoped
NPT address authority. Signed source
`980f4777ddf026165e995e0412018715955ac1a7` passed the independent mapped
lifecycle in 12.077 seconds and the read-only networkd delegated-prefix
restart in 3.678 seconds. The actual systemd runner passed all five cases
with zero skips in 28.182 seconds. The old published executable failed the
scoped receipt assertion in 3.75 seconds. Local Linux ARM64 checks passed.
Commit `11a3ae23884964509b15e2b88174bd51332f9c08` changes one Init
comment. The branch rebased onto the native readiness merge at signed
`2895d7b49a1a226b74051459396bfae8a931861e`. Final integrated acceptance
remains required.

The two-provider test reproduced a duplicate address detection readiness
race with the original one-hour reconciliation interval. NPT readiness
changed without requesting route reconciliation. The correction requests
reconciliation only when the stored translation result changes. Repeated
packet tests verified both provider marks and no additional request during
the bounded steady-state observation.

Kernel reproduction confirmed that changing the internal interface can
retain a policy on the former internal interface after a translator error.
The current provider and internal index checks incorrectly accepted release
in 0.017 seconds. A fresh translator reproduced the failure in 0.029 seconds:
surviving TC programs retained the original policy map after restart.
Obsolete-edge release must inspect surviving managed programs and their
actual maps before deleting an address. PR #143 remains open for this fix.

Source inspection verified that a hard NPT reconciliation error withdraws
both primary default announcements through the forwarding readiness socket.
The backup speaker does not consume that socket. Actual downstream backup
traffic and interruption duration remain unmeasured for this activation.
Shared testbed deployment and production promotion remain pending.

### Downstream baseline and deployment observation

At 2026-09-30 10:50 PDT, actual OPNsense VM 201 route queries selected
primary next hops `10.240.240.3` and `3d06:bad:b01:201::3`. Source-bound
HTTPS requests from `10.240.240.2` and `3d06:bad:b01:201::2` returned
HTTP 301 with successful, complete, untruncated guest execution. This proves
primary downstream egress at that checkpoint, not balancing or backup.

The existing downstream deployment probe accepts backup next-hop inputs
`10.240.240.4` and `3d06:bad:b01:201::4`. It verifies actual guest route
selection and source-bound HTTPS in both families. Use that expectation
during the preboot primary withdrawal interval and the ordinary primary
expectation after reboot. No additional deployment simulator is required
to measure this shared testbed sequence.

Integrated namespace CI on `2895d7b49a1a226b74051459396bfae8a931861e`
failed because the native-readiness fixture lacked the new real address
authority. The fixture must initialize the production address module before
testing the intended unrelated BPF policy failure. Retrying unchanged CI
does not address that integration failure.

### Surviving NPT program verification

PR #143 published signed head
`d08750553e015ca494ded22b11fc83e1bbf41d55`. Its verifier enumerates
surviving managed programs and reads their actual policy maps. It rejects
references to an obsolete edge and inspection failures. Unrelated policies
and exact desired broader prefixes do not prevent release.

The public old-source control failed address retention in 23.22 seconds
after verifying actual old program and map references before and after
restart. The candidate passed in 11.391 seconds. Its fixture prepares the
replacement edge through the production address authority and waits for
duplicate address detection. An actual WAN address event triggers recovery;
TC filter removal and an MTU change alone do not trigger NPT reconciliation.
Initial fixture failures remain preserved separately from this result.

Independent final controls passed same-process and restart retention,
unrelated policy, broader-prefix, missing-policy-map inspection failure,
and actual cleanup recovery with zero skips. The independent public daemon
lifecycle passed in 14.216 seconds. Local check/test and the full namespace
suite passed. CI passed all required checks and namespace/ARM64 tests;
the firewall/protocol job remained pending at this checkpoint.

The constructor review finding was disproven. Actual execution against
the signed source accepted a valid scoped /128 journal and rejected three
malformed records without panic. Parse errors short-circuit the validation
condition. The finding was resolved without a source change.

The surviving-policy finding was resolved after the verified repair.
A separate review finding about valid cached DHCPv6 delegation during
restart remains under investigation. PR #143 is not merged. No shared
deployment or production promotion occurred.

### Cached delegation restart defect

The independent real-Kea restart control confirmed the cached DHCPv6
finding at `d08750553e015ca494ded22b11fc83e1bbf41d55`. During a
750 millisecond delay before the actual Rebind reply, the daemon deleted
`2001:db8:30::1/128` and its scoped receipt despite remaining lease validity.
The real reply recreated the address. The netlink watcher failed in
10.334 seconds with zero skips. CI run `36757339674` independently failed
the existing restart case with the same deletion in 10.67 seconds.

The correction preserves only an existing scoped edge associated with
the current connection and verified link while recovery is pending and
its matching cached delegated prefix remains valid. Each prefix's own
valid lifetime governs retention. The cache must not establish translation
or routing readiness. Rejection, matching-prefix expiry, and explicit
configuration withdrawal must permit verified cleanup. Implementation
and independent public-boundary controls remain pending.

The external continuous observer passed a real ten-second read-only run:
each family received all ten transmitted packets, and all eight completed
route observations selected the primary. Signal controls preserved partial
reports and removed the observer and client ping processes. This proves
the recorder's operation, not restart continuity, backup selection,
balancing, complete client preflight, or independent monitor history.

PR #143 remains unmerged. Shared testbed deployment and production
promotion remain pending.

### Cached recovery acceptance and external monitor evidence

Signed source `3398e61af4e635a45d80bd97d3a2475998ba6beb` preserves
only matching valid scoped edges during pending DHCPv6 recovery. Independent
real-Kea controls passed delayed validation and expiry in 37.02 seconds,
NoBinding rejection in 7.45 seconds, and native withdrawal in 8.59 seconds.
The committed two-prefix case passed in 12.104 seconds: the matching edge
expired while a different cached prefix remained valid. Every selected
independent case completed with zero skips. The query does not publish
cached delegation or family readiness, and introduces no reverse lock
dependency. The independent review has zero scoped findings.

The complete local protocol namespace suite passed in 324.369 seconds
with zero skips. Firewall, network namespace, check and test gates passed.
The previous executable and deliberately incorrect maximum-lifetime control
failed their intended assertions. All six branch-local signatures passed
verification, including raw signature inspection. Final CI and the updated
authority interface example remain pending at this checkpoint.

Full Cloudflare alert bodies identified the three independent pools
`sf-att-1335`, `sf-webpass-1335`, and `sf-1335-ipv6`. The bounded exact-pool
queries returned sixty alerts and thirty complete unhealthy/healthy pairs
without pagination. Body event timestamps establish monitor intervals;
they do not establish packet outage duration or deployment attribution.
The exact pool API returned HTTP 403 with an authentication error. Current
health, monitor targets, and protocols remain unverified. The temporary
credential file was deleted without displaying its contents.

PR #143 remains unmerged. Compatible release verification, Configs pins,
shared testbed deployment, complete downstream acceptance, physical
forward/reverse transfer and production promotion remain required.

### Merge NPT edge authority

PR #143 merged as `c80877958125bcb11dfb49b3612c538720fb5018` on
September 30, 2026 at 19:33:16 UTC. The merged head is
`10798d795521246ccb29614bcbb36ee67a64fee4`. All seven branch-local
commits passed signature verification and raw signature inspection.

CI run `36763504811` passed every required check, ARM64, namespace,
firewall and both protocol lanes. All four cached-prefix cases passed.
The nonrequired vulnerability check reported the unchanged GoBGP
`GO-2026-4736` advisory with no published fixed version.

The independent systemd battery passed all five required cases and four
interface naming subcases in 25.890 seconds with zero skips. It used the
explicit candidate executable from signed source `3398e61`; the final
commit changed only the acquisition plan. The final authority interface
example includes cached-prefix retention without readiness publication.

The final review has zero actionable findings. The literal empty-WAN
cleanup conclusion uses source inspection; the public withdrawal control
retains provider metadata and does not prove a literal empty-WAN input.
The stale-map fixture uses real journal, kernel address and DAD operations
before actual daemon startup. No mocked dependency replaces that path.

The clean primary checkout now matches the merged commit. Release
publication and verification, paired Configs pins and journal configuration,
shared testbed deployment, downstream balancing and restart acceptance,
physical forward/reverse transfer and production promotion remain pending.
Current Cloudflare health verification requires working read access; the
pool API returned HTTP 403 and Chrome requires sign-in.

### Pair the published release and deployment configuration

Release `202609301934-8c-c808779` passed its terminal release workflow.
All four downloaded archives match published checksums and asset digests.
Each archive's attestation verifies the exact merged source and package
workflow. Actual ARM64 execution reports clean `c808779`; its SHA256 is
`edb21db2b66294a8770c48012ddf39045af009986f3a006bf3948d79b73607df`.
AMD64 execution failed with an executable format error on the ARM64 host;
its archive provenance and embedded full revision passed verification.

Configs PR #572 is open at signed
`adbcd95225998a6cdb4a600fbc43df357ec80e7a`. It pairs the address journal
with the compatible release in both environments. Ownership and recovery
flags are unchanged. Both actual environment renders passed, and the
published ARM64 executable accepted their network and firewall inputs.
Lint and data CI passed. Independent review and full rendered startup remain
pending. The primary Configs checkout remains on merged `3f28f3bf`.

Live client preflight passed for both downstream clients and simulator
identities. Calibration sent twenty source-bound IPv4 requests and produced
five capture artifacts. Capture shutdown failed on an unavailable transient
unit; process cleanup then raised a permission error. This failed run does
not establish balancing. Diagnosis and exact-owned cleanup are required
before a new calibration run. No network configuration or deployment changed.

The merged authority worktree and exact local/remote branch were removed
after containment verification. Ten ignored build files were copied and
hash-verified in external evidence before removal. Shared Docker fixtures
and external acceptance artifacts remain available.

### Merge the compatible deployment pair

Configs PR #572 merged as `61feeebfc7870e597a35619616e64d9e3b67bdc2`
at 19:51:32 UTC. Required checks passed, no review threads remained open,
and independent review found zero actionable defects at signed `adbcd952`.
The clean primary Configs checkout matches the merge.

Both unchanged runtime renders initialized addresses and completed initial
reconciliation with the published executable. Both processes remained alive
and served IPv4 and IPv6 readiness as false. The controls supplied no
physical provider identity or delegation. The absent cold journal is correct
when no edge is reserved. This proves initialization compatibility without
claiming forwarding or edge creation. Private startup containers were removed.

Further inspection of the failed calibration found successful initial
simulator stop commands followed by timed-out SSH observers. Host journals
showed both container-attached capture services exceeded their three-second
stop deadline and received SIGKILL. The unloaded-unit errors occurred during
the subsequent cleanup retry. The local process-group permission failure
remains separately recorded. Diagnosis, corrected capture lifecycle and a
fresh calibration remain required before complete downstream acceptance.
Shared testbed deployment and production promotion have not begun.

### Record the production capability failure

Physical attempt 4 used the unchanged published c808779 ARM64 executable
and production service capabilities. Six phases completed source-bound IPv4
and IPv6 requests. Both ten-second DHCP release windows recorded zero
packets. All fifteen completed captures recorded zero kernel drops.

Reverse edge cleanup failed. Attached BPF program inspection returned
`get program by id: operation not permitted` on Debian
`6.12.107+deb13-cloud-arm64`. The obsolete provider A address receipt
remained. Networkd reacquisition and the cold-baseline reboot sequence did
not execute. Both private guests shut down cleanly. Deployment requires a
verified service-permission repair and a fresh complete physical run.

Configs PR #573 adds the required journal to the real acceptance fixture at
signed `96469501ae8926d6a114d2058afe3b40cf865af9`. Independent validation
cleared the missing-journal error but failed two of three public cases.
The published daemon rejected the veth provider's permanent MAC identity
before readiness. Assertions and deadlines remain unchanged. The fixture
identity requires correction before merge.

Process cleanup candidate `affcdfec87892af7777226209cecc7660963344d`
reaps exited leaders before group signals. Real TERM and KILL controls pass
on macOS and Linux. Independent review must verify descendant cleanup when
the leader exits first. The candidate remains unpublished. Guest-owned
capture services require a separate implementation after cleanup review.
Shared testbed and production deployment remain pending.

### Repair the verified acceptance defects

MWAN-532 is In Progress under MWAN-305. Exact Linux 6.12.107 source
requires `CAP_SYS_ADMIN` for BPF program and map lookup by ID. Current
descriptors cannot inspect surviving prior-process attachments. The repair
adds this capability to the WAN service's ambient and bounding sets and
retains strict inspection. The capability permits operations beyond BPF
inspection. Acceptance requires the actual production unit, an unchanged-unit
EPERM control, restart, obsolete edge removal and surviving provider packets.

Independent real-fork review confirmed that `affcdfec` returns after reaping
the leader while its TERM-resistant descendant remains alive in the original
group. Exact-owned reviewer cleanup removed the descendant. The correction
must preserve original group IDs through cleanup and reap each direct child
once. The candidate remains unpublished pending real macOS/Linux validation.

Actual veth inspection found no permanent MAC attribute. Configs PR #573
will use matching real tagged VLAN endpoints and declared VLAN identity
instead of claiming a physical MAC identity. Its packet assertions and
deadlines remain unchanged. The public released-daemon battery remains the
acceptance requirement. These repairs do not authorize an unmerged deploy.

### Publish the process cleanup correction

Configs PR #574 contains signed `595e0dc8163cf5134a29b929e50f23c94f699f03`.
The old permission control and the first correction's descendant control
both fail against their original sources. The final three real-child cases
pass on macOS and Linux ARM64 with zero skips. Root inspection and the
independent macOS rerun passed. Permission errors remain strict. The private
Linux container was removed. The PR changes process cleanup and its public
regressions only; capture placement and Engine behavior are unchanged.

The active ruleset requires signed commits, resolved threads, secret checks,
lint and data tests. Lint and secret checks passed; data CI remains active.
No review threads were open when exact-head auto-merge was enabled.
Guest capture implementation must follow the actual merge. PR #573's real
VLAN fixture also requires a declared parent and hand-authored networkd
intent because the fixture creates the links without running networkd.
Its full published-daemon acceptance remains incomplete.

### Merge the acceptance process correction

Configs PR #574 merged as `b30f1564a610a459f88ff9e4610ef68fbf98a24a`
at 20:40:08 UTC. All required checks passed, and no review threads remained
open. The clean primary Configs checkout matches the merge. Main delivers
direct-child reaping and original process-group termination at
`lib/mwan_acceptance/processes.rb:85`, with the three real-child regressions.
The merged worktree and local branch were removed after exact tree equality
and clean-state verification. The remote branch was already absent. Only
regenerable CPython inventory bytecode was removed; external evidence remains.

PR #573's next supported fixture uses the existing hand-authored legacy NPT
contract: original veth topology, no renderable link or explicit DHCP intent,
translation configuration and the required journal. Exact public loader and
mapped-fixture evidence established this contract. Earlier invalid VLAN
contracts remain preserved. Packet acceptance is still pending. The capture
repair uses a new worktree from the merged Configs base. No shared testbed
or production deployment occurred.

### Verify actual guest capture shutdown

Frozen capture source SHA256
`f7290abd6034b8682969f2e933b09c99a79ce53703f15aa6c05644d7572102b8`
passed twenty source-bound IPv4 HTTP requests from client 225. TCP sequence
attribution passed across actual transit, provider and simulator captures.
All five observers recorded active unit, PID and tcpdump executable identity,
successful stop and runner completion, zero kernel drops and successful
exact-PID absence commands. Root verified every PCAP hash and receipt.
The control lasted 99.04 seconds and reported no cleanup errors.

The two Proxmox simulator services executed tcpdump inside their guests;
their attach and SSH streams closed successfully. This control establishes
capture lifecycle behavior only. It does not establish calibration, configured
weight distribution, IPv6 acceptance or a deployment. Actual SIGKILL failure
rejection and the integrated Linux suite remain required before publication.

The supported legacy packet fixture passed loader, health and routing checks.
Its private runner lacks Bird and the required updater unit. The correction
must provision the real dependency and run the unchanged updater service
inside the private gateway namespace. Earlier fixture and prerequisite
failures remain preserved. No product enforcement or packet deadline changed.

### Verify capture failure rejection and combine acceptance repairs

Capture commit `36c9ec6e2f4d0be99c07acaf3f89e6a23fa0239b` passed the actual
Proxmox positive control. SIGKILL of its uniquely owned simulator observer
failed acceptance despite twenty successful HTTP requests. The failure receipt
preserved the missing-unit stop error and the separate cleanup error. Strict
commands confirmed absence of all five capture PIDs and local observers.

The final legacy fixture uses the real updater inside its private gateway
namespace. Published `c808779` passed journal authorization, updater completion,
BGP route installation, preflight and calibration. Its three-case battery
reported two capture deadline failures and zero skips. Independent review found
no actionable source defect at fixture SHA256
`938f8bf404b27eac20b19dc8199680a265e190ea5b93bdc503036dffad45d7d1`.

Graphite restacked the fixture onto merged Configs `b30f1564`, then moved the
capture correction above it. The signed commits are `a66a46b8`, `174ad62b`
and `3221aacf`. Every signature and raw `gpgsig` header passed verification.
Recoverable pre-stack refs preserve both original branches. Submit preview
updates PR #573 and creates only the capture PR. The combined public battery
is pending at frozen tip `3221aacfcf4c4351e3ad9a9268127e4e78874220`.

MWAN-532 signed commit `d4d60df811fb1506779dd4a404724aebdf36531b`
adds the kernel-required capability to the production service. The unchanged
unit retained actual prior A program/map references and failed withdrawal with
EPERM. The candidate passed five real systemd cases, removed A's address and
receipt, and preserved B's translated request and reply. Independent review
and its separate real run remain pending. No shared testbed or production
deployment occurred for these corrections.

### Review the service repair and detect interrupted capture startup

MWAN PR #144 publishes signed `d4d60df8`. Independent review found no
actionable defect. Its separate real systemd run passed all five cases with
zero skips in 27.936 seconds. Actual A references survived SIGKILL; withdrawal
removed A's address and receipt while B request and reply passed. Install
output matched the production service bytes. Required CI remains pending.

Graphite published Configs PR #573 below PR #575. Frozen `3221aacf` ran
three public cases with two failures and zero skips in 93.46 seconds.
Interruption preceded the second observer's verified process receipt.
Both capture services stopped, but the missing receipt failed cleanup.
The other failure exceeded the five-second SSH deadline despite HTTP 200
and a 0.000394-second curl result. Its cause remains unconfirmed.
The private container was removed, and source hashes remained unchanged.

The capture correction requires actual process verification during interrupted
startup cleanup before service stop, followed by strict process absence.
The public interrupt regression remains unchanged. Fresh integrated acceptance
and exact failure rejection controls remain required. MWAN-522 and MWAN-532
remain In Progress. Shared testbed and production deployment remain pending.

### Merge the production permission repair

MWAN PR #144 merged as `c25720051c7efd71aa1fa4eb5e4c1a6c7babd559`
at 21:15:18 UTC. Every active required check passed; no review threads
remained open. The clean primary MWAN checkout matches the merge.
The unchanged nonrequired GoBGP advisory remains recorded. The additional
protocol job was canceled after merge; its cancellation establishes no
completed full-suite result. Merged-source CI and release verification remain
pending. No deployment occurred.

Capture source `a15cd293` passed the public interrupt boundary with status 130
and no cleanup errors. The complete battery still reported two failures and
zero skips in 109.43 seconds. Source-bound HTTP responses returned 200, but
their SSH processes exceeded the five-second command deadline. Actual exit
statuses and timestamps remain preserved; the cause remains unconfirmed.
A duplicate private run was interrupted and its owned container removed.
Its concurrency window remains part of the evidence. The capture correction
also requires stop/reap attempts after failed process verification. The stack
remains unmerged pending those controls and complete acceptance.

### Verify the published service repair

Release `202609302115-8d-c257200` completed workflow `36778234531`.
All four archive checksums, API digests and exact-source attestations passed.
Actual ARM64 execution reports clean `c257200`. Its executable SHA256 is
`600507ae01dc834bb4e6357e243c9a00196ca2bfcf358f4d44ef0203afacaf87`.
Published installation emits the exact production service with the required
capability in both sets. The service SHA256 is
`8b2a7ef93ffb372188d4ccd29156aa6e5ec21af91e275c9988b23c748abc41fa`.

The merged MWAN-532 worktree and refs were removed after proof. Eleven ignored
artifacts were preserved externally. A native Configs worktree at
`/Users/agoodkind/.codex/worktrees/mwan-532-release-pin/configs` prepares
the independent release companion. Actual physical and shared acceptance
remain pending. Capture cleanup and SSH completion diagnosis remain separate.

### Publish verified capture cleanup and merge the release companion

The public capture identity-failure control kept the actual running tcpdump
inode while renaming its executable inside a private container. Acceptance
rejected the identity and preserved that failure. Cleanup still stopped the
owned service, reaped its runner, recorded zero drops and verified exact PID
absence. The original executable path was restored and the container removed.
Source `613797a5` preserves failed verification and caches actual runner status
to prevent a second reap. Root inspected the diff and runtime report.

Graphite published the correction, then restacked each branch from its owning
worktree after Configs PR #576 merged as
`d749838112eda0703685b88caefb28e5d56c4c9f`. All four rewritten signatures
and raw `gpgsig` headers passed. PR #573 is below ready PR #575 at signed tip
`80338d2d6ce5a08f6d881dfcdad5cec2c5a82236`. Both remain unmerged pending
complete public acceptance. Both release companion renders and exact published
startup checks passed without physical identity or delegation inputs.

Merged-source MWAN CI `36778234620` completed. Firewall/protocol, namespace,
ARM64 and required Go checks passed. The unchanged Govulncheck advisory failed
separately. The physical runner will use the verified published `c257200`.

The bounded HTTP diagnostic verified stdin EOF and reproduced the timeout
with explicit SSH `-n`. Full TCP shutdown completed before curl waited
4.999 seconds on a UDP socket. JSON output followed that wait; cleanup had
closed SSH and its write returned EPIPE. UDP destination and query require
measurement before assigning a cause. Packet and command deadlines remain
unchanged. Shared testbed and production deployment remain pending.

### Identify the private runner's hostname lookup delay

The full-start trace captured A and AAAA queries for the actual private
container hostname after TCP shutdown. The hosts file omitted that hostname.
Curl waited 5.002387 seconds for the inherited DNS server, then wrote JSON
after SSH cleanup had closed its output. The source function initiating
resolution remains unassigned. A supported container host mapping is under
validation with unchanged request arguments and deadlines. Fresh full public
acceptance will use signed `80338d2d` and the verified published `c257200`.

Configs PR #576's native worktree archive was refused because the app reports
a pinned task or workspace. The branch deletion attempt was blocked while
that checkout remained attached. The worktree and local ref remain intact;
the remote ref is absent. Inventory bytecode was preserved externally.
Physical preparation continues from the corrected frozen attempt4 helpers.
Restart and cold-baseline phases require explicit acceptance before any
complete-transfer claim. No shared deployment occurred.

### Accept the exact published public battery

Signed `80338d2d` passed all three public cases with zero failures and zero
skips in 106.84 seconds against the read-only published `c257200` executable.
Pre/post source and binary hashes matched. Both families, mapping replies,
actual restart, compressed history, interruption, stopped-observer rejection
and strict history deadline controls passed. The private runner was removed.
The supported hostname mapping eliminated the measured post-transfer DNS delay
without modifying request arguments or deadlines. This proves isolated public
acceptance, not physical transfer, shared balancing or backup gateway failover.

The PR #573 updater-path finding was disproved: its absent-path guard precedes
the cleanup ownership assignment, and teardown requires that assignment.
The evidence reply and thread resolution passed. Every required check was green
when Graphite merge preview succeeded. Graphite then started merging PR #573
and PR #575; staging checks remain pending. The physical attempt5 runs the
bounded forward/reverse and restart sequence. Its separate contract review
requires executed physical identity, protocol identity, scoped journal lifecycle,
concurrent provider continuity and cold-baseline assertions before complete
physical acceptance. The verified historical `2587f14` executable supplies the
pre-authority writer baseline, subject to its actual loader/runtime validation.

### Verify acceptance stack merge and physical transfer

Graphite merged Configs PR #573 as `74bee79ecc388f91f4a8a155f42109845d6160ef`
at 21:44:30 UTC and PR #575 as `566d58350f550ed6ec1474fb41937644a20b0a57`
at 21:47:30 UTC on September 30. Every required check passed. The clean
Configs main checkout fast-forwarded to the latter merge.

Physical attempt5 passed eight source-bound IPv4/IPv6 phases, actual MWAN
service restart and networkd reacquisition. All 21 captures reported zero
drops and equal captured/decoded counts. Both ten-second release windows
contained zero target DHCP packets. Provider A's edge and scoped receipt
were removed; provider B's receipt remained unchanged. Protocol identities
matched baseline, acquisition, restart and reacquisition captures.
The guests shut down gracefully. Cold recovery, provider B packets during
the silence windows and exact effective guest capability readback remain
required. No shared testbed or production deployment occurred.

Tack MWAN-522 and MWAN-532 received current evidence and actual In Progress
state updates. Raw MWAN-522 properties exceeded the tool's 32 KB response
limit; the existing description was preserved and new evidence was posted
as a separate comment. The ergonomic issue read confirmed In Progress.

### Verify calibration, package recovery and owner continuity

The corrected shared baseline used provider-facing eth0 captures and 40 fresh
requests per family. Both families selected AT&T 21 times and Webpass 19 times.
The predetermined acceptance bounds were 13 through 27. All 20 captures
reported zero drops and strict process cleanup. This baseline used the older
installed daemon; it does not prove the published c257200 release on testbed.

Configs PR #577 merged as 1c601538d68938ce5b540ba59ec83fa57b39b4a8. The
package-only testbed deployment failed when CT904's 128 MiB memory cgroup
killed apt-get. The unchanged merged play then completed with exit 0 and
verified curl on CT900 through CT904. The original failure remains evidence.
The durable correction increases only CT904 memory to 256 MiB; the provider
will reboot that container during the update. No memory apply occurred.

Downstream guests 225 and 226 each received all 180 IPv4 and 180 IPv6 probes
during the three-minute primary-route baseline. Route samples selected the
primary throughout. The primary was not stopped; backup failover remains
untested.

Physical attempt6 proved same-boot foreign-address rejection and cold kernel
absence before scoped recreation. Its final capture reported one kernel drop;
the full attempt failed. Attempt8 passed all six dual-stack packet phases but
failed the configured-edge continuity assertion. Netlink recorded deletion of
2001:db8:30::1/128 at 22:18:49.198608 UTC and subsequent DAD recreation.
The acquisition owner changed while configured translation and WAN membership
remained unchanged. Startup networkd reload preceded replacement NPT runtime
initialization. Networkd writer attribution follows source and timing; the
fixture did not capture a writer syscall. MWAN-533 records this defect under
MWAN-305 and is In Progress. Independent repair review remains pending.

Both physical guests powered off normally. Shared daemon deployment and
production promotion remain pending. Tack received current package, baseline
and defect evidence without overwriting truncated descriptions.

### Apply the simulator correction and observe BGP handover

Configs PR #578 merged as 7e58ae2de9c36bbfa5ebe459e00dc9022db6f723.
The saved merged plan changed only CT904 memory from 128 to 256 MiB.
Actual configuration retains 512 MiB swap. The running memory cgroup reports
268435456 bytes. A refreshed targeted plan reports no changes. The merged
package-only play verified curl on all five simulators and reported no failures.
Both downstream guests received all 180 probes per family across the update.

The primary testbed BGP service was stopped and restored under continuous
downstream observation. OPNsense selected backup IPv4 gateway 10.240.240.4
and IPv6 gateway 3d06:bad:b01:201::4, then restored both primary gateways.
Guest 225 observed backup selections by 22:37:52.525802 and 22:37:52.862851
UTC. Guest 226 observed them by 22:37:51.193602 and 22:37:52.804819 UTC.
Both guests received all 180 probes per family. The largest observed reply
interval was 1.062439 seconds. The primary BGP and WAN services are active.
This proves service-stop route handover at one-second packet sampling;
full gateway reboot acceptance remains separate. No exact withdrawal latency
is assigned because the stop command lacks an independent timestamp receipt.

Configs PR #579 merged as 46297164259fc75f7c751d356258f82b2946ef05.
Independent exact-head review found zero actionable defects. The disproven
Graphite finding received an evidence reply and resolution. Required checks
passed. The read-only acceptance tag audits packages and measures actual
source-bound mapping responses through the supported Ansible boundary.
Its first live invocation is pending. Packet captures remain a separate gate.

The MWAN-533 repair uses static preservation only on networkd NPT connections.
Systemd also preserves other foreign static addresses and routes on those
connections. MWAN scoped withdrawal and cold foreign-address rejection remain
required. Repository runtime regression uses a real VLAN provider; the physical
owner transition requires the existing virtio guest fixture.

### Verify NPT regression and diagnose the mapping fixture

MWAN PR #145 contains signed candidate 56e87533fd87c60fdbee087a535da561b3a709af.
The real networkd/VLAN daemon regression passed in 7.766 seconds with exit 0.
Published c257200 failed the same assertion in 9.352 seconds with exit 1.
Its address observer recorded deletion and tentative/DAD recreation. Complete
terminal logs and actual exit files are preserved. Independent review verified
the retained logs and found zero actionable source defects. It did not execute
an independent rerun. Existing package tests and blocking make checks passed.

The first physical candidate required an absent shared libsysrepo library and
failed before behavior. A static cgo rebuild preserves the release's schema
binding and starts in Debian. Its SHA256 is
375c52532ea031d3cb24d6bb9ca0fbeedc1eed186ab5dc6a39151d0cb2d9dcd6.
Its build metadata reports unknown/dirty because the container cannot resolve
the linked host Git directory. The host source remains clean at signed
56e87533. This is an unpublished candidate, not a verified release.
The next physical attempt failed its baseline because the inherited ISP
evidence directory prevented fixture initialization. Neither failed attempt
proves owner transfer. Fresh private directories and persistent archive output
correct those prerequisites; the original packet and address assertions remain.

The merged mapping acceptance play verified all five package audits and both
provider-facing routes. Both HTTP requests timed out. A coordinated repeat
captured eight SYN retransmissions per provider before DNAT and identical
sequences addressed to 10.240.240.2:80 after DNAT. OPNsense PF rule 25 blocked
the exact tuples on vtnet1. All three captures reported zero drops, strict
process reaping and absent capture PIDs. No translation defect was demonstrated.
The admin HTTP endpoint also redirects to HTTPS. A deliberately permitted
stable HTTP endpoint remains required. No GUI exposure or PF change occurred.

### Preserve the configured edge across physical owner changes

The isolated physical test passed with exit 0 using signed source 56e87533.
All 14 source-bound IPv4 and IPv6 replies passed the original deadlines.
The configured edge 2001:db8:30::1/128 and exact scoped receipt remained
unchanged across networkd release, MWAN acquisition, external release, and
networkd return. The continuous address observer recorded no event for that
edge and no observation error. Generated networkd configuration contained
KeepConfiguration=static without fixture insertion.

Seven strict captures recorded 664 packets. Captured, decoded, and received
counts matched; every kernel drop counter was zero. The separate ring control
recorded 40 complete packets with zero drops. Both QEMU guests powered off
normally with exit 0. The executable matched the previously recorded static
ARM64 hash. This proves the focused physical transition, not release
publication, cold boot, or a deployment outage bound.

PR #145 now contains signed head ab7005cbc1ab5b233a2e06e73e22dbfc135e284a.
The follow-up removes duplicate helper logging and constructs the unchanged
interface-qualified validation error before returning it. Existing package
tests and every blocking make check gate passed. The typed-map review finding
received an evidence reply and resolution. The valid logging finding received
a verified fix reply and resolution. Required CI checks and independent
follow-up review remain pending. No shared daemon or production deployment
occurred. MWAN-533 remains In Progress pending release and live acceptance.

The mapping fixture still requires a deliberately permitted HTTP endpoint.
The expected /cf_check service on port 1406 is absent from testbed OPNsense.
Its configuration path requires inspection before a focused repair. The
admin HTTP firewall restriction remains active.

### Publish the merged NPT preservation repair

PR #145 merged as 93d3c3579334fc618427560e58aca919247bc2dc after all ten
required checks passed. Independent final-head review found zero actionable
defects. The primary MWAN checkout is clean on main at the merged revision.
Both candidate refs and the exact feature worktree were removed after trunk
behavior and ancestry verification. All 12 ignored files were copied and
hash-verified in retained cleanup recovery storage.

Release 202609302311-8e-93d3c35 was published by successful workflow
36789759627. All four downloaded archives passed published checksums,
release API digests, and attestations binding their bytes to the exact merged
source. Published ARM64 execution reports commit 93d3c35, a clean build,
and libsysrepo 7.34.6. Its SHA256 is
69a2ed1fe30e135d6382ff7ad9c2ef8608686195d72a4ca7a2fea1e6bf4e1b8e.
The actual installer emits the reviewed production WAN unit byte for byte.

The focused Configs release pin and dedicated mapping endpoint changes remain
under implementation. Published protocol acceptance is running. Shared daemon
deployment and production promotion remain pending. MWAN-533 remains
In Progress. Actual Tack comments distinguish publication from live acceptance.

### Record published protocol and mapping fixture failures

Configs PR #580 merged as 37e66118d275244c6c76e3d7a31d3a1f17f72ac1.
Its production and testbed release pins select the verified 93d3c35 release.
Configs PR #581 merged as d2f33ce9d5d385271a479f55c5156629021e63d6.
It configures a separate testbed HTTP mapping endpoint on port 1406 and
requires HTTP 200 with the exact expected body. Both PRs passed all required
checks. PR #581 passed independent final-head review.

Published protocol acceptance passed all 22 namespace cases. Its original
systemd lane passed four cases and failed the ordered startup assertion
because networkd was active. Fresh isolated and resolver-then-startup runs
passed unchanged. Both subsequent full systemd lanes passed startup but
failed the NPT UDP6 request after the route metric changed from 500 to 501
and the daemon restarted. The configured edge and routing readiness state
were present at failure. Packet loss location remains unproven. The complete
diagnostic report and journals are retained in the published release evidence
directory. Every owned diagnostic container was removed. No source assertion
or deadline changed.

The merged mapping fixture deploy ran from clean Configs main at d2f33ce9
with the isp-acceptance-fixture tag. The play failed when sysrc returned
status 1 with empty output for lighttpd_instances. The play reported seven
successful tasks, zero changes, and one failure. Its log is
/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/deploy-opnsense-20260930T233414Z.log.
Both downstream observers terminated with status 1; their artifacts require
inspection before attributing that status to a network interruption.

MWAN-305, MWAN-522, and MWAN-533 remain In Progress. Published protocol
acceptance and the mapping fixture require correction and fresh proof before
the shared daemon deploy. Production promotion has not occurred.

### Restore route observation and correct mapping activation

The first observers received all 240 replies in each family from each client.
Their route queries failed because Proxmox received no QEMU guest-agent
response. Read-only inspection found VM201 and its guest agent running with
the correct port. The transport failure cause remains unknown. Direct SSH
route reads succeeded. The retained recorder now accepts an explicit route
host and records those commands without changing the guest agent or VM.

Configs PR #582 merged as ed881f0e394516c504542f82293f3a9370a5502e after
all required checks and Graphite AI review passed. It accepts only status 1
with empty output for optional sysrc settings. The subsequent merged fixture
deploy passed those reads, configuration validation, and reconnection. Native
listener startup failed because www could not create its log file in the
root-owned /var/log directory. The play reported ok=18, changed=5, failed=1.
PHP validation, filter reconciliation, and final endpoint assertions did not
execute. A dedicated writable log directory requires a focused correction.

Both corrected 240-second observers exited zero. Each client received 240
IPv4 and 240 IPv6 replies, with zero missing sequences. Each client recorded
128 successful route samples per family and zero failed samples. Every
sample selected the primary next hop. The largest packet reply gap was
1.032402 seconds. This proves continuity during the failed fixture attempt;
it does not prove failover, load balancing, or the pending daemon deployment.

Read-only inspection of the installed OPNsense Config API found that lock()
reloads configuration under an exclusive lock. The updater's immediate
forceReload() closes the locked file handle. Remove that initial reload in
a separate focused fix; retain the post-unlock verification reload.

Later complete systemd lanes passed unchanged, but the repeated NPT failure
cause remains unknown. Gateway tcpdump cannot establish UDP visibility
through the TC redirect path. Endpoint namespace captures require verified
visibility and complete export before assigning a packet loss cause.

### Verify native fixture dependencies and startup timing

Configs PR #583 merged as ddd5ac159d652037f6e707a8dd60b6afa910f45c.
It creates a dedicated www-owned log directory. PR #584 merged as
baafcee99b50604d9be656d16e7f167144b259a0 after a signed rebase to that
base. Both passed required checks and Graphite AI review with resolved
threads. The following merged fixture deploy started the listener and
passed actual guest PHP syntax validation. Rule reconciliation failed on
the missing shell_safe function before either rule was saved. Native
config.inc requires util.inc for revision creation. PR #585 adds that
include and guards error-output parsing; its final review remains pending.
No filter reload or mapped HTTP acceptance occurred in this attempt.
The PHP helper configures only testbed firewall rules. The endpoint serves
a static file; production MWAN does not depend on the helper.

The longer continuation observers lost all four ping SSH processes at
23:51:38Z with status 255 and empty stderr. They recorded 187 replies per
family from client225 and 186 from client226, but no final counts. Route
sampling continued successfully. Their cleanup occurred later. The cause
remains unknown; these incomplete streams do not prove packet loss or
zero loss. Fresh observers use independent SSH connections and preserve
the incomplete artifacts.

Exact endpoint captures reproduced the NPT request during the restarted
daemon's initialization, before its first reconciliation. The provider
emitted an Ethernet UDP frame; the client received none. Both endpoint
counts matched exactly and kernel drops were zero. The fixture accepted
previous-process TC programs. The protective-firewall READY contract
deliberately precedes forwarding convergence. A focused fixture correction
will require replacement programs and the requested route/policy outcome
before the unchanged packet assertion. Startup loss remains distinct from
steady-state translation failure.

The complete shared-plan-93d3c35.json passed the actual public Plan
constructor. Its expected AMD64 binary hash is
f8b1ce2a5379c9a484e3539ba7fdd46ee56c103b8bb0fb63d0fdad78bac8c6bc,
computed from the verified release archive. Existing calibration counts,
bounds, capture hashes, and identities remain unchanged. The new history
observation window is 90 seconds, not an outage allowance. Engine execution
and fresh installed-identity verification remain pending. The plan does
not authorize bypassing the rejected SSH pct transport.

### Verify the applied mapping rules and current NPT programs

Configs PR #585 merged as e1020d68104c56618fe94132b0a2bb58c258111d.
The subsequent fixture play exited one with ok=26, changed=4,
unreachable=0, and failed=1. The native updater, filter reload, reconnection,
and isolated listener assertions passed. Actual PF readback contained both
exact source-restricted rules with destination (vtnet1). The assertion
incorrectly required (vtnet1:1) and used incorrect escaping. HTTP and final
GUI assertions did not execute. A focused assertion repair remains pending.
The [actual play log](/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/deploy-opnsense-20261001T001225Z.log)
retains the failed assertions and completed tasks. This result does not
establish complete fixture or daemon deployment acceptance.

MWAN PR #146 changes only the existing NPT readiness fixture. Independent
review of exact commit 857d69394647bc1af6304ef8f481ae7cb2981d42 found no
actionable defect in the generation, route, or unchanged packet assertions.
The retained published-binary systemd lane passed all five required cases
with zero skips in 31.189 seconds. A subsequent review identified a valid
potential inspection race if a kernel program disappears during replacement.
That concern remains under investigation; merge acceptance remains pending.
The [independent review](/Users/agoodkind/.local/state/mwan305/20261001-mwan533-release-93d3c35/startup-diagnostic/independent-pr146-review.md)
and [public verification report](/Users/agoodkind/.local/state/mwan305/20261001-mwan533-release-93d3c35/startup-diagnostic/npt-fixture-verification.md)
retain the exact source and runtime evidence. The corrected readiness wait
does not establish uninterrupted forwarding during startup. The original
captured startup loss remains a separate result.

The final 900-second fixture observers received 899 of 899 replies in each
family from each client, with zero missing sequences. Each client recorded
343 successful route samples per family; every successful sample selected
the primary next hop. Client225 exited zero with no failed route samples.
Client226 exited one after a single IPv6 route observation failed. That
SSH command returned 255 at 00:05:41.745819Z with connection-reset stderr
from suburban, before the fixture deploy started at 00:12:25Z. It did not
report a route rejection or packet outage. The [client225 report](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/mapping-fixture-final-client225/report.json)
and [client226 report](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/mapping-fixture-final-client226/report.json)
retain the packet and route results. The separate 600-second observers
remain active; their terminal results are not included here.

### Verify the unchanged fixture and preserve failed simulator reporting

Configs PR #586 merged as c0859644ada4b2f95cc4592d5c94b9eecbc88d78.
Independent review found no actionable issue in its exact PF assertion.
The merged fixture play passed with ok=29, changed=0, unreachable=0,
and failed=0. Both PF rules, local HTTP 200, exact response bytes, and
original GUI process/configuration assertions passed. Listener activation
and filter reload tasks skipped. The [fixture log](/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/deploy-opnsense-20261001T002359Z.log)
retains this unchanged run.

The following merged simulator acceptance play received HTTP 200 and the
expected body from both source-selected mapping requests. Its report
template failed while splitting the response delimiter. Final HTTP status
and body assertions did not execute. The [failed simulator log](/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/deploy-testbed-20261001T002553Z.log)
retains both real responses. A single YAML-decoded delimiter correction
remains under validation. This result does not establish packet attribution.

Both 300-second observers received 300 of 300 replies per family and
recorded 115 successful primary route samples per family, with no failed
samples. The [client225 result](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/mapping-c085-client225/report.json)
and [client226 result](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/mapping-c085-client226/report.json)
retain the completed observations.

The earlier 600-second observers recorded different results. Client225
received 600 of 600 replies per family and failed one route observation
after an SSH key exchange reset from suburban. Client226 received 599 of
600 replies per family, with sequence 189 missing around 00:15:20Z,
about 70 seconds after the fixture play ended. Its observer exited zero
because the recorder reports missing sequences without rejecting them.
The [observer review](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/mapping-native-observer-review.md)
retains the command failures and packet counts. The loss cause and backup
selection remain unproven.

PR #146 corrected the vanished-program inspection race at signed commit
29579c1d2314b04cb12e3309adb2c7aa13513df5. Independent source review passed.
Its complete published-binary aggregate passed all 22 namespace cases
and four of five systemd cases, with zero skips. The NPT case passed its
packet check, then failed a receipt assertion during withdrawal. Production
retirement removes and verifies the address before persisting receipt
removal. The fixture now requires the exact expected receipt within its
existing readiness deadline. Final receipt and packet assertions remain.
Signed commit 7201aa87f6d076fa66cbac546aa03ffe784986cb passed the affected
five-case systemd lane and code checks; its complete aggregate remains
pending. Shared daemon deployment and production acceptance remain pending.

### Verify the complete mapping report and published protocol aggregate

Configs PR #587 merged as 5da630fb3341032b132f021f98e89552e496a1f0
at 00:39:06Z. Exact head 671624559f1b184263e8ee8e0cfc9b41dfe679f5
passed all three required checks, signature verification, and raw signature
inspection. Graphite AI review completed. The request for a constructed
stdout specification was answered with the actual public diagnostic and
testing rules, then resolved. The original expressions failed against the
real retained responses; the corrected expressions passed.

The fresh merged ISP tag passed with ok=14, changed=0, unreachable=0,
failed=0, and skipped=1. All five package audits passed. AT&T simulator 901
used source 10.240.205.1 and external destination 10.241.205.2. Webpass
simulator 900 used source 10.241.204.1 and external destination 10.241.204.2.
Both source-selected routes used eth0. Both actual requests returned zero,
HTTP 200, and response SHA256
70c035bfb6878b96ba12eb10ab3d93dcee5b333e7b7a10762747854788afbd42.
The final status and body assertions passed. The [fresh ISP log](/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/deploy-testbed-20261001T003926Z.log)
retains the actual requests and structured responses. Packet capture
verification remains separate; this play reports it as false.

MWAN PR #146 merged as cb88cab11ff273b909410586cf9c98275ee4e30d
at 00:39:50Z after required checks and final independent review of exact head
7201aa87f6d076fa66cbac546aa03ffe784986cb. The complete aggregate used
the unchanged published daemon and passed all 22 namespace cases and
five systemd cases with zero skips. Namespace execution took 324.451
seconds; systemd execution took 28.389 seconds. The [aggregate report](/Users/agoodkind/.local/state/mwan305/20261001-mwan533-release-93d3c35/startup-diagnostic/npt-receipt-aggregate/report.md)
retains the manifests, exact binary hash, and results. The fixture correction
requires current programs, routes, and persisted receipt retirement within
the original readiness deadline. The three-second packet assertion and
continuous address checks remain unchanged. Earlier startup loss and
receipt failure artifacts remain intact. This merge changes fixture source,
not production forwarding behavior.

MWAN-522 and MWAN-533 remain In Progress. Shared daemon deployment, the
complete shared battery, and cutover acceptance remain pending. Genuine
management SSH provisioning is under implementation in a separate Configs
worktree. That prerequisite has not installed keys or changed live guests.

### Verify SSH identities and cutover ordering

The testbed PHP updater configures the OPNsense mapping fixture through
OPNsense's native configuration API. The MWAN daemon has no PHP dependency.

Configs PR #588 remains open at signed head
9db86203b480486f5293bddb53be6f4c9e1280ca. Independent source review found
no actionable defects. Subsequent review identified incorrect change reporting
for management directory creation. That repair remains under implementation.
Guest provisioning, idempotency, and authenticated simulator SSH remain pending.

Fresh authenticated SSH confirmed gateway hostname mwan and machine ID
bdd916f95e3e44568e6a5d3096cf2dea. SSH through suburban confirmed client225
hostname mwan-client-a and machine ID 46393cb237bb436c84675dd426cb58d3,
and client226 hostname mwan-client-b and machine ID
9f56214dcf9c473fa4a5b9b3821829fb. Direct client IPv4 SSH returned
Network is unreachable; the SSH jump connection succeeded with existing host
verification. These identity checks do not establish downstream forwarding.

The owned render prerequisite must emit explicit delegation IAID, persistent
link and kernel policy journals, and optional static mapping delivery. The
actual published decoder rejects owned Webpass mappings without delivery.
The renderer must preserve explicit local or routed values without inferring
them. Live ownership and delivery configuration have not changed.

Source review confirms mixed ownership support with legacy AT&T active.
The supported deployment still lacks per-interface networkd release readback,
administrative exclusion from new selection, and unmanaged sentinel retention.
Startup reloads networkd before owned journal cleanup. Reverse transfer must
verify address cleanup and kernel policy restoration before networkd acquisition.
An owner change alone does not establish that ordering. No shared daemon
deployment, ownership transfer, or production deployment occurred during these
checks.

### Merge rendering and simulator SSH prerequisites

Configs PR #589 merged as fe822642ca180a9fb5ab546ce1069db10f5a558a
at 01:17:40Z. Signed head ad416651f6124bf9e96bc25cac17e7e10464ef3b
passed all three required checks, raw signature inspection, and independent
review. The actual published decoder accepted both unchanged environment
renders and the explicit owned Webpass sample. All five sample mappings
remain present. Sample DHCP identity does not establish live identity.
The [render report](/Users/agoodkind/.local/state/mwan305/20261001-owned-render-prerequisite/report.md)
retains inputs, hashes, and decoder results. Native archive protection retains
the merged worktree and local branch; its remote branch is deleted.

Configs PR #588 merged as 3d010b7a53825e3986a76c4d456d21ecd2d89029
at 01:23:52Z. Signed head 5c238f99f8ce3d7eba0b3f6cb73192e36e0238e2
passed required checks and Graphite review. Both branch commits passed raw
signature inspection and verification. All five review threads are resolved.
Actual public diagnostics proved loop return-code handling, strict permission
failures, directory creation and rerun reporting, and key reconciliation for
absent, matching, and mismatched file bytes. The [review report](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/management-review-triage.md)
separates these results from guest transport acceptance. The exact feature
branch and unregistered worktree are removed; validation evidence is retained.

The targeted OpenTofu plan refreshed all five simulator containers and reported
no changes. It also warned that three prior bridge/VLAN state objects include
an unsupported reload attribute. The [plan log](/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/tofu-plan-20261001T012445Z.log)
retains that scope and those warnings. No apply ran.

The first merged simulator management play failed on CT 900's directory
creation command with return code 129 and empty stdout/stderr. Its recap was
ok=20, changed=2, unreachable=0, failed=1, skipped=2. The [failed play log](/var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/deploy-testbed-20261001T012528Z.log)
retains the exact command and result. A subsequent read-only public diagnostic
confirmed CT 900 running and the requested directory present with mode 0755
and root ownership. GNU install is present. The queried container and kernel
journals contain no corresponding event. The cause of code 129 remains
unproven. The unchanged idempotent play is under retry; complete provisioning,
authenticated simulator SSH, and whole-play idempotency remain pending.

Administrative exclusion and read-only per-connection release verification
remain under implementation in separate MWAN worktrees. Forward and reverse
transfer must retain AT&T under networkd. Shared daemon deployment and
production acceptance remain pending.

### Diagnose simulator SSH reload and prepare repair

The second unchanged merged management play failed after CT 900's SSH reload.
Its recap was ok=35, changed=5, unreachable=0, failed=1, skipped=4.
The reload returned zero; the following active check returned code 3 and
failed. The guest journal records sshd receiving SIGHUP, reporting Cannot bind
any address, and exiting with status 255. The socket remained active, and
systemd owned port 22. The service unit prevents restart after status 255.
The [diagnosis](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/management-ssh-reload-failure.md)
retains actual unit, journal, configuration, listener, and command results.
These observations do not establish the cause of the earlier install exit 129.

Configs PR #590 configures service-only SSH on the testbed simulators.
Root review found an unquoted comma in the existing service properties
argument. That argument must remain one YAML string. The listener assertion
must verify port 22 and the actual sshd process. Both changes remain under
review. Complete guest provisioning, authenticated SSH, and unchanged-play
idempotency remain pending. No shared gateway or production deployment ran.

### Merge SSH repair and verify remaining process migration

Configs PR #590 merged as 0704bf40142be7c36e15ec02b0435e8a297ef0ff.
Required checks passed, both feature signatures verified, and all three review
threads were resolved. The exact branch and unregistered worktree are removed.
The primary checkout matched clean merged main before deployment.

The actual management play configured CT 900, exported its identity, and
authenticated SSH verified isp-webpass and its machine ID. The play then
failed on CT 901's reload with recap ok=72, changed=12, unreachable=0,
failed=1, skipped=10. CT 901's old socket-activated process remained active
after socket shutdown. Its HUP reload reported Cannot bind any address and
exited 255. Read-only observations confirmed the socket disabled/inactive,
the service failed, and no port 22 listener. The next repair must stop the
old process during transition before the existing fresh service startup.
The [process migration report](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/management-901-reload-failure.md)
retains terminal logs and actual identity evidence. All-five provisioning
and unchanged-play idempotency remain incomplete.

Both downstream guests received all 300 IPv4 and IPv6 probes during the
five-minute observation after play startup. Client 225 had one failed IPv6
route query; client 226 had no failed route queries. Successful queries
selected the primary. These results do not cover the complete play interval,
balancing, or a gateway deployment. No production deployment ran.

### Verify simulator process recovery and package failure

Configs PR #591 merged as c543a3017f6de4892dd7da11cfd663007dd80ead.
The signed feature commit passed verification, all required checks passed,
and the review thread was resolved. The exact branch and unregistered
worktree are removed. The management play ran from clean merged main.
It configured CTs 900, 901, and 902, then failed installing tcpdump in CT 903
with exit 137. Its recap was ok=116, changed=14, unreachable=0, failed=1,
skipped=24. CT 904 was not processed by this run.

The read-only diagnostic confirms the kernel killed apt-get in CT 903's
memory cgroup at its 128 MiB limit. The package reports installed, dpkg audit
returns no findings, and tcpdump reports version 4.99.5. These results do not
convert the failed play into successful provisioning. Correct the recurring
resource limit through merged OpenTofu configuration before resuming.
The [package diagnostic](</var/folders/jq/hwwlnpr56_vdb42ff743hy040000gn/T/configs-runs/management-903-package-readonly-20261001T022342Z.log>)
retains the actual configuration, kernel event, and package results.

The Linux acceptance plan passes the production plan validator. Authenticated
SSH verifies hostnames and machine IDs for VM 213, clients 225 and 226, and
simulators 900 and 901. Dedicated simulator host keys originate from the
hypervisor's exported identities. The
[identity preflight](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/shared-linux-identity-preflight/)
does not verify product hashes or execute the acceptance engine. All-five
simulator provisioning and unchanged-play idempotency remain incomplete.
No gateway upgrade, ownership transfer, or production deployment ran.

### Merge release verification and apply the Astound memory correction

MWAN PR #148 merged as 9577cbb99153c5061a82a0bd0e2902536c018838.
All ten required checks passed. All three feature commits passed signature
and raw-header verification, and all six review threads were resolved.
Independent review accepted the runtime changes at f4d77cb; the final
dff38a4 change corrected only a comment. Six real systemd cases passed with
zero skips. The CI firewall job was cancelled after merge and does not
establish full CI firewall acceptance. The advisory Govulncheck failure
reports the existing unchanged GoBGP vulnerability GO-2026-4736.
The release feature branch and worktree are removed; primary main matches
the merged revision. Shared release installation remains pending.

Configs PR #592 merged as cec8de0d9981b877582852e115535e3798b8aa33.
Its signed feature commit, all three required checks, and thread resolution
passed. The saved plan changed only CT 903's memory from 128 to 256 MiB.
OpenTofu applied one in-place update with zero additions or deletions.
A fresh scoped plan returned zero with no changes. Actual pct configuration
reports memory 256; package audit remains clean. The
[resource implementation report](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/ct903-memory-implementation.md)
records the source mapping and local validation.

The management play has resumed from clean merged Configs with both
downstream guest observers started before the play. Provisioning and the
observers remain active; their eventual terminal results must establish
completion separately. The renderer PR #593 is open and passed its real
omitted/true/false rendering and Linux loader regression. PR #147 selection
exclusion remains open while its final schema prose revision completes CI.
Ownership transfer and production promotion remain incomplete.

### Complete simulator provisioning and merge selection configuration

The merged management play completed with ok=178, changed=21,
unreachable=0, failed=0, skipped=39. Authenticated SSH independently verifies
the exported host key, hostname, and machine ID for all five simulators.
The unchanged management play is running from the same cec8de0d checkout;
repeat deployment acceptance remains pending its terminal result.

Both downstream guests received all 600 IPv4 and IPv6 probes in each of
the first two overlapping observation windows. Client 225's first window
had one IPv4 route-query failure at 02:41:39Z: SSH reported a connection
reset from suburban before key exchange. No probe reply was missing.
The second windows had no failed route queries. Successful route queries
selected the primary. These windows cover the completed provisioning play;
they do not establish balancing or acceptance of a gateway upgrade.
The [first client 225 report](/Users/agoodkind/.local/state/mwan305/20261001-npt-deploy-acceptance/management-cec8-client225/report.json)
retains the failed sample alongside packet counts. Additional observers
remain active during the repeat play.

MWAN PR #147 merged as f02554367bb40b0189dd0e7c28916a38f6f3342f.
All ten required checks passed, all review threads were resolved, and all
seven rewritten feature commits passed signature and raw-header verification.
The branch was rebased onto merged PR #148. Full ARM64 checks and tests
passed after integration. Root review accepted the original exclusion,
same-tier fallback repair, and mechanical selection-default normalization.
The real daemon regression verifies both address families and backup traffic.

Configs PR #593 merged as aaeffd4873177ccc701c0159338dd91477a88711.
All three required checks passed, all review threads were resolved, and all
three feature signatures verified. CI executes the real rendering assertions
without requiring a Linux binary. The selected Webpass entry must exist
before its permission is asserted. Both actual rendering and the integrated
Linux loader passed in 6.74 seconds. Published-release validation remains
pending. The running play's checkout has not been updated during execution.

Transfer review found that the legacy WAN routing address writer still
installs mapped IPv4 addresses for external owners. A focused runtime repair
is under implementation. It must preserve networkd legacy writes, MWAN's
journaled address consumer, and separate NPT authority. The address manager
already rejects retained unjournaled mapped addresses; the real networkd
sentinel fixture must establish whether release removes those addresses.
The Configs transfer procedure remains under implementation. No shared
gateway upgrade, ownership transfer, or production deployment ran.

### Accept unchanged simulator deployment

The unchanged management play completed with ok=162, changed=0,
unreachable=0, failed=0, skipped=55. It used the same clean merged cec8de0d
checkout as the successful first run. Primary Configs advanced to merged
aaeffd48 only after the play's terminal result. Primary MWAN remains clean
at f025543. Both merged feature worktrees and branches are removed with
their external evidence preserved.

Both guests' final overlapping observer windows completed successfully.
Each guest received all 600 IPv4 and IPv6 probes, with no failed route
queries. Successful queries selected the primary. The second and final
windows overlap and cover the entire unchanged play. Simulator management
and repeat deployment behavior pass; full gateway release acceptance and
load balancing remain pending. MWAN-521 and MWAN-522 were read from Tack
and both remain In Progress, matching their unfinished acceptance work.

### Review external-owner mapping exclusion and aggregate coverage

MWAN PR #149 contains signed head b40a83654054362857557e7d29b1875093b8d3c8
on merged f025543. Root verified its raw signature and git signature,
inspected the complete runtime diff, and read its real regression report.
The final fixture passed in 14.23 seconds and failed against the earlier
published daemon in 14.10 seconds after observing both unwanted mapped
IPv4 addresses. The fix excludes explicit external owners from legacy
mapped-address creation and reporting. It preserves networkd legacy writes,
MWAN receipt consumption, and scoped NPT authority. All ten required CI
checks passed; no review threads were posted at inspection. Independent
review remains in progress. The nonrequired vulnerability check reports
the existing GoBGP GO-2026-4736 advisory with no fixed release.

The public protocol runner includes release verification but omits the
mapped-address and selection regressions from its namespace case list.
A focused aggregate coverage correction is assigned separately from the
frozen runtime PR. The combined published-release acceptance must execute
these existing cases and reject skips. The previous complete 27-case
published aggregate remains valid evidence for its earlier source.

The shared promotion manifest now includes accepted all-five simulator
provisioning, authenticated SSH, the unchanged zero-change repeat, and
the downstream observation results. It requires a new combined release
and a new release-specific acceptance plan while preserving earlier hashes
and evidence. The transfer procedure still requires real forward/reverse
packets, prior-owner cleanup, and management/transit verification. No shared
gateway upgrade, ownership transfer, or production deployment ran.

### Merge external-owner mapping repair

Independent review approved exact PR #149 head
b40a83654054362857557e7d29b1875093b8d3c8 without findings. PR #149 merged
as 7d0fb56a876b272dc797bd81c553dbb060c0ed3d at 03:13:33Z. All ten
required checks passed and no review threads remained. Primary MWAN
advanced from its own clean checkout to that merged revision.

The completed AMD64 firewall job passed all seven firewall cases, including
mapped addresses, static addresses, and selection exclusion. Its subsequent
protocol aggregate failed DHCPv6 restart withdrawal and prefix expiry.
Both failures observed the NPT edge still in the journal after kernel
withdrawal. The complete failed job is retained at
[/tmp/mwan149-firewall-failure.log](/tmp/mwan149-firewall-failure.log).
Diagnosis must distinguish asynchronous persistence from failed cleanup;
the failure alone establishes neither cause. An independent agent owns
the exact public reproduction without longer deadlines or skipped cases.
The combined release remains unaccepted for deployment.

The transfer fixture's IPv6 forwarding failed because its global forwarding
sysctl was zero. Applying the production template's exact all.forwarding=1
setting produced a downstream IPv6 reply with TTL 63 and no packet loss.
This proves the fixture prerequisite correction, not complete transfer
acceptance. Forward/reverse validation remains in progress.

### Merge published aggregate coverage and verify transfer limits

PR #150 merged as 78edc3faec12d25c1129f39751d588f3a3c6c6e5 at 03:23:21Z.
Root reviewed the exact four-file patch, all five real case results, and
the final builder checks. All ten required CI checks passed, no review
threads remained, and the signed feature commit verified. The public
aggregate now requires 27 namespace and six systemd cases. Static, firewall,
and kernel-policy cases use the existing published-binary boundary instead
of unconditionally building source. The kernel-policy fixture's stale error
expectation was corrected to the observed production ownership-journal
error. Its malformed-input rejection and no-write assertions remain.
Final published aggregate acceptance remains pending.

Real netlink observation established the DHCPv6 test race: kernel deletion
was observable while the prior NPT receipt remained on disk, followed by
receipt removal. PR #151 changes only the two affected fixture cases to
one joint kernel/journal wait under their original ten-second deadline.
All four real restart cases passed in 58.602 seconds. Its signed rebased
head is 7ee0ff75a9359e87b1cc1f5f839d31c95b28bdc5 on merged PR #150;
integrated checks passed locally and required CI remains in progress.
Root disproved and resolved the bot's proposed weaker receipt check:
the fixture selects only expected-prefix 2001:db8:30::/60 and already
requires no NPT receipts after withdrawal or matching-prefix expiry.
The other valid cached prefix remains separately asserted.

The actual networkd release test retained mapped .3 through .6 IPv4 /32s
after primary-address removal and unmanaged-state verification. The
[kernel snapshot](/Users/agoodkind/.local/state/mwan305/20261001-transfer-procedure/manual-external-addresses.json)
proves retention. The approved repair uses the single address-module
reconciler to reserve and create legacy networkd mappings, then prune exact
receipts on external release. Networkd retains ordinary acquisition and
routes. The initial baseline reboot creates fresh receipts without adopting
existing unjournaled aliases. No new journal scope or second reconciler
is required; implementation and real release/acquisition proof remain pending.

The veth transfer fixture lacks a permanent hardware MAC, so MWAN correctly
reports its physical link not ready. Backup replies do not prove selected
replacement acquisition. The Configs fixture must use an exclusively owned
real virtual NIC and require applied assignments, routing, translation,
exact mapping receipts, and selected-provider packets before restoring
selection. Existing shared guests remain unchanged. No gateway or production
deployment ran.

### Merge the bounded journal-observation correction

PR #151 merged as cc928cfeceb74b493271627d5787c21697d05158 at 03:28:59Z.
All ten required CI checks passed, the sole rewritten feature commit's
signature and raw header verified, and the disputed review thread was
resolved with the exact fixture policy and actual pass evidence. Primary
MWAN advanced from its clean owning checkout to the merged revision.
The bounded cleanup is assigned to the original implementation agent.

The legacy mapped receipt repair remains under implementation. It uses
the existing address-module reconciler and preserves networkd ordinary
acquisition. The Configs procedure must verify actual selected-provider
acquisition with a real permanent-MAC NIC before selection restoration.
Published aggregate, shared baseline deployment, complete transfer/reversal,
and production acceptance remain pending. MWAN-305 remains active.

### Verify merged protocol CI and isolate transfer control

Merged-main CI run 36810709792 completed. All DHCPv6 restart, rejection,
withdrawal, and prefix-expiry cases passed on AMD64. The kernel-policy
packet case failed with forwarding=1 and a missing UDP reply. The
[actual CI log](/Users/agoodkind/.local/state/mwan305/20261001-dhcpv6-journal-observation/merged-main-firewall.log)
includes the firewall, routes, policy rules, and rp_filter=2. This failure
requires diagnosis before published aggregate acceptance. ARM64 and ordinary
network-namespace jobs passed; their results do not establish aggregate acceptance.

The real networkd JSON observation marks retained mapped /32 addresses
as ConfigSource=foreign and ordinary configured addresses as static.
The [released provider observation](/Users/agoodkind/.local/state/mwan305/20261001-transfer-procedure/networkd-external-address-sources.json)
and [transit observation](/Users/agoodkind/.local/state/mwan305/20261001-transfer-procedure/networkd-transit-address-sources.json)
are preserved. Actual DHCP source encoding remains unverified. The receipt
repair must preserve configured and acquired primary /32 addresses without
adopting foreign mapped aliases.

The isolated physical fixture exposed SSH input drops on enoob0 after
production firewall activation. Its configuration now declares an external
control connection and typed management services restricted to 10.0.2.2/32.
The production management role still uses enmgmt0. Downstream packet tests
use transfer-downstream; control SSH does not prove provider connectivity.
The fixture agent verified its launcher remains live and reported both guests
ready. Baseline cold creation, forward/reverse transfer, and owner-aware
management/transit acceptance remain unproven. Shared testbed and production
gateways remain unchanged.

### Review the transfer acceptance boundary

Source review confirmed that role activation and recovery still compare
networkd-generated files, DNS, and static route observations. These checks
require an owner-aware companion before transit or management transfer.
Provider acceptance does not complete that integration requirement.
The new packet-check input appears in the tasks and fixture but lacks an
inventory declaration; declare it explicitly before publishing the transfer.

The public loader rejects explicitly disabled families. Astound instead
configures an empty IPv6 family with DHCP=false, accept-ra=false, and native
translation. Its renderer emits that family. The acquisition gate must
distinguish requested acquisition from a present but empty family. Require
applied assignments, routing, translation, and actual downstream replies for
families that request acquisition; retain owner and release checks for all.

Both exclusive QEMU processes were verified live at PIDs 40331 and 40332.
The recorded ports and permanent MACs match the fixture readiness artifact.
Three unchanged ARM64 kernel-policy executions passed; they do not identify
the cause of the failed AMD64 packet. Route-table and neighbor observations
remain required before changing the packet fixture.

### Verify acquired primary identity and preserve packet failure evidence

Actual Kea and networkd acquired IPv4 198.51.100.100/32. The
[acquisition observation](/Users/agoodkind/.local/state/mwan305/20261001-transfer-procedure/networkd-dhcpv4-source-acquired32.json)
reports ConfigSource=DHCPv4, ConfigState=configured, and its active lease.
The capture and server logs preserve the actual exchange. Extend the existing
typed networkd observer to distinguish acquired primaries from foreign aliases.
The runtime integration and its packet regression still require acceptance.

The corrected six-case systemd lane passed in 36.361 seconds with zero skips.
The release case passed in 4.49 seconds after verifying pending mapping receipts
on the surviving unmanaged interface and subsequent external pruning.
The accepted events are under mapped-receipts-systemd-native; the earlier
mapped-receipts-systemd-final failure remains preserved. This result precedes
the acquired-primary observer integration and does not prove that later change.

The physical fixture rejected replacement acquisition with unrecorded mapped
IPv4 and ordinary static IPv6 addresses still present. The selected native
IPv6 source file has no KeepConfiguration directive. The release cause remains
unverified pending exact prior and external snapshots. No address was adopted
or manually deleted. Selected-provider proof during exclusion must use actual
inbound request/reply packets; backup outbound replies are separate evidence.

Repeated ARM64 kernel-policy runs preserved the 300-millisecond deadline and
confirmed the marked table's LAN route. Their successful packets do not assign
the original AMD64 failure. Suburban has no Docker or Podman runtime, and
existing CI has no targeted manual job or retained protocol result artifacts.
A focused diagnostics change will preserve failure-time routes, selectors,
neighbors, daemon logs, and existing CI results without changing packet
assertions or production behavior. No shared gateway deployment occurred.

### Publish packet diagnostics and verify focused physical release

PR #152 publishes signed ca3c5e1ecf71c8a39600a3a5adfe9dbe9ef1d03e.
Independent root review verified its complete two-file diff, raw signature,
project checks, and the unchanged real packet case's 3.201-second pass.
The test records marked routes, rule selectors, neighbors, kernel settings,
and the current daemon log only after failure. Existing CI preserves actual
protocol result artifacts. Native AMD64 CI and required checks are running;
no product failure cause or completed aggregate acceptance is claimed.

The first focused release observation contained neither ordinary primary
address after networkd reported enwebpass0 index 3 unmanaged. A repeated
observation retained IPv6. The prior generated file has no KeepConfiguration
directive. Generic before/after output filenames were reused; preserve each
attempt separately before citing its files as acceptance. The transfer must
verify actual prior-owner release without banning unrelated foreign objects
or creating ordinary MWAN receipts.

### Record diagnostics merge and pending native acceptance

PR #152 merged as 045d391530865eb4ba447a733f3ed9b7508fe589 at 04:04:55Z
after all ten required checks passed and no unresolved threads remained.
The signed feature head and complete diff were independently reviewed.
Its original native run 36813073217 and firewall job 110212160994 subsequently
completed cancelled. Follow the new merged-main execution for native packet
acceptance and preserved artifacts. The canceled run is not accepted evidence.
The mapped-address repair, exact source release boundary, complete transfer,
and downstream testbed and production acceptance remain incomplete.

### Verify the failed baseline's address provenance

The repeated physical baseline already classified fd39:10::2/64 as foreign,
tentative, and configuring before release. AdministrativeState=configured did
not establish usable IPv6 acquisition. The
[preserved retained-address snapshot](/Users/agoodkind/.local/state/mwan305/20261001-configs-physical-transfer/root-retained-ipv6-20261001T040941Z.json)
records the failure. The fixture must establish a fresh cold baseline and
actual configured static acquisition before testing source release. A
networkd restart during duplicate-address detection requires separate diagnosis;
this failed baseline does not prove a retained networkd-owned address defect.
The [first-attempt report](/Users/agoodkind/.local/state/mwan305/20261001-configs-physical-transfer/attempt-1-source-release/result.md)
discloses that its original raw snapshots were overwritten. The second
attempt's raw snapshots and debug journal are preserved separately under
attempt-2-networkd-restart. Do not use the current generic filenames as
first-attempt evidence.

The real DHCP mapping regression exposed premature alias creation before
networkd acquired its configured primary. Defer networkd mapping installation
until actual configured DHCPv4 acquisition, then exclude the primary from
mapped receipts. The runtime correction and its packet proof remain pending.

Merged-main CI run 36813450461 and firewall job 110213303425 are verified live
for source 045d391. The canceled feature run uploaded partial protocol results;
execution stopped before the kernel-policy case. Artifact preservation passed,
but native packet and aggregate acceptance remain pending. The clean owning
MWAN main checkout advanced to the merge. Shared gateway and production
deployment have not changed.

### Record native packet failure and diagnostic correction

Merged-main run 36813450461 completed with a kernel-policy packet failure.
Firewall job 110213303425 uploaded artifact 11140693854. The
[native failure report](/Users/agoodkind/.local/state/mwan305/20261001-kernel-policy-packets/report.md)
records the preserved log and protocol results. The first enabled packet
timed out. The container lacks the ip executable; route, rule, neighbor, and
marked-route diagnostics failed. Sysctls and the complete daemon log were
captured. The daemon reported table-100 LAN RouteReplace before the packet
deadline. This evidence does not establish the packet failure's cause.
Replace the diagnostic commands with existing netlink APIs and repeat native
packet validation without changing its deadline or assertions.

Ledger commit c978cf1 passed raw-header and signature verification across all
55 branch-local commits and was pushed. The physical transfer and complete
shared testbed acceptance remain incomplete. Production has not changed.

### Prioritize implementation and actual cutover

The user directed implementation and cutover to take priority over additional
tests. Stop expanding auxiliary fixtures. Publish and merge focused runtime
and transfer changes under the active GitHub merge contract. Verify actual
downstream forwarding, ownership, reversal, and recovery during testbed
cutover before production promotion. Preserve failed observations without
claiming a pass or weakening a failing assertion.

PR #153 publishes signed b1c8e90c354898d0e1db86158b62f857fa070dd2.
Root reviewed the complete diagnostic patch, verified its signature and raw
header, and inspected the project checks and existing ARM64 packet output.
All ten required checks passed. Native firewall job 110217644153 in run
36814871029 remains active. The diagnostic PR does not block publication of
the runtime and transfer changes.

Root review found stale mapped-address writer claims in the loader. The
runtime repair must assign the actual writer for networkd, MWAN, and external
owners. The Configs transfer changes still require management and transit
recovery that supports the configured owner. No shared ownership transfer
or production deployment occurred.

### Publish runtime repair and transfer implementation

MWAN PR #154 publishes signed 6c24cd6682659688d5666c6f93035e9bd128add9
on merged 045d391. Root reviewed the address journal migration, DHCP primary
exclusion, release gate, and corrected writer claims. Required checks remain
pending. The supplementary DHCP reply failure remains preserved and excluded
from the accepted coverage. Actual downstream delivery requires testbed
cutover proof.

Configs PR #594 publishes signed 5ad0c4c7fe727ffa51186d1cf093bcbe3af0fa52.
Root reviewed exclusion, source release, replacement acquisition, recovery,
managed-input pruning, and packet execution. Both branch-local signatures and
raw headers passed verification. Required lint passed; required data tests
remain pending. The source-release gate checks captured static, DHCPv4,
DHCPv6, DHCP-PD, and NDisc objects without deleting foreign or kernel objects.
The dependent management and transit recovery implementation remains pending.
Shared testbed and production ownership have not changed.

### Require actual testbed cutover cycles before production

The user requires testbed cutover, failure identification, reversal, repair,
merged deployment, and repeated cutover validation. Yield only after actual
testbed evidence establishes production readiness. Do not deploy production
before that readiness report. Keep both downstream observers active during
each risky operation and retain failed observations.

PR #153 merged as 1c46529aab2909d6baaa73fe2347c4594f98e67a.
PR #154 merged as 0ff387b589773a914201ce0e129dc4feae2f8786 and includes
the diagnostic repair. Required checks passed and review threads are resolved.
The primary MWAN checkout is clean at 0ff387b. Published release verification
and the focused Configs baseline pin remain pending.

Configs PR #594 publishes signed 9e7f277019e6acece126d195d9b660e7bc70f764.
All three required checks and all three branch-local signatures passed.
Two new review findings require verification: the recovery backup filename and
dynamic networkd acquisition readiness. The PR remains open. The separate
management and transit recovery changes remain in implementation.

Read-only shared checks confirm the WAN daemon is active. Client 225 uses
the IPv4 default through 10.240.1.1 on eth0. Client 226 uses IPv6 defaults
through 3d06:bad:b01:211::1 and fe80::1 on eth0. These checks do not establish
cutover acceptance. Shared and production owners remain unchanged.

### Execute the first merged shared baseline deployment

Configs PR #594 merged as 67544f8a2a267257aec0a97b63b609a50795304c.
Its acquisition check now requires actual configured DHCP addresses, usable
router advertisement state, and a served delegated prefix when requested.
The recovery backup finding was disproven by the transition task's templated
transfer-source.json copy before the recovery snapshot boundary.

Configs PR #595 merged as 2b93519ab43f6707bbf3554d6939f63eb16da9c1.
Release 202610010439-97-0ff387b passed all archive checksum, API digest,
and attestation checks. Its actual AMD64 executable SHA256 is
8b25781720ad04e9ebc7e8dc82fd0e1176c07f73f7b1e016f153e73413976907.
The merged pin preserves every owner and both lease recovery policies.

The complete shared deploy ran from clean merged 2b93519a with both source-bound
downstream observers. It failed at the OPNsense certificate retrieval before
daemon installation, ownership activation, or reboot. The command
qm guest exec 201 returned QEMU guest agent is not running. The predeploy gate
failed for the same reason and skipped its snapshot. Actual downstream packets
continued receiving replies. VM213 still reports active daemon 2f9a40a.

The FreeBSD guest agent service is running and enabled. Its custom MWAN channel
and QEMU channel use different tty devices. The actual agent channel selection
requires diagnosis before another deploy. Do not infer network loss from this
guest execution failure.

The prior binary and network document are preserved under
/Users/agoodkind/.local/state/mwan305/20261001-real-cutover/baseline-before.
The failed deploy log is baseline-deploy-ansible.log in the parent directory.
Ownership cutover, reversal, balancing, restart, reboot, and production readiness
remain unaccepted. Production has not changed.

## Save the mandatory execution contract on September 30 at 22:35 PDT

The operator requires strict subagent-driven development, complete rereads
after every compaction and before every slice and integration, and a regular
heartbeat. The coordination plan now includes the operational goal and these
requirements. The active goal includes every current epic child ticket,
all plans and applicable specifications, the complete memory registry, and
this entire ledger. Partial reads and summaries do not satisfy a checkpoint.

Automation mwan-305-execution-checkpoints is active every 30 minutes on this
thread. Its saved prompt repeats the complete reread and evidence requirements.
The plan's skill links resolve. The plan passed git diff --check. No runtime
code or deployment configuration changed for this request.

This checkpoint is incomplete. The coordinator read all 347 lines of the
updated plan. Memory reads included truncated output; the complete 8126-line
registry has not been reread. Only the ledger's final 42 lines were read in
this turn. All six slice plans, applicable specifications, and every current
epic child ticket still require complete reads before the next runtime slice.
No full checkpoint is certified.

The plan SHA256 after its complete read is
060bf378eb632a18f61cf543932b3e70b74ab3a62a7f657a4d8b31ebd0cbfa9b.
The registry SHA256 is
76cc7105d84d8b0702666495a8ab7c7b3f601577409b20697cc4ac05a1254e06.
The ledger SHA256 before this entry is
95ec52e15ad2611f79d73c6d839c0db43d5c467201bdd48b26bf1452cc54532c.
The existing release_completion agent observes only the already running
baseline deployment. It has no authorization for another mutation or repair.
Root retains sole coordination plan and ledger ownership. The next runtime
slice remains baseline acceptance before Webpass ownership activation.

## Apply the clarified reorientation requirement

The operator clarified that each slice and compaction requires practical
reorientation. This replaces the exhaustive reread requirement above.
Review the current plan, applicable specifications and tickets, relevant
memory, and recent ledger entries. Read older decisions when needed.
Do not block implementation on reading unrelated memory, the entire ledger,
or collecting read counts and hashes. Strict delegation and operational
acceptance requirements remain unchanged. The goal and 30-minute heartbeat
use this clarified requirement.

## Accept the baseline reboot and begin packet acceptance

At September 30, 22:46 PDT, the coordinator reoriented using the current
coordination plan, deployment and cutover plans, interface specification,
relevant memory, recent ledger, current checkout, and active agent handles.
Root owns runtime ordering and this ledger. Three agents independently
refresh ticket prerequisites, live deployed identity, and runner operations.
Their assignments permit read-only inspection and exclude target mutations.

The baseline deploy from merged Configs
4abe359398a0c9e5362c01d34d258bf3c65328ed finished successfully.
Its recap reported ok=306, changed=33, unreachable=0, failed=0.
The verdict 20260930-221030-deploy-745801 reported reboot_rc=0,
egress_rc=0, and owned_rc=0. VM 213 changed boot identity and ran MWAN
0ff387b with the expected installed and running executable hash.
Both downstream clients received every sequence from 1 through 1798 for
each IP family. Both observed backup selection during reboot and primary
selection afterward. Four SSH route-query connections reset while packet
sequences continued. The complete evidence is retained in
[the baseline result](../../../../.local/state/mwan305/20261001-real-cutover/baseline-final-readback/result.md).

Fresh downstream observers run as sessions 69796 and 80141 for clients
225 and 226. The existing public acceptance runner runs as session 21128
with shared-plan-0ff387b-linux.json and output baseline-battery-0ff387b.
All artifacts are under the existing 20261001-real-cutover evidence directory.
The runner must prove balancing, mappings, translation, and persistent
failure history after a real route deletion, restoration, and daemon restart.
The destructive operation has not run. Webpass activation remains undeployed.
Production readiness and ownership transfer remain unaccepted.

## Record preflight failures and Astound drift

The first packet battery exited 1 during client226-fallback-routes6 because
the Suburban SSH connection closed. Its cleanup_errors array was empty and
its results object was empty. A direct repeat of the client route query
succeeded. The second battery, session 41179, then exited 1 during
client225-hostname because SSH banner exchange timed out. It also reported
empty results and no cleanup errors. Both failed artifact directories remain
unchanged. Neither run started captures or injected a network fault.

Direct Suburban SSH subsequently closed connections before authentication.
Its journal could not be read; the cause remains unverified. Root stopped
only local observers 75197 and 75434 with SIGINT to reduce route-query
connection attempts. Sessions 69796 and 80141 exited 130 and their processes
are absent. No target service or network configuration changed. Fresh
observation is required before any subsequent risky operation.

Independent live readback verified the expected binary and network hashes,
all five target identities, seven networkd owners, and healthy AT&T/Webpass
routing and translation. Astound CT 903 is running, but enastound0 has carrier
without an IPv4 address or provider default in table 700. Networkd reports
configuring/degraded and current daemon probes time out. Simulator DHCP
processes and the timing of acquisition loss remain unverified. Restore and
accept the Astound baseline before ownership activation. Do not attribute
this defect to the reboot without evidence.

Tack refresh confirms MWAN-305, 519, 521, 522, and 520 are In Progress;
MWAN-399 remains Todo. No incomplete acceptance ticket was closed.
The current blockers are unavailable SSH control access and degraded Astound
IPv4 acquisition. Production and Webpass ownership remain unchanged.

## Assign the measured DHCP startup repair

The third baseline battery, session 63506, exited 1 during
client225-ipv4-route-get after SSH banner exchange timed out. It started no
captures or network fault and reported no cleanup errors. Direct SSH later
recovered. The Suburban SSH journal reports failed-authentication penalties
and dropped connections during the failed interval. Host observations show
load averages above 75 and 70.2% time waiting for input/output. No SSH
security or unrelated service configuration changed. The source of
authentication failures and host resource pressure remains unverified.

Read-only CT 903 diagnosis verified isp-astound, an active Kea DHCPv4
process, and eth0 up at 10.240.207.1/24. Kea's startup log reports eth0
down, zero retries, and DHCPSRV_NO_SOCKETS_OPEN. Its packet-socket table
contains no receive socket. The gateway's missing IPv4 acquisition remains
unaccepted. This is a confirmed simulator startup defect, separate from
MWAN ownership implementation and the earlier completed MWAN-524 repair.

MWAN-534 is In Progress under MWAN-305. Independent contract review
identified supported Kea DHCPv4 socket retry fields. The implementation
assignment owns only testbed/isp-lxc/kea-dhcp4.conf.j2 in
/Users/agoodkind/.codex/worktrees/mwan-305-dhcp-socket-recovery/configs.
The branch is codex/mwan-305-dhcp-socket-recovery from merged
1b8ec0dd8a6cafe741b41d7a9149670f7e07b3b9. Native worktree creation
succeeded, but attachment registration failed at the 100-artifact limit;
the returned checkout is used without creating a duplicate.

The settled repair requires all sockets, retries up to 60 times, and waits
1000 milliseconds between attempts. Installed-version validation and actual
down-interface startup, recovery, DHCP acquisition, and bounded exhaustion
remain required. No repair has merged or deployed. The next operation is
review and real validation of this focused fix before simulator deployment
and baseline packet acceptance. Production and gateway ownership remain
unchanged.

## Deploy the merged DHCP socket recovery

Configs PR [598](https://github.com/agoodkind/configs/pull/598) merged as
4865782dbd3107217ec7a6bb2e4107a4c258d8a6 at 2026-10-01T06:08:28Z.
Independent review of signed source 64ae0298 found no actionable defects.
The actual rendered Astound configuration passed Kea 2.6.3 parsing.
An isolated real DHCP client obtained 10.240.207.2 after delayed interface
activation in 4.188 seconds without changing Kea PID 525. The permanently
down interface exhausted 60 retries and exited 1 after 60.182 seconds.
These checks prove socket recovery and lease negotiation, not shared
gateway acquisition or downstream forwarding. The owned container was removed.
The retained evidence is in the existing dhcp-socket-validation directory.

The clean merged Configs main checkout started
`./configsctl deploy deploy-testbed --limit suburban --tags isp-lxcs`
at 2026-10-01T06:11:32Z. Session 96066 remains active. This operation
configures the testbed simulators and does not activate gateway ownership.
The deployment log is deploy-testbed-20261001T061132Z.log in the existing
configs-runs directory. Do not change this checkout or start another deployment
until its actual process terminates.

Fresh observers run as sessions 22513 and 89809 for clients 225 and 226.
Their output directories are simulator-repair-client225 and
simulator-repair-client226 under the existing 20261001-real-cutover evidence
directory. The local multiplexed-ssh-config reuses authenticated SSH
connections without changing server security configuration. Both guests
received actual IPv4 and IPv6 replies before deployment and at 06:17:23Z.
Final packet totals and interruption remain unmeasured while observation runs.

MWAN-534 remains In Progress. MWAN-519 now depends on its live acceptance.
Require the deployed Astound DHCP socket, gateway address, provider route,
readiness, downstream packets, and repeated cold startup before closing it.
Webpass ownership activation, the full packet battery, and production
readiness remain unaccepted. Production configuration remains unchanged.

## Complete simulator deployment and prepare reversal

Session 96066 terminated with exit 0. The simulator-only deployment recap
reported ok=257, changed=70, unreachable=0, failed=0, skipped=77,
rescued=0, and ignored=0. The primary Configs checkout remains clean at
merged 4865782d. Astound live acquisition and cold startup remain unaccepted.

The observer snapshot at 06:21:59Z recorded 627 replies per guest and family
since deployment start. Every sequence had zero observed gaps or duplicates;
route queries selected the primary and reported zero failures. Observers
22513 and 89809 remain active. Final transmission totals remain pending.
The retained snapshot is in simulator-repair-observer-snapshot/report.md.

Root verified the PR #598 slice against current trunk before removing its
clean feature worktree and local branch. The remote branch was already absent.
No ignored, untracked, modified, or submodule paths required preservation.
The first local deletion from the unrelated checkout failed its merged check;
deletion from the unchanged main checkout then succeeded. Trunk and active
deployment files were not changed by cleanup.

The merged ownership mechanism renders the requested owner and permits one
changed connection. It has no manual reversal selector. Prepare a focused
Webpass networkd inventory PR in the returned
/Users/agoodkind/.codex/worktrees/mwan-519-webpass-reversal/configs checkout.
Do not merge or deploy it before the first forward transfer passes.
The actual released loader rejected an owner-only candidate because networkd
requires rendered link files. The candidate must restore that declaration
and pass the loader before review. Second forward transfer requires restoring
the MWAN owner through another merged configuration.

## Resume actual Webpass cutover after Astound acceptance

MWAN-534 is Done. The simulator deployment passed. Actual Astound cold
startup reproduced the initial eth0-down error; Kea PID 83 recovered its
packet socket without a service restart. The gateway retained its valid
DHCP lease, provider default, ready IPv4 routing and translation, and healthy
probes. This cold startup did not produce a fresh DHCP exchange.

The completed simulator observers transmitted 1798 packets per guest and
family. Client 225 missed IPv4 sequence 773; its cause remains unverified.
The other three guest/family observations received every packet. Route
queries selected the primary and reported no query failures.

The independently reviewed reversal candidate is signed commit da7d4218
on codex/mwan-519-webpass-reversal. Released loader, render, identity and
networkd unit checks passed. The candidate is not merged or deployed.
Merge it only after the actual forward transfer passes.

The first forward deployment used clean merged Configs
4865782dbd3107217ec7a6bb2e4107a4c258d8a6 at 2026-10-01T06:43:43Z.
Session 23760 terminated with exit 1 at 06:48:39Z. Ansible failed while
creating the pre-deploy snapshot because VM 213 was locked for
snapshot-delete. The recap reported ok=116, changed=9, unreachable=0,
failed=1, skipped=14, rescued=0, and ignored=0. Ownership transfer did
not execute. Investigate the active snapshot operation before retrying;
do not remove an active operation's lock.

Downstream observers 22271 and 18961 remain active in
webpass-forward-client225 and webpass-forward-client226 under the existing
20261001-real-cutover evidence directory. Final packet results remain
pending. The standby calibration drill is deferred. Prioritize actual
forward transfer, reversal, recovery and second forward transfer.

## Retry Webpass forward transfer after the snapshot operation

Read-only Proxmox inspection confirmed VM 213 running, no configuration
lock, and no active tasks on node hypervisor. The first API task query used
an unsupported option; the corrected query used source=active and returned
an empty list. Root did not unlock the VM or interrupt a snapshot operation.

Each downstream guest and family received all 296 probes during the failed
deployment interval 06:43:43Z through 06:48:39Z. The longest reply gap was
1.017292 seconds. Every sampled route selected the primary gateway; no
route query failed. This proves observed ICMP continuity, not completed
ownership transfer or new-flow load balancing.

Root retried the same clean merged deployment at 06:51:43Z. Session 68771
is active. Its log is deploy-mwan-20261001T065143Z.log in configs-runs.
Observers 22271 and 18961 continue. Do not mutate the primary checkout or
start another deployment while this process runs. The reversal PR may be
prepared independently, but it must not merge before forward acceptance.

## Correct the acquisition gate found by actual cutover

The retry created its recovery snapshot successfully. Source exclusion
passed. The source networkd acquisition gate then failed after 30 attempts,
before owner release. Session 68771 began restoring captured prior inputs;
its recovery result remains pending.

Independent review identified the sole false condition: the RA acquisition
check accepts NDisc addresses or Routes but ignores configured NDisc
NextHops. Live Webpass has static IPv4 10.241.204.2/29 and DHCPv6 PD /56.
Its configured NDisc nexthop 2311400098 matches the actual kernel RA default
and gateway fe80::be24:11ff:fe7f:de4e. Preserve the real networkctl, network
document and kernel routes in webpass-source-acquisition-failure under the
existing 20261001-real-cutover evidence directory.

Delegate the focused acquisition check correction in the isolated
/Users/agoodkind/.codex/worktrees/mwan-519-ra-readiness/configs checkout.
Do not change the active primary checkout, relax routing or translation
requirements, or deploy an unmerged fix. Validate the production predicate,
review, merge, and retry actual cutover after recovery completes.

Both downstream guests and families selected the backup during the source
restart around 07:05:58Z through 07:06:11Z. The observer found no missing
probe sequences. These observations prove sampled gateway failover, not
individual ISP selection or new-flow balancing.

Configs PR #599 publishes signed reversal head
da7d4218e877015f19b76a066252993bad6cbd84. Required checks and Graphite AI
review passed; no review threads remain. PR-Agent quota exhaustion is not
a required check. Keep the PR unmerged until forward acceptance passes.
Native attachment failed because the thread has 100 identities; the PR
remains accessible as [Configs PR #599](https://github.com/agoodkind/configs/pull/599).

MWAN-519 remains In Progress. Snapshot task records prove a lock conflict,
but they do not identify the caller. Deploy and watchdog snapshots lack
shared coordination; that separate gap does not block the successful retry's
snapshot. Continue the actual cutover correction and recovery.

## Verify recovery and correct the packet observation

Session 68771 terminated with exit 1 at 2026-10-01T07:15:07Z. The recap
reported ok=290, changed=33, unreachable=0, failed=1, skipped=38, rescued=1,
and ignored=0. The playbook restored prior role inputs and verified the
applied recovery state. Owner release never ran. The gateway retains the
baseline network checksum c8a32e91b4b42a9d189b34211b943d6c827f4dafbe4d78e8007272d7cfb33307,
binary checksum 8b25781720ad04e9ebc7e8dc82fd0e1176c07f73f7b1e016f153e73413976907,
and boot ID 204ae526-a4d8-4ee1-8c4e-11a575a3e546. Actual served state
reports Webpass owned by networkd, healthy, selected, and ready for routing
in both families. Preserve recovered-operational.json with the failure evidence.

The earlier no-loss statement used an incomplete observation. Client 225
missed 246 replies per family, sequences 1450 through 1695. Its guest
interreply gap was 252.942 seconds for IPv4 and 252.946 seconds for IPv6,
around 07:06:11Z through 07:10:24Z. Raw remote ping output records every
unanswered sequence. SSH output loss does not explain these records.
The cause and loss location remain unverified. Client 226 had no missing
sequences in either family. All channels currently receive replies.

Both clients sampled the backup around 07:05:58Z through 07:06:11Z and
07:10:24Z through 07:10:39Z. Route queries did not fail. Do not claim
uninterrupted forwarding or production readiness from this attempt.

The isolated acquisition correction is signed
1e6375e4a554d1a2034db5d023bb8703fa0a8c19. Independent review found no
blocking defect. The production expression accepts the captured live
configured IPv6 NDisc nexthop; absent, IPv4, foreign and unconfigured
nexthops do not satisfy the RA predicate. Lint passes. The existing suite
reported 204 examples, one unrelated quote-escaping failure, and 18 pending.
The correction is not merged or deployed. Preserve the later operational
routing and translation requirements and retry through merged deployment.

## Preserve existing forwarding during selection exclusion

Configs PR #600 merged as b0c0c6bd9aa4a0d4c2960da73b716b1c4cf297e5 at
07:19:12Z. Required lint, data and security checks passed, as did Graphite
AI review. Root fast-forwarded the clean primary Configs checkout to that
merge after recovery terminated. The fix has not been deployed.

Independent source review confirmed a separate contract discrepancy.
Administrative exclusion removes the provider from new-flow assignments
and guard eligibility. Existing conntrack marks persist, while the excluded
provider's forwarding guard drops internal packets routed through that
provider. Ready provider routes and translation remain installed. The
approved contract excludes new traffic and preserves existing connection
affinity while forwarding remains ready. This discrepancy could explain
the guest 225 gap, but the actual flow mark and packet loss location were
not captured during that attempt.

Implement the narrow forwarding correction in the isolated
/Users/agoodkind/.worktrees/mwan-519-excluded-forwarding checkout, based on
merged MWAN 0ff387b. Separate new-flow selection permission from forwarding
readiness. Keep excluded providers out of new assignments, permit existing
ready-provider marks, and preserve guards for unhealthy or unready providers.
Do not add a blanket established-connection exception.

The implementation, independent review, merged release, compatible Configs
pin and actual retry remain pending. Capture the existing ICMP flow marks,
installed guards and paired internal/provider packets during the retry.
Do not expand unrelated tests or declare the probe gap's cause proven.
The testbed remains recovered; no deployment or fault is active.
Observers 22271 and 18961 still measure the first attempt and recovery.

## Review the focused exclusion correction

The forwarding correction is signed
d546441a2128d7e1a3e9ded49bf9a1900ed0f7cc. Only the steering calculation and
rule construction changed. Actual per-family health, routing and translation
determine forwarding guard eligibility. Administrative permission separately
determines new-flow assignments. NEW pinned packets select an eligible
replacement; without a replacement, a ready excluded-provider packet receives
mark zero and the existing guard rejects it. Established packets retain
their ready-provider marks. No blanket established-flow bypass was added.

Required Docker check and test gates passed. Existing real steering namespace
packet tests passed in 2.211 seconds. No test expectations changed.
Independent review found no actionable findings at this exact signed head,
including the immediate-value mark-zero encoding, inbound boundaries and
hairpin exclusions. Publication, merge, verified release, compatible Configs
pin and actual cutover acceptance remain pending.

Read-only live diagnostics verified the existing guest 225 IPv6 ICMP flow
has Webpass mark 2. IPv4 on the primary uses the OPNsense-translated source
10.240.240.2, so filtering conntrack by the original IPv4 guest address returns
no entry. Capture ICMP identifiers and sequences to correlate those IPv4
flows; do not infer the guest from an uncorrelated translated entry.

Reuse the accepted bounded systemd capture lifecycle on the primary transit,
AT&T and Webpass interfaces. Capture the existing probes, actual guard rules
and conntrack state during exclusion. Do not run unrelated HTTPS calibration
to diagnose ICMP. Primary captures do not establish backup packet forwarding;
retain downstream replies and router selection as separate evidence.

## Prepare the next actual cutover retry

[MWAN PR #155](https://github.com/agoodkind/mwan/pull/155) publishes the
focused exclusion repair. The revised signed head is
8753e9bffd49796fcaf1adffefdd9fed01f7fe2d. Independent exact-head review found
no actionable findings. Root inspected the complete three-file diff and
verified signatures and raw headers for all three branch commits.

The existing public daemon case initially failed because its convergence
helper rejected every mark assignment, including the valid mark-zero rule.
The corrected helper rejects nonzero assignments and retains the real IPv4
and IPv6 UDP absence assertions. The focused case passed in 4.41 seconds;
required Docker check and test gates passed. The shared assignment predicate
applies identical conditions to both families. Final CI remains pending.

Continue only the cutover repair, verified release, testbed-only Configs pin,
and actual forward transfer. Do not expand simulator calibration or unrelated
features. Production remains unchanged. Configs PR #600 is merged but has not
been deployed. MWAN-519 remains In Progress. Reverse transfer, second forward
transfer, restart, reboot, balancing, mappings, both downstream guests and both
families remain unaccepted. Keep reversal PR #599 unmerged until forward
acceptance passes.

## Finish the failed-attempt observation and establish retry probes

Both original observers ended naturally at 07:42:02 UTC. Guest 225 received
3344 of 3590 IPv4 probes and 3344 of 3591 IPv6 probes. Both families missed
sequences 1450 through 1695 during the approximately 253-second interruption.
IPv6 also lacks the final reply at observer expiry; that sample does not prove
a second outage. Guest 226 received all 3596 probes in each family. All four
ping commands returned zero, which does not establish packet continuity.

Reports and raw output remain in webpass-forward-client225 and
webpass-forward-client226 under the existing real-cutover evidence directory.
The actual failure location remains unverified.

Fresh 7200-second probes began around 07:46:35 UTC. Session 99158 observes
guest 225; session 76919 observes guest 226. Separate webpass-retry-client225
and webpass-retry-client226 directories preserve their evidence. Each family
currently receives replies and each router observation selects the primary.
The actual served Webpass owner remains networkd. No deployment is active.

The bounded operational capture recorder passed independent inspection.
Start it near source exclusion, capture primary transit and both provider
interfaces, and stop it successfully before the planned reboot. Its captures
cannot prove backup forwarding. Keep the downstream observers active through
reboot and acceptance. The final PR #155 firewall/protocol CI job remains
active; required checks passed and no review threads remain.

## Merge the cutover forwarding repair

MWAN PR #155 merged normally as ff37bec50a7837d09b6910fbc8183de3c0478174
at 07:57:21 UTC. All ten active required checks passed, signatures were
verified, and required review threads were resolved. No bypass was used.
The selection-exclusion case passed in both CI lanes, in 5.90 and 5.57 seconds.

The optional combined protocol lane failed its existing kernel-policy
one-shot packet assertion. Its unchanged focused rerun passed in 3.39 seconds.
Independent review confirmed its default-enabled provider has unchanged
assignments and guards; its provider-to-internal packet does not match the
changed outbound guard. The case waits for the static firewall and kernel
settings, not complete routing readiness. The precise loss cause remains
unproved. Preserve the failed full-lane output and focused rerun evidence;
do not report the full protocol lane as passing or expand this cutover repair.

Release run 36833372083 targets the exact merged source. Publication,
artifact verification, a merged testbed-only Configs pin, and deployment
remain pending. Production is unchanged.

The operational recorder captured primary transit, AT&T and Webpass from
07:54:08 through 07:54:56 UTC. It exited zero after successful runner exits,
zero kernel drops and verified capture PID absence. Preserve report.json
and the packet captures in retry-capture-baseline. Both existing retry probe
flows currently have AT&T mark 1 in each family. They do not establish
Webpass affinity preservation. Additional distinct guest-225 probe session
12936 began around 07:58 UTC to observe provider selection without changing
policy or connection marks. Preserve webpass-affinity-client225 separately.

## Start the merged repaired Webpass cutover

Release 202610010759-98-ff37bec completed publication and verification in
run 36833372083. All four archive checksums, API digests and exact-source
attestations passed. Root independently hashed the archives and executables.
The expected AMD64 executable checksum is
98a858c8400e0cd67d2809d54e337b29f7d4a012c910aa47b44919e3352f011a.

[Configs PR #601](https://github.com/agoodkind/configs/pull/601) passed the
actual configsctl render, published loader and isolated firewall checks.
Independent review passed signed head 01d561ad1c719f79333f14631003b29e216dbe65.
The one-file patch changes only the testbed release tag and AMD64 checksum.
All three active required checks and Graphite AI review passed. No threads
remained. Normal merge completed at 08:15:19 UTC as
fd03855dc1c8b6075d49045d9546f56dfb6c649b; no bypass was used.

Root fast-forwarded the clean primary Configs checkout to that merged commit.
The hypervisor reported no active tasks before deployment. The gateway still
had its verified old binary and network checksums and boot ID
204ae526-a4d8-4ee1-8c4e-11a575a3e546. Both primary and backup BGP sessions were
established in both families. Both default prefixes had valid primary and
backup paths. Primary neighbors reported Remote GR Mode Disable and received
restart time zero. This proves control-plane availability, not backup packets.

Actual deployment began at 08:16:24 UTC through
./configsctl deploy deploy-mwan --limit mwan_suburban_servers. Session 23598
is active. The log is deploy-mwan-20261001T081624Z.log under configs-runs.
Do not mutate the primary checkout or start another live operation until the
play ends. Root owns deployment and capture phase transitions. Preserve
observers 99158, 76919 and 12936 through transfer and reboot.

The additional guest-225 IPv6 flow has verified Webpass mark 2 and ICMP
identifier 26647. Original guest-225 and guest-226 flow identifiers are
IPv4 32792/55052 and IPv6 26644/8388, each with AT&T mark 1. The additional
IPv4 identifier remains pending the transfer capture. Guest capture clocks
and gateway capture clocks differ by approximately 0.780 seconds; correlate
identifiers and sequences. Gateway health probes with identifier 8192 do not
prove guest flow delivery. Original guest-225 IPv6 recorded one missing probe
before this deployment; preserve phase-specific loss instead of claiming a
zero-loss cumulative baseline.

PR #599 remains unmerged. Its isolated signed rebase and validation may proceed
against the new main while root performs the forward deployment. Do not merge
or deploy reversal before forward acceptance. Production is unchanged.

## Capture the live transfer phases

Recorder session 63760 began around 08:24:52 UTC. It captures primary transit,
AT&T and Webpass in webpass-retry-capture under the real-cutover evidence
directory. Its 1800-second bound expires around 08:54:52 UTC. Root controls
retry-capture-phase.txt and retry-capture.stop. Stop the recorder successfully
before reboot; do not interrupt it or duplicate capture units.

The initial capture and conntrack snapshot identify the affinity observer's
IPv4 flow as ICMP identifier 16105 and IPv6 flow as 26647. Both have Webpass
mark 2 and actual replies. Their sequence 1583 differs from original probes'
2295. This establishes the pre-exclusion flows; exclusion preservation and
exclusive ownership transfer remain pending. Deployment 23598 is active.

Before transfer, original guest-225 IPv6 missed sequence 2085, with a
2.798083-second interreply gap around 08:21:21 through 08:21:24 UTC.
Delayed IPv4 sequences subsequently arrived. The other channels received
replies. Preserve this staging loss separately from the prior baseline miss
and subsequent ownership-transition results. Its cause remains unverified.

Rebased PR #599 uses signed ab00d28d3b051eff5139f31a8b7a38b47f62ed43 on
fd03855d. Independent source review found no actionable findings. The same
DUID, IAID and delegation settings, mappings, new pin and RA correction are
preserved. Actual reversal remains unproved. Keep this PR unmerged until
forward acceptance passes.

## Diagnose the source readiness failure before ownership release

Deployment 23598 passed source exclusion and the actual networkd acquisition
check. Both guests selected the backup around 08:31:40 UTC and returned to
the primary around 08:31:56 UTC. No additional missing replies appeared in
that interval, including the established Webpass mark-2 flows in both families.

The served assignment check failed after 30 attempts at 08:35:35 UTC. The
operational export returned successfully. The captured Webpass source state
reports networkd ownership, routing ready and translation ready in both
families. IPv6 translation resolves 3d06:bad:b01:2200::/60, but the IPv6
operational family lacks goodkind-mwan-steering:delegated-prefix. The check
requires that field for delegation regardless of the active owner. This
missing field explains the failed predicate; delegation acquisition and the
correct source-owner evidence require further inspection.

Ownership release did not execute. The play restored captured role inputs and
restarted the writer. Both guests selected backup around 08:36:14 UTC and
returned to primary around 08:36:31 UTC without additional missing replies.
Recovery acceptance remains pending. Recorder 63760 remains active; root
changed its phase to role-input-recovery. Original guest-225 IPv6 retains the
single staging miss 2085. Do not claim a completed forward cutover.

Root retains exclusive live deployment and capture control. The delegated
read-only diagnosis examines the owner-specific readiness contract. PR #599
remains unmerged. Production is unchanged. Focus remains cutover, recovery,
and the minimum demonstrated repair.

## Verify recovery after the source readiness failure

Deployment 23598 ended at 08:41:00 UTC with exit status 1. The play explicitly
verified restored role inputs, kernel address identities, forwarding, return
routes and resolver tuples, removed its recovered backup, and reported the
original activation failure. No ownership release or reboot executed.

A separate live read confirms the installed executable SHA-256 is
98a858c8400e0cd67d2809d54e337b29f7d4a012c910aa47b44919e3352f011a,
the approved ff37bec binary. The restored network document SHA-256 is
c8a32e91b4b42a9d189b34211b943d6c827f4dafbe4d78e8007272d7cfb33307.
The recovered operational export reports networkd ownership for all seven
interfaces. It is saved as recovered-0841-operational.json in the real-cutover
evidence directory. The source-excluded export remains separately preserved.

Recorder 63760 finished at 08:41:34 UTC with exit status 0 and report status 0.
Transit captured 11614 packets, AT&T 8593 and Webpass 4736. Every capture
reports zero kernel drops. All three process-absence checks passed. The final
phase includes a recovered-state nftables and conntrack snapshot. Continuous
downstream observers remain active for the corrected retry.

The minimum owner-specific delegation verification repair is under independent
read-only diagnosis. PR #599 remains unmerged and MWAN-519 remains InProgress.
Production is unchanged.

## Verify established Webpass flows during exclusion

Independent capture reconciliation confirms guest-225 IPv4 ICMP identifier
16105 and IPv6 identifier 26647 retain Webpass mark 2 in all six conntrack
snapshots. Actual Webpass replies and matching guest replies exist before
exclusion at sequence 1985 around 08:31:35 UTC, after exclusion at sequence
2123 around 08:33:53 UTC, and after verified recovery at sequence 2572 around
08:41:22 UTC. The excluded-phase firewall retains mark-2 forwarding exemptions
in both families while its Webpass new-selection rules are absent.

Original guest-225 identifiers 32792 and 26644 and guest-226 identifiers 55052
and 8388 retain AT&T mark 1 and receive replies before and after these phases.
The primary captures have approximately 17-second gaps during each observed
backup interval. Downstream replies continued, but primary captures do not
observe backup forwarding. Preserve that evidence boundary. These results
verify exclusion preservation from the new runtime; they do not establish
exclusive ownership transfer, reversal, or reboot acceptance.

The gate repair must verify actual networkd DHCPv6Client.Prefixes and boot-clock
expiry for networkd ownership and valid served dhcpv6-ia-pd assignments for MWAN
ownership. Neither publisher emits the previously required delegated-prefix
leaf. Keep routing, translation and applied-assignment guards. Verify the
resolved translation subnet is contained in the current delegated prefix,
rather than requiring equality between a negotiated /56 and translated /60.
The bounded implementation includes the existing gate and a read-only clock
and networkctl transport helper. No runtime release or lease import is needed.

## Merge the owner-specific delegation gate and retry cutover

Configs PR #602 merged normally at 09:08:22 UTC as
c802ebece1acd610a0632de8fdc9e3d52a2985d3. Its reviewed signed head is
23eba403e0243af4059f04052bb31e79edc6c3c6. Root inspected the complete patch
and all four branch-local commit signatures and raw headers. Independent
final-head review found no actionable findings. The three required checks
passed. All review threads were resolved without an administrative bypass.
The optional final PR-Agent check required quota action; it is not a merge
requirement. The app attachment failed at its existing 100-identity limit.

The gate now verifies networkd DHCPv6Client.Prefixes against a fresh guest
CLOCK_BOOTTIME sample and MWAN valid dhcpv6-ia-pd assignments. Both paths
retain routing, translation and applicable last-apply/assignment guards.
A read-only Python helper samples networkctl and clock, parses its JSON object,
and preserves command failures. Its type alias supports Python 3.10 and newer
without provisioning changes. Actual VM213 helper execution, lint and latest
Python compilation passed. Source predicates passed ten checks through native
configsctl/Jinja. Independent review found the initial CIDR filter accepted
None as non-false; the final correction uses Jinja truthiness. Actual AT&T
2300::/60 and overlapping Webpass 2200::/55 controls both reject containment.
The accepted actual Webpass translated /60 remains contained in its live /56.
Validation logs end 085217Z and 090156Z under configs-runs; external evidence
is under 20261001-networkd-pd-readiness in the MWAN-305 state directory.

Root reconciled clean primary Configs main to the merge and started the actual
merged testbed deploy at 09:08:36 UTC. Session 59233 remains active. The log is
deploy-mwan-20261001T090836Z.log under configs-runs. Do not mutate the primary
checkout or run another live deployment while this play is active. Root owns
live mutation and capture phases; the delegated observer is read-only.

Recorder session 15838 started around 09:09 UTC with a 1800-second bound.
Its output directory is webpass-pd-retry-capture, phase file
pd-retry-capture-phase.txt and stop file pd-retry-capture.stop under the
real-cutover evidence directory. Finish strict cleanup before the planned
reboot. Existing downstream observer sessions 99158, 76919 and 12936 remain
active; preserve phase-specific losses. The hypervisor reported no active
Proxmox tasks and the guest boot ID remains
204ae526-a4d8-4ee1-8c4e-11a575a3e546. PR #599 remains unmerged until forward
acceptance passes. No ownership transfer, reversal, or reboot is claimed from
this retry yet. Production is unchanged.

## Retry after existing snapshot-lock recovery

Deployment 59233 ended at 09:13:34 UTC with exit status 1 before source
exclusion. Proxmox rejected the pre-deploy snapshot because VM213 had a
snapshot-delete lock. The play reports 116 successful tasks, 9 changed,
1 failed, no rescue and no unreachable host. No ownership transfer or reboot
executed. Original guest-225 IPv6 missed sequence 4954 during startup, between
replies around 09:09:13.857 and 09:09:16.142 UTC. Other channels had no new
misses. This is separate from prior misses 902 and 2085; its cause is unproved.

Recorder 15838 finished strictly at 09:15:16 UTC with status 0. Transit captured
4272 packets, AT&T 3148 and Webpass 1724; all report zero kernel drops.
Its observation started after the new missing probe. The captures do not
localize that loss. Continuous downstream observers remain active.

Read-only diagnosis proved mwan-watchdog-testbed.service PID1245262 created
known-good-20261001-020911 and attempted to prune an older snapshot. The
original deletion task stopped with a ZFS missing-snapshot error, but its
snapshot-delete lock persisted while the watchdog continued its two retention
passes. Empty active-task results between deletions did not prove completion.
The watchdog completed pruning and applied its existing stale-lock recovery
at 09:18:48 UTC. Subsequent reads confirm no lock, no active VM213 task and no
ZFS process. No agent unlocked the guest, changed a host service, changed
retention, or force-deleted a snapshot. Evidence is preserved under
real-cutover/snapshot-delete-lock in the MWAN-305 state directory.

Root verified clean primary HEAD equals fetched origin/main c802ebec and live
binary/network hashes remain 98a858c8/c8a32e91. The actual retry started at
09:21:43 UTC. Session 80192 is active; log deploy-mwan-20261001T092143Z.log
under configs-runs. Keep the primary checkout unchanged until it ends.
Recorder 54027 is active with a 1800-second bound, output
webpass-pd-unlocked-capture, phase pd-unlocked-capture-phase.txt and stop
pd-unlocked-capture.stop under real-cutover. Root owns all live mutations
and capture controls. The delegated observer is read-only. Finish capture
strictly before the planned reboot. PR #599 remains unmerged until forward
acceptance. MWAN-519 and MWAN-521 remain InProgress. Production is unchanged.

## Renew downstream observation during the cutover retry

Deployment 80192 created its pre-deploy snapshot successfully. At 09:33 UTC,
the play remains active at reconnection after the asynchronous udev trigger.
Source exclusion and ownership transfer have not executed. The primary
checkout remains unchanged. Recorder 54027 remains active before exclusion.

New observer sessions 18926 and 5808 started around 09:26 UTC with 7200-second
bounds. Their output directories are webpass-cutover-client225 and
webpass-cutover-client226 under the real-cutover evidence directory. Both
guests have fresh IPv4 and IPv6 replies at 09:32 UTC. Preserve the original
observer sessions and established Webpass affinity streams until their
bounded runs end. The delegated observer now includes the new windows.

Affinity guest 225 IPv4 missed sequence 5285 before source exclusion. Replies
5284 and 5286 occurred at 09:26:38.478175 and 09:26:40.700349 UTC. The other
five original channels had no new misses through 09:26:58 UTC. This miss does
not establish an ownership-transfer failure; its cause remains unproved.

Independent read-only review confirmed the existing post-forward battery
requires a separate plan with actual verified executable and network hashes.
Preserve calibration, identities, mapping checks, and history requirements.
Do not run its route-deletion and daemon-restart operations during deployment.
Forward acquisition, reversal, reboot, and production readiness remain
unaccepted. Cutover remains the current workstream.

## Verify source release and observe replacement acquisition

Deployment 80192 passed both corrected source acquisition checks against the
live networkd connection. Excluded AT&T mapped HTTP, NPT edge, and both
downstream guests' IPv4 and IPv6 packet checks passed. The source-exclusion
restart selected backup from approximately 09:36:20.863 to 09:36:36.516 UTC.
All ten observed channels continued replying during that interval.

The external-owner restart selected backup from approximately 09:39:12.570
to 09:39:52.700 UTC. All ten channels continued replying. The deployment
then passed the actual previous-owner release check and recorded networkd
address and route removal checks. It installed the excluded MWAN replacement
and restarted the daemon. Backup selection during replacement startup lasted
approximately 09:45:58.219 to 09:46:15.473 UTC without new observed misses.

Live operational export at 09:46 UTC reports Webpass owner mwan, connection
and family application ready, valid IPv4 static/mapped assignments, and a
fresh DHCPv6 delegated prefix 3d06:bad:b01:2200::/56 acquired at
09:46:00.228034 UTC. Both families report routing and translation ready;
IPv6 translation uses 3d06:bad:b01:2200::/60. Preserve the full export in
real-cutover/mwan-acquisition-0946-operational.json. The play still needs its
replacement acquisition and downstream packet verdicts before selection.

Capture 54027 finished strictly at 09:44:05.468422 UTC with status 0. Transit
captured 23492 packets, AT&T 15856, and Webpass 9526. All kernel drop counts
are zero and all three recorded process-absence checks returned 0.
Continuation capture 4133 was active on all three interfaces by
09:43:22.648812 UTC, before stopping the first window. Its output is
webpass-pd-acquisition-capture, stop file pd-acquisition-capture.stop, and
phase file pd-unlocked-capture-phase.txt under real-cutover. Its 1800-second
deadline is approximately 10:13 UTC. Stop it strictly before planned reboot.

Original observer sessions 99158 and 76919 ended naturally around 09:46:35
UTC. Affinity observer 12936 and renewed observers 18926 and 5808 remain
active with fresh replies in both families at 09:47 UTC. Do not interpret
the original windows' terminal timestamps as an outage. No production
activation occurred. PR #599 remains unmerged until forward acceptance.

## Verify selection restoration before reboot

Deployment 80192 passed the MWAN replacement assignment check and acquired
AT&T/Webpass mapped HTTP and NPT edge replies. It restored target selection
and passed the corresponding selected-phase packet checks. The live installed
binary SHA256 is 98a858c8400e0cd67d2809d54e337b29f7d4a012c910aa47b44919e3352f011a;
network SHA256 is 50c5db1725b57814dcedff43baea84f263fa10f86f1126a1ebc2f76b198a192f.
Both match the reviewed release and rendered intent. Operational export
selection-restored-0953-operational.json reports Webpass owner mwan and all
six other connections networkd, including AT&T, management, and transit.

Capture 4133 finished strictly at 09:51:49.806110 UTC with status 0. Transit
captured 6314 packets, AT&T 6668, and Webpass 288. All kernel drop counts are
zero and all three recorded process-absence checks returned 0. No guest
capture remains active before the planned reboot.

Guest 225's affinity and renewed local probe output froze at approximately
09:48:07 UTC. Independent packet inspection proves later requests and replies
for IPv4 IDs 16105 and 53124 and IPv6 IDs 26647 and 26660 on transit and AT&T,
including replies after 09:50 UTC. Every captured request has a matching
reply. Each stream omits 16 consecutive sequences from the primary-only
capture; backup forwarding or loss during those omissions remains unproved.
Do not classify frozen output as a sustained packet outage or claim zero
end-to-end loss. Fresh bounded guest-225 probes passed 3 of 3 in each family.

Fresh guest-225 observer 21087 started around 09:53 UTC with a separate SSH
ControlPath and a 7200-second bound. Its output is webpass-reboot-client225.
Both families have fresh replies. Working guest-226 observer 5808 remains
active. Preserve old windows as incomplete evidence and do not interrupt
their processes. Use the fresh windows for reboot observation.

Independent review of the first completed capture proves established Webpass
forwarding after exclusion: IDs 16105 and 26647 retained mark 2 and had paired
transit/provider requests, replies, and guest output. Health probes were
excluded. The isolated pre-exclusion missing sequence 5285 has no packet on
any recorded interface; adjacent sequences are paired. Its cause is unknown.

The separate post-forward plan shared-plan-ff37bec-post-forward.json changes
only the verified executable and network hashes. Root verified that structured
comparison. Do not start its fault and restart operations during this play.
At 09:55 UTC, deployment remains active in management/transit verification.
Reboot, complete acceptance battery, reversal, and second forward transfer
remain required. PR #599 remains unmerged. Production remains unchanged.

## Reject cold-boot acceptance and repair default link activation

Deployment 80192 completed at approximately 09:58 UTC with exit status 0:
379 successful tasks, 45 changed, no failure, unreachable host, or rescue.
The deploy gate accepted reboot, egress, and mapped-address checks. Boot ID
changed from 204ae526-a4d8-4ee1-8c4e-11a575a3e546 to
6dd73ec5-6b17-46ae-a87a-83e11034ad61. Both downstream guests used backup from
approximately 09:57:07 to 09:58:01 UTC and returned to primary. The fresh
guest-225 and working guest-226 windows recorded no missing sequences in
that reboot interval. Installed binary and network hashes remain unchanged.

The overall deploy verdict does not establish Webpass cold-boot acceptance.
Live exports postboot-0958-operational.json and postboot-0959-operational.json
report Webpass routing not ready, IPv4 application failed with network is
unreachable, and IPv6 acquisition pending. Actual enwebpass0 is DOWN with its
five IPv4 addresses, only local routes, and no IPv6 link-local address.
IPv6 is enabled with EUI-64 generation configured. Networkd correctly reports
the transferred link unmanaged. DHCPv6 recovery repeatedly reports no matching
address for the interface. AT&T continues serving downstream packets.

The deployed cold-failure-network.json omits the enabled leaf. Source review
proved applyLinkEnabled skipped writes when the optional field was nil,
although the public schema defaults enabled to true. The earlier warm
transfer inherited an already enabled link from networkd. Do not activate
the post-forward battery or claim production readiness from this failure.

The narrow runtime correction is signed 5d002265f133731120cead09886b2b636b6b70f8
on codex/mwan-519-cold-physical-link in the reused excluded-forwarding worktree.
Only owned_links.go and the existing public daemon link regression changed.
The unchanged runtime failed its initially DOWN link assertion. The corrected
runtime passed in 2.006 seconds; independent replay passed in 1.880 seconds.
Real packet delivery, event-driven repair, and unchanged foreign link state
passed. Local Linux check/test and signature verification passed. Independent
exact-source review found no blocking defect. The negative-control artifact
was verified, but its deleted source variant was not independently replayed.
MWAN PR #156 is published; required CI and release publication remain pending.
Actual physical cold boot and DHCPv6 recovery remain required.

Recovery PR #599 is rebased onto c802ebec at signed 04896cba. Both environment
renders, released network/firewall validation, exact client identity/mapping
checks, and lint passed independently. Required CI passed and no review
threads are present. The source acquisition gate requires usable current
assignments. The recovery deployment requires the published link correction.
Preserve that ordering. Prepare the merged release pin before deploying the
networkd reversal. MWAN-519 and MWAN-521 remain InProgress; production is
unchanged.

## Continue cold-boot recovery before further cutover

PR #156's required checks passed at 5d002265. The privileged namespace job
110320734155 failed TestOwnedLinksCreateRestartAndRemove: the kernel rejected
raising owned-vlan over its disabled external parent. The implementer verified
that the existing fixture never enabled that parent. A focused fixture
correction and affected privileged validation are in progress. Firewall job
110320734147 also failed; independent diagnosis is pending. Do not dismiss
either failure or deploy the candidate. The existing GoBGP vulnerability
check failure is unrelated to this patch.

Recovery PR #599 merged normally as 1ea637962de2959baf550068b608f737906364e1
at 10:22:13 UTC after required checks, signature verification, and independent
review passed. No recovery deployment occurred. The deployment still requires
the published link correction and merged testbed release pin. Both downstream
guests have fresh IPv4 and IPv6 replies at this checkpoint. Webpass cold boot,
reverse transfer, repeated forward transfer, and full acceptance remain
incomplete. Production remains unchanged.

## Publish the cold-boot correction and renew guest observation

PR #156 merged normally as 2df8faa0cb9ebe306c50fbfeb615bceacdde9d62
at 10:29:10 UTC. Signed final head e533c84 passed all ten required checks,
privileged namespace and ARM64 checks, Graphite review, and independent
review with no blocking findings or unresolved threads. The fixture enables
three external parents before creating their VLANs; runtime code does not
change external ownership. Local full privileged namespace checks and builder
check/test passed. The firewall IPv4 UDP timeout also occurred on prior merged
main cc928cf; its precise packet failure remains unexplained. The new source
push started another firewall run. Do not claim every optional check passed.

Release workflow 36849430846 is queued for exact merge 2df8faa0. The testbed
release pin is not yet edited. Recovery deployment remains pending published
artifact verification and a merged compatible pin.

Guest 226's original window recorded 3502 replies per family without a missing
sequence before both ping SSH commands exited 255 at 10:24:46.857 UTC. The
observer still samples routes, but guest inspection confirms its ping
processes ended. This output gap does not establish network loss. Fresh
guest-226 observer 1403 uses the existing independent reboot SSH configuration
and output webpass-recovery-client226. Guest-225 observer 21087 remains active.
No packet capture or deployment is active. Webpass remains DOWN; AT&T serves
downstream traffic. Preserve terminal windows and incomplete evidence.

## Deploy the merged cold-boot recovery pair

Published release 202610011030-99-2df8faa passed all four archive checksum,
GitHub digest, hosted-package attestation, and exact-source checks. Its actual
Linux ARM64 executable passed version/schema and both environment network and
firewall validators in ARM64 Docker containers. Extracted AMD64 executable
SHA256 is 599392d397367743432fa6c2c642dbb4b2d32b06e0bc9529d3b73d8b47965127.
No direct macOS execution of the Linux artifact succeeded or is required.

Testbed pin PR #603 merged normally as 829af8d18f2de0884a7f82390376006833945a2c
at 10:43:34 UTC after all three required checks and independent mechanical
review passed. Only the testbed MWAN release tag and archive checksum changed.
Clean primary Configs main matches that merge. Recovery deployment 4497 began
at 10:43:55 UTC through ./configsctl deploy deploy-mwan --limit
mwan_suburban_servers. Its log is deploy-mwan-20261001T104355Z.log under the
existing configs-runs directory. Preserve that checkout until the play ends.

Pre-deploy binary/network hashes still matched 98a858c8 and 50c5db17. Packet
capture 23045 failed before any phase at 10:42:32 UTC: tcpdump rejected the
DOWN Webpass interface. Both successfully started captures stopped, recorded
zero-drop counters, and passed process-absence checks. The Webpass unit was
not loaded and had no PID. Preserve the failed capture report; start a new
window only after the corrected daemon enables Webpass. Downstream observers
21087 and 1403 remain active with fresh replies in both families.

The repeated-forward configuration is prepared and independently reviewed,
but remains unpublished. Its initial signed head 82db9134 restores only the
previously accepted Webpass intent and owner assertion. Both environment
renders and published validators passed. Rebase it onto the merged release
pin before publication. Actual networkd reversal must pass before repeating
forward transfer. Recovery, cold boot, and full acceptance are still pending;
production remains unchanged.

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
