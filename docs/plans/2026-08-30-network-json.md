> Read the [repository context](../README.md) before using copied commands or historical plans.

# network.json Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on this page refer to the tree as it was.

**Goal:** Move the gateway's network configuration into `/etc/mwan/network.json` in the model's own JSON encoding, validated against the same schema at deploy time and at load time, with the TOML file keeping every non-network section.

**Architecture:** One additive revision of the steering module adds the per-provider `wan` container and the group-wide translation, routes, and health leaves. Ansible renders the network tree from the same group_vars the TOML template reads, the deploy validates the rendered file with yanglint on the controller before it reaches the gateway, and `mwan ifmgr` validates it again with libyang at startup and fills the same internal structures the TOML sections filled. The TOML struct fields for those sections stop being decoded in the same change the JSON loader starts filling them, so exactly one file owns each section at every moment.

**Tech Stack:** YANG 1.1 and libyang 3.13.6 (yanglint on the controller, the cgo binding in the daemon), Go with `encoding/json` and `github.com/BurntSushi/toml`, Ansible with Jinja2 templates, systemd.

**Spec:** [docs/superpowers/wanconfig/config.md](../superpowers/wanconfig/config.md)

Supporting artifacts, binding on shape rather than on wording: the approved schema revision at `.superpowers/sdd/mwan-339-draft/goodkind-mwan-steering@2026-08-30.yang`, the design notes at `.superpowers/sdd/mwan-339-schema-notes.md`, and the leaf-by-leaf coverage map at `.superpowers/sdd/mwan-339-coverage.md`.

## Global Constraints

- `network.json` carries only the gateway network tree: the provider inventory, each provider's routing numbers, translation prefix, IPv4 source pin, and health probe settings, and the group-wide translation, internal-link, and probe-timeout values.
- No secret ever enters the JSON.
- The inventory remains the source of truth: group_vars render the file, and a provider change is an inventory edit plus a deploy.
- No value conversion logic exists anywhere: the inventory holds bare integers, and the schema's time spans are integers that name their unit.
- Deploy-time and load-time validation use the same schema files, never copies.
- A missing value is a load-time failure, never a defaulted one.
- The TOML loader stops reading a section in the same change the JSON loader starts owning it.
- Behavior is unchanged, proven through the served tree and the traffic matrix, not through file equivalence.
- The failover container and the hypervisor keep their TOML-only configuration untouched; their roles run none of the sections the JSON carries.
- Every mwan-installing deploy requires `--release <tag>`.
- Testbed before production, always. Each production command is separately approved by the operator before it runs.
- Go style: comments explain non-obvious why, never what; full-word names; struct literals enumerate every field, because the `exhaustruct` gate requires it.
- Commits are signed (`git commit -S`), the subject is imperative with no trailing period, and the body ends with `Co-authored-by: Claude <noreply@anthropic.com>`.

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

On a linux host with the cgo dependencies built (`make check` builds them), the
same run is `cd mwan/go && go test -count=1 ./internal/networkjson/ -v`.

Ansible is never invoked directly. Syntax checks run through the repository's
rake wrappers (`cd ansible && rake syntax:mwan`), and deploys run through
`./configsctl deploy <play> --limit <group>`. The play pulls the release its
environment pins in group_vars, so no release is named on the command line.

The controller needs `yanglint` on PATH for both `make yang-validate` and the new
deploy-time check. It comes from the `libyang` Homebrew formula on macOS and the
`libyang2-tools` package on Debian. This is an operator-visible prerequisite on
the controller: a deploy from a controller without yanglint fails at the
validation task rather than shipping an unvalidated file.

---

### Task 1: The schema revision lands

The approved draft becomes the repository's model revision. The repository keeps
exactly one revision file at a time and bumps it by renaming: git records commit
b4111130, the `@2026-08-28` to `@2026-08-29` bump, as a rename with 67 percent
similarity rather than as an add plus a delete. The deploy play names the
revision in two places, both of which move with it.

This task also adds an instance gate. `make yang-validate` proves the module
compiles, and nothing today proves a configuration instance validates against it.
The fixture uses documentation addresses only, so no inventory value is copied
into the repository.

**Files:**
- Rename: `mwan/yang/goodkind-mwan-steering@2026-08-29.yang` to `mwan/yang/goodkind-mwan-steering@2026-08-30.yang`, content replaced from `.superpowers/sdd/mwan-339-draft/goodkind-mwan-steering@2026-08-30.yang`
- Create: `mwan/yang/instances/network-min.json`
- Modify: `mwan/go/Makefile:157-170` (the YANG model gate section)
- Modify: `ansible/playbooks/deploy-wanconfig-stack.yml:49` and `:136`

**Interfaces:**
- Consumes: nothing.
- Produces: the model revision `goodkind-mwan-steering@2026-08-30`, whose configuration nodes every later task renders, validates, and loads: `/ietf-interfaces:interfaces/interface[name]/goodkind-mwan-steering:wan` with leaves `name`, `table-id`, `fw-mark`, `fw-mark-prio`, `from-prio`, `npt-prefix`, `v4-source`, and container `health` with leaves `enabled`, `ping-count`, `success-threshold`, `failure-threshold`, `recovery-threshold`, `check-interval` (seconds), and leaf-lists `targets-v4`, `targets-v6`, `http-urls`; and `/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group` with containers `translation` (`internal-prefix`, `opnsense-edge-v6`, `mwanbr-edge-v6`), `routes` (`internal-iface`, `internal-net-v4`), and `health` (`probe-timeout`, milliseconds).
- Produces: the make target `yang-validate-instances`, which validates every `mwan/yang/instances/*.json` file and is wired into `check`.

- [ ] **Step 1: Write the failing instance fixture**

