# Implement native network validation and typed rule planning

## Goal

Implement native provider validation and stable configured rule projections under the approved [contract](spec.md). Extend the existing production paths.

## Current behavior

At baseline commit `37205aae3716c41c131f3d4cc24de7224a751918`, merged MWAN PR #202 provides canonicalization, configured route projections, provider state, and local OpenTofu plan tests. Provider planning uses canonicalization and Go decoding. Apply and gateway startup use native network loading.

## Constraints

Limit documentation changes to this plan, the specification, and the network-planning index sentence. Do not review unchanged prose. Do not repeat the superseded portable-provider implementation sequence. Follow [AGENTS.md](../../../AGENTS.md) for implementation gates and repository conventions.

## Assign ownership and dependencies

| Workstream | Owner and scope | Dependencies |
| --- | --- | --- |
| Shared library and provider | tofu-wanconfig-mwan-provider implements Tasks 1 through 4 and coordinates Go tooling and packaging. | Tasks 1 and 2 and the Task 5 native recipe foundation may proceed in parallel after ownership is agreed. Task 3 requires Tasks 1 and 2 and the Task 5 foundation. Task 4 follows Task 3. |
| Release packaging | poweredge-mwan-package-integration implements Tasks 5 and 6 with the provider owner. | Task 5 native recipes may begin alongside Tasks 1 and 2 after ownership is agreed. Task 5 final release packaging follows Task 4. Task 6 follows Task 5 final packaging. |
| Configs acceptance | proxmox-guest-provider-migration implements Task 7. | Module discovery and wiring preparation may begin early. Acceptance follows Tasks 4 and 6 and requires the selected release. |
| Shared runtime review | mwan-network-cutover reviews validation, configured derivation, compiler semantics, and future mutable updates. | Review each shared interface before its consumers merge. |

Coordinate shared-file edits and the local Docker engine. Serialize builder operations that share caches. Coordinate deployment windows with tack-deployment-hardening per target environment. Separate QA, production, and PowerEdge environments may proceed independently.

## Task 1. Extract mandatory libyang validation

### Files

Modify [schema_cgo.go](../../../gateway/internal/yangpub/schema_cgo.go), affected yangpub callers, and [networkload/load.go](../../../gateway/internal/networkload/load.go). Reuse [schema/schema.go](../../../gateway/internal/yangpub/schema/schema.go).

Create [gateway/internal/yangschema/schema_cgo.go and gateway/internal/yangschema/schema_cgo_test.go, with optional gateway/internal/yangschema/embedded.go for shared embedded-module loading](../../../gateway/internal) in the new libyang-only package.

### Behavior

Provider and gateway callers use one libyang-only implementation. Schema handles own their native context and any embedded-module temporary directory. Gateway loading retains path-qualified errors and existing entry-rejection behavior.

### Steps

1. Extract libyang context loading, feature selection, JSON validation, error handling, and Close from yangpub. Preserve operating-system-thread pinning around native calls and error retrieval.
2. Implement `LoadSchema(schemaDir string)`, `LoadEmbedded()`, and `Schema.ValidateConfigJSON([]byte) error`. Derive embedded modules and features from `schema.Modules()` and `schema.Read()`.
3. Preserve the current `Close` signature, error behavior, closed-handle rejection, and idempotent lifecycle behavior. Clean partially initialized contexts and temporary directories on failure.
4. Update yangpub consumers without importing sysrepo into the new package. Keep gateway publishing and sysrepo lifecycle behavior unchanged.
5. Create `networkload.ValidateAndDecode(data []byte, schema *yangschema.Schema) (*networkjson.Config, error)` through the common JSON rejection, native schema validation, and semantic decoding path. Decode precisely the bytes validated.
6. Make `Load` use the helper. Preserve gateway path-qualified errors and read, schema-load, validation, syntax, and semantic error classification and logging. New JSON guards may change malformed-input messages.

### Verification

