> Read the [repository context](../README.md) before using copied commands or historical plans.

# MWAN-506 Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject an incomplete provider configuration before deployment, report any provider that the runtime omits, and close MWAN-506 without changing the accepted schema failure contract.

**Architecture:** The released `mwan` binary validates a rendered network document with the same `networkjson.Load` path the daemon uses. A deployment rejects every loader error and every omitted provider before it changes a gateway. Runtime startup still omits one provider when a schema-valid entry fails a provider-local loader rule, and the operational datastore reports that rejection beside the steering group state.

**Tech Stack:** Go 1.27, libyang, sysrepo, YANG 1.1, Ansible, Ruby RSpec, GitHub releases, and the existing `configsctl deploy` path.

**Spec:** [../wanconfig/providers.md](../superpowers/wanconfig/providers.md), with the deployment boundary defined by [../wanconfig/config.md](../superpowers/wanconfig/config.md)

## Global Constraints

- A schema-invalid document fails before any gateway file changes.
- A missing group value, a provider-set collision, a reserved table, and a document with no accepted provider remain fatal to the whole document.
- A schema-valid error within one provider entry omits only that provider at runtime.
- A deployment fails when runtime would omit any provider. A requested provider set must never become an unannounced partial deployment.
- Both checks use `networkjson.Load`. Do not duplicate loader rules in Ansible or another Go package.
- The deploy-gate command reads the supplied document and schema directory. It writes no file and reads no kernel state.
- The operational state reports the interface, provider name when available, and loader error for every omitted provider.
- The five-epic order remains unchanged. MWAN-324 is complete. This plan closes its MWAN-506 follow-up. MWAN-340 and MWAN-341 remain the fourth and fifth epics.
- Each pull request needs its own explicit merge approval.
- Finish all code and repository checks before live testing. Run testbed validation before production.

## Review Focus

- A schema-invalid provider must fail the deployment as a document error.
- One schema-valid provider rejection must fail the deployment and identify the rejected interface.
- Several provider rejections must all appear in command output and operational state.
- A collision between accepted providers must fail the whole document.
- A valid document must report every provider accepted and no rejection.

---

### Task 1: Finish the production-loader command in `agoodkind/mwan`

**Files:**
- Modify: `AGENTS.md`
- Modify: `cmd/mwan/deploygate.go`
- Modify: `cmd/mwan/deploygate_checknetwork_test.go`

**Interfaces:**
- Consumes: `networkjson.Load(path string, schemaDir string) (*networkjson.Config, error)`
- Produces: `mwan deploy-gate check-network <network_json> <schema_dir>` with exit status 0 for complete acceptance, 1 for any document failure or provider rejection, and 64 for bad arguments.

- [x] **Step 1: Add the command to the existing deploy-gate dispatcher**

Add `check-network` to `deployGateMode`, require exactly two arguments, and call `networkjson.Load` through the production dependency wiring.

- [x] **Step 2: Fail when the runtime would omit a provider**

Print every `networkjson.Rejection`, print accepted and rejected counts, and return `exitDeployGateFailed` when `len(loaded.Rejected) > 0`.

- [x] **Step 3: Test the real command boundary**

Run the test binary through `main()` with `MWAN_INSTALL_TEST_MAIN=1`. Cover a valid document, one rejection, two rejections, a schema error, a routing-table collision, an unreadable file, and missing arguments.

- [x] **Step 4: Run the Linux suite**

Run:

```bash
make test
```

Expected: every package passes through the repository's Linux builder image.

- [x] **Step 5: Commit and push the reviewed change**

The final signed branch commit was `3997c914acb518c78f51d01ed54b913d427d6edb` on `mwan-506-check-network`. `AGENTS.md` records that Darwin skips host `make check`; required Linux CI runs that gate, with the repository's Linux builder container as the fallback when GitHub Actions is unavailable.

- [x] **Step 6: Verify required Linux checks and merge PR 19**

