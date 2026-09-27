# MWAN-341 acceptance record

The testbed deployment from merged MWAN main `ab7543a` and merged Configs
main `d2aeefe2` completed. The results below are testbed observations.
DSCP verification and production deployment and acceptance remain pending.

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
| Clean-room review | The 31-rule review reported B0/SF0/N1. |

The reported results do not include exact execution times, command output
locations, or a DSCP result. The remaining acceptance checks in the
[implementation plan](plans/2026-09-26-mwan-341-firewall.md) have no result
recorded here unless listed above.

## Production status

Production deployment and acceptance have not been recorded. The production
result must use the accepted release and report downstream IPv4 and IPv6,
ruleset state, provider health, and measured interruption after deployment.
