# MWAN-341 acceptance record

The first testbed and production deployments used MWAN `ab7543a` and Configs
`d2aeefe2`. The current testbed and production gateways run MWAN `f61a4d7`
from release `202609271118-2c-f61a4d7`. Configs `b612d1c8` deployed the
release and the restart-handler correction. The five implementation tickets
are Done in Tack. MWAN-341 remains In Progress until its ledger merges.

## Testbed results

| Check | Recorded result |
| --- | --- |
| Fresh-flow provider selection | IPv4 selected the two providers at 55/45. IPv6 selected them at 49/51. |
| AT&T provider pin | 20/20 attempts selected AT&T. |
| NPT hairpin traffic | Pings succeeded 3/3 times for each tested internal path. |
| Daemon stop and restart | Recovery passed. |
| nftables flush | Rule recovery passed. |
| Invalid configuration | The gateway rejected it. |
| Routed fallback | Fallback succeeded after approximately ten minutes of downstream outage. |
| Missing-link boot | The gateway recovered after reboot. |
| DSCP check and deployment | Configs `d2aeefe2` check mode reported `ok=171 failed=0`. The live deploy reported `ok=221 failed=0`. |
| DSCP packets | Twenty IPv4 and twenty IPv6 requests marked CS1 succeeded. AT&T simulator ingress captured all 40 unique paths; Webpass captured zero. |
| Observer after reboot | The downstream observer recorded 194 probes per family with zero failures. |
| Clean-room review | The 31-rule review reported B0/SF0/N1. |
| First deployed egress gate | The host probe passed, but it did not test testbed MWAN translation. |
| Corrected gate deploy | Configs `b53bcb7` check mode reported `ok=175 failed=0`. The live deploy reported `ok=223 failed=0`. The reboot, egress, and mapped-address verdict codes were all zero. |
| Corrected gate probes | The released `ff82fbc` command passed with `ipv6=yes ipv4=yes` before and after reboot. An incorrect expected IPv4 next hop returned `ipv6=yes ipv4=no` and a nonzero exit. |
| Broken IPv4 translation | A temporary rule translated only OPNsense source `10.240.240.2` traffic to `1.1.1.1` into `192.0.2.1`. The gate returned `ipv6=yes ipv4=no` and a nonzero exit. The rule was removed, its cleanup timer was stopped, and the gate returned `ipv6=yes ipv4=yes`. |
| Firewall after reboot | `mwan-ifmgr@wan` was active, `nftables.service` was masked, and `inspect-firewall` matched the intended rules to the kernel. |
| Fresh flows after the corrected deploy | Thirty-two IPv4 and thirty-two IPv6 HTTPS connections succeeded from downstream guest 225. Each family selected Webpass 18 times and AT&T 14 times in ISP bridge captures. All four captures reported zero dropped packets. |
| HTTPS gate release | MWAN PR #47 merged as `f61a4d7`. The testbed check run on Configs `3394d5ec` reported `ok=175 failed=0`; its live deploy reported `ok=223 failed=0`. Trace `20260927-044213-deploy-44032` returned zero for reboot, egress, and owned addresses. A fresh gate check returned `ipv6=yes ipv4=yes`. |
| HTTPS gate fault | A timed, temporary nftables rule changed only OPNsense source `10.240.240.2` traffic to `1.1.1.1` into source `192.0.2.1`. The HTTPS gate returned `ipv6=yes ipv4=no` and exit 1. After removal and timer stop, it returned `ipv6=yes ipv4=yes`; the temporary table was absent. |
| HTTPS release load balancing | Guest 225 completed 32 IPv4 and 32 IPv6 HTTPS requests. ISP captures counted Webpass 17 and AT&T 15 IPv4 connection starts, and Webpass 18 and AT&T 14 IPv6 starts. |
| Restart handler correction | Configs PR #526 merged as `b612d1c8`. A testbed config newline forced the handler. Check mode reported `ok=174 failed=0`; the live play reported `ok=227 failed=0`. Ansible recorded the asynchronous restart, reconnect, and completed job. Trace `20260927-052653-deploy-480650` returned zero for reboot, egress, and owned addresses. A fresh gate check and guest 225 passed IPv4 and IPv6 after reboot. |

Exact execution times and command output locations for the first testbed
checks were not supplied. The [implementation plan](plans/2026-09-26-mwan-341-firewall.md)
includes MWAN-341 checks that are not recorded here. It assigns direct BGP,
tunnel, and packet-size integrated tests to MWAN-507.

