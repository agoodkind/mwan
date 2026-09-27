# MWAN-305 execution ledger

Record implementation and deployment evidence against the
[coordination plan](2026-09-26-link-ownership.md). Update this file after each
completed PR or deployment phase and before a context handoff. Preserve exact
commands, commit and configuration revisions, observed results, failed
checks, and remaining work. Never mark a runtime result complete from a
documentation change.

## Current work

September 26, 2026: The specification records the approved migration design.
The coordination plan groups 15 tickets into six work plans. Their tasks have
implementation or operational instructions and explicit review requirements.
Tack contains the parent and ticket dependency relationships. Slice ordering
comes from the coordination plan. This planning work changed no runtime code
and performed no deployment.

The operator subsequently confirmed AT&T will retire and delegated the
migration choice. Omit 802.1X integration from the new manager. Preserve the
existing AT&T setup until retirement is confirmed. Generic VLAN support
remains required. MWAN-400 must verify that AT&T no longer requires networkd
before removing it globally. No live service was changed.

## Track slice execution

Every slice below is planned. No slice has implementation, independent
review, merge, or live acceptance evidence from this planning work.

| Ticket | Slices | Current execution state |
| --- | --- | --- |
| MWAN-516 | 516-model; 516-state | Execution has not started. |
| MWAN-397 | 397-links | Execution has not started. |
| MWAN-523 | 523-observation | Execution has not started. |
| MWAN-398 | 398-addresses; 398-dhcpv4 | Execution has not started. |
| MWAN-227 | 227-delegation | Execution has not started. |
| MWAN-517 | 517-autoconfiguration; 517-dhcpv6 | Execution has not started. |
| MWAN-518 | 518-restart | Execution has not started. |
| MWAN-505 | 505-route-repair | Execution has not started. |
| MWAN-521 | 521-configuration; 521-deployment | Execution has not started. |
| MWAN-522 | 522-acceptance | Execution has not started. |
| MWAN-519 | 519-first-connection | Execution has not started. |
| MWAN-399 | 399-remaining-connections | Execution has not started. |
| MWAN-400 | 400-retirement | Execution has not started. |
| MWAN-401 | 401-testbed | Execution has not started. |
| MWAN-520 | 520-production | Execution has not started. |

## Resume the work

The next task is independent contract review for 516-model against current
MWAN and Configs source. No implementer or cutover agent has an execution
assignment from this planning update.

For every handoff, record the slice, agent responsibility, exact source
revision, agreed interfaces, owned files, current PR, last passing check,
remaining acceptance, next action, and prerequisite evidence.

## Record each implementation result

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
