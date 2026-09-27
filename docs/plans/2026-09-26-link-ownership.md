# Coordinate the MWAN-305 interface migration

Complete the interface ownership migration using the slice plans below.
The [interface specification](../interfaces.md) defines approved behavior.
This plan defines execution order, agent responsibilities, and acceptance
gates. Each slice plan defines its edits and verification. The
[execution ledger](2026-09-26-link-ownership-ledger.md) records actual progress.

The inspected baselines are MWAN `03dd43a` and Configs `52056ec`.
Recheck changed source before delegation. These plans authorize no claim
that implementation, review, or live acceptance has already happened.

## Assign work by responsibility

| Agent | Assigned work | Required handoff |
| --- | --- | --- |
| Coordinator with high reasoning capability | Verify prerequisites, settle interfaces within the approved design, assign exclusive file ownership, sequence PRs, and reconcile evidence. | Give each agent one slice, exact source revision, settled contracts, dependencies, and acceptance requirements. |
| Code implementer only | Implement the settled slice, add necessary public-boundary tests, and run local checks. Verify the plan's premise against current source first. | Return the patch, exact checks and results, public behavior proved, and unresolved contradictions. Do not deploy, decide architecture, or substitute self-review for independent review. |
| Independent reviewer with high reasoning capability | Review contracts before implementation and inspect the resulting patch afterward. Reproduce the slice's failure cases and check shared consumers. | Return findings with evidence and a verdict tied to the reviewed commit. Report missing proof explicitly. |
| Cutover agent with high reasoning capability | Inspect live ownership, execute the approved merged deployment, monitor downstream traffic, perform recovery, and record acceptance. | Return exact release and configuration revisions, commands, before/after owners, interruption, recovery, and packet results. |

Use the code-implementer role only after contract review has settled the
behavior and interfaces. Do not assign open architecture questions,
investigation, review, or live networking to that role. A contradiction in
current source returns to the coordinator before further edits. A change to
an approved product decision requires the operator's decision.

An independent reviewer must not review their own implementation. The
coordinator handles focused PR submission and merge using the applicable PR
workflow. Use Graphite for dependent stacks and the individual PR workflow
for a standalone PR. Preserve required AI reviews and resolve review findings.

## Preserve the approved boundaries

Transfer one complete connection at a time. Use fresh DHCP negotiation for
the first transfer with preserved client identity. Recover subsequent
restarts from MWAN-owned persistent state with protocol validation. Do not
import networkd lease files.

Use Linux for router discovery and SLAAC, which configures IPv6 addresses
from router advertisements. Use one MWAN DHCPv6 lifecycle per connection
for interface addresses and delegated prefixes.

Preserve translation, live-prefix source routing, steering, internal BGP,
and MWAN-341 firewall startup protection. MWAN-507 adds external BGP and
tunnels through the shared contracts. It remains a separate epic and does
not depend on global networkd retirement.

Leave AT&T's existing authentication and networkd setup unchanged while the
circuit remains required. Implement generic VLAN support without 802.1X
integration. Confirm AT&T retirement before removing its dependencies.
Do not cancel service or delete reusable credentials.

## Use six work plans

| Plan | Work covered | Execution |
| --- | --- | --- |
| [Model and observation](interfaces/model.md) | MWAN-516 configuration and state; MWAN-523 kernel observation. | Review contracts, then assign code implementers. |
| [Links, addresses, and route repair](interfaces/kernel.md) | MWAN-397 links; MWAN-398 static addressing; MWAN-505 route repair. | Assign code implementers after the model contracts pass review. |
| [Address acquisition](interfaces/acquisition.md) | MWAN-398 DHCPv4; MWAN-227 delegation; MWAN-517 kernel IPv6 and DHCPv6 addresses. | Assign separate implementers to independent protocols; share one DHCPv6 client. |
| [Restart recovery](interfaces/restart.md) | MWAN-518 persistent assignments and restart. | Review protocol recovery, then assign a code implementer. |
| [Deployment and acceptance tooling](interfaces/deployment.md) | MWAN-521 Configs integration; MWAN-522 real protocol and downstream tests. | Assign code implementers; reserve live provisioning and validation for cutover agents. |
| [Cutover and retirement](interfaces/cutover.md) | MWAN-519 first connection; MWAN-399 remaining interfaces; MWAN-400 retirement; MWAN-401 and MWAN-520 final acceptance. | Assign cutover agents; use bounded code implementers for reviewed removal patches. |

Each plan groups related tasks. The PR map below preserves one logical
change per PR. A plan is not a PR or a requirement to execute every task
serially.

## Define PR and Graphite boundaries

Use independent branches from current remote trunk by default. A Graphite
stack contains only unmerged PRs with a real code dependency, in one
repository. End each stack at the boundary below and merge its prerequisites
before starting the next stack. Cross-repository release dependencies use
pinned revisions and required checks, not a Graphite parent relationship.