Create `mwan/yang/instances/network-min.json`:

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
        }
      },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }
    ],
    "goodkind-mwan-steering:steering-group": {
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

- [ ] **Step 2: Add the instance gate to the Makefile**

In `mwan/go/Makefile`, after the `yang-validate` target and before the
`check: yang-validate` line, insert:

```make
# A data instance carrying an interface must give it a type, and that identity
# lives in the interface-type registry, so the instance gate needs one module
# the schema gate does not.
YANG_INSTANCE_MODULES := \
	$(YANG_RFC_DIR)/ietf-yang-types@2025-12-22.yang \
	$(YANG_RFC_DIR)/ietf-inet-types@2025-12-22.yang \
	$(YANG_RFC_DIR)/iana-if-type@2014-05-08.yang \
	$(YANG_RFC_DIR)/ietf-interfaces@2018-02-20.yang \
	$(YANG_RFC_DIR)/ietf-ip@2018-02-22.yang \
	$(YANG_RFC_DIR)/ietf-routing@2018-03-13.yang \
	$(YANG_RFC_DIR)/ietf-nat@2019-01-10.yang

YANG_INSTANCES := $(wildcard $(YANG_DIR)/instances/*.json)

# Each instance is validated as configuration, the same check the deploy runs on
# the rendered file and the daemon runs at startup. A schema change that would
# reject the shape the gateway is configured with fails here.
.PHONY: yang-validate-instances
yang-validate-instances:
	@for instance in $(YANG_INSTANCES); do \
		echo "yanglint -t config $$instance"; \
		$(YANGLINT) -t config $(YANG_INSTANCE_MODULES) $(YANG_DIR)/*.yang "$$instance" || exit 1; \
	done
```

Then change the `check` hook line from `check: yang-validate` to:

```make
check: yang-validate yang-validate-instances
```

- [ ] **Step 3: Run the instance gate to verify it fails**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make yang-validate-instances`
Expected: FAIL. The installed revision is `@2026-08-29`, which defines no `wan`
container and no `translation`, `routes`, or `health` container under
`steering-group`, so yanglint reports `Node "wan" not found in the
"goodkind-mwan-steering" module` and exits nonzero.

- [ ] **Step 4: Rename the model file to the new revision**

```bash
cd "$(git rev-parse --show-toplevel)"
git mv mwan/yang/goodkind-mwan-steering@2026-08-29.yang mwan/yang/goodkind-mwan-steering@2026-08-30.yang
```

- [ ] **Step 5: Replace the model content with the approved draft**

```bash
cd "$(git rev-parse --show-toplevel)"
cp .superpowers/sdd/mwan-339-draft/goodkind-mwan-steering@2026-08-30.yang \
   mwan/yang/goodkind-mwan-steering@2026-08-30.yang
```

- [ ] **Step 6: Run both YANG gates to verify they pass**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make yang-validate yang-validate-instances`
Expected: PASS. `yang-validate` prints the yanglint version and exits 0;
`yang-validate-instances` prints one line per instance and exits 0.

- [ ] **Step 7: Point the stack deploy at the new revision**

In `ansible/playbooks/deploy-wanconfig-stack.yml`, change the model name in
`wanconfig_yang_modules` from:

```yaml
      - name: goodkind-mwan-steering@2026-08-29
        features: []
```

to:

```yaml
      - name: goodkind-mwan-steering@2026-08-30
        features: []
```

and change the last entry of the "Copy the gateway model and its IETF imports"
loop from:

```yaml
        - "../../mwan/yang/goodkind-mwan-steering@2026-08-29.yang"
```

to:

```yaml
        - "../../mwan/yang/goodkind-mwan-steering@2026-08-30.yang"
```

- [ ] **Step 8: Verify the plays still parse**

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:all`
Expected: PASS, exit 0.

- [ ] **Step 9: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/yang/goodkind-mwan-steering@2026-08-30.yang mwan/yang/instances/network-min.json mwan/go/Makefile ansible/playbooks/deploy-wanconfig-stack.yml
git commit -S -m "Add the gateway network configuration nodes in goodkind-mwan-steering revision 2026-08-30" -m "Carry the per-provider wan container beside steering with its routing slots, translation prefix, source pin, and health probe, and the group-wide translation, routes, and probe-timeout leaves; add a yanglint instance gate over mwan/yang/instances; and point deploy-wanconfig-stack.yml at the new revision." -m "Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 2 (MWAN-347): The renderer

Ansible renders the network tree beside the TOML file. In this slice the daemon
still loads only TOML, so the rendered file is inert and behavior cannot change.

The template mirrors what the TOML template actually does rather than inventing a
loop the inventory does not support. `mwan/config/config-vm.toml.j2:130-157`
writes three explicit provider blocks with per-provider variable names, and only
the health section loops, at `:169-181`, over `mwan_health_checks`. The template
below declares the same three providers once as a Jinja list and loops that, so
the three entries cannot drift in shape, and it looks each provider's probe up in
`mwan_health_checks` by name, which is the same join `buildHealthConfig` makes at
runtime in `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go:216-218`.

Two rendering details matter. Ansible runs Jinja with `trim_blocks` on and
`lstrip_blocks` off, so every block tag sits at column zero; indenting one would
emit its leading spaces into the JSON. The document is built as a data structure
and emitted through `to_nice_json` rather than written as literal JSON with
hand-placed commas, so a conditional leaf cannot produce invalid JSON.

The probe timeout is the one value with no inventory variable today: the TOML
template hardcodes `timeout = "2s"` at `config-vm.toml.j2:167`. It becomes an
inventory integer in milliseconds, added to both gateway groups. The TOML line
stays exactly as it is until Task 5.

There is no red step for the template itself, and that gap is worth naming. No
gate in this repository renders a Jinja template, because rendering needs the
vaulted inventory. The shape is pinned by the instance fixture from Task 1, the
play is checked by `rake syntax:mwan`, the rendered output is validated on every
deploy by Task 4, and the rendered file is read back and validated by hand in the
runbook's first testbed slice in Task 6.

**Files:**
- Create: `mwan/config/network.json.j2`
- Modify: `ansible/playbooks/deploy-mwan.yml:248-253` (insert a task directly after "Deploy MWAN runtime config")
- Modify: `ansible/inventory/group_vars/mwan_servers.yml:207` (insert above `mwan_health_checks`)
- Modify: `ansible/inventory/group_vars/mwan_suburban_servers.yml:226` (insert above `mwan_health_checks`)

**Interfaces:**
- Consumes: the model revision from Task 1, and these inventory variables, all of which both gateway groups already define: `mwan_att_iface`, `mwan_att_vlan_id`, `mwan_webpass_iface`, `mwan_monkeybrains_iface`, `mwan_internal_iface`, `mwan_rt_tables`, `mwan_ifmgr_wan_fw_marks`, `mwan_ifmgr_wan_fw_mark_prios`, `mwan_ifmgr_wan_from_prios`, `mwan_npt_att_prefix`, `mwan_npt_webpass_prefix`, `mwan_npt_monkeybrains_prefix`, `mwan_webpass_ipv4_addr`, `mwan_health_checks`, `mwan_internal_prefix`, `mwan_opnsense_edge_ipv6`, `mwan_mwanbr_edge_ipv6`, `mwan_internal_net_v4`, `mwan_ifmgr_wan_enabled`.
- Produces: `/etc/mwan/network.json` on both gateway VMs at mode 0600, and the inventory variable `mwan_health_probe_timeout_ms` (integer, milliseconds).

- [ ] **Step 1: Add the probe-timeout variable to production inventory**

In `ansible/inventory/group_vars/mwan_servers.yml`, directly above the
`mwan_health_checks:` mapping, insert:

```yaml
# How long one health probe attempt may take before it counts as failed.
# Milliseconds, because a probe timeout is the one span here an operator tunes
# below a second. Rendered into network.json as steering-group/health/probe-timeout.
mwan_health_probe_timeout_ms: 2000
```

- [ ] **Step 2: Add the probe-timeout variable to testbed inventory**

In `ansible/inventory/group_vars/mwan_suburban_servers.yml`, directly above the
`mwan_health_checks:` mapping, insert:

```yaml
# How long one health probe attempt may take before it counts as failed.
# Milliseconds, because a probe timeout is the one span here an operator tunes
# below a second. Rendered into network.json as steering-group/health/probe-timeout.
mwan_health_probe_timeout_ms: 2000
```

- [ ] **Step 3: Write the template**

Create `mwan/config/network.json.j2`:

```jinja
{#
  The gateway's network tree in the model's own JSON encoding, deployed to
  /etc/mwan/network.json and loaded by mwan-ifmgr@wan. It renders from the same
  group_vars the TOML template reads, and every value crosses unchanged: the
  model's time spans are integers that name their unit, and the inventory
  already holds integers.

  Each provider hangs off the interface that carries it. iana-if-type:other is
  the type the served tree already publishes for these links, so the mandatory
  ietf-interfaces type leaf carries no invented value.

  Block tags sit at column zero on purpose. Ansible renders with trim_blocks on
  and lstrip_blocks off, so an indented tag would emit its leading spaces into
  the document.
#}
{% set providers = [
  {
    "name": "att",
    "iface": mwan_att_iface ~ ("." ~ mwan_att_vlan_id if mwan_att_vlan_id else ""),
    "npt_prefix": mwan_npt_att_prefix,
    "v4_source": ""
  },
  {
    "name": "webpass",
    "iface": mwan_webpass_iface,
    "npt_prefix": mwan_npt_webpass_prefix,
    "v4_source": mwan_webpass_ipv4_addr
  },
  {
    "name": "monkeybrains",
    "iface": mwan_monkeybrains_iface,
    "npt_prefix": mwan_npt_monkeybrains_prefix,
    "v4_source": ""
  }
] %}
{% set interfaces = [] %}
{% for provider in providers %}
{% set wan = {
  "name": provider.name,
  "table-id": mwan_rt_tables[provider.name],
  "fw-mark": mwan_ifmgr_wan_fw_marks[provider.name],
  "fw-mark-prio": mwan_ifmgr_wan_fw_mark_prios[provider.name],
  "from-prio": mwan_ifmgr_wan_from_prios[provider.name],
  "npt-prefix": provider.npt_prefix
} %}
{% if provider.v4_source %}
{% set wan = wan | combine({"v4-source": provider.v4_source}) %}
{% endif %}
{% if provider.name in mwan_health_checks %}
{% set check = mwan_health_checks[provider.name] %}
{% set wan = wan | combine({"health": {
  "enabled": check.enabled,
  "ping-count": check.ping_count,
  "success-threshold": check.success_threshold,
  "failure-threshold": check.failure_threshold,
  "recovery-threshold": check.recovery_threshold,
  "check-interval": check.check_interval,
  "targets-v4": check.targets_v4,
  "targets-v6": check.targets_v6,
  "http-urls": check.http_targets
}}) %}
{% endif %}
{% set _ = interfaces.append({
  "name": provider.iface,
  "type": "iana-if-type:other",
  "goodkind-mwan-steering:wan": wan
}) %}
{% endfor %}
{% set _ = interfaces.append({"name": mwan_internal_iface, "type": "iana-if-type:other"}) %}
{{ {
  "ietf-interfaces:interfaces": {
    "interface": interfaces,
    "goodkind-mwan-steering:steering-group": {
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

- [ ] **Step 4: Add the render task to the deploy**

In `ansible/playbooks/deploy-mwan.yml`, directly after the "Deploy MWAN runtime
config" task and before "Discover MWAN VM runtime network (MACs + NIC presence)",
insert:

```yaml
    # The gateway's network tree in the model's own JSON encoding. Rendered only
    # where the wan role runs, because it carries that role's sections and no
    # other. The mode matches config.toml beside it; the file carries no secret.
    - name: Deploy MWAN network configuration
      ansible.builtin.template:
        src: "{{ repo_root }}/mwan/config/network.json.j2"
        dest: /etc/mwan/network.json
        mode: "0600"
      when: mwan_ifmgr_wan_enabled | bool
      notify: Restart mwan-ifmgr@wan
```

- [ ] **Step 5: Verify the play parses**

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:mwan`
Expected: PASS, exit 0.

- [ ] **Step 6: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/config/network.json.j2 ansible/playbooks/deploy-mwan.yml ansible/inventory/group_vars/mwan_servers.yml ansible/inventory/group_vars/mwan_suburban_servers.yml
git commit -S -m "Render the gateway network tree to /etc/mwan/network.json" -m "Add mwan/config/network.json.j2, which emits the three providers with their routing slots, translation prefix, source pin, and health probe plus the group-wide translation, routes, and probe timeout in the model's JSON encoding; deploy it from deploy-mwan.yml where the wan role runs; and add mwan_health_probe_timeout_ms to both gateway groups so the probe timeout stops being hardcoded." -m "Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 3 (MWAN-348): The loader

The daemon reads the file, validates it against the installed schema, and fills
the same fields the TOML sections filled.

**Where the code lives.** The validator goes in `internal/yangpub`, not in a new
package: that package is already the tree's one cgo binding to libyang and
sysrepo, it is already linux-only, and it already carries the cgo and stub split
for a build with cgo off. It does not, however, expose a context. `New` connects
to sysrepo, and `SendNotification` borrows the connection's context through
`sr_acquire_context` at `publisher_cgo.go:334-336`. Configuration loading cannot
depend on a sysrepo connection, because `wanconfig.publish` gates that connection
and a gateway must load its configuration whether or not it publishes. So the new
file builds its own libyang context from the installed model directory using the
same `pkg-config libyang` binding, and no second binding enters the tree.

The decode and the mapping go in a new `internal/networkjson` package rather than
in `internal/config`. `internal/config` is compiled on every platform and imports
only the standard library and the TOML decoder, and pulling a linux-and-cgo-only
libyang dependency into it would tie the freebsd and darwin builds' configuration
package to a build tag matrix it has never needed. `internal/networkjson` is
linux-only, imports `internal/config` for the target types, and is called from
the linux-only ifmgr entry point in `cmd/mwan`.

**How the TOML stops being read.** The TOML struct fields for the network
sections get `toml:"-"`, which the decoder honours by skipping the field
entirely: `type_fields.go:94-97` reads `getOptions(sf.Tag)` and continues on
`opts.skip`, and `encode.go:647-651` sets `skip` for the tag value `-`.
Overwriting the fields after decode was the alternative and is rejected, because
a JSON file missing a value would then silently inherit whatever TOML carried,
which the spec forbids. The tag approach also keeps a gateway whose `config.toml`
still carries the old keys parsing cleanly, because `toml.Unmarshal` ignores keys
no field claims. That is what lets Task 5 delete the render in a later change
with no window where the gateway cannot start.

The failover container and the hypervisor are untouched by this. They render
`[ifmgr]` without the three prefixes: `mwan-failover/config.toml.j2:50-54` writes
its own block, and `mwan/config/_ifmgr_common.toml.j2:6-10` is gated on
`mwan_ifmgr_wan_enabled`, which `ansible/inventory/group_vars/all/vars.yml:75`
sets false everywhere but the two gateway groups. They render none of
`[ifmgr.wan.*]`, `[ifmgr.modules.wan.routes]`, or `[ifmgr.modules.health]`.

**What stays in TOML.** The three filesystem paths in these sections stay TOML's:
`[ifmgr.modules.health].state_file`, `.persist_state_file`, and
`[ifmgr.modules.wan.routes].health_state_file`. A network tree that carries
filesystem layout stops being a network tree, and the first and third must name
the same path, an invariant that stays where it is today. So `IfMgrHealthSection`
and `IfMgrWANRoutesSection` keep their path fields decoded from TOML and lose
only the fields the JSON owns.

**Two spans change type.** `IfMgrHealthSection.Timeout string` becomes
`ProbeTimeoutMillis int`, and `IfMgrHealthWANSection.CheckInterval string`
becomes `CheckIntervalSeconds int`. Keeping them as strings would force the
loader to format `10` back into `"10s"`, which is the value conversion this work
exists to delete. Both fields are unreachable from TOML once tagged `-`, so the
retype breaks no decode path.

**One schema file is added to the gateway.** The gateway's model directory does
not carry `iana-if-type`, which the stack deploy installs into sysrepo from
rousette's own directory at `deploy-wanconfig-stack.yml:146-161`. A data instance
naming an interface must give it a type, and that identity lives there, so the
model copy list gains the file and the gateway's model directory becomes
self-contained.

**Files:**
- Create: `mwan/go/internal/yangpub/schema_cgo.go`
- Create: `mwan/go/internal/yangpub/schema_stub.go`
- Create: `mwan/go/internal/networkjson/networkjson.go`
- Create: `mwan/go/internal/networkjson/networkjson_test.go`
- Modify: `mwan/go/internal/config/config.go:410-423` (`IfMgrSection` tags)
- Modify: `mwan/go/internal/config/ifmgr_modules.go:94-142` (`IfMgrWANEntry`, `IfMgrWANRoutesSection`, `IfMgrHealthSection`, `IfMgrHealthWANSection`)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go:204-215` and `:257-264` (the two span reads)
- Modify: `mwan/go/cmd/mwan/ifmgr_linux.go:44-73` (the ifmgr entry point)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go:608-709` (the round-trip test)
- Modify: `ansible/playbooks/deploy-wanconfig-stack.yml:129-136` (the model copy list)

**Interfaces:**
- Consumes: the model revision and its node paths from Task 1; the rendered file from Task 2 at `/etc/mwan/network.json`.
- Produces: `yangpub.LoadSchema(searchDir string) (*yangpub.Schema, error)`, `(*yangpub.Schema).ValidateConfigJSON(data []byte) error`, `(*yangpub.Schema).Close()`.
- Produces: `networkjson.DefaultPath = "/etc/mwan/network.json"`, `networkjson.DefaultSchemaDir = "/usr/local/share/wanconfig/yang"`, `networkjson.Load(path string, schemaDir string) (*networkjson.Config, error)`, and `(*networkjson.Config).Apply(cfg *config.Config)`.
- Produces: `networkjson.Config` with fields `InternalPrefix string`, `OpnsenseEdgeV6 string`, `MwanbrEdgeV6 string`, `InternalIface string`, `InternalNetV4 string`, `ProbeTimeoutMillis int`, `WAN map[string]config.IfMgrWANEntry`, `Health map[string]config.IfMgrHealthWANSection`.
- Produces: the renamed config fields `config.IfMgrHealthSection.ProbeTimeoutMillis int` and `config.IfMgrHealthWANSection.CheckIntervalSeconds int`, which Task 5's tests read.

- [ ] **Step 1: Write the failing loader test**

Create `mwan/go/internal/networkjson/networkjson_test.go`:

```go
//go:build linux

package networkjson_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkjson"
)

// schemaDirForTest assembles the model set the gateway installs into a
// temporary directory: the vendored IETF modules at the revisions the deploy
// copies, plus the repository's steering module at whatever revision it
// currently carries, so a revision bump does not touch this test.
func schemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sources := []string{
		"../../../../third_party/yang/standard/ietf/RFC/ietf-yang-types@2025-12-22.yang",
		"../../../../third_party/yang/standard/ietf/RFC/ietf-inet-types@2025-12-22.yang",
		"../../../../third_party/yang/standard/ietf/RFC/iana-if-type@2014-05-08.yang",
		"../../../../third_party/yang/standard/ietf/RFC/ietf-interfaces@2018-02-20.yang",
		"../../../../third_party/yang/standard/ietf/RFC/ietf-ip@2018-02-22.yang",
		"../../../../third_party/yang/standard/ietf/RFC/ietf-nat@2019-01-10.yang",
	}
	matches, err := filepath.Glob("../../../yang/goodkind-mwan-steering@*.yang")
	if err != nil {
		t.Fatalf("glob steering model: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one steering model, found %d", len(matches))
	}
	for _, source := range append(sources, matches[0]) {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		target := filepath.Join(dir, filepath.Base(source))
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	return dir
}

// validDocument is one gateway's network tree: two providers, one of them with
// an IPv4 source pin and one with no probe at all, plus the internal link and
// the group-wide values. Addresses are documentation prefixes.
const validDocument = `{
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
        }
      },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }
    ],
    "goodkind-mwan-steering:steering-group": {
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

// writeDocument puts body in a file the loader can read.
func writeDocument(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

func TestLoadValidFile(t *testing.T) {
	t.Parallel()

	loaded, err := networkjson.Load(writeDocument(t, validDocument), schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.WAN); got != 2 {
		t.Fatalf("provider count = %d, want 2", got)
	}
	if got := loaded.WAN["webpass"].Iface; got != "enwebpass0" {
		t.Fatalf("webpass iface = %q, want enwebpass0", got)
	}
	if got := loaded.WAN["webpass"].TableID; got != 200 {
		t.Fatalf("webpass table id = %d, want 200", got)
	}
	if got := loaded.WAN["webpass"].V4Source; got != "203.0.113.2" {
		t.Fatalf("webpass v4 source = %q, want 203.0.113.2", got)
	}
	if got := loaded.WAN["att"].V4Source; got != "" {
		t.Fatalf("att v4 source = %q, want empty", got)
	}
	if _, probed := loaded.Health["att"]; probed {
		t.Fatal("att carries no health container and must hold no probe")
	}
	if got := loaded.Health["webpass"].CheckIntervalSeconds; got != 10 {
		t.Fatalf("webpass check interval = %d, want 10", got)
	}
	if got := loaded.Health["webpass"].SuccessThreshold; got != 2 {
		t.Fatalf("webpass success threshold = %d, want 2", got)
	}
	if got := loaded.ProbeTimeoutMillis; got != 2000 {
		t.Fatalf("probe timeout = %d, want 2000", got)
	}
	if got := loaded.InternalPrefix; got != "2001:db8:b01::/60" {
		t.Fatalf("internal prefix = %q, want 2001:db8:b01::/60", got)
	}
	if got := loaded.InternalIface; got != "enmwanbr0" {
		t.Fatalf("internal iface = %q, want enmwanbr0", got)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := networkjson.Load(writeDocument(t, "{not json"), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a file that is not JSON")
	}
}

func TestLoadRejectsSchemaViolation(t *testing.T) {
	t.Parallel()

	// The schema bounds fw-mark at 1 or higher, which is the check the daemon
	// makes on every provider today.
	body := strings.Replace(validDocument, `"fw-mark": 2,`, `"fw-mark": 0,`, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a zero firewall mark")
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	t.Parallel()

	_, err := networkjson.Load(filepath.Join(t.TempDir(), "absent.json"), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a missing file")
	}
}

func TestLoadRejectsMissingRequiredLeaf(t *testing.T) {
	t.Parallel()

	// The schema leaves table-id optional, because a leaf's type cannot see
	// whether the daemon needs it. The loader is where that requirement lives,
	// so an absent table id must fail rather than default to zero.
	body := strings.Replace(validDocument, `"table-id": 100,`, ``, 1)
	_, err := networkjson.Load(writeDocument(t, body), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted a provider with no table id")
	}
	if !strings.Contains(err.Error(), "table-id") {
		t.Fatalf("error does not name the missing leaf: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```bash
cd "$(git rev-parse --show-toplevel)/mwan/go" && make wanconfig-builder-image
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/networkjson/ -v
```

Expected: FAIL to build, with `no required module provides package
goodkind.io/mwan/internal/networkjson`.

- [ ] **Step 3: Write the libyang schema binding**

Create `mwan/go/internal/yangpub/schema_cgo.go`:

```go
//go:build linux && cgo

package yangpub

/*
#cgo pkg-config: libyang
#include <stdlib.h>
#include <libyang/libyang.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// ErrSchemaClosed means the schema was already closed. A closed handle is
// rejected rather than handed back to libyang as a freed context.
var ErrSchemaClosed = errors.New("yangpub: schema is closed")

// A configuration instance is read strictly, so a node the schema does not
// define is an error rather than a silently ignored key, and state nodes are
// refused. Present validation checks the subtrees the file carries rather than
// a whole datastore, so a mandatory leaf inside a present container is enforced
// while an absent container is not. Together these are what yanglint runs as
// `-t config`, which is what the deploy checks the same file with.
const (
	schemaParseOptions    = C.LYD_PARSE_STRICT | C.LYD_PARSE_NO_STATE
	schemaValidateOptions = C.LYD_VALIDATE_PRESENT | C.LYD_VALIDATE_NO_STATE
)

// Schema is a libyang context holding the modules the gateway's configuration
// is written against.
type Schema struct {
	ctx *C.struct_ly_ctx
}

// schemaModule is one module loaded by name, with the features that must be
// enabled for its types to carry values.
type schemaModule struct {
	name     string
	features []string
}

// schemaModules names the modules a configuration instance needs beyond what
// imports pull in. The interface-type registry is here because only the data
// references it, through the mandatory type leaf. ietf-nat is here because its
// enums are feature-gated and a module loaded with no features has leaves with
// no valid value, which libyang rejects; loading it first with the features the
// deploy enables means the steering module's import finds it already enabled.
// ietf-interfaces, ietf-ip, ietf-inet-types, and ietf-yang-types resolve as
// imports from the same directory.
var schemaModules = []schemaModule{
	{name: "iana-if-type", features: nil},
	{name: "ietf-nat", features: []string{"basic-nat44", "napt44", "dst-nat", "nptv6"}},
	{name: "goodkind-mwan-steering", features: nil},
}

// LoadSchema builds a context from the model files in searchDir, the directory
// the deploy installs the gateway's models into. Each module is loaded without a
// revision, because that directory holds exactly one file per module and the
// revision then has one home. The caller closes the result.
func LoadSchema(searchDir string) (*Schema, error) {
	cSearchDir := C.CString(searchDir)
	defer C.free(unsafe.Pointer(cSearchDir))

	var ctx *C.struct_ly_ctx
	newErr := C.ly_ctx_new(cSearchDir, C.uint16_t(C.LY_CTX_NO_YANGLIBRARY), &ctx)
	if lyFailed(newErr) {
		return nil, fmt.Errorf("yangpub: ly_ctx_new %s: libyang code %d", searchDir, int(newErr))
	}
	for _, module := range schemaModules {
		if err := loadSchemaModule(ctx, module); err != nil {
			C.ly_ctx_destroy(ctx)
			return nil, err
		}
	}
	return &Schema{ctx: ctx}, nil
}

// loadSchemaModule loads one module from the context's search directory,
// handing libyang a NULL-terminated array of feature names or NULL when the
// module needs none.
func loadSchemaModule(ctx *C.struct_ly_ctx, module schemaModule) error {
	cName := C.CString(module.name)
	defer C.free(unsafe.Pointer(cName))

	var cFeatures **C.char
	if len(module.features) > 0 {
		slotSize := C.size_t(unsafe.Sizeof((*C.char)(nil)))
		block := C.malloc(slotSize * C.size_t(len(module.features)+1))
		defer C.free(block)
		slots := unsafe.Slice((**C.char)(block), len(module.features)+1)
		for i, feature := range module.features {
			slots[i] = C.CString(feature)
			defer C.free(unsafe.Pointer(slots[i]))
		}
		slots[len(module.features)] = nil
		cFeatures = (**C.char)(block)
	}
	if loaded := C.ly_ctx_load_module(ctx, cName, nil, cFeatures); loaded == nil {
		return fmt.Errorf("yangpub: load module %s: %s", module.name, lastSchemaError(ctx))
	}
	return nil
}

// ValidateConfigJSON validates data as a configuration instance of the loaded
// modules and returns the first violation libyang reports.
func (s *Schema) ValidateConfigJSON(data []byte) error {
	if s == nil || s.ctx == nil {
		return ErrSchemaClosed
	}
	cData := C.CString(string(data))
	defer C.free(unsafe.Pointer(cData))

	var tree *C.struct_lyd_node
	parseErr := C.lyd_parse_data_mem(s.ctx, cData, C.LYD_JSON,
		C.uint32_t(schemaParseOptions), C.uint32_t(schemaValidateOptions), &tree)
	if tree != nil {
		C.lyd_free_all(tree)
	}
	if lyFailed(parseErr) {
		return fmt.Errorf("yangpub: %s", lastSchemaError(s.ctx))
	}
	return nil
}

// lastSchemaError renders libyang's most recent message, so a rejection never
// returns an empty reason.
func lastSchemaError(ctx *C.struct_ly_ctx) string {
	item := C.ly_err_last(ctx)
	if item == nil || item.msg == nil {
		return "libyang reported no message"
	}
	return C.GoString(item.msg)
}

// Close frees the context. A second call is a no-op.
func (s *Schema) Close() {
	if s == nil || s.ctx == nil {
		return
	}
	C.ly_ctx_destroy(s.ctx)
	s.ctx = nil
}
```

If a symbol or a struct field does not compile, read the pinned headers rather
than guessing. `make go-mk-cgo-dep-libyang` installs them under
`$(GO_MK_CGO_PREFIX)/include/libyang/`, and the version pin is
`wanconfig_libyang_version` in `ansible/inventory/group_vars/all/vars.yml`.

- [ ] **Step 4: Write the cgo-less stub**

Create `mwan/go/internal/yangpub/schema_stub.go`:

```go
//go:build linux && !cgo

package yangpub

// Schema is the handle a cgo-less build cannot provide. It exists so the
// package's callers compile in a development build; the release guard refuses
// to ship this variant.
type Schema struct{}

// LoadSchema reports the binding unavailable: this linux binary was built with
// cgo off, so libyang is not linked.
func LoadSchema(_ string) (*Schema, error) {
	return nil, ErrUnavailable
}

// ValidateConfigJSON reports the binding unavailable, so a caller never treats
// an unvalidated file as validated.
func (s *Schema) ValidateConfigJSON(_ []byte) error {
	return ErrUnavailable
}

// Close is a no-op: a stub holds no context.
func (s *Schema) Close() {}
```

- [ ] **Step 5: Write the loader**

Create `mwan/go/internal/networkjson/networkjson.go`:

```go
//go:build linux

// Package networkjson loads the gateway's network configuration: the provider
// inventory, each provider's routing slots, translation prefix, source pin, and
// health probe, and the group-wide translation, internal link, and probe
// timeout. The file is written in the model's own JSON encoding and validated
// against the installed schema before any value is read, so the file the daemon
// loads and the tree the management surface serves describe one thing.
//
// The package is linux-only: validation binds libyang, which only the linux
// build links, and the only role that reads the file runs there.
package networkjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/yangpub"
)

// DefaultPath is where the deploy writes the network configuration.
const DefaultPath = "/etc/mwan/network.json"

// DefaultSchemaDir is where the wanconfig stack deploy installs the model
// files. The deploy validates the rendered file against the same files before
// it lands here, so one schema serves both checkpoints.
const DefaultSchemaDir = "/usr/local/share/wanconfig/yang"

// document mirrors the model's JSON encoding. Every scalar the daemon needs is
// a pointer, so an absent leaf is distinguishable from a zero and can be
// rejected rather than defaulted.
type document struct {
	Interfaces interfaces `json:"ietf-interfaces:interfaces"`
}

type interfaces struct {
	Interface     []ifaceEntry  `json:"interface"`
	SteeringGroup steeringGroup `json:"goodkind-mwan-steering:steering-group"`
}

type ifaceEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	WAN  *wan   `json:"goodkind-mwan-steering:wan"`
}

type wan struct {
	Name       string  `json:"name"`
	TableID    *int    `json:"table-id"`
	FwMark     *int    `json:"fw-mark"`
	FwMarkPrio *int    `json:"fw-mark-prio"`
	FromPrio   *int    `json:"from-prio"`
	NptPrefix  string  `json:"npt-prefix"`
	V4Source   string  `json:"v4-source"`
	Health     *health `json:"health"`
}

type health struct {
	Enabled           *bool    `json:"enabled"`
	PingCount         *int     `json:"ping-count"`
	SuccessThreshold  *int     `json:"success-threshold"`
	FailureThreshold  *int     `json:"failure-threshold"`
	RecoveryThreshold *int     `json:"recovery-threshold"`
	CheckInterval     *int     `json:"check-interval"`
	TargetsV4         []string `json:"targets-v4"`
	TargetsV6         []string `json:"targets-v6"`
	HTTPURLs          []string `json:"http-urls"`
}

type steeringGroup struct {
	Translation translation `json:"translation"`
	Routes      routes      `json:"routes"`
	Health      groupHealth `json:"health"`
}

type translation struct {
	InternalPrefix string `json:"internal-prefix"`
	OpnsenseEdgeV6 string `json:"opnsense-edge-v6"`
	MwanbrEdgeV6   string `json:"mwanbr-edge-v6"`
}

type routes struct {
	InternalIface string `json:"internal-iface"`
	InternalNetV4 string `json:"internal-net-v4"`
}

type groupHealth struct {
	ProbeTimeout *int `json:"probe-timeout"`
}

// Config is the network tree one file carries, in the shape the daemon's
// configuration holds it.
type Config struct {
	InternalPrefix     string
	OpnsenseEdgeV6     string
	MwanbrEdgeV6       string
	InternalIface      string
	InternalNetV4      string
	ProbeTimeoutMillis int
	WAN                map[string]config.IfMgrWANEntry
	Health             map[string]config.IfMgrHealthWANSection
}

// Load reads path, validates it against the models in schemaDir, and returns
// the network tree it carries. Every failure is fatal to startup: an unreadable
// file, a file the schema rejects, and a missing value are all configuration
// errors, and none of them has a safe default.
func Load(path string, schemaDir string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	schema, err := yangpub.LoadSchema(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("load schema from %s: %w", schemaDir, err)
	}
	defer schema.Close()
	if err := schema.ValidateConfigJSON(data); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	loaded, err := build(&doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return loaded, nil
}

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
	return loaded, nil
}

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
	routing := config.IfMgrWANEntry{
		Iface:      entry.Name,
		TableID:    *provider.TableID,
		FwMark:     *provider.FwMark,
		FwMarkPrio: *provider.FwMarkPrio,
		FromPrio:   *provider.FromPrio,
		NptPrefix:  provider.NptPrefix,
		V4Source:   provider.V4Source,
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

// buildHealth reads one provider's probe. Every setting is required, matching
// the daemon's rule that an enabled provider fully specifies its policy rather
// than inheriting a module-wide default.
func buildHealth(label string, probe *health) (*config.IfMgrHealthWANSection, error) {
	counts := []struct {
		leaf  string
		value *int
	}{
		{leaf: "ping-count", value: probe.PingCount},
		{leaf: "success-threshold", value: probe.SuccessThreshold},
		{leaf: "failure-threshold", value: probe.FailureThreshold},
		{leaf: "recovery-threshold", value: probe.RecoveryThreshold},
		{leaf: "check-interval", value: probe.CheckInterval},
	}
	for _, count := range counts {
		if count.value == nil {
			return nil, fmt.Errorf("%s: health/%s is required", label, count.leaf)
		}
	}
	if probe.Enabled == nil {
		return nil, fmt.Errorf("%s: health/enabled is required", label)
	}
	return &config.IfMgrHealthWANSection{
		Enabled:              *probe.Enabled,
		PingCount:            *probe.PingCount,
		SuccessThreshold:     *probe.SuccessThreshold,
		CheckIntervalSeconds: *probe.CheckInterval,
		FailureThreshold:     *probe.FailureThreshold,
		RecoveryThreshold:    *probe.RecoveryThreshold,
		TargetsV4:            probe.TargetsV4,
		TargetsV6:            probe.TargetsV6,
		HTTPURLs:             probe.HTTPURLs,
	}, nil
}

// Apply writes the loaded tree onto cfg, filling the fields the TOML sections
// filled before this file owned them. The health and routes sections keep the
// filesystem paths TOML still carries, so only the network values are written.
func (c *Config) Apply(cfg *config.Config) {
	cfg.IfMgr.InternalPrefix = c.InternalPrefix
	cfg.IfMgr.OpnsenseEdgeV6 = c.OpnsenseEdgeV6
	cfg.IfMgr.MwanbrEdgeV6 = c.MwanbrEdgeV6
	cfg.IfMgr.WAN = c.WAN

	if cfg.IfMgr.Modules.WAN == nil {
		cfg.IfMgr.Modules.WAN = &config.IfMgrModulesWANSection{Routes: nil}
	}
	if cfg.IfMgr.Modules.WAN.Routes == nil {
		cfg.IfMgr.Modules.WAN.Routes = &config.IfMgrWANRoutesSection{
			InternalIface:   "",
			InternalNetV4:   "",
			HealthStateFile: "",
		}
	}
	cfg.IfMgr.Modules.WAN.Routes.InternalIface = c.InternalIface
	cfg.IfMgr.Modules.WAN.Routes.InternalNetV4 = c.InternalNetV4

	if cfg.IfMgr.Modules.Health == nil {
		cfg.IfMgr.Modules.Health = &config.IfMgrHealthSection{
			StateFile:          "",
			PersistStateFile:   "",
			ProbeTimeoutMillis: 0,
			WAN:                nil,
		}
	}
	cfg.IfMgr.Modules.Health.ProbeTimeoutMillis = c.ProbeTimeoutMillis
	cfg.IfMgr.Modules.Health.WAN = c.Health
}
```

- [ ] **Step 6: Retag and retype the configuration structs**

In `mwan/go/internal/config/config.go`, replace these three `IfMgrSection`
fields:

```go
	InternalPrefix    string                       `toml:"internal_prefix"`
	OpnsenseEdgeV6    string                       `toml:"opnsense_edge_v6"`
	MwanbrEdgeV6      string                       `toml:"mwanbr_edge_v6"`
```

with:

```go
	// These three translation values and the WAN map below come from
	// /etc/mwan/network.json. The skip tag is what stops the decoder reading a
	// stale key out of a config.toml that still carries one, so exactly one
	// file owns them at every moment.
	InternalPrefix    string                       `toml:"-"`
	OpnsenseEdgeV6    string                       `toml:"-"`
	MwanbrEdgeV6      string                       `toml:"-"`
```

and replace:

```go
	WAN               map[string]IfMgrWANEntry     `toml:"wan"`
```

with:

```go
	WAN               map[string]IfMgrWANEntry     `toml:"-"`
```

In `mwan/go/internal/config/ifmgr_modules.go`, replace `IfMgrWANEntry`'s doc
comment and body with:

```go
// IfMgrWANEntry is one provider's routing configuration, keyed by provider
// name. It comes from network.json: the interface the provider rides plus the
// policy-routing slots wan.routes owns. Modules read the fields they need; npt
// uses only the name and interface. The shared internal prefix and edge
// addresses live on IfMgrSection, because no single provider owns them.
type IfMgrWANEntry struct {
	Iface      string
	TableID    int
	FwMark     int
	FwMarkPrio int
	FromPrio   int
	NptPrefix  string
	V4Source   string
}
```

Replace `IfMgrWANRoutesSection` with:

```go
// IfMgrWANRoutesSection is the [ifmgr.modules.wan.routes] table. The health
// state file is a filesystem path and stays in TOML; the internal link and
// network are network values and come from network.json.
type IfMgrWANRoutesSection struct {
	InternalIface   string `toml:"-"`
	InternalNetV4   string `toml:"-"`
	HealthStateFile string `toml:"health_state_file"`
}
```

Replace `IfMgrHealthSection` with:

```go
// IfMgrHealthSection keeps the module's two state-file paths, which stay in
// TOML, beside the probe timeout and the per-provider policy, which come from
// network.json. The timeout is milliseconds because that is the unit the model
// carries it in, and converting it back into a duration string would be the
// value conversion this format change removes.
type IfMgrHealthSection struct {
	StateFile          string                           `toml:"state_file"`
	PersistStateFile   string                           `toml:"persist_state_file"`
	ProbeTimeoutMillis int                              `toml:"-"`
	WAN                map[string]IfMgrHealthWANSection `toml:"-"`
}
```

Replace `IfMgrHealthWANSection` with:

```go
// IfMgrHealthWANSection is one provider's probe policy, read from network.json.
// The interval is seconds because that is the unit the model carries it in.
type IfMgrHealthWANSection struct {
	Enabled              bool
	PingCount            int
	SuccessThreshold     int
	CheckIntervalSeconds int
	FailureThreshold     int
	RecoveryThreshold    int
	TargetsV4            []string
	TargetsV6            []string
	HTTPURLs             []string
}
```

- [ ] **Step 7: Read the two spans as the integers they now are**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux.go`, replace this block in
`buildHealthConfig`:

```go
	var err error
	cfg.Timeout, err = parseDurationSetting(
		section.Timeout,
		0,
		"ifmgr.modules.health.timeout",
	)
	if err != nil {
		return health.Config{}, err
	}
```

with:

```go
	cfg.Timeout = time.Duration(section.ProbeTimeoutMillis) * time.Millisecond

	var err error
```

and replace this block further down the same function:

```go
		healthWAN.CheckInterval, err = parseDurationSetting(
			wanSection.CheckInterval,
			0,
			fieldPrefix+".check_interval",
		)
		if err != nil {
			return health.Config{}, err
		}
```

with:

```go
		healthWAN.CheckInterval = time.Duration(wanSection.CheckIntervalSeconds) * time.Second
```

- [ ] **Step 8: Load the file at ifmgr startup**

In `mwan/go/cmd/mwan/ifmgr_linux.go`, add `"goodkind.io/mwan/internal/networkjson"`
to the existing import block, then insert this call directly after the
`logger.Info("ifmgr: starting", ...)` call and before
`dcfg, err := buildIfMgrDaemonConfig(cfg, role)`:

```go
	if err := loadNetworkConfig(cfg, role); err != nil {
		logger.Error("ifmgr: network configuration unusable", "err", err)
		return err
	}
```

and add these two functions at the end of the file:

```go
// loadNetworkConfig fills the network tree from the gateway's network
// configuration file for a role that steers providers. Any other role leaves
// cfg untouched: the file describes providers, and a role that steers none has
// nothing to read. A role that does steer them cannot start without it, which
// is the contract a bad configuration has always had.
func loadNetworkConfig(cfg *config.Config, role string) error {
	steers, err := roleSteersProviders(role)
	if err != nil {
		return err
	}
	if !steers {
		return nil
	}
	loaded, err := networkjson.Load(networkjson.DefaultPath, networkjson.DefaultSchemaDir)
	if err != nil {
		return fmt.Errorf("load network configuration: %w", err)
	}
	loaded.Apply(cfg)
	return nil
}

// roleSteersProviders reports whether role runs the modules the network
// configuration file configures. wan.routes is the module that programs the
// per-provider routes and rules, so its presence is what makes the file
// required.
func roleSteersProviders(role string) (bool, error) {
	names, err := ifmgr.ModulesForRole(role)
	if err != nil {
		return false, fmt.Errorf("ModulesForRole(%q): %w", role, err)
	}
	for _, name := range names {
		if name == "wan.routes" {
			return true, nil
		}
	}
	return false, nil
}
```

- [ ] **Step 9: Run the loader test to verify it passes**

Run:

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./internal/networkjson/ -v
```

Expected: PASS. All five tests report `ok`.

- [ ] **Step 10: Rewrite the round-trip test around the two files**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go`, replace
`TestIfMgrWANConfigRoundTrips` and its doc comment with:

```go
// TestNetworkConfigOwnsTheNetworkTree drives the real two-file load path. A
// config.toml that still carries the legacy network keys must contribute none
// of them, and the network tree must reach the module configs from the JSON
// loader's output instead. A render-versus-schema mismatch that struct-built
// fixtures cannot catch fails here rather than in production.
func TestNetworkConfigOwnsTheNetworkTree(t *testing.T) {
	t.Parallel()

	// Every network key below is one a pre-cutover config.toml carries. None of
	// them may reach the parsed config.
	const configTOML = `
[ifmgr]
role = "wan"
internal_prefix = "3d06:bad:b01:999::/60"
opnsense_edge_v6 = "3d06:bad:b01:999::2"
mwanbr_edge_v6 = "3d06:bad:b01:999::3"

[ifmgr.iface.enmbrains0]
name = "enmbrains0"

[ifmgr.wan.att]
iface = "stale0"
table_id = 999

[ifmgr.modules.wan.routes]
internal_iface = "stale0"
internal_net_v4 = "192.0.2.240/29"
health_state_file = "/run/mwan-health.state"

[ifmgr.modules.health]
state_file = "/run/mwan-health.state"
persist_state_file = "/var/lib/mwan/health-state"
timeout = "2s"

[ifmgr.modules.health.wan.att]
enabled = true
ping_count = 99
`
	var cfg config.Config
	if err := toml.Unmarshal([]byte(configTOML), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}
	if len(cfg.IfMgr.WAN) != 0 {
		t.Fatalf("[ifmgr.wan] still decodes from TOML: %#v", cfg.IfMgr.WAN)
	}
	if cfg.IfMgr.InternalPrefix != "" {
		t.Fatalf("internal_prefix still decodes from TOML: %q", cfg.IfMgr.InternalPrefix)
	}
	if cfg.IfMgr.Modules.WAN.Routes.InternalIface != "" {
		t.Fatalf("internal_iface still decodes from TOML: %q",
			cfg.IfMgr.Modules.WAN.Routes.InternalIface)
	}
	if len(cfg.IfMgr.Modules.Health.WAN) != 0 {
		t.Fatalf("[ifmgr.modules.health.wan] still decodes from TOML: %#v",
			cfg.IfMgr.Modules.Health.WAN)
	}
	// The two paths the network file deliberately does not carry must survive.
	if cfg.IfMgr.Modules.Health.StateFile != "/run/mwan-health.state" {
		t.Fatalf("health state_file did not parse: %q", cfg.IfMgr.Modules.Health.StateFile)
	}
	if cfg.IfMgr.Modules.WAN.Routes.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf("wan.routes health_state_file did not parse: %q",
			cfg.IfMgr.Modules.WAN.Routes.HealthStateFile)
	}

	network := networkjson.Config{
		InternalPrefix:     "3d06:bad:b01:210::/60",
		OpnsenseEdgeV6:     "3d06:bad:b01:201::2",
		MwanbrEdgeV6:       "3d06:bad:b01:201::3",
		InternalIface:      "enmwanbr0",
		InternalNetV4:      "192.0.2.0/29",
		ProbeTimeoutMillis: 2000,
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:      "enatt0",
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:2300::/60",
				V4Source:   "",
			},
			"webpass": {
				Iface:      "enwebpass0",
				TableID:    200,
				FwMark:     2,
				FwMarkPrio: 200,
				FromPrio:   56,
				NptPrefix:  "3d06:bad:b01:2200::/60",
				V4Source:   "10.240.204.2",
			},
		},
		Health: map[string]config.IfMgrHealthWANSection{
			"att": {
				Enabled:              true,
				PingCount:            3,
				SuccessThreshold:     2,
				CheckIntervalSeconds: 10,
				FailureThreshold:     2,
				RecoveryThreshold:    2,
				TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
				TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
				HTTPURLs:             []string{"https://ifconfig.co/ip"},
			},
			"webpass": {
				Enabled:              true,
				PingCount:            3,
				SuccessThreshold:     2,
				CheckIntervalSeconds: 30,
				FailureThreshold:     2,
				RecoveryThreshold:    2,
				TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
				TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
				HTTPURLs:             []string{"https://ifconfig.co/ip"},
			},
		},
	}
	network.Apply(&cfg)

	set, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan): %v", err)
	}
	wr, ok := set["wan.routes"].(wanroutes.Config)
	if !ok {
		t.Fatalf("wan.routes config missing or wrong type: %T", set["wan.routes"])
	}
	if wr.InternalIface != "enmwanbr0" {
		t.Fatalf("wan.routes internal iface = %q, want enmwanbr0", wr.InternalIface)
	}
	if wr.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf("wan.routes health state file = %q, want /run/mwan-health.state",
			wr.HealthStateFile)
	}
	byName := map[string]wanroutes.WAN{}
	for _, w := range wr.WANs {
		byName[w.Name] = w
	}
	if byName["att"].Iface != "enatt0" || byName["webpass"].Iface != "enwebpass0" {
		t.Fatalf("wan.routes ifaces did not resolve from the network file: %#v", byName)
	}
	if byName["att"].TableID != 100 || byName["webpass"].V4Source != "10.240.204.2" {
		t.Fatalf("wan.routes routing fields did not resolve from the network file: %#v", byName)
	}
	hc, ok := set["health"].(health.Config)
	if !ok {
		t.Fatalf("health config missing or wrong type: %T", set["health"])
	}
	if hc.Timeout != 2*time.Second {
		t.Fatalf("health timeout = %s, want 2s", hc.Timeout)
	}
	if hc.Interval != 10*time.Second {
		t.Fatalf("health interval = %s, want the shortest provider interval 10s", hc.Interval)
	}
	if _, ok := set["npt"]; !ok {
		t.Fatal("wan role must build an npt config from the two-file load")
	}
}
```

Add `"goodkind.io/mwan/internal/networkjson"` to that file's import block if it
is not already there, and keep the existing `health`, `wanroutes`, `time`, and
`toml` imports.

- [ ] **Step 11: Run the command-package tests to verify they pass**

Run:

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./cmd/mwan/ ./internal/config/ -v -run 'Network|IfMgr|Health|WAN'
```

Expected: PASS. `TestNetworkConfigOwnsTheNetworkTree` passes, and the existing
`TestBuildWANRefs`, `TestBuildWANRoutesConfig`, and health builder tests pass
with the retyped fields. Any test that still sets `CheckInterval: "10s"` or
`Timeout: "2s"` fails to compile; change it to `CheckIntervalSeconds: 10` and
`ProbeTimeoutMillis: 2000`.

- [ ] **Step 12: Ship the interface-type registry to the gateway**

In `ansible/playbooks/deploy-wanconfig-stack.yml`, add one entry to the "Copy
the gateway model and its IETF imports" loop, directly before the steering
module line:

```yaml
        - "../../third_party/yang/standard/ietf/RFC/iana-if-type@2014-05-08.yang"
```

and extend that task's preceding comment block with:

```yaml
    # The interface-type registry rides along because the daemon validates its
    # own network configuration against this directory, and a data instance
    # naming an interface must give it a type from that registry. sysrepo gets
    # its copy from rousette's directory below; this one is for the daemon.
```

- [ ] **Step 13: Run the whole gate**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make check`
Expected: PASS. The YANG gates, the lint gates for both platforms, and the test
suite all exit 0.

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:all`
Expected: PASS, exit 0.

- [ ] **Step 14: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/go/internal/yangpub/schema_cgo.go mwan/go/internal/yangpub/schema_stub.go mwan/go/internal/networkjson/ mwan/go/internal/config/config.go mwan/go/internal/config/ifmgr_modules.go mwan/go/cmd/mwan/ifmgr_linux.go mwan/go/cmd/mwan/ifmgr_module_configs_linux.go mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go ansible/playbooks/deploy-wanconfig-stack.yml
git commit -S -m "Load the gateway network tree from /etc/mwan/network.json" -m "Add a libyang context and configuration validator to yangpub, decode the validated document in internal/networkjson into the ifmgr WAN map, translation values, routes, and per-provider health, call it from the ifmgr entry point for a role that steers providers, mark the matching TOML fields skipped so exactly one file owns each section, retype the two spans to the units the model carries, and copy the interface-type registry onto the gateway." -m "Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 4 (MWAN-349): Deploy-time validation

The deploy validates the rendered file against the schema before it reaches the
gateway. That needs the rendered bytes on the controller, so the single template
task from Task 2 becomes three: render locally, validate, copy. The copied file
is the validated file, so nothing renders twice and no unvalidated bytes reach
the gateway.

The spec's Failure modes section says the deploy validates with the schema staged
from the release. Nothing stages a schema there. The release stages the binaries
under `mwan_release_dir` and the wanconfig stack bundle under
`wanconfig_stack_dir`, while the model files are copied to the gateway from the
repository checkout by `ansible/playbooks/deploy-wanconfig-stack.yml:122-136`. The
controller therefore validates with the repository's model files, which are the
same files that play installs on the gateway and the same files the daemon
validates with at load. That satisfies the constraint the spec states, one schema
and two checkpoints with zero copies; only the spec's account of where the files
come from is stale.

The steering model is found by glob rather than named, so a revision bump has one
home in `mwan/yang/` and needs no matching edit here.

**Files:**
- Modify: `ansible/playbooks/deploy-mwan.yml:28-31` (play `vars`)
- Modify: `ansible/playbooks/deploy-mwan.yml` (replace the "Deploy MWAN network configuration" task added in Task 2 with four tasks)

**Interfaces:**
- Consumes: `mwan/config/network.json.j2` and the inventory variables from Task 2; the model files from Task 1.
- Produces: the play variables `mwan_network_json_local` (controller-side path of the rendered file) and `mwan_steering_model` (absolute path of the current steering model file); the validated `/etc/mwan/network.json` on the gateway.

- [ ] **Step 1: Add the two play variables**

In `ansible/playbooks/deploy-mwan.yml`, extend the play's `vars` block from:

```yaml
  vars:
    mwan_config_dir: /etc/mwan
    repo_root: "{{ playbook_dir | dirname | dirname }}"
```

to:

```yaml
  vars:
    mwan_config_dir: /etc/mwan
    repo_root: "{{ playbook_dir | dirname | dirname }}"
    # The network configuration is rendered here first so the schema check runs
    # on the exact bytes the gateway receives. One file per host, because a play
    # against both gateways renders two different trees.
    mwan_network_json_local: "{{ repo_root }}/.cache/network-{{ inventory_hostname }}.json"
    # The model file carries its revision in its name and the directory holds
    # exactly one, so a revision bump keeps one home.
    mwan_steering_model: >-
      {{ query('fileglob', repo_root ~ '/mwan/yang/goodkind-mwan-steering@*.yang') | first }}
```

- [ ] **Step 2: Replace the render task with render, validate, and copy**

In `ansible/playbooks/deploy-mwan.yml`, replace the whole "Deploy MWAN network
configuration" task added in Task 2 with these four tasks, in this order:

```yaml
    - name: Ensure the controller render directory exists
      delegate_to: localhost
      become: false
      ansible.builtin.file:
        path: "{{ repo_root }}/.cache"
        state: directory
        mode: "0755"
      when: mwan_ifmgr_wan_enabled | bool

    # Rendered on the controller, not on the gateway, so the schema check below
    # runs before anything reaches the target.
    - name: Render the MWAN network configuration on the controller
      delegate_to: localhost
      become: false
      ansible.builtin.template:
        src: "{{ repo_root }}/mwan/config/network.json.j2"
        dest: "{{ mwan_network_json_local }}"
        mode: "0600"
      when: mwan_ifmgr_wan_enabled | bool

    # The same model files this repository copies onto the gateway, checked with
    # yanglint the way the daemon checks the file with libyang at startup. This
    # is the pre-flight validation that replaces the ruleset parse the firewall
    # work removes, so a rejected render fails the play with the gateway
    # untouched.
    - name: Validate the rendered network configuration against the schema
      delegate_to: localhost
      become: false
      ansible.builtin.command:
        argv:
          - yanglint
          - -t
          - config
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/ietf-yang-types@2025-12-22.yang"
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/ietf-inet-types@2025-12-22.yang"
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/iana-if-type@2014-05-08.yang"
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/ietf-interfaces@2018-02-20.yang"
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/ietf-ip@2018-02-22.yang"
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/ietf-routing@2018-03-13.yang"
          - "{{ repo_root }}/third_party/yang/standard/ietf/RFC/ietf-nat@2019-01-10.yang"
          - "{{ mwan_steering_model }}"
          - "{{ mwan_network_json_local }}"
      register: mwan_network_json_validate
      changed_when: false
      failed_when: mwan_network_json_validate.rc != 0
      when: mwan_ifmgr_wan_enabled | bool

    # The validated bytes, not a second render.
    - name: Deploy MWAN network configuration
      ansible.builtin.copy:
        src: "{{ mwan_network_json_local }}"
        dest: /etc/mwan/network.json
        mode: "0600"
      when: mwan_ifmgr_wan_enabled | bool
      notify: Restart mwan-ifmgr@wan
```

- [ ] **Step 3: Verify the play parses**

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:mwan`
Expected: PASS, exit 0.

- [ ] **Step 4: Prove the check rejects a bad render, by hand on the testbed**

This is the breakage probe the spec requires. Run it once, on the testbed only,
and record the output in the ticket.

```bash
cd "$(git rev-parse --show-toplevel)"
# 1. Record what the gateway holds now.
ssh mwan.suburban.goodkind.io 'sha256sum /etc/mwan/network.json; stat -c %y /etc/mwan/network.json'
# 2. Break one value the schema bounds: edit
#    ansible/inventory/group_vars/mwan_suburban_servers.yml and set the att
#    entry of mwan_ifmgr_wan_fw_marks to 0.
# 3. Deploy the testbed and watch it fail.
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
# 4. Confirm the gateway is untouched.
ssh mwan.suburban.goodkind.io 'sha256sum /etc/mwan/network.json; stat -c %y /etc/mwan/network.json'
# 5. Revert the inventory edit.
git checkout ansible/inventory/group_vars/mwan_suburban_servers.yml
```

Expected: the play fails at "Validate the rendered network configuration against
the schema" with `Unsatisfied range - value "0" is out of the allowed range` and
a path ending in `goodkind-mwan-steering:wan/fw-mark`. The gateway's checksum and
mtime are unchanged between steps 1 and 4.

- [ ] **Step 5: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add ansible/playbooks/deploy-mwan.yml
git commit -S -m "Validate the rendered network configuration before it reaches the gateway" -m "Render network.json on the controller, run yanglint over it against the same model files the deploy installs on the gateway, and copy the validated bytes rather than rendering a second time, so a configuration the schema rejects fails the play with the gateway untouched." -m "Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 5: The network tree leaves the TOML render

The daemon stopped reading these TOML keys in Task 3. This task stops rendering
them, so the file on disk stops carrying two representations of one thing.

What stays is as important as what goes. `[ifmgr.modules.health]` keeps
`state_file` and `persist_state_file`, and `[ifmgr.modules.wan.routes]` keeps
`health_state_file`. Those are filesystem paths, they are still decoded from
TOML, and the first and third must name the same path. Removing them would leave
the health module with no state file to write and the routing module with no
verdict file to read.

Two templates are deliberately not touched. `mwan/config/config-host.toml.j2`
includes the same `_ifmgr_common.toml.j2`, but the block being deleted there is
already gated on `mwan_ifmgr_wan_enabled`, which
`ansible/inventory/group_vars/all/vars.yml:75` sets false for every host but the
two gateway groups, so the hypervisors render none of it today.
`mwan-failover/config.toml.j2` does not include the common file at all. It writes
its own `[ifmgr]` block at lines 50-54 with no translation prefixes, and it
renders none of the sections this task removes.

**Files:**
- Modify: `mwan/config/config-vm.toml.j2:129-182` (the WAN blocks and the health loop)
- Modify: `mwan/config/_ifmgr_common.toml.j2:6-10` (the three translation values)
- Modify: `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go` (add the post-deletion load test)

**Interfaces:**
- Consumes: the loader and the retyped fields from Task 3; the renderer from Task 2; the deploy validation from Task 4.
- Produces: a `config.toml` on the gateway that carries no network value, and `TestGatewayLoadWithoutNetworkTOML` proving the module configs still build from a TOML that never had them.

- [ ] **Step 1: Write the end-state test**

In `mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go`, add this test after
`TestNetworkConfigOwnsTheNetworkTree`:

```go
// TestGatewayLoadWithoutNetworkTOML is the end state: the gateway's config.toml
// carries no network section at all, and the wan role still builds every module
// config from the network file plus the two state-file paths TOML keeps.
func TestGatewayLoadWithoutNetworkTOML(t *testing.T) {
	t.Parallel()

	const configTOML = `
[ifmgr]
role = "wan"

[ifmgr.iface.enmbrains0]
name = "enmbrains0"

[ifmgr.modules.wan.routes]
health_state_file = "/run/mwan-health.state"

[ifmgr.modules.health]
state_file = "/run/mwan-health.state"
persist_state_file = "/var/lib/mwan/health-state"
`
	var cfg config.Config
	if err := toml.Unmarshal([]byte(configTOML), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}
	network := networkjson.Config{
		InternalPrefix:     "3d06:bad:b01:210::/60",
		OpnsenseEdgeV6:     "3d06:bad:b01:201::2",
		MwanbrEdgeV6:       "3d06:bad:b01:201::3",
		InternalIface:      "enmwanbr0",
		InternalNetV4:      "192.0.2.0/29",
		ProbeTimeoutMillis: 2000,
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:      "enatt0",
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:2300::/60",
				V4Source:   "",
			},
		},
		Health: map[string]config.IfMgrHealthWANSection{
			"att": {
				Enabled:              true,
				PingCount:            3,
				SuccessThreshold:     2,
				CheckIntervalSeconds: 10,
				FailureThreshold:     2,
				RecoveryThreshold:    2,
				TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
				TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
				HTTPURLs:             []string{"https://ifconfig.co/ip"},
			},
		},
	}
	network.Apply(&cfg)

	set, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan): %v", err)
	}
	nptConfig, ok := set["npt"].(npt.Config)
	if !ok {
		t.Fatalf("npt config missing or wrong type: %T", set["npt"])
	}
	if nptConfig.InternalPrefix != "3d06:bad:b01:210::/60" {
		t.Fatalf("npt internal prefix = %q, want the network file's value",
			nptConfig.InternalPrefix)
	}
	if len(nptConfig.WANs) != 1 || nptConfig.WANs[0].Iface != "enatt0" {
		t.Fatalf("npt WAN list did not resolve: %#v", nptConfig.WANs)
	}
	wr, ok := set["wan.routes"].(wanroutes.Config)
	if !ok {
		t.Fatalf("wan.routes config missing or wrong type: %T", set["wan.routes"])
	}
	if wr.InternalNetV4 != "192.0.2.0/29" {
		t.Fatalf("wan.routes internal net = %q, want the network file's value", wr.InternalNetV4)
	}
	if wr.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf("wan.routes health state file = %q, want TOML's value", wr.HealthStateFile)
	}
	hc, ok := set["health"].(health.Config)
	if !ok {
		t.Fatalf("health config missing or wrong type: %T", set["health"])
	}
	if hc.PersistStateFile != "/var/lib/mwan/health-state" {
		t.Fatalf("health persist state file = %q, want TOML's value", hc.PersistStateFile)
	}
	if len(hc.WANs) != 1 || hc.WANs[0].CheckInterval != 10*time.Second {
		t.Fatalf("health WAN list did not resolve: %#v", hc.WANs)
	}
}
```

Add `npt "goodkind.io/mwan/internal/ifmgr/modules/npt"` to that file's import
block if it is not already there.

- [ ] **Step 2: Run the test to verify it passes**

Run:

```bash
docker run --rm --platform linux/amd64 \
  -v "$(git rev-parse --show-toplevel)":/src -w /src/mwan/go \
  -v mwan-wanconfig-gomod:/go/pkg/mod -e GOWORK=off \
  mwan-wanconfig-builder go test -count=1 ./cmd/mwan/ -run TestGatewayLoadWithoutNetworkTOML -v
```

Expected: PASS. This test is green from the start, because Task 3 already made
the load path two-file. It is the end-state guard that the template deletion
below cannot break the gateway.

- [ ] **Step 3: Delete the provider blocks and the health loop from the gateway template**

In `mwan/config/config-vm.toml.j2`, replace everything from
`{% if mwan_ifmgr_wan_enabled | bool %}` through the `{% endif %}` that closes it,
currently lines 129 to 182, holding the three `[ifmgr.wan.*]` tables, the
`[ifmgr.modules.wan.routes]` table, the `[ifmgr.modules.health]` table, and the
`mwan_health_checks` loop, with:

```jinja
{% if mwan_ifmgr_wan_enabled | bool %}
# The provider inventory, their routing slots, translation prefixes, source
# pins, and probe policies live in /etc/mwan/network.json, in the model's own
# encoding. What stays here is the two filesystem paths that are not network
# values: the health module writes its verdict to state_file and the routing
# module reads the same path as health_state_file.
[ifmgr.modules.wan.routes]
health_state_file = "{{ mwan_ifmgr_wan_health_state_file }}"

[ifmgr.modules.health]
state_file = "{{ mwan_ifmgr_wan_health_state_file }}"
persist_state_file = "/var/lib/mwan/health-state"

{% endif %}
```

- [ ] **Step 4: Delete the translation values from the common include**

In `mwan/config/_ifmgr_common.toml.j2`, delete these five lines:

```jinja
{% if mwan_ifmgr_wan_enabled | bool %}
internal_prefix = "{{ mwan_internal_prefix }}"
opnsense_edge_v6 = "{{ mwan_opnsense_edge_ipv6 }}"
mwanbr_edge_v6 = "{{ mwan_mwanbr_edge_ipv6 }}"
{% endif %}
```

so the `[ifmgr]` table ends at `json_log_file`, and the next line is the blank
line before `[ifmgr.iface.{{ mwan_ifmgr_iface_key }}]`.

- [ ] **Step 5: Run the gates**

Run: `cd "$(git rev-parse --show-toplevel)/mwan/go" && make check`
Expected: PASS, exit 0.

Run: `cd "$(git rev-parse --show-toplevel)/ansible" && rake syntax:all`
Expected: PASS, exit 0.

- [ ] **Step 6: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add mwan/config/config-vm.toml.j2 mwan/config/_ifmgr_common.toml.j2 mwan/go/cmd/mwan/ifmgr_module_configs_linux_test.go
git commit -S -m "Stop rendering the network tree into the gateway's config.toml" -m "Remove the three provider tables, the shared translation values, the routing module's internal link and network, and the per-provider probe loop from the gateway templates, keep the two health state-file paths the network file deliberately does not carry, and cover the post-deletion load path." -m "Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 6: Cutover runbook

No code. The controller session executes this task, not a subagent: it runs
deploys and production commands, and every production command needs the
operator's explicit approval immediately before it runs.

Read every command before running it. Resolve each ISP simulator's guest id from
`testbed_isp_lxcs` in the suburban group vars rather than typing one, and take
each environment's management address from `mwan_config_mgmt_addr` in that
environment's group vars.

Three shell values recur. `TAG` is the release tag the operator names. `MGMT` is
the gateway's management address for the environment being worked on. `SIM_ID` is
the resolved guest id of the simulator being observed.

**Files:** none.

**Interfaces:**
- Consumes: every artifact from Tasks 1 through 5.
- Produces: the recorded before-and-after evidence that pins the cutover, attached to MWAN-347, MWAN-348, and MWAN-349.

#### Testbed

- [ ] **Step 1: Install the model revision on the testbed gateway**

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-wanconfig-stack --release "$TAG" --limit mwan_suburban_servers
ssh mwan.suburban.goodkind.io 'sysrepoctl --list; ls /usr/local/share/wanconfig/yang'
```

Expected: the module listing shows `goodkind-mwan-steering | 2026-08-30`, and the
model directory holds the seven files including `iana-if-type@2014-05-08.yang`.
This must precede the loader slice, because the daemon validates against these
files.

- [ ] **Step 2: Capture the before snapshot on the testbed**

```bash
MGMT=3d06:bad:b01:210::213
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-interfaces:interfaces" > /tmp/before-tree.json
curl -sS "http://[$MGMT]:10080/restconf/data/ietf-nat:nat" > /tmp/before-nat.json
ssh mwan.suburban.goodkind.io 'nft list ruleset' > /tmp/before-nft.txt
ssh mwan.suburban.goodkind.io 'ip -6 rule show; ip rule show' > /tmp/before-rules.txt
ssh mwan.suburban.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; ip route show table $t; done' > /tmp/before-routes.txt
```

- [ ] **Step 3: Run the traffic matrix as the before run**

For each provider (att, webpass, monkeybrains) and each address family, from an
internal client behind the testbed router, force egress onto that provider with
the mechanism the firewall already provides, the DSCP mark the router stamps.
Observe at that provider's simulator ingress, because the simulators masquerade
outbound and anything seen past one proves nothing:

```bash
ssh suburban "pct exec $SIM_ID '--' timeout 30 tcpdump -ni any -c 20 'ip6 or ip'"
```

Record, per provider and family: the source address seen at the simulator, that
it is the provider's translated prefix or its pinned IPv4 source, and that the
reply returns on the same provider. Then record the spread of new connections
across the preferred tier's members, observed rather than assumed; a fallback
drill that forces the preferred tier unhealthy and shows traffic exiting the
fallback member translated in both families, then recovering; that a pinned
destination exits its pinned provider; and that the inbound translation paths
reach their internal targets.

- [ ] **Step 4: Cut the testbed over**

One release carries every slice, so this single deploy renders and validates
`network.json`, installs the binary that reads it, and stops rendering the
network tree into `config.toml`. Step 1 must already have installed the model
revision, or the daemon cannot validate the file it is handed.

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_suburban_servers
```

Expected: the play passes, including "Validate the rendered network
configuration against the schema".

- [ ] **Step 5: Read the rendered file back and validate it independently**

```bash
cd "$(git rev-parse --show-toplevel)"
ssh mwan.suburban.goodkind.io 'cat /etc/mwan/network.json' > /tmp/testbed-network.json
yanglint -t config \
  third_party/yang/standard/ietf/RFC/ietf-yang-types@2025-12-22.yang \
  third_party/yang/standard/ietf/RFC/ietf-inet-types@2025-12-22.yang \
  third_party/yang/standard/ietf/RFC/iana-if-type@2014-05-08.yang \
  third_party/yang/standard/ietf/RFC/ietf-interfaces@2018-02-20.yang \
  third_party/yang/standard/ietf/RFC/ietf-ip@2018-02-22.yang \
  third_party/yang/standard/ietf/RFC/ietf-routing@2018-03-13.yang \
  third_party/yang/standard/ietf/RFC/ietf-nat@2019-01-10.yang \
  mwan/yang/goodkind-mwan-steering@2026-08-30.yang \
  /tmp/testbed-network.json
jq '.["ietf-interfaces:interfaces"].interface[] | {name, wan: .["goodkind-mwan-steering:wan"].name}' /tmp/testbed-network.json
```

Expected: yanglint exits 0, and the four interfaces are the three providers plus
the internal link. Compare each provider's numbers against the values the
before snapshot recorded and against the inventory; they must match leaf for
leaf. This proves acceptance item one on a real gateway.

- [ ] **Step 6: Confirm the daemon accepted the file**

```bash
ssh mwan.suburban.goodkind.io 'journalctl -u mwan-ifmgr@wan -n 80 --no-pager'
```

Expected: no "network configuration unusable" line, and the wan modules
reconcile as they did before. The daemon reads `network.json` from this deploy
onward, so a load failure here is the one that stops the gateway steering.

- [ ] **Step 7: Run the deploy-validation breakage probe**

Run Task 4's step 4 now if it has not already been run, and record its output.

- [ ] **Step 8: Confirm the cutover took**

```bash
ssh mwan.suburban.goodkind.io 'systemctl status mwan-ifmgr@wan --no-pager; journalctl -u mwan-ifmgr@wan -n 50 --no-pager'
ssh mwan.suburban.goodkind.io 'systemctl status mwan-agent --no-pager'
ssh mwan.suburban.goodkind.io 'grep -c "ifmgr.wan" /etc/mwan/config.toml || true'
```

Expected: both units are active, the journal carries no "network configuration
unusable" line, and the grep count is 0. One release carries every slice, so the
deploy in step 4 already moved the daemon onto `network.json` and already
removed the network tree from `config.toml`; there is no separate loader or
deletion deploy.

- [ ] **Step 9: Capture the after snapshot and compare**

Repeat step 2 into `/tmp/after-loader-*`, then:

```bash
diff <(jq -S . /tmp/before-tree.json) <(jq -S . /tmp/after-loader-tree.json)
diff <(jq -S . /tmp/before-nat.json) <(jq -S . /tmp/after-loader-nat.json)
diff /tmp/before-nft.txt /tmp/after-loader-nft.txt
diff /tmp/before-rules.txt /tmp/after-loader-rules.txt
diff /tmp/before-routes.txt /tmp/after-loader-routes.txt
```

Expected: the served tree, the NAT instances, the ruleset, the policy rules, and
every provider routing table are identical. Live state that legitimately moves, a
health verdict's timestamp or a consecutive-failure counter, is the only
permitted difference; name each one in the ticket rather than waving at it.

- [ ] **Step 10: Run the traffic matrix as the after run**

Repeat step 3 in full. Every row must match the before run: same egress provider,
same translated source at the same simulator ingress, same reply path, same
fallback behavior, same pinned destinations, same inbound paths.

- [ ] **Step 11: Prove the agent's BGP table ownership survived**

The agent installs router-learned routes into the per-provider tables, and it
reads the provider set from the same network configuration. This step is what
would have caught the defect the whole-branch review found.

```bash
ssh mwan.suburban.goodkind.io 'journalctl -u mwan-agent -n 30 --no-pager'
ssh mwan.suburban.goodkind.io 'for t in 100 200 300; do echo "== $t"; ip -6 route show table $t; done'
```

Expected: every provider table carries the internal prefixes learned from the
router, not just its own default route, and the same prefixes the before
snapshot recorded in step 2. A table holding only a default route means the
agent lost its owned-table list.

#### Production

Each command below needs the operator's explicit approval immediately before it
runs. Do not batch them, and do not run a production command because a testbed
command like it succeeded.

- [ ] **Step 12: Install the model revision on the production gateway**

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-wanconfig-stack --release "$TAG" --limit mwan_servers
```

Expected: `sysrepoctl --list` on the gateway shows `goodkind-mwan-steering |
2026-08-30`.

- [ ] **Step 13: Capture the production before snapshot**

Repeat step 2 with `MGMT=3d06:bad:b01::113` and the production gateway
`mwan.home.goodkind.io`. Add a per-provider forced-egress probe in both families
and record the BGP session state, so the reduced live checks have a baseline.

- [ ] **Step 14: Cut production over**

Announce the reboot to the peer sessions first, and wait for their
acknowledgement.

```bash
cd "$(git rev-parse --show-toplevel)"
./configsctl deploy deploy-mwan --release "$TAG" --limit mwan_servers
```

Expected: the schema validation passes, the deploy gate's reboot and egress
verdicts pass, and the reboot window is reported. Record the exact window and
report it to the peer sessions afterwards.

- [ ] **Step 15: Compare production against its baseline**

Repeat step 13's captures, compare the served tree and kernel state against the
baseline, run the per-provider forced-egress probes in both families, and run
step 11's per-provider table check for the agent's learned routes.

Expected: identical tree and kernel state, every provider carries traffic in
both families, and every provider table still holds the router-learned
prefixes. No synthetic failover drill runs on production unless the operator
orders one.

- [ ] **Step 16: Confirm the TOML no longer carries the network tree**

```bash
ssh mwan.home.goodkind.io 'grep -c "ifmgr.wan" /etc/mwan/config.toml || true'
```

Expected: 0. The same deploy that moved the daemon onto `network.json` removed
those tables, so this confirms rather than performs the deletion.

- [ ] **Step 17: Record the outcome**

Append the before-and-after evidence, the reboot windows, and the breakage
probe's output to MWAN-347, MWAN-348, and MWAN-349, and append one entry to the
wanconfig ledger naming what shipped and what remains.

---

## Self-review

**Spec coverage.** Scope: Tasks 2, 3, and 5 together put exactly the network tree
in the file and leave everything else in TOML. The schema: Task 1. The renderer,
MWAN-347: Task 2. The loader,
MWAN-348: Task 3. Deploy-time validation, MWAN-349: Task 4. Cutover and proof:
Task 6, with one cutover deploy per environment, the traffic matrix run before
and after it, the breakage probe, the agent's per-provider table check, and the
production sequence under approval gates. Error handling: Task 4
covers the invalid render, Task 3's `loadNetworkConfig` and its five tests cover
the invalid or missing file at boot, and the single-schema constraint is
structural in Tasks 3 and 4. Testing: Task 3 carries the loader's red-green set,
Task 4 the breakage probe, Task 6 the behavioral matrix. The renderer's
CI-validated fixture is the one spec line this plan does not implement as
written, and Task 2 says why in full: no gate in this repository renders a Jinja
template, a committed render would have to carry live inventory addresses that
belong only in the inventory, and the render is instead validated on every deploy
by Task 4 and by hand in Task 6 step 5.

**Placeholders.** None. Every code step carries the code, every command step
carries the command and its expected result, and the two places where evidence
contradicts the source documents are stated with their file and line rather than
deferred: the release does not stage the model files, and the TOML template has
no provider loop to mirror.

**Two stale statements stay in the spec.** This plan edits no documentation, so
the spec keeps both. Its "What changes" section says the per-provider catalogue
lives in a shared inventory group keyed by environment; the settled design leaves
the provider values in the two gateway groups, which is what Task 2 renders from.
Its "Failure modes" section says the deploy validates with the schema staged from
the release; Task 4 explains where the model files actually come from. Neither
sentence changes what any task does, and correcting them is a documentation
change someone should make separately.

**Type and name consistency.** `networkjson.Config` fields are spelled the same
in Task 3's loader, Task 3's round-trip test, and Task 5's end-state test.
`config.IfMgrHealthSection.ProbeTimeoutMillis` and
`config.IfMgrHealthWANSection.CheckIntervalSeconds` are introduced in Task 3 step
6, consumed in Task 3 step 7, and read in Tasks 3 and 5's tests.
`yangpub.Schema`, `LoadSchema`, `ValidateConfigJSON`, and `Close` are defined in
Task 3 steps 3 and 4 and used in step 5 only. The inventory variable is
`mwan_health_probe_timeout_ms` in Task 2's group_vars and Task 2's template, and
nowhere else. The make target is `yang-validate-instances` in Task 1 steps 2, 3,
and 6, and in the `check` hook.