Run `make -C gateway docker-make TARGETS="check test"`. Exercise native validation with real embedded and path-loaded schemas. Verify closed handles, initialization failure cleanup, enabled features, strict/no-state/present semantics, compatible gateway load outcomes, and path-qualified errors. Inspect the provider dependency graph after Task 3 to establish that sysrepo and Linux services are absent.

## Task 2. Share configured rule derivation

### Files

Modify [networkjson/plan.go](../../../gateway/internal/networkjson/plan.go), [wanroutes.go](../../../gateway/internal/ifmgr/modules/wanroutes/wanroutes.go), [firewall/rules.go](../../../gateway/internal/firewall/rules.go), and [firewall/apply_linux.go](../../../gateway/internal/firewall/apply_linux.go).

Create [gateway/internal/networkjson/policy_plan.go and gateway/internal/networkjson/firewall_plan.go](../../../gateway/internal/networkjson) for projections. Create [gateway/internal/interfaceintent/policy.go](../../../gateway/internal/interfaceintent) for configuration-only intent. Create [gateway/internal/firewall/plan.go](../../../gateway/internal/firewall) for compiler metadata. Extend [gateway/internal/networkjson/plan_test.go](../../../gateway/internal/networkjson/plan_test.go) and [gateway/internal/networkjson/decode_parity_test.go](../../../gateway/internal/networkjson/decode_parity_test.go). Extend provider public plan tests in Task 4. Move or reuse existing shared types; do not create mirrored Go structs.

### Behavior

Planning derives policy intent without runtime observations. Runtime rendering and provider firewall projection consume the same structured compiler result.

### Steps

1. Separate wanroutes configuration calculation from readiness, health, and runtime translation selection. Preserve runtime eligibility decisions in wanroutes.
2. Return policy-rule metadata with `connection_id`, `family`, `kind`, `priority`, `table_id`, `mark`, `source`, `source_kind`, and `activation_conditions`. Derive activation conditions from shared daemon prerequisites. Represent unavailable concrete selectors as null in the provider projection.
3. Derive policy identities with the shared encoder and the contract's key format. Keep mutable fields out of keys. Preserve existing route and provider-default keys, projections, and exclusions.
4. Replace the compiler's string-only rule representation with structured rules containing stable source metadata and canonical expressions. Update Ruleset.String and ApplyWithReport to render that result.
5. Derive firewall keys with the shared encoder and the contract's key formats. Use existing YANG source keys and logical compiler purposes for distinguishable source identities. Disambiguate indistinguishable duplicates by deterministic occurrence within the same source group. Exclude whole-chain position from identities. Document the limit for arbitrary edits to indistinguishable duplicates. Do not require JSON IDs.
6. Return per-chain rule_order separately from rule fields. Project configured set definitions and exclude runtime membership.
7. Validate renderer output through the existing production path. Preserve all generated rules and service, provider, forwarding, and management behavior.

### Verification

Run the gateway check and test gates. Test through decoded documents, firewall.Compile, and runtime rendering with real dependencies. Verify add, remove, mutable-field, source-identity, duplicate, and order outcomes. Exercise configured conditional policy selectors without supplying fabricated runtime values. Require runtime-owner review of both derivations.

## Task 3. Extend provider validation and state

### Files

Modify [network_data_source.go](../../../provider/internal/provider/network_data_source.go), [network_model.go](../../../provider/internal/provider/network_model.go), [network_config_resource.go](../../../provider/internal/provider/network_config_resource.go), and [network_test.go](../../../provider/internal/provider/network_test.go).

Create [provider/internal/provider/network_state_upgrade_test.go](../../../provider/internal/provider) for migration coverage.

### Behavior

Known content receives native and semantic validation before output conversion. Existing route-only state upgrades without replacement. Official integration receives all configured maps.

### Steps

