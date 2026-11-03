# Coordinate the MWAN-305 interface migration

Complete the interface ownership migration using the slice plans below.
The [interface specification](../interfaces.md) defines approved behavior.
This plan defines execution order, agent responsibilities, and acceptance
gates. Each slice plan defines its edits and verification. The
[execution ledger](2026-09-26-link-ownership-ledger.md) records actual progress.

The [plan audit](2026-09-26-link-ownership-audit.md) records the corrected
document gaps and the remaining implementation gates.

## Complete the operational goal

Prioritize the first safe production cutover. Complete the exact merged
release's physical testbed proof, then perform the conditionally authorized
production preparation and Webpass activation. Limit deployment refinements
to requirements for that cutover. Preserve the remaining epic scope; later
interface transfers, AT&T retirement and reboot detection remain subsequent
phases.

Complete MWAN-305 interface ownership through verified operational acceptance.
Preserve AT&T and networkd coexistence, deployment authorization, original
sources, production backups, and production recovery state. Testbed state is
disposable; do not require backups or preservation before fault injection.
Deploy only clean merged revisions
through `./configsctl deploy`. Perform actual testbed forward and reverse
cutovers, restart, reboot, failover, recovery, and repeated unchanged operation.
Observe both downstream guests and both IP families. Verify acquisition,
load balancing, mappings, IPv6 translation, binary identity, and configuration
identity. Recover or revert measured failures, implement focused fixes, review,
merge, redeploy, and repeat acceptance.

Require the complete testbed evidence before production cutover. The operator
authorized production once the defect is fully fixed. That authorization
applies only after the incident corrections and required testbed acceptance
pass for the exact merged release and compatible configuration. Do not ask
again for this authorized scope. Preserve recovered production until these
conditions pass. Final retirement still requires confirmed AT&T retirement
and its separate acceptance prerequisites.

Apply the subagent-driven-development skill strictly. Give every implementer
exact working directories, exclusive file ownership, prerequisite revisions,
settled interfaces, constraints,
verification, and report requirements. Assign coupled changes to one owner.
Inspect reports and diffs before integration. Preserve concurrent edits.

Reuse maintained protocol libraries and operating system services. Before
adding custom protocol code, inspect existing library APIs and verify upstream
maintenance using code changes, releases, issue responses, and archive status.
Do not treat a recent repository push alone as maintenance proof. Require
implementers and reviewers to identify the library operations used for DHCP,
neighbor discovery, routing, and resolver configuration. Implement only MWAN
policy and lifecycle integration where existing APIs do not provide them.
Record the exact API limitation and evaluated alternatives before approving
custom protocol code.

## Correct the failed preparation before production

Production preparation failed on October 1 and the original snapshot was
restored. Require the incident corrections under MWAN-535 through MWAN-544
before another production attempt. Production activation remains stopped until
the repair and testbed gates pass.
Complete every incident correction as part of the operational goal. Import
the existing MWAN Cloudflare load balancers, pools and health monitors into
OpenTofu under MWAN-543. Preserve their current configuration and alert policy.
Verify an import plan without infrastructure changes before applying state
adoption. Exclude unrelated zones and services.

The operator authorized one manual lock cleanup and stopped further
MWAN-548 investigation and reproduction. Continue cutover acceptance after
that cleanup. Do not add automatic lock removal or require a new prevention
project before resuming the approved migration. The unpublished lock-removal
helper is abandoned; its earlier permission question requires no answer.

Run legacy upgrade repairs, Cloudflare imports and health contract inspection
in separate lanes with exclusive files. Review the health interfaces before
implementing dependent checks. Serialize deployment recovery edits and live
testbed operations under one controller. Require the original-release upgrade,
inbound failure recovery and independent health results before promotion.
Reproduce the original-release upgrade on the physical testbed with existing
legacy edges, an empty new ownership journal and the actual legacy device
selector. Do not require preservation of the current testbed fixture.
Require positive inbound and downstream results before accepting the repair.
An omitted optional upgrade branch does not establish successful coverage.

Keep durable health and acceptance results separate for actual downstream
client experience, configured connection distribution and inbound pool health,
provider-specific egress, each ping's source and selected path, and the public
source address observed externally. Keep both IP families and primary/backup
selection explicit. Preserve acquisition, ownership, translation, mapping,
BGP, management, lifetime, restart, reboot and recovery checks. Report missing
observation separately from healthy or unhealthy service.

Arm recovery before network-affecting changes. Include required inbound
application replies in the deploy verdict even when backup egress succeeds.
Verify actual failure and recovery alerts. Keep new application and acceptance
code in MWAN. Apply the operator's conditional production authorization only
after the repair and testbed gates pass.

