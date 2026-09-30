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
