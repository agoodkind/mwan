# MWAN-341 acceptance record

The first testbed and production deployments used merged MWAN main `ab7543a`
and merged Configs main `d2aeefe2`. A second testbed deploy used MWAN
`ff82fbc` and Configs `b53bcb7` to verify the corrected downstream gate.
Production still runs the first release. Ticket reconciliation remains pending.

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

Exact execution times and command output locations for the first testbed
checks were not supplied. The [implementation plan](plans/2026-09-26-mwan-341-firewall.md)
includes MWAN-341 checks that are not recorded here. It assigns direct BGP,
tunnel, and packet-size integrated tests to MWAN-507.

The suburban host routes `1.1.1.1` through `10.240.0.1` on `vmbr0`, outside
the MWAN path. The corrected gate runs its route and ping checks inside
OPNsense VM 201. During the second deployment, the downstream observer first
failed at 10:31:20 UTC for IPv4 and 10:31:17 UTC for IPv6. IPv4 resumed at
10:32:04 UTC and IPv6 at 10:32:02 UTC. The controlled translation fault later
produced nine IPv4 observer failures while all 25 IPv6 probes succeeded in
that window. After removal, 63 probes per family succeeded without failure.

## Production results

| Check | Recorded result |
| --- | --- |
| Configs check mode | `ok=184 failed=0`. |
| Live deployment | The merged-main deploy to VM 113 reported `ok=242 changed=33 failed=0`. |
| Installed gateway | MWAN `ab7543a` was installed. `ifmgr` was active, `nftables.service` was masked and inactive, and `inspect-firewall` passed. |
| Egress and providers | The existing host-side gate reported `ipv4=yes ipv6=yes`. All three providers were healthy. The separate downstream checks below verified traffic through the gateway. |
| Gateway connectivity | `mwan debug connectivity` reported P4, P6, and NPT OK for AT&T, Monkeybrains, and Webpass. |
| Fresh downstream connections | Twenty of twenty IPv4 and twenty of twenty IPv6 requests succeeded after deployment. |
| Provider source addresses | Successful `ifconfig.co` replies showed 9 AT&T and 9 Webpass IPv4 sources, and 9 AT&T and 7 Webpass IPv6 sources. Other requests returned HTTP 429 rate limits. The counts do not establish the full selection distribution. |

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

Production still uses the first host-side gate. The corrected release requires
a production check run, deployment, downstream acceptance, and ticket
reconciliation before MWAN-341 closure.