## Store and clean generated artifacts

Store all generated ephemeral artifacts under
[/Volumes/Chaos Storage/Codex/mwan305](</Volumes/Chaos Storage/Codex/mwan305>)
only while needed. This includes build
outputs, caches, downloads, VM disks, captures, logs, temporary scripts and test
fixtures. Keep source changes and the durable coordination plan and ledger in
their repositories.

1. Verify Chaos Storage is mounted before every artifact-producing operation.
   Stop the operation if the volume is unavailable. Do not create a replacement
   directory or silently fall back to the internal Mac disk.
2. Configure each tool's temporary, output and cache directories explicitly on
   Chaos Storage before generation. Assign each subagent an exclusive artifact
   directory and these storage and cleanup requirements.
3. Record each artifact's purpose, location and retention need in a compact
   catalog. Retain only minimal unique evidence required for current acceptance,
   incident analysis or production recovery.
4. After each slice, failed attempt and compaction reorientation, summarize
   durable results in the ledger and delete owned artifacts no longer required.
   Regenerate disposable outputs instead of keeping redundant copies.
5. Remove owned temporary containers and images once unused. Verify ownership
   before deletion and preserve other agents' resources.
6. Check disk usage regularly. Prevent unbounded accumulation and finish owned
   artifact cleanup before declaring the goal complete.

The previous local artifact directory is a symlink to Chaos Storage. Verify
that symlink before using an old absolute evidence reference. An unavailable
external volume does not permit generation under that old path.

## Reorient before each slice

Reorient immediately after every compaction, before each slice and
integration, and on every scheduled heartbeat. Require implementers to
reorient before dependent work.

1. Review this coordination plan, the current slice plan, applicable
   specifications, and relevant tickets. Refresh states and dependencies
   that determine the next operation.
2. Review the supplied memory summary, relevant memory entries, and recent
   ledger entries. Read older decisions when needed to resolve uncertainty.
   Do not reread unrelated memory or the entire historical ledger.
3. Confirm the approved scope, completed work, remaining acceptance,
   deployed revisions, active operations, agent ownership, and recovery
   procedure. Verify current evidence before a risky operation.
4. Record the selected slice, prerequisites, evidence, and actual blockers
   in the ledger. Preserve useful summaries and exact evidence references.
   Do not require read counts, hashes, or exhaustive rereads as a condition
   for continuing work.

Keep the existing thread automation `mwan-305-execution-checkpoints` active
while the goal is active. Use a short prompt that references this plan and
the recent ledger. Use a two-hour interval and adjust it when the next useful
checkpoint changes. Preserve reorientation at every slice and compaction.
Reuse that automation. Inspect
actual process and agent handles before dispatching work. Do not interrupt
an active mutation or create duplicate workers. Respect explicit pauses.
Suspend the automation after verified completion. Notify only for a meaningful
result, failure, completion, or required decision.

Separate passed, failed, speculative, and unperformed results. Record each
exact binary, configuration, source revision, operation, result, and remaining
gap. State the operation needed to resolve missing evidence. Plans, component
tests, merged PRs, and installed binaries do not prove operational acceptance.
Do not infer causes, reduce intended behavior, increase limits to conceal
failures, or add unrelated repairs and speculative safeguards.

Follow the enforce-rules skill and repository testing rules for every new
test, including complete required
rule-file reads. Use the smallest necessary public-boundary regression test
with real dependencies and an observable result. Use inspection, generation,
compilation, or existing checks for mechanical changes. Delete tests for
removed behavior. Do not use mocks, stubs, spies, recorded responses, static
content checks, or tests that repeat implementation.

Continue authorized safe and reversible work autonomously. Ask only when a
required decision remains after useful independent work is exhausted.
Reconcile the ledger and actual Tack ticket states with verified operational
acceptance. Mark the goal complete only after the required deployed revisions,
live behavior, ledger, and tickets pass verification.

## Restore the Astound baseline first