1. Load the embedded schema through yangschema and call ValidateAndDecode. Close the handle on every completed evaluation. Retain canonical_content from formatting-only canonicalization.
2. Convert native errors, semantic errors, projection errors, and every rejected entry into diagnostics. Preserve interface/provider context and existing semantic messages.
3. Extend networkMaps and shared object conversion with policy_rules, firewall_chains, firewall_rules, and firewall_sets. Return rule_order within each chain object.
4. Preserve nullable values, uint32 checks, route source distinctions, translation-family selection, and provider-default runtime exclusions. Return guest_type from shared decoding.
5. Set resource schema version 1. Add optional new maps with empty defaults and a version 0 upgrader. Preserve existing required maps and route keys.
6. Validate keys and chain-order references. Keep Create/Update plan copying, prior-state Read, state removal on Delete, and absence of import or replacement modifiers.
7. Exercise both objects through protocol 6 with actual schema loading. Cover null rejection, unknown deferral, schema diagnostics, identity validation, and state upgrades.

### Verification

After the Task 5 native recipe foundation supplies developer dependencies, run `make -C provider check test`. Require migration tests with existing version 0 route-only state. Verify that upgraded routes retain identities and that new rule field updates require no replacement. Known unknown-member, enum, range, mandatory-node, duplicate, case-fold, and surrogate failures must fail provider evaluation.

## Task 4. Extend real OpenTofu plan acceptance

### Files

Modify [tofu_plan_test.go](../../../provider/internal/provider/tofu_plan_test.go), its existing fixtures under [testdata/networkplan](../../../provider/internal/provider/testdata/networkplan), and [provider/Makefile](../../../provider/Makefile).

Extend existing shared tests and unchanged baseline inputs [network-min.json](../../../gateway/yang/instances/network-min.json) and [network-freeform.json](../../../gateway/yang/instances/network-freeform.json) through temporary variants.

### Behavior

OpenTofu shows configured route and rule changes under stable keys. Canonical file differences remain independently visible. Native validation rejects known schema errors during planning.

### Steps

1. Preserve the real provider/OpenTofu/local-state harness and terraform_data.file dependency. Wire every new output to the configured-state resource.
2. Retain route addition, removal, gateway, metric, on-link, shorthand, source, and provider-internal-destination cases. Assert structured before/after values and representative rendered text.
3. Add policy and firewall add/remove/field-change cases. Change mutable fields under existing keys. Change natural source identities and assert removal/addition.
4. Reorder firewall rules and assert rule_order changes without replacing rule identities. Exercise configured set changes when applicable.
5. Verify typed equality for system-ordered lists, explicit defaults, and equivalent IPv6 spelling. Verify canonical file differences where source arrays or scalar spellings change.
6. Verify no changes for whitespace and object-member ordering. Compare complete semantic results for accepted original and canonical bytes.
7. Cover native-only rejection cases and initialization failures. Test unknown content through a genuinely unknown dependency.
8. Preserve the plan-only lan0 negative case. Use accepted fixtures with allowed roles and no LAN client services or LAN client forwarding.

### Verification

Run `make -C provider test-tofu-plan` with the selected OpenTofu executable. Assert no replacement actions and visible keyed changes while the file dependency has a pending update. Keep dev_overrides evidence separate from package acceptance. Local terraform_data tests establish neither guest validation nor service restart behavior.

## Task 5. Build native provider release packages

### Files

Modify [gateway/Makefile](../../../gateway/Makefile), the provider Makefile assigned in Task 4, [.github/workflows/ci.yml](../../../.github/workflows/ci.yml), and [.github/workflows/release.yml](../../../.github/workflows/release.yml).

[Create build/wanconfig.mk for shared authoritative pins and build/yang.mk for libyang/PCRE2-only native recipes](../../../) using the existing shared build hooks.

### Behavior

Prebuilt providers include libyang/PCRE2 dependencies for all three targets. Gateway releases retain static libyang/sysrepo linkage. Provider releases exclude sysrepo.

### Steps

