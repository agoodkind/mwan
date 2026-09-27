# MWAN-305 execution ledger

Record implementation and deployment evidence against the
[migration plan](2026-09-26-link-ownership.md). Update this file after each
completed PR or deployment phase and before a context handoff. Preserve exact
commands, commit and configuration revisions, observed results, failed
checks, and remaining work. Never mark a runtime result complete from a
documentation change.

## Current work

September 26, 2026: The specification records the approved migration design.
The implementation plan assigns all 15 remaining work packages. Tack contains
their parent and dependency relationships. This planning work changed no
runtime code and performed no deployment.

The operator subsequently confirmed AT&T will retire and delegated the
migration choice. Omit 802.1X integration from the new manager. Preserve the
existing AT&T setup until retirement is confirmed. Generic VLAN support
remains required. MWAN-400 must verify that AT&T no longer requires networkd
before removing it globally. No live service was changed.

| Ticket | Implementation | Testbed acceptance | Production acceptance |
| --- | --- | --- | --- |
| MWAN-516 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-397 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-523 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-398 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-227 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-517 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-518 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-505 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-521 | Work has not started. | Evidence is pending. | Evidence is pending. |
| MWAN-522 | Work has not started. | Evidence is pending. | The suite supplies evidence for deployment tickets. |
| MWAN-519 | Work has not started. | Evidence is pending. | MWAN-520 records phase acceptance. |
| MWAN-399 | Work has not started. | Evidence is pending. | MWAN-520 records phase acceptance. |
| MWAN-400 | Work has not started. | Evidence is pending. | MWAN-520 records final acceptance. |
| MWAN-401 | This ticket executes final testbed acceptance. | Evidence is pending. | MWAN-520 records final acceptance. |
| MWAN-520 | This ticket executes production acceptance. | Each promoted phase needs its own evidence. | Evidence is pending. |

## Record each implementation result

1. Record the ticket, PR, signed commit, merge result, and exact checks.
2. State the observable behavior demonstrated and any missing acceptance.
3. Identify the next unfinished task and its prerequisites.

## Record each deployment result

1. Record the environment, release, configuration revision, and connection.
2. Record ownership before and after, deployment commands, and reconnect result.
3. Record downstream packet checks, balancing observations, interruption, and
   recovery times separately for each address family.
4. Record reverse-transfer evidence and the recovery release/configuration.
5. Update the relevant ticket only to the state supported by that evidence.