[MWAN-524](https://tack.home.goodkind.io/browse/MWAN-524) restored the
managed IPv4-only Astound testbed provider. The merged Configs deployment
passed reboot, downstream packet, balancing, and recovery checks. The ledger
records the revisions,
packet counts, and interruptions. Keep this accepted baseline during the
migration cutovers.

Require [MWAN-534](https://tack.home.goodkind.io/browse/MWAN-534) live
acceptance before resuming ownership transfer.

MWAN-331 deliberately removed Astound after its configuration-only acceptance
test. Preserve that ticket and MWAN-491 as historical completed work. The
operator requires the testbed connection to remain managed.

## Assign work by responsibility

| Agent | Assigned work | Required handoff |
| --- | --- | --- |
| Coordinator | Verify prerequisites, settle interfaces within the approved design, assign exclusive file ownership, sequence PRs, and reconcile evidence. | Give each agent one slice, exact source revision, settled contracts, dependencies, and acceptance requirements. |
| Code implementer only | Implement the settled slice, add necessary public-boundary tests, and run local checks. Verify the plan's premise against current source first. | Return the patch, exact checks and results, public behavior proved, and unresolved contradictions. Do not deploy, decide architecture, or substitute self-review for independent review. |
| Independent reviewer | Review contracts before implementation and inspect the resulting patch afterward. Reproduce the slice's failure cases and check shared consumers. | Return findings with evidence and a verdict tied to the reviewed commit. Report missing proof explicitly. |
| Cutover agent | Inspect live ownership, execute the approved merged deployment, monitor downstream traffic, perform recovery, and record acceptance. | Return exact release and configuration revisions, commands, before/after owners, interruption, recovery, and packet results. |

Run implementation and testbed defect correction concurrently. Give the
implementation lane the remaining approved code slices. Verify missing code
against current source; a Todo ticket or unfinished live transfer alone does
not establish missing implementation. Prepare removals without activating
them before retirement prerequisites pass.

Give the validation lane measured testbed failures and their evidence.
Diagnose each failure before assigning a focused fix. Use separate worktrees
and exclusive file ownership. Serialize shared file changes and integration.
Keep one controller for every live deployment, cutover and recovery operation.
Record code completion separately from operational acceptance.

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

Keep new MWAN application and acceptance code in the MWAN repository.
Use Configs for deployment configuration, inventory, templates and
infrastructure declarations. Do not restore the deleted Ruby acceptance
harness or add its replacement to Configs. Use existing public daemon
commands and protocol runners for acceptance.

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
| [Links, addresses, and route repair](interfaces/kernel.md) | MWAN-397 links; MWAN-398 static local addresses and optional main default routes; MWAN-398 mapped address-writer transfer; MWAN-505 route repair. | Review each writer boundary before implementation. Keep MWAN-505 independent. |
| [Address acquisition](interfaces/acquisition.md) | MWAN-398 DHCPv4; MWAN-227 delegation; MWAN-517 kernel IPv6 and DHCPv6 addresses; the NPT address authority. | Start DHCPv4 after both MWAN-398 address PRs. Assign separate implementers to independent protocols; share one DHCPv6 client. Complete the NPT authority before physical transfer. |
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
| MWAN | [MWAN-516] Separate stable connection IDs from provider display names | Use a standalone PR from trunk. Re-key every identity consumer and preserve legacy IDs before accepting repeated provider names. |
| MWAN | [MWAN-516] Define shared interface configuration and assignment contracts | Start the model stack after the identity PR merges. |
| MWAN | [MWAN-523] Observe device identity and kernel assignment state | Stack on the model PR. |
| MWAN | [MWAN-516] Publish connection ownership and acquisition history | Stack on observation. End and merge the model stack here. |
| MWAN | [MWAN-397] Manage physical links and VLAN dependencies | Merge this focused PR after the model stack. Admit owned links and install their runtime writer together. Deploy the merged revision to the testbed, validate synthetic owned links, then promote the proven revision. Keep live providers under networkd. |
| MWAN | [MWAN-398] Admit and reconcile static local addresses and optional main default routes | Start after link management passes testbed and production validation. Admit MWAN-owned non-provider links and install the runtime writer in this PR. Journal exact owned objects, replace the recorded route when only its metric changes, report source validity separately from kernel apply results, and test through the public daemon namespace suite. Merge and validate this PR before mapped address transfer. |
| MWAN | [MWAN-398] Transfer mapped address writes for exclusively owned connections | Add this PR after static addressing. Preserve legacy IPv4 mapped writers. Test synthetic exclusive ownership and legacy ownership through the public daemon. Do not transfer a live provider before MWAN-519 or MWAN-399. |
| MWAN | [MWAN-305] Journal NPT edge addresses across connection owners | Use a focused standalone PR from merged acquisition and address contracts. Include the authority, scoped journal, verified translation removal, and real daemon regressions. Preserve legacy acquisition and unchanged edges across owner changes. Publish the merged release before the Configs companion. |
| MWAN | [MWAN-505] Restore deleted owned routes | Use a standalone PR for the existing defect. Integrate any new observation API after it merges. |
| MWAN | [MWAN-398] Complete DHCPv4 acquisition and lease replacement | Use a standalone PR after both MWAN-398 address PRs merge. |
| MWAN | [MWAN-517] Configure and observe kernel IPv6 acquisition | Use a sibling standalone PR after the kernel stack merges. |
| MWAN | [MWAN-227] Acquire delegated prefixes through one DHCPv6 lifecycle | Start the DHCPv6 stack after the kernel stack merges. |
| MWAN | [MWAN-517] Add interface assignments to the DHCPv6 client | Stack on delegation after kernel IPv6 acquisition merges. End the DHCPv6 stack here. |
| MWAN | [MWAN-518] Persist lease records | Standalone. |
| MWAN | [MWAN-518] Validate DHCPv4 restart | Independent. |
| MWAN | [MWAN-518] Validate DHCPv6 restart | Independent. |
| MWAN | [MWAN-518] Integrate recovery | After the three PRs merge. |
| Configs | [MWAN-521] Render complete interface roles and ownership | Start the deployment stack after the model contract merges. Keep default ownership unchanged. |
| Configs | [MWAN-521] Pair the NPT address journal with its compatible release | Use a focused standalone companion after the authority release publishes. Verify both environment renders and actual legacy daemon startup. Prove the same-boot pre-reboot gates and absence-before-creation baseline reboot in isolation and on testbed. Require physical dual-stack transfer acceptance before production promotion. This cross-repository dependency is not a Graphite parent. |
| Configs | [MWAN-521] Implement exclusive ownership transfer and recovery | Stack on rendering. End the deployment stack here; gate activation on the complete application release. |
| Configs | [MWAN-522] Configure protocol lifecycle scenarios in ISP simulators | Use an independent PR. Preserve existing simulator defaults. |
| MWAN | [MWAN-522] Run privileged daemon acceptance through public boundaries | Bootstrap the runner in a standalone PR after the model merges and before protocol PR acceptance. Add each feature's scenarios in its own PR. |
| MWAN | [MWAN-522] Verify downstream forwarding and balancing during migration | Use a standalone PR after deployment integration and simulator changes merge. Keep executable acceptance code outside Configs. |
| MWAN | [MWAN-400] Remove retired networkd application dependencies | Use a standalone PR after the retirement inventory verifies removal scope. |
| Configs | [MWAN-400] Remove retired networkd deployment dependencies | Use a separate standalone PR after that inventory; pin the compatible MWAN release. |

Live acceptance phases are operational work, not placeholder PRs. A required
ownership configuration change gets its own focused Configs PR for that phase.
Do not combine several providers' activation changes merely because the
underlying code is ready.

The release workflow publishes each merged runtime slice. Group compatible
changes that do not transfer live connection ownership into a bounded
deployment phase. Pin the latest verified merged release in a focused Configs
testbed PR and deploy from merged Configs main through `./configsctl deploy`.
A focused PR merge alone does not require a deployment. Record every included
PR and validate the combined release through both downstream address families,
current provider balancing, controlled failover, and recovery. Add
protocol-specific checks when the phase changes acquisition or ownership.
Record detection time and successful forwarding after failover separately.
Keep production on its prior release until the complete testbed phase passes.
Transfer each live provider in its own phase and run the full cutover battery.

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
| Kernel implementation | Implement links, static local addressing, and mapped writer transfer in separate PRs while Configs rendering and the daemon test runner develop against the merged model. | Merge both address PRs and runner bootstrap before protocol PR acceptance. |
| Acquisition | Implement DHCPv4, kernel IPv6, and DHCPv6 delegation in separate lanes. | Merge kernel IPv6 before the DHCPv6 interface-address PR; finish all acquisition before persistence acceptance. |
| Integration | Implement persistence, deployment mechanics, and acceptance tooling where files do not overlap. | Review and merge all required code and pin compatible revisions before first transfer. |
| NPT authority follow-up | Serialize address-journal, NPT, and shared module edits in one implementation lane. Prepare Configs render tests independently without activating the journal. | Merge and publish the authority, pair the explicit journal and release, prove the isolated and testbed baseline, then pass physical dual-stack transfer acceptance before production promotion. |
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
| Address and route ownership | Review static local writes on non-provider links first. Review mapped transfer with WAN routing and OOB consumers. Give one implementer the NPT edge authority, scoped journal, and verified translation removal; ordinary family pruning must exclude those edge records. |
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

## Finish deployment reboot detection

Implement this task last, after the daemon owns every interface and the
ownership acceptance checks pass. Compare hashes or an inventory of applied
state with the requested deployment to determine whether a reboot is needed.
Skip the reboot when the changes do not require it.