1. Extract existing authoritative pins without changing gateway versions or sysrepo behavior. Preserve libyang v3.13.6 at `c2ddd01b9b810a30d6a7d6749a3bc9adeb7b01fb`. Select and verify a PCRE2 source commit in the shared authoritative dependency version set.
2. Reuse GO_MK_CGO_DEPS and go-mk-cgo-dep-libyang conventions. Separate provider dependency construction from Linux-only gateway/sysrepo construction.
3. Enable cgo in provider builds and CI/release module configuration. Remove the provider's CGO_ENABLED=0 requirement. Provide native target runners and required developer build dependencies. Require native package builds and runtime smoke tests across the three existing targets. Preserve existing platform lint coverage. Do not widen platform lint matrices.
4. Validate calls against the pinned libyang headers and library. Do not introduce optional native-validation stubs or a second diff engine.
5. Attempt static libyang/PCRE2 linkage per target. Inspect linked libraries before selecting the packaging mode. Keep macOS system linkage compatible with Darwin.
6. If a target requires bundled dynamic libraries, package them beside the provider with relative loader paths. Remove build-prefix references and verify the relocated archive.
7. Preserve signatures, checksums, attestations, all platform assets, and provider/schema/mwan commit matching.

### Verification

Run provider gates and target builds with native dependencies enabled. Run `make -C gateway test-provider-drift` and gateway builder gates. Inspect Linux packages with readelf/ldd and Darwin packages with otool. Record dependency paths and packaging decisions per target. Build success alone does not establish user installation acceptance.

## Task 6. Test published-format packages without build dependencies

### Files

Create [provider/scripts/test-release-package.sh and provider/internal/provider/release_package_test.go](../../../provider). Add the new `test-release-package` Make target and target-runtime CI jobs in the files assigned above.

### Behavior

A released-format archive installs through an OpenTofu filesystem mirror and performs native planning without compiler access or separately installed libyang/PCRE2.

### Steps

1. Require absolute PROVIDER_PACKAGE and TOFU inputs. Reject missing or relative paths. Do not compile during the target's runtime phase.
2. Implement the shell harness in its own .sh file with a shebang, set -euo pipefail, interrupt handling, tracked child PIDs, and temporary-directory cleanup.
3. Extract the supplied archive into the provider mirror layout. Use the actual provider address and packaged version. Generate isolated HOME and CLI configuration without dev_overrides or direct installation fallback.
4. Run in native darwin/arm64, linux/amd64, and linux/arm64 runtime environments. Exclude compiler access, dependency-build prefixes, and separately installed libyang/PCRE2. Inspect loader dependencies, resolved library paths, and runtime filesystem contents. Hiding executables through PATH alone does not prove clean-host packaging.
5. Execute real `tofu init` and `tofu plan` through the runtime shell harness. Never invoke `go test` or compilation at runtime. Precompile any Go test runner in build CI. Run plan against a native-only invalid document and require the schema diagnostic.
6. Exercise relocation and archive metadata. Verify that unpacked signatures/checksum evidence corresponds to the archive tested.

### Verification

Set `PROVIDER_PACKAGE` to the absolute release archive path from the build output. Set `TOFU` to the absolute path of the installed test OpenTofu executable. Run the new target with both required inputs.

```bash
make -C provider test-release-package PROVIDER_PACKAGE="$PROVIDER_PACKAGE" TOFU="$TOFU"
```

Require passing evidence for every target. Record init success, valid-plan success, native rejection, dependency inspection, isolated environment properties, archive identity, and exit status. Do not report clean-host acceptance until these runtime checks pass.

## Task 7. Complete Configs wiring and acceptance

### Files