Open [agoodkind/mwan PR 19](https://github.com/agoodkind/mwan/pull/19). Confirm every required Linux check uses head `3997c914acb518c78f51d01ed54b913d427d6edb`. Ask for explicit merge approval, then merge with the repository's accepted merge method.

Result: PR 19 merged as signed commit `a8208a3f9834cffe243630de5405848b12968a01`. Release `202609212257-11-a8208a3` contains the command.

---

### Task 2: Report runtime provider rejections through the served tree

**Files:**
- Rename: `internal/yangpub/schema/goodkind-mwan-steering@2026-09-19.yang` to `internal/yangpub/schema/goodkind-mwan-steering@2026-09-21.yang`
- Modify: `internal/yangpub/schema.go`
- Modify: `cmd/mwan/ifmgr.go`
- Modify: `cmd/mwan/wanconfig_publish.go`
- Modify: `cmd/mwan/wanconfig_livestate.go`
- Modify: `cmd/mwan/wanconfig_selftest.go`
- Modify: `cmd/mwan/wanconfig_selftest_test.go`

**Interfaces:**
- Consumes: `networkjson.Config.Rejected []networkjson.Rejection`
- Produces: a `rejected-provider` list under `/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state`, keyed by `interface` and containing `provider` and `reason` leaves.

- [x] **Step 1: Write the failing private-datastore test**

Pass these startup rejections into the real operational provider used by `wanconfig-selftest`:

```go
[]networkjson.Rejection{
    {
        Interface: "enrejected0",
        Provider:  "rejected-example",
        Err:       errors.New("ipv6/dhcp is required"),
    },
}
```

Extend `checkSelftestInterfaces` to require one `rejected-provider` entry under the steering group state with those exact three values. Keep the existing real libyang and sysrepo path.

- [x] **Step 2: Run the private selftest and verify the node is rejected by the current schema**

Run:

```bash
make test
```

Expected: the private wanconfig selftest fails because `rejected-provider` is absent from the 2026-09-19 model.

- [x] **Step 3: Add the operational YANG list**

Rename the steering module file to the new revision and add this revision before the older revisions:

```yang
revision 2026-09-21 {
  description
    "Report every provider entry the runtime loader rejected while the
     remaining providers continue to run. Every addition is a new node:
     no node defined by an earlier revision changes name, type, or shape.";
}
```

Add this list inside `steering-group/state`:

```yang
list rejected-provider {
  key "interface";
  description
    "A provider entry the loader omitted while the daemon continued with
     the remaining providers.";
  leaf interface {
    type string;
    description "The interface key from the rejected entry.";
  }
  leaf provider {
    type string;
    description "The provider name, when the rejected entry supplied one.";
  }
  leaf reason {
    type string;
    mandatory true;
    description "The loader error that caused the rejection.";
  }
}
```

Update `internal/yangpub/schema.go` to embed `goodkind-mwan-steering@2026-09-21.yang` with `Update: true`.

- [x] **Step 4: Preserve the rejections after startup loading**

Change `loadNetworkConfig` to return `([]networkjson.Rejection, error)`. Return `loaded.Rejected` after a successful load and return `nil, nil` for a role that does not steer providers. Keep `networkjson.Rejection` as the only type for this fact.

Change `runIfMgr` to pass the returned slice into `startWanconfigSurface`. Do not store it in `config.Config`; a rejected entry is runtime state, not accepted configuration.

- [x] **Step 5: Serve the rejections with the existing operational provider**

Add `rejected []networkjson.Rejection` to `startWanconfigSurface`, `registerLiveStateProviders`, and `interfacesLiveItems`.

Append these items for each rejection:

```go
base := groupBase + "/rejected-provider[interface='" + rejected.Interface + "']"
items = append(items,
    yangpub.Item{Path: base + "/interface", Value: rejected.Interface},
    yangpub.Item{Path: base + "/reason", Value: rejected.Err.Error()},
)
if rejected.Provider != "" {
    items = append(items, yangpub.Item{Path: base + "/provider", Value: rejected.Provider})
}
```

Set `groupBase` to `/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state`. Use the existing interface-key path convention already used by `interfacesLiveItems` and `internal/wanconfig`.

- [x] **Step 6: Run all repository gates**

Run the full test suite:

```bash
make test
```

Required Linux GitHub CI runs `make check`. Do not run host `make check` on Darwin because that path cannot compile the Linux-only libyang and sysrepo bindings. If GitHub Actions is unavailable, run the fallback recorded in `AGENTS.md`:

```bash
make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
    -v "$PWD:/src" -w /src \
    -v mwan-wanconfig-gomod:/go/pkg/mod \
    -e GOWORK=off \
    mwan-wanconfig-builder make check
```

Expected: the YANG gate accepts the new revision, the private sysrepo selftest reads the rejection, and every existing package passes.

- [x] **Step 7: Commit, push, and open a separate pull request**

Create a signed commit:

```bash
git add internal/yangpub/schema internal/yangpub/schema.go cmd/mwan
git commit -S -m "Report rejected providers in steering state" \
  -m "Co-authored-by: Codex <noreply@openai.com>"
```

Open a problem-first pull request for MWAN-506. State that schema-invalid documents remain fatal and explain that the new list reports only provider-local errors after schema validation.

- [x] **Step 8: Merge after explicit approval and wait for the signed release**

Confirm required checks, ask for approval for this pull request, merge it, and record the signed release tag, commit, checksums, and attestations.

Result: PR 20 merged as signed commit `ae1cc51c35010d218955e9130272e086093c7bdb`. Release `202609212312-12-ae1cc51` passed its publish and verification jobs. The MWAN archive checksum is `75106f252b073e589e428e7673b9fc944001849a92ca4fa19d635c8071226068`. The management stack checksum is `5b99e8a521b34466ef37bdaf3179ee125b531783b09177b75e9969e51989b2d9`. Both downloaded archives matched those checksums. `gh attestation verify` found one valid `agoodkind/go-makefile` attestation for each archive.

---

### Task 3: Replace the schema-only deploy check with the production loader

**Files:**
- Modify: `ansible/playbooks/deploy-mwan.yml`
- Modify: `ansible/playbooks/tasks/verify-mwan-release.yml`
- Modify: `spec/ansible/mwan_install_spec.rb`
- Modify: `ansible/inventory/group_vars/mwan_testbed_all.yml`
- Modify: `ansible/inventory/group_vars/mwan_prod_all.yml`

**Interfaces:**
- Consumes: the final signed `mwan` release from Task 2 and `mwan deploy-gate check-network <network_json> <schema_dir>` from Task 1.
- Produces: a deploy that validates the exact rendered bytes with the exact released loader before `wanconfig-stack.yml` installs the MWAN management stack.

- [x] **Step 1: Write the failing playbook-order test**

Change the existing `validates the gateway render` example to find these tasks by exact name:

```ruby
printed = tasks.index { |task| Array(described_class.command_argv(task)).include?('--print-schema') }
loader_check = tasks.index do |task|
  Array(described_class.command_argv(task)).include?('check-network')
end
management_stack_install = tasks.index do |task|
  task['ansible.builtin.import_tasks'] == 'tasks/mwan-vm/wanconfig-stack.yml'
end

expect([printed, loader_check, management_stack_install]).to all(be_a(Integer))
expect(printed).to be < loader_check
expect(loader_check).to be < management_stack_install
```

Also assert that the command uses `/usr/local/sbin/mwan-deploy-gate`, the remote rendered document, and `mwan_schema_remote.path`.

- [x] **Step 2: Run the playbook-order test and verify it fails**

Run:

```bash
bundle exec rspec spec/ansible/mwan_install_spec.rb
```

Expected: the new RSpec example fails because the play has no `check-network` task.

- [x] **Step 3: Make check mode install the pinned deploy-gate helper**

Set `check_mode: false` on `Push the deploy-gate binary to the Proxmox delegate`. Update the adjacent comment to state that check mode must validate with the pinned release rather than an older helper already present on the delegate.

Update the release verification task comment. Its command still skips in check mode even though this caller copies the pinned binary.

- [x] **Step 4: Validate inside one remote temporary directory**

Keep the schema temporary directory on the Proxmox delegate. Copy `mwan_network_json_local` to `{{ mwan_schema_remote.path }}/network.json`, then run:

```yaml
- name: Validate the rendered network configuration with the released loader
  delegate_to: "{{ mwan_proxmox_delegate }}"
  ansible.builtin.command:
    argv:
      - /usr/local/sbin/mwan-deploy-gate
      - deploy-gate
      - check-network
      - "{{ mwan_schema_remote.path }}/network.json"
      - "{{ mwan_schema_remote.path }}"
  changed_when: false
  when: mwan_ifmgr_wan_enabled | bool
  check_mode: false
```

Place schema creation, schema printing, document copy, and validation inside one `block`. Put remote directory removal in the block's `always` section so a rejected render leaves no temporary directory.

- [x] **Step 5: Remove the duplicate controller validation path**

Delete `mwan_schema_local`, the controller schema-directory cleanup, the module listing and fetch, and the direct `yanglint` command. `networkjson.Load` already invokes libyang against the same printed schema before it applies loader rules. One production path now proves both layers.

- [x] **Step 6: Pin both environments to the final release**

Set the same final release tag and asset checksums in `mwan_testbed_all.yml` and `mwan_prod_all.yml`. The selected release must contain both Task 1 and Task 2.

- [ ] **Step 7: Run the repository checks**

Run:

```bash
./configsctl lint
bundle exec rspec
```

Expected: the Configs lint gate and every RSpec example pass, including the playbook-order example.

Local result: `uv run --with jinja2 ./configsctl lint` passed, and the focused playbook suite passed 24 examples. The new example failed against the `origin/main` playbook because the rendered document copy and `check-network` task were absent. The full local RSpec suite still has one existing macOS failure in `CollectDeployVerdict quotes a verdict path containing a quote`; the same focused example fails on clean `origin/main`. Linux CI remains the full suite gate.

Adversarial review result: the final release source confirms that `networkjson.Load` validates the document with libyang before it applies the loader rules. `check-network` fails on a loader error or any rejected provider. The removed controller `yanglint` path provided no validation that the released loader omits. The review also corrected an inaccurate claim that validation preceded every gateway write; the required and tested boundary is the MWAN management stack installation.

- [x] **Step 8: Commit, push, and open the Configs pull request**

Create a signed commit:

```bash
git add ansible/playbooks/deploy-mwan.yml \
  ansible/playbooks/tasks/verify-mwan-release.yml \
  spec/ansible/mwan_install_spec.rb \
  ansible/inventory/group_vars/mwan_testbed_all.yml \
  ansible/inventory/group_vars/mwan_prod_all.yml \
  docs/superpowers/plans/2026-09-21-mwan-506-completion.md
git commit -S -m "Validate MWAN renders with the released loader" \
  -m "Co-authored-by: Codex <noreply@openai.com>"
```

The pull request must open with the deployment gap. State that the loader check replaces the direct `yanglint` command because the loader already performs the same schema validation before its additional rules.

- [ ] **Step 9: Merge after the repository rules are satisfied**

Read the active GitHub ruleset for `main`. Confirm every required check, approval rule, review-thread rule, signature rule, conflict rule, and merge-method rule. Resolve every review thread, then merge with an accepted method.

---

After Task 3 merges, execute the separate [MWAN-506 live validation plan](2026-09-21-mwan-506-live-validation.md). It owns testbed proof, production deployment, and final Tack updates.
