> Read the [repository context](../README.md) before using copied commands or historical plans.

# Provider set as data Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on this page refer to the tree as it was.

> **Completed.** MWAN-324 shipped on both gateways in release `202609210222-10-0b079b8`. [The MWAN-506 completion plan](2026-09-21-mwan-506-completion.md) covers the remaining provider-rejection visibility and deployment validation. MWAN-340 and MWAN-341 remain the next two epics.

**Goal:** Re-tiering or re-weighting an internet provider on the MWAN gateway becomes an inventory edit and a configuration deploy, adding or removing one is the same plus its two hand-written link files, and the binary never changes; the current three providers keep every live number and behavior.

**Architecture:** Each gateway group carries one `mwan_providers` list, and the network configuration file renders by looping over it. The daemon stops knowing any provider by name: it checks routing numbers for uniqueness and reserved-table collisions at load, reads tier and weight per provider, and owns load balancing through a new steering module that programs the split into its own kernel chain from the active tier's healthy providers. The gateway pushes its per-provider health verdict to the hypervisor watchdog, which drops its own interface list. The systemd-networkd link files stay hand-written. The testbed gains a fourth simulated provider so a fourth member is proven by inventory alone.

**Tech Stack:** Go 1.25 with google/nftables v0.3.0 and mdlayher/vsock, YANG 1.1 with libyang (yanglint on the controller, the cgo binding in the daemon), Ansible with Jinja2 templates, OpenTofu with the bpg/proxmox provider, systemd, nftables.

**Spec:** [docs/superpowers/wanconfig/providers.md](../superpowers/wanconfig/providers.md)

## Global Constraints

- The four routing numbers per provider (`table`, `mark`, `mark_prio`, `from_prio`) are typed in inventory and never derived.
- The two fixed priority validators are deleted in the same change that adds the uniqueness, reserved-table, and weight checks; no intermediate commit carries one without the other.
- The reserved-table set is typed once in inventory (`mwan_reserved_tables`), rendered into `network.json`, and read from there; the kernel tables 0, 253, 254, and 255 are reserved by the daemon itself.
- The daemon's steering module is the only writer of the connection-split rule from this epic on; the three fixed balancer lines leave the firewall ruleset file in the same change the module lands.
- Tiers in inventory decide fallback; the daemon carries no tie-break rule of its own.
- An unknown health verdict reads as healthy, so every provider reads healthy at startup and the lowest tier activates, as today.
- The systemd-networkd link files and the unreferenced static copies stay as they are; no renderer is written and no link bring-up enters the schema.
- No provider name remains in Go outside tests or in a per-provider inventory variable; the hardware variables that feed the link files keep their names.
- The kernel set names `att_pinned_v4` and `att_pinned_v6` and the refresher script, service, and timer keep their names; only the inventory lists and the pin target variable change.
- A missing value in `network.json` is a load-time failure, never a defaulted one; the renderer always emits tier and weight.
- Deploy-time and load-time validation use the same schema files, never copies; the repository keeps exactly one revision file.
- No secret ever enters the JSON.
- Behavior for the current three providers is unchanged, judged through the served tree, the policy rules, the routing tables, and the traffic matrix, with exactly one mapped difference: the balancer lines move from the ruleset file into the daemon's chain.
- Every mwan-installing deploy requires `--release <tag>`.
- Testbed before production, always. Each production command is separately approved by the operator before it runs, and every gateway reboot window is announced to the audit-ops, backup-reach, and prod-outage sessions before it opens and reported after it closes.
- OpenTofu is applied only with `-target=module.suburban` for testbed work; production OpenTofu is never touched by this plan.
- Go style: comments explain non-obvious why, never what; full-word names; struct literals enumerate every field, because the `exhaustruct` gate requires it; every wrapped error is logged with slog by the function that wraps it; no lint suppressions.
- Tests exercise real behavior through public boundaries with in-memory fakes at the kernel and vsock seams only; no mock soup, no snapshot-only tests.
- Commits are signed (`git commit -S`), the subject is imperative with no trailing period, and the body ends with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.

### Running the gates

The Go gates live in `mwan/go`. On macOS the host `go test` compiles none of the
linux-tagged files and cannot build the cgo libyang binding, so the suite runs
inside the builder image; `make test` already routes itself that way on darwin.
A single-package run uses the same image directly:

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/networkjson/ -v
```

Replace the package path for each task. The real gates are the make targets
`make help` lists in `mwan/go`, all run from that directory: `make check`
(every lint gate for both shipped platforms plus the YANG model and instance
gates; the cgo dependency hook skips itself on darwin), `make test` (routed
through the builder image on darwin), and `make build` (vet, lint,
govulncheck, and the Go version check, then the compile). There is no
Makefile at the repository root. The pull request's CI runs the same gates
on linux and is the verdict that counts when a host-only toolchain fault
(such as a staticcheck panic on the darwin host) blocks a local lint run;
report such a fault with its output rather than working around it. Inside
the builder lane `go test -race` prints a false ok with zero tests run, so
never cite a race result from it. See
[agoodkind/mwan](https://github.com/agoodkind/mwan) for the build rules every
change follows.

This docker lane and the per-package recipe above are a stopgap that exists
only because the Go code still lives inside this configuration repository
and the macOS controller cannot build its cgo binding. The repository split
(epic MWAN-379 and its siblings) moves the Go code into its own repository
with its own native gates, and this plan's recipe retires with it. Do not
treat it as the long-term way to build or test anything.

Ansible is never invoked directly. Syntax checks run through the repository's
rake wrappers (`cd ansible && rake syntax:mwan`), and deploys run through
`./configsctl deploy <play> --limit <group>`. The play pulls the release its
environment pins in group_vars, so no release is named on the command line.
The deploy tool runs its own template lint that bans self-ternary presence
checks (`x if x else ""`) in templates; use a block `if`.

The controller needs `yanglint` on PATH for `make yang-validate`,
`make yang-validate-instances`, and the deploy-time check.

### File structure

Inventory and templates carry the provider list and render from it:

- `ansible/inventory/group_vars/mwan_servers.yml` and `mwan_suburban_servers.yml`: the `mwan_providers` list, `mwan_hash_mode`, `mwan_pin_provider`, the renamed pinned-destination lists.
- `ansible/inventory/group_vars/all/vars.yml`: `mwan_reserved_tables`, read by the gateway and hypervisor groups.
- `mwan/config/network.json.j2`, `mwan/config/nftables.conf.j2`, `mwan/config/rt_tables.j2`: loop over the list.
- `mwan/yang/goodkind-mwan-steering@2026-09-02.yang`: the reserved-tables leaf-list.

The daemon reads the list and steers from it:

- `mwan/go/internal/networkjson`: loads tier, weight, hash mode, reserved tables; runs the provider-set checks.
- `mwan/go/internal/netif`: the active-tier function both modules share.
- `mwan/go/internal/ifmgr/modules/wanroutes`: name constants gone; catch-all from tiers.
- `mwan/go/internal/ifmgr/modules/steering`: the new balancer module and its nft applier and watcher.
- `mwan/go/internal/statuspush`: the vsock status sender and listener.
- `mwan/go/internal/ifmgr/modules/health`: pushes the verdict.
- `mwan/go/internal/watchdog`: listens; drops the interface list and per-interface pings.
- `mwan/go/cmd/mwan`: module config plumbing, publishing, debug CLI.

The testbed gains a provider:

- `ansible/inventory/group_vars/all/service_mapping.yml`, `opentofu/suburban/{networks,containers,vms}.tf`, `ansible/inventory/group_vars/suburban_servers.yml`, `mwan/networkd/30-astound.{link,network}.j2`.

---

### What the source says that the tasks follow

Each item below was read out of the tree at commit e0d159cc on branch
`mwan-324-design`, and each task below follows the source rather than the
design contract where the two differ. Every line number in this plan cites
that commit. A task that runs after an earlier task in the same file finds
those lines shifted, so re-read the file and anchor on the quoted text rather
than the number.

- **The schema revision ships before the rendered file.** The daemon parses
  `network.json` with libyang under strict parsing, so a file carrying
  `reserved-tables` against the installed `@2026-08-30` revision is rejected
  at load. `deploy-mwan` installs the model revision itself before it writes
  any gateway file (Task 10), so one command carries the order. Task 1 lands
  first for the same reason: Task 2's loader tests validate a document
  carrying the new leaf-list.
- **Every template edit survives the repository's template lint.** The lint
  parses `.j2` files and playbook expressions and rejects a default or
  presence check on a declared input variable: the `default` and `d` filters,
  the `defined`, `undefined`, and `none` tests, `x in groups`, `x in hostvars`,
  `x in vars`, a `.get()` call, a `| length` operand of a comparison, and a
  self-ternary `{{ X ... if X else ... }}`. The rules live in
  `scripts/internal/lint/expr.go:227-325`. Every template in this plan uses a
  block `if` and typed empty values, and every provider entry carries every
  key, with an optional value written as `""` or `[]` rather than left absent.
- **The status package carries no build tag.** `mwan/go/internal/ops/ops.go`
  imports the vsock library with no build constraint and the watchdog compiles
  on darwin today, so a linux-tagged status package imported from the watchdog
  would break that build. The library returns an error at dial time on other
  platforms, which is why the untagged import works.
- **The hypervisor's interface list is in `config-host.toml.j2`, not the
  topology file.** `config.Load` reads exactly one file, and the watchdog unit
  sets no `EnvironmentFile`, so `proxmox/config/mwan-network.toml.j2` renders
  a file nothing reads. Task 7 deletes the `wan_interfaces` blocks from all
  three templates anyway, because a dead loop that names providers is the
  hand-typed list this epic removes.
- **The astound container copies the AT&T shape for its simulated link.**
  The Monkeybrains simulator carries an IPv6 address on that link because it
  serves SLAAC; astound serves prefix delegation only, so its link needs only
  link-local, which is the AT&T simulator's shape.
- **OpenTofu runs through the repository wrapper.** A bare `tofu` has neither
  the state-backend keys nor the Proxmox provider tokens; the wrapper injects
  both from the vault and forwards every argument, so the apply is
  `./configsctl tofu apply -target=module.suburban`.
- **The deploy stops checking the gateway's NICs.** The monkeybrains NIC
  assertion reads a variable this epic deletes, and a per-provider
  hardware-address form would fail on production, where AT&T rides an X710
  virtual function and Webpass a full NIC passthrough that `qm config` never
  lists as network devices. The operator ruled that the deploy is not the
  place for that check, so Task 5 deletes it and adds no replacement.
- **Every merge leaves main deployable, and every merge is deployed before
  the next task starts.** A pull request whose merge would make a deploy
  from main fail a host is not ready to merge (operator ruling 2026-09-07).
  Task 2's loader requires the steering leaves, so Task 5, which renders
  them, lands and deploys (testbed, live validation, then production)
  before Task 3 merges; Task 5 consumes only Task 1 and the loader. Task 10
  (the stack install folded into `deploy-mwan`) rides the Task 5 pull
  request, so that first deploy is one command. Each
  later task follows the same cycle: merge, testbed deploy, live validation,
  production deploy, then the next branch.
- **The published hash mode comes from the loaded configuration.** The
  routing task publishes `hash-mode` from the daemon configuration the loader
  fills, not from the steering module's config, so no task imports a package
  a later task creates.

### Open items for the operator

- Three kernel-facing claims in Task 4 (the chain's rendered priority, the
  map's byte order, and the two hash modes) are proven against the library
  source and must be read back on the testbed before production, as listed
  under "What the cutover capture must show".

---

### Task 1: the schema revision carries the reserved table set

The model gains one node: the group-wide list of routing tables no provider may
take. The daemon reads it from the served configuration instead of holding a
copy, which is what lets a fourth provider be checked against the same set the
operator typed.

The repository keeps exactly one revision file at a time and bumps it by
renaming, the way commit b4111130 recorded the `@2026-08-28` to `@2026-08-29`
bump. Everything that finds the model by glob needs no edit, and this task
proves which places those are rather than assuming: `mwan/go/Makefile:168` and
`:191` glob `$(YANG_DIR)/*.yang`, `ansible/playbooks/deploy-mwan.yml:38` uses
`query('fileglob', ...)`, `mwan/go/internal/networkjson/networkjson_test.go:29`
and `mwan/go/cmd/mwan/wanconfig_selftest_linux_test.go:31` use `filepath.Glob`,
and `mwan/go/cmd/mwan/wanconfig_selftest_linux.go:207` carries the pattern
`goodkind-mwan-steering@*.yang`. The only two places that name the revision in
full are in the stack deploy.

**Files:**
- Rename: `mwan/yang/goodkind-mwan-steering@2026-08-30.yang` to
  `mwan/yang/goodkind-mwan-steering@2026-09-02.yang`
- Modify: the renamed file, at `:52` (insert a revision statement above the
  existing one) and `:381-400` (insert the leaf-list after `hash-mode`)
- Modify: `mwan/yang/instances/network-min.json:1-55` (steering containers on
  both provider interfaces, plus `hash-mode` and `reserved-tables` on the
  group)
- Modify: `ansible/playbooks/deploy-wanconfig-stack.yml:49` and `:141`

**Interfaces:**
- Consumes: nothing.
- Produces: the model revision `goodkind-mwan-steering@2026-09-02`, whose new
  configuration node is
  `/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/reserved-tables`,
  a `leaf-list` of `uint32`. Task 5's renderer emits it; the Go loader task
  reads it into `networkjson.Config.ReservedTables`.
- Produces: the already-defined nodes this revision now proves in an instance,
  which Task 5's renderer starts emitting:
  `/ietf-interfaces:interfaces/interface[name]/goodkind-mwan-steering:steering`
  with `tier` (mandatory) and `weight` (default 1), and
  `/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/hash-mode`.

- [ ] **Step 1: Extend the instance fixture with the nodes the renderer will emit**

Replace the whole of `mwan/yang/instances/network-min.json` with:

```json
{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:wan": {
          "name": "webpass",
          "table-id": 200,
          "fw-mark": 2,
          "fw-mark-prio": 200,
          "from-prio": 56,
          "npt-prefix": "2001:db8:beef:200::/60",
          "v4-source": "203.0.113.2",
          "health": {
            "enabled": true,
            "ping-count": 3,
            "success-threshold": 2,
            "failure-threshold": 2,
            "recovery-threshold": 2,
            "check-interval": 10,
            "targets-v4": ["192.0.2.10", "192.0.2.11"],
            "targets-v6": ["2001:db8:53::1", "2001:db8:53::2"],
            "http-urls": ["https://example.test/ip"]
          }
        },
        "goodkind-mwan-steering:steering": {
          "tier": 0,
          "weight": 1
        }
      },
      {
        "name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:wan": {
          "name": "att",
          "table-id": 100,
          "fw-mark": 1,
          "fw-mark-prio": 100,
          "from-prio": 55,
          "npt-prefix": "2001:db8:beef:100::/60"
        },
        "goodkind-mwan-steering:steering": {
          "tier": 1,
          "weight": 3
        }
      },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }
    ],
    "goodkind-mwan-steering:steering-group": {
      "hash-mode": "source",
      "reserved-tables": [400, 500],
      "translation": {
        "internal-prefix": "2001:db8:b01::/60",
        "opnsense-edge-v6": "2001:db8:b01:fe::2",
        "mwanbr-edge-v6": "2001:db8:b01:fe::3"
      },
      "routes": {
        "internal-iface": "enmwanbr0",
        "internal-net-v4": "192.0.2.0/29"
      },
      "health": { "probe-timeout": 2000 }
    }
  }
}
```

The fixture carries a non-default `hash-mode` and a weight above one on
purpose, so a schema change that narrowed either would fail here rather than in
production. The addresses stay documentation ranges, so no inventory value
enters the repository.

- [ ] **Step 2: Run the instance gate to verify it fails**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make yang-validate-instances`

Expected: FAIL, nonzero exit. The installed `@2026-08-30` revision defines no
`reserved-tables` under `steering-group`, so yanglint reports a message of the
form `Node "reserved-tables" not found in the "goodkind-mwan-steering" module`
with the instance path, and the target's `|| exit 1` stops the loop.

- [ ] **Step 3: Rename the model file to the new revision**

```bash
cd "$(git rev-parse --show-toplevel)"
git mv mwan/yang/goodkind-mwan-steering@2026-08-30.yang \
       mwan/yang/goodkind-mwan-steering@2026-09-02.yang
```

- [ ] **Step 4: Add the revision statement**

In `mwan/yang/goodkind-mwan-steering@2026-09-02.yang`, directly above the
existing `revision 2026-08-30 {` line, insert:

```yang
  revision 2026-09-02 {
    description
      "Carry the routing tables no provider may take, so the gateway checks a
       provider's table against the set the operator typed rather than against
       a copy each reader holds. Every addition is a new node: no node defined
       by an earlier revision changes name, type, or shape.";
  }
```

- [ ] **Step 5: Add the leaf-list to the steering group**

In the same file, inside `container steering-group`, directly after the closing
brace of `leaf hash-mode` and before `container translation`, insert:

```yang
      leaf-list reserved-tables {
        type uint32;
        description
          "Routing tables the gateway holds for something other than a
           provider: the tunnel table and the out-of-band table today. A
           provider whose table is in this set is a configuration error, and
           the gateway stops before it writes to the kernel. The kernel's own
           tables are not listed here, because they are fixed rather than
           configured.";
      }
```

- [ ] **Step 6: Run both YANG gates to verify they pass**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make yang-validate yang-validate-instances`

Expected: PASS, exit 0. `yang-validate` prints the yanglint version and then
nothing; `yang-validate-instances` prints
`yanglint -t config ../yang/instances/network-min.json` and then nothing.
yanglint is silent on a valid instance.

- [ ] **Step 7: Point the stack deploy at the new revision**

In `ansible/playbooks/deploy-wanconfig-stack.yml`, change line 49 from:

```yaml
      - name: goodkind-mwan-steering@2026-08-30
```

to:

```yaml
      - name: goodkind-mwan-steering@2026-09-02
```

and change line 141, the last entry of the "Copy the gateway model and its IETF
imports" loop, from:

```yaml
        - "../../mwan/yang/goodkind-mwan-steering@2026-08-30.yang"
```

to:

```yaml
        - "../../mwan/yang/goodkind-mwan-steering@2026-09-02.yang"
```

- [ ] **Step 8: Verify the plays still parse**

```bash
cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:all
```

Expected: PASS, exit 0.

- [ ] **Step 9: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/yang/goodkind-mwan-steering@2026-09-02.yang mwan/yang/instances/network-min.json ansible/playbooks/deploy-wanconfig-stack.yml
git commit -S -m "Add the reserved routing tables to goodkind-mwan-steering revision 2026-09-02" -m "Carry the tables no provider may take as a group-wide leaf-list the gateway reads from its own configuration, cover the steering container and the group hash mode in the instance fixture so the yanglint gate proves the shape the renderer emits, and point deploy-wanconfig-stack.yml at the new revision." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

---

### Task 2: the loader carries steering, and the provider-set checks move into it

The network configuration loader learns the two steering leaves, the group's
hash mode, and the reserved-table list, and it becomes the one place the whole
provider set is checked. The four checks need every provider at once and the
reserved list beside them, which is exactly what this package holds and what
`wan.routes` does not: a module config is built one module at a time, after the
file is already accepted.

`wan.routes` keeps only structural checks in Task 3. Both halves land inside
one release, so no window exists where one layer admits a fourth provider and
another refuses it.

**Files:**
- Modify: `mwan/go/internal/networkjson/networkjson.go:14-23` (imports),
  `:45-49` (`ifaceEntry`), `:74-93` (`steeringGroup`), `:95-106` (`Config`),
  `:146-198` (`build`), `:200-245` (`buildProvider`), `:319-351` (`Apply`)
- Modify: `mwan/go/internal/networkjson/networkjson_test.go:52-106`
  (`validDocument`) and append new tests after `:232`
- Modify: `mwan/go/internal/config/ifmgr_modules.go:94-107` (`IfMgrWANEntry`)
- Modify: `mwan/go/internal/config/config.go:416-433` (`IfMgrSection`)

**Interfaces:**
- Consumes: the model revision's `steering` container (`tier`, `weight`), the
  `steering-group/hash-mode` leaf, and the new `steering-group/reserved-tables`
  leaf-list from the schema task.
- Produces: `config.IfMgrWANEntry` fields `Tier uint8` and `Weight int`, which
  Task 3's `sharedWAN` and Task 4's `steering.Member` read.
- Produces: `config.IfMgrSection` fields `HashMode string` and
  `ReservedTables []int`, which Task 4's `buildSteeringConfig` reads.
- Produces: `networkjson.Config` fields `HashMode string` and
  `ReservedTables []int`.
- Produces: the load-time contract that a provider set reaching any module has
  unique routing numbers, sits on no reserved table, and carries a weight of at
  least one.

- [ ] **Step 1: Extend the loader test fixture and add the failing tests**

In `mwan/go/internal/networkjson/networkjson_test.go`, replace `validDocument`
at `:52-106` with the version below. The three replacement targets the existing
tests use (`"fw-mark": 2,`, `"table-id": 100,`, and
`"npt-prefix": "2001:db8:beef:100::/60"`) are preserved verbatim, so those tests
keep working. AT&T moves to tier 1 and Webpass takes weight 2 so both new leaves
are observable rather than defaulted.

```go
// validDocument is one gateway's network tree: two providers in different
// tiers, one of them with an IPv4 source pin and one with no probe at all,
// plus the internal link and the group-wide values. Addresses are
// documentation prefixes.
const validDocument = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:steering": { "tier": 0, "weight": 2 },
        "goodkind-mwan-steering:wan": {
          "name": "webpass",
          "table-id": 200,
          "fw-mark": 2,
          "fw-mark-prio": 200,
          "from-prio": 56,
          "npt-prefix": "2001:db8:beef:200::/60",
          "v4-source": "203.0.113.2",
          "health": {
            "enabled": true,
            "ping-count": 3,
            "success-threshold": 2,
            "failure-threshold": 2,
            "recovery-threshold": 2,
            "check-interval": 10,
            "targets-v4": ["192.0.2.10", "192.0.2.11"],
            "targets-v6": ["2001:db8:53::1", "2001:db8:53::2"],
            "http-urls": ["https://example.test/ip"]
          }
        }
      },
      {
        "name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:steering": { "tier": 1, "weight": 1 },
        "goodkind-mwan-steering:wan": {
          "name": "att",
          "table-id": 100,
          "fw-mark": 1,
          "fw-mark-prio": 100,
          "from-prio": 55,
          "npt-prefix": "2001:db8:beef:100::/60"
        }
      },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }
    ],
    "goodkind-mwan-steering:steering-group": {
      "hash-mode": "source",
      "reserved-tables": [400, 500],
      "translation": {
        "internal-prefix": "2001:db8:b01::/60",
        "opnsense-edge-v6": "2001:db8:b01:fe::2",
        "mwanbr-edge-v6": "2001:db8:b01:fe::3"
      },
      "routes": {
        "internal-iface": "enmwanbr0",
        "internal-net-v4": "192.0.2.0/29"
      },
      "health": { "probe-timeout": 2000 }
    }
  }
}`
```

Then append these tests to the end of the file, after
`TestLoadAcceptsADisabledProbeWithNoSettings` at `:232`:

```go
func TestLoadCarriesSteeringAndTheGroupSettings(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := loaded.WAN["webpass"].Tier; got != 0 {
		t.Fatalf("webpass tier = %d, want 0", got)
	}
	if got := loaded.WAN["webpass"].Weight; got != 2 {
		t.Fatalf("webpass weight = %d, want 2", got)
	}
	if got := loaded.WAN["att"].Tier; got != 1 {
		t.Fatalf("att tier = %d, want 1", got)
	}
	if got := loaded.WAN["att"].Weight; got != 1 {
		t.Fatalf("att weight = %d, want 1", got)
	}
	if got := loaded.HashMode; got != "source" {
		t.Fatalf("hash mode = %q, want source", got)
	}
	if !reflect.DeepEqual(loaded.ReservedTables, []int{400, 500}) {
		t.Fatalf("reserved tables = %v, want [400 500]", loaded.ReservedTables)
	}
}

func TestLoadRejectsAProviderWithNoSteeringContainer(t *testing.T) {
	t.Parallel()

	// Tier decides which providers carry traffic, so a provider that does not
	// say where it sits cannot be steered at all. The schema cannot require the
	// container, because an interface that carries no provider must be free of
	// it, so the requirement lives here.
	body := strings.Replace(
		validDocument,
		`"goodkind-mwan-steering:steering": { "tier": 1, "weight": 1 },`,
		``,
		1,
	)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider with no steering container")
	}
	if !strings.Contains(err.Error(), "steering is required") {
		t.Fatalf("error does not name the missing container: %v", err)
	}
}

func TestLoadRejectsAMissingWeight(t *testing.T) {
	t.Parallel()

	// The schema defaults weight to 1 for the served tree. That default never
	// reaches the file the daemon decodes, so a document with no weight would
	// silently balance a provider at zero share. The loader refuses it instead.
	body := strings.Replace(
		validDocument,
		`"goodkind-mwan-steering:steering": { "tier": 0, "weight": 2 },`,
		`"goodkind-mwan-steering:steering": { "tier": 0 },`,
		1,
	)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider with no weight")
	}
	if !strings.Contains(err.Error(), "steering/weight is required") {
		t.Fatalf("error does not name the missing leaf: %v", err)
	}
}

func TestLoadRejectsAMissingHashMode(t *testing.T) {
	t.Parallel()

	// The renderer always emits the hash mode, and the steering module switches
	// on it, so an absent value is a rendering fault rather than a request for
	// the schema default.
	body := strings.Replace(validDocument, `"hash-mode": "source",`, ``, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a steering group with no hash mode")
	}
	if !strings.Contains(err.Error(), "hash-mode") {
		t.Fatalf("error does not name the missing leaf: %v", err)
	}
}

func TestLoadRejectsDuplicateRoutingNumbers(t *testing.T) {
	t.Parallel()

	// Each of the four numbers addresses a distinct kernel slot, and two
	// providers sharing one means the second silently takes the first's
	// traffic. Nothing derives them, so this is the only check that catches a
	// typo in inventory.
	cases := map[string]struct {
		from string
		to   string
		leaf string
	}{
		"table":        {from: `"table-id": 100,`, to: `"table-id": 200,`, leaf: "table-id"},
		"mark":         {from: `"fw-mark": 1,`, to: `"fw-mark": 2,`, leaf: "fw-mark"},
		"mark priority": {from: `"fw-mark-prio": 100,`, to: `"fw-mark-prio": 200,`, leaf: "fw-mark-prio"},
		"from priority": {from: `"from-prio": 55,`, to: `"from-prio": 56,`, leaf: "from-prio"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := strings.Replace(validDocument, tc.from, tc.to, 1)
			_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
			if err == nil {
				t.Fatalf("Load accepted a duplicate %s", tc.leaf)
			}
			if !strings.Contains(err.Error(), tc.leaf) {
				t.Fatalf("error does not name %s: %v", tc.leaf, err)
			}
		})
	}
}

func TestLoadRejectsAProviderOnAReservedTable(t *testing.T) {
	t.Parallel()

	// The tunnel holds table 400 and the out-of-band path holds 500. A provider
	// on either would install a default route the tunnel's own rules then
	// select, which is an outage nobody would attribute to an inventory edit.
	body := strings.Replace(validDocument, `"table-id": 100,`, `"table-id": 400,`, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider on a reserved table")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error does not say the table is reserved: %v", err)
	}
}

func TestLoadRejectsAProviderOnAKernelTable(t *testing.T) {
	t.Parallel()

	// The kernel's own tables are reserved whether or not inventory says so,
	// because they are not inventory values. Table 254 is main: a provider
	// there would replace the host's own default route.
	body := strings.Replace(validDocument, `"table-id": 100,`, `"table-id": 254,`, 1)
	body = strings.Replace(body, `"reserved-tables": [400, 500],`, ``, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider on a kernel table")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error does not say the table is reserved: %v", err)
	}
}

func TestLoadAcceptsAGroupWithNoReservedTables(t *testing.T) {
	t.Parallel()

	// An empty leaf-list renders as an absent key, so a gateway that reserves
	// nothing beyond the kernel's own tables must load.
	body := strings.Replace(validDocument, `"reserved-tables": [400, 500],`, ``, 1)
	loaded, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load rejected a group with no reserved tables: %v", err)
	}
	if len(loaded.ReservedTables) != 0 {
		t.Fatalf("reserved tables = %v, want none", loaded.ReservedTables)
	}
}
```

Add `"reflect"` to the test file's import block at `:5-12`, which currently
holds `os`, `path/filepath`, `strings`, `testing`, and the loader package.

- [ ] **Step 2: Run the loader tests to verify they fail**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/networkjson/ -v
```

Expected: FAIL to build, with `loaded.HashMode undefined (type *networkjson.Config
has no field or method HashMode)` and the same for `ReservedTables`, `Tier`, and
`Weight` on `config.IfMgrWANEntry`.

- [ ] **Step 3: Add the two fields to the provider entry**

In `mwan/go/internal/config/ifmgr_modules.go`, replace the `IfMgrWANEntry` block
at `:94-107` with:

```go
// IfMgrWANEntry is one provider's routing configuration, keyed by provider
// name. It comes from network.json: the interface the provider rides, the
// policy-routing slots wan.routes owns, and the steering properties the
// balancer reads. Modules read the fields they need; npt uses only the name and
// interface. The shared internal prefix and edge addresses live on
// IfMgrSection, because no single provider owns them.
type IfMgrWANEntry struct {
	Iface      string
	TableID    int
	FwMark     int
	FwMarkPrio int
	FromPrio   int
	NptPrefix  string
	V4Source   string
	// Tier is the preference tier. The lowest-numbered tier holding at least
	// one healthy provider is the tier that carries new connections.
	Tier uint8
	// Weight is this provider's share of its tier, at least one. The loader
	// refuses a missing or smaller value rather than defaulting it, because a
	// zero share would make the balancer's divisor wrong.
	Weight int
}
```

- [ ] **Step 4: Add the group settings to the ifmgr section**

In `mwan/go/internal/config/config.go`, replace lines `:426-432` with:

```go
	InternalPrefix string                       `toml:"-"`
	OpnsenseEdgeV6 string                       `toml:"-"`
	MwanbrEdgeV6   string                       `toml:"-"`
	// HashMode and ReservedTables come from network.json's steering group
	// beside the WAN map, so the same skip tag keeps a stale config.toml key
	// out of them. HashMode decides how the steering module assigns a new
	// connection; ReservedTables is the set no provider may route into.
	HashMode       string                       `toml:"-"`
	ReservedTables []int                        `toml:"-"`
	Iface          map[string]IfMgrIfaceSection `toml:"iface"`
	Modules        IfMgrModulesSection          `toml:"modules"`
	Alerts         IfMgrAlertsSection           `toml:"alerts"`
	WAN            map[string]IfMgrWANEntry     `toml:"-"`
```

- [ ] **Step 5: Decode the new leaves**

In `mwan/go/internal/networkjson/networkjson.go`, replace the `ifaceEntry` type
at `:45-49` with:

```go
type ifaceEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	WAN      *wan      `json:"goodkind-mwan-steering:wan"`
	Steering *steering `json:"goodkind-mwan-steering:steering"`
}

// steering is the member's steering properties: which tier it sits in and how
// much of that tier's traffic it takes. Both are pointers so an absent leaf is
// distinguishable from a zero the daemon would act on. Tier zero is the
// preferred tier, and weight zero would make the balancer's divisor wrong.
type steering struct {
	Tier   *int `json:"tier"`
	Weight *int `json:"weight"`
}
```

Replace the `steeringGroup` type at `:74-78` with:

```go
type steeringGroup struct {
	HashMode       string      `json:"hash-mode"`
	ReservedTables []int       `json:"reserved-tables"`
	Translation    translation `json:"translation"`
	Routes         routes      `json:"routes"`
	Health         groupHealth `json:"health"`
}
```

Replace the `Config` type at `:95-106` with:

```go
// Config is the network tree one file carries, in the shape the daemon's
// configuration holds it.
type Config struct {
	InternalPrefix     string
	OpnsenseEdgeV6     string
	MwanbrEdgeV6       string
	InternalIface      string
	InternalNetV4      string
	HashMode           string
	ReservedTables     []int
	ProbeTimeoutMillis int
	WAN                map[string]config.IfMgrWANEntry
	Health             map[string]config.IfMgrHealthWANSection
}
```

- [ ] **Step 6: Read the steering container in the provider builder**

In the same file, replace `buildProvider` at `:200-245` with:

```go
// buildProvider turns one interface's provider entry into the two sections the
// daemon holds it in: the routing entry keyed by provider name, and the health
// policy under the same name. A nil probe is how a provider the gateway does
// not probe is expressed, which is one of the two ways the daemon already
// declines to probe a provider.
func buildProvider(entry ifaceEntry) (config.IfMgrWANEntry, *config.IfMgrHealthWANSection, error) {
	provider := entry.WAN
	if provider.Name == "" {
		return config.IfMgrWANEntry{}, nil, fmt.Errorf("interface %q carries a provider with no name", entry.Name)
	}
	label := "wan " + provider.Name
	numbers := []struct {
		leaf  string
		value *int
	}{
		{leaf: "table-id", value: provider.TableID},
		{leaf: "fw-mark", value: provider.FwMark},
		{leaf: "fw-mark-prio", value: provider.FwMarkPrio},
		{leaf: "from-prio", value: provider.FromPrio},
	}
	for _, number := range numbers {
		if number.value == nil {
			return config.IfMgrWANEntry{}, nil, fmt.Errorf("%s: %s is required", label, number.leaf)
		}
	}
	if provider.NptPrefix == "" {
		return config.IfMgrWANEntry{}, nil, fmt.Errorf("%s: npt-prefix is required", label)
	}
	tier, weight, err := buildSteering(label, entry.Steering)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	routing := config.IfMgrWANEntry{
		Iface:      entry.Name,
		TableID:    *provider.TableID,
		FwMark:     *provider.FwMark,
		FwMarkPrio: *provider.FwMarkPrio,
		FromPrio:   *provider.FromPrio,
		NptPrefix:  provider.NptPrefix,
		V4Source:   provider.V4Source,
		Tier:       tier,
		Weight:     weight,
	}
	if provider.Health == nil {
		return routing, nil, nil
	}
	probe, err := buildHealth(label, provider.Health)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	return routing, probe, nil
}

// buildSteering reads one provider's tier and weight. The schema cannot require
// the container, because an interface carrying no provider must be free of it,
// and the schema's weight default fills only the served tree rather than the
// file this decodes. Both values are therefore required here: a provider with
// no tier cannot be placed in the failover order, and one with no weight cannot
// be given a share of its tier.
func buildSteering(label string, member *steering) (uint8, int, error) {
	if member == nil {
		return 0, 0, fmt.Errorf("%s: steering is required", label)
	}
	if member.Tier == nil {
		return 0, 0, fmt.Errorf("%s: steering/tier is required", label)
	}
	if member.Weight == nil {
		return 0, 0, fmt.Errorf("%s: steering/weight is required", label)
	}
	tier := *member.Tier
	if tier < 0 || tier > int(^uint8(0)) {
		return 0, 0, fmt.Errorf("%s: steering/tier %d is outside 0 to 255", label, tier)
	}
	return uint8(tier), *member.Weight, nil
}
```

- [ ] **Step 7: Fill the group settings and run the provider-set checks**

In the same file, replace `build` at `:146-198` with:

```go
// kernelReservedTables are the routing tables the kernel owns: unspecified,
// default, main, and local. They are not inventory values, so they are reserved
// here rather than typed into the reserved-tables leaf-list, which carries only
// the tables this gateway's other software claims.
var kernelReservedTables = []int{0, 253, 254, 255}

// build turns the decoded document into the daemon's shape, rejecting a value
// the schema cannot require. The schema makes a provider's name mandatory and
// bounds the table id and firewall mark; it leaves the rest optional, because a
// leaf's type cannot see its siblings. The daemon needs them all, so the check
// lives here.
func build(doc *document) (*Config, error) {
	group := doc.Interfaces.SteeringGroup
	loaded := &Config{
		InternalPrefix:     group.Translation.InternalPrefix,
		OpnsenseEdgeV6:     group.Translation.OpnsenseEdgeV6,
		MwanbrEdgeV6:       group.Translation.MwanbrEdgeV6,
		InternalIface:      group.Routes.InternalIface,
		InternalNetV4:      group.Routes.InternalNetV4,
		HashMode:           group.HashMode,
		ReservedTables:     group.ReservedTables,
		ProbeTimeoutMillis: 0,
		WAN:                make(map[string]config.IfMgrWANEntry, len(doc.Interfaces.Interface)),
		Health:             make(map[string]config.IfMgrHealthWANSection, len(doc.Interfaces.Interface)),
	}
	required := []struct {
		leaf  string
		value string
	}{
		{leaf: "steering-group/translation/internal-prefix", value: loaded.InternalPrefix},
		{leaf: "steering-group/translation/opnsense-edge-v6", value: loaded.OpnsenseEdgeV6},
		{leaf: "steering-group/translation/mwanbr-edge-v6", value: loaded.MwanbrEdgeV6},
		{leaf: "steering-group/routes/internal-iface", value: loaded.InternalIface},
		{leaf: "steering-group/routes/internal-net-v4", value: loaded.InternalNetV4},
		{leaf: "steering-group/hash-mode", value: loaded.HashMode},
	}
	for _, leaf := range required {
		if leaf.value == "" {
			return nil, fmt.Errorf("%s is required", leaf.leaf)
		}
	}
	if group.Health.ProbeTimeout == nil {
		return nil, errors.New("steering-group/health/probe-timeout is required")
	}
	loaded.ProbeTimeoutMillis = *group.Health.ProbeTimeout

	for _, entry := range doc.Interfaces.Interface {
		if entry.WAN == nil {
			continue
		}
		routing, probe, err := buildProvider(entry)
		if err != nil {
			return nil, err
		}
		if _, seen := loaded.WAN[entry.WAN.Name]; seen {
			return nil, fmt.Errorf("provider %q appears on more than one interface", entry.WAN.Name)
		}
		loaded.WAN[entry.WAN.Name] = routing
		if probe != nil {
			loaded.Health[entry.WAN.Name] = *probe
		}
	}
	if len(loaded.WAN) == 0 {
		return nil, errors.New("no interface carries a provider")
	}
	if err := checkProviderSet(loaded); err != nil {
		return nil, err
	}
	return loaded, nil
}

// checkProviderSet runs the checks that need the whole provider set rather than
// one entry: every routing number is unique across providers, no provider sits
// on a reserved table, and every weight is at least one. Nothing derives these
// numbers, so a typo in inventory has no other place to surface. A failure here
// stops the daemon before it writes anything to the kernel, which is the
// existing failure contract for a bad configuration.
func checkProviderSet(loaded *Config) error {
	reserved := make(map[int]string, len(loaded.ReservedTables)+len(kernelReservedTables))
	for _, table := range kernelReservedTables {
		reserved[table] = "the kernel"
	}
	for _, table := range loaded.ReservedTables {
		reserved[table] = "steering-group/reserved-tables"
	}

	names := make([]string, 0, len(loaded.WAN))
	for name := range loaded.WAN {
		names = append(names, name)
	}
	slices.Sort(names)

	slots := []struct {
		leaf  string
		value func(entry config.IfMgrWANEntry) int
	}{
		{leaf: "table-id", value: func(entry config.IfMgrWANEntry) int { return entry.TableID }},
		{leaf: "fw-mark", value: func(entry config.IfMgrWANEntry) int { return entry.FwMark }},
		{leaf: "fw-mark-prio", value: func(entry config.IfMgrWANEntry) int { return entry.FwMarkPrio }},
		{leaf: "from-prio", value: func(entry config.IfMgrWANEntry) int { return entry.FromPrio }},
	}
	for _, slot := range slots {
		owner := make(map[int]string, len(names))
		for _, name := range names {
			value := slot.value(loaded.WAN[name])
			if taken, seen := owner[value]; seen {
				return fmt.Errorf("wan %s: %s %d is already taken by wan %s",
					name, slot.leaf, value, taken)
			}
			owner[value] = name
		}
	}

	for _, name := range names {
		entry := loaded.WAN[name]
		if owner, isReserved := reserved[entry.TableID]; isReserved {
			return fmt.Errorf("wan %s: table-id %d is reserved by %s", name, entry.TableID, owner)
		}
		if entry.Weight < 1 {
			return fmt.Errorf("wan %s: steering/weight must be at least 1, got %d", name, entry.Weight)
		}
	}
	return nil
}
```

Add `"slices"` to the import block at `:14-23`, between `os` and the module
imports.

- [ ] **Step 8: Write the group settings onto the daemon configuration**

In the same file, replace the first four lines of `Apply` at `:322-326` with:

```go
func (c *Config) Apply(cfg *config.Config) {
	cfg.IfMgr.InternalPrefix = c.InternalPrefix
	cfg.IfMgr.OpnsenseEdgeV6 = c.OpnsenseEdgeV6
	cfg.IfMgr.MwanbrEdgeV6 = c.MwanbrEdgeV6
	cfg.IfMgr.HashMode = c.HashMode
	cfg.IfMgr.ReservedTables = c.ReservedTables
	cfg.IfMgr.WAN = c.WAN
```

The rest of `Apply` is unchanged.

- [ ] **Step 9: Run the loader tests to verify they pass**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/networkjson/ -v
```

Expected: PASS. Twelve `--- PASS` lines plus the four duplicate-number subtests,
`ok goodkind.io/mwan/internal/networkjson`, exit 0.

- [ ] **Step 10: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/go/internal/networkjson mwan/go/internal/config
git commit -S -m "Load steering tier and weight and check the whole provider set at load" -m "networkjson reads the per-provider steering container and the group's hash mode and reserved-tables leaf-list, requiring tier, weight, and hash-mode rather than defaulting them, and rejects a provider set whose routing numbers collide, whose table is reserved by the kernel or by inventory, or whose weight is below one. config.IfMgrWANEntry gains Tier and Weight; config.IfMgrSection gains HashMode and ReservedTables, both filled by Apply." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: tiers become data, and the three provider names leave wan.routes

The routing module stops knowing any provider by name. It reads each provider's
tier from configuration, computes the active tier through one shared function,
and generalizes the Monkeybrains catch-all into a rule that any single healthy
provider in a non-first tier gets. The two fixed priority validators go with the
names, replaced by the load-time checks Task 2 installed.

The shared active-tier function goes in `internal/netif` rather than in a
package named for steering, because the module Task 4 adds is called
`steering` and would import a package of the same name in a cycle. `netif`
already owns `HealthStates` and `HealthIsHealthy`, which the function reads.

**Files:**
- Create: `mwan/go/internal/netif/tier.go`
- Create: `mwan/go/internal/netif/tier_test.go`
- Modify: `mwan/go/internal/ifmgr/modules/wanroutes/wanroutes.go:19-60`,
  `:77-90`, `:202-230`, `:297-341`, `:559-629`, `:645-675`
- Modify: `mwan/go/internal/ifmgr/modules/wanroutes/wanroutes_test.go` (whole
  file; every reference to the three name constants goes)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go:650-661` (`sharedWAN`),
  `:686-711` (`buildWANRefs`), `:713-757` (`buildWANRoutesConfig`)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go:126-161`,
  `:166-197`, `:199-242`
- Modify: `mwan/go/internal/wanconfig/tree.go:34-50` (`Member`), `:105-116`
  (`Gateway`), `:127-146` (constants), `:156-177` (`ConfigItems`), `:293-304`
  (`steeringItems`), `:332-359` (`validate`)
- Modify: `mwan/go/internal/wanconfig/tree_test.go:17-79`, `:84-101`, `:203-232`
- Modify: `mwan/go/cmd/mwan/wanconfig_publish_linux.go:294-351`
- Modify: `mwan/go/cmd/mwan/wanconfig_publish_linux_test.go:20-106`
- Modify: `mwan/go/cmd/mwan/debug_probes_linux.go:583-606`, `:615-631`
- Modify: `mwan/go/cmd/mwan/debug_probes_linux_test.go:46-196`, `:233-260`,
  `:338-360`, `:512-585`, `:651-686`

**Interfaces:**
- Consumes: `config.IfMgrWANEntry.Tier` and `.Weight` from Task 2.
- Produces: `netif.TierMember` and
  `netif.ActiveTier(members []TierMember, health HealthStates) (uint8, bool)`,
  which Task 4's balancer reads.
- Produces: `wanroutes.WAN` fields `Tier uint8` and `Weight int`.
- Produces: `wanconfig.Member.Weight` and `wanconfig.Gateway.HashMode`, which
  become the served leaves `.../goodkind-mwan-steering:steering/weight` and
  `/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/hash-mode`.
- Removes: `wanroutes.TierOf`, `wanroutes.TierPreferred`,
  `wanroutes.TierFallback`, and the unexported `wanNameATT`, `wanNameWebpass`,
  `wanNameMonkeybrains`, `fallbackEnabled`, `findWAN`, `isFwMarkPriority`, and
  `isFromPriority`. `wanconfig_publish_linux.go:328` is the only caller of
  `TierOf` outside the package.

- [ ] **Step 1: Write the failing active-tier test**

Create `mwan/go/internal/netif/tier_test.go`:

```go
//go:build linux

package netif

import "testing"

func TestActiveTier(t *testing.T) {
	t.Parallel()

	members := []TierMember{
		{Name: "att", Tier: 0},
		{Name: "webpass", Tier: 0},
		{Name: "monkeybrains", Tier: 1},
		{Name: "astound", Tier: 2},
	}
	cases := []struct {
		name       string
		health     HealthStates
		wantTier   uint8
		wantHealthy bool
	}{
		{
			name:        "no verdict recorded reads healthy and activates the first tier",
			health:      HealthStates{},
			wantTier:    0,
			wantHealthy: true,
		},
		{
			name: "one healthy member in the first tier keeps it active",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateHealthy,
				"monkeybrains": HealthStateHealthy, "astound": HealthStateHealthy,
			},
			wantTier:    0,
			wantHealthy: true,
		},
		{
			name: "the first tier going unhealthy activates the next one that is not",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateUnhealthy,
				"monkeybrains": HealthStateHealthy, "astound": HealthStateHealthy,
			},
			wantTier:    1,
			wantHealthy: true,
		},
		{
			name: "an empty tier is skipped rather than activated",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateUnhealthy,
				"monkeybrains": HealthStateUnhealthy, "astound": HealthStateHealthy,
			},
			wantTier:    2,
			wantHealthy: true,
		},
		{
			name: "no healthy member anywhere activates no tier",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateUnhealthy,
				"monkeybrains": HealthStateUnhealthy, "astound": HealthStateUnhealthy,
			},
			wantTier:    0,
			wantHealthy: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotTier, gotHealthy := ActiveTier(members, tc.health)
			if gotHealthy != tc.wantHealthy {
				t.Fatalf("healthy = %v, want %v", gotHealthy, tc.wantHealthy)
			}
			if gotHealthy && gotTier != tc.wantTier {
				t.Fatalf("active tier = %d, want %d", gotTier, tc.wantTier)
			}
		})
	}
}

func TestActiveTierWithNoMembers(t *testing.T) {
	t.Parallel()

	if _, healthy := ActiveTier(nil, HealthStates{}); healthy {
		t.Fatal("an empty member list reported a healthy tier")
	}
}
```

- [ ] **Step 2: Run the netif test to verify it fails**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/netif/ -run TestActiveTier -v
```

Expected: FAIL to build, with `undefined: TierMember` and `undefined: ActiveTier`.

- [ ] **Step 3: Write the shared active-tier function**

Create `mwan/go/internal/netif/tier.go`:

```go
//go:build linux

package netif

// TierMember is one steering member's tier membership: the name the health
// state file records a verdict under, and the tier the inventory puts it in.
type TierMember struct {
	Name string
	Tier uint8
}

// ActiveTier returns the lowest-numbered tier holding at least one healthy
// member, and whether any member is healthy at all. The tiers in inventory
// decide the failover order and nothing else does, so this function carries no
// tie-break of its own.
//
// An unknown verdict reads healthy, which is what makes every member usable
// before the health module writes its first state and is the startup behavior
// the gateway has today.
//
// Both the routing module and the steering module decide from this one
// function, so the catch-all route and the balancing marks can never disagree
// about which tier is carrying traffic.
func ActiveTier(members []TierMember, health HealthStates) (uint8, bool) {
	active := uint8(0)
	found := false
	for _, member := range members {
		if !HealthIsHealthy(health.State(member.Name)) {
			continue
		}
		if !found || member.Tier < active {
			active = member.Tier
			found = true
		}
	}
	return active, found
}
```

- [ ] **Step 4: Run the netif test to verify it passes**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/netif/ -run TestActiveTier -v
```

Expected: PASS. Six `--- PASS` lines, exit 0.

- [ ] **Step 5: Rewrite the wan.routes tests around tiers**

Replace `mwan/go/internal/ifmgr/modules/wanroutes/wanroutes_test.go` in full:

```go
//go:build linux

package wanroutes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/wanstate"
)

func TestDesiredState(t *testing.T) {
	t.Parallel()

	baseConfig := testConfig()
	baseGateways := testGateways()
	allHealthy := netif.HealthStates{
		"att":          netif.HealthStateHealthy,
		"webpass":      netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	}
	noneHealthy := netif.HealthStates{
		"att":          netif.HealthStateUnhealthy,
		"webpass":      netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateUnhealthy,
	}

	cases := []struct {
		name       string
		cfg        Config
		gateways   gateways
		health     netif.HealthStates
		wantRules  []netif.DesiredRule
		wantRoutes []netif.RouteSpec
	}{
		{
			name:       "every provider healthy with gateways",
			cfg:        baseConfig,
			gateways:   baseGateways,
			health:     allHealthy,
			wantRules:  allHealthyRules(baseConfig),
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			// No verdict has been recorded yet, so every provider reads healthy
			// and the first tier activates. This is the startup pass.
			name:       "no verdict recorded activates the first tier",
			cfg:        baseConfig,
			gateways:   baseGateways,
			health:     netif.HealthStates{},
			wantRules:  allHealthyRules(baseConfig),
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			name: "unhealthy provider and missing gateway drop enabled rules",
			cfg:  baseConfig,
			gateways: gateways{
				"att":          baseGateways["att"],
				"webpass":      baseGateways["webpass"],
				"monkeybrains": {V4: "198.51.100.1", V6: ""},
			},
			health: netif.HealthStates{
				"att":          netif.HealthStateHealthy,
				"webpass":      netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 100, 1, 100),
				fwmarkRule(familyV6, 100, 1, 100),
				fromRule(55, "3d06:bad:b01:1100::/56", 100),
				fwmarkRule(familyV4, 300, 3, 300),
			},
			wantRoutes: routesForGateways(baseConfig, gateways{
				"att":          baseGateways["att"],
				"webpass":      baseGateways["webpass"],
				"monkeybrains": {V4: "198.51.100.1", V6: ""},
			}),
		},
		{
			// The active tier is not the first configured tier and exactly one
			// provider in it is healthy, so nothing else marks internal traffic
			// and the catch-all pair carries it. This reproduces today's
			// behavior with Monkeybrains alone in the fallback tier.
			name:     "a lone healthy provider below the first tier gets the catch-all",
			cfg:      baseConfig,
			gateways: baseGateways,
			health: netif.HealthStates{
				"att":          netif.HealthStateUnhealthy,
				"webpass":      netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 300, 3, 300),
				fwmarkRule(familyV6, 300, 3, 300),
				fromRule(57, "3d06:bad:b01:3300::/56", 300),
				catchAllRule(familyV4, "vmbr250", 300),
				catchAllRule(familyV6, "vmbr250", 300),
			},
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			// Two healthy providers share the active tier, so the steering
			// module's marks carry the split. A catch-all would send every
			// unmarked packet to one of them and undo it.
			name:     "two healthy providers in the active tier get no catch-all",
			cfg:      configWithWebpassInTierOne(baseConfig),
			gateways: baseGateways,
			health: netif.HealthStates{
				"att":          netif.HealthStateUnhealthy,
				"webpass":      netif.HealthStateHealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 200, 2, 200),
				fromRuleV4(56, "203.0.113.2", 200),
				fwmarkRule(familyV6, 200, 2, 200),
				fromRule(56, "3d06:bad:b01:2200::/56", 200),
				fwmarkRule(familyV4, 300, 3, 300),
				fwmarkRule(familyV6, 300, 3, 300),
				fromRule(57, "3d06:bad:b01:3300::/56", 300),
			},
			wantRoutes: routesForGateways(configWithWebpassInTierOne(baseConfig), baseGateways),
		},
		{
			// Nothing is healthy, so no tier is active and no catch-all is
			// installed. Sending traffic at a provider that failed its probes
			// would be worse than letting it fall to the main table.
			name:       "no healthy provider installs no catch-all",
			cfg:        baseConfig,
			gateways:   baseGateways,
			health:     noneHealthy,
			wantRules:  []netif.DesiredRule{},
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			name:     "from-PD rule requires NPT prefix",
			cfg:      configWithoutWebpassNPT(baseConfig),
			gateways: baseGateways,
			health:   allHealthy,
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 100, 1, 100),
				fwmarkRule(familyV6, 100, 1, 100),
				fromRule(55, "3d06:bad:b01:1100::/56", 100),
				fwmarkRule(familyV4, 200, 2, 200),
				fromRuleV4(56, "203.0.113.2", 200),
				fwmarkRule(familyV6, 200, 2, 200),
				fwmarkRule(familyV4, 300, 3, 300),
				fwmarkRule(familyV6, 300, 3, 300),
				fromRule(57, "3d06:bad:b01:3300::/56", 300),
			},
			wantRoutes: routesForGateways(configWithoutWebpassNPT(baseConfig), baseGateways),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotRules, gotRoutes := desiredState(tc.gateways, tc.health, tc.cfg)
			if !reflect.DeepEqual(gotRules, tc.wantRules) {
				t.Fatalf("rules mismatch\ngot:  %#v\nwant: %#v", gotRules, tc.wantRules)
			}
			if !reflect.DeepEqual(gotRoutes, tc.wantRoutes) {
				t.Fatalf("routes mismatch\ngot:  %#v\nwant: %#v", gotRoutes, tc.wantRoutes)
			}
		})
	}
}

// TestPublishLiveStateReportsTheActiveTier pins what the management surface
// serves: the active tier the pass decided, and one carrying flag per provider
// that is true only for a provider in that tier which is healthy and has a
// gateway.
func TestPublishLiveStateReportsTheActiveTier(t *testing.T) {
	t.Parallel()

	store := wanstate.New()
	module := &Module{cfg: testConfig()}
	module.InitBase(testEnvWithStore(store), "module", moduleName)

	module.publishLiveState(testGateways(), netif.HealthStates{
		"att":          netif.HealthStateUnhealthy,
		"webpass":      netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateHealthy,
	})

	snapshot := store.Snapshot()
	if !snapshot.TierValid || snapshot.ActiveTier != 1 {
		t.Fatalf("active tier = %d (valid=%v), want 1", snapshot.ActiveTier, snapshot.TierValid)
	}
	want := map[string]bool{"att": false, "webpass": false, "monkeybrains": true}
	for name, wantCarrying := range want {
		if got := snapshot.Routing[name].Carrying; got != wantCarrying {
			t.Fatalf("%s carrying = %v, want %v", name, got, wantCarrying)
		}
	}
}

// TestPublishLiveStateWithNoHealthyProvider pins that a pass in which nothing is
// healthy carries nobody, rather than reporting the first tier as if it were
// serving traffic.
func TestPublishLiveStateWithNoHealthyProvider(t *testing.T) {
	t.Parallel()

	store := wanstate.New()
	module := &Module{cfg: testConfig()}
	module.InitBase(testEnvWithStore(store), "module", moduleName)

	module.publishLiveState(testGateways(), netif.HealthStates{
		"att":          netif.HealthStateUnhealthy,
		"webpass":      netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateUnhealthy,
	})

	for name, routing := range store.Snapshot().Routing {
		if routing.Carrying {
			t.Fatalf("%s reported carrying with no healthy provider", name)
		}
	}
}

// TestValidateWANAcceptsAnyPositivePriority pins that the two fixed priority
// checks are gone: a fourth provider's numbers are admitted, and only a
// non-positive value is refused.
func TestValidateWANAcceptsAnyPositivePriority(t *testing.T) {
	t.Parallel()

	fourth := WAN{
		WANRef:     ifmgr.WANRef{Name: "astound", Iface: "astound0"},
		TableID:    600,
		FwMark:     4,
		FwMarkPrio: 600,
		FromPrio:   58,
		NptPrefix:  "3d06:bad:b01:2500::/60",
		V4Source:   "",
		Tier:       2,
		Weight:     1,
	}
	if err := validateWAN(fourth); err != nil {
		t.Fatalf("validateWAN rejected a fourth provider: %v", err)
	}
	fourth.FwMarkPrio = 0
	if err := validateWAN(fourth); err == nil {
		t.Fatal("validateWAN accepted a zero fw_mark_prio")
	}
	fourth.FwMarkPrio = 600
	fourth.FromPrio = 0
	if err := validateWAN(fourth); err == nil {
		t.Fatal("validateWAN accepted a zero from_prio")
	}
}

func TestInitReturnsDisabledSentinelWhenWANsEmpty(t *testing.T) {
	t.Parallel()

	module, err := New(Config{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	initErr := module.Init(context.Background(), testEnv())
	if initErr == nil {
		t.Fatal("Init returned nil error for empty WANs, want ErrModuleDisabled")
	}
	if !errors.Is(initErr, ifmgr.ErrModuleDisabled) {
		t.Fatalf("Init returned err=%v, want errors.Is(err, ifmgr.ErrModuleDisabled)", initErr)
	}
}

func TestDesiredStateOmitsStaticInternalRoute(t *testing.T) {
	t.Parallel()

	gateways := testGateways()
	health := netif.HealthStates{
		"att":          netif.HealthStateHealthy,
		"webpass":      netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	}
	cfg := testConfig()

	_, got := desiredState(gateways, health, cfg)

	want := routesForGateways(cfg, gateways)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	for _, gotRoute := range got {
		if gotRoute.Via == cfg.OpnsenseEdgeV6 {
			t.Fatalf("static internal route via the edge address remains: %#v", gotRoute)
		}
	}
	for _, wan := range cfg.WANs {
		wantTransit := route(familyV4, cfg.InternalNetV4, "", cfg.InternalIface, wan.TableID, 0)
		wantEdge := route(familyV6, withPrefix(cfg.OpnsenseEdgeV6, "128"), "", cfg.InternalIface, wan.TableID, 0)
		if !containsRoute(got, wantTransit) {
			t.Fatalf("missing transit route: %#v", wantTransit)
		}
		if !containsRoute(got, wantEdge) {
			t.Fatalf("missing edge route: %#v", wantEdge)
		}
	}
}

func testConfig() Config {
	return Config{
		InternalIface:   "vmbr250",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/run/mwan-health.state",
		WANs: []WAN{
			{
				WANRef:     ifmgr.WANRef{Name: "att", Iface: "att0"},
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:1100::/56",
				V4Source:   "",
				Tier:       0,
				Weight:     1,
			},
			{
				WANRef:     ifmgr.WANRef{Name: "webpass", Iface: "webpass0"},
				TableID:    200,
				FwMark:     2,
				FwMarkPrio: 200,
				FromPrio:   56,
				NptPrefix:  "3d06:bad:b01:2200::/56",
				V4Source:   "203.0.113.2",
				Tier:       0,
				Weight:     1,
			},
			{
				WANRef:     ifmgr.WANRef{Name: "monkeybrains", Iface: "mbrains0"},
				TableID:    300,
				FwMark:     3,
				FwMarkPrio: 300,
				FromPrio:   57,
				NptPrefix:  "3d06:bad:b01:3300::/56",
				V4Source:   "",
				Tier:       1,
				Weight:     1,
			},
		},
	}
}

func testGateways() gateways {
	return gateways{
		"att":          {V4: "192.0.2.1", V6: "fe80::a"},
		"webpass":      {V4: "203.0.113.1", V6: "fe80::b"},
		"monkeybrains": {V4: "198.51.100.1", V6: "fe80::c"},
	}
}

func allHealthyRules(cfg Config) []netif.DesiredRule {
	return []netif.DesiredRule{
		fwmarkRule(familyV4, 100, 1, 100),
		fwmarkRule(familyV6, 100, 1, 100),
		fromRule(55, cfg.WANs[0].NptPrefix, 100),
		fwmarkRule(familyV4, 200, 2, 200),
		fromRuleV4(56, cfg.WANs[1].V4Source, 200),
		fwmarkRule(familyV6, 200, 2, 200),
		fromRule(56, cfg.WANs[1].NptPrefix, 200),
		fwmarkRule(familyV4, 300, 3, 300),
		fwmarkRule(familyV6, 300, 3, 300),
		fromRule(57, cfg.WANs[2].NptPrefix, 300),
	}
}

func routesForGateways(cfg Config, currentGateways gateways) []netif.RouteSpec {
	routes := make([]netif.RouteSpec, 0, len(cfg.WANs)*5+1)
	for _, wan := range cfg.WANs {
		wanGateways := currentGateways[wan.Name]
		if wanGateways.V4 != "" {
			routes = append(routes, route(familyV4, "default", wanGateways.V4, wan.Iface, wan.TableID, 0))
		}
		if wanGateways.V6 != "" {
			routes = append(routes, route(familyV6, "default", wanGateways.V6, wan.Iface, wan.TableID, 0))
		}
		routes = append(routes,
			route(familyV4, cfg.InternalNetV4, "", cfg.InternalIface, wan.TableID, 0),
			route(familyV6, withPrefix(cfg.OpnsenseEdgeV6, "128"), "", cfg.InternalIface, wan.TableID, 0),
		)
	}
	return routes
}

func containsRoute(routes []netif.RouteSpec, want netif.RouteSpec) bool {
	for _, got := range routes {
		if got == want {
			return true
		}
	}
	return false
}

func route(family string, dest string, via string, dev string, tableID int, metric int) netif.RouteSpec {
	return netif.RouteSpec{
		Family:   family,
		Dest:     dest,
		Via:      via,
		Dev:      dev,
		TableID:  tableID,
		Metric:   metric,
		Protocol: 0,
	}
}

func fwmarkRule(family string, priority int, mark uint32, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   family,
		Priority: priority,
		From:     "",
		Mark:     mark,
		IifName:  "",
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func fromRule(priority int, from string, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   familyV6,
		Priority: priority,
		From:     from,
		Mark:     0,
		IifName:  "",
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func fromRuleV4(priority int, from string, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   familyV4,
		Priority: priority,
		From:     from,
		Mark:     0,
		IifName:  "",
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func catchAllRule(family string, iifName string, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   family,
		Priority: catchAllPriority,
		From:     "",
		Mark:     0,
		IifName:  iifName,
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func configWithoutWebpassNPT(cfg Config) Config {
	cfg.WANs = append([]WAN(nil), cfg.WANs...)
	cfg.WANs[1].NptPrefix = ""
	return cfg
}

// configWithWebpassInTierOne moves Webpass down beside Monkeybrains, so the
// active tier can hold two healthy providers while not being the first
// configured tier.
func configWithWebpassInTierOne(cfg Config) Config {
	cfg.WANs = append([]WAN(nil), cfg.WANs...)
	cfg.WANs[1].Tier = 1
	return cfg
}

func testEnv() *ifmgr.Env {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &ifmgr.Env{
		Iface: "vmbr250",
		Log: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(&config.Config{}, log, "mwan-ifmgr")),
	}
}

func testEnvWithStore(store *wanstate.Store) *ifmgr.Env {
	env := testEnv()
	env.LiveState = store
	return env
}
```

Note `catchAllPriority`: the constant `fallbackPriority` is renamed in Step 6,
because the rule it names is no longer about one fallback provider.
`wanroutes_nexthop_test.go` uses only `testConfig`, `testEnv`, and
`checkNextHopLocked`, all of which keep their names, so it needs no edit.

- [ ] **Step 6: Generalize the routing module**

In `mwan/go/internal/ifmgr/modules/wanroutes/wanroutes.go`, replace the constant
block and the tier block at `:19-60` with:

```go
const (
	moduleName = "wan.routes"
	familyV4   = "inet"
	familyV6   = "inet6"

	// catchAllPriority is the rule pair that sends everything arriving on the
	// internal link to one provider's table. It sits above every mark rule, so
	// it is consulted only after those miss.
	catchAllPriority = 50

	// alertKindNextHopUnresolved is the alert kind for an internal next hop
	// with no usable neighbour entry.
	alertKindNextHopUnresolved = "wan_routes_next_hop_unresolved"
	// nextHopAlertThreshold is how many consecutive reconciles the next hop
	// must fail to resolve before the alert fires. Two ticks tolerate one
	// in-flight NDP resolution without a false alert.
	nextHopAlertThreshold = 2
)
```

Replace the `WAN` type at `:77-90` with:

```go
// WAN is one configured uplink and its owned policy-routing slots. The
// embedded ifmgr.WANRef carries the shared per-WAN identity (Name, Iface); the
// remaining fields are the wan.routes-specific per-WAN routing and steering
// data.
type WAN struct {
	ifmgr.WANRef
	TableID    int
	FwMark     uint32
	FwMarkPrio int
	FromPrio   int
	NptPrefix  string
	// V4Source is the WAN's static IPv4 link address. When set, traffic the box
	// sources from that address is pinned to this WAN's table via a v4 source
	// rule at FromPrio, the IPv4 twin of the NptPrefix v6 source rule. Only
	// static-link WANs set it; a WAN on a dynamic link leaves it empty and gets
	// no v4 source rule.
	V4Source string
	// Tier is the preference tier this provider sits in, from configuration.
	// The lowest-numbered tier holding a healthy provider carries new
	// connections.
	Tier uint8
	// Weight is this provider's share of its tier. wan.routes does not spread
	// traffic itself, so it carries the value only to hand it to the management
	// surface, which publishes what the daemon loaded.
	Weight int
}
```

Replace `publishLiveState` at `:202-230` with:

```go
// publishLiveState writes this pass's steering decision to the management
// surface's snapshot store, when this host serves one. A provider is carrying
// when it sits in the active tier, its verdict allows it, and it has a gateway
// in at least one family, which is exactly the condition under which this pass
// installed its rules.
func (m *Module) publishLiveState(currentGateways gateways, health netif.HealthStates) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	activeTier, anyHealthy := netif.ActiveTier(tierMembers(m.cfg), health)
	members := make(map[string]wanstate.MemberRouting, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		wanGateways := currentGateways[wan.Name]
		reachable := wanEnabled(wanGateways.V4, health.State(wan.Name)) ||
			wanEnabled(wanGateways.V6, health.State(wan.Name))
		members[wan.Name] = wanstate.MemberRouting{
			Carrying: anyHealthy && wan.Tier == activeTier && reachable,
		}
	}
	m.Env.LiveState.SetRouting(activeTier, members)
}
```

Replace `desiredState` at `:297-341` with:

```go
func desiredState(
	currentGateways gateways,
	health netif.HealthStates,
	cfg Config,
) ([]netif.DesiredRule, []netif.RouteSpec) {
	rules := make([]netif.DesiredRule, 0, len(cfg.WANs)*3+2)
	routes := make([]netif.RouteSpec, 0, len(cfg.WANs)*5+1)

	for _, wan := range cfg.WANs {
		wanGateways := currentGateways[wan.Name]
		routes = appendWANDefaultRoutes(routes, wan, wanGateways)
		routes = appendWANInternalRoutes(routes, cfg, wan.TableID)

		rules = appendWANRules(rules, wan, wanGateways, health)
	}

	if carrier := catchAllCarrier(cfg, health); carrier != nil {
		rules = append(
			rules,
			netif.DesiredRule{
				Family:   familyV4,
				Priority: catchAllPriority,
				From:     "",
				Mark:     0,
				IifName:  cfg.InternalIface,
				UIDRange: "",
				Table:    "",
				TableID:  carrier.TableID,
			},
			netif.DesiredRule{
				Family:   familyV6,
				Priority: catchAllPriority,
				From:     "",
				Mark:     0,
				IifName:  cfg.InternalIface,
				UIDRange: "",
				Table:    "",
				TableID:  carrier.TableID,
			},
		)
	}

	return rules, routes
}

// catchAllCarrier returns the provider the catch-all rule pair points at, or
// nil when this pass installs none. The pair exists for one case: the active
// tier is below the first configured tier and exactly one provider in it is
// healthy. Nothing marks internal traffic for that provider then, so without
// the pair the traffic would fall to the main table.
//
// With two or more healthy providers in the active tier the steering module's
// marks carry the split, and a catch-all would send every unmarked packet to
// one of them and undo it. With no healthy provider anywhere there is nothing
// to point at.
func catchAllCarrier(cfg Config, health netif.HealthStates) *WAN {
	activeTier, anyHealthy := netif.ActiveTier(tierMembers(cfg), health)
	if !anyHealthy {
		return nil
	}
	if activeTier == lowestConfiguredTier(cfg) {
		return nil
	}
	var carrier *WAN
	for i := range cfg.WANs {
		wan := &cfg.WANs[i]
		if wan.Tier != activeTier || !netif.HealthIsHealthy(health.State(wan.Name)) {
			continue
		}
		if carrier != nil {
			return nil
		}
		carrier = wan
	}
	return carrier
}

// tierMembers projects the configured providers onto the list the shared
// active-tier function reads.
func tierMembers(cfg Config) []netif.TierMember {
	members := make([]netif.TierMember, 0, len(cfg.WANs))
	for _, wan := range cfg.WANs {
		members = append(members, netif.TierMember{Name: wan.Name, Tier: wan.Tier})
	}
	return members
}

// lowestConfiguredTier returns the first tier the configuration names, whether
// or not any of its providers is healthy. Callers reach it only after
// netif.ActiveTier reported a healthy provider, so the list is never empty.
func lowestConfiguredTier(cfg Config) uint8 {
	lowest := cfg.WANs[0].Tier
	for _, wan := range cfg.WANs[1:] {
		if wan.Tier < lowest {
			lowest = wan.Tier
		}
	}
	return lowest
}
```

In `ownedRuleSlots` at `:531-532`, rename the two `fallbackPriority` references
to `catchAllPriority`:

```go
	appendSlot(ruleSlot{family: familyV4, priority: catchAllPriority})
	appendSlot(ruleSlot{family: familyV6, priority: catchAllPriority})
```

Replace `validateWAN` at `:600-620` with:

```go
// validateWAN checks the structure of one provider's entry. The set-wide checks
// (unique routing numbers, no reserved table, a weight of at least one) run at
// load time in networkjson, because they need every provider at once and the
// reserved list beside them.
func validateWAN(wan WAN) error {
	if wan.Name == "" {
		return fmt.Errorf("name is required")
	}
	if wan.Iface == "" {
		return fmt.Errorf("iface is required")
	}
	if wan.TableID <= 0 {
		return fmt.Errorf("table_id must be > 0")
	}
	if wan.FwMark == 0 {
		return fmt.Errorf("fw_mark must be > 0")
	}
	if wan.FwMarkPrio <= 0 {
		return fmt.Errorf("fw_mark_prio must be > 0")
	}
	if wan.FromPrio <= 0 {
		return fmt.Errorf("from_prio must be > 0")
	}
	if wan.FwMarkPrio == catchAllPriority || wan.FromPrio == catchAllPriority {
		return fmt.Errorf("rule priorities must not equal the catch-all priority %d", catchAllPriority)
	}
	return nil
}
```

The catch-all check is new: the module owns the rule pair at that priority
itself, so a provider typed onto it would have its rule pruned by the cleanup
pass. Add one subtest to the validation test in Step 5 with `FwMarkPrio: 50`
and assert the error mentions the catch-all priority.

Delete `fallbackEnabled` at `:645-649`, `findWAN` at `:651-658`,
`isFwMarkPriority` at `:669-671`, and `isFromPriority` at `:673-675`. Keep
`wanEnabled` and `withPrefix`.

- [ ] **Step 7: Run the wan.routes tests to verify they pass**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/ifmgr/modules/wanroutes/ -v
```

Expected: PASS. Seven `TestDesiredState` subtests plus the two live-state tests,
the validate test, the disabled-sentinel test, the static-route test, and the two
next-hop tests, `ok goodkind.io/mwan/internal/ifmgr/modules/wanroutes`, exit 0.

- [ ] **Step 8: Carry tier and weight through the module config builders**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go`, replace `sharedWAN` at
`:650-661` with:

```go
// sharedWAN is one WAN's full config from its network.json wan container: the
// identity (WANRef), the policy-routing slots wan.routes consumes, and the
// steering properties the balancer reads. npt reads only the embedded WANRef.
// One home per WAN.
type sharedWAN struct {
	ifmgr.WANRef
	TableID    int
	FwMark     int
	FwMarkPrio int
	FromPrio   int
	NptPrefix  string
	V4Source   string
	Tier       uint8
	Weight     int
}
```

In `buildWANRefs` at `:698-709`, replace the loop body with:

```go
	for _, name := range names {
		entry := ifmgrCfg.WAN[name]
		inputs.WANs = append(inputs.WANs, sharedWAN{
			WANRef:     ifmgr.WANRef{Name: name, Iface: entry.Iface},
			TableID:    entry.TableID,
			FwMark:     entry.FwMark,
			FwMarkPrio: entry.FwMarkPrio,
			FromPrio:   entry.FromPrio,
			NptPrefix:  entry.NptPrefix,
			V4Source:   entry.V4Source,
			Tier:       entry.Tier,
			Weight:     entry.Weight,
		})
	}
```

Replace `buildWANRoutesConfig` at `:713-757` with:

```go
func buildWANRoutesConfig(
	shared sharedWANInputs,
	section *config.IfMgrWANRoutesSection,
) (wanroutes.Config, error) {
	cfg := wanroutes.Config{
		InternalIface:   "",
		OpnsenseEdgeV6:  "",
		InternalNetV4:   "",
		HealthStateFile: "",
		WANs:            nil,
	}
	if section == nil {
		return cfg, nil
	}
	cfg.InternalIface = section.InternalIface
	cfg.OpnsenseEdgeV6 = shared.OpnsenseEdgeV6
	cfg.InternalNetV4 = section.InternalNetV4
	cfg.HealthStateFile = section.HealthStateFile
	cfg.WANs = make([]wanroutes.WAN, 0, len(shared.WANs))
	for _, wan := range shared.WANs {
		mark, err := wanFwMark(wan)
		if err != nil {
			return wanroutes.Config{}, err
		}
		cfg.WANs = append(cfg.WANs, wanroutes.WAN{
			WANRef:     wan.WANRef,
			TableID:    wan.TableID,
			FwMark:     mark,
			FwMarkPrio: wan.FwMarkPrio,
			FromPrio:   wan.FromPrio,
			NptPrefix:  wan.NptPrefix,
			V4Source:   wan.V4Source,
			Tier:       wan.Tier,
			Weight:     wan.Weight,
		})
	}
	return cfg, nil
}

// wanFwMark narrows one provider's firewall mark onto the kernel's width. The
// model bounds the leaf at 1 or higher and the loader refuses a missing value,
// so the only case left is a value too wide for a mark. Both the routing module
// and the steering module go through here, so a mark either module refuses is
// refused by both.
func wanFwMark(wan sharedWAN) (uint32, error) {
	if wan.FwMark < 0 {
		return 0, fmt.Errorf("network.json wan %s fw-mark must be >= 0", wan.Name)
	}
	if wan.FwMark > int(^uint32(0)) {
		return 0, fmt.Errorf("network.json wan %s fw-mark %d exceeds uint32", wan.Name, wan.FwMark)
	}
	return uint32(wan.FwMark), nil
}
```

- [ ] **Step 9: Update the module-config tests**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go`, give both providers in
`sharedWANForTest` at `:126-161` a tier and a weight:

```go
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:      "att0",
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:1100::/56",
				V4Source:   "",
				Tier:       0,
				Weight:     1,
			},
			"webpass": {
				Iface:      "webpass0",
				TableID:    200,
				FwMark:     2,
				FwMarkPrio: 200,
				FromPrio:   56,
				NptPrefix:  "3d06:bad:b01:2200::/56",
				V4Source:   "203.0.113.2",
				Tier:       1,
				Weight:     3,
			},
		},
```

In `TestBuildWANRefs` at `:174-192`, add the two fields to each expected entry:
`att` gets `Tier: 0, Weight: 1` and `webpass` gets `Tier: 1, Weight: 3`. In
`TestBuildWANRoutesConfig` at `:219-237`, add the same two fields to each
expected `wanroutes.WAN`.

- [ ] **Step 10: Publish weight and hash mode, and read tier from configuration**

In `mwan/go/internal/wanconfig/tree.go`, add the weight field to `Member` after
`Tier` at `:41`:

```go
	// Tier is the steering tier the configuration assigns; lower is preferred.
	Tier uint8
	// Weight is the member's share of its tier, at least one. The schema bounds
	// it there, so a zero would be refused by the datastore rather than
	// published.
	Weight uint16
```

Add the hash mode to `Gateway` after `InternalIface` at `:111`:

```go
	// HashMode is how a new connection is assigned to a member of the active
	// tier. It is published because the daemon acts on it, so a reader of the
	// tree sees the rule the kernel is running under. Empty publishes nothing,
	// which is what a role that runs no steering module carries.
	HashMode string
```

Add the group path beside the other path constants at `:127-130`:

```go
const (
	interfacesPath    = "/ietf-interfaces:interfaces"
	steeringGroupPath = interfacesPath + "/goodkind-mwan-steering:steering-group"
	natPath           = "/ietf-nat:nat"
	daemonPath        = "/goodkind-mwan-steering:daemon"
```

In `ConfigItems` at `:161-176`, insert the group items after the member loop and
before the translation instances:

```go
	items := make([]Item, 0, 8*len(g.Members)+6)
	items = append(items, interfaceItems(g.InternalIface)...)
	for _, member := range g.Members {
		items = append(items, interfaceItems(member.Iface)...)
		items = append(items, steeringItems(member)...)
	}
	items = append(items, steeringGroupItems(g.HashMode)...)
	instanceID := uint32(0)
```

Replace `steeringItems` at `:293-304` with:

```go
// steeringItems marks the member's link as a steering member with its tier and
// weight and, when the daemon probes it, the probe policy that decides its
// state.
func steeringItems(member Member) []Item {
	base := interfacePath(member.Iface) + "/goodkind-mwan-steering:steering"
	items := []Item{
		{Path: base + "/tier", Value: strconv.FormatUint(uint64(member.Tier), 10)},
		{Path: base + "/weight", Value: strconv.FormatUint(uint64(member.Weight), 10)},
	}
	if member.ProbePolicy != "" {
		items = append(items, Item{Path: base + "/probe-policy", Value: member.ProbePolicy})
	}
	return items
}

// steeringGroupItems describes the settings that apply to the member set as a
// whole. A gateway whose role runs no steering module carries no hash mode and
// publishes none, rather than a value it does not act on.
func steeringGroupItems(hashMode string) []Item {
	if hashMode == "" {
		return nil
	}
	return []Item{{Path: steeringGroupPath + "/hash-mode", Value: hashMode}}
}

// hashModes are the values the model's enumeration accepts. A value outside the
// set would make the datastore reject the whole replace, taking every other
// item with it, so it is caught before the write.
var hashModes = map[string]bool{
	"random":             true,
	"source":             true,
	"source-destination": true,
}
```

In `validate` at `:338-357`, add the weight check inside the member loop after
the link check, and the hash-mode check before the loop:

```go
func validate(g Gateway) error {
	if err := validateKey("internal link", g.InternalIface); err != nil {
		return err
	}
	if g.HashMode != "" && !hashModes[g.HashMode] {
		return invalid(fmt.Sprintf("hash mode %q is not one of the model's values", g.HashMode))
	}
	seen := map[string]string{g.InternalIface: "internal link"}
	for _, member := range g.Members {
		if err := validateKey("member name", member.Name); err != nil {
			return err
		}
		if err := validateKey("member "+member.Name+" link", member.Iface); err != nil {
			return err
		}
		if member.Weight == 0 {
			return invalid(fmt.Sprintf("member %s weight must be at least 1", member.Name))
		}
		if owner, dup := seen[member.Iface]; dup {
			return invalid(fmt.Sprintf("member %s link %q is already the %s", member.Name, member.Iface, owner))
		}
		seen[member.Iface] = "member " + member.Name
		if member.NPTInternal.IsValid() != member.NPTExternal.IsValid() {
			return invalid(fmt.Sprintf("member %s translation needs both prefixes (internal %q, external %q)",
				member.Name, member.NPTInternal, member.NPTExternal))
		}
		if member.NPTInternal.IsValid() && (!member.NPTInternal.Addr().Is6() || !member.NPTExternal.Addr().Is6()) {
			return invalid(fmt.Sprintf("member %s translation prefixes must be IPv6 (internal %q, external %q)",
				member.Name, member.NPTInternal, member.NPTExternal))
		}
	}
	return nil
}
```

- [ ] **Step 11: Update the tree tests**

In `mwan/go/internal/wanconfig/tree_test.go`:

- In `TestConfigItems_DescribesEveryMemberAndTranslation` at `:17-79`, set
  `HashMode: "random"` on the gateway, give each member a `Weight` (att 1,
  monkeybrains 1, webpass 2), insert a `.../steering/weight` item directly after
  each `.../steering/tier` item, and insert
  `{Path: "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/hash-mode", Value: "random"}`
  between the last member's items and the first NAT item.
- In `TestConfigItems_LeavesUnprobedMemberWithoutPolicy` at `:84-101`, give the
  member `Weight: 1` and change the count assertion from `4+4+1` to `4+4+2`,
  because tier and weight are now two items and the gateway carries no hash
  mode.
- In `TestConfigItems_PublishesTheDaemonSettingsItHolds` at `:107-179` and
  `TestConfigItems_PublishesNoDaemonSettingsWhenAbsent` at `:184-198`, give the
  single member `Weight: 1`.
- In `TestConfigItems_RejectsWhatAPathCannotCarry` at `:203-232`, give every
  member in every case `Weight: 1`, and add two cases:

```go
		"zero weight": {InternalIface: "eninternal0", Members: []Member{
			{Name: "att", Iface: "enatt0", Tier: 0, Weight: 0},
		}},
		"unknown hash mode": {
			InternalIface: "eninternal0", HashMode: "round-robin",
			Members: []Member{{Name: "att", Iface: "enatt0", Tier: 0, Weight: 1}},
		},
```

- [ ] **Step 12: Read tier and weight from the loaded configuration when publishing**

In `mwan/go/cmd/mwan/wanconfig_publish_linux.go`, replace `:319-349` with:

```go
	gateway := wanconfig.Gateway{
		InternalIface: routesCfg.InternalIface,
		HashMode:      hashModeFromConfig(cfg),
		Members:       make([]wanconfig.Member, 0, len(routesCfg.WANs)),
		Daemon:        daemonSettings(cfg, configs),
	}
	for _, wan := range routesCfg.WANs {
		member := wanconfig.Member{
			Name:        wan.Name,
			Iface:       wan.Iface,
			Tier:        wan.Tier,
			Weight:      clampUint16(wan.Weight),
			ProbePolicy: "",
			NPTInternal: netip.Prefix{},
			NPTExternal: netip.Prefix{},
		}
		if probed[wan.Name] {
			// The probe policy is named after the member: the health module
			// keys its per-member policy by the same name.
			member.ProbePolicy = wan.Name
		}
		if wan.NptPrefix != "" {
			external, err := netip.ParsePrefix(wan.NptPrefix)
			if err != nil {
				logger.Warn("wanconfig: member npt prefix unparsable",
					"member", wan.Name, "value", wan.NptPrefix, "err", err)
				return none, false, fmt.Errorf("wanconfig: member %s npt prefix %q: %w", wan.Name, wan.NptPrefix, err)
			}
			member.NPTInternal = internalPrefix
			member.NPTExternal = external
		}
		gateway.Members = append(gateway.Members, member)
	}
	return gateway, true, nil
}

// hashModeFromConfig reads the group's hash mode from the loaded network
// configuration, the same value the steering module acts on. A nil
// configuration, which a role-only projection passes, publishes no hash
// mode rather than a guess.
func hashModeFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.IfMgr.HashMode
}
```

No import changes: `config` is already imported by this file, and the hash
mode comes from the loaded configuration rather than from any module's
config, so this task never touches the steering package Task 4 creates.

- [ ] **Step 13: Update the publish test fixture**

In `mwan/go/cmd/mwan/wanconfig_publish_linux_test.go`, give each
`wanroutes.WAN` in `wanconfigTestModuleConfigs` at `:21-34` a `Tier` and
`Weight` (att `Tier: 0, Weight: 1`, monkeybrains `Tier: 1, Weight: 1`, webpass
`Tier: 0, Weight: 2`). In `TestGatewayFromModuleConfigs_ProjectsTheWANRole`
at `:65-76`, pass a configuration that carries a hash mode instead of `nil`,
add the matching `Weight` to each expected `wanconfig.Member` at `:79-92`,
and assert the hash mode. An empty `config.Config{}` literal followed by a
field assignment is the shape the fixture already uses for `health.Config`,
and it keeps the exhaustive-literal gate quiet:

```go
	cfg := &config.Config{}
	cfg.IfMgr.HashMode = "source"
	gateway, ok, err := gatewayFromModuleConfigs(cfg, wanconfigTestModuleConfigs())
```

```go
	if gateway.HashMode != "source" {
		t.Fatalf("HashMode = %q, want source", gateway.HashMode)
	}
```

Add `"goodkind.io/mwan/internal/config"` to the test file's import block at
`:5-14`, before the `ifmgr` import.

The tiers in the expected members no longer come from a name rule, so the
comment above the test changes from "the router's tier" to "the tier the
configuration assigns".

- [ ] **Step 14: Take the provider names out of the debug views**

In `mwan/go/cmd/mwan/debug_probes_linux.go`, replace `debugProbeIface` at
`:583-606` with:

```go
func debugProbeIface(
	cfg *config.Config,
	view string,
	args []string,
) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s accepts at most one positional interface", view)
	}
	if len(args) == 1 {
		if strings.TrimSpace(args[0]) == "" {
			return "", fmt.Errorf("%s positional interface is empty", view)
		}
		return args[0], nil
	}
	wans := debugActiveProbeWANs(cfg)
	if len(wans) == 0 {
		return "", fmt.Errorf("%s: no configured WAN has a usable interface", view)
	}
	// The default is the provider with the lowest firewall mark. Nothing in the
	// configuration says which provider a bare probe should use, and the mark
	// order is the one an operator already reads as the provider order.
	lowest := wans[0]
	for _, wan := range wans[1:] {
		if wan.FwMark < lowest.FwMark {
			lowest = wan
		}
	}
	return lowest.Iface, nil
}
```

Replace `debugActiveProbeWANs` at `:615-631` with:

```go
// debugActiveProbeWANs returns every configured provider that has a usable
// interface, ordered by name. The set comes from the loaded configuration
// rather than a list of names in code, so a provider added to inventory appears
// in every active probe view with the binary unchanged.
func debugActiveProbeWANs(cfg *config.Config) []debugWAN {
	wans := make([]debugWAN, 0, len(cfg.IfMgr.WAN))
	for _, wan := range debugWANs(cfg) {
		if strings.TrimSpace(wan.Iface) == "" {
			continue
		}
		wans = append(wans, wan)
	}
	return wans
}
```

`debugWANs` in `mwan/go/cmd/mwan/debug_helpers.go:21-39` already sorts by name
and copies the mark, so nothing there changes.

- [ ] **Step 15: Update the debug probe tests**

In `mwan/go/cmd/mwan/debug_probes_linux_test.go`, replace
`debugProbeTestConfig` at `:675-686` with:

```go
func debugProbeTestConfig() *config.Config {
	return &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{
				"monkeybrains": {
					Iface: "enmonkeybrains", TableID: 300, FwMark: 3, FwMarkPrio: 300,
					FromPrio: 57, NptPrefix: "", V4Source: "", Tier: 1, Weight: 1,
				},
				"webpass": {
					Iface: "enwebpass0", TableID: 200, FwMark: 2, FwMarkPrio: 200,
					FromPrio: 56, NptPrefix: "", V4Source: "", Tier: 0, Weight: 1,
				},
				"att": {
					Iface: "enatt0", TableID: 100, FwMark: 1, FwMarkPrio: 100,
					FromPrio: 55, NptPrefix: "", V4Source: "", Tier: 0, Weight: 1,
				},
				// A provider with no interface stays in the fixture so the
				// usable-interface filter is still exercised. Its mark is not
				// the lowest, so it can never become the default either.
				"noiface": {
					Iface: "", TableID: 600, FwMark: 4, FwMarkPrio: 600,
					FromPrio: 58, NptPrefix: "", V4Source: "", Tier: 2, Weight: 1,
				},
			},
		},
	}
}
```

In `TestDebugConnectivityRendersOrderedWANStateAndSequentialProbes` at
`:46-196`, the views now iterate by name rather than by the hardcoded att,
webpass, monkeybrains order. Change three expectations:

```go
		wantListCalls := []string{"enatt0", "enmonkeybrains", "enwebpass0"}
```

```go
		wantCalls := []debugProbeTestCall{
			{kind: "ping4", iface: "enatt0", target: "1.1.1.1"},
			{kind: "ping4", iface: "enatt0", target: "1.1.1.1"},
			{kind: "ping6", iface: "enatt0", target: "2606:4700:4700::1111"},
			{kind: "ping6", iface: "enatt0", target: "2606:4700:4700::1111"},
			{kind: "ping6", iface: "enmonkeybrains", target: "2606:4700:4700::1111"},
			{kind: "ping6", iface: "enmonkeybrains", target: "2606:4700:4700::1111"},
			{kind: "ping4", iface: "enwebpass0", target: "1.1.1.1"},
			{kind: "ping4", iface: "enwebpass0", target: "1.1.1.1"},
		}
```

```go
		assertDebugProbeOrderedText(t, rendered, "att", "monkeybrains", "webpass")
```

The `pingOutcomes` map at `:53-58` is keyed by interface, not by order, so it
needs no change.

In `TestDebugLoadBalanceIfaceViewsRotateConfiguredWANs` at `:561-568`, change the
rotation to the name order:

```go
			ifaces := []string{
				"enatt0",
				"enmonkeybrains",
				"enwebpass0",
				"enatt0",
				"enmonkeybrains",
				"enwebpass0",
			}
```

and the ordered-text assertion at `:573-582` to match:

```go
			assertDebugProbeOrderedText(
				t,
				output.String(),
				"iter 1 via enatt0",
				"iter 2 via enmonkeybrains",
				"iter 3 via enwebpass0",
				"iter 4 via enatt0",
				"iter 5 via enmonkeybrains",
				"iter 6 via enwebpass0",
			)
```

Rename the two default-interface subtest names at `:245` and `:350` from
`"ping4 defaults to att"` and `"curl4 defaults to att"` to
`"ping4 defaults to the lowest mark"` and `"curl4 defaults to the lowest mark"`.
Both keep `wantIface: "enatt0"`, which is now the lowest mark rather than a name
in code.

Replace `TestDebugProbeDefaultIfaceRequiresConfiguredAtt` at `:651-673` with:

```go
// TestDebugProbeDefaultIfaceIsTheLowestMark pins that the default interface
// comes from the configured mark order rather than a provider name in code, so
// a gateway with no provider called att still has a default.
func TestDebugProbeDefaultIfaceIsTheLowestMark(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{
				"monkeybrains": {Iface: "enmonkeybrains", FwMark: 3},
				"webpass":      {Iface: "enwebpass0", FwMark: 2},
			},
		},
	}
	var calls []debugProbeTestCall
	dependencies := debugProbeDependencies{
		ping4: func(
			_ context.Context,
			iface string,
			target netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			calls = append(calls, debugProbeTestCall{kind: "ping4", iface: iface, target: target.String()})
			return time.Millisecond, nil
		},
	}
	err := runDebugProbeViewWithDependencies(
		context.Background(),
		io.Discard,
		debugProbeTestLogger(),
		cfg,
		"ping4",
		nil,
		dependencies,
	)
	if err != nil {
		t.Fatalf("ping4 returned error: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("ping4 made no probe")
	}
	for _, call := range calls {
		if call.iface != "enwebpass0" {
			t.Fatalf("probe used %q, want the lowest-mark interface enwebpass0", call.iface)
		}
	}
}

// TestDebugProbeDefaultIfaceRequiresAConfiguredProvider pins the error when the
// configuration carries no provider with a usable interface at all.
func TestDebugProbeDefaultIfaceRequiresAConfiguredProvider(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{"webpass": {Iface: ""}},
		},
	}
	err := runDebugProbeViewWithDependencies(
		context.Background(),
		io.Discard,
		debugProbeTestLogger(),
		cfg,
		"ping4",
		nil,
		debugProbeDependencies{},
	)
	if err == nil || !strings.Contains(err.Error(), "no configured WAN has a usable interface") {
		t.Fatalf("ping4 error = %v, want the no-usable-WAN error", err)
	}
}
```

- [ ] **Step 16: Run the full suite**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./...
```

Expected: PASS, exit 0, with `ok` lines for `internal/netif`,
`internal/ifmgr/modules/wanroutes`, `internal/wanconfig`, `internal/networkjson`,
and `cmd/mwan`.

- [ ] **Step 17: Run the repository gates**

```bash
cd "$(git rev-parse --show-toplevel)" && make check
```

Expected: PASS, exit 0. `TierOf`, `TierPreferred`, and `TierFallback` are
exported and were removed rather than left unused, so neither `unused` nor the
deadcode gate has anything new to report; the deadcode baseline at
`mwan/go/.deadcode-baseline.txt` names none of them today and must not gain an
entry.

- [ ] **Step 18: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/go/internal/netif mwan/go/internal/ifmgr/modules/wanroutes \
  mwan/go/internal/wanconfig mwan/go/cmd/mwan
git commit -S -m "Read the steering tier from configuration and drop the provider names from wan.routes" -m "Add netif.ActiveTier over a tier list and a health map, generalize the Monkeybrains catch-all into a rule any lone healthy provider below the first tier gets, delete TierOf and the att, webpass, and monkeybrains constants with the two fixed priority validators the loader now replaces, carry tier and weight from network.json through sharedWAN into wan.routes, publish member weight and the group hash mode, and pick the debug default interface by lowest mark instead of by name." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: the steering module owns the balancer

The daemon takes over load balancing. A new module in the wan role computes the
mark assignment from the active tier's healthy providers and their weights, and
programs it into a kernel table and chain it creates and owns. The three fixed
lines leave the ruleset file in the same change (that edit lives in the firewall
task, decision 13), so at no point are both programming the same marks.

**Where the chain sits.** The ruleset file's `prerouting` chain runs at priority
`mangle`, which is -150. That chain restores the connection mark for an
established flow and sets the per-link ingress marks. The steering chain runs at
-149, one step later, so those marks are already set when its `meta mark 0`
guard looks. Getting this backwards would overwrite the control-plane pins, and
the spec names it as a failure mode.

**What it never does.** The module has no teardown. It never deletes a table or
a chain, so a binary swap leaves the kernel forwarding on the last programmed
rules, the same guarantee the translation module carries.

**Files:**
- Create: `mwan/go/internal/ifmgr/modules/steering/steering.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/rules.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/applier.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/nftwatch.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/rules_test.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/applier_test.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/nftwatch_test.go`
- Create: `mwan/go/internal/ifmgr/modules/steering/steering_test.go`
- Modify: `mwan/go/internal/ifmgr/roles.go:77-84` (the wan role list)
- Modify: `mwan/go/cmd/mwan/ifmgr_linux.go:25-40` (the registration imports)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go:5-29` (imports),
  `:138-166` (`addWANRoleConfigs`), and append `buildSteeringConfig`
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go` (append a
  builder test)

**Interfaces:**
- Consumes: `netif.ActiveTier` and `netif.TierMember` from Task 3;
  `config.IfMgrSection.HashMode` from Task 2; `sharedWAN.Tier` and `.Weight`
  and the `wanFwMark` helper from Task 3.
- Produces: `steering.Config{InternalIface, InternalNetV4, InternalPrefix,
  OpnsenseEdgeV6, HashMode, HealthStateFile, Members []steering.Member}` and
  `steering.Member{ifmgr.WANRef, Mark uint32, Tier uint8, Weight int}`, read by
  `buildSteeringConfig` in this task.
- Produces: the kernel table `inet mwan_steer` with chain `prerouting`, whose
  contents the cutover capture reads with `nft list table inet mwan_steer`.
- Produces: the module name `steering` in the wan role, between `wan.routes` and
  `npt`.

**What the nftables binding does and does not give us at v0.3.0.** Every
expression named below was read out of
`$(go env GOMODCACHE)/github.com/google/nftables@v0.3.0`. Four notes matter:

1. `expr.Numgen` accepts only `unix.NFT_NG_INCREMENTAL` and
   `unix.NFT_NG_RANDOM` (`expr/numgen.go:46-53`); anything else fails to
   marshal.
2. `expr.Hash` omits `NFTA_HASH_SEED` when `Seed` is zero
   (`expr/hash.go:61-65`). A hash rule carrying no seed does not carry a seed
   this daemon controls, so the source-to-member mapping would not be
   reproducible across reconciles. Seed zero is therefore not expressible, and
   the code uses a fixed non-zero constant instead. This is a deliberate
   departure from the contract's "Seed 0" wording, forced by the library.
3. `Conn.AddTable` and `Conn.AddChain` send `NLM_F_CREATE` without
   `NLM_F_EXCL` (`table.go:69-90`, `chain.go:110-152`), so calling them on
   every pass is idempotent. `CreateTable` uses `NLM_F_EXCL` and would fail on
   the second pass; it is not used.
4. An anonymous map is added with `Conn.AddSet(set, elements)` and referenced by
   `set.Name` and `set.ID` from the rule in the same batch
   (`set.go:493-513`, and the worked example at `nftables_test.go:3564-3614`).
   The library fills `Name` with `"__map%d"` and allocates `ID`, which is what
   nft itself does for a literal map inside a rule.

- [ ] **Step 1: Write the failing balancer tests**

Create `mwan/go/internal/ifmgr/modules/steering/rules_test.go`:

```go
//go:build linux

package steering

import (
	"net/netip"
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// membersForTest is three providers in two tiers with equal weights: the shape
// the gateway runs today.
func membersForTest() []Member {
	return []Member{
		{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, Mark: 1, Tier: 0, Weight: 1},
		{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "enwebpass0"}, Mark: 2, Tier: 0, Weight: 1},
		{WANRef: ifmgr.WANRef{Name: "monkeybrains", Iface: "enmbrains0"}, Mark: 3, Tier: 1, Weight: 1},
	}
}

func TestBalancerForSpreadsTheActiveTier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		members []Member
		health  netif.HealthStates
		want    balancer
		wantOK  bool
	}{
		{
			// Two healthy providers in the first tier, one slot each: the
			// half-and-half split the three lines in the ruleset file express.
			name:    "two equal providers in the first tier",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}},
			wantOK: true,
		},
		{
			// Weight two and weight one: three slots, two of them the heavier
			// provider's mark, assigned in ascending mark order.
			name: "unequal weights take proportional slots",
			members: []Member{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, Mark: 1, Tier: 0, Weight: 2},
				{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "enwebpass0"}, Mark: 2, Tier: 0, Weight: 1},
			},
			health: netif.HealthStates{
				"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 0, Modulus: 3, Slots: []uint32{1, 1, 2}},
			wantOK: true,
		},
		{
			// One healthy provider takes everything with no generator, whatever
			// its weight: there is nothing to divide.
			name:    "a lone healthy provider in the first tier",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateHealthy, "webpass": netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 1, Modulus: 0, Slots: nil},
			wantOK: true,
		},
		{
			// The first tier is out, so the next tier that is not carries. It
			// holds one provider, so its mark is set outright. This is today's
			// Monkeybrains behavior.
			name:    "the fallback tier's lone provider",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateUnhealthy, "webpass": netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 3, Modulus: 0, Slots: nil},
			wantOK: true,
		},
		{
			// Nothing is healthy anywhere, so no mark is assigned at all and
			// the chain is left empty.
			name:    "no healthy provider anywhere",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateUnhealthy, "webpass": netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateUnhealthy,
			},
			want:   balancer{Mark: 0, Modulus: 0, Slots: nil},
			wantOK: false,
		},
		{
			// No verdict has been recorded, so every provider reads healthy and
			// the first tier spreads. This is the startup pass.
			name:    "no verdict recorded activates the first tier",
			members: membersForTest(),
			health:  netif.HealthStates{},
			want:    balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}},
			wantOK:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := balancerFor(tc.members, tc.health)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("balancer mismatch\ngot:  %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

// TestBuildRulesCoversBothFamilies pins the three rules the module programs:
// internal IPv4 arriving on the internal link, the router's own IPv6 edge
// address, and the internal IPv6 prefix.
func TestBuildRulesCoversBothFamilies(t *testing.T) {
	t.Parallel()

	assign := balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}}
	rules := buildRules(ruleInput{
		InternalIface:  "enmwanbr0",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdgeV6: netip.MustParseAddr("3d06:bad:b01:201::1"),
		Mode:           hashModeRandom,
		Assign:         assign,
	})

	want := []steerRule{
		{
			IifName: "enmwanbr0",
			Source:  netip.MustParsePrefix("10.250.250.0/29"),
			Mode:    hashModeRandom,
			Assign:  assign,
		},
		{
			IifName: "",
			Source:  netip.MustParsePrefix("3d06:bad:b01:201::1/128"),
			Mode:    hashModeRandom,
			Assign:  assign,
		},
		{
			IifName: "",
			Source:  netip.MustParsePrefix("3d06:bad:b01::/60"),
			Mode:    hashModeRandom,
			Assign:  assign,
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules mismatch\ngot:  %#v\nwant: %#v", rules, want)
	}
}
```

- [ ] **Step 2: Write the failing applier and watcher tests**

Create `mwan/go/internal/ifmgr/modules/steering/applier_test.go`:

```go
//go:build linux

package steering

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// fakeConn captures the batch the applier builds so tests can assert on the
// table and chain creation, the flush-then-add ordering, the rules, and the
// single-transaction guarantee without a kernel netlink socket.
type fakeConn struct {
	ops        []string
	rules      []*nftables.Rule
	sets       []*nftables.Set
	elements   [][]nftables.SetElement
	flushCount int
	flushErr   error
}

func (f *fakeConn) AddTable(t *nftables.Table) *nftables.Table {
	f.ops = append(f.ops, "addtable:"+t.Name)
	return t
}

func (f *fakeConn) AddChain(c *nftables.Chain) *nftables.Chain {
	f.ops = append(f.ops, "addchain:"+c.Name)
	return c
}

func (f *fakeConn) FlushChain(c *nftables.Chain) {
	f.ops = append(f.ops, "flushchain:"+c.Name)
}

func (f *fakeConn) AddSet(s *nftables.Set, vals []nftables.SetElement) error {
	f.ops = append(f.ops, "addset")
	// The real connection allocates the id and the name here, which the rule
	// then references, so the fake does the same.
	s.ID = uint32(len(f.sets) + 1)
	s.Name = "__map%d"
	f.sets = append(f.sets, s)
	f.elements = append(f.elements, vals)
	return nil
}

func (f *fakeConn) AddRule(r *nftables.Rule) *nftables.Rule {
	f.ops = append(f.ops, "addrule:"+r.Chain.Name)
	f.rules = append(f.rules, r)
	return r
}

func (f *fakeConn) Flush() error {
	f.flushCount++
	f.ops = append(f.ops, "flush")
	return f.flushErr
}

func newFakeApplier() (*fakeConn, *nftApplier) {
	fake := &fakeConn{
		ops: nil, rules: nil, sets: nil, elements: nil, flushCount: 0, flushErr: nil,
	}
	return fake, &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}
}

func spreadRulesForTest(mode hashMode) []steerRule {
	return buildRules(ruleInput{
		InternalIface:  "enmwanbr0",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdgeV6: netip.MustParseAddr("3d06:bad:b01:201::1"),
		Mode:           mode,
		Assign:         balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}},
	})
}

// TestApplierCreatesOwnsAndCommitsInOneTransaction is the traffic-safety
// property: one reconcile creates the table and the chain, empties the chain,
// refills it, and commits everything with exactly one Flush.
func TestApplierCreatesOwnsAndCommitsInOneTransaction(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if fake.flushCount != 1 {
		t.Fatalf("Flush called %d times, want exactly 1", fake.flushCount)
	}
	wantOps := []string{
		"addtable:mwan_steer",
		"addchain:prerouting",
		"flushchain:prerouting",
		"addset", "addrule:prerouting",
		"addset", "addrule:prerouting",
		"addset", "addrule:prerouting",
		"flush",
	}
	if len(fake.ops) != len(wantOps) {
		t.Fatalf("op sequence length = %d, want %d\ngot:  %v\nwant: %v",
			len(fake.ops), len(wantOps), fake.ops, wantOps)
	}
	for i := range wantOps {
		if fake.ops[i] != wantOps[i] {
			t.Fatalf("op[%d] = %q, want %q\nfull: %v", i, fake.ops[i], wantOps[i], fake.ops)
		}
	}
}

// TestApplierChainPlacement pins where the chain hooks in. One step after the
// ruleset file's mangle chain at -150 is what lets the mark-zero guard see the
// marks that chain sets.
func TestApplierChainPlacement(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	chain := fake.rules[0].Chain
	if chain.Table.Family != nftables.TableFamilyINet || chain.Table.Name != "mwan_steer" {
		t.Fatalf("table = %v %q, want inet mwan_steer", chain.Table.Family, chain.Table.Name)
	}
	if chain.Type != nftables.ChainTypeFilter {
		t.Fatalf("chain type = %q, want filter", chain.Type)
	}
	if chain.Hooknum == nil || *chain.Hooknum != *nftables.ChainHookPrerouting {
		t.Fatalf("chain hook = %v, want prerouting", chain.Hooknum)
	}
	if chain.Priority == nil || *chain.Priority != -149 {
		t.Fatalf("chain priority = %v, want -149", chain.Priority)
	}
	if chain.Policy == nil || *chain.Policy != nftables.ChainPolicyAccept {
		t.Fatalf("chain policy = %v, want accept", chain.Policy)
	}
}

// TestApplierSpreadRuleShape asserts one spread rule carries, in order, the
// incoming-link match, the family match, the source match, the mark-zero guard,
// the new-flow guard, the generator, the map lookup, and the mark write.
func TestApplierSpreadRuleShape(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	var numgen *expr.Numgen
	var lookup *expr.Lookup
	markWrites := 0
	for _, e := range fake.rules[0].Exprs {
		switch v := e.(type) {
		case *expr.Numgen:
			numgen = v
		case *expr.Lookup:
			lookup = v
		case *expr.Meta:
			if v.Key == expr.MetaKeyMARK && v.SourceRegister {
				markWrites++
			}
		}
	}
	if numgen == nil {
		t.Fatal("spread rule carries no generator")
	}
	if numgen.Type != unix.NFT_NG_RANDOM {
		t.Fatalf("generator type = %d, want random", numgen.Type)
	}
	if numgen.Modulus != 2 {
		t.Fatalf("generator modulus = %d, want 2", numgen.Modulus)
	}
	if lookup == nil || !lookup.IsDestRegSet {
		t.Fatalf("spread rule carries no map lookup writing a register: %#v", lookup)
	}
	if lookup.SetID != fake.sets[0].ID || lookup.SetName != fake.sets[0].Name {
		t.Fatalf("lookup names set %q/%d, want %q/%d",
			lookup.SetName, lookup.SetID, fake.sets[0].Name, fake.sets[0].ID)
	}
	if markWrites != 1 {
		t.Fatalf("mark writes = %d, want exactly 1", markWrites)
	}

	// The map is the slot-to-mark table, keyed in the kernel's own byte order
	// because both the generated slot and the mark are host-order words.
	wantElements := []nftables.SetElement{
		{Key: binaryutil.NativeEndian.PutUint32(0), Val: binaryutil.NativeEndian.PutUint32(1)},
		{Key: binaryutil.NativeEndian.PutUint32(1), Val: binaryutil.NativeEndian.PutUint32(2)},
	}
	got := fake.elements[0]
	if len(got) != len(wantElements) {
		t.Fatalf("map has %d elements, want %d", len(got), len(wantElements))
	}
	for i := range wantElements {
		if !bytes.Equal(got[i].Key, wantElements[i].Key) || !bytes.Equal(got[i].Val, wantElements[i].Val) {
			t.Fatalf("map element %d = %v -> %v, want %v -> %v",
				i, got[i].Key, got[i].Val, wantElements[i].Key, wantElements[i].Val)
		}
	}
	if !fake.sets[0].IsMap || !fake.sets[0].Anonymous || !fake.sets[0].Constant {
		t.Fatalf("set flags = map:%v anonymous:%v constant:%v, want all true",
			fake.sets[0].IsMap, fake.sets[0].Anonymous, fake.sets[0].Constant)
	}
}

// TestApplierGuardsEverySpreadRule pins the two guards on every rule: the mark
// must still be zero, which is what preserves the control-plane pins the
// ruleset file sets earlier in the pass, and the flow must be new, which is
// what keeps an established flow on the provider it already has.
func TestApplierGuardsEverySpreadRule(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for index, rule := range fake.rules {
		sawMarkRead, sawCtState, sawCtMask := false, false, false
		for _, e := range rule.Exprs {
			switch v := e.(type) {
			case *expr.Meta:
				if v.Key == expr.MetaKeyMARK && !v.SourceRegister {
					sawMarkRead = true
				}
			case *expr.Ct:
				if v.Key == expr.CtKeySTATE {
					sawCtState = true
				}
			case *expr.Bitwise:
				if bytes.Equal(v.Mask, binaryutil.NativeEndian.PutUint32(0x08)) {
					sawCtMask = true
				}
			}
		}
		if !sawMarkRead || !sawCtState || !sawCtMask {
			t.Fatalf("rule %d missing a guard: mark=%v ct=%v mask=%v",
				index, sawMarkRead, sawCtState, sawCtMask)
		}
	}
}

// TestApplierSingleMemberSetsTheMarkOutright asserts that one carrying provider
// produces an immediate mark with no generator and no map, which is what the
// gateway runs while only one provider is healthy.
func TestApplierSingleMemberSetsTheMarkOutright(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	rules := buildRules(ruleInput{
		InternalIface:  "enmwanbr0",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdgeV6: netip.MustParseAddr("3d06:bad:b01:201::1"),
		Mode:           hashModeRandom,
		Assign:         balancer{Mark: 3, Modulus: 0, Slots: nil},
	})
	if err := app.Apply(context.Background(), slog.Default(), rules); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(fake.sets) != 0 {
		t.Fatalf("added %d maps for a single provider, want 0", len(fake.sets))
	}
	var immediate *expr.Immediate
	for _, e := range fake.rules[0].Exprs {
		if value, isImmediate := e.(*expr.Immediate); isImmediate {
			immediate = value
		}
		if _, isNumgen := e.(*expr.Numgen); isNumgen {
			t.Fatal("single-provider rule carries a generator")
		}
	}
	if immediate == nil {
		t.Fatal("single-provider rule carries no immediate mark")
	}
	if !bytes.Equal(immediate.Data, binaryutil.NativeEndian.PutUint32(3)) {
		t.Fatalf("immediate mark = %v, want 3", immediate.Data)
	}
}

// TestApplierSourceHashUsesJenkinsOverTheSourceAddress asserts the source mode
// derives the slot from the source address with the daemon's own seed, so one
// source keeps landing on one provider across reconciles.
func TestApplierSourceHashUsesJenkinsOverTheSourceAddress(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeSource)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	var hash *expr.Hash
	for _, e := range fake.rules[0].Exprs {
		if value, isHash := e.(*expr.Hash); isHash {
			hash = value
		}
		if _, isNumgen := e.(*expr.Numgen); isNumgen {
			t.Fatal("source mode carries a random generator")
		}
	}
	if hash == nil {
		t.Fatal("source mode carries no hash")
	}
	if hash.Type != expr.HashTypeJenkins {
		t.Fatalf("hash type = %d, want jenkins", hash.Type)
	}
	// The first rule matches the internal IPv4 network, so the hash reads the
	// four-byte IPv4 source address only.
	if hash.Length != 4 {
		t.Fatalf("hash length = %d, want 4 (IPv4 source only)", hash.Length)
	}
	if hash.Modulus != 2 {
		t.Fatalf("hash modulus = %d, want 2", hash.Modulus)
	}
	if hash.Seed == 0 {
		t.Fatal("hash seed is zero; the binding omits the attribute and the kernel picks its own")
	}
	if hash.SourceRegister != unix.NFT_REG32_00 {
		t.Fatalf("hash source register = %d, want NFT_REG32_00", hash.SourceRegister)
	}
}

// TestApplierSourceDestinationHashReadsBothAddresses asserts the concatenated
// mode loads the destination directly after the source in the 32-bit register
// file and hashes both together.
func TestApplierSourceDestinationHashReadsBothAddresses(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeSourceDestination)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	// The third rule matches the internal IPv6 prefix, so both addresses are
	// sixteen bytes and the destination sits four 32-bit slots along.
	var hash *expr.Hash
	daddrLoads := 0
	for _, e := range fake.rules[2].Exprs {
		switch value := e.(type) {
		case *expr.Hash:
			hash = value
		case *expr.Payload:
			if value.Offset == 24 && value.DestRegister == unix.NFT_REG32_04 {
				daddrLoads++
			}
		}
	}
	if hash == nil {
		t.Fatal("source-destination mode carries no hash")
	}
	if daddrLoads != 1 {
		t.Fatalf("destination loads = %d, want 1", daddrLoads)
	}
	if hash.Length != 32 {
		t.Fatalf("hash length = %d, want 32 (both IPv6 addresses)", hash.Length)
	}
}

// TestApplierEmptyStillCreatesAndCommits confirms a pass with no healthy
// provider still creates the table and the chain and commits an empty chain, so
// the previous split is removed rather than left behind.
func TestApplierEmptyStillCreatesAndCommits(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), nil); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if fake.flushCount != 1 {
		t.Fatalf("Flush called %d times, want 1", fake.flushCount)
	}
	if len(fake.rules) != 0 {
		t.Fatalf("added %d rules, want 0", len(fake.rules))
	}
	want := []string{"addtable:mwan_steer", "addchain:prerouting", "flushchain:prerouting", "flush"}
	if len(fake.ops) != len(want) {
		t.Fatalf("ops = %v, want %v", fake.ops, want)
	}
}

// _ is a compile-time guard that *nftables.Conn satisfies nftConn, so the
// production applier can pass a real connection through the same seam.
var _ nftConn = (*nftables.Conn)(nil)
```

Create `mwan/go/internal/ifmgr/modules/steering/nftwatch_test.go`:

```go
//go:build linux

package steering

import (
	"testing"

	"github.com/google/nftables"
)

func TestNftEventWipesSteering(t *testing.T) {
	t.Parallel()

	steerTable := &nftables.Table{Family: nftables.TableFamilyINet, Name: steerTableName}
	otherInetTable := &nftables.Table{Family: nftables.TableFamilyINet, Name: "filter"}
	v6SteerTable := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: steerTableName}

	tests := []struct {
		name  string
		event *nftables.MonitorEvent
		want  bool
	}{
		{
			name:  "delete the steering table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: steerTable},
			want:  true,
		},
		{
			name: "delete the steering chain",
			event: &nftables.MonitorEvent{
				Type: nftables.MonitorEventTypeDelChain,
				Data: &nftables.Chain{Name: steerChainName, Table: steerTable},
			},
			want: true,
		},
		{
			// The module's own Apply flushes the chain on every reconcile,
			// which emits rule deletes. Matching them would make each pass
			// request another and spin.
			name: "delete a steering rule is not a wipe",
			event: &nftables.MonitorEvent{
				Type: nftables.MonitorEventTypeDelRule,
				Data: &nftables.Rule{Table: steerTable, Chain: &nftables.Chain{Name: steerChainName}},
			},
			want: false,
		},
		{
			// Apply creates the table and the chain every pass, so their
			// creation events must not be a wipe signal either.
			name:  "create the steering table is not a wipe",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeNewTable, Data: steerTable},
			want:  false,
		},
		{
			name:  "delete a different inet table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: otherInetTable},
			want:  false,
		},
		{
			name:  "delete a same-named table in another family",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: v6SteerTable},
			want:  false,
		},
		{name: "nil event", event: nil, want: false},
		{
			name:  "delete a chain with no table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelChain, Data: &nftables.Chain{}},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := nftEventWipesSteering(test.event); got != test.want {
				t.Fatalf("nftEventWipesSteering = %t, want %t", got, test.want)
			}
		})
	}
}
```

Create `mwan/go/internal/ifmgr/modules/steering/steering_test.go`:

```go
//go:build linux

package steering

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
)

// fakeApplier records the rule set the module hands it, so a test can assert on
// the computed rules without a kernel.
type fakeApplier struct {
	calls int
	last  []steerRule
	err   error
}

func (f *fakeApplier) Apply(_ context.Context, _ *slog.Logger, desired []steerRule) error {
	f.calls++
	f.last = desired
	return f.err
}

func testConfig() Config {
	return Config{
		InternalIface:   "enmwanbr0",
		InternalNetV4:   "10.250.250.0/29",
		InternalPrefix:  "3d06:bad:b01::/60",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		HashMode:        "random",
		HealthStateFile: "/run/mwan-health.state",
		Members:         membersForTest(),
	}
}

func newTestModule(t *testing.T, cfg Config, health netif.HealthStates) (*Module, *fakeApplier) {
	t.Helper()
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	typed, isModule := module.(*Module)
	if !isModule {
		t.Fatalf("New returned %T, want *Module", module)
	}
	if err := typed.parse(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	typed.Env = &ifmgr.Env{Log: slog.Default(), Alerts: ifmgr.WrapNotifier(notify.NullNotifier{})}
	typed.Log = slog.Default()
	typed.readHealth = func(string) (netif.HealthStates, error) { return health, nil }
	applier := &fakeApplier{calls: 0, last: nil, err: nil}
	typed.apply = applier
	return typed, applier
}

// TestReconcileProgramsTheActiveTierSplit is the happy path: both first-tier
// providers are healthy, so one reconcile hands the applier three rules whose
// map spreads across both marks.
func TestReconcileProgramsTheActiveTierSplit(t *testing.T) {
	t.Parallel()

	module, applier := newTestModule(t, testConfig(), netif.HealthStates{
		"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	})
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if applier.calls != 1 {
		t.Fatalf("applier called %d times, want 1", applier.calls)
	}
	if len(applier.last) != 3 {
		t.Fatalf("rule count = %d, want 3", len(applier.last))
	}
	for index, rule := range applier.last {
		want := balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}}
		if !reflect.DeepEqual(rule.Assign, want) {
			t.Fatalf("rule %d balancer = %#v, want %#v", index, rule.Assign, want)
		}
	}
}

// TestReconcileWithNoHealthyProviderProgramsNothing pins the empty case: the
// applier is still called, so the previous split is cleared, but it is handed
// no rules.
func TestReconcileWithNoHealthyProviderProgramsNothing(t *testing.T) {
	t.Parallel()

	module, applier := newTestModule(t, testConfig(), netif.HealthStates{
		"att": netif.HealthStateUnhealthy, "webpass": netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateUnhealthy,
	})
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if applier.calls != 1 {
		t.Fatalf("applier called %d times, want 1", applier.calls)
	}
	if len(applier.last) != 0 {
		t.Fatalf("rule count = %d, want 0", len(applier.last))
	}
}

// TestModuleDisablesWithoutMembers checks Init self-disables when the provider
// list is empty, so the wan role can list the module unconditionally.
func TestModuleDisablesWithoutMembers(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Members = nil
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &ifmgr.Env{Log: slog.Default(), Alerts: ifmgr.WrapNotifier(notify.NullNotifier{})}
	initErr := module.Init(context.Background(), env)
	if initErr == nil {
		t.Fatal("Init should disable when no providers are configured")
	}
	if !strings.Contains(initErr.Error(), "disabled") {
		t.Fatalf("Init error = %v, want the disabled sentinel", initErr)
	}
}

// TestParseRejectsAnUnknownHashMode pins the loud failure: a hash mode the
// module cannot program stops the daemon rather than silently balancing some
// other way.
func TestParseRejectsAnUnknownHashMode(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.HashMode = "round-robin"
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	typed, isModule := module.(*Module)
	if !isModule {
		t.Fatalf("New returned %T, want *Module", module)
	}
	if parseErr := typed.parse(); parseErr == nil {
		t.Fatal("parse accepted an unknown hash mode")
	}
}

// TestParseRejectsAWeightSumThatCannotBeProgrammed pins the bound on the map: a
// weight the kernel would turn into a runaway element list is a configuration
// error, not something to attempt.
func TestParseRejectsAWeightSumThatCannotBeProgrammed(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Members[0].Weight = maxSlots
	module, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	typed, isModule := module.(*Module)
	if !isModule {
		t.Fatalf("New returned %T, want *Module", module)
	}
	if parseErr := typed.parse(); parseErr == nil {
		t.Fatal("parse accepted a weight sum above the bound")
	}
}

// TestNoTeardownMethod is the traffic-continuity guard: the module must not
// expose a stop or teardown that would empty the chain on exit. The kernel keeps
// marking on the last programmed rules across a binary swap.
func TestNoTeardownMethod(t *testing.T) {
	t.Parallel()

	module := &Module{}
	typ := reflect.TypeOf(module)
	for _, name := range []string{"Stop", "Close", "Teardown", "Shutdown", "Remove"} {
		if _, found := typ.MethodByName(name); found {
			t.Fatalf("Module exposes %q; steering must not clear its chain on stop", name)
		}
	}
}
```

- [ ] **Step 3: Run the steering tests to verify they fail**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/ifmgr/modules/steering/ -v
```

Expected: FAIL to build, with `no required module provides package
goodkind.io/mwan/internal/ifmgr/modules/steering`.

- [ ] **Step 4: Write the rule computation**

Create `mwan/go/internal/ifmgr/modules/steering/rules.go`:

```go
//go:build linux

package steering

import (
	"cmp"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"goodkind.io/mwan/internal/netif"
)

// hashMode selects how a new connection is assigned to a provider of the active
// tier. The values are the model's hash-mode enumeration, so the string the
// configuration carries is the value this module switches on.
type hashMode string

const (
	// hashModeRandom draws a fresh number per connection.
	hashModeRandom hashMode = "random"
	// hashModeSource derives the assignment from the source address, so one
	// internal host keeps using one provider.
	hashModeSource hashMode = "source"
	// hashModeSourceDestination derives it from source and destination, so one
	// host spreads across providers but each conversation stays put.
	hashModeSourceDestination hashMode = "source-destination"
)

// balancer is how one rule assigns a mark. A single carrying provider takes the
// immediate form; two or more take the generated form.
type balancer struct {
	// Mark is the single mark every matched connection takes. Meaningful only
	// when Slots is empty.
	Mark uint32
	// Modulus is what the generator divides by, always the length of Slots. It
	// is carried rather than derived so the rule builder never has to widen an
	// int at the point it programs the kernel.
	Modulus uint32
	// Slots maps slot index to mark, one entry per weight unit, in ascending
	// mark order. Ascending order is what makes two reconciles with the same
	// provider set produce the same map, which the hash modes depend on.
	Slots []uint32
}

// steerRule is one balancing rule in typed form, independent of the nftables
// wire encoding. Every rule carries the same two guards, so they are not
// fields: the mark must still be zero, which preserves the control-plane pins
// an earlier chain set, and the flow must be new, which leaves an established
// flow on the provider conntrack already gave it.
type steerRule struct {
	// IifName is the incoming link the rule matches, or empty for no match.
	// The IPv6 rules carry none, because a hairpinned reply the router sources
	// from its own edge address does not arrive on the internal link.
	IifName string
	// Source is the source prefix the rule matches. Its family decides which
	// header offsets the rule reads.
	Source netip.Prefix
	// Mode is the hash mode the group is configured with.
	Mode hashMode
	// Assign is how the rule picks the mark.
	Assign balancer
}

// String renders one rule the way the ruleset file wrote its equivalent, for
// error messages and debug logging.
func (r steerRule) String() string {
	where := "saddr " + r.Source.String()
	if r.IifName != "" {
		where = "iif " + r.IifName + " " + where
	}
	if len(r.Assign.Slots) == 0 {
		return where + " mark set " + strconv.FormatUint(uint64(r.Assign.Mark), 10)
	}
	marks := make([]string, 0, len(r.Assign.Slots))
	for _, mark := range r.Assign.Slots {
		marks = append(marks, strconv.FormatUint(uint64(mark), 10))
	}
	return where + " mark set " + string(r.Mode) +
		" mod " + strconv.FormatUint(uint64(r.Assign.Modulus), 10) +
		" map {" + strings.Join(marks, ",") + "}"
}

// ruleInput is everything the rule builder needs for one pass.
type ruleInput struct {
	InternalIface  string
	InternalNetV4  netip.Prefix
	InternalPrefix netip.Prefix
	OpnsenseEdgeV6 netip.Addr
	Mode           hashMode
	Assign         balancer
}

// buildRules returns the three rules the module programs, in the order the
// ruleset file wrote them. The first covers internal IPv4 traffic arriving on
// the internal link. The second covers replies the router sources from its own
// edge address, which a hairpinned inbound flow produces and which must leave
// over the provider that carried it in. The third covers internal IPv6 traffic.
func buildRules(in ruleInput) []steerRule {
	return []steerRule{
		{
			IifName: in.InternalIface,
			Source:  in.InternalNetV4,
			Mode:    in.Mode,
			Assign:  in.Assign,
		},
		{
			IifName: "",
			Source:  netip.PrefixFrom(in.OpnsenseEdgeV6, 128),
			Mode:    in.Mode,
			Assign:  in.Assign,
		},
		{
			IifName: "",
			Source:  in.InternalPrefix,
			Mode:    in.Mode,
			Assign:  in.Assign,
		},
	}
}

// balancerFor returns how the active tier's healthy providers share new
// connections: one slot per weight unit, ordered by ascending mark. ok is false
// when no provider is healthy anywhere, which programs no rules at all rather
// than a mark whose table holds no route.
func balancerFor(members []Member, health netif.HealthStates) (balancer, bool) {
	none := balancer{Mark: 0, Modulus: 0, Slots: nil}
	activeTier, anyHealthy := netif.ActiveTier(tierMembers(members), health)
	if !anyHealthy {
		return none, false
	}
	carrying := make([]Member, 0, len(members))
	for _, member := range members {
		if member.Tier != activeTier || !netif.HealthIsHealthy(health.State(member.Name)) {
			continue
		}
		carrying = append(carrying, member)
	}
	if len(carrying) == 0 {
		return none, false
	}
	slices.SortFunc(carrying, func(left Member, right Member) int {
		return cmp.Compare(left.Mark, right.Mark)
	})
	if len(carrying) == 1 {
		// One provider takes everything whatever its weight, because there is
		// nothing to divide. This is also the shape the gateway runs in while
		// only the fallback tier is up.
		return balancer{Mark: carrying[0].Mark, Modulus: 0, Slots: nil}, true
	}
	slots := make([]uint32, 0, len(carrying))
	modulus := uint32(0)
	for _, member := range carrying {
		for range member.Weight {
			slots = append(slots, member.Mark)
			modulus++
		}
	}
	return balancer{Mark: 0, Modulus: modulus, Slots: slots}, true
}

// tierMembers projects the configured providers onto the list the shared
// active-tier function reads.
func tierMembers(members []Member) []netif.TierMember {
	tiers := make([]netif.TierMember, 0, len(members))
	for _, member := range members {
		tiers = append(tiers, netif.TierMember{Name: member.Name, Tier: member.Tier})
	}
	return tiers
}
```

- [ ] **Step 5: Write the nftables applier**

Create `mwan/go/internal/ifmgr/modules/steering/applier.go`:

```go
//go:build linux

package steering

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	// steerTableName and steerChainName are the table and chain this module
	// owns outright. Nothing else writes them, so the module may empty the
	// chain wholesale on every pass.
	steerTableName = "mwan_steer"
	steerChainName = "prerouting"

	// steerChainPriority puts the chain one step after the ruleset file's
	// mangle chain at -150, so that chain's connection-mark restore and its
	// per-link ingress marks have already run when this chain's mark-zero guard
	// looks. Reversing the two would overwrite the control-plane pins.
	steerChainPriority nftables.ChainPriority = -149

	// ipv4SaddrOffset, ipv4DaddrOffset, and ipv4AddrLen locate the IPv4
	// addresses relative to the network header.
	ipv4SaddrOffset uint32 = 12
	ipv4DaddrOffset uint32 = 16
	ipv4AddrLen     uint32 = 4
	ipv4AddrBits    int    = 32

	// ipv6SaddrOffset, ipv6DaddrOffset, and ipv6AddrLen locate the IPv6
	// addresses relative to the network header.
	ipv6SaddrOffset uint32 = 8
	ipv6DaddrOffset uint32 = 24
	ipv6AddrLen     uint32 = 16
	ipv6AddrBits    int    = 128

	// ctStateNew is NF_CT_STATE_BIT(IP_CT_NEW) from
	// linux/netfilter/nf_conntrack_common.h: 1 shifted left by IP_CT_NEW plus
	// one, with IP_CT_NEW equal to 2. It is the bit nft's `ct state new` masks,
	// and the ruleset file's ingress rules already match on it.
	ctStateNew uint32 = 0x08
	// ctStateLen is the width of the conntrack state word.
	ctStateLen uint32 = 4

	// hashSeed is the seed every hashed balancer uses. It is deliberately not
	// zero: google/nftables v0.3.0 omits NFTA_HASH_SEED when the field is zero
	// (expr/hash.go:61), and a rule that carries no seed does not carry a seed
	// this daemon controls, so one source would land on a different provider
	// after a reconcile. A fixed seed keeps a source on a provider for as long
	// as the provider set and the weights hold.
	hashSeed uint32 = 0x6d77616e

	// hashKeyRegister is the first 32-bit register. The hash modes load the
	// addresses into the 32-bit register file rather than into registers 1 and
	// 2, because those are four 32-bit slots apart and a concatenation must be
	// contiguous for one hash to read both addresses. This is the register
	// layout nft itself compiles a concatenated hash into.
	hashKeyRegister uint32 = unix.NFT_REG32_00
	// hashKeyRegisterV4Second holds the IPv4 destination address, one 32-bit
	// slot after the four-byte source address.
	hashKeyRegisterV4Second uint32 = unix.NFT_REG32_01
	// hashKeyRegisterV6Second holds the IPv6 destination address, four 32-bit
	// slots after the sixteen-byte source address.
	hashKeyRegisterV6Second uint32 = unix.NFT_REG32_04
)

// nftConn is the subset of *nftables.Conn the applier drives. Injecting it lets
// tests capture the batch without opening a kernel netlink socket.
type nftConn interface {
	AddTable(t *nftables.Table) *nftables.Table
	AddChain(c *nftables.Chain) *nftables.Chain
	FlushChain(c *nftables.Chain)
	AddSet(s *nftables.Set, vals []nftables.SetElement) error
	AddRule(r *nftables.Rule) *nftables.Rule
	Flush() error
}

// applier commits a desired steering rule set. The module depends on this
// interface so its tests can substitute a fake that records what was computed.
type applier interface {
	Apply(ctx context.Context, log *slog.Logger, desired []steerRule) error
}

// nftApplier translates a desired rule set into google/nftables operations and
// replaces the chain's contents in one atomic transaction.
type nftApplier struct {
	newConn func() (nftConn, error)
}

// newNFTApplier returns the production applier backed by a real netlink
// connection opened per Apply call.
func newNFTApplier() *nftApplier {
	return &nftApplier{newConn: defaultNFTConn}
}

func defaultNFTConn() (nftConn, error) {
	conn, err := nftables.New()
	if err != nil {
		slog.Warn("steering: open nftables netlink connection failed", "err", err)
		return nil, fmt.Errorf("nftables.New: %w", err)
	}
	return conn, nil
}

// Apply creates the table and the chain, replaces the chain's contents with
// desired, and commits everything in one netlink batch. The table and the chain
// are created on every pass rather than once at Init, because an nftables
// reload begins with a ruleset flush that deletes them; AddTable and AddChain
// carry the create flag without the exclusive flag, so a pass over structures
// that still exist is a no-op. Nothing here ever deletes a table or a chain.
func (a *nftApplier) Apply(ctx context.Context, log *slog.Logger, desired []steerRule) error {
	conn, err := a.newConn()
	if err != nil {
		return err
	}

	table := &nftables.Table{
		Name:   steerTableName,
		Use:    0,
		Flags:  0,
		Family: nftables.TableFamilyINet,
	}
	conn.AddTable(table)

	policy := nftables.ChainPolicyAccept
	priority := steerChainPriority
	chain := &nftables.Chain{
		Name:     steerChainName,
		Table:    table,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: &priority,
		Type:     nftables.ChainTypeFilter,
		Policy:   &policy,
		Device:   "",
	}
	conn.AddChain(chain)

	// The chain is emptied and refilled inside the same batch, committed by a
	// single Flush, so no packet ever sees a half-written chain.
	conn.FlushChain(chain)
	for _, rule := range desired {
		exprs, buildErr := ruleExprs(conn, table, rule)
		if buildErr != nil {
			return fmt.Errorf("build steering rule %s: %w", rule, buildErr)
		}
		conn.AddRule(&nftables.Rule{
			Table: table, Chain: chain, Position: 0, Handle: 0,
			Flags: 0, Exprs: exprs, UserData: nil,
		})
	}

	if err := conn.Flush(); err != nil {
		log.WarnContext(ctx, "steering: nft flush failed", "err", err)
		return fmt.Errorf("nft flush: %w", err)
	}
	return nil
}

// ruleExprs translates one typed rule into its ordered expressions: the
// incoming-link match, the address-family match, the source-address match, the
// two guards, and the mark assignment. The family match is not optional: the
// chain lives in an inet table, where the network header layout is unknown
// until the protocol has been compared.
func ruleExprs(conn nftConn, table *nftables.Table, rule steerRule) ([]expr.Any, error) {
	exprs := make([]expr.Any, 0, 20)
	if rule.IifName != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, SourceRegister: false, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(rule.IifName)},
		)
	}
	exprs = append(exprs, familyMatchExprs(rule.Source)...)
	exprs = append(exprs, sourceMatchExprs(rule.Source)...)
	exprs = append(exprs, markZeroGuardExprs()...)
	exprs = append(exprs, ctStateNewExprs()...)
	assign, err := assignExprs(conn, table, rule)
	if err != nil {
		return nil, err
	}
	return append(exprs, assign...), nil
}

// familyMatchExprs compares the packet's protocol family, which an inet chain
// must do before it reads any network-header offset.
func familyMatchExprs(source netip.Prefix) []expr.Any {
	proto := byte(unix.NFPROTO_IPV6)
	if source.Addr().Is4() {
		proto = byte(unix.NFPROTO_IPV4)
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, SourceRegister: false, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

// sourceMatchExprs matches the packet's source address against p. A host prefix
// is an exact compare; a shorter one masks the loaded address before comparing
// it to the network.
func sourceMatchExprs(source netip.Prefix) []expr.Any {
	source = source.Masked()
	offset, length, bits := ipv6SaddrOffset, ipv6AddrLen, ipv6AddrBits
	if source.Addr().Is4() {
		offset, length, bits = ipv4SaddrOffset, ipv4AddrLen, ipv4AddrBits
	}
	load := &expr.Payload{
		OperationType: expr.PayloadLoad, DestRegister: 1, SourceRegister: 0,
		Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length,
		CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
	}
	network := addrBytes(source.Addr())
	if source.Bits() == bits {
		return []expr.Any{
			load,
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: network},
		}
	}
	return []expr.Any{
		load,
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: length,
			Mask: net.CIDRMask(source.Bits(), bits), Xor: make([]byte, length),
		},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: network},
	}
}

// markZeroGuardExprs matches a packet whose mark is still zero. It is what
// preserves the control-plane pins the ruleset file sets earlier in the pass: a
// packet already carrying a mark falls through this rule untouched.
func markZeroGuardExprs() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: false, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
	}
}

// ctStateNewExprs matches a packet opening a new flow. An established flow keeps
// the provider conntrack already assigned it, which is what the mangle chain's
// connection-mark restore depends on.
func ctStateNewExprs() []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, SourceRegister: false, Key: expr.CtKeySTATE, Direction: 0},
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: ctStateLen,
			Mask: binaryutil.NativeEndian.PutUint32(ctStateNew),
			Xor:  binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
	}
}

// assignExprs builds the mark assignment. A single carrying provider is an
// immediate value; two or more are a generated slot looked up in an anonymous
// map from slot to mark, which is the shape the three lines in the ruleset file
// used, generalized past two providers and past equal weights.
func assignExprs(conn nftConn, table *nftables.Table, rule steerRule) ([]expr.Any, error) {
	if len(rule.Assign.Slots) == 0 {
		return []expr.Any{
			&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(rule.Assign.Mark)},
			&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
		}, nil
	}
	generator, err := generatorExprs(rule)
	if err != nil {
		return nil, err
	}
	set, err := slotMapSet(conn, table, rule.Assign.Slots)
	if err != nil {
		return nil, err
	}
	return append(generator,
		&expr.Lookup{
			SourceRegister: 1, DestRegister: 1, IsDestRegSet: true,
			SetID: set.ID, SetName: set.Name, Invert: false,
		},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
	), nil
}

// generatorExprs produces the slot index the map is keyed on. random draws a
// fresh number per connection; the two hash modes derive it from the addresses,
// so one source, or one source and destination pair, keeps landing on the same
// provider while the provider set and the weights hold.
func generatorExprs(rule steerRule) ([]expr.Any, error) {
	switch rule.Mode {
	case hashModeRandom:
		return []expr.Any{
			&expr.Numgen{
				Register: 1, Modulus: rule.Assign.Modulus,
				Type: unix.NFT_NG_RANDOM, Offset: 0,
			},
		}, nil
	case hashModeSource:
		return hashExprs(rule.Source, rule.Assign.Modulus, false), nil
	case hashModeSourceDestination:
		return hashExprs(rule.Source, rule.Assign.Modulus, true), nil
	}
	return nil, fmt.Errorf("steering: unknown hash mode %q", string(rule.Mode))
}

// hashExprs loads the addresses into the 32-bit register file and hashes them
// into the slot index. The addresses are loaded again here rather than reused
// from the source match, because the two guards in between overwrite the
// register the match used, and a concatenation must sit in adjacent 32-bit
// slots for one hash to read both.
func hashExprs(source netip.Prefix, modulus uint32, withDestination bool) []expr.Any {
	saddrOffset, daddrOffset := ipv6SaddrOffset, ipv6DaddrOffset
	addrLen, secondRegister := ipv6AddrLen, hashKeyRegisterV6Second
	if source.Addr().Is4() {
		saddrOffset, daddrOffset = ipv4SaddrOffset, ipv4DaddrOffset
		addrLen, secondRegister = ipv4AddrLen, hashKeyRegisterV4Second
	}
	exprs := []expr.Any{
		&expr.Payload{
			OperationType: expr.PayloadLoad, DestRegister: hashKeyRegister, SourceRegister: 0,
			Base: expr.PayloadBaseNetworkHeader, Offset: saddrOffset, Len: addrLen,
			CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
		},
	}
	keyLength := addrLen
	if withDestination {
		exprs = append(exprs, &expr.Payload{
			OperationType: expr.PayloadLoad, DestRegister: secondRegister, SourceRegister: 0,
			Base: expr.PayloadBaseNetworkHeader, Offset: daddrOffset, Len: addrLen,
			CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
		})
		keyLength = addrLen * 2
	}
	return append(exprs, &expr.Hash{
		SourceRegister: hashKeyRegister, DestRegister: 1, Length: keyLength,
		Modulus: modulus, Seed: hashSeed, Offset: 0, Type: expr.HashTypeJenkins,
	})
}

// slotMapSet adds the anonymous slot-to-mark map one rule reads. The map is
// anonymous and constant, which is what nft builds for a literal map inside a
// rule: it lives and dies with the rule that references it, so the chain flush
// that starts every pass takes the previous one with it. Both the slot and the
// mark are host-order words, so both sides use the kernel's own byte order.
func slotMapSet(conn nftConn, table *nftables.Table, slots []uint32) (*nftables.Set, error) {
	set := &nftables.Set{
		Table: table, ID: 0, Name: "",
		Anonymous: true, Constant: true, Interval: false, AutoMerge: false,
		IsMap: true, HasTimeout: false, Counter: false, Dynamic: false,
		Concatenation: false, Timeout: 0,
		KeyType: nftables.TypeInteger, DataType: nftables.TypeMark,
		KeyByteOrder: binaryutil.NativeEndian, Comment: "", Size: 0,
	}
	elements := make([]nftables.SetElement, 0, len(slots))
	slotIndex := uint32(0)
	for _, mark := range slots {
		elements = append(elements, nftables.SetElement{
			Key:         binaryutil.NativeEndian.PutUint32(slotIndex),
			Val:         binaryutil.NativeEndian.PutUint32(mark),
			KeyEnd:      nil,
			IntervalEnd: false,
			VerdictData: nil,
			Timeout:     0,
			Expires:     0,
			Counter:     nil,
			Comment:     "",
		})
		slotIndex++
	}
	if err := conn.AddSet(set, elements); err != nil {
		return nil, fmt.Errorf("add slot map: %w", err)
	}
	return set, nil
}

// addrBytes returns the on-wire form of a: four bytes for IPv4 and sixteen for
// IPv6, matching the payload the rule compares against.
func addrBytes(a netip.Addr) []byte {
	if a.Is4() {
		octets := a.As4()
		return octets[:]
	}
	octets := a.As16()
	return octets[:]
}

// ifname returns the NUL-terminated interface name used for the incoming-link
// compare.
func ifname(name string) []byte {
	return []byte(name + "\x00")
}
```

- [ ] **Step 6: Write the ruleset watcher**

Create `mwan/go/internal/ifmgr/modules/steering/nftwatch.go`:

```go
//go:build linux

package steering

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/nftables"
)

// nftMonitorRetryDelay is the backoff before re-establishing the nftables
// monitor after it errors or its socket closes. Short, so a transient netlink
// error does not leave the module blind to a ruleset flush for long.
const nftMonitorRetryDelay = 2 * time.Second

// watchNFTChanges runs for the module's lifetime and asks the daemon to
// reconcile whenever this module's table or its chain is deleted. An `nft -f`
// or an nftables restart reloads the static ruleset, which begins by flushing
// the whole ruleset and so removes this table. Without an event the chain would
// come back only on the next periodic tick, and until then no new connection
// would be assigned a provider.
func (m *Module) watchNFTChanges(ctx context.Context, log *slog.Logger) {
	log = log.With("goroutine", "nft-watch")
	log.DebugContext(ctx, "steering: nft-watch starting")
	for {
		if ctx.Err() != nil {
			log.DebugContext(ctx, "steering: nft-watch exiting (ctx done)")
			return
		}
		err := m.runNFTMonitor(ctx, log)
		if ctx.Err() != nil {
			log.DebugContext(ctx, "steering: nft-watch exiting (ctx done)")
			return
		}
		log.WarnContext(ctx, "steering: nft monitor ended; retrying", "err", err,
			"retry_in", nftMonitorRetryDelay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(nftMonitorRetryDelay):
		}
	}
}

// runNFTMonitor opens one nftables ruleset monitor and forwards wipes of this
// module's table to the daemon as reconcile requests until the socket closes or
// ctx is done. A dedicated netlink connection keeps the monitor's long-lived
// socket independent of the applier's programming connection.
func (m *Module) runNFTMonitor(ctx context.Context, log *slog.Logger) error {
	conn, err := nftables.New()
	if err != nil {
		log.WarnContext(ctx, "steering: nft monitor open conn failed", "err", err)
		return fmt.Errorf("nftables.New: %w", err)
	}
	monitor := nftables.NewMonitor(
		nftables.WithMonitorAction(nftables.MonitorActionAny),
		nftables.WithMonitorObject(nftables.MonitorObjectRuleset),
	)
	events, err := conn.AddMonitor(monitor)
	if err != nil {
		// Do not close the monitor here. AddMonitor cleans up its own failure
		// paths, and when the netlink dial fails it returns before setting the
		// monitor's closer, so Close would call a nil closer and panic.
		log.WarnContext(ctx, "steering: nft AddMonitor failed", "err", err)
		return fmt.Errorf("nftables AddMonitor: %w", err)
	}
	log.DebugContext(ctx, "steering: nft monitor established")

	for {
		select {
		case <-ctx.Done():
			// Close the monitor, then drain events until the channel closes.
			// google/nftables runs a forwarding goroutine that blocks sending on
			// the unbuffered events channel; returning without draining can
			// strand it mid-send.
			monitor.Close()
			for discarded := range events {
				_ = discarded
			}
			return fmt.Errorf("steering: nft monitor stopping: %w", ctx.Err())
		case event, ok := <-events:
			if !ok {
				monitor.Close()
				return errors.New("nft monitor channel closed")
			}
			m.handleNFTEvent(ctx, log, event)
		}
	}
}

// handleNFTEvent requests a reconcile when event removed this module's table or
// chain. Kept out of the monitor loop body so its info log is a per-wipe state
// change rather than a per-iteration emission.
func (m *Module) handleNFTEvent(
	ctx context.Context, log *slog.Logger, event *nftables.MonitorEvent,
) {
	if event.Error != nil {
		log.WarnContext(ctx, "steering: nft monitor event error", "err", event.Error)
		return
	}
	if !nftEventWipesSteering(event) {
		return
	}
	log.InfoContext(ctx, "steering: table change detected; requesting reconcile",
		"event_type", int(event.Type))
	if m.Env != nil && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("steering: table change")
	}
}

// nftEventWipesSteering reports whether a monitor event removed this module's
// table or its chain, the structural wipes the next reconcile must repair. It
// matches a delete of the table or of a chain inside it, and deliberately does
// not match a rule delete.
//
// Rule deletes are excluded to avoid a feedback loop: the module's own applier
// empties the chain on every reconcile, which emits rule deletes. Matching them
// would make each pass request another and spin. Emptying a chain removes rules
// but never the chain or the table, so a chain or table delete never comes from
// this module's own work.
//
// Creates are excluded for the same reason from the other direction: the
// applier creates the table and the chain on every pass, so their creation
// events are this module's own doing.
func nftEventWipesSteering(event *nftables.MonitorEvent) bool {
	if event == nil {
		return false
	}
	switch data := event.Data.(type) {
	case *nftables.Table:
		return event.Type == nftables.MonitorEventTypeDelTable && isSteerTable(data)
	case *nftables.Chain:
		return event.Type == nftables.MonitorEventTypeDelChain && isSteerTable(data.Table)
	default:
		return false
	}
}

// isSteerTable reports whether table is the inet table this module owns.
func isSteerTable(table *nftables.Table) bool {
	return table != nil &&
		table.Family == nftables.TableFamilyINet &&
		table.Name == steerTableName
}
```

- [ ] **Step 7: Write the module**

Create `mwan/go/internal/ifmgr/modules/steering/steering.go`:

```go
//go:build linux

// Package steering assigns each new connection to a provider. It owns the nft
// table and chain that carry the balancing rules, computes the split from the
// active tier's healthy providers and their weights, and reprograms the chain on
// every reconcile.
//
// It runs in the wan role after wan.routes, so the policy rules its marks select
// are installed before any mark is set, and before npt, which translates the
// prefix the chosen provider delegates. It never deletes a table or a chain, so
// the kernel keeps marking on the last programmed rules across a binary swap.
package steering

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

const (
	moduleName = "steering"

	// maxSlots bounds the sum of the configured weights. The sum becomes the
	// generator's modulus and the map's element count, so a runaway weight in
	// inventory would otherwise build a map with millions of entries inside one
	// netlink batch. A gateway with a handful of providers never approaches it.
	maxSlots = 1024
)

// readHealthFunc is the health-state seam. Injecting it lets module tests run
// against a verdict set without a file on disk.
type readHealthFunc func(path string) (netif.HealthStates, error)

// Member is one provider as the balancer sees it: the shared identity, the mark
// that selects its routing table, the tier it sits in, and its share of that
// tier.
type Member struct {
	ifmgr.WANRef
	Mark   uint32
	Tier   uint8
	Weight int
}

// Config is the runtime config for the steering module. The provider list, the
// translation prefix, and the edge address come from the shared network
// configuration; the internal link and network come from the same wan.routes
// section the routing module reads, so the rules that set a mark and the rules
// that act on it name one link.
type Config struct {
	InternalIface   string
	InternalNetV4   string
	InternalPrefix  string
	OpnsenseEdgeV6  string
	HashMode        string
	HealthStateFile string
	Members         []Member
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return moduleName }

// Module owns the inet steering table and its prerouting chain.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	// Parsed once at Init from cfg.
	internalNetV4  netip.Prefix
	internalPrefix netip.Prefix
	opnsenseEdge   netip.Addr
	mode           hashMode

	// Injectable seams (real implementations wired at Init when nil).
	apply      applier
	readHealth readHealthFunc
}

// Init implements ifmgr.Module. It self-disables when the provider list is
// empty, so the wan role can list the module unconditionally.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	log.InfoContext(ctx, "steering: Init",
		"member_count", len(m.cfg.Members),
		"hash_mode", m.cfg.HashMode,
		"health_state_file", m.cfg.HealthStateFile)

	if len(m.cfg.Members) == 0 {
		log.WarnContext(ctx, "steering: no providers configured; disabling module")
		return fmt.Errorf("%w: steering: no providers", ifmgr.ErrModuleDisabled)
	}
	if err := m.parse(); err != nil {
		log.WarnContext(ctx, "steering: config parse failed", "err", err)
		return err
	}
	if m.apply == nil {
		m.apply = newNFTApplier()
	}
	if m.readHealth == nil {
		m.readHealth = netif.ReadHealthState
	}

	// The rules match on the internal link by name, so a link that comes back
	// with a new index needs the chain rewritten. The provider links are not
	// watched: a provider going away is a health verdict, which the health
	// module owns and which already drives a reconcile.
	ifmgr.StartIfaceMonitors(ctx, log, moduleName, []string{m.cfg.InternalIface}, m.onMonitorEvent)

	// The recover keeps a monitor panic from taking down the daemon.
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "steering: nft-watch panicked",
					"err", fmt.Sprint(recovered))
			}
		}()
		m.watchNFTChanges(ctx, log)
	}()
	return nil
}

// parse validates the configuration and stores the parsed prefixes, the edge
// address, and the hash mode.
func (m *Module) parse() error {
	if err := validateConfig(m.cfg); err != nil {
		return err
	}
	internalNetV4, err := netip.ParsePrefix(m.cfg.InternalNetV4)
	if err != nil {
		slog.Warn("steering: invalid internal_net_v4", "value", m.cfg.InternalNetV4, "err", err)
		return fmt.Errorf("steering: internal_net_v4 %q: %w", m.cfg.InternalNetV4, err)
	}
	if !internalNetV4.Addr().Is4() {
		return fmt.Errorf("steering: internal_net_v4 %q is not IPv4", m.cfg.InternalNetV4)
	}
	internalPrefix, err := netip.ParsePrefix(m.cfg.InternalPrefix)
	if err != nil {
		slog.Warn("steering: invalid internal_prefix", "value", m.cfg.InternalPrefix, "err", err)
		return fmt.Errorf("steering: internal_prefix %q: %w", m.cfg.InternalPrefix, err)
	}
	if internalPrefix.Addr().Is4() {
		return fmt.Errorf("steering: internal_prefix %q is not IPv6", m.cfg.InternalPrefix)
	}
	edge, err := netip.ParseAddr(m.cfg.OpnsenseEdgeV6)
	if err != nil {
		slog.Warn("steering: invalid opnsense_edge_v6", "value", m.cfg.OpnsenseEdgeV6, "err", err)
		return fmt.Errorf("steering: opnsense_edge_v6 %q: %w", m.cfg.OpnsenseEdgeV6, err)
	}
	if edge.Is4() {
		return fmt.Errorf("steering: opnsense_edge_v6 %q is not IPv6", m.cfg.OpnsenseEdgeV6)
	}
	m.internalNetV4 = internalNetV4.Masked()
	m.internalPrefix = internalPrefix.Masked()
	m.opnsenseEdge = edge
	m.mode = hashMode(m.cfg.HashMode)
	return nil
}

// validateConfig rejects a configuration the module cannot program. The
// set-wide routing-number checks run at load time in networkjson; what is left
// here is what this module alone needs.
func validateConfig(cfg Config) error {
	if cfg.InternalIface == "" {
		slog.Warn("steering: missing internal_iface")
		return fmt.Errorf("steering: internal_iface is required")
	}
	switch hashMode(cfg.HashMode) {
	case hashModeRandom, hashModeSource, hashModeSourceDestination:
	default:
		slog.Warn("steering: unknown hash mode", "value", cfg.HashMode)
		return fmt.Errorf("steering: hash_mode %q is not one of random, source, source-destination",
			cfg.HashMode)
	}
	seenNames := make(map[string]bool, len(cfg.Members))
	weightSum := 0
	for i, member := range cfg.Members {
		if member.Name == "" {
			return fmt.Errorf("steering: member[%d]: name is required", i)
		}
		if member.Iface == "" {
			return fmt.Errorf("steering: member[%d] (%s): iface is required", i, member.Name)
		}
		if seenNames[member.Name] {
			return fmt.Errorf("steering: member[%d]: duplicate name %q", i, member.Name)
		}
		seenNames[member.Name] = true
		if member.Mark == 0 {
			return fmt.Errorf("steering: member[%d] (%s): mark must be > 0", i, member.Name)
		}
		if member.Weight < 1 {
			return fmt.Errorf("steering: member[%d] (%s): weight must be >= 1", i, member.Name)
		}
		weightSum += member.Weight
	}
	if weightSum > maxSlots {
		return fmt.Errorf("steering: weights sum to %d, above the %d the balancer programs",
			weightSum, maxSlots)
	}
	return nil
}

// Reconcile implements ifmgr.Module. It reads the current verdicts, computes
// the split across the active tier, and replaces the chain's contents with it.
// The applier is called even when nothing is healthy, so a stale split is
// cleared rather than left marking traffic at a provider that failed.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	log = log.With("op", "reconcile")

	health, err := m.readHealth(m.cfg.HealthStateFile)
	if err != nil {
		log.WarnContext(ctx, "steering: ReadHealthState failed", "err", err)
		return fmt.Errorf("read health state %q: %w", m.cfg.HealthStateFile, err)
	}
	desired := m.desiredRules(health)
	if applyErr := m.apply.Apply(ctx, log, desired); applyErr != nil {
		return fmt.Errorf("apply: %w", applyErr)
	}
	return nil
}

// desiredRules computes this pass's rule set. No healthy provider anywhere
// programs no rules, which leaves the chain empty and every packet unmarked, so
// traffic falls to whatever the main table holds rather than being sent at a
// provider that failed its probes.
func (m *Module) desiredRules(health netif.HealthStates) []steerRule {
	assign, anyCarrying := balancerFor(m.cfg.Members, health)
	if !anyCarrying {
		return nil
	}
	return buildRules(ruleInput{
		InternalIface:  m.cfg.InternalIface,
		InternalNetV4:  m.internalNetV4,
		InternalPrefix: m.internalPrefix,
		OpnsenseEdgeV6: m.opnsenseEdge,
		Mode:           m.mode,
		Assign:         assign,
	})
}

// onMonitorEvent reconciles when the internal link changes state. The rules
// match that link by name, so a link that goes down and comes back needs the
// chain rewritten against its new index.
func (m *Module) onMonitorEvent(ctx context.Context, log *slog.Logger, event netif.Event) {
	if event.Kind != netif.EvLinkUp && event.Kind != netif.EvLinkDown {
		return
	}
	eventLog := log.With("kind", event.Kind.String(), "iface", event.Iface)
	eventLog.DebugContext(ctx, "steering: internal link event, reconciling")
	if err := m.Reconcile(ctx, eventLog); err != nil {
		eventLog.WarnContext(ctx, "steering: reconcile after link event failed", "err", err)
	}
}

// New is the Constructor registered with ifmgr.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		InternalIface:   "",
		InternalNetV4:   "",
		InternalPrefix:  "",
		OpnsenseEdgeV6:  "",
		HashMode:        "",
		HealthStateFile: "",
		Members:         nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("steering: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	if c.HealthStateFile == "" && len(c.Members) > 0 {
		c.HealthStateFile = netif.DefaultHealthStatePath
	}
	return &Module{
		BaseModule:     ifmgr.NewBaseModule(moduleName),
		cfg:            c,
		internalNetV4:  netip.Prefix{},
		internalPrefix: netip.Prefix{},
		opnsenseEdge:   netip.Addr{},
		mode:           "",
		apply:          nil,
		readHealth:     nil,
	}, nil
}

func init() { ifmgr.Register(moduleName, New) }
```

- [ ] **Step 8: Run the steering tests to verify they pass**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/ifmgr/modules/steering/ -v
```

Expected: PASS. The six `TestBalancerFor` subtests, the rule-builder test, the
eight applier tests, the eight watcher subtests, and the six module tests,
`ok goodkind.io/mwan/internal/ifmgr/modules/steering`, exit 0.

- [ ] **Step 9: Put the module in the wan role**

In `mwan/go/internal/ifmgr/roles.go`, replace the wan role at `:75-84` with:

```go
	// wan owns the MWAN VM policy-routing inventory. It runs as a
	// separate instance from any OOB role.
	"wan": {
		// health writes the state consumed by later WAN-role modules.
		"health",
		"wan.routes",
		// steering assigns each new connection to a provider of the active
		// tier. It runs after wan.routes so the policy rules its marks select
		// are installed before any mark is set. Self-disables when the network
		// configuration lists no providers.
		"steering",
		// npt programs the ip6 nat NPT chains from the live DHCPv6-PD.
		// Self-disables when the network configuration lists no providers.
		"npt",
	},
```

- [ ] **Step 10: Register the module in the daemon**

In `mwan/go/cmd/mwan/ifmgr_linux.go`, add one line to the blank-import block at
`:25-40`, between `slaachealth` and `wanroutes`:

```go
	_ "goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	_ "goodkind.io/mwan/internal/ifmgr/modules/steering"
	_ "goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
```

- [ ] **Step 11: Build the module config**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go`, add the import at `:25-26`,
between `slaachealth` and `wanroutes`:

```go
	slaachealth "goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	steering "goodkind.io/mwan/internal/ifmgr/modules/steering"
	wanroutes "goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
```

Replace `addWANRoleConfigs` at `:136-166` with:

```go
// addWANRoleConfigs builds the wan-role module configs from the one shared
// network configuration, so health, wan.routes, steering, and npt read the same
// provider list.
func addWANRoleConfigs(
	moduleConfigs ifmgr.ModuleConfigSet,
	want map[string]bool,
	ifmgrCfg config.IfMgrSection,
) error {
	shared := buildWANRefs(ifmgrCfg)
	var routesSection *config.IfMgrWANRoutesSection
	if ifmgrCfg.Modules.WAN != nil {
		routesSection = ifmgrCfg.Modules.WAN.Routes
	}
	if want["health"] {
		healthConfig, err := buildHealthConfig(shared, ifmgrCfg.Modules.Health)
		if err != nil {
			return err
		}
		moduleConfigs["health"] = healthConfig
	}
	if want["wan.routes"] {
		wanRoutesConfig, err := buildWANRoutesConfig(shared, routesSection)
		if err != nil {
			return err
		}
		moduleConfigs["wan.routes"] = wanRoutesConfig
	}
	if want["steering"] {
		steeringConfig, err := buildSteeringConfig(shared, ifmgrCfg, routesSection)
		if err != nil {
			return err
		}
		moduleConfigs["steering"] = steeringConfig
	}
	if want["npt"] {
		moduleConfigs["npt"] = buildNPTConfig(shared)
	}
	return nil
}
```

Add `buildSteeringConfig` directly after `buildWANRoutesConfig`:

```go
// buildSteeringConfig projects the shared provider list and the group-wide
// steering settings onto the steering module config. The internal link and
// network come from the same wan.routes section the routing module reads, so
// the rules that set a mark and the rules that act on it name one link.
func buildSteeringConfig(
	shared sharedWANInputs,
	ifmgrCfg config.IfMgrSection,
	section *config.IfMgrWANRoutesSection,
) (steering.Config, error) {
	cfg := steering.Config{
		InternalIface:   "",
		InternalNetV4:   "",
		InternalPrefix:  shared.InternalPrefix,
		OpnsenseEdgeV6:  shared.OpnsenseEdgeV6,
		HashMode:        ifmgrCfg.HashMode,
		HealthStateFile: "",
		Members:         nil,
	}
	if section == nil {
		return cfg, nil
	}
	cfg.InternalIface = section.InternalIface
	cfg.InternalNetV4 = section.InternalNetV4
	cfg.HealthStateFile = section.HealthStateFile
	cfg.Members = make([]steering.Member, 0, len(shared.WANs))
	for _, wan := range shared.WANs {
		mark, err := wanFwMark(wan)
		if err != nil {
			return steering.Config{}, err
		}
		cfg.Members = append(cfg.Members, steering.Member{
			WANRef: wan.WANRef,
			Mark:   mark,
			Tier:   wan.Tier,
			Weight: wan.Weight,
		})
	}
	return cfg, nil
}
```

- [ ] **Step 12: Test the builder**

Append to `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go`:

```go
// TestBuildSteeringConfig pins that the balancer reads the same provider list,
// internal link, and internal network the routing module reads, plus the group
// hash mode the network configuration carries.
func TestBuildSteeringConfig(t *testing.T) {
	t.Parallel()

	ifmgrCfg := sharedWANForTest()
	ifmgrCfg.HashMode = "source"
	shared := buildWANRefs(ifmgrCfg)
	cfg, err := buildSteeringConfig(shared, ifmgrCfg, &config.IfMgrWANRoutesSection{
		InternalIface:   "vmbr250",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/var/run/mwan-health.state",
	})
	if err != nil {
		t.Fatalf("buildSteeringConfig returned error: %v", err)
	}

	want := steering.Config{
		InternalIface:   "vmbr250",
		InternalNetV4:   "10.250.250.0/29",
		InternalPrefix:  "3d06:bad:b01::/60",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		HashMode:        "source",
		HealthStateFile: "/var/run/mwan-health.state",
		Members: []steering.Member{
			{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}, Mark: 1, Tier: 0, Weight: 1},
			{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"}, Mark: 2, Tier: 1, Weight: 3},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildSteeringConfig mismatch\ngot:  %#v\nwant: %#v", cfg, want)
	}
}

// TestBuildIfMgrModuleConfigsWANRoleBuildsSteering pins that the wan role builds
// the steering config, so a gateway that runs the role programs the balancer.
func TestBuildIfMgrModuleConfigsWANRoleBuildsSteering(t *testing.T) {
	t.Parallel()

	ifmgrCfg := ifmgrForTest(modulesWithUnresolvableUIDRule())
	ifmgrCfg.HashMode = "random"
	set, err := buildIfMgrModuleConfigs(ifmgrCfg, "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan) returned error: %v", err)
	}
	steeringCfg, isSteering := set["steering"].(steering.Config)
	if !isSteering {
		t.Fatalf("wan role built %T for steering, want steering.Config", set["steering"])
	}
	if steeringCfg.HashMode != "random" {
		t.Fatalf("steering hash mode = %q, want random", steeringCfg.HashMode)
	}
	if len(steeringCfg.Members) != 2 {
		t.Fatalf("steering member count = %d, want 2", len(steeringCfg.Members))
	}
}
```

Add `steering "goodkind.io/mwan/internal/ifmgr/modules/steering"` to the test
file's import block at `:18-19`, beside the existing `npt` and `wanroutes`
imports.

- [ ] **Step 13: Run the whole suite and the repository gates**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./...
cd "$(git rev-parse --show-toplevel)" && make check
```

Expected: both PASS, exit 0.

- [ ] **Step 14: Build the gateway binary**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make build-wanconfig
```

Expected: PASS, exit 0, and `mwan/go/bin/mwan-wanconfig` written with a fresh
timestamp. This is the lane that proves the linux cgo build links, which the host
`go test` on darwin does not.

- [ ] **Step 15: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/go/internal/ifmgr/modules/steering mwan/go/internal/ifmgr/roles.go \
  mwan/go/cmd/mwan/ifmgr_linux.go mwan/go/cmd/mwan/ifmgr_module_configs_linux.go \
  mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go
git commit -S -m "Add the steering module that owns the load balancer" -m "The daemon computes the mark assignment across the active tier's healthy providers, weighted by their configured weights, and programs it into the inet mwan_steer table's prerouting chain at priority -149, one step after the ruleset file's mangle chain so the mark-zero guard sees the control-plane pins. Random, source, and source-destination hash modes are supported; the table and chain are created idempotently on every pass and repaired through an nft monitor when a ruleset flush removes them. The module registers in the wan role between wan.routes and npt." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

---

### Task 5: inventory takes the model's shape

Each gateway group collapses its per-provider variables into one list, and
every reader of a removed variable is rewritten to read the list. The firewall
ruleset is the one reader this task leaves alone, because Task 6 rewrites it
and deletes its variable together.

**What the numbers do not do.** Nothing derives. Each provider's table, mark,
mark rule priority, and source rule priority are typed in its entry exactly as
they are typed today, and the values do not change. The list is a shape change,
not a renumber.

**Where the reserved tables live.** Decision 3 says they are typed once. Three
templates that hardcode table 500 render outside the gateway groups:
`mwan/config/_ifmgr_common.toml.j2:18` and `:22` are included by both
`config-vm.toml.j2:132` and `config-host.toml.j2:82`, and
`config-host.toml.j2:113`, `:122`, and `:131` carry their own copies.
`config-host.toml.j2` is rendered by
`ansible/playbooks/tasks/proxmox-host.yml:68-70`, imported by
`ansible/playbooks/deploy-proxmox.yml:30` against `hosts: proxmox_servers`. So
the hypervisor groups and the gateway groups both read the value, and one
definition serving both means `ansible/inventory/group_vars/all/vars.yml`. Both
gateway groups inherit it there, which is what the contract's group-key list
asks for in effect.

**Files:**
- Modify: `ansible/inventory/group_vars/all/vars.yml:68-76` (insert after the
  ifmgr block)
- Modify: `ansible/inventory/group_vars/mwan_servers.yml:167-174`, `:211-257`,
  `:259-264`, `:265`, `:299-300`, `:326-327`, `:330-331`, `:344`, `:364-374`,
  `:381-392`
- Modify: `ansible/inventory/group_vars/mwan_suburban_servers.yml:167-174`,
  `:179-188`, `:195-206`, `:229-261`, `:263-267`
- Modify: `mwan/config/network.json.j2:1-92` (whole file)
- Modify: `mwan/config/rt_tables.j2:13-17`
- Modify: `mwan/config/mwan.env.j2:30-35`, `:42-46`, `:55-60`
- Modify: `mwan/config/_ifmgr_common.toml.j2:18` and `:22`
- Modify: `mwan/config/config-host.toml.j2:113`, `:122`, `:131`
- Modify: `mwan/config/config-vm.toml.j2:177`
- Modify: `mwan/config/nftables.conf.j2:171`, `:173`, `:184`, `:186` (the
  pinned-list rename only; Task 6 rewrites the file)
- Modify: `mwan/networkd/20-webpass.network.j2:1-45`
- Modify: `mwan/networkd/testbed/20-webpass.network.j2:1-40`
- Modify: `mwan/networkd/21-att-vlan.network.j2:37-47`
- Modify: `ansible/playbooks/tasks/mwan-vm/discover-runtime-network.yml:65-73`
- Modify: `docs/ops/infra/network.md:298-299`

**Interfaces:**
- Consumes: the model revision from Task 1.
- Produces the group key `mwan_providers`, a list whose entries carry exactly
  these keys: `name`, `iface`, `vlan_id`, `table`, `mark`, `mark_prio`,
  `from_prio`, `tier`, `weight`, `npt_prefix`, `v4_source`, `static_mappings`,
  and `health` with `enabled`, `ping_count`, `success_threshold`,
  `failure_threshold`, `recovery_threshold`, `check_interval`, `targets_v4`,
  `targets_v6`, `http_targets`.
- Produces the group keys `mwan_hash_mode` and `mwan_pin_provider`, and the
  repository-wide key `mwan_reserved_tables`.
- Produces the renamed keys `mwan_pinned_v4_seed_cidrs`,
  `mwan_pinned_v4_fqdns`, `mwan_pinned_v6_seed_cidrs`, and
  `mwan_pinned_v6_fqdns`.
- Produces, in the rendered `network.json`, the per-provider leaf pair
  `goodkind-mwan-steering:steering/tier` and `.../weight`, and the group leaves
  `steering-group/hash-mode` and `steering-group/reserved-tables`, which the Go
  loader task reads.
- Removes: `mwan_npt_att_prefix`, `mwan_npt_webpass_prefix`,
  `mwan_npt_monkeybrains_prefix`, `mwan_rt_tables`, `mwan_ifmgr_wan_fw_marks`,
  `mwan_ifmgr_wan_fw_mark_prios`, `mwan_ifmgr_wan_from_prios`, and
  `mwan_health_checks`. `mwan_static_mappings` survives this task and dies in
  Task 6.
- Unchanged and still read: `mwan_att_iface`, `mwan_att_vlan_id`,
  `mwan_webpass_iface`, `mwan_monkeybrains_iface`, `mwan_webpass_ipv4_addr`,
  `mwan_static_internal_hosts`, the DUID and MAC variables, the delegated
  prefix variables, `mwan_ifmgr_wan_enabled`, and
  `mwan_ifmgr_wan_health_state_file`.

- [ ] **Step 1: Type the reserved tables once**

In `ansible/inventory/group_vars/all/vars.yml`, directly after the
`mwan_ifmgr_wan_enabled: false` line and its comment block, insert:

```yaml

# Routing tables the gateway holds for something other than a provider. The
# tunnel table carries cloudflared; the out-of-band table carries management
# traffic on the host that holds the OOB tunnel. Both gateway groups render
# these into network.json, where the daemon reads the set it checks every
# provider table against, and the hypervisor groups render the out-of-band id
# into their policy rules. One definition, because both scopes read the same
# numbers and a second copy is the one that drifts. The kernel's own tables
# are reserved in the daemon and are not values an operator sets.
mwan_reserved_tables:
  cloudflared: 400
  oob: 500
```

- [ ] **Step 2: Give the production group its provider list**

In `ansible/inventory/group_vars/mwan_servers.yml`, replace lines 364 to 374,
which are the whole "Routing table IDs" section from its banner comment through
the blank line after `cloudflared: 400`:

```yaml
# ----------------------------------------------------------------------------
# Routing table IDs
# ----------------------------------------------------------------------------

# Used in /etc/iproute2/rt_tables and systemd-networkd configs
mwan_rt_tables:
  att: 100
  webpass: 200
  monkeybrains: 300
  cloudflared: 400

```

with:

```yaml
# ----------------------------------------------------------------------------
# Providers
# ----------------------------------------------------------------------------

# One entry per provider the gateway steers. Every value the daemon reads about
# a provider is here, so adding one is one entry and a deploy. The routing
# numbers are typed, not derived: table, mark, mark_prio, and from_prio are the
# numbers this gateway has always used, and the daemon checks them for
# uniqueness and against mwan_reserved_tables rather than computing them. tier
# decides fallback, lowest first, and weight splits a shared tier. Hardware
# values stay in their own variables above, because the link files read them;
# an entry references them rather than repeating a value.
mwan_providers:
  - name: att
    iface: "{{ mwan_att_iface }}"
    vlan_id: "{{ mwan_att_vlan_id }}"
    table: 100
    mark: 1
    mark_prio: 100
    from_prio: 55
    tier: 0
    weight: 1
    npt_prefix: "2600:1700:2f71:c80::/60"
    v4_source: ""
    static_mappings:
      - { internal: "10.250.250.2", external: "104.57.226.193" }
      - { internal: "10.250.250.3", external: "104.57.226.194" }
      - { internal: "10.250.250.4", external: "104.57.226.195" }
      - { internal: "10.250.250.5", external: "104.57.226.196" }
      - { internal: "10.250.250.6", external: "104.57.226.197" }
    health:
      enabled: true
      ping_count: 3
      success_threshold: 2
      failure_threshold: 2
      recovery_threshold: 2
      check_interval: 10
      targets_v4:
        - "1.1.1.1" # Cloudflare
        - "8.8.8.8" # Google
      targets_v6:
        - "2606:4700:4700::1111" # Cloudflare
        - "2001:4860:4860::8888" # Google
      http_targets:
        - "https://ifconfig.co/ip"

  - name: webpass
    iface: "{{ mwan_webpass_iface }}"
    vlan_id: ""
    table: 200
    mark: 2
    mark_prio: 200
    from_prio: 56
    tier: 0
    weight: 1
    npt_prefix: "2604:5500:c271:be00::/60"
    v4_source: "{{ mwan_webpass_ipv4_addr }}"
    static_mappings:
      - { internal: "10.250.250.2", external: "136.25.91.242" }
      - { internal: "10.250.250.3", external: "136.25.91.243" }
      - { internal: "10.250.250.4", external: "136.25.91.244" }
      - { internal: "10.250.250.5", external: "136.25.91.245" }
      - { internal: "10.250.250.6", external: "136.25.91.246" }
    health:
      enabled: true
      ping_count: 3
      success_threshold: 2
      failure_threshold: 2
      recovery_threshold: 2
      check_interval: 10
      targets_v4:
        - "1.1.1.1"
        - "8.8.8.8"
      targets_v6:
        - "2606:4700:4700::1111"
        - "2001:4860:4860::8888"
      http_targets:
        - "https://ifconfig.co/ip"

  # Tertiary, all-else-failed fallback WAN. Alone in tier 1, so it carries
  # everything when both tier 0 providers are unhealthy and nothing otherwise.
  - name: monkeybrains
    iface: "{{ mwan_monkeybrains_iface }}"
    vlan_id: ""
    table: 300
    mark: 3
    mark_prio: 300
    from_prio: 57
    tier: 1
    weight: 1
    npt_prefix: "2607:f598:d3e8:4500::/60"
    v4_source: ""
    static_mappings: []
    health:
      enabled: true
      ping_count: 5
      success_threshold: 1 # Lax, because the link is lossy.
      failure_threshold: 5
      recovery_threshold: 3
      check_interval: 30
      targets_v4:
        - "1.1.1.1"
        - "8.8.8.8"
      targets_v6:
        - "2606:4700:4700::1111"
        - "2001:4860:4860::8888"
      http_targets:
        - "https://ifconfig.co/ip"

# How a new connection is assigned among the active tier's healthy providers.
# random, source, or source-destination.
mwan_hash_mode: random

# The provider the pinned-destination sets and the router's WireGuard control
# plane are pinned to.
mwan_pin_provider: att

```

- [ ] **Step 3: Delete the production group's superseded variables**

In `ansible/inventory/group_vars/mwan_servers.yml`, make these three deletions.

Delete lines 381 to 392, the three routing-number maps, so the "Interface
manager WAN route module" section keeps only `mwan_ifmgr_wan_enabled` and
`mwan_ifmgr_wan_health_state_file`:

```yaml
mwan_ifmgr_wan_fw_marks:
  att: 1
  webpass: 2
  monkeybrains: 3
mwan_ifmgr_wan_fw_mark_prios:
  att: 100
  webpass: 200
  monkeybrains: 300
mwan_ifmgr_wan_from_prios:
  att: 55
  webpass: 56
  monkeybrains: 57
```

Delete lines 211 to 257, the blank line and the whole `mwan_health_checks`
mapping, so the "Health check configuration" section keeps only
`mwan_health_probe_timeout_ms`. The mapping runs from `mwan_health_checks:` on
line 212 through the `https://ifconfig.co/ip` element on line 257.

Delete lines 167 to 174, which are the four-line comment about NPT targets, the
three `mwan_npt_*_prefix` assignments, and the blank line after them. The
"IPv6 prefixes" banner then runs straight into the delegated-prefix comment on
what is currently line 175.

- [ ] **Step 4: Rename the production group's pinned-destination lists**

The lists lose the provider name, because `mwan_pin_provider` now names the
target. The kernel set names and the refresher unit names do not change.

Change line 265 from `mwan_att_pinned_v4_seed_cidrs:` to
`mwan_pinned_v4_seed_cidrs:`, keeping every element under it.

Change line 300 from `mwan_att_pinned_v4_fqdns:` to `mwan_pinned_v4_fqdns:`.

Change line 331 from `mwan_att_pinned_v6_seed_cidrs:` to
`mwan_pinned_v6_seed_cidrs:`.

Change line 344 from `mwan_att_pinned_v6_fqdns:` to `mwan_pinned_v6_fqdns:`.

Replace the section banner at lines 259 to 264:

```yaml
# ----------------------------------------------------------------------------
# Latency-sensitive services pinned to AT&T
# ----------------------------------------------------------------------------

# Seed CIDRs for the nftables set used to pin IPv4 traffic to AT&T.
# This should be kept small and stable; dynamic sources (Zoom, Verizon DNS) are handled by a timer.
```

with:

```yaml
# ----------------------------------------------------------------------------
# Latency-sensitive services pinned to one provider
# ----------------------------------------------------------------------------

# Seed CIDRs for the nftables set that pins IPv4 traffic to the provider
# mwan_pin_provider names. Keep it small and stable; dynamic sources (Zoom,
# Verizon DNS) are handled by a timer.
```

Replace the comment on line 299:

```yaml
# Verizon Wi-Fi Calling: resolve these FQDNs periodically and pin their A records to AT&T.
```

with:

```yaml
# Verizon Wi-Fi Calling: resolve these FQDNs periodically and pin their A
# records to the pinned provider.
```

Replace the comment on lines 326 to 327:

```yaml
# Zoom: pull published IPv4 prefixes periodically and pin to AT&T.
# This is a plain-text list of prefixes (one per line).
```

with:

```yaml
# Zoom: pull published IPv4 prefixes periodically and pin them to the pinned
# provider. This is a plain-text list of prefixes (one per line).
```

Replace the comment on line 330:

```yaml
# AT&T ePDG infrastructure - IPv6 seed CIDRs
```

with:

```yaml
# IPv6 seed CIDRs for AT&T ePDG infrastructure. AT&T is the destination
# network here, not the egress provider.
```

Leave the comment on line 343 as it is, because it names AT&T as a destination
network rather than as an egress provider.

- [ ] **Step 5: Give the testbed group its provider list**

In `ansible/inventory/group_vars/mwan_suburban_servers.yml`, replace lines 179
to 188, the whole "Routing tables" section through the blank line after
`cloudflared: 400`:

```yaml
# ----------------------------------------------------------------------------
# Routing tables
# ----------------------------------------------------------------------------

mwan_rt_tables:
  att: 100
  webpass: 200
  monkeybrains: 300
  cloudflared: 400

```

with:

```yaml
# ----------------------------------------------------------------------------
# Providers
# ----------------------------------------------------------------------------

# Mirrors mwan_servers.yml entry for entry, with the simulated ISPs' addresses.
# The routing numbers match production on purpose, so a testbed proof exercises
# the numbers the production gateway runs.
mwan_providers:
  - name: att
    iface: "{{ mwan_att_iface }}"
    vlan_id: "{{ mwan_att_vlan_id }}"
    table: 100
    mark: 1
    mark_prio: 100
    from_prio: 55
    tier: 0
    weight: 1
    npt_prefix: "3d06:bad:b01:2300::/60"
    v4_source: ""
    static_mappings:
      - { internal: "{{ mwan_static_internal_hosts[0] }}", external: "10.241.205.2" }
      - { internal: "{{ mwan_static_internal_hosts[1] }}", external: "10.241.205.3" }
      - { internal: "{{ mwan_static_internal_hosts[2] }}", external: "10.241.205.4" }
      - { internal: "{{ mwan_static_internal_hosts[3] }}", external: "10.241.205.5" }
      - { internal: "{{ mwan_static_internal_hosts[4] }}", external: "10.241.205.6" }
    health:
      enabled: true
      ping_count: 3
      success_threshold: 2
      failure_threshold: 2
      recovery_threshold: 2
      check_interval: 10
      targets_v4: ["1.1.1.1", "8.8.8.8"]
      targets_v6: ["2606:4700:4700::1111", "2001:4860:4860::8888"]
      http_targets: ["https://ifconfig.co/ip"]

  - name: webpass
    iface: "{{ mwan_webpass_iface }}"
    vlan_id: ""
    table: 200
    mark: 2
    mark_prio: 200
    from_prio: 56
    tier: 0
    weight: 1
    npt_prefix: "3d06:bad:b01:2200::/60"
    v4_source: "{{ mwan_webpass_ipv4_addr }}"
    static_mappings:
      - { internal: "{{ mwan_static_internal_hosts[0] }}", external: "10.241.204.2" }
      - { internal: "{{ mwan_static_internal_hosts[1] }}", external: "10.241.204.3" }
      - { internal: "{{ mwan_static_internal_hosts[2] }}", external: "10.241.204.4" }
      - { internal: "{{ mwan_static_internal_hosts[3] }}", external: "10.241.204.5" }
      - { internal: "{{ mwan_static_internal_hosts[4] }}", external: "10.241.204.6" }
    health:
      enabled: true
      ping_count: 3
      success_threshold: 2
      failure_threshold: 2
      recovery_threshold: 2
      check_interval: 10
      targets_v4: ["1.1.1.1", "8.8.8.8"]
      targets_v6: ["2606:4700:4700::1111", "2001:4860:4860::8888"]
      http_targets: ["https://ifconfig.co/ip"]

  - name: monkeybrains
    iface: "{{ mwan_monkeybrains_iface }}"
    vlan_id: ""
    table: 300
    mark: 3
    mark_prio: 300
    from_prio: 57
    tier: 1
    weight: 1
    npt_prefix: "3d06:bad:b01:2400::/60"
    v4_source: ""
    static_mappings: []
    health:
      enabled: true
      ping_count: 5
      success_threshold: 1
      failure_threshold: 5
      recovery_threshold: 3
      check_interval: 30
      targets_v4: ["1.1.1.1", "8.8.8.8"]
      targets_v6: ["2606:4700:4700::1111", "2001:4860:4860::8888"]
      http_targets: ["https://ifconfig.co/ip"]

# How a new connection is assigned among the active tier's healthy providers.
mwan_hash_mode: random

# The provider the pinned-destination sets and the router's WireGuard control
# plane are pinned to. The testbed's pinned lists are empty, so this decides
# only the WireGuard pin there.
mwan_pin_provider: att

```

- [ ] **Step 6: Delete the testbed group's superseded variables**

In `ansible/inventory/group_vars/mwan_suburban_servers.yml`, make these three
deletions.

Delete lines 195 to 206, the three routing-number maps under the "Interface
manager WAN route module" banner, leaving `mwan_ifmgr_wan_enabled` and
`mwan_ifmgr_wan_health_state_file`.

Delete lines 229 to 261: the blank line, the `# Health checks (same structure
as production)` comment, and the whole `mwan_health_checks` mapping.

Replace lines 167 to 174, which are the four-line comment about NPT targets,
the three `mwan_npt_*_prefix` assignments, and the blank line after them, with
this comment alone, so it sits directly above `mwan_att_pd_prefix`:

```yaml
# PD sizes match prod: webpass /56, att /60, monkeybrains /56. Sim delegation
# prefixes (the 22/23/24 /56-clean scheme) live in suburban_servers.yml
# testbed_isp_lxcs. Each provider's translation target is the first /60 of its
# delegation and is typed in that provider's entry below.
```

- [ ] **Step 7: Rename the testbed group's pinned-destination lists**

Replace lines 263 to 267:

```yaml
# AT&T pinning lists not used in testbed
mwan_att_pinned_v4_seed_cidrs: []
mwan_att_pinned_v4_fqdns: []
mwan_att_pinned_v6_seed_cidrs: []
mwan_att_pinned_v6_fqdns: []
```

with:

```yaml
# Destination pinning is not exercised on the testbed, so every list is empty
# and the nft sets render with no elements.
mwan_pinned_v4_seed_cidrs: []
mwan_pinned_v4_fqdns: []
mwan_pinned_v6_seed_cidrs: []
mwan_pinned_v6_fqdns: []
```

- [ ] **Step 8: Rewrite the network configuration template**

Replace the whole of `mwan/config/network.json.j2` with:

```jinja
{#
  The gateway's network tree in the model's own JSON encoding, deployed to
  /etc/mwan/network.json and loaded by mwan-ifmgr@wan. It renders from
  mwan_providers, the one list each gateway group carries, so a provider is one
  entry there and nothing else. Every value crosses unchanged: the model's time
  spans are integers that name their unit, and the inventory holds integers.

  Each provider hangs off the interface that carries it. iana-if-type:other is
  the type the served tree already publishes for these links, so the mandatory
  ietf-interfaces type leaf carries no invented value.

  Block tags sit at column zero on purpose. Ansible renders with trim_blocks on
  and lstrip_blocks off, so an indented tag would emit its leading spaces into
  the document.
#}
{% set interfaces = [] %}
{% for provider in mwan_providers %}
{% if provider.vlan_id %}
{% set iface_name = provider.iface ~ "." ~ provider.vlan_id %}
{% else %}
{% set iface_name = provider.iface %}
{% endif %}
{% set wan = {
  "name": provider.name,
  "table-id": provider.table,
  "fw-mark": provider.mark,
  "fw-mark-prio": provider.mark_prio,
  "from-prio": provider.from_prio,
  "npt-prefix": provider.npt_prefix,
  "health": {
    "enabled": provider.health.enabled,
    "ping-count": provider.health.ping_count,
    "success-threshold": provider.health.success_threshold,
    "failure-threshold": provider.health.failure_threshold,
    "recovery-threshold": provider.health.recovery_threshold,
    "check-interval": provider.health.check_interval,
    "targets-v4": provider.health.targets_v4,
    "targets-v6": provider.health.targets_v6,
    "http-urls": provider.health.http_targets
  }
} %}
{% if provider.v4_source %}
{% set wan = wan | combine({"v4-source": provider.v4_source}) %}
{% endif %}
{% set _ = interfaces.append({
  "name": iface_name,
  "type": "iana-if-type:other",
  "goodkind-mwan-steering:wan": wan,
  "goodkind-mwan-steering:steering": {
    "tier": provider.tier,
    "weight": provider.weight
  }
}) %}
{% endfor %}
{% set _ = interfaces.append({"name": mwan_internal_iface, "type": "iana-if-type:other"}) %}
{{ {
  "ietf-interfaces:interfaces": {
    "interface": interfaces,
    "goodkind-mwan-steering:steering-group": {
      "hash-mode": mwan_hash_mode,
      "reserved-tables": mwan_reserved_tables.values() | sort,
      "translation": {
        "internal-prefix": mwan_internal_prefix,
        "opnsense-edge-v6": mwan_opnsense_edge_ipv6,
        "mwanbr-edge-v6": mwan_mwanbr_edge_ipv6
      },
      "routes": {
        "internal-iface": mwan_internal_iface,
        "internal-net-v4": mwan_internal_net_v4
      },
      "health": {"probe-timeout": mwan_health_probe_timeout_ms}
    }
  }
} | to_nice_json }}
```

Two shapes changed and both are deliberate. The health container is now
unconditional, because every provider entry carries a health block; all three
current providers carry one today, so the rendered document neither loses nor
gains a health container. The interface name is built with a block-if rather
than a ternary, because a self-ternary is what the template lint rejects.

- [ ] **Step 9: Rewrite the routing-table names file**

Replace lines 13 to 17 of `mwan/config/rt_tables.j2`:

```jinja
# Multi-WAN routing tables
{{ mwan_rt_tables.att }}	att
{{ mwan_rt_tables.webpass }}	webpass
{{ mwan_rt_tables.monkeybrains }}	monkeybrains
{{ mwan_rt_tables.cloudflared }}	cloudflared
```

with:

```jinja
# One table per provider, named for the provider.
{% for provider in mwan_providers %}
{{ provider.table }}	{{ provider.name }}
{% endfor %}

# Tables held for something other than a provider. The daemon refuses a
# provider whose table is one of these.
{% for name, table in mwan_reserved_tables | dictsort(by='value') %}
{{ table }}	{{ name }}
{% endfor %}
```

The character between the id and the name is a literal tab in both loops, the
same separator the current file uses.

This renders one more line than today: `500	oob` joins the file. That is a
mapped file difference for the cutover capture, and it is inert, because
`/etc/iproute2/rt_tables` only names table ids and the gateway installs nothing
in table 500.

- [ ] **Step 10: Drop the dead routing and translation keys from the env file**

`MWAN_RT_ATT`, `MWAN_RT_WEBPASS`, `MWAN_RT_MONKEYBRAINS`,
`MWAN_RT_CLOUDFLARED`, `MWAN_NPT_ATT_PREFIX`, `MWAN_NPT_WEBPASS_PREFIX`, and
`MWAN_NPT_MONKEYBRAINS_PREFIX` have no reader. The five consumers that source
or set `EnvironmentFile=/etc/mwan/mwan.env` are
`mwan/services/wpa-cli-action.service:8`,
`mwan/services/wpa_supplicant.service:9`,
`mwan/scripts/bringup-att-vlan.sh:23`, `mwan/scripts/wpa-wait-att-iface.sh:18`,
and `mwan/scripts/update-att-pinned-dests.sh:16`, and between them they read
`MWAN_ATT_IFACE`, `MWAN_ATT_VLAN_IFACE`, the trace and debug keys, and the
pinned-destination keys. `mwan/go/internal/agent/server.go:336` hashes the
file's bytes without parsing keys. So the rewrite for these seven keys is
deletion.

In `mwan/config/mwan.env.j2`, delete lines 30 to 35:

```jinja
# Routing table IDs
MWAN_RT_ATT={{ mwan_rt_tables.att }}
MWAN_RT_WEBPASS={{ mwan_rt_tables.webpass }}
MWAN_RT_MONKEYBRAINS={{ mwan_rt_tables.monkeybrains }}
MWAN_RT_CLOUDFLARED={{ mwan_rt_tables.cloudflared }}

```

and delete lines 42 to 46:

```jinja
# NPT targets (per-WAN /60s)
MWAN_NPT_ATT_PREFIX={{ mwan_npt_att_prefix }}
MWAN_NPT_WEBPASS_PREFIX={{ mwan_npt_webpass_prefix }}
MWAN_NPT_MONKEYBRAINS_PREFIX={{ mwan_npt_monkeybrains_prefix }}

```

- [ ] **Step 11: Repoint the env file's pinned-destination keys**

The four env keys keep their names, because
`mwan/scripts/update-att-pinned-dests.sh` reads them at lines 107, 119, 155,
and 167 and the contract keeps the refresher script unchanged. Only the Jinja
source changes.

In `mwan/config/mwan.env.j2`, replace lines 55 to 60:

```jinja
# AT&T pinned destinations (nft set seed + FQDN expansion)
MWAN_ATT_PINNED_V4_SEED_CIDRS="{{ mwan_att_pinned_v4_seed_cidrs | join(' ') }}"
MWAN_ATT_PINNED_V4_FQDNS="{{ mwan_att_pinned_v4_fqdns | join(' ') }}"
MWAN_ZOOM_IPRANGES_URL="{{ mwan_zoom_ipranges_url }}"
MWAN_ATT_PINNED_V6_SEED_CIDRS="{{ mwan_att_pinned_v6_seed_cidrs | join(' ') }}"
MWAN_ATT_PINNED_V6_FQDNS="{{ mwan_att_pinned_v6_fqdns | join(' ') }}"
```

with:

```jinja
# Pinned destinations (nft set seed + FQDN expansion). The key names carry the
# provider's name because the refresher script and the kernel sets do; the
# provider they pin to is mwan_pin_provider.
MWAN_ATT_PINNED_V4_SEED_CIDRS="{{ mwan_pinned_v4_seed_cidrs | join(' ') }}"
MWAN_ATT_PINNED_V4_FQDNS="{{ mwan_pinned_v4_fqdns | join(' ') }}"
MWAN_ZOOM_IPRANGES_URL="{{ mwan_zoom_ipranges_url }}"
MWAN_ATT_PINNED_V6_SEED_CIDRS="{{ mwan_pinned_v6_seed_cidrs | join(' ') }}"
MWAN_ATT_PINNED_V6_FQDNS="{{ mwan_pinned_v6_fqdns | join(' ') }}"
```

- [ ] **Step 12: Repoint the pinned-set seeds in the firewall ruleset**

Task 6 rewrites this file, but the rename lands with its readers, so make the
four mechanical changes now.

In `mwan/config/nftables.conf.j2`, change line 171 from
`{% if mwan_att_pinned_v4_seed_cidrs %}` to
`{% if mwan_pinned_v4_seed_cidrs %}`, line 173 from
`{% for cidr in mwan_att_pinned_v4_seed_cidrs %}` to
`{% for cidr in mwan_pinned_v4_seed_cidrs %}`, line 184 from
`{% if mwan_att_pinned_v6_seed_cidrs %}` to
`{% if mwan_pinned_v6_seed_cidrs %}`, and line 186 from
`{% for cidr in mwan_att_pinned_v6_seed_cidrs %}` to
`{% for cidr in mwan_pinned_v6_seed_cidrs %}`.

- [ ] **Step 13: Read the out-of-band table id from the registry**

Four templates carry the literal 500. The rendered value does not change, so
this is a source change with no behavioral one.

In `mwan/config/_ifmgr_common.toml.j2`, change line 18 and line 22 from
`oob_table_id = 500` to `oob_table_id = {{ mwan_reserved_tables.oob }}`.

In `mwan/config/config-host.toml.j2`, change lines 113, 122, and 131 from
`table_id = 500` to `table_id = {{ mwan_reserved_tables.oob }}`.

In `mwan/config/config-vm.toml.j2`, change line 177 from `table_id = 500` to
`table_id = {{ mwan_reserved_tables.oob }}`.

- [ ] **Step 14: Look the Webpass table up in the provider list**

In `mwan/networkd/20-webpass.network.j2`, insert this line directly below the
comment on line 4 and above the blank line before `[Match]`:

```jinja
{% set webpass = mwan_providers | selectattr('name', 'equalto', 'webpass') | first %}
```

Then change line 34 and line 43 from `Table={{ mwan_rt_tables.webpass }}` to
`Table={{ webpass.table }}`.

In `mwan/networkd/testbed/20-webpass.network.j2`, insert the same `{% set %}`
line directly below the comment on line 4, and change line 30 and line 38 from
`Table={{ mwan_rt_tables.webpass }}` to `Table={{ webpass.table }}`.

`selectattr` with `equalto` raises when no entry matches, which is the loud
failure a missing Webpass entry deserves, and it is not one of the presence
forms the lint rejects.

- [ ] **Step 15: Correct the AT&T VLAN routing comment**

The comment at `mwan/networkd/21-att-vlan.network.j2:46` reads a removed
variable and names a dispatcher script that no longer exists.
`ansible/playbooks/deploy-mwan.yml:486-489` records that the ifmgr modules own
reconvergence and that no networkd-dispatcher hooks remain.

Replace lines 37 to 47:

```jinja
# Note on Routing Policy:
# Unlike the Webpass interface (which has static IPs), this AT&T interface
# obtains its IP and Gateway dynamically via DHCP/RA.
#
# We CANNOT define static [Route] or [RoutingPolicyRule] sections here because
# we don't know the Gateway or Source IP at template time.
#
# Instead, we rely on the networkd-dispatcher script (50-update-routes.sh) to:
# 1. Detect the dynamic Gateway/IP after the link comes up.
# 2. Add the default route to table {{ mwan_rt_tables.att }}.
# 3. Add the ip rules (fwmark/from) for policy routing.
```

with:

```jinja
# Note on Routing Policy:
# Unlike the Webpass interface (which has static IPs), this AT&T interface
# obtains its IP and Gateway dynamically via DHCP/RA, so no static [Route] or
# [RoutingPolicyRule] section can be written here.
#
# The ifmgr wan.routes module owns them instead. It watches this link over
# netlink and, on each default-route change, installs the default route in the
# AT&T provider's routing table and the firewall-mark and source policy rules
# at the priorities that provider's entry carries.
```

- [ ] **Step 16: Delete the NIC check**

`ansible/playbooks/tasks/mwan-vm/discover-runtime-network.yml:65-73` asserts
that net2 exists when `mwan_health_checks.monkeybrains.enabled`. The variable
and the provider name both leave the repository in this task, and the operator
ruled that the deploy is not the place to check the VM's hardware, so the
task is deleted rather than generalized. A provider whose NIC is missing
surfaces where the daemon already reports it: the interface never appears,
the routing module never finds a gateway for it, and the health module
reports it unhealthy.

Delete lines 65 to 73 in full:

```yaml
- name: Ensure Monkeybrains NIC exists when enabled
  ansible.builtin.fail:
    msg: >-
      Monkeybrains fallback is enabled, but no virtio net2 interface was found in Proxmox
      config for VM {{ actual_vmid }}. Add net2 (virtio) attached to the Monkeybrains network
      or disable mwan_health_checks.monkeybrains.enabled.
  when:
    - mwan_health_checks.monkeybrains.enabled
    - not mwan_net2_present
```

Then confirm `mwan_net2_present`, set at lines 43 to 46, still has a reader.
Search the repository for it; if this file was its only consumer, delete its
clause from the `Parse virtio MAC addresses from VM config` task in the same
commit, and update the header comment at lines 1 to 10, which lists the
facts this file produces.

- [ ] **Step 17: Correct the routing documentation**

In `docs/ops/infra/network.md`, replace lines 298 to 299:

```markdown
Expected: one entry in the main table and one in each WAN table
(`mwan_rt_tables` names the ids).
```

with:

```markdown
Expected: one entry in the main table and one in each provider's routing table.
The provider list in the MWAN group vars names the table ids.
```

- [ ] **Step 18: Write the render check**

No gate in this repository renders a Jinja template, because rendering needs the
vaulted inventory. This check closes that gap for the two templates whose output
this work changes, by rendering the pre-change and post-change templates from
the same variables and diffing them.

Create `render-template.py` in the session's scratchpad directory:

```python
#!/usr/bin/env python3
"""Render one repository Jinja template the way Ansible renders it.

Ansible runs Jinja with trim_blocks on and lstrip_blocks off, and supplies
to_nice_json, to_json, combine, and ternary. This reproduces exactly those four
filters and those two settings, so a template that renders here renders the same
way on the controller. StrictUndefined is on, so a template reading a variable
the fixture does not carry fails loudly instead of emitting an empty string.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

from jinja2 import Environment, FileSystemLoader, StrictUndefined


def to_nice_json(value: Any) -> str:
    """Match ansible.builtin.to_nice_json: indent four, keys sorted."""
    return json.dumps(value, indent=4, sort_keys=True, separators=(",", ": "))


def to_json(value: Any) -> str:
    """Match ansible.builtin.to_json."""
    return json.dumps(value, separators=(",", ": "))


def combine(base: dict[str, Any], other: dict[str, Any]) -> dict[str, Any]:
    """Match the non-recursive default of ansible.builtin.combine."""
    merged = dict(base)
    merged.update(other)
    return merged


def ternary(condition: Any, true_value: Any, false_value: Any) -> Any:
    """Match ansible.builtin.ternary."""
    if condition:
        return true_value
    return false_value


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: render-template.py <template.j2> <variables.json>", file=sys.stderr)
        return 2
    template_path = Path(sys.argv[1]).resolve()
    variables = json.loads(Path(sys.argv[2]).read_text())
    environment = Environment(
        loader=FileSystemLoader(str(template_path.parent)),
        trim_blocks=True,
        lstrip_blocks=False,
        keep_trailing_newline=True,
        undefined=StrictUndefined,
        autoescape=False,
    )
    environment.filters["to_nice_json"] = to_nice_json
    environment.filters["to_json"] = to_json
    environment.filters["combine"] = combine
    environment.filters["ternary"] = ternary
    sys.stdout.write(environment.get_template(template_path.name).render(**variables))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

Create `prod-vars.json` beside it. It carries both the removed variables and
the new ones, so one fixture renders the pre-change and post-change templates.
The values are the resolved forms Ansible would produce: the provider entries'
Jinja references are already substituted, because this renderer does not
re-template inventory values. The pinned seed lists carry a real subset rather
than all thirty-odd production CIDRs, because this work does not alter how a
seed element renders.

```json
{
  "mwan_att_iface": "enatt0",
  "mwan_att_vlan_id": 3242,
  "mwan_webpass_iface": "enwebpass0",
  "mwan_monkeybrains_iface": "enmbrains0",
  "mwan_mgmt_iface": "enmgmt0",
  "mwan_internal_iface": "enmwanbr0",
  "mwan_internal_prefix": "3d06:bad:b01::/60",
  "mwan_internal_net_v4": "10.250.250.0/29",
  "mwan_opnsense_internal_v4": "10.250.250.2",
  "mwan_opnsense_edge_ipv6": "3d06:bad:b01:fe::2",
  "mwan_mwanbr_edge_ipv6": "3d06:bad:b01:fe::3",
  "mwan_webpass_ipv4_addr": "136.25.91.242",
  "mwan_health_probe_timeout_ms": 2000,
  "mwan_ifmgr_wan_enabled": true,
  "wanconfig_restconf_port": 10080,
  "actual_mgmt_iface": "enmgmt0",
  "actual_att_iface": "enatt0",
  "actual_webpass_iface": "enwebpass0",
  "actual_internal_iface": "enmwanbr0",
  "mwan_hash_mode": "random",
  "mwan_pin_provider": "att",
  "mwan_reserved_tables": {"cloudflared": 400, "oob": 500},
  "mwan_providers": [
    {
      "name": "att",
      "iface": "enatt0",
      "vlan_id": 3242,
      "table": 100,
      "mark": 1,
      "mark_prio": 100,
      "from_prio": 55,
      "tier": 0,
      "weight": 1,
      "npt_prefix": "2600:1700:2f71:c80::/60",
      "v4_source": "",
      "static_mappings": [
        {"internal": "10.250.250.2", "external": "104.57.226.193"},
        {"internal": "10.250.250.3", "external": "104.57.226.194"},
        {"internal": "10.250.250.4", "external": "104.57.226.195"},
        {"internal": "10.250.250.5", "external": "104.57.226.196"},
        {"internal": "10.250.250.6", "external": "104.57.226.197"}
      ],
      "health": {
        "enabled": true,
        "ping_count": 3,
        "success_threshold": 2,
        "failure_threshold": 2,
        "recovery_threshold": 2,
        "check_interval": 10,
        "targets_v4": ["1.1.1.1", "8.8.8.8"],
        "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
        "http_targets": ["https://ifconfig.co/ip"]
      }
    },
    {
      "name": "webpass",
      "iface": "enwebpass0",
      "vlan_id": "",
      "table": 200,
      "mark": 2,
      "mark_prio": 200,
      "from_prio": 56,
      "tier": 0,
      "weight": 1,
      "npt_prefix": "2604:5500:c271:be00::/60",
      "v4_source": "136.25.91.242",
      "static_mappings": [
        {"internal": "10.250.250.2", "external": "136.25.91.242"},
        {"internal": "10.250.250.3", "external": "136.25.91.243"},
        {"internal": "10.250.250.4", "external": "136.25.91.244"},
        {"internal": "10.250.250.5", "external": "136.25.91.245"},
        {"internal": "10.250.250.6", "external": "136.25.91.246"}
      ],
      "health": {
        "enabled": true,
        "ping_count": 3,
        "success_threshold": 2,
        "failure_threshold": 2,
        "recovery_threshold": 2,
        "check_interval": 10,
        "targets_v4": ["1.1.1.1", "8.8.8.8"],
        "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
        "http_targets": ["https://ifconfig.co/ip"]
      }
    },
    {
      "name": "monkeybrains",
      "iface": "enmbrains0",
      "vlan_id": "",
      "table": 300,
      "mark": 3,
      "mark_prio": 300,
      "from_prio": 57,
      "tier": 1,
      "weight": 1,
      "npt_prefix": "2607:f598:d3e8:4500::/60",
      "v4_source": "",
      "static_mappings": [],
      "health": {
        "enabled": true,
        "ping_count": 5,
        "success_threshold": 1,
        "failure_threshold": 5,
        "recovery_threshold": 3,
        "check_interval": 30,
        "targets_v4": ["1.1.1.1", "8.8.8.8"],
        "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
        "http_targets": ["https://ifconfig.co/ip"]
      }
    }
  ],
  "mwan_rt_tables": {"att": 100, "webpass": 200, "monkeybrains": 300, "cloudflared": 400},
  "mwan_ifmgr_wan_fw_marks": {"att": 1, "webpass": 2, "monkeybrains": 3},
  "mwan_ifmgr_wan_fw_mark_prios": {"att": 100, "webpass": 200, "monkeybrains": 300},
  "mwan_ifmgr_wan_from_prios": {"att": 55, "webpass": 56, "monkeybrains": 57},
  "mwan_npt_att_prefix": "2600:1700:2f71:c80::/60",
  "mwan_npt_webpass_prefix": "2604:5500:c271:be00::/60",
  "mwan_npt_monkeybrains_prefix": "2607:f598:d3e8:4500::/60",
  "mwan_health_checks": {
    "att": {
      "enabled": true, "ping_count": 3, "success_threshold": 2,
      "failure_threshold": 2, "recovery_threshold": 2, "check_interval": 10,
      "targets_v4": ["1.1.1.1", "8.8.8.8"],
      "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
      "http_targets": ["https://ifconfig.co/ip"]
    },
    "webpass": {
      "enabled": true, "ping_count": 3, "success_threshold": 2,
      "failure_threshold": 2, "recovery_threshold": 2, "check_interval": 10,
      "targets_v4": ["1.1.1.1", "8.8.8.8"],
      "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
      "http_targets": ["https://ifconfig.co/ip"]
    },
    "monkeybrains": {
      "enabled": true, "ping_count": 5, "success_threshold": 1,
      "failure_threshold": 5, "recovery_threshold": 3, "check_interval": 30,
      "targets_v4": ["1.1.1.1", "8.8.8.8"],
      "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
      "http_targets": ["https://ifconfig.co/ip"]
    }
  },
  "mwan_static_mappings": {
    "att": [
      {"internal": "10.250.250.2", "external": "104.57.226.193"},
      {"internal": "10.250.250.3", "external": "104.57.226.194"},
      {"internal": "10.250.250.4", "external": "104.57.226.195"},
      {"internal": "10.250.250.5", "external": "104.57.226.196"},
      {"internal": "10.250.250.6", "external": "104.57.226.197"}
    ],
    "webpass": [
      {"internal": "10.250.250.2", "external": "136.25.91.242"},
      {"internal": "10.250.250.3", "external": "136.25.91.243"},
      {"internal": "10.250.250.4", "external": "136.25.91.244"},
      {"internal": "10.250.250.5", "external": "136.25.91.245"},
      {"internal": "10.250.250.6", "external": "136.25.91.246"}
    ]
  },
  "mwan_att_pinned_v4_seed_cidrs": ["74.125.250.0/24", "208.54.0.0/16", "8.28.124.0/24"],
  "mwan_att_pinned_v6_seed_cidrs": ["2600:1000::/28", "2607:fb90::/32"],
  "mwan_pinned_v4_seed_cidrs": ["74.125.250.0/24", "208.54.0.0/16", "8.28.124.0/24"],
  "mwan_pinned_v6_seed_cidrs": ["2600:1000::/28", "2607:fb90::/32"]
}
```

Create `testbed-vars.json` beside it, with the same keys and the testbed's
resolved values. The addresses come from
`ansible/inventory/group_vars/all/service_mapping.yml:171-177` and `:203-224`:
the transit network is 10.240.240.0/29, the router sits at 10.240.240.2 and
3d06:bad:b01:201::2, and the gateway at 10.240.240.3 and 3d06:bad:b01:201::3.
The five mapped internal hosts resolve to 10.240.240.2 through 10.240.240.6.

```json
{
  "mwan_att_iface": "enatt0",
  "mwan_att_vlan_id": "",
  "mwan_webpass_iface": "enwebpass0",
  "mwan_monkeybrains_iface": "enmbrains0",
  "mwan_mgmt_iface": "enmgmt0",
  "mwan_internal_iface": "enmwanbr0",
  "mwan_internal_prefix": "3d06:bad:b01:210::/60",
  "mwan_internal_net_v4": "10.240.240.0/29",
  "mwan_opnsense_internal_v4": "10.240.240.2",
  "mwan_opnsense_edge_ipv6": "3d06:bad:b01:201::2",
  "mwan_mwanbr_edge_ipv6": "3d06:bad:b01:201::3",
  "mwan_webpass_ipv4_addr": "10.240.204.2",
  "mwan_health_probe_timeout_ms": 2000,
  "mwan_ifmgr_wan_enabled": true,
  "wanconfig_restconf_port": 10080,
  "actual_mgmt_iface": "enmgmt0",
  "actual_att_iface": "enatt0",
  "actual_webpass_iface": "enwebpass0",
  "actual_internal_iface": "enmwanbr0",
  "mwan_hash_mode": "random",
  "mwan_pin_provider": "att",
  "mwan_reserved_tables": {"cloudflared": 400, "oob": 500},
  "mwan_providers": [
    {
      "name": "att", "iface": "enatt0", "vlan_id": "",
      "table": 100, "mark": 1, "mark_prio": 100, "from_prio": 55,
      "tier": 0, "weight": 1,
      "npt_prefix": "3d06:bad:b01:2300::/60", "v4_source": "",
      "static_mappings": [
        {"internal": "10.240.240.2", "external": "10.241.205.2"},
        {"internal": "10.240.240.3", "external": "10.241.205.3"},
        {"internal": "10.240.240.4", "external": "10.241.205.4"},
        {"internal": "10.240.240.5", "external": "10.241.205.5"},
        {"internal": "10.240.240.6", "external": "10.241.205.6"}
      ],
      "health": {
        "enabled": true, "ping_count": 3, "success_threshold": 2,
        "failure_threshold": 2, "recovery_threshold": 2, "check_interval": 10,
        "targets_v4": ["1.1.1.1", "8.8.8.8"],
        "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
        "http_targets": ["https://ifconfig.co/ip"]
      }
    },
    {
      "name": "webpass", "iface": "enwebpass0", "vlan_id": "",
      "table": 200, "mark": 2, "mark_prio": 200, "from_prio": 56,
      "tier": 0, "weight": 1,
      "npt_prefix": "3d06:bad:b01:2200::/60", "v4_source": "10.240.204.2",
      "static_mappings": [
        {"internal": "10.240.240.2", "external": "10.241.204.2"},
        {"internal": "10.240.240.3", "external": "10.241.204.3"},
        {"internal": "10.240.240.4", "external": "10.241.204.4"},
        {"internal": "10.240.240.5", "external": "10.241.204.5"},
        {"internal": "10.240.240.6", "external": "10.241.204.6"}
      ],
      "health": {
        "enabled": true, "ping_count": 3, "success_threshold": 2,
        "failure_threshold": 2, "recovery_threshold": 2, "check_interval": 10,
        "targets_v4": ["1.1.1.1", "8.8.8.8"],
        "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
        "http_targets": ["https://ifconfig.co/ip"]
      }
    },
    {
      "name": "monkeybrains", "iface": "enmbrains0", "vlan_id": "",
      "table": 300, "mark": 3, "mark_prio": 300, "from_prio": 57,
      "tier": 1, "weight": 1,
      "npt_prefix": "3d06:bad:b01:2400::/60", "v4_source": "",
      "static_mappings": [],
      "health": {
        "enabled": true, "ping_count": 5, "success_threshold": 1,
        "failure_threshold": 5, "recovery_threshold": 3, "check_interval": 30,
        "targets_v4": ["1.1.1.1", "8.8.8.8"],
        "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
        "http_targets": ["https://ifconfig.co/ip"]
      }
    }
  ],
  "mwan_rt_tables": {"att": 100, "webpass": 200, "monkeybrains": 300, "cloudflared": 400},
  "mwan_ifmgr_wan_fw_marks": {"att": 1, "webpass": 2, "monkeybrains": 3},
  "mwan_ifmgr_wan_fw_mark_prios": {"att": 100, "webpass": 200, "monkeybrains": 300},
  "mwan_ifmgr_wan_from_prios": {"att": 55, "webpass": 56, "monkeybrains": 57},
  "mwan_npt_att_prefix": "3d06:bad:b01:2300::/60",
  "mwan_npt_webpass_prefix": "3d06:bad:b01:2200::/60",
  "mwan_npt_monkeybrains_prefix": "3d06:bad:b01:2400::/60",
  "mwan_health_checks": {
    "att": {
      "enabled": true, "ping_count": 3, "success_threshold": 2,
      "failure_threshold": 2, "recovery_threshold": 2, "check_interval": 10,
      "targets_v4": ["1.1.1.1", "8.8.8.8"],
      "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
      "http_targets": ["https://ifconfig.co/ip"]
    },
    "webpass": {
      "enabled": true, "ping_count": 3, "success_threshold": 2,
      "failure_threshold": 2, "recovery_threshold": 2, "check_interval": 10,
      "targets_v4": ["1.1.1.1", "8.8.8.8"],
      "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
      "http_targets": ["https://ifconfig.co/ip"]
    },
    "monkeybrains": {
      "enabled": true, "ping_count": 5, "success_threshold": 1,
      "failure_threshold": 5, "recovery_threshold": 3, "check_interval": 30,
      "targets_v4": ["1.1.1.1", "8.8.8.8"],
      "targets_v6": ["2606:4700:4700::1111", "2001:4860:4860::8888"],
      "http_targets": ["https://ifconfig.co/ip"]
    }
  },
  "mwan_static_mappings": {
    "att": [
      {"internal": "10.240.240.2", "external": "10.241.205.2"},
      {"internal": "10.240.240.3", "external": "10.241.205.3"},
      {"internal": "10.240.240.4", "external": "10.241.205.4"},
      {"internal": "10.240.240.5", "external": "10.241.205.5"},
      {"internal": "10.240.240.6", "external": "10.241.205.6"}
    ],
    "webpass": [
      {"internal": "10.240.240.2", "external": "10.241.204.2"},
      {"internal": "10.240.240.3", "external": "10.241.204.3"},
      {"internal": "10.240.240.4", "external": "10.241.204.4"},
      {"internal": "10.240.240.5", "external": "10.241.204.5"},
      {"internal": "10.240.240.6", "external": "10.241.204.6"}
    ]
  },
  "mwan_att_pinned_v4_seed_cidrs": [],
  "mwan_att_pinned_v6_seed_cidrs": [],
  "mwan_pinned_v4_seed_cidrs": [],
  "mwan_pinned_v6_seed_cidrs": []
}
```

- [ ] **Step 19: Run the render check on the network configuration**

```bash
cd "$(git rev-parse --show-toplevel)"
SCRATCH="$SCRATCHPAD"           # the session's scratchpad directory
python3 -m venv "$SCRATCH/venv" && "$SCRATCH/venv/bin/pip" install --quiet jinja2
git show HEAD:mwan/config/network.json.j2 > "$SCRATCH/network.json.j2.before"
cp mwan/config/network.json.j2 "$SCRATCH/network.json.j2.after"
for env in prod testbed; do
  "$SCRATCH/venv/bin/python" "$SCRATCH/render-template.py" \
    "$SCRATCH/network.json.j2.before" "$SCRATCH/$env-vars.json" > "$SCRATCH/$env-network-before.json"
  "$SCRATCH/venv/bin/python" "$SCRATCH/render-template.py" \
    "$SCRATCH/network.json.j2.after" "$SCRATCH/$env-vars.json" > "$SCRATCH/$env-network-after.json"
  diff -u "$SCRATCH/$env-network-before.json" "$SCRATCH/$env-network-after.json"
done
```

Expected, for each environment: the diff shows added lines only, and exactly
these. One block per provider interface, ahead of that interface's
`goodkind-mwan-steering:wan` key:

```
+                "goodkind-mwan-steering:steering": {
+                    "tier": 0,
+                    "weight": 1
+                },
```

with `"tier": 1` on the monkeybrains interface. And two additions inside
`goodkind-mwan-steering:steering-group`:

```
+            "hash-mode": "random",
+            "reserved-tables": [
+                400,
+                500
+            ],
```

No removed line, no changed line. The `HEAD` render is taken before the commit
in Step 23, so run this while the working tree still holds the change.

- [ ] **Step 20: Read back the production render in full**

```bash
cat "$SCRATCH/prod-network-after.json"
```

Expected, exactly:

```json
{
    "ietf-interfaces:interfaces": {
        "goodkind-mwan-steering:steering-group": {
            "hash-mode": "random",
            "health": {
                "probe-timeout": 2000
            },
            "reserved-tables": [
                400,
                500
            ],
            "routes": {
                "internal-iface": "enmwanbr0",
                "internal-net-v4": "10.250.250.0/29"
            },
            "translation": {
                "internal-prefix": "3d06:bad:b01::/60",
                "mwanbr-edge-v6": "3d06:bad:b01:fe::3",
                "opnsense-edge-v6": "3d06:bad:b01:fe::2"
            }
        },
        "interface": [
            {
                "goodkind-mwan-steering:steering": {
                    "tier": 0,
                    "weight": 1
                },
                "goodkind-mwan-steering:wan": {
                    "from-prio": 55,
                    "fw-mark": 1,
                    "fw-mark-prio": 100,
                    "health": {
                        "check-interval": 10,
                        "enabled": true,
                        "failure-threshold": 2,
                        "http-urls": [
                            "https://ifconfig.co/ip"
                        ],
                        "ping-count": 3,
                        "recovery-threshold": 2,
                        "success-threshold": 2,
                        "targets-v4": [
                            "1.1.1.1",
                            "8.8.8.8"
                        ],
                        "targets-v6": [
                            "2606:4700:4700::1111",
                            "2001:4860:4860::8888"
                        ]
                    },
                    "name": "att",
                    "npt-prefix": "2600:1700:2f71:c80::/60",
                    "table-id": 100
                },
                "name": "enatt0.3242",
                "type": "iana-if-type:other"
            },
            {
                "goodkind-mwan-steering:steering": {
                    "tier": 0,
                    "weight": 1
                },
                "goodkind-mwan-steering:wan": {
                    "from-prio": 56,
                    "fw-mark": 2,
                    "fw-mark-prio": 200,
                    "health": {
                        "check-interval": 10,
                        "enabled": true,
                        "failure-threshold": 2,
                        "http-urls": [
                            "https://ifconfig.co/ip"
                        ],
                        "ping-count": 3,
                        "recovery-threshold": 2,
                        "success-threshold": 2,
                        "targets-v4": [
                            "1.1.1.1",
                            "8.8.8.8"
                        ],
                        "targets-v6": [
                            "2606:4700:4700::1111",
                            "2001:4860:4860::8888"
                        ]
                    },
                    "name": "webpass",
                    "npt-prefix": "2604:5500:c271:be00::/60",
                    "table-id": 200,
                    "v4-source": "136.25.91.242"
                },
                "name": "enwebpass0",
                "type": "iana-if-type:other"
            },
            {
                "goodkind-mwan-steering:steering": {
                    "tier": 1,
                    "weight": 1
                },
                "goodkind-mwan-steering:wan": {
                    "from-prio": 57,
                    "fw-mark": 3,
                    "fw-mark-prio": 300,
                    "health": {
                        "check-interval": 30,
                        "enabled": true,
                        "failure-threshold": 5,
                        "http-urls": [
                            "https://ifconfig.co/ip"
                        ],
                        "ping-count": 5,
                        "recovery-threshold": 3,
                        "success-threshold": 1,
                        "targets-v4": [
                            "1.1.1.1",
                            "8.8.8.8"
                        ],
                        "targets-v6": [
                            "2606:4700:4700::1111",
                            "2001:4860:4860::8888"
                        ]
                    },
                    "name": "monkeybrains",
                    "npt-prefix": "2607:f598:d3e8:4500::/60",
                    "table-id": 300
                },
                "name": "enmbrains0",
                "type": "iana-if-type:other"
            },
            {
                "name": "enmwanbr0",
                "type": "iana-if-type:other"
            }
        ]
    }
}
```

The key order is alphabetical because `to_nice_json` sorts keys, which is why
`from-prio` precedes `fw-mark` and the steering group precedes the interface
list.

- [ ] **Step 21: Validate both renders against the new schema**

```bash
cd "$(git rev-parse --show-toplevel)"
for env in prod testbed; do
  yanglint -t config \
    third_party/yang/standard/ietf/RFC/ietf-yang-types@2025-12-22.yang \
    third_party/yang/standard/ietf/RFC/ietf-inet-types@2025-12-22.yang \
    third_party/yang/standard/ietf/RFC/iana-if-type@2014-05-08.yang \
    third_party/yang/standard/ietf/RFC/ietf-interfaces@2018-02-20.yang \
    third_party/yang/standard/ietf/RFC/ietf-ip@2018-02-22.yang \
    third_party/yang/standard/ietf/RFC/ietf-routing@2018-03-13.yang \
    third_party/yang/standard/ietf/RFC/ietf-nat@2019-01-10.yang \
    mwan/yang/goodkind-mwan-steering@2026-09-02.yang \
    "$SCRATCH/$env-network-after.json" || echo "FAILED: $env"
done
```

Expected: silence and exit 0 for both, and no `FAILED` line. This is the same
check `ansible/playbooks/deploy-mwan.yml:264-284` runs on every deploy, run
here before a deploy exists.

- [ ] **Step 22: Verify the plays parse and the templates lint**

```bash
cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:mwan
```

Expected: PASS, exit 0.

```bash
cd "$(git rev-parse --show-toplevel)" && ./configsctl lint
```

Expected: PASS, exit 0, and in particular no line of the form
`banned default or presence check: self-ternary on ...` or
`banned default or presence check: length on mwan_providers`.

- [ ] **Step 23: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add ansible/inventory/group_vars/all/vars.yml ansible/inventory/group_vars/mwan_servers.yml ansible/inventory/group_vars/mwan_suburban_servers.yml mwan/config/network.json.j2 mwan/config/rt_tables.j2 mwan/config/mwan.env.j2 mwan/config/_ifmgr_common.toml.j2 mwan/config/config-host.toml.j2 mwan/config/config-vm.toml.j2 mwan/config/nftables.conf.j2 mwan/networkd/20-webpass.network.j2 mwan/networkd/testbed/20-webpass.network.j2 mwan/networkd/21-att-vlan.network.j2 ansible/playbooks/tasks/mwan-vm/discover-runtime-network.yml docs/ops/infra/network.md
git commit -S -m "Collapse the per-provider gateway variables into mwan_providers" -m "Give each gateway group one provider list carrying the interface, routing numbers, tier, weight, translation prefix, source pin, static mappings, and probe policy; add mwan_hash_mode, mwan_pin_provider, and a single mwan_reserved_tables registry; render the network tree, the routing-table names, and the out-of-band table id from them; look the Webpass table up in the list from the two networkd files; delete the monkeybrains-specific NIC assertion; and drop the routing and translation keys from the env file, which no consumer reads." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

#### Carried by this task for the proof task to use (decisions 7 and 14)

The link files stay hand written. No renderer is introduced, and the network
configuration describes no link bring-up, because the daemon bringing links up
itself is the monolith epic's work and any renderer written now would be
deleted when that lands.

A fourth testbed provider therefore needs two files added by hand, plus two
lines in the testbed group's `mwan_networkd_files`. The proof task adds these;
they are written out here so that task carries no placeholder.

Create `mwan/networkd/30-astound.link.j2`:

```jinja
# Astound WAN Interface (testbed simulator, virtio)
# Generated by Ansible
# Matches on virtio MAC address from Proxmox config, assigns stable name.

[Match]
MACAddress={{ mwan_astound_mac | lower }}

[Link]
Name={{ mwan_astound_iface }}
```

Create `mwan/networkd/30-astound.network.j2`:

```jinja
# Astound WAN Interface (testbed simulator, virtio)
# Generated by Ansible
# The fourth simulated provider, modelled on Monkeybrains: DHCPv4 pinned to a
# stable address by the sim's MAC reservation, plus DHCPv6-PD. The sim does not
# check DHCPv6 identities, so no DUID is set here.

[Match]
Name={{ mwan_astound_iface }}

[Network]
DHCP=yes
IPv6AcceptRA=yes

# Needed for policy routing and forwarding
IPv4Forwarding=yes
IPv6Forwarding=yes

[DHCPv4]
# Tier 2: the least preferred main-table default of the four.
RouteMetric=6000

[DHCPv6]
UseDelegatedPrefix=yes
PrefixDelegationHint=::/56
WithoutRA=solicit

[IPv6PrefixDelegation]
RouterLifetimeSec=1800

[IPv6AcceptRA]
RouteMetric=6000
```

Add to `mwan_networkd_files` in
`ansible/inventory/group_vars/mwan_suburban_servers.yml`:

```yaml
  - 30-astound.link
  - 30-astound.network
```

And append this entry to the testbed `mwan_providers` list:

```yaml
  - name: astound
    iface: "{{ mwan_astound_iface }}"
    vlan_id: ""
    table: 600
    mark: 4
    mark_prio: 600
    from_prio: 58
    tier: 2
    weight: 1
    npt_prefix: "3d06:bad:b01:2500::/60"
    v4_source: ""
    static_mappings: []
    health:
      enabled: true
      ping_count: 5
      success_threshold: 1
      failure_threshold: 5
      recovery_threshold: 3
      check_interval: 30
      targets_v4: ["1.1.1.1", "8.8.8.8"]
      targets_v6: ["2606:4700:4700::1111", "2001:4860:4860::8888"]
      http_targets: ["https://ifconfig.co/ip"]
```

Note for the proof task: the link files above take the address by DHCP, so
`mwan_astound_ipv4` from decision 14 has no reader. Either drop that variable
or write the network file with a static address and gateway the way the testbed
Webpass file does. The simulator's reservation pins 10.240.207.2 either way.

---

### Task 6: the firewall ruleset renders from the provider list

The ruleset stops naming three providers and loops the list. The three fixed
balancing lines leave the file, because the daemon's steering module owns the
balance from this change on.

**Why the ordering still holds.** Two invariants the spec names must be checked
rather than assumed, and both hold in the template below.

The outbound translation rules stay grouped by rule kind, not by provider: all
one-to-one source translations first, then the per-mark masquerades, then the
emergency masquerades. That is the order the file has today, so a translation
statement that stops rule evaluation stops it at the same rule it stops at
today. Every rule in the postrouting chain carries an outgoing-interface match
and every inbound one-to-one rule carries an incoming-interface match, which is
what makes the grouping safe. Step 5 asserts both.

The control-plane pins keep winning. The IPv6 pin sits in the mangle chain at
priority -150, and the daemon's steering chain runs at -149 with a `meta mark 0`
guard, so the guard sees the pin's mark and skips. The IPv4 pin sits in the nat
prerouting chain at priority -100, which now runs after the steering chain
rather than in the same chain as the balancer, so it overwrites whatever the
balancer chose. Both cases end with the pin's mark on the connection, which is
what the postrouting `ct mark set meta mark` saves.

**Files:**
- Modify: `mwan/config/nftables.conf.j2:1-245` (whole file)
- Modify: `ansible/playbooks/deploy-mwan.yml:527-531` (the template task's vars)
- Modify: `ansible/inventory/group_vars/mwan_servers.yml:185-202` (delete the
  static-mapping section)
- Modify: `ansible/inventory/group_vars/mwan_suburban_servers.yml:93-116`
  (delete `mwan_static_mappings`, keep `mwan_static_internal_hosts`)

**Interfaces:**
- Consumes: `mwan_providers`, `mwan_pin_provider`, and the renamed pinned lists
  from Task 5.
- Produces: an `/etc/nftables.conf` with no `numgen` statement, so the daemon's
  `inet mwan_steer` chain is the only writer of the balanced mark.
- Removes: `mwan_static_mappings` from both gateway groups, and the
  `actual_att_iface` and `actual_webpass_iface` task variables.

- [ ] **Step 1: Rewrite the ruleset template**

Replace the whole of `mwan/config/nftables.conf.j2` with:

```jinja
#!/usr/sbin/nftables -f
# nftables configuration for the mwan gateway
# Generated by Ansible
#
# NOTE: Interfaces may be DOWN (for example, cable unplugged). That is fine.
# Rules simply do not match until an interface comes back up. nftables uses
# actual interface names (not altnames) for reliability.
#
# Every per-provider rule below is rendered from mwan_providers, so a provider
# added to that list is carried here with no edit to this file.
#
# Block tags sit at column zero on purpose. Ansible renders with trim_blocks on
# and lstrip_blocks off, so an indented tag would emit its leading spaces into
# the ruleset.

{#
  providers is mwan_providers with one derived value added: the kernel
  interface name, which carries the VLAN tag when the provider rides one. A
  block-if builds it, because the template lint rejects a self-ternary.
#}
{% set providers = [] %}
{% for entry in mwan_providers %}
{% if entry.vlan_id %}
{% set _ = providers.append(entry | combine({"ifname": entry.iface ~ "." ~ entry.vlan_id})) %}
{% else %}
{% set _ = providers.append(entry | combine({"ifname": entry.iface})) %}
{% endif %}
{% endfor %}
{% set wan_ifnames = providers | map(attribute='ifname') | join('", "') %}
{% set lowest_tier = providers | map(attribute='tier') | min %}
{% set pin_mark = (providers | selectattr('name', 'equalto', mwan_pin_provider) | first).mark %}
flush ruleset

# Define variables using actual kernel interface names
define MGMT_IFACE = "{{ actual_mgmt_iface }}"
define INTERNAL_IFACE = "{{ actual_internal_iface }}"

# Internal network (mwan to OPNsense link)
define INTERNAL_NET = {{ mwan_internal_net_v4 }}
define OPNSENSE_INTERNAL = {{ mwan_opnsense_internal_v4 }}

table inet filter {
    chain input {
        type filter hook input priority filter; policy drop;

        # Allow established/related
        ct state established,related accept

        # Allow loopback
        iif lo accept

        # Allow ICMP
        ip protocol icmp accept
        ip6 nexthdr icmpv6 accept

        # Allow SSH from management network
        iifname $MGMT_IFACE tcp dport 22 accept

        # Allow mwan-agent gRPC (TCP) from hypervisor (vault)
        iifname $MGMT_IFACE tcp dport 50052 accept

        # Allow the wanconfig RESTCONF surface (nghttpx front end) on the
        # management interface only (MWAN-357)
        iifname $MGMT_IFACE tcp dport {{ wanconfig_restconf_port }} accept

        # Allow BGP and BFD from OPNsense on internal interface
        iifname $INTERNAL_IFACE tcp dport 179 accept
        iifname $INTERNAL_IFACE udp dport { 3784, 3785 } accept

        # Allow DHCP on every provider link, both families
{% for provider in providers %}
        iifname "{{ provider.ifname }}" udp sport 67 udp dport 68 accept
        iifname "{{ provider.ifname }}" udp sport 547 udp dport 546 accept
{% endfor %}

        # Log dropped packets (rate limited)
        limit rate 1/second log prefix "nftables input drop: " drop
    }

    chain forward {
        type filter hook forward priority filter; policy drop;

        # Allow established/related
        ct state established,related accept

        # Allow forwarding from internal to every provider.
        iifname $INTERNAL_IFACE oifname { "{{ wan_ifnames }}" } accept

        # Allow forwarding from every provider to internal (for inbound services)
        iifname { "{{ wan_ifnames }}" } oifname $INTERNAL_IFACE accept

        # Log dropped packets (rate limited)
        limit rate 1/second log prefix "nftables forward drop: " drop
    }

    chain output {
        type filter hook output priority filter; policy accept;
    }
}

table ip nat {
    chain prerouting {
        type nat hook prerouting priority dstnat; policy accept;

        # Pin OPNsense-initiated WireGuard control-plane to the pinned provider
        # regardless of peer IP. WG ports are static (OPNsense listens on 51820,
        # configured peers listen on 51821) even when the peer's IP renumbers.
        # Without this pin the daemon's steering chain would split rekey
        # attempts across the active tier; the resulting peer-IP flapping (via
        # WG roaming) destabilizes long-lived peers like suburban whose Comcast
        # IPv6 also renumbers. This chain runs after the steering chain, so it
        # overwrites whatever mark the balance chose.
        iifname $INTERNAL_IFACE ip saddr $OPNSENSE_INTERNAL udp sport 51820 udp dport 51821 ct state new meta mark set {{ pin_mark }}

        # Inbound 1:1 NAT (DNAT) for static IPs.
        # Map each provider's routed block back onto the internal hosts so
        # OPNsense (and downstream) can receive inbound traffic. Every rule
        # matches on the incoming interface, which is what keeps these separate
        # from the outbound rules below.
{% for provider in providers %}
{% for mapping in provider.static_mappings %}
        iifname "{{ provider.ifname }}" ip daddr {{ mapping.external }} dnat to {{ mapping.internal }}
{% endfor %}
{% endfor %}
    }

    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;

        # 1:1 NAT for static IPs, per provider. These come first, so a host
        # with a mapped address keeps its own external address rather than
        # falling into the masquerade below.
{% for provider in providers %}
{% for mapping in provider.static_mappings %}
        oifname "{{ provider.ifname }}" ip saddr {{ mapping.internal }} snat to {{ mapping.external }}
{% endfor %}
{% endfor %}

        # Default SNAT for other LAN traffic, selected by the provider's mark.
{% for provider in providers %}
        oifname "{{ provider.ifname }}" meta mark {{ provider.mark }} ip saddr $INTERNAL_NET masquerade
{% endfor %}

        # Emergency fallback SNAT for a provider outside the preferred tier:
        # traffic routed out of it with any mark, or none, still translates.
{% for provider in providers %}
{% if provider.tier > lowest_tier %}
        oifname "{{ provider.ifname }}" ip saddr $INTERNAL_NET masquerade
{% endif %}
{% endfor %}
    }
}

table ip6 nat {
    chain prerouting {
        type nat hook prerouting priority dstnat; policy accept;

        # Inbound NPT is programmed at runtime by the ifmgr npt module.
    }

    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;

        # Outbound NPT is programmed at runtime by the ifmgr npt module: the
        # internal /60 onto each provider's delegated /60.
        #
        # NOTE:
        # Avoid NAT66 on a provider link. IPv6 uses the same /60 NPT mechanism
        # on every provider. A static masquerade here would override the NPT
        # rules through rule order.
    }
}

table inet mangle {
    set att_pinned_v4 {
        type ipv4_addr;
        flags interval;
        auto-merge;
{% if mwan_pinned_v4_seed_cidrs %}
        elements = {
{% for cidr in mwan_pinned_v4_seed_cidrs %}
            {{ cidr }}{% if not loop.last %},{% endif %}
{% endfor %}
        }
{% endif %}
    }

    set att_pinned_v6 {
        type ipv6_addr;
        flags interval;
        auto-merge;
{% if mwan_pinned_v6_seed_cidrs %}
        elements = {
{% for cidr in mwan_pinned_v6_seed_cidrs %}
            {{ cidr }}{% if not loop.last %},{% endif %}
{% endfor %}
        }
{% endif %}
    }

    chain prerouting {
        type filter hook prerouting priority mangle; policy accept;

        # Mark inbound NEW flows by ingress provider, which keeps replies
        # symmetric through policy routing.
{% for provider in providers %}
        iifname "{{ provider.ifname }}" ct state new meta mark set {{ provider.mark }}
{% endfor %}

        # Outbound balancing is not here. The daemon's steering module owns it,
        # in table inet mwan_steer, chain prerouting at priority -149, which
        # runs directly after this chain. Do not mark hosts in $INTERNAL_NET
        # here, or outbound traffic pins to one provider.

        # Pin latency-sensitive destinations to the pinned provider.
        # On failure the daemon prunes that provider's policy rules, and the
        # marked traffic falls through to the main table.
        ip daddr @att_pinned_v4 meta mark set {{ pin_mark }}
        ip6 daddr @att_pinned_v6 meta mark set {{ pin_mark }}

        # Pin OPNsense-initiated WireGuard control-plane (IPv6) to the pinned
        # provider. Mirror of the IPv4 pin in `table ip nat / chain prerouting`.
        # See the note there for rationale; this covers the case where
        # suburban's WG peer is reachable over IPv6 and prone to Comcast prefix
        # renumbers. It runs before the steering chain, whose `meta mark 0`
        # guard then leaves this mark alone.
        ip6 saddr {{ mwan_opnsense_edge_ipv6 }}/128 udp sport 51820 udp dport 51821 ct state new meta mark set {{ pin_mark }}

        # Restore mark for established connections (session affinity)
        ct state established,related meta mark set ct mark
    }

    chain postrouting {
        type filter hook postrouting priority mangle; policy accept;

        # Save mark to conntrack
        ct mark set meta mark
    }
}
```

- [ ] **Step 2: Stop passing the two per-provider interface variables**

In `ansible/playbooks/deploy-mwan.yml`, replace the "Deploy nftables config"
task's `vars` block at lines 527 to 531:

```yaml
      vars:
        actual_mgmt_iface: "{{ mwan_mgmt_iface }}"
        actual_att_iface: "{{ mwan_att_iface }}"
        actual_webpass_iface: "{{ mwan_webpass_iface }}"
        actual_internal_iface: "{{ mwan_internal_iface }}"
```

with:

```yaml
      vars:
        actual_mgmt_iface: "{{ mwan_mgmt_iface }}"
        actual_internal_iface: "{{ mwan_internal_iface }}"
```

- [ ] **Step 3: Delete the static-mapping variables**

In `ansible/inventory/group_vars/mwan_servers.yml`, delete lines 185 to 202,
the whole "Static IP mappings (1:1 NAT)" section including its banner, the
`mwan_static_mappings` mapping, and the trailing blank line. The mappings now
live on the two provider entries that own them.

In `ansible/inventory/group_vars/mwan_suburban_servers.yml`, delete lines 104
to 116, the `mwan_static_mappings` mapping only. Keep
`mwan_static_internal_hosts` at lines 98 to 103, which the provider entries
reference by index. Change the last sentence of the comment above it, on line
97, from `Each maps onto the matching host of the ISP block, so last octets
line up.` to `Each provider entry maps them onto the matching host of its ISP
block, so last octets line up.`

- [ ] **Step 4: Run the ruleset render check**

```bash
cd "$(git rev-parse --show-toplevel)"
SCRATCH="$SCRATCHPAD"
cp mwan/config/nftables.conf.j2 "$SCRATCH/nftables.conf.j2.after"
for env in prod testbed; do
  "$SCRATCH/venv/bin/python" "$SCRATCH/render-template.py" \
    "$SCRATCH/nftables.conf.j2.after" "$SCRATCH/$env-vars.json" > "$SCRATCH/$env-nftables.conf"
done
```

Expected: both renders succeed. `StrictUndefined` means a variable the fixture
does not carry fails here rather than rendering empty.

- [ ] **Step 5: Assert the ruleset's invariants**

Create `check-ruleset.py` in the scratchpad directory:

```python
#!/usr/bin/env python3
"""Assert the invariants the provider loop must preserve in a rendered ruleset.

Takes the rendered ruleset and the same variables fixture that rendered it, so
the expectations derive from the provider list rather than being typed twice.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path


def chain_body(text: str, table: str, chain: str) -> list[str]:
    """Return the rule lines of one chain, without comments or blanks."""
    table_match = re.search(
        r"^table " + re.escape(table) + r" \{\n(.*?)^\}", text, re.S | re.M
    )
    if table_match is None:
        raise SystemExit(f"table {table} not found")
    chain_match = re.search(
        r"^    chain " + re.escape(chain) + r" \{\n(.*?)^    \}",
        table_match.group(1),
        re.S | re.M,
    )
    if chain_match is None:
        raise SystemExit(f"chain {chain} not found in table {table}")
    body = []
    for raw_line in chain_match.group(1).splitlines():
        line = raw_line.strip()
        if line == "" or line.startswith("#"):
            continue
        body.append(line)
    return body


def interface_name(provider: dict[str, object]) -> str:
    """Build the kernel interface name the template builds."""
    if provider["vlan_id"]:
        return f'{provider["iface"]}.{provider["vlan_id"]}'
    return str(provider["iface"])


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: check-ruleset.py <rendered.conf> <variables.json>", file=sys.stderr)
        return 2
    text = Path(sys.argv[1]).read_text()
    variables = json.loads(Path(sys.argv[2]).read_text())
    providers = variables["mwan_providers"]
    pin_name = variables["mwan_pin_provider"]
    pin_mark = 0
    for provider in providers:
        if provider["name"] == pin_name:
            pin_mark = int(provider["mark"])
    failures: list[str] = []

    if "numgen" in text:
        failures.append("the ruleset still carries a numgen balancer statement")
    for stale in ("ATT_IFACE", "WEBPASS_IFACE", "MB_IFACE"):
        if stale in text:
            failures.append(f"the ruleset still defines or uses {stale}")

    mangle = chain_body(text, "inet mangle", "prerouting")
    for provider in providers:
        expected = (
            f'iifname "{interface_name(provider)}" ct state new '
            f'meta mark set {provider["mark"]}'
        )
        if expected not in mangle:
            failures.append(f"missing ingress mark rule: {expected}")

    postrouting = chain_body(text, "ip nat", "postrouting")
    for line in postrouting:
        if line.startswith("type nat hook"):
            continue
        if "oifname" not in line:
            failures.append(f"postrouting rule with no outgoing-interface match: {line}")
    marked = [line for line in postrouting if "meta mark" in line]
    if len(marked) != len(providers):
        failures.append(
            f"{len(marked)} per-mark masquerade rules for {len(providers)} providers"
        )
    lowest_tier = min(int(provider["tier"]) for provider in providers)
    fallbacks = sum(1 for provider in providers if int(provider["tier"]) > lowest_tier)
    unmarked = [
        line
        for line in postrouting
        if "masquerade" in line and "meta mark" not in line
    ]
    if len(unmarked) != fallbacks:
        failures.append(
            f"{len(unmarked)} emergency masquerade rules for {fallbacks} providers "
            "outside the preferred tier"
        )

    prerouting = chain_body(text, "ip nat", "prerouting")
    for line in prerouting:
        if "dnat to" in line and "iifname" not in line:
            failures.append(f"DNAT rule with no incoming-interface match: {line}")
    for line in prerouting:
        if "51820" in line and f"meta mark set {pin_mark}" not in line:
            failures.append(f"control-plane pin does not use the pin mark: {line}")

    for line in mangle:
        if "@att_pinned_v" in line and f"meta mark set {pin_mark}" not in line:
            failures.append(f"pinned-set rule does not use the pin mark: {line}")

    if failures:
        for failure in failures:
            print(f"FAIL: {failure}")
        return 1
    print(f"OK: {len(providers)} providers, pin mark {pin_mark}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

Run it for both environments:

```bash
for env in prod testbed; do
  "$SCRATCH/venv/bin/python" "$SCRATCH/check-ruleset.py" \
    "$SCRATCH/$env-nftables.conf" "$SCRATCH/$env-vars.json"
done
```

Expected, exactly:

```
OK: 3 providers, pin mark 1
OK: 3 providers, pin mark 1
```

- [ ] **Step 6: Diff the rendered ruleset against today's**

```bash
cd "$(git rev-parse --show-toplevel)"
git show HEAD:mwan/config/nftables.conf.j2 > "$SCRATCH/nftables.conf.j2.before"
for env in prod testbed; do
  "$SCRATCH/venv/bin/python" "$SCRATCH/render-template.py" \
    "$SCRATCH/nftables.conf.j2.before" "$SCRATCH/$env-vars.json" > "$SCRATCH/$env-nftables-before.conf"
  diff -u "$SCRATCH/$env-nftables-before.conf" "$SCRATCH/$env-nftables.conf"
done
```

The before render needs the pre-rename pinned variables, and the fixtures carry
both names, so one fixture drives both renders.

Expected rule differences, and only these:

1. The three `define` lines for the provider interfaces are gone, and the rules
   that used them carry quoted literal interface names instead. The rendered
   interface names are identical.
2. Three `numgen` lines are gone: the IPv4 balancer from the nat prerouting
   chain and the two IPv6 balancers from the mangle prerouting chain.
3. The monkeybrains link gains one rule,
   `oifname "enmbrains0" meta mark 3 ip saddr $INTERNAL_NET masquerade`, placed
   ahead of its existing unmarked masquerade. Both rules perform the same
   translation on the same traffic, so the added rule changes which rule matches
   first and changes no packet's fate. Name it in the cutover capture rather
   than letting it read as an unexplained diff.
4. The commented-out management DHCP lines and the commented-out internal SSH
   line are gone. They were dead comments. If the reviewer prefers them kept,
   restore them verbatim, since nothing depends on their absence.
5. Comment text changes where a comment named AT&T as the pin target or
   described the balancer this change removes.

No rule ordering changes within a chain, no accept becomes a drop, and no
address or mark value changes.

- [ ] **Step 7: Verify the play parses and the ruleset is syntactically valid**

```bash
cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:mwan
```

Expected: PASS, exit 0.

```bash
cd "$(git rev-parse --show-toplevel)" && ./configsctl lint
```

Expected: PASS, exit 0.

The kernel-level syntax proof is the deploy's own gate.
`ansible/playbooks/deploy-mwan.yml:524` runs `validate: /usr/sbin/nft -c -f %s`
on the target before the file lands, so a ruleset nft cannot parse fails the
play with `/etc/nftables.conf` untouched. Do not add a second parse on the
controller, which has no nft.

- [ ] **Step 8: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/config/nftables.conf.j2 ansible/playbooks/deploy-mwan.yml ansible/inventory/group_vars/mwan_servers.yml ansible/inventory/group_vars/mwan_suburban_servers.yml
git commit -S -m "Render the firewall ruleset from the provider list" -m "Loop mwan_providers for the DHCP accepts, the forward sets, the one-to-one translations, the per-mark and emergency masquerades, and the ingress marks; take the pinned-destination and WireGuard control-plane marks from the provider mwan_pin_provider names; delete the three fixed balancing lines, which the daemon's steering chain now owns; and move the static mappings onto the provider entries that own them." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

---

### Task 7: the gateway pushes its status and the watchdog drops its interface list

Today the watchdog diagnoses a total outage by pinging the internet through each
WAN interface inside the gateway, over guest-exec, from a list of interface
names typed into its configuration. `mwan/go/internal/watchdog/probe.go:161-211`
is that code. The list omits AT&T on both environments
(`ansible/inventory/group_vars/proxmox_servers.yml:40-42` and
`ansible/inventory/group_vars/suburban_servers.yml:84-86` both hold webpass and
monkeybrains only), the probe's boolean return is discarded by its one caller at
`diagnosis.go:65`, and a fourth provider would need a fourth hand-typed entry in
three templates.

The gateway already knows the answer. Its health module probes every provider on
its own cadence with hysteresis, and after the steering slice it also knows which
tier is carrying traffic. This task makes the gateway say so: one JSON line per
probe cycle over vsock to the hypervisor, kept by the watchdog with its arrival
time and logged during diagnosis. The interface list, its method, and its two
inventory variables leave.

**Direction matters.** Everything vsock in this tree today goes host to guest:
`ops.go:256` dials the gateway's agent. This channel goes the other way, guest
to host. A guest reaches its hypervisor at context id 2, the kernel's fixed
`VMADDR_CID_HOST`, so the gateway needs no knowledge of which machine it is on.
Port 50053 is free: the only ports in this family today are 50051 (agent vsock)
and 50052 (agent TCP fallback).

**One push per cycle.** The push happens at the end of `runCycle`, after
transitions are committed. A verdict change is therefore on the wire in the same
cycle that made it, and a cycle never sends twice. This satisfies the spec's
"after every verdict change and at every probe cycle" without a duplicate
message on transition cycles.

**Files:**
- Create: `mwan/go/internal/statuspush/statuspush.go`
- Create: `mwan/go/internal/statuspush/statuspush_test.go`
- Modify: `mwan/go/internal/config/config.go:16-38` (delete `WANInterface`, the
  `WANInterfaces` field, and `WanIfaceNames`), `:59-91` (add
  `StatusListenPort`), `:724-738` (delete the emptiness check)
- Modify: `mwan/go/internal/config/ifmgr_modules.go:118-128`
  (`IfMgrHealthSection` gains the two push fields)
- Modify: `mwan/go/internal/watchdog/probe.go:158-211` (delete `testISP`)
- Modify: `mwan/go/internal/watchdog/diagnosis.go:59-101` (replace the `testISP`
  call, add `logPushedStatus`)
- Modify: `mwan/go/internal/watchdog/watchdog.go:36-144` (the `status` field and
  its zero value), `:788-813` (start the listener)
- Modify: `mwan/go/internal/watchdog/startup.go:25-31` and `:89-99` (drop the
  two `wan_interfaces` log fields)
- Modify: `mwan/go/internal/watchdog/watchdog_test.go:337-350` (`testNC` loses
  the field), plus the new diagnosis test
- Modify: `mwan/go/internal/ifmgr/modules/health/health.go` (`WAN.Tier`,
  `Config` push fields, `Module.pusher`, `pushStatus`, the `runCycle` call)
- Modify: `mwan/go/internal/ifmgr/modules/health/config.go:181-218` (`New`
  builds the sender)
- Modify: `mwan/go/internal/ifmgr/modules/health/health_test.go` (the push test)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go:169-259`
  (`buildHealthConfig` fills `Tier` and the push address)
- Modify: `mwan/config/config-vm.toml.j2:31-35` (delete) and `:143-145` (add the
  two push keys)
- Modify: `mwan/config/config-host.toml.j2:36-40` (delete) and `:59` (add
  `status_listen_port`)
- Modify: `proxmox/config/mwan-network.toml.j2:9-12` (delete the loop)
- Modify: `ansible/inventory/group_vars/all/vars.yml` (add
  `mwan_status_push_port`)
- Modify: `ansible/inventory/group_vars/proxmox_servers.yml:40-42` (delete) and
  `ansible/inventory/group_vars/suburban_servers.yml:84-86` (delete)

**Interfaces:**
- Consumes: `netif.ActiveTier(members []netif.TierMember, health netif.HealthStates) (uint8, bool)`
  and `netif.TierMember{Name string; Tier uint8}`, produced by the steering
  part of this plan. `health.WAN.Tier` is filled from `sharedWAN.Tier`, which
  that part adds to `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go`.
- Produces: `statuspush.Status`, `statuspush.Sender`, `statuspush.Listener`,
  `statuspush.DefaultPort`, `statuspush.HostCID`.
- Produces: `config.WatchdogSection.StatusListenPort uint32`
  (`status_listen_port`), `config.IfMgrHealthSection.StatusPushCID uint32`
  (`status_push_cid`) and `.StatusPushPort uint32` (`status_push_port`).
- Produces: the inventory variable `mwan_status_push_port`, whose one home is
  `ansible/inventory/group_vars/all/vars.yml` because the two ends of the
  channel must agree on it.
- Removes: `config.WANInterface`, `config.NetworkConfig.WANInterfaces`,
  `config.NetworkConfig.WanIfaceNames`, `watchdog.testISP`,
  `mwan_watchdog_wan_interfaces`.

- [ ] **Step 1: Write the failing status-channel test**

Create `mwan/go/internal/statuspush/statuspush_test.go`:

```go
package statuspush_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"goodkind.io/mwan/internal/statuspush"
)

// discardLogger keeps test output readable; every assertion below is on
// delivered state, never on a log line.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loopbackPair starts the real Listener on a loopback socket and returns a real
// Sender pointed at it. vsock needs a hypervisor, so the transport is the one
// thing substituted; the framing, the encoding, the accept loop, and the
// latest-status bookkeeping are production code.
func loopbackPair(t *testing.T) (*statuspush.Sender, *statuspush.Listener) {
	t.Helper()

	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	address := socket.Addr().String()

	listener := statuspush.NewListenerWithListen(
		func() (net.Listener, error) { return socket, nil },
		discardLogger(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = listener.Run(ctx)
	}()

	sender := statuspush.NewSenderWithDial(
		func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", address)
		},
		discardLogger(),
	)
	return sender, listener
}

// waitForStatus polls until the listener has recorded a status whose active
// tier matches, or the deadline passes. The push is asynchronous by design, so
// the test waits for the effect rather than sleeping a fixed span.
func waitForStatus(
	t *testing.T,
	listener *statuspush.Listener,
	wantTier uint8,
) (statuspush.Status, time.Time) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, receivedAt, ok := listener.Latest()
		if ok && status.ActiveTier == wantTier {
			return status, receivedAt
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no status with active tier %d arrived within the deadline", wantTier)
	return statuspush.Status{}, time.Time{}
}

func TestListenerHoldsNothingBeforeTheFirstPush(t *testing.T) {
	t.Parallel()

	_, listener := loopbackPair(t)

	if _, _, ok := listener.Latest(); ok {
		t.Fatal("Latest reported a status before any push arrived")
	}
}

func TestRoundTripKeepsTheLatestStatus(t *testing.T) {
	t.Parallel()

	sender, listener := loopbackPair(t)
	sentAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	sender.Send(context.Background(), statuspush.Status{
		SentAt:     sentAt,
		ActiveTier: 0,
		Providers: map[string]string{
			"att": "healthy", "webpass": "healthy", "monkeybrains": "healthy",
		},
	})
	first, _ := waitForStatus(t, listener, 0)
	if got := first.Providers["att"]; got != "healthy" {
		t.Fatalf("att verdict = %q, want healthy", got)
	}
	if !first.SentAt.Equal(sentAt) {
		t.Fatalf("sent_at = %s, want %s", first.SentAt, sentAt)
	}

	sender.Send(context.Background(), statuspush.Status{
		SentAt:     sentAt.Add(10 * time.Second),
		ActiveTier: 1,
		Providers: map[string]string{
			"att": "unhealthy", "webpass": "unhealthy", "monkeybrains": "healthy",
		},
	})
	second, receivedAt := waitForStatus(t, listener, 1)
	if got := second.Providers["att"]; got != "unhealthy" {
		t.Fatalf("att verdict after failover = %q, want unhealthy", got)
	}
	if got := len(second.Providers); got != 3 {
		t.Fatalf("provider count = %d, want 3", got)
	}
	if receivedAt.IsZero() {
		t.Fatal("received time is zero on a status that arrived")
	}
}

func TestMalformedLineLeavesTheLastGoodStatus(t *testing.T) {
	t.Parallel()

	sender, listener := loopbackPair(t)
	sender.Send(context.Background(), statuspush.Status{
		SentAt:     time.Now(),
		ActiveTier: 2,
		Providers:  map[string]string{"astound": "healthy"},
	})
	good, _ := waitForStatus(t, listener, 2)

	conn, err := net.Dial("tcp", listener.Address())
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A rejected line must not clear what the watchdog knows. Give the accept
	// loop time to have processed and discarded it, then re-read.
	time.Sleep(100 * time.Millisecond)
	after, _, ok := listener.Latest()
	if !ok {
		t.Fatal("a malformed line cleared the stored status")
	}
	if after.ActiveTier != good.ActiveTier {
		t.Fatalf("active tier = %d, want the last good %d", after.ActiveTier, good.ActiveTier)
	}
}

func TestSendToNothingIsNotFatal(t *testing.T) {
	t.Parallel()

	// The hypervisor may be running an older watchdog with no listener. That
	// costs one failed dial and nothing else: no panic, no block, no error the
	// probe cycle has to handle.
	sender := statuspush.NewSenderWithDial(
		func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", "127.0.0.1:1")
		},
		discardLogger(),
	)
	done := make(chan struct{})
	go func() {
		sender.Send(context.Background(), statuspush.Status{
			SentAt:     time.Now(),
			ActiveTier: 0,
			Providers:  map[string]string{"att": "healthy"},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked when the receiver was absent")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/statuspush/ -v
```

Expected: FAIL to build, with `no required module provides package
goodkind.io/mwan/internal/statuspush`.

- [ ] **Step 3: Write the status channel**

Create `mwan/go/internal/statuspush/statuspush.go`:

```go
// Package statuspush carries the gateway's provider verdict to the hypervisor
// watchdog. The gateway is the only process that probes every provider, so the
// watchdog is told what it needs rather than made to rediscover it by pinging
// through a list of interface names typed into its own configuration.
//
// One message is one connection: dial, write a single JSON line, close. There
// is no session to resynchronise after a hypervisor restart, and a hypervisor
// running an older watchdog with no listener costs the gateway one failed dial
// per probe cycle and nothing else.
//
// The package carries no build constraint. github.com/mdlayher/vsock compiles
// everywhere and returns an error on a platform without the transport, which is
// what lets internal/ops import it untagged today, and the watchdog that
// imports this package is untagged too.
package statuspush

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mdlayher/vsock"
)

const (
	// DefaultPort is the vsock port the hypervisor watchdog listens on and the
	// gateway dials. It sits beside the agent's 50051 and its TCP fallback
	// 50052, which are the only other ports in this family.
	DefaultPort uint32 = 50053

	// HostCID is VMADDR_CID_HOST, the context id every guest reaches its own
	// hypervisor on. A guest therefore needs no knowledge of which machine it
	// runs on to find the watchdog.
	HostCID uint32 = 2

	// transferTimeout bounds one push and one receive. Both ends are on the
	// same machine over a virtio transport, so a transfer that has not finished
	// in this long is not going to.
	transferTimeout = 2 * time.Second

	// failureLogInterval rate-limits the sender's failure log. A hypervisor
	// with no listener fails every cycle, and at a ten-second cadence that is
	// over eight thousand identical lines a day for one fact the first line
	// already carried.
	failureLogInterval = 5 * time.Minute

	// maxMessageBytes caps one status line. The real message is a few hundred
	// bytes; the cap stops a wedged or hostile writer from growing the read
	// buffer without bound.
	maxMessageBytes = 64 * 1024
)

// Status is what the gateway knows about its providers at one instant. When no
// provider is healthy anywhere, ActiveTier carries no meaning and every entry
// in Providers reads unhealthy, which is what a reader checks.
type Status struct {
	SentAt     time.Time         `json:"sent_at"`
	ActiveTier uint8             `json:"active_tier"`
	Providers  map[string]string `json:"providers"`
}

// DialFunc opens one connection to the receiver. Production dials vsock; a test
// dials a loopback socket, because vsock needs a hypervisor.
type DialFunc func(ctx context.Context) (net.Conn, error)

// ListenFunc opens the socket the Listener accepts on.
type ListenFunc func() (net.Listener, error)

// Sender writes one Status per call over a fresh connection. It reports no
// error: the push is advisory, and a probe cycle that had to handle a delivery
// failure would be a probe cycle whose verdict depended on the hypervisor.
type Sender struct {
	dial DialFunc
	log  *slog.Logger

	mu             sync.Mutex
	lastFailureLog time.Time
	suppressed     int
}

// NewSender returns a Sender that dials the hypervisor at cid on port.
func NewSender(cid uint32, port uint32, log *slog.Logger) *Sender {
	return NewSenderWithDial(func(_ context.Context) (net.Conn, error) {
		conn, err := vsock.Dial(cid, port, nil)
		if err != nil {
			return nil, fmt.Errorf("vsock dial cid %d port %d: %w", cid, port, err)
		}
		return conn, nil
	}, log)
}

// NewSenderWithDial is NewSender with the transport named, so a test drives the
// real encoder and framing over a socket it can create.
func NewSenderWithDial(dial DialFunc, log *slog.Logger) *Sender {
	return &Sender{
		dial:           dial,
		log:            log,
		mu:             sync.Mutex{},
		lastFailureLog: time.Time{},
		suppressed:     0,
	}
}

// Send delivers status, or gives up quietly. Every failure path logs at debug
// under the rate limit and returns.
func (s *Sender) Send(ctx context.Context, status Status) {
	if s == nil || s.dial == nil {
		return
	}
	payload, err := json.Marshal(status)
	if err != nil {
		s.log.WarnContext(ctx, "statuspush: marshal status failed", "err", err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()

	conn, err := s.dial(sendCtx)
	if err != nil {
		s.noteFailure(ctx, "dial", err)
		return
	}
	defer func() {
		_ = conn.Close()
	}()
	deadline, hasDeadline := sendCtx.Deadline()
	if hasDeadline {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			s.noteFailure(ctx, "set write deadline", err)
			return
		}
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		s.noteFailure(ctx, "write", err)
		return
	}
	s.noteSuccess()
}

// noteFailure logs at most one line per failureLogInterval and counts what it
// swallowed, so the next line says how long the channel has been down.
func (s *Sender) noteFailure(ctx context.Context, operation string, err error) {
	s.mu.Lock()
	now := time.Now()
	suppressed := s.suppressed
	report := s.lastFailureLog.IsZero() ||
		now.Sub(s.lastFailureLog) >= failureLogInterval
	if report {
		s.lastFailureLog = now
		s.suppressed = 0
	} else {
		s.suppressed++
	}
	s.mu.Unlock()

	if !report {
		return
	}
	s.log.DebugContext(
		ctx,
		"statuspush: send failed",
		"operation", operation,
		"suppressed_since_last", suppressed,
		"err", err,
	)
}

// noteSuccess clears the failure window, so a later outage logs its first line
// at once rather than waiting out an interval that started before the recovery.
func (s *Sender) noteSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFailureLog = time.Time{}
	s.suppressed = 0
}

// Listener keeps the most recent Status the gateway pushed, with the time it
// arrived. Nothing is queued: a diagnosis wants what is true now, and an older
// status is never the answer.
type Listener struct {
	listen ListenFunc
	log    *slog.Logger

	mu       sync.Mutex
	latest   Status
	received time.Time
	seen     bool
	address  string
}

// NewListener returns a Listener bound to this host's vsock context on port.
func NewListener(port uint32, log *slog.Logger) *Listener {
	return NewListenerWithListen(func() (net.Listener, error) {
		listener, err := vsock.Listen(port, nil)
		if err != nil {
			return nil, fmt.Errorf("vsock listen port %d: %w", port, err)
		}
		return listener, nil
	}, log)
}

// NewListenerWithListen is NewListener with the socket named, so a test drives
// the real accept loop and decoder over a socket it can create.
func NewListenerWithListen(listen ListenFunc, log *slog.Logger) *Listener {
	return &Listener{
		listen:   listen,
		log:      log,
		mu:       sync.Mutex{},
		latest:   Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil},
		received: time.Time{},
		seen:     false,
		address:  "",
	}
}

// Latest returns the last status received, when it arrived, and whether one
// ever has.
func (l *Listener) Latest() (Status, time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latest, l.received, l.seen
}

// Address reports the socket the listener bound, once Run has bound it. It is
// empty before that and on a listener whose socket failed.
func (l *Listener) Address() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.address
}

// Run accepts connections until ctx is cancelled or the socket fails. The
// caller runs it in a goroutine and treats a returned error as the channel
// being down, not as a reason to stop watching connectivity.
func (l *Listener) Run(ctx context.Context) error {
	socket, err := l.listen()
	if err != nil {
		l.log.WarnContext(ctx, "statuspush: listen failed", "err", err)
		return fmt.Errorf("statuspush: listen: %w", err)
	}
	l.mu.Lock()
	l.address = socket.Addr().String()
	l.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = socket.Close()
	}()

	for {
		conn, acceptErr := socket.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("statuspush: listener stopping: %w", ctx.Err())
			}
			l.log.WarnContext(ctx, "statuspush: accept failed", "err", acceptErr)
			return fmt.Errorf("statuspush: accept: %w", acceptErr)
		}
		l.readOne(ctx, conn)
	}
}

// readOne reads one status line and records it. Connections are served one at a
// time on purpose: a push is one short line, and a serial loop means the stored
// status is the last one that arrived rather than whichever goroutine won a
// race. The read deadline keeps a client that connects and says nothing from
// holding the loop.
func (l *Listener) readOne(ctx context.Context, conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()
	if err := conn.SetReadDeadline(time.Now().Add(transferTimeout)); err != nil {
		l.log.WarnContext(ctx, "statuspush: set read deadline failed", "err", err)
		return
	}
	reader := bufio.NewReader(io.LimitReader(conn, maxMessageBytes))
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		l.log.WarnContext(ctx, "statuspush: read status failed", "err", err)
		return
	}
	if len(line) == 0 {
		return
	}
	var status Status
	if err := json.Unmarshal(line, &status); err != nil {
		// A rejected line leaves the stored status alone. The watchdog would
		// rather diagnose against a slightly older verdict than against none.
		l.log.WarnContext(ctx, "statuspush: decode status failed", "err", err)
		return
	}
	l.mu.Lock()
	l.latest = status
	l.received = time.Now()
	l.seen = true
	l.mu.Unlock()

	l.log.DebugContext(
		ctx,
		"statuspush: status received",
		"active_tier", status.ActiveTier,
		"provider_count", len(status.Providers),
	)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/statuspush/ -v
```

Expected: PASS, four tests.

- [ ] **Step 5: Add the configuration fields**

In `mwan/go/internal/config/config.go`, delete lines 16 to 38 entirely: the
`WANInterface` type, the `WANInterfaces` field inside `NetworkConfig`, and the
`WanIfaceNames` method. `NetworkConfig` becomes:

```go
// NetworkConfig holds site-specific topology values. It carries no provider
// list: the gateway pushes its own per-provider verdict to the watchdog, so no
// reader here has to be told which interfaces exist.
type NetworkConfig struct {
	PingTargetIPv4 string   `toml:"ping_target_ipv4"`
	PingTargetIPv6 string   `toml:"ping_target_ipv6"`
	PingTargets    []string `toml:"ping_targets"`
	CurlTarget     string   `toml:"curl_target"`
	LastDeployPath string   `toml:"last_deploy_path"`
	LastChangePath string   `toml:"last_change_path"`
}
```

In `WatchdogSection`, directly after the `MwanAgentTCPAddr` line at `:79`,
insert:

```go
	// StatusListenPort is the vsock port the gateway pushes its provider
	// verdict to. Zero, the default everywhere but the two hypervisors, starts
	// no listener at all.
	StatusListenPort uint32 `toml:"status_listen_port"`
```

In `validateWatchdog`, delete the emptiness check at `:734-736`, so the function
ends:

```go
	if cfg.PVE.TokenID != "" && cfg.PVE.TokenSecret == "" {
		return errors.New("[pve] token_id set but token_secret empty")
	}
	return nil
}
```

In `mwan/go/internal/config/ifmgr_modules.go`, `IfMgrHealthSection` gains the
two push fields. They stay TOML's, beside the two state-file paths, because a
vsock address is host plumbing and not a value in the network tree:

```go
// IfMgrHealthSection keeps the module's two state-file paths and the address it
// pushes its verdict to, all of which stay in TOML, beside the probe timeout and
// the per-provider policy, which come from network.json. The push address is
// not a network value: it names a transport between two processes on one
// machine, so it belongs where state_file belongs.
type IfMgrHealthSection struct {
	StateFile          string                           `toml:"state_file"`
	PersistStateFile   string                           `toml:"persist_state_file"`
	StatusPushCID      uint32                           `toml:"status_push_cid"`
	StatusPushPort     uint32                           `toml:"status_push_port"`
	ProbeTimeoutMillis int                              `toml:"-"`
	WAN                map[string]IfMgrHealthWANSection `toml:"-"`
}
```

- [ ] **Step 6: Write the failing health-module push test**

In `mwan/go/internal/ifmgr/modules/health/health_test.go`, add these imports to
the existing block if absent: `"sync"` and
`"goodkind.io/mwan/internal/statuspush"`. Then add:

```go
// fakeStatusSender records every push so the test asserts on what the module
// actually put on the wire. The real Sender is exercised end to end in the
// statuspush package's own round-trip test; here the question is what the
// health module sends and when.
type fakeStatusSender struct {
	mu   sync.Mutex
	sent []statuspush.Status
}

func (f *fakeStatusSender) Send(_ context.Context, status statuspush.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, status)
}

func (f *fakeStatusSender) all() []statuspush.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statuspush.Status(nil), f.sent...)
}

// TestPushSendsOncePerCycleAndCarriesTheTransition is the contract the watchdog
// depends on: every probe cycle puts one status on the wire, and the cycle that
// flips a provider to unhealthy carries that verdict and the tier that took
// over. Two providers, att alone in tier 0 and monkeybrains alone in tier 1, so
// an att failure moves the active tier.
func TestPushSendsOncePerCycleAndCarriesTheTransition(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pusher := &fakeStatusSender{}
	failing := func(
		_ context.Context, iface string, _ netip.Addr, _ time.Duration,
	) (time.Duration, error) {
		if iface == "enatt0" {
			return 0, errors.New("att is down")
		}
		return time.Millisecond, nil
	}
	noHTTP := func(
		_ context.Context, _ string, _ string, _ time.Duration,
	) (int, error) {
		return 0, errors.New("no http")
	}
	module := &Module{
		BaseModule: ifmgr.NewBaseModule("health"),
		cfg: Config{
			StateFile:         filepath.Join(tempDir, "mwan-health.state"),
			PersistStateFile:  filepath.Join(tempDir, "health-state"),
			TargetsV4:         []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			TargetsV6:         []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
			Timeout:           time.Second,
			Interval:          time.Second,
			PingCount:         1,
			SuccessThreshold:  1,
			FailureThreshold:  1,
			RecoveryThreshold: 1,
			StatusPushCID:     statuspush.HostCID,
			StatusPushPort:    statuspush.DefaultPort,
			WANs: []WAN{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0"}, Tier: 0},
				{WANRef: ifmgr.WANRef{Name: "monkeybrains", Iface: "enmbrains0"}, Tier: 1},
			},
		},
		statuses: map[string]wanStatus{
			"att":          {State: StateHealthy},
			"monkeybrains": {State: StateHealthy},
		},
		probeV4:    failing,
		probeV6:    failing,
		probeHTTP6: noHTTP,
		probeHTTP4: noHTTP,
		pusher:     pusher,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := module.runCycle(context.Background(), log); err != nil {
		t.Fatalf("first runCycle: %v", err)
	}
	if got := len(pusher.all()); got != 1 {
		t.Fatalf("pushes after one cycle = %d, want exactly 1", got)
	}
	first := pusher.all()[0]
	if first.Providers["att"] != string(StateUnhealthy) {
		t.Fatalf("att verdict = %q, want unhealthy", first.Providers["att"])
	}
	if first.Providers["monkeybrains"] != string(StateHealthy) {
		t.Fatalf("monkeybrains verdict = %q, want healthy",
			first.Providers["monkeybrains"])
	}
	if first.ActiveTier != 1 {
		t.Fatalf("active tier = %d, want 1 after att failed", first.ActiveTier)
	}
	if first.SentAt.IsZero() {
		t.Fatal("sent_at is zero")
	}

	// A steady cycle with no transition still reports, because the watchdog
	// reads the age of the last status and a silent gateway must look silent.
	if err := module.runCycle(context.Background(), log); err != nil {
		t.Fatalf("second runCycle: %v", err)
	}
	if got := len(pusher.all()); got != 2 {
		t.Fatalf("pushes after two cycles = %d, want 2", got)
	}
}

// TestPushIsSkippedWhenUnconfigured proves a gateway with no push address does
// not build a sender and does not fail a cycle for the lack of one.
func TestPushIsSkippedWhenUnconfigured(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	passing := func(
		_ context.Context, _ string, _ netip.Addr, _ time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}
	module := &Module{
		BaseModule: ifmgr.NewBaseModule("health"),
		cfg: Config{
			StateFile:         filepath.Join(tempDir, "mwan-health.state"),
			PersistStateFile:  filepath.Join(tempDir, "health-state"),
			TargetsV4:         []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			TargetsV6:         []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
			Timeout:           time.Second,
			Interval:          time.Second,
			PingCount:         1,
			SuccessThreshold:  1,
			FailureThreshold:  1,
			RecoveryThreshold: 1,
			WANs: []WAN{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0"}, Tier: 0},
			},
		},
		statuses: map[string]wanStatus{"att": {State: StateUnknown}},
		probeV4:  passing,
		probeV6:  passing,
		pusher:   nil,
	}

	if err := module.runCycle(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	); err != nil {
		t.Fatalf("runCycle with no pusher: %v", err)
	}
}
```

- [ ] **Step 7: Run the health test to verify it fails**

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 \
  ./internal/ifmgr/modules/health/ -run TestPush -v
```

Expected: FAIL to compile, with `unknown field pusher in struct literal`,
`unknown field Tier`, and `unknown field StatusPushCID`.

- [ ] **Step 8: Add the push to the health module**

In `mwan/go/internal/ifmgr/modules/health/health.go`, add
`"goodkind.io/mwan/internal/statuspush"` to the import block.

Add the tier to `WAN`, after the embedded `WANRef` at `:50`:

```go
// WAN embeds the shared identity and carries optional health-policy overrides
// plus the provider's tier. The tier is here because the module computes the
// active tier for the status it pushes, and it already holds the verdict half
// of that computation.
type WAN struct {
	ifmgr.WANRef
	Tier              uint8
	TargetsV4         []netip.Addr
	TargetsV6         []netip.Addr
	HTTPURLs          []string
	PingCount         int
	SuccessThreshold  int
	FailureThreshold  int
	RecoveryThreshold int
	CheckInterval     time.Duration
}
```

Add the push address to `Config`, after `RecoveryThreshold` at `:122`:

```go
	StatusPushCID     uint32
	StatusPushPort    uint32
```

Add the interface. After the `transition` type at `:147`:

```go
// statusSender is the push side of the status channel. The module depends on
// the interface so a test observes what it sends without opening a socket; the
// production implementation is statuspush.Sender.
type statusSender interface {
	Send(ctx context.Context, status statuspush.Status)
}
```

In `Module`, after `probeHTTP4` at `:168`:

```go
	// pusher delivers the cycle verdict to the hypervisor watchdog. Nil on a
	// host with no push address configured, which is every host but the two
	// gateways.
	pusher statusSender
```

Add a clock helper after `snapshotStatuses` at `:492`:

```go
// now reads the injected clock, falling back to the wall clock for a test that
// builds the struct bare. Init installs the real clock on the daemon path.
func (m *Module) now() time.Time {
	if m.clock == nil {
		return time.Now()
	}
	return m.clock.Now()
}
```

In `runCycle`, change the last three lines from:

```go
	m.publishLiveState(nextStatuses, results)
	m.emitTransitions(ctx, log, transitions)
	return nil
```

to:

```go
	m.publishLiveState(nextStatuses, results)
	m.emitTransitions(ctx, log, transitions)
	// One push per cycle, after the transitions are committed, so a verdict
	// change reaches the watchdog in the cycle that made it and a transition
	// cycle never sends the same thing twice.
	m.pushStatus(ctx, nextStatuses)
	return nil
```

Add `pushStatus` directly after `publishLiveState` ends at `:384`:

```go
// pushStatus tells the hypervisor watchdog what this cycle decided: every
// provider's verdict and the tier now carrying traffic. The watchdog holds no
// provider list of its own, so this message is the whole of what it knows.
func (m *Module) pushStatus(ctx context.Context, statuses map[string]wanStatus) {
	if m.pusher == nil {
		return
	}
	providers := make(map[string]string, len(m.cfg.WANs))
	states := make(netif.HealthStates, len(m.cfg.WANs))
	members := make([]netif.TierMember, 0, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		state := StateUnknown
		if status, ok := statuses[wan.Name]; ok && status.State.Valid() {
			state = status.State
		}
		providers[wan.Name] = string(state)
		states[wan.Name] = string(state)
		members = append(members, netif.TierMember{Name: wan.Name, Tier: wan.Tier})
	}
	// ActiveTier reports false when nothing is healthy. The tier then carries
	// no meaning and the provider map is what says so, with every entry
	// unhealthy, so the message shape stays fixed and the reader checks the map.
	activeTier, _ := netif.ActiveTier(members, states)
	m.pusher.Send(ctx, statuspush.Status{
		SentAt:     m.now(),
		ActiveTier: activeTier,
		Providers:  providers,
	})
}
```

In `mwan/go/internal/ifmgr/modules/health/config.go`, add `"log/slog"` and
`"goodkind.io/mwan/internal/statuspush"` to the imports, and change `New` so the
constructed module carries a sender:

```go
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	healthConfig := Config{
		StateFile:         "",
		PersistStateFile:  "",
		TargetsV4:         nil,
		TargetsV6:         nil,
		HTTPURLs:          nil,
		Timeout:           0,
		Interval:          0,
		PingCount:         0,
		SuccessThreshold:  0,
		FailureThreshold:  0,
		RecoveryThreshold: 0,
		StatusPushCID:     0,
		StatusPushPort:    0,
		WANs:              nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("health: invalid config type %T", cfg)
		}
		healthConfig = typedConfig
	}
	applyDefaults(&healthConfig)
	// A zero port means no watchdog is listening for this host's verdict, which
	// is every host but the two gateways. Building no sender there keeps a
	// pointless dial out of every probe cycle.
	var pusher statusSender
	if healthConfig.StatusPushPort != 0 {
		pusher = statuspush.NewSender(
			healthConfig.StatusPushCID,
			healthConfig.StatusPushPort,
			slog.Default().With("component", "ifmgr", "module", moduleName),
		)
	}
	return &Module{
		BaseModule:       ifmgr.NewBaseModule(moduleName),
		cfg:              healthConfig,
		clock:            nil,
		cycleMu:          sync.Mutex{},
		reconcileMu:      sync.Mutex{},
		reconcilePending: true,
		statuses:         nil,
		lastTransition:   nil,
		probeV4:          netif.Ping4,
		probeV6:          netif.Ping6,
		probeHTTP6:       netif.HTTPCheck6,
		probeHTTP4:       netif.HTTPCheck4,
		pusher:           pusher,
	}, nil
}
```

- [ ] **Step 9: Fill the tier and the push address in the module builder**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go`, `buildHealthConfig` gains
the two config fields and the per-provider tier. `sharedWAN.Tier` is added by
the steering part of this plan and read here.

Change the `health.Config` literal at `:173-186` to:

```go
	cfg := health.Config{
		StateFile:         "",
		PersistStateFile:  "",
		TargetsV4:         nil,
		TargetsV6:         nil,
		HTTPURLs:          nil,
		Timeout:           0,
		Interval:          0,
		PingCount:         0,
		SuccessThreshold:  0,
		FailureThreshold:  0,
		RecoveryThreshold: 0,
		StatusPushCID:     0,
		StatusPushPort:    0,
		WANs:              make([]health.WAN, 0, len(shared.WANs)),
	}
```

Both `health.WAN` literals in that function gain `Tier: wan.Tier,` directly
after the `WANRef` line: the nil-section literal at `:189-199` and the
configured one at `:225-235`. Then, at `:204`, add the push address beside the
state files:

```go
	cfg.StateFile = section.StateFile
	cfg.PersistStateFile = section.PersistStateFile
	// The watchdog's address, not a network value: it names a vsock endpoint on
	// this machine's hypervisor, which is why it comes from TOML beside the
	// state files rather than from the network tree.
	cfg.StatusPushCID = section.StatusPushCID
	cfg.StatusPushPort = section.StatusPushPort
```

- [ ] **Step 10: Run the health test to verify it passes**

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 \
  ./internal/ifmgr/modules/health/ ./cmd/mwan/ -v
```

Expected: PASS. `TestPushSendsOncePerCycleAndCarriesTheTransition` and
`TestPushIsSkippedWhenUnconfigured` are green, and the existing health and
module-builder tests stay green.

- [ ] **Step 11: Write the failing watchdog diagnosis test**

In `mwan/go/internal/watchdog/watchdog_test.go`, first delete the
`WANInterfaces` field from `testNC` at `:343-346`, so it reads:

```go
func testNC() config.NetworkConfig {
	return config.NetworkConfig{
		PingTargetIPv4: "1.1.1.1",
		PingTargetIPv6: "2606:4700:4700::1111",
		PingTargets:    []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
		CurlTarget:     "https://ifconfig.co/ip",
		LastDeployPath: "/var/lib/mwan/last-deploy",
		LastChangePath: "/var/run/mwan-last-change",
	}
}
```

Then add this test, with `"bytes"`, `"strings"`, and
`"goodkind.io/mwan/internal/statuspush"` added to the file's imports if absent:

```go
// fixedStatus is a gateway verdict already received. The listener's own
// round-trip is proven in the statuspush package; here the question is what a
// diagnosis does with a verdict it holds.
type fixedStatus struct {
	status     statuspush.Status
	receivedAt time.Time
	seen       bool
}

func (f fixedStatus) Latest() (statuspush.Status, time.Time, bool) {
	return f.status, f.receivedAt, f.seen
}

// TestDiagnosisLogsThePushedStatus is what replaces the per-interface pings: a
// diagnosis reports every provider's verdict and the age of that report, from
// what the gateway pushed, and it names AT&T, which the hand-typed interface
// list never did.
func TestDiagnosisLogsThePushedStatus(t *testing.T) {
	var logged bytes.Buffer
	mock := &mockOps{}
	w := newTestWatchdog(t, mock)
	w.log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	now := time.Date(2026, 9, 2, 12, 0, 30, 0, time.UTC)
	w.nowFn = func() time.Time { return now }
	w.status = fixedStatus{
		status: statuspush.Status{
			SentAt:     now.Add(-20 * time.Second),
			ActiveTier: 1,
			Providers: map[string]string{
				"att":          "unhealthy",
				"webpass":      "unhealthy",
				"monkeybrains": "healthy",
			},
		},
		receivedAt: now.Add(-20 * time.Second),
		seen:       true,
	}

	w.logPushedStatus(context.Background())

	output := logged.String()
	for _, want := range []string{
		"Gateway provider status",
		"att=unhealthy",
		"webpass=unhealthy",
		"monkeybrains=healthy",
		"active_tier=1",
		"age=20s",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diagnosis log missing %q\nfull log:\n%s", want, output)
		}
	}
	if len(mock.guestCalls) != 0 {
		t.Fatalf("diagnosis ran %d guest-exec probes, want 0", len(mock.guestCalls))
	}
}

// TestDiagnosisSaysSoWithNoPushedStatus keeps the silent case legible: a
// gateway that has never pushed must read as silent, not as healthy.
func TestDiagnosisSaysSoWithNoPushedStatus(t *testing.T) {
	var logged bytes.Buffer
	w := newTestWatchdog(t, &mockOps{})
	w.log = slog.New(slog.NewTextHandler(&logged, nil))
	w.status = fixedStatus{
		status:     statuspush.Status{},
		receivedAt: time.Time{},
		seen:       false,
	}

	w.logPushedStatus(context.Background())

	if !strings.Contains(logged.String(), "No gateway status received yet") {
		t.Fatalf("silent gateway not reported\nfull log:\n%s", logged.String())
	}
}
```

- [ ] **Step 12: Run the watchdog test to verify it fails**

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/watchdog/ -run TestDiagnosis -v
```

Expected: FAIL to compile, with `w.status undefined` and
`w.logPushedStatus undefined`.

- [ ] **Step 13: Delete testISP and read the pushed status instead**

In `mwan/go/internal/watchdog/probe.go`, delete lines 158 to 211 entirely: the
`testISP` doc comment and method. The `strings` import stays; `guestExecProbe`
at `:14-42` still uses it.

In `mwan/go/internal/watchdog/watchdog.go`, add
`"goodkind.io/mwan/internal/statuspush"` to the imports, and add the source
interface directly above the `watchdog` struct at `:36`:

```go
// statusSource is the gateway verdict a diagnosis reads. The watchdog depends
// on the interface so a test seeds a verdict without a socket; the production
// implementation is the statuspush listener run starts.
type statusSource interface {
	Latest() (statuspush.Status, time.Time, bool)
}
```

Add the field to the struct, after `tracker` at `:62`:

```go
	// status holds the gateway's last pushed provider verdict. Nil on a host
	// with no listener port configured, which is every host but the two
	// hypervisors.
	status statusSource
```

In `newWatchdog`, add the zero value after `tracker:  nil,` at `:122`:

```go
		status:   nil,
```

In `run`, start the listener before the startup checks so a status can arrive
while they run. After `w.lastHeartbeat = w.now()` at `:793` and before
`iteration := 0`:

```go
	// The gateway pushes its provider verdict here. A listener that cannot bind
	// leaves w.status reporting nothing received, which a diagnosis says out
	// loud; it never fabricates a verdict and never stops the connectivity loop.
	if w.cfg.Watchdog.StatusListenPort != 0 {
		statusListener := statuspush.NewListener(
			w.cfg.Watchdog.StatusListenPort,
			w.log.With("component", "statuspush"),
		)
		w.status = statusListener
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.ErrorContext(ctx, "status listener panic",
						"err", fmt.Errorf("panic: %v", recovered))
				}
			}()
			if err := statusListener.Run(ctx); err != nil {
				log.WarnContext(ctx, "status listener stopped", "err", err)
			}
		}()
	}
```

In `mwan/go/internal/watchdog/diagnosis.go`, replace the `w.testISP(ctx)` call
at `:65` with `w.logPushedStatus(ctx)`, and add the method directly above
`diagnoseNoRecentChange`:

```go
// logPushedStatus records what the gateway last said about its providers and
// how old that report is. It replaces the per-interface pings this diagnosis
// used to run through guest-exec from a hand-typed interface list: the gateway
// probes every provider on its own cadence with hysteresis, its verdict is
// fresher, and it covers every provider rather than the two the list named.
func (w *watchdog) logPushedStatus(ctx context.Context) {
	log := w.tracedLogger(ctx)
	if w.status == nil {
		log.InfoContext(ctx, "No gateway status listener configured")
		w.appendProbe("Gateway status: no listener configured")
		return
	}
	status, receivedAt, ok := w.status.Latest()
	if !ok {
		log.InfoContext(ctx, "No gateway status received yet")
		w.appendProbe("Gateway status: none received")
		return
	}
	names := make([]string, 0, len(status.Providers))
	for name := range status.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	verdicts := make([]string, 0, len(names))
	for _, name := range names {
		verdicts = append(verdicts, name+"="+status.Providers[name])
	}
	joined := strings.Join(verdicts, " ")
	age := w.now().Sub(receivedAt).Round(time.Second)
	log.InfoContext(ctx,
		"Gateway provider status",
		"active_tier", status.ActiveTier,
		"providers", joined,
		"sent_at", status.SentAt.Format(time.RFC3339),
		"age", age,
	)
	w.appendProbe(fmt.Sprintf(
		"Gateway status (age %s): active tier %d, %s",
		age, status.ActiveTier, joined,
	))
}
```

Add `"sort"` and `"strings"` to `diagnosis.go`'s import block; it already
imports `context`, `fmt`, and `time`.

In `mwan/go/internal/watchdog/startup.go`, delete the `wan_interfaces` key and
value from both log calls, at `:29` and `:98`. The `strings` import stays;
`:69` still uses `strings.TrimSpace`.

- [ ] **Step 14: Run the watchdog and config tests to verify they pass**

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/watchdog/ ./internal/config/ -v
```

Expected: PASS. If a config test asserts on the old
`[network] wan_interfaces must not be empty` error, delete that case: the
behavior it pins is gone.

- [ ] **Step 15: Add the port to the shared inventory**

In `ansible/inventory/group_vars/all/vars.yml`, add:

```yaml
# The vsock port the gateway pushes its provider verdict to and the hypervisor
# watchdog listens on. One variable because the two ends must agree, and both
# templates read it here. It sits beside the agent's 50051 and its TCP fallback
# 50052, which are the only other ports in this family.
mwan_status_push_port: 50053
```

- [ ] **Step 16: Drop the interface lists from the two hypervisor groups**

In `ansible/inventory/group_vars/proxmox_servers.yml`, delete lines 40 to 42:

```yaml
mwan_watchdog_wan_interfaces:
  - "enwebpass0"
  - "enmbrains0"
```

In `ansible/inventory/group_vars/suburban_servers.yml`, delete lines 84 to 86,
which are the same three lines.

- [ ] **Step 17: Drop the interface blocks from the three templates**

In `mwan/config/config-vm.toml.j2`, delete lines 30 to 36, the blank line and
the two `[[network.wan_interfaces]]` blocks, so line 29
(`last_change_path = ...`) is followed by a blank line and then `[watchdog]`.

In `mwan/config/config-host.toml.j2`, delete lines 35 to 41, the same shape, so
line 34 is followed by a blank line and then `[watchdog]`.

In `proxmox/config/mwan-network.toml.j2`, delete lines 8 to 12, the blank line
and the whole `for` loop, so the file is:

```jinja
# Generated by Ansible
# MWAN watchdog network topology config
# Consumed by /usr/local/bin/mwan watchdog (Go binary)

ping_target_ipv4 = "{{ mwan_watchdog_ping_target_ipv4 }}"
ping_target_ipv6 = "{{ mwan_watchdog_ping_target_ipv6 }}"
last_deploy_path = "{{ mwan_watchdog_last_deploy_path }}"
```

- [ ] **Step 18: Render the two ends of the channel**

In `mwan/config/config-host.toml.j2`, directly after `mwan_agent_tcp_addr` at
`:59`, insert:

```jinja
# The gateway pushes its per-provider verdict here. This hypervisor's watchdog
# is the only listener; a zero port would start none.
status_listen_port = {{ mwan_status_push_port }}
```

In `mwan/config/config-vm.toml.j2`, extend the `[ifmgr.modules.health]` block at
`:143-145` to:

```jinja
[ifmgr.modules.health]
state_file = "{{ mwan_ifmgr_wan_health_state_file }}"
persist_state_file = "/var/lib/mwan/health-state"
# Push the per-provider verdict to the hypervisor watchdog. Context id 2 is
# VMADDR_CID_HOST, the kernel's fixed id for a guest's own hypervisor, so it is
# a literal rather than an inventory value: no site can change it.
status_push_cid = 2
status_push_port = {{ mwan_status_push_port }}
```

- [ ] **Step 19: Verify the plays still parse**

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:all`
Expected: PASS, exit 0.

- [ ] **Step 20: Run the full gates**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make check`
Expected: PASS, exit 0.

Run: `cd "$(git rev-parse --show-toplevel)" && make check`
Expected: PASS, exit 0.

- [ ] **Step 21: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/go/internal/statuspush mwan/go/internal/config/config.go mwan/go/internal/config/ifmgr_modules.go mwan/go/internal/watchdog mwan/go/internal/ifmgr/modules/health mwan/go/cmd/mwan/ifmgr_module_configs_linux.go mwan/config/config-vm.toml.j2 mwan/config/config-host.toml.j2 proxmox/config/mwan-network.toml.j2 ansible/inventory/group_vars/all/vars.yml ansible/inventory/group_vars/proxmox_servers.yml ansible/inventory/group_vars/suburban_servers.yml
git commit -S -m "Push the gateway provider verdict to the watchdog and delete its interface list" -m "Add internal/statuspush, one JSON line per probe cycle over vsock from the gateway health module to the hypervisor watchdog; log the verdict and its age in the watchdog's diagnosis; and remove testISP, NetworkConfig.WANInterfaces, WanIfaceNames, the wan_interfaces blocks in the three daemon templates, and mwan_watchdog_wan_interfaces from both hypervisor groups, so no reader holds a hand-typed provider list." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: the fourth testbed simulator, astound

The testbed grows a fourth simulated ISP so a fourth provider can be added,
re-tiered, and removed against real hardware. Nothing here is production, and
nothing here is a Go change: the proof this simulator exists to serve is Task
9, run with the binary unchanged.

The simulator machinery is already generic. `ansible/playbooks/deploy-testbed.yml`
loops `testbed_isp_lxcs` at `:239`, `:250`, and `:257`, and the four templates it
renders per entry read only that entry's fields, so a fourth entry needs no new
template: `testbed/isp-lxc/kea-dhcp6.conf.j2` builds its prefix pool from
`isp.pd_prefix` and `isp.pd_len`, `testbed/isp-lxc/nftables.conf.j2` masquerades
`isp.v4_subnet` and `isp.pd_prefix`, and `testbed/isp-lxc/pd-route.service.j2`
routes the delegation back via `isp.mwan_vm_ll`.

Delegation is not keyed to an identity. With `ia_na: false` the DHCPv6 subnet is
`fe80::/10` with a prefix pool, so the sim delegates to whichever client asks.
That is why the gateway's astound network file carries no DUID and no new DUID
variable is added.

**Hot-adding the interface is safe.** The link files match on MAC address, so
the sixth virtio NIC comes up as `enastound0` whenever the kernel sees it,
whatever order the others enumerate in. The deploy that adds it reboots the
gateway anyway, so the naming is settled before any provider entry references
the interface.

**Files:**
- Modify: `ansible/inventory/group_vars/all/service_mapping.yml:288` (insert the
  `isp_astound_suburban` entry after the Monkeybrains entry)
- Modify: `opentofu/suburban/networks.tf:62` (insert the bridge after the
  Monkeybrains bridge)
- Modify: `opentofu/suburban/containers.tf:372` (insert the container after the
  Monkeybrains container)
- Modify: `opentofu/suburban/vms.tf:19` and `:95` (the dependency and the sixth
  network device)
- Modify: `ansible/inventory/group_vars/suburban_servers.yml:114` (deny RA on
  the new bridge) and `:230` (the fourth `testbed_isp_lxcs` entry)
- Modify: `ansible/inventory/group_vars/mwan_suburban_servers.yml:39`, `:50`,
  `:58`, `:87` (interface name, networkd files, MAC, addresses)
- Create: `mwan/networkd/30-astound.link.j2`
- Create: `mwan/networkd/30-astound.network.j2`

**Interfaces:**
- Consumes: nothing from the other tasks. This task runs on its own and leaves
  the gateway's provider set at three.
- Produces: guest 903 on bridge vmbr7 serving DHCPv4 with a reservation, plus
  DHCPv6 prefix delegation of `3d06:bad:b01:2500::/56`, reachable from the
  gateway on `enastound0`.
- Produces: the hardware variables `mwan_astound_iface`, `mwan_astound_mac`,
  `mwan_astound_ipv4`, `mwan_astound_gateway`, which the provider entry Task 9
  adds references by Jinja.

- [ ] **Step 1: Add the simulator to the service map**

In `ansible/inventory/group_vars/all/service_mapping.yml`, directly after the
`isp_mbrains_suburban` block ending at `:288` and before the blank line at
`:289`, insert:

```yaml
  isp_astound_suburban:
    hostname: isp-astound.suburban.goodkind.io
    vmid: 903
    inventory: false
    ipv4: "10.240.207.1"
    ipv4_uplink: "10.240.200.93"
    ipv6_uplink: "3d06:bad:b01:200::93"
```

The comment block above the three existing entries at `:255-266` already covers
this one: the 9xx range, an uplink suffix below 100, and no place in the SSH
inventory.

- [ ] **Step 2: Add the bridge**

In `opentofu/suburban/networks.tf`, directly after the `isp_mbrains_suburban`
bridge ending at `:62`, insert:

```hcl
resource "proxmox_network_linux_bridge" "isp_astound_suburban" {
  node_name = "hypervisor"
  name      = "vmbr7"

  autostart = true

  lifecycle {
    prevent_destroy = true
  }
}
```

vmbr7 is the next free name: vmbr0 is the uplink, vmbr1 management, vmbr2 the
MWAN internal link, vmbr4 through vmbr6 the three existing simulators, and
vmbrtrunk the VLAN trunk.

- [ ] **Step 3: Add the container**

In `opentofu/suburban/containers.tf`, directly after the
`isp_mbrains_suburban` container ending at `:372`, insert:

```hcl
resource "proxmox_virtual_environment_container" "isp_astound_suburban" {
  node_name = "hypervisor"
  vm_id     = local.service_mapping.isp_astound_suburban.vmid

  depends_on = [
    proxmox_network_linux_bridge.isp_astound_suburban,
    proxmox_network_linux_bridge.vm_management_suburban,
  ]

  initialization {
    hostname = local.service_mapping.isp_astound_suburban.hostname
    dns {
      servers = ["2606:4700:4700::1111", "1.1.1.1"]
    }
    # The simulated link carries IPv4 and link-local only. This sim hands out no
    # IA_NA address and advertises no SLAAC prefix, so prefix delegation runs
    # over link-local exactly as the AT&T sim's does.
    ip_config {
      ipv4 {
        address = "${local.service_mapping.isp_astound_suburban.ipv4}/24"
      }
    }
    ip_config {
      ipv4 {
        address = "${local.service_mapping.isp_astound_suburban.ipv4_uplink}/24"
        gateway = local.service_mapping.vmbr1_suburban.ipv4
      }
      ipv6 {
        address = "${local.service_mapping.isp_astound_suburban.ipv6_uplink}/64"
        gateway = local.service_mapping.vmbr1_suburban.ipv6
      }
    }
  }

  features {
    nesting = true
  }

  network_interface {
    name        = "eth0"
    bridge      = proxmox_network_linux_bridge.isp_astound_suburban.name
    mac_address = "BC:24:11:A5:70:04"
  }

  network_interface {
    name        = "eth1"
    bridge      = proxmox_network_linux_bridge.vm_management_suburban.name
    mac_address = "BC:24:11:A5:70:05"
  }

  disk {
    datastore_id = "local-zfs"
    size         = 2
  }

  memory {
    dedicated = 128
    swap      = 512
  }

  cpu {
    architecture = "amd64"
    cores        = 1
    limit        = 0
  }

  console {
    enabled   = true
    tty_count = 2
    type      = "tty"
  }

  tags = []

  operating_system {
    template_file_id = "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst"
    type             = "debian"
  }

  started       = true
  start_on_boot = true
  unprivileged  = false

  lifecycle {
    prevent_destroy = true
    ignore_changes = [
      operating_system[0].template_file_id,
    ]
  }
}
```

- [ ] **Step 4: Give the gateway VM its sixth interface**

In `opentofu/suburban/vms.tf`, add the bridge to the dependency list. After
`proxmox_network_linux_bridge.isp_mbrains_suburban,` at `:19`, insert:

```hcl
    proxmox_network_linux_bridge.isp_astound_suburban,
```

Then, after the Monkeybrains `network_device` block ending at `:95` and before
the comment at `:97`, insert:

```hcl
  network_device {
    bridge      = proxmox_network_linux_bridge.isp_astound_suburban.name
    model       = "virtio"
    mac_address = "BC:24:11:A5:70:06"
  }
```

- [ ] **Step 5: Deny router advertisements on the new bridge**

In `ansible/inventory/group_vars/suburban_servers.yml`, in
`mwan_ifmgr_host_ipv6_denied_ra_ifaces`, add after `- "vmbr6"` at `:114`:

```yaml
  - "vmbr7"
```

The simulator advertises on its own segment. The hypervisor must not accept
those advertisements, exactly as it does not accept the other three simulators'.

- [ ] **Step 6: Add the simulator's capability entry**

In `ansible/inventory/group_vars/suburban_servers.yml`, directly after the
`mbrains` entry ending at `:230`, insert:

```yaml
  - id: "{{ service_mapping.isp_astound_suburban.vmid }}"
    name: astound
    # A fourth provider that exists only to prove one can be added. It models a
    # plain dynamic link: DHCPv4 pinned by a MAC reservation and a DHCPv6-PD
    # /56, with no routed static block and no SLAAC, so nothing about it depends
    # on a real ISP's quirks.
    pd_prefix: "3d06:bad:b01:2500::/56"
    pd_len: 56
    v4_subnet: "10.240.207.0/24"
    mwan_vm_ll: "fe80::be24:11ff:fea5:7006"
    dynamic_v4: true
    v4_gateway: "10.240.207.1"
    v4_pool: "10.240.207.100 - 10.240.207.200"
    v4_dns: "1.1.1.1"
    ia_na: false
    slaac_prefix: ""
    v4_reservations:
      - { mac: "BC:24:11:A5:70:06", addr: "10.240.207.2" }
    static_v4_block: ""
    static_v4_route_to: ""
```

`mwan_vm_ll` is the EUI-64 link-local of `BC:24:11:A5:70:06`: the universal bit
of the first octet flips, giving `be:24:11`, and `ff:fe` is inserted in the
middle, giving `fe80::be24:11ff:fea5:7006`. That matches how the three existing
entries derive theirs from their own MACs.

- [ ] **Step 7: Add the gateway's hardware variables and link files**

In `ansible/inventory/group_vars/mwan_suburban_servers.yml`, after
`mwan_monkeybrains_iface` at `:39`, insert:

```yaml
mwan_astound_iface: "enastound0"
```

After `- 30-monkeybrains.network` at `:50`, and before the mwanbr pair, insert:

```yaml
  - 30-astound.link
  - 30-astound.network
```

After `mwan_mbrains_mac` at `:58`, insert:

```yaml
mwan_astound_mac: "BC:24:11:A5:70:06"
```

After `mwan_monkeybrains_gateway` at `:87`, insert:

```yaml
mwan_astound_ipv4: "10.240.207.2/24"
mwan_astound_gateway: "10.240.207.1"
```

The address pair mirrors the Monkeybrains pair at `:86-87`, which is likewise
declared beside a link that takes its address by DHCP. It records what the sim's
reservation hands out, so an operator reading inventory sees the address without
opening the simulator's lease pool.

Create `mwan/networkd/30-astound.link.j2`:

```jinja
# Astound WAN Interface (virtio, testbed only)
# Generated by Ansible
# Matches on virtio MAC address from Proxmox config, assigns stable name.

[Match]
MACAddress={{ mwan_astound_mac | lower }}

[Link]
Name={{ mwan_astound_iface }}
```

Create `mwan/networkd/30-astound.network.j2`:

```jinja
# Astound WAN Interface (virtio, testbed only)
# Generated by Ansible

[Match]
Name={{ mwan_astound_iface }}

[Network]
# A dynamic link: DHCPv4 for the address and DHCPv6 for the delegation.
DHCP=yes
IPv6AcceptRA=yes

# Needed for policy routing / forwarding
IPv4Forwarding=yes
IPv6Forwarding=yes

[DHCPv4]
# The least preferred provider on the testbed, below monkeybrains at 5000, so a
# kernel default route from this link never outranks one from another.
RouteMetric=6000

[DHCPv6]
# Request prefix delegation so the daemon can translate the internal /60 onto
# this provider. The simulator delegates from a /56 pool to whichever client
# asks, so no DUID is configured here and none is needed.
UseDelegatedPrefix=yes
PrefixDelegationHint=::/56
WithoutRA=solicit

[IPv6PrefixDelegation]
RouterLifetimeSec=1800

[IPv6AcceptRA]
# Matches the DHCPv4 metric: least preferred of the four.
RouteMetric=6000
```

- [ ] **Step 8: Verify the plays parse**

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:all`
Expected: PASS, exit 0.

- [ ] **Step 9: Verify the tofu configuration and read the plan**

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl tofu validate
./configsctl tofu plan -target=module.suburban
```

Expected: `validate` reports success. The plan shows exactly three changes:
`module.suburban.proxmox_network_linux_bridge.isp_astound_suburban` added,
`module.suburban.proxmox_virtual_environment_container.isp_astound_suburban`
added, and one in-place update of
`module.suburban.proxmox_virtual_environment_vm.mwan_suburban` adding the sixth
`network_device`. Read the plan before the next step and stop if it proposes to
destroy or replace anything, in particular the gateway VM: every guest carries
`prevent_destroy`, so a replacement proposal is a bug in the change rather than
a prompt to approve.

- [ ] **Step 10: Commit before applying**

```bash
cd "$(git rev-parse --show-toplevel)"
git add ansible/inventory/group_vars/all/service_mapping.yml ansible/inventory/group_vars/suburban_servers.yml ansible/inventory/group_vars/mwan_suburban_servers.yml opentofu/suburban/networks.tf opentofu/suburban/containers.tf opentofu/suburban/vms.tf mwan/networkd/30-astound.link.j2 mwan/networkd/30-astound.network.j2
git commit -S -m "Add the astound ISP simulator to the testbed" -m "Create guest 903 on a new vmbr7 bridge serving DHCPv4 with a MAC reservation and a DHCPv6-PD /56, give the testbed gateway a sixth virtio NIC on that bridge with link files that name it enastound0, and deny router advertisements from the new bridge on the hypervisor." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

- [ ] **Step 11: Apply the infrastructure change**

Operator-run. The apply prompts for approval and keeps the terminal.

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl tofu apply -target=module.suburban
```

Expected: `Apply complete! Resources: 2 added, 1 changed, 0 destroyed.` The
target flag is not optional here: an untargeted apply reaches the production
module as well.

- [ ] **Step 12: Confirm the guest and the interface exist**

Resolve the guest id from `service_mapping.isp_astound_suburban.vmid` rather
than typing it, and export it for every command that follows:

```bash
cd "$(git rev-parse --show-toplevel)"
SIM_ID=$(awk '/^  isp_astound_suburban:/{found=1} found && /vmid:/{print $2; exit}' \
  ansible/inventory/group_vars/all/service_mapping.yml)
echo "$SIM_ID"
ssh suburban "pct status $SIM_ID; pct config $SIM_ID | grep -E '^net[01]:'"
ssh suburban 'qm config 213 | grep -E "^net[0-9]:"'
```

Expected: `SIM_ID` prints `903`. The guest is `status: running`, its `net0` is
on vmbr7 with MAC `BC:24:11:A5:70:04` and its `net1` on vmbr1 with
`BC:24:11:A5:70:05`. The gateway VM lists six network devices, the sixth on
vmbr7 with `BC:24:11:A5:70:06`.

- [ ] **Step 13: Provision the simulator**

Operator-run. This is a testbed deploy and needs no release tag, because it
installs no mwan binary.

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-testbed --limit suburban_servers
```

Expected: the play passes. "Install packages on ISP LXCs", "Install kea-dhcp4 on
dynamic-v4 ISP LXCs", and "Render and push ISP LXC configs" each run four times
now rather than three.

- [ ] **Step 14: Confirm the simulator serves**

```bash
ssh suburban "pct exec $SIM_ID '--' systemctl is-active kea-dhcp6-server kea-dhcp4-server radvd nftables pd-route"
ssh suburban "pct exec $SIM_ID '--' ip -4 addr show dev eth0"
ssh suburban "pct exec $SIM_ID '--' nft list ruleset"
```

Expected: all five units report `active`. eth0 holds `10.240.207.1/24`. The
ruleset masquerades `10.240.207.0/24` and `3d06:bad:b01:2500::/56` out eth1, and
carries no static-block rule, because this simulator has none.

- [ ] **Step 15: Bring the gateway's interface up**

Operator-run. This deploy reboots the gateway; announce the window first, as
Task 9's announcement step describes.

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
```

Expected: the play passes and reports its reboot window. The link files land, so
after the reboot the sixth NIC is named by MAC rather than by enumeration order.

- [ ] **Step 16: Confirm the gateway holds the link**

```bash
ssh mwan.suburban.goodkind.io 'ip -brief addr show dev enastound0'
ssh mwan.suburban.goodkind.io 'networkctl status enastound0'
ssh mwan.suburban.goodkind.io 'ip -6 route show | grep -i 2500 || echo "no delegation route yet"'
```

Expected: `enastound0` is `UP` and holds `10.240.207.2/24` from the reservation,
`networkctl` shows it configured with a DHCPv6 delegated prefix inside
`3d06:bad:b01:2500::/56`, and no provider route exists yet, because no provider
entry names it. That last point is the state Task 9 starts from: the interface
is live and the daemon is unaware of it.

---

---

## What the cutover capture must show

- `/etc/mwan/network.json` gains a steering container per provider, a hash mode,
  and a reserved-table list. Every other leaf is byte-identical.
- `/etc/iproute2/rt_tables` gains one line, naming table 500 as `oob`.
- `/etc/nftables.conf` loses the three balancing lines and the three interface
  defines, gains one masquerade rule on the monkeybrains link that duplicates
  the fate of the rule below it, and otherwise keeps every rule in place.
- `/etc/mwan/mwan.env` loses seven keys no consumer reads.
- `/etc/mwan/config.toml` is byte-identical on both gateways. The only edited
  line is gated on an out-of-band role neither gateway runs, and the reserved
  registry renders the same 500 the literal did.
- The three hypervisor policy rules render the same table id they render today.

Three claims in Task 4 are proven against the library source and the kernel's
documented register model but not against a running kernel. The cutover proof
(contract decision 16) must read them back on the testbed before production:

1. `nft list table inet mwan_steer` renders the chain at
   `type filter hook prerouting priority -149; policy accept;` with three rules
   whose text matches the three lines that left `nftables.conf.j2`.
2. The map in the rendered rule reads `{ 0 : 1, 1 : 2 }` rather than a
   byte-swapped pair, which is what proves the native byte order chosen for the
   slot keys and the mark values is right.
3. With `mwan_hash_mode` set to `source` and then to `source-destination` on the
   testbed, the rendered rule reads `jhash ip saddr mod 2` and
   `jhash ip6 saddr . ip6 daddr mod 2`, which is what proves the 32-bit register
   layout the concatenated hash relies on. Production ships `random`, so a
   failure here does not block the cutover.

If (2) or (3) reads wrong, the fix is in `slotMapSet` and `hashExprs` in the
applier and nowhere else, and the rules test asserting the element bytes is the
test that changes with it.

---

### Task 9: the cutover runbook

No code. The controlling session executes this task, not a subagent: it runs
deploys and production commands, and every production command needs the
operator's explicit approval immediately before it runs.

One release tag carries every slice of MWAN-324, so each environment cuts over
in a single `deploy-mwan` run. The evidence set is MWAN-339's, with one mapped
difference and one added proof.

**The one mapped difference.** Three balancing lines leave
`mwan/config/nftables.conf.j2` and appear in the steering module's own kernel
table. Before the cutover they are `mwan/config/nftables.conf.j2:107` in
`table ip nat chain prerouting`, and `:223` and `:226` in
`table inet mangle chain prerouting`. After it they are three rules in
`table inet mwan_steer chain prerouting`. Every other rule in the ruleset is
byte-identical.

**The added proof.** On the testbed only, with the binary unchanged, astound is
added by inventory edit, proven to carry traffic in both families, moved into
tier 0 and proven to join the split, then removed and proven to leave no trace.

Three shell values recur. `TAG` is the release tag the operator names. `MGMT` is
the gateway's management address for the environment in hand, from
`mwan_config_mgmt_addr` in that environment's group vars. `SIM_ID` is the
resolved guest id of the simulator being observed, from `testbed_isp_lxcs`.

**Announcements.** Every gateway reboot window is announced to the audit-ops,
backup-reach, and prod-outage peer sessions before it opens, and reported to
them after it closes. That is separate from the watchdog's thirty-minute deploy
window, which governs the OPNsense router and is not what these deploys reboot.

**Files:** none.

**Interfaces:**
- Consumes: every artifact of MWAN-324, including Tasks 7 and 8.
- Produces: the recorded before-and-after evidence pinning the cutover, and the
  add, re-tier, and remove evidence proving acceptance.

#### Testbed

- [ ] **Step 1: Announce the testbed window**

Send to the audit-ops, backup-reach, and prod-outage peer sessions: the testbed
gateway reboots during a MWAN-324 deploy, the expected window is the deploy
gate's, and testbed egress drops for its duration. Wait for acknowledgement
before the deploy in step 4.

- [ ] **Step 2: Capture the testbed before snapshot**

```bash
MGMT=3d06:bad:b01:210::213
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-interfaces:interfaces" > /tmp/before-tree.json
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-nat:nat" > /tmp/before-nat.json
ssh mwan.suburban.goodkind.io 'nft list ruleset' > /tmp/before-nft.txt
ssh mwan.suburban.goodkind.io 'cat /etc/nftables.conf' > /tmp/before-nftconf.txt
ssh mwan.suburban.goodkind.io 'ip -6 rule show; ip rule show' > /tmp/before-rules.txt
ssh mwan.suburban.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; ip route show table $t; done' > /tmp/before-routes.txt
ssh mwan.suburban.goodkind.io 'for m in 1 2 3; do echo "== mark $m"; ip route get 1.1.1.1 mark $m; ip -6 route get 2606:4700:4700::1111 mark $m; done' > /tmp/before-marks.txt
```

Expected: `before-nft.txt` holds no `mwan_steer` table, and `before-nftconf.txt`
holds all three balancer lines.

- [ ] **Step 3: Run the traffic matrix as the before run**

For each provider (att, webpass, monkeybrains) and each address family, from an
internal client behind the testbed router, force egress onto that provider with
the DSCP mark the router stamps, and observe at that provider's simulator
ingress, because the simulators masquerade outbound and anything seen past one
proves nothing:

```bash
ssh suburban "pct exec $SIM_ID '--' timeout 30 tcpdump -ni any -c 20 'ip6 or ip'"
```

Record, per provider and family: the source address seen at the simulator, that
it is the provider's translated prefix or its pinned IPv4 source, and that the
reply returns on the same provider. Then record the spread of new connections
across tier 0's members, observed rather than assumed; a fallback drill that
forces tier 0 unhealthy and shows traffic exiting monkeybrains translated in
both families, then recovering; that a pinned destination exits AT&T; and that
the inbound translation paths reach their internal targets.

- [ ] **Step 4: Cut the testbed over**

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
```

Expected: the play passes, including "Validate the rendered network
configuration against the schema", and reports its reboot window. Report the
window to the three peer sessions.

- [ ] **Step 5: Confirm the daemon started and owns the steering table**

```bash
ssh mwan.suburban.goodkind.io 'systemctl status mwan-ifmgr@wan --no-pager'
ssh mwan.suburban.goodkind.io 'journalctl -u mwan-ifmgr@wan -n 120 --no-pager'
ssh mwan.suburban.goodkind.io 'nft list table inet mwan_steer'
```

Expected: the unit is active, the journal carries no "network configuration
unusable" line and no validation failure, and the steering table exists with a
`prerouting` chain and three rules. A daemon that stopped at load is the failure
this cutover is most exposed to, because the new checks over the whole provider
set run before any kernel write.

- [ ] **Step 6: Confirm the mapped difference, and only that**

```bash
ssh mwan.suburban.goodkind.io 'cat /etc/nftables.conf' > /tmp/after-nftconf.txt
diff /tmp/before-nftconf.txt /tmp/after-nftconf.txt
ssh mwan.suburban.goodkind.io 'nft list ruleset' > /tmp/after-nft.txt
diff /tmp/before-nft.txt /tmp/after-nft.txt
```

Expected from the first diff: exactly the three balancer lines removed, plus the
`define` lines for the three provider interfaces, plus whatever the firewall
slice's loops re-render identically. Every remaining line matches.

Expected from the second diff: the same three lines gone from the live
`table ip nat` and `table inet mangle` chains, and a new `table inet mwan_steer`
present. Nothing else moves.

The steering table's expected content on the testbed, with two healthy providers
in tier 0:

```
table inet mwan_steer {
	chain prerouting {
		type filter hook prerouting priority mangle + 1; policy accept;
		iifname "enmwanbr0" ip saddr <internal-net-v4> meta mark 0x00000000 ct state new meta mark set numgen random mod 2 map { 0 : 0x00000001, 1 : 0x00000002 }
		ip6 saddr <opnsense-edge-v6>/128 meta mark 0x00000000 ct state new meta mark set numgen random mod 2 map { 0 : 0x00000001, 1 : 0x00000002 }
		ip6 saddr <internal-prefix> meta mark 0x00000000 ct state new meta mark set numgen random mod 2 map { 0 : 0x00000001, 1 : 0x00000002 }
	}
}
```

Substitute `<internal-net-v4>`, `<opnsense-edge-v6>`, and `<internal-prefix>`
with the values the before snapshot's file already carries. Three renderings are
acceptable variations of the same ruleset and are not differences: the priority
may print as `priority -149` instead of `priority mangle + 1`; marks may print
in decimal instead of hex; and an `ip saddr` match inside an inet table may
print with a leading `meta nfproto ipv4`. A different modulus, a different map,
a missing `meta mark 0` guard, or a missing `ct state new` is a real difference
and stops the cutover.

- [ ] **Step 7: Confirm the routes and rules did not move**

```bash
ssh mwan.suburban.goodkind.io 'ip -6 rule show; ip rule show' > /tmp/after-rules.txt
ssh mwan.suburban.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; ip route show table $t; done' > /tmp/after-routes.txt
ssh mwan.suburban.goodkind.io 'for m in 1 2 3; do echo "== mark $m"; ip route get 1.1.1.1 mark $m; ip -6 route get 2606:4700:4700::1111 mark $m; done' > /tmp/after-marks.txt
diff /tmp/before-rules.txt /tmp/after-rules.txt
diff /tmp/before-routes.txt /tmp/after-routes.txt
diff /tmp/before-marks.txt /tmp/after-marks.txt
```

Expected: all three diffs are empty. The policy rules and the three provider
tables are byte-identical, and every mark still resolves to its provider. The
routing numbers were hand-typed before this change and are hand-typed after it,
so any movement here means a value was derived somewhere it should not have
been.

- [ ] **Step 8: Confirm the served tree**

```bash
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-interfaces:interfaces" > /tmp/after-tree.json
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-nat:nat" > /tmp/after-nat.json
diff <(jq -S . /tmp/before-tree.json) <(jq -S . /tmp/after-tree.json)
diff <(jq -S . /tmp/before-nat.json) <(jq -S . /tmp/after-nat.json)
```

Expected: the NAT instances are identical. The interfaces tree differs only by
additions: each provider gains a `steering` container with its tier and weight,
and `steering-group` gains `hash-mode` and `reserved-tables`. Live state that
legitimately moves, a health verdict's timestamp or a consecutive-failure
counter, is the only other permitted difference; name each one in the ticket
rather than waving at it.

- [ ] **Step 9: Confirm the watchdog is hearing the gateway**

```bash
ssh suburban 'journalctl -u mwan-watchdog-testbed -n 100 --no-pager | grep -i statuspush'
ssh suburban 'grep status_listen_port /etc/mwan/config.toml'
ssh mwan.suburban.goodkind.io 'grep -A2 status_push /etc/mwan/config.toml'
ssh suburban 'grep -c wan_interfaces /etc/mwan/config.toml /etc/mwan-watchdog/network.toml || true'
```

Expected: the watchdog journal carries `statuspush: status received` lines at
the health module's cadence; the hypervisor renders `status_listen_port = 50053`
and the gateway renders `status_push_cid = 2` with `status_push_port = 50053`;
and both greps for `wan_interfaces` count zero. A missing received line with a
`listen failed` line beside it means the hypervisor's vsock listener could not
bind, which is a defect to fix rather than a reason to continue.

- [ ] **Step 10: Confirm the old ISP probe cannot appear**

```bash
ssh suburban 'journalctl -u mwan-watchdog-testbed --since "-1h" --no-pager | grep -E "Gateway provider status|Testing ISP reachability"'
```

Expected: no "Testing ISP reachability" line exists any more. If the watchdog
has not entered a diagnosis during the window this returns nothing, which is the
healthy case; the behavior itself is covered by the diagnosis test in Task 7,
and the live check is that the old line cannot appear.

- [ ] **Step 11: Run the traffic matrix as the after run**

Repeat step 3 in full. Every row must match the before run: same egress
provider, same translated source at the same simulator ingress, same reply path,
same fallback behavior, same pinned destinations, same inbound paths. The split
across tier 0 now comes from the daemon's chain rather than the ruleset file,
and the observed spread must be the same half and half.

- [ ] **Step 12: Prove the agent still owns the provider tables**

```bash
ssh mwan.suburban.goodkind.io 'journalctl -u mwan-agent -n 30 --no-pager'
ssh mwan.suburban.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; done'
```

Expected: every provider table carries the internal prefixes learned from the
router, not just its own default route, and the same prefixes step 2 recorded. A
table holding only a default route means the agent lost its owned-table list.

#### The astound proof, testbed only, binary unchanged

Everything below runs against the binary the testbed already has. Nothing is
rebuilt and no release tag changes.

- [ ] **Step 13: Add the astound provider entry**

In `ansible/inventory/group_vars/mwan_suburban_servers.yml`, append to
`mwan_providers`:

```yaml
  - name: astound
    iface: "{{ mwan_astound_iface }}"
    table: 600
    mark: 4
    mark_prio: 600
    from_prio: 58
    tier: 2
    weight: 1
    npt_prefix: "3d06:bad:b01:2500::/60"
    health:
      enabled: true
      ping_count: 3
      success_threshold: 2
      failure_threshold: 2
      recovery_threshold: 2
      check_interval: 10
      targets_v4: ["1.1.1.1", "8.8.8.8"]
      targets_v6: ["2606:4700:4700::1111", "2001:4860:4860::8888"]
      http_targets: ["https://ifconfig.co/ip"]
```

Tables 400 and 500 are reserved for the tunnel and the out-of-band path, so 600
is the first free hundred. The interface, its MAC, its address, and its two link
files are already in inventory from Task 8, so this entry adds a provider and
nothing else. The translation prefix is the first /60 of the simulator's /56
delegation, matching how the other three take theirs.

- [ ] **Step 14: Deploy the fourth provider**

Announce the reboot window to the three peer sessions, wait for acknowledgement,
then run:

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
```

Expected: the play passes with the same release tag as step 4. The schema
validation accepts a four-provider tree, which is the check that used to be
impossible: the two fixed priority validators would have rejected mark-rule
priority 600 and source-rule priority 58 before this work. Report the window
afterwards.

- [ ] **Step 15: Prove the fourth provider's routes and rules exist**

```bash
ssh mwan.suburban.goodkind.io 'ip route show table 600; ip -6 route show table 600'
ssh mwan.suburban.goodkind.io 'ip rule show | grep -E "^(58|600):"'
ssh mwan.suburban.goodkind.io 'ip -6 rule show | grep -E "^(58|600):"'
ssh mwan.suburban.goodkind.io 'nft list table inet mwan_steer'
```

Expected: table 600 holds a default route via `10.240.207.1` on `enastound0`.
`ip rule` shows a rule at priority 600 matching `fwmark 0x4 lookup 600` and a
rule at priority 58 matching `from 10.240.207.2 lookup 600`, in both families
where the family applies. The steering table's map is unchanged, still
`mod 2 map { 0 : 1, 1 : 2 }`, because astound is in tier 2 and tier 0 still has
two healthy providers. A map that grew here means the tier was ignored.

- [ ] **Step 16: Prove traffic leaves through astound in both families**

Start the capture on the simulator first, in one terminal, with `SIM_ID`
resolved as in Task 8 step 12:

```bash
ssh suburban "pct exec $SIM_ID '--' timeout 60 tcpdump -ni eth0 'icmp or icmp6'"
```

Then, from the gateway:

```bash
ssh mwan.suburban.goodkind.io 'ping -c 4 -m 4 -W 2 1.1.1.1'
ssh mwan.suburban.goodkind.io 'ping -c 4 -m 4 -W 2 2606:4700:4700::1001'
```

Expected: both pings answer, and the simulator's capture shows the echo requests
arriving on eth0 with the replies going back, once in IPv4 and once in ICMPv6.
Mark 4 is what selects table 600, so this is the whole chain: mark, rule, table,
route, interface, simulator ingress. Both families run every time; an IPv4
success is never evidence for IPv6.

- [ ] **Step 17: Move astound into the active tier**

Change the astound entry's `tier: 2` to `tier: 0`, leave `weight: 1`, announce
the window, and deploy with the same tag:

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
```

Then:

```bash
ssh mwan.suburban.goodkind.io 'nft list table inet mwan_steer'
```

Expected: all three balancing rules now read
`mod 3 map { 0 : 1, 1 : 2, 2 : 4 }`, one slot per weight unit across the three
healthy tier-0 providers, with slots assigned in ascending mark order. Marks 1,
2, and 4 appear; mark 3 does not, because monkeybrains is still alone in tier 1.
The routes, the rules, and table 600 are unchanged from step 15: re-tiering
changes the split and nothing else.

Confirm the traffic follows, with new connections generated from an internal
client behind the testbed router:

```bash
ssh suburban "pct exec $SIM_ID '--' timeout 60 tcpdump -ni eth0 'ip or ip6'"
```

Expected: roughly a third of new connections appear at the astound simulator, in
both families, with the translated source inside `3d06:bad:b01:2500::/60` for
IPv6 and the masqueraded link address for IPv4.

- [ ] **Step 18: Remove astound and prove it leaves no trace**

Delete the astound entry from `mwan_providers`, leaving the hardware variables
and the two link files in place, announce the window, and deploy with the same
tag:

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
```

Then:

```bash
ssh mwan.suburban.goodkind.io 'ip route show table 600; ip -6 route show table 600'
ssh mwan.suburban.goodkind.io 'ip rule show | grep -E "^(58|600):" || echo "no astound rules"'
ssh mwan.suburban.goodkind.io 'ip -6 rule show | grep -E "^(58|600):" || echo "no astound rules"'
ssh mwan.suburban.goodkind.io 'nft list table inet mwan_steer'
ssh mwan.suburban.goodkind.io 'ip -6 rule show; ip rule show' > /tmp/astound-removed-rules.txt
diff /tmp/after-rules.txt /tmp/astound-removed-rules.txt
```

Expected: table 600 is empty, both rule greps print `no astound rules`, the
steering map is back to `mod 2 map { 0 : 1, 1 : 2 }`, and the rule diff against
step 7's capture is empty. The testbed is byte for byte where the cutover left
it, and acceptance item three is proven: a provider was added, re-tiered, and
removed by inventory edit and deploy, with the binary unchanged throughout.

Then confirm the branch carries the three-provider testbed inventory it started
with, plus only Task 8's hardware variables and networkd entries, which stay:

```bash
cd "$(git rev-parse --show-toplevel)"
git status --short ansible/inventory/group_vars/mwan_suburban_servers.yml
git diff ansible/inventory/group_vars/mwan_suburban_servers.yml
```

Expected: no astound provider entry remains in `mwan_providers`.

#### Production

Each command below needs the operator's explicit approval immediately before it
runs. Do not batch them, and do not run a production command because a testbed
command like it succeeded.

- [ ] **Step 19: Capture the production before snapshot**

```bash
MGMT=3d06:bad:b01::113
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-interfaces:interfaces" > /tmp/prod-before-tree.json
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-nat:nat" > /tmp/prod-before-nat.json
ssh mwan.home.goodkind.io 'nft list ruleset' > /tmp/prod-before-nft.txt
ssh mwan.home.goodkind.io 'cat /etc/nftables.conf' > /tmp/prod-before-nftconf.txt
ssh mwan.home.goodkind.io 'ip -6 rule show; ip rule show' > /tmp/prod-before-rules.txt
ssh mwan.home.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; ip route show table $t; done' > /tmp/prod-before-routes.txt
ssh mwan.home.goodkind.io 'for m in 1 2 3; do echo "== mark $m"; ip route get 1.1.1.1 mark $m; ip -6 route get 2606:4700:4700::1111 mark $m; done' > /tmp/prod-before-marks.txt
ssh mwan.home.goodkind.io 'journalctl -u mwan-agent -n 40 --no-pager' > /tmp/prod-before-bgp.txt
```

Add a per-provider forced-egress probe in both families and record its result,
so the reduced live checks after the cutover have a baseline.

- [ ] **Step 20: Announce the production window**

Send to the audit-ops, backup-reach, and prod-outage peer sessions: the
production gateway reboots during the MWAN-324 deploy, household egress drops
for the reboot, and the expected window is the deploy gate's. Wait for
acknowledgement from all three.

- [ ] **Step 21: Cut production over**

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_servers
```

Expected: the schema validation passes, the deploy gate's reboot and egress
verdicts pass, and the reboot window is reported. Record the exact window and
report it to the three peer sessions afterwards.

- [ ] **Step 22: Confirm the daemon started and the mapped difference is the only one**

```bash
ssh mwan.home.goodkind.io 'systemctl status mwan-ifmgr@wan --no-pager'
ssh mwan.home.goodkind.io 'journalctl -u mwan-ifmgr@wan -n 120 --no-pager'
ssh mwan.home.goodkind.io 'cat /etc/nftables.conf' > /tmp/prod-after-nftconf.txt
diff /tmp/prod-before-nftconf.txt /tmp/prod-after-nftconf.txt
ssh mwan.home.goodkind.io 'nft list ruleset' > /tmp/prod-after-nft.txt
diff /tmp/prod-before-nft.txt /tmp/prod-after-nft.txt
ssh mwan.home.goodkind.io 'nft list table inet mwan_steer'
```

Expected: the unit is active with no "network configuration unusable" line; the
file diff removes exactly the three balancer lines and the three interface
`define` lines; and the live ruleset gains `table inet mwan_steer` with three
rules whose map is:

```
mod 2 map { 0 : 0x00000001, 1 : 0x00000002 }
```

AT&T is mark 1 and Webpass is mark 2, both tier 0 and both weight 1, so the
production split is the same half and half the three deleted lines expressed.
Monkeybrains, mark 3, is alone in tier 1 and appears in no map. Each rule
carries the `meta mark 0` guard and the `ct state new` match; without the guard
the control-plane pins set earlier in the pass would be overwritten, which is
the failure mode the spec names.

- [ ] **Step 23: Confirm production routes, rules, and the served tree**

```bash
ssh mwan.home.goodkind.io 'ip -6 rule show; ip rule show' > /tmp/prod-after-rules.txt
ssh mwan.home.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; ip route show table $t; done' > /tmp/prod-after-routes.txt
ssh mwan.home.goodkind.io 'for m in 1 2 3; do echo "== mark $m"; ip route get 1.1.1.1 mark $m; ip -6 route get 2606:4700:4700::1111 mark $m; done' > /tmp/prod-after-marks.txt
diff /tmp/prod-before-rules.txt /tmp/prod-after-rules.txt
diff /tmp/prod-before-routes.txt /tmp/prod-after-routes.txt
diff /tmp/prod-before-marks.txt /tmp/prod-after-marks.txt
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-nat:nat" > /tmp/prod-after-nat.json
diff <(jq -S . /tmp/prod-before-nat.json) <(jq -S . /tmp/prod-after-nat.json)
```

Expected: the three kernel diffs are empty and the NAT instances are identical.
The interfaces tree differs only by the added steering containers and the two
added steering-group leaves.

- [ ] **Step 24: Confirm production egress in both families**

Run the per-provider forced-egress probes from step 19 again, in both families,
and confirm each provider carries traffic with the source the baseline recorded.
Confirm the agent's per-provider tables still hold the router-learned prefixes:

```bash
ssh mwan.home.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; done'
ssh mwan.home.goodkind.io 'journalctl -u mwan-agent -n 40 --no-pager'
```

Expected: identical to the baseline. No synthetic failover drill runs on
production unless the operator orders one.

- [ ] **Step 25: Confirm the production watchdog hears the gateway**

```bash
ssh vault 'journalctl -u mwan-watchdog -n 100 --no-pager | grep -i statuspush'
ssh vault 'grep status_listen_port /etc/mwan/config.toml'
ssh vault 'grep -c wan_interfaces /etc/mwan/config.toml /etc/mwan-watchdog/network.toml || true'
ssh mwan.home.goodkind.io 'grep -A2 status_push /etc/mwan/config.toml'
```

Expected: the watchdog journal carries `statuspush: status received` lines at
the health cadence, `status_listen_port = 50053` is rendered, both
`wan_interfaces` counts are zero, and the gateway renders context id 2 with port
50053. The production hypervisor is reached through the pinned out-of-band
tunnel; a single failed probe there is the known-lossy wireless path rather than
an outage, so confirm through the vault path rather than by pinging.

- [ ] **Step 26: Record the outcome**

Append to the MWAN-324 tickets: the before-and-after evidence for both
environments, the two reboot windows, the steering table's rendered content on
each gateway, the astound add, re-tier, and remove evidence with the simulator
captures, and the confirmation that no reader holds a provider list any more.
Append one entry to the wanconfig ledger naming what shipped and what remains,
which is MWAN-442, whether a pushed verdict blocks a rollback.

---

### Task 10: the management stack install becomes part of deploy-mwan

The gateway deploy installs the management stack itself, so one command puts
the schema revision, the packages, and the gateway configuration on a gateway
in the order the daemon needs. Today the stack has its own play, and an
operator who runs `deploy-mwan` without first running `deploy-wanconfig-stack`
after a model revision bump gets a daemon that validates its network file
against a model directory that does not carry the new leaves. The order is
load-bearing and lives only in a runbook, which is where such rules drift.

This task lands in the Task 5 pull request before it merges (operator ruling
2026-09-07), so the first deploy after that merge runs one play.

**Files:**
- Create: `ansible/playbooks/tasks/mwan-vm/wanconfig-stack.yml`
- Modify: `ansible/playbooks/deploy-mwan.yml` (insert one import after the
  controller-side schema validation task; add two handlers)
- Delete: `ansible/playbooks/deploy-wanconfig-stack.yml`

**Interfaces:**
- Consumes: the extra vars every `--release` deploy stages
  (`mwan_release_tag`, `wanconfig_stack_dir`), which the deploy command sets
  for any play; the model file `mwan/yang/goodkind-mwan-steering@2026-09-02.yang`.
- Produces: a `deploy-mwan` run that installs the stack packages, the model
  directory the daemon validates against, the sysrepo modules at the current
  revision, the NACM policy, and the two RESTCONF units before it writes the
  gateway configuration or restarts the daemon.
- Removes: the standalone `deploy-wanconfig-stack` play and the two-command
  ordering rule.

- [ ] **Step 1: Move the stack tasks into a task file**

Create `ansible/playbooks/tasks/mwan-vm/wanconfig-stack.yml` with exactly the
task list of `ansible/playbooks/deploy-wanconfig-stack.yml` (its `tasks:`
entries, from "Install the front-end proxy" through "Enable and start the
stack services"), de-indented to top level and unchanged in content, preceded
by this header:

```yaml
---
# Install the wanconfig management stack on the MWAN gateway (MWAN-434).
# The stack arrives as Debian packages built, proven, and attested by the
# mwan release: the controller stages the bundle beside the binaries, and
# these tasks copy the packages to the gateway and install them with apt.
# No gateway runs a compiler, a clone, or cmake. The vendored YANG model
# loads into the sysrepo datastore, and rousette serves RESTCONF on ::1
# behind an nghttpx front end bound to the management address only.
# Read-only: NACM denies every write and grants anonymous read per
# rousette's contract.
#
# Imported by deploy-mwan.yml before the gateway configuration is written,
# because the daemon validates its network file against the model directory
# these tasks fill, and a revision bump has to reach that directory before
# the daemon restarts. The importing play sets the variables these tasks
# read and runs them as root.
```

The two `notify:` lines in the moved tasks keep their handler names,
`Restart rousette` and `Restart nghttpx-wanconfig`; Step 3 gives the
importing play those handlers.

- [ ] **Step 2: Import the task file from the gateway deploy**

In `ansible/playbooks/deploy-mwan.yml`, directly after the "Validate the
rendered network configuration against the schema" task and before "Deploy
MWAN runtime environment file", insert (the controller-side render and
validation run first, so a rejected render leaves the gateway untouched):

```yaml
    # The management stack goes on after the controller has validated the
    # render and before any gateway file is written, so a rejected render
    # leaves the gateway untouched, and the model directory the daemon
    # validates its network file against carries the revision this deploy
    # renders for by the time the file lands. The release staged the bundle
    # beside the binaries; mwan_release_tag and wanconfig_stack_dir are read
    # bare, so a deploy without --release fails at its first read of a
    # release variable rather than installing nothing.
    - name: Install the wanconfig management stack
      ansible.builtin.import_tasks: tasks/mwan-vm/wanconfig-stack.yml
      become: true
      vars:
        wanconfig_deb_dir: "/var/lib/mwan-wanconfig/debs/{{ mwan_release_tag }}"
        wanconfig_yang_dir: /usr/local/share/wanconfig/yang
        # The rousette package ships the models its RESTCONF contract needs.
        wanconfig_rousette_yang_dir: /opt/mwan-wanconfig/share/yang/modules/rousette
        # ietf-nat guards every enum value behind its nat-type features, so an
        # install with no features enabled leaves leaves with zero valid
        # values and libyang rejects the module. Enable exactly the
        # translation types the model uses.
        wanconfig_yang_modules:
          - name: ietf-interfaces@2018-02-20
            features: []
          - name: ietf-ip@2018-02-22
            features: []
          - name: ietf-routing@2018-03-13
            features: []
          - name: ietf-nat@2019-01-10
            features:
              - basic-nat44
              - napt44
              - dst-nat
              - nptv6
          - name: goodkind-mwan-steering@2026-09-02
            features: []
```

- [ ] **Step 3: Give the play the stack handlers**

In the same file, in the `handlers:` list of the "Configure MWAN VM" play,
directly before the "Restart mwan-ifmgr@wan" handler, insert (handlers run
in definition order, so the management services restart before the daemon
that publishes into them):

```yaml
    - name: Restart rousette
      ansible.builtin.systemd:
        name: rousette
        state: restarted
        daemon_reload: true

    - name: Restart nghttpx-wanconfig
      ansible.builtin.systemd:
        name: nghttpx-wanconfig
        state: restarted
        daemon_reload: true
```

- [ ] **Step 4: Delete the standalone play**

```bash
cd "$(git rev-parse --show-toplevel)"
git rm ansible/playbooks/deploy-wanconfig-stack.yml
```

Nothing else in the repository names the play: the rake wrappers list five
canonical playbooks and this is not one, the CI workflows never run it, and
the deploy command resolves any playbook name by file.

- [ ] **Step 5: Verify the play parses and the templates lint**

```bash
cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:mwan
```

Expected: PASS, exit 0. A syntax check resolves every `import_tasks`, so a
wrong path or a task-level keyword the import rejects fails here.

```bash
cd "$(git rev-parse --show-toplevel)" && ./configsctl lint
```

Expected: PASS, exit 0. The deploy's lint scope follows `import_tasks`, so the
moved file is linted as part of `deploy-mwan`.

- [ ] **Step 6: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add ansible/playbooks/tasks/mwan-vm/wanconfig-stack.yml ansible/playbooks/deploy-mwan.yml ansible/playbooks/deploy-wanconfig-stack.yml
git commit -S -m "Install the wanconfig management stack from deploy-mwan" -m "Move the stack tasks into tasks/mwan-vm/wanconfig-stack.yml, import them from deploy-mwan before the gateway configuration is written with the stack variables and the two restart handlers, and delete the standalone deploy-wanconfig-stack play." -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---