These are planned PR subjects and relationships. Recheck existing PRs before
creation and reuse a matching PR. Keep regression tests with their behavior.
Split further only when each resulting PR remains usable and passes required
checks at its own position.

| Repository | PR subject | Branch relationship and boundary |
| --- | --- | --- |
| MWAN | [MWAN-516] Define shared interface configuration and assignment contracts | Start the model stack from trunk. |
| MWAN | [MWAN-523] Observe device identity and kernel assignment state | Stack on the model PR. |
| MWAN | [MWAN-516] Publish connection ownership and acquisition history | Stack on observation. End and merge the model stack here. |
| MWAN | [MWAN-397] Manage physical links and VLAN dependencies | Start the kernel stack after the model stack merges. |
| MWAN | [MWAN-398] Reconcile owned addresses and acquired routes | Stack on links. End and merge the kernel stack here. |
| MWAN | [MWAN-505] Restore deleted owned routes | Use a standalone PR for the existing defect. Integrate any new observation API after it merges. |
| MWAN | [MWAN-398] Complete DHCPv4 acquisition and lease replacement | Use a standalone PR after the kernel stack merges. |
| MWAN | [MWAN-517] Configure and observe kernel IPv6 acquisition | Use a sibling standalone PR after the kernel stack merges. |
| MWAN | [MWAN-227] Acquire delegated prefixes through one DHCPv6 lifecycle | Start the DHCPv6 stack after the kernel stack merges. |
| MWAN | [MWAN-517] Add interface assignments to the DHCPv6 client | Stack on delegation after kernel IPv6 acquisition merges. End the DHCPv6 stack here. |
| MWAN | [MWAN-518] Recover persisted assignments after restart | Use a standalone PR after acquisition implementations merge. |
| Configs | [MWAN-521] Render complete interface roles and ownership | Start the deployment stack after the model contract merges. Keep default ownership unchanged. |
| Configs | [MWAN-521] Implement exclusive ownership transfer and recovery | Stack on rendering. End the deployment stack here; gate activation on the complete application release. |
| Configs | [MWAN-522] Configure protocol lifecycle scenarios in ISP simulators | Use an independent PR. Preserve existing simulator defaults. |
| MWAN | [MWAN-522] Run privileged daemon acceptance through public boundaries | Bootstrap the runner in a standalone PR after the model merges and before protocol PR acceptance. Add each feature's scenarios in its own PR. |
| Configs | [MWAN-522] Verify downstream forwarding and balancing during migration | Use a standalone PR after deployment integration and simulator changes merge. |
| MWAN | [MWAN-400] Remove retired networkd application dependencies | Use a standalone PR after the retirement inventory verifies removal scope. |
| Configs | [MWAN-400] Remove retired networkd deployment dependencies | Use a separate standalone PR after that inventory; pin the compatible MWAN release. |

Live acceptance phases are operational work, not placeholder PRs. A required
ownership configuration change gets its own focused Configs PR for that phase.
Do not combine several providers' activation changes merely because the
underlying code is ready.

Before stack mutations, use Graphite MCP with the explicit worktree path to
inspect state and parents. Fetch current remote trunk. Update local trunk
only from its clean, idle owning worktree, then restack the scoped branch
chain. Never move a branch checked out elsewhere.

Enable signed commits and signed rebases before creating or restacking.
Use Graphite MCP for stack creation, modification, submission, and merging.
Verify signatures after rewrites. Dry-run submission and merge; inspect
each GitHub base branch. Review every PR's active ruleset, checks, approvals,
and unresolved threads before merging from the bottom upward. Use the
ordinary GitHub workflow for standalone PRs.

## Parallelize independent work

| Stage | Work that can run concurrently | Completion required before the next dependent stage |
| --- | --- | --- |
| Contract review | Inspect configuration, kernel operations, protocols, and deployment sources concurrently. | Approve 516-model interfaces before implementers use them. |
| Model implementation | Implement the model stack serially. Prepare simulator configuration and reproduce route repair independently. | Merge the model stack. |
| Kernel implementation | Implement the kernel stack while Configs rendering and the daemon test runner develop against the merged model. | Merge kernel address ownership and runner bootstrap before protocol PR acceptance. |
| Acquisition | Implement DHCPv4, kernel IPv6, and DHCPv6 delegation in separate lanes. | Merge kernel IPv6 before the DHCPv6 interface-address PR; finish all acquisition before persistence acceptance. |
| Integration | Implement persistence, deployment mechanics, and acceptance tooling where files do not overlap. | Review and merge all required code and pin compatible revisions before first transfer. |
| Live migration | Review the next phase's evidence and prepare documentation while one cutover agent controls network mutation. | Complete testbed and production acceptance for the current phase before the next live transfer. |