The migration owner must discover the existing gateway module in [Configs](https://github.com/agoodkind/configs). Modify that module's document writer, configured-state wiring, unit restart dependencies, and both lifecycle preconditions. Record exact discovered paths before editing; do not invent a module path.

### Behavior

Validated file installation precedes typed-state completion and startup. Both consumers restart after content writes. LAN client traffic remains disabled.

### Steps

1. Select a published, package-tested release. Reconcile check-network and check-firewall syntax against its CLI. Keep the deploy-gate rename outside this workstream.
2. Supply known per-gateway content through file(). Wire all maps, including new rule maps, and pass decoded guest_type to mwan_role.
3. Preserve the prerequisite chain. Verify host kernel-module API support and required modules, container capabilities, binary/packages, installed schema and sysrepo role, staged validation, and unit startup. Verify current deployment state rather than relying on historical overlay claims.
4. Retire the gateway's Ansible network document writer before first file adoption. Make the configured-state resource depend on successful pveguest_file installation.
5. Include network write_id and existing binary/package write IDs in both unit restart dependencies. Prevent document mutations from replacing the guest.
6. Preserve role and route preconditions on the file resource. Check container bindings and actual host bridge members for prohibited LAN ports on the container resource.
7. Coordinate the target environment's deployment window and locks. Use configsctl deploy or configsctl tofu from the authorized Configs main checkout for real API operations.
8. Perform a content write that passes both deployed checks inside the selected LXC before startup. A validate-only change is insufficient.
9. Exercise a failed staged content write. Verify unchanged installed bytes, sha256, write_id, and typed state. Verify the next plan repeats pending changes.
10. Verify file-drift detection and both consumer restarts. Keep live reload, kernel-drift management by this resource, and client LAN activation excluded.

### Verification

Require live LXC firewall evidence; userspace validation cannot prove namespace/nft acceptance. Require both lifecycle preconditions to fail on disallowed examples. Plan the negative LAN fixture without applying it. Record release commit, targets, content-write result, failure recovery, state behavior, and unit restart results.

## Task 8. Review and complete the workstreams

### Files

Create no additional documentation. Review only changed implementation prose and the scoped documents.

### Behavior

Completion claims correspond to passing public boundaries, reviewed semantics, signed commits, and current merge requirements.

### Steps

1. Generate every newly written non-code text fragment through the bounded ephemeral CLI procedure. Include comments, commit messages, and pull request text. Supply the responses and writing rules verbatim. Create a unique temporary directory with `mktemp -d` for each invocation. Assign `PROSE_INPUT`, `PROSE_RESULT`, `PROSE_EVENTS`, and `PROSE_STDERR` to distinct files in that directory. Set `REPO_ROOT` to the absolute repository root. Write the input to `PROSE_INPUT`. Start the command through the host's background facility.

```bash
gtimeout --signal=TERM --kill-after=10s 300s codex exec --ephemeral --model gpt-6.1-sol --config 'model_reasoning_effort="high"' --sandbox read-only --output-last-message "$PROSE_RESULT" --json --cd "$REPO_ROOT" - < "$PROSE_INPUT" > "$PROSE_EVENTS" 2> "$PROSE_STDERR"
```

2. Check progress every 15 seconds during the active invocation. Inspect exit status, events, stderr, and the complete result. Accept only exit 0 with a complete reviewed result. Apply accepted output verbatim. After a timeout, inspect the old worker and stop surviving processes before starting a narrower bounded replacement. Request corrections through another bounded invocation. Do not create recurring automation or review unchanged prose.
3. Create commits in logical groups with `git commit -S` and the committing harness's exact `Co-authored-by` trailer. Codex prose drafting does not change commit attribution. Fetch before branch comparisons. Before pushing, verify every `origin/main..HEAD` commit signature and inspect its raw `gpgsig` header. Repeat after any signed rebase or restack.
4. Obtain runtime-owner review and Configs contract confirmation. Read the live GitHub ruleset and required checks; do not reuse historical check-name inventories.
5. Resolve review threads and satisfy current merge requirements. Report package and Configs acceptance independently when their evidence differs.

### Verification

Require passing gateway, provider, real-plan, state-migration, renderer, package-runtime, and Configs acceptance evidence. Record any missing boundary as incomplete. Do not claim deployment from a merged PR, local plan, or workspace message.