The suburban host routes `1.1.1.1` through `10.240.0.1` on `vmbr0`, outside
the MWAN path. The current gate checks the OPNsense route and makes a
source-bound HTTPS request inside VM 201. During the second deployment, the
downstream observer first failed at 10:31:20 UTC for IPv4 and 10:31:17 UTC
for IPv6. IPv4 resumed at 10:32:04 UTC and IPv6 at 10:32:02 UTC. The
controlled translation fault later
produced nine IPv4 observer failures while all 25 IPv6 probes succeeded in
that window. After removal, 63 probes per family succeeded without failure.

## Production results

| Check | Recorded result |
| --- | --- |
| Initial Configs check mode | `ok=184 failed=0`. |
| Initial live deployment | The merged-main deploy to VM 113 reported `ok=242 changed=33 failed=0`. |
| Initial installed gateway | MWAN `ab7543a` was installed. `ifmgr` was active, `nftables.service` was masked and inactive, and `inspect-firewall` passed. |
| Initial egress and providers | The host-side gate reported `ipv4=yes ipv6=yes`. All three providers were healthy. The separate downstream checks below verified traffic through the gateway. |
| Initial gateway connectivity | `mwan debug connectivity` reported P4, P6, and NPT OK for AT&T, Monkeybrains, and Webpass. |
| Initial downstream connections | Twenty of twenty IPv4 and twenty of twenty IPv6 requests succeeded after deployment. |
| Initial provider source addresses | Successful `ifconfig.co` replies showed 9 AT&T and 9 Webpass IPv4 sources, and 9 AT&T and 7 Webpass IPv6 sources. Other requests returned HTTP 429 rate limits. The counts do not establish the full selection distribution. |
| HTTPS gate predeploy stop | The first live attempt from Configs `b53bcb7` stopped before snapshot or reboot at `ok=111 failed=1`: source-bound IPv6 ICMP was intermittent while downstream HTTPS worked. The source-bound HTTPS gate replaced ICMP in MWAN `f61a4d7`. |
| Interrupted production run | Configs `3394d5ec` check mode reported `ok=186 failed=0`. Its live play stopped at `ok=203 unreachable=1` while restarting `mwan-ifmgr@wan`; SSH reported “Network is unreachable.” VM 113 stayed running, the service and direct egress gate recovered, and downstream guest 102 recorded 3 of 246 IPv4 and 1 of 246 IPv6 checks failed from 11:58:39 to 12:03:00 UTC. No reboot occurred in that attempt. |
| Current production deployment | Configs `b612d1c8` check mode reported `ok=185 failed=0`; the merged-main live play reported `ok=235 changed=16 failed=0`. Trace `20260927-054315-deploy-58614` returned zero for reboot, egress, and owned addresses. The fresh gate returned `ipv6=yes ipv4=yes` with MWAN build `f61a4d7`. |
| Current firewall and providers | `mwan-ifmgr@wan` is active, `nftables.service` is masked, and `inspect-firewall` passed. `mwan debug connectivity` reported P4, P6, and NPT OK for AT&T, Monkeybrains, and Webpass. |
| Current downstream load balancing | UniFi LXC 102 completed 32 IPv4 and 32 IPv6 HTTPS requests. Gateway interface captures counted AT&T 15 and Webpass 17 IPv4 connection starts, and AT&T 21 and Webpass 11 IPv6 starts. |

The downstream observer on UniFi LXC 102 first recorded IPv4 failure at
08:55:33 UTC and IPv6 failure at 08:55:35 UTC during the reload. Failures
continued through 08:57:06 UTC for IPv4 and 08:57:08 UTC for IPv6 during
reboot. IPv4 recovered at 08:57:11 UTC and IPv6 at 08:57:12 UTC. From
08:57:12 through 08:59:29 UTC, it recorded 135 IPv4 and 136 IPv6
successes with zero failures.

The latest Cloudflare alert emails reported these pool transitions in UTC.
No live dashboard query was supplied.

| Pool | Unhealthy email | Healthy email |
| --- | --- | --- |
| `sf-webpass-1335` | 08:57:04 | 08:57:47 |
| `sf-att-1335` | 08:57:08 | 08:57:35 |
| `sf-1335-ipv6` | 08:57:10 | 08:57:40 |

During the current production reboot, UniFi LXC 102 recorded 9 IPv4 and 10
IPv6 failures from 12:45:34 through 12:46:16 UTC. From 12:46:17 through
12:48:30 UTC, it recorded 128 successes per family without failure. Cloudflare
emails reported `sf-att-1335` unhealthy at 12:46:24 and healthy at 12:47:08,
and `sf-webpass-1335` unhealthy at 12:46:25 and healthy at 12:47:03. No new
unhealthy email for `sf-1335-ipv6` appeared in that interval. These are email
times; no live dashboard query was supplied.