Parallel work requires separate file ownership. Changes to shared daemon
startup, assignment types, operational state, and Configs deployment order
must be serialized as specified below. Design review can proceed in parallel
with unrelated implementation; a slice's final review requires its patch.

Keep one cutover agent responsible for a live gateway at a time. Do not
overlap provider transfers, simulator changes, or production deployment
against the environment being measured.

MWAN-520 executes after each testbed phase. Its ticket dependency on MWAN-401
controls final completion, not the first production phase. Final acceptance
follows remaining transfers, confirmed AT&T retirement, networkd removal,
and MWAN-401. A future AT&T retirement date does not block earlier code or
unrelated connection transfers.

Prepare removal code before cutover where the source inventory is sufficient.
Do not activate global removal until MWAN-400's live prerequisites pass.
Final removal patches may depend on the verified post-transfer inventory;
review and merge those patches before deployment.

## Serialize edits to shared sources

Assign one writer at a time to each shared file. Parallel agents may prepare
independent work against an agreed interface, but must not independently
redesign it.

| Shared source or contract | Coordination requirement |
| --- | --- |
| Network loader, served model, schema, and assignment types | Finish 516-model contract review before consumers edit against it. |
| Kernel monitor and event types | Let 523-observation own event changes; coordinate 505-route-repair consumers. |
| Interface-manager startup and daemon lifecycle | Serialize integration changes from links, acquisition, state, and persistence. |
| Address and route ownership | Review 398-addresses with WAN routing and OOB consumers before moving writes. |
| DHCPv6 lifecycle | Let 227-delegation establish the client; 517-dhcpv6 extends it. Do not create two clients for one connection. |
| Operational state and failure history | Let 516-state define publication; protocol slices supply transitions through that contract. |
| Configs deployment playbook | Finish discovery and rendering order before integrating ownership transfer. |
| Acceptance runner and simulator configuration | Let 522-acceptance own the command manifest; protocol slices contribute real scenarios. |

Recheck and reconcile changed shared files before submission. Never revert
another agent's work to make a slice compile.

## Review behavior through public boundaries

Run existing project checks appropriate to each code change. On macOS, the
current MWAN gate is `make docker-make TARGETS="check test"`.
Run the additional privileged protocol suite explicitly. Ordinary compilation
or a skipped privileged test does not prove kernel or protocol behavior.

Use the production configuration loader, running daemon, operational
interface, real DHCP servers, router advertisements, kernel state, and
downstream packet delivery. Add the smallest test that detects the changed
public behavior. Do not add mock-heavy unit tests, static document checks,
or tests that repeat implementation details.

Require review of ownership exclusion, object removal, assignment deadlines,
provider independence, restart behavior, and preserved translation/firewall
consumers. Each slice adds its specific attack cases. Review the exact commit
that will merge and repeat affected checks after fixes.

MWAN-522 must publish executable acceptance commands and expected observations
before a cutover agent begins. Do not invent missing runner commands or
substitute hand inspection for an unimplemented required test.

## Execute live phases

1. Verify the exact merged application release and Configs revision, current
   ownership, another usable provider where applicable, and working
   hypervisor recovery. Check the current task's deployment authorization.
2. Establish continuous downstream guest probes without an OOB bypass.
   Record IPv4 and IPv6 independently, plus configured inbound mappings and
   provider traffic needed to prove new-connection balancing.
3. Deploy the phase to the testbed through `./configsctl deploy` with the
   target and command from its slice plan. Treat simulator provisioning and
   deployment check mode as live operations.
4. Run the phase's full acceptance battery, exclusive reverse transfer,
   forward transfer, and required restart or reboot checks. Measure actual
   interruption and recovery.
5. Execute MWAN-520 for that phase using the accepted revisions. Verify live
   downstream traffic throughout production deployment and afterward.
6. Update the ledger and relevant tickets from the observed results before
   starting the next phase.

Transfer providers first, transit next, and management last. Preserve the
failover LXC's separate networking. Keep networkd for every unmigrated
connection that requires it.

A failed gate stops promotion. Use the prepared recovery procedure if live
forwarding or access fails. Do not improvise an unreviewed live fix. Reproduce
the defect, fix and review it in code, merge, and repeat affected testbed
acceptance before production.

## Maintain the execution ledger

Update the ledger after each review, merge, deployment, and context handoff.
Record each slice's exact branch, PR, commit, source/configuration revisions,
checks, evidence, next task, and unresolved dependency. Distinguish written
code, passing local checks, independent review, merge, testbed acceptance,
and production acceptance.

For each live phase, record owner before and after, protocol identity,
assignment changes, reconnect result, downstream packets, balancing,
interruption, recovery, and reverse-transfer evidence. Keep credentials out
of logs and documentation. Record command failures and skipped checks.

Close MWAN-305 only after all slice acceptance requirements, final MWAN-401
testbed acceptance, and final MWAN-520 production acceptance pass. Preserve
completed historical tickets and leave MWAN-507 separate.
