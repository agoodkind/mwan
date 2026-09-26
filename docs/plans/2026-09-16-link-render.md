> Read the [repository context](../README.md) before using copied commands or historical plans.

# Link bring-up renders from the provider entry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on this page refer to the tree as it was.

> **Completed.** MWAN-492 and MWAN-491 run on both gateways in release `202609210222-10-0b079b8`. [The MWAN-506 completion plan](2026-09-21-mwan-506-completion.md) finishes the provider-local failure contract introduced during this work.

**Goal:** The gateway daemon writes every provider link's systemd-networkd unit files from the network configuration it already loads, so a provider is one inventory entry and a deploy with no file authored by hand.

**Architecture:** The provider entry gains link identity in two layers: typed leaves for what the published models do not name, and a free-form container that mirrors the unit-file format so any shape networkd reads is expressible. The daemon maps typed leaves to unit-file keys through one table, appends the free-form sections, and writes the files through a maintained serializer. Because udev applies a `.link` file when a device appears and the kernel refuses to rename a link that is already up, the daemon's unit moves ahead of udev's coldplug trigger and of systemd-networkd, writes the files, and then waits on netlink as it does today. The hand-authored per-provider files are deleted only after a continuous integration check proves the rendered output matches them byte for byte outside comment lines.

**Tech Stack:** Go 1.25 with coreos/go-systemd/v22/unit, YANG 1.1 with libyang (yanglint on the controller, the cgo binding in the daemon), Ansible with Jinja2 templates, systemd and systemd-networkd, udev.

**Spec:** [docs/superpowers/wanconfig/providers.md](../superpowers/wanconfig/providers.md), section "Link bring-up renders from the provider entry"

**Tickets:** MWAN-492 (the boot-order move and its proof) and MWAN-491 (the renderer), both under MWAN-324.

This plan supersedes one line of the provider-set plan, which scoped itself to
leaving the unit files hand-written. Every other constraint that plan carries
still holds.

## Global Constraints

- The daemon is the only writer of a provider link's unit files. Neither the deploy nor any verb of the binary run by hand writes one.
- systemd-networkd keeps matching, naming, addressing, and leasing every link. Nothing in this plan moves a lease or a netlink write off it.
- AT&T is exempt entirely. Its entry names its files hand-authored in one leaf and carries no link identity, so the daemon renders none of its four unit files and nothing compares them against the entry. A link that comes up through 802.1X authentication is described once, in the files beside the services that drive it.
- The boot-order change lands and is proven on its own, before any renderer code reaches a gateway, so a failure in either is attributable to one of them.
- The hand-authored per-provider unit files are deleted only in a change whose continuous integration check already proves the rendered output matches them outside comment lines.
- A key set by both the typed layer and the free-form layer is a load-time failure, never a silent override.
- A missing value is never defaulted. A missing group-wide value fails the load. After schema validation succeeds, a missing value inside one provider entry rejects that entry alone: the daemon reports the rejection and steers the remaining providers (MWAN-506).
- Deploy-time and load-time validation use the same schema files, never copies; the repository keeps exactly one revision file.
- The delegation client identity is a public identifier and lives in the configuration file. No secret ever enters the JSON.
- The daemon takes no ordering after `network-online.target`, which would close the cycle the firewall override documents.
- Behavior for the current provider set is unchanged, judged through the served tree, the policy rules, the routing tables, the firewall, and the traffic matrix.
- A deploy names no release on the command line. Each play installs the release its environment pins in group variables, verifying the checksum and the attestation on the controller. Moving a pin is its own reviewed commit carrying the new tag and both checksum lines.
- Testbed before production, always. Each production command is separately approved by the operator immediately before it runs, and every gateway reboot window is announced to the peer sessions before it opens and reported after it closes.
- Go style: comments explain non-obvious why, never what; full-word names; struct literals enumerate every field, because the `exhaustruct` gate requires it; every wrapped error is logged with slog by the function that wraps it; no lint suppressions.
- Tests exercise real behavior through public boundaries, with fakes at the kernel seam only. A rendering test compares real serializer output against a real checked-in file.
- Commits are signed (`git commit -S`), the subject is imperative with no trailing period, and the body ends with `Co-authored-by: Claude <noreply@anthropic.com>`.

### Running the gates

The Go gates live in `mwan/go`. On macOS the host toolchain compiles none of the
linux-tagged files and cannot build the cgo libyang binding, so the suite runs
inside the builder image; `make test` already routes itself that way on darwin.
`make check` runs `yang-validate` and `yang-validate-instances`, which validate
`mwan/yang/*.yang` and every instance under `mwan/yang/instances/` with the same
`yanglint` invocation the deploy and the daemon use.

### File structure

The model and the file it validates:

- `mwan/yang/goodkind-mwan-steering@<date>.yang`: one revision file, renamed on each revision. Gains the link container and the free-form networkd container.
- `mwan/yang/instances/`: instance documents the schema gate validates, gaining one that carries free-form sections.
- `mwan/config/network.json.j2`: emits link identity per provider from the same loop it already runs.
- `ansible/inventory/group_vars/mwan_servers.yml`, `mwan_suburban_servers.yml`: each provider entry gains a `link` block; the per-provider hardware variables collapse into it.

The daemon loads it, renders from it, and writes the files:

- `mwan/go/internal/networkjson`: decodes the link identity and the free-form sections, and fails the load when both layers set one key.
- `mwan/go/internal/networkd`: new package. Maps typed leaves to unit-file sections and keys through one table, appends the free-form sections, serializes, and writes only on change.
- `mwan/go/cmd/mwan/ifmgr_linux.go`: renders before the daemon starts, and again on configuration reload.
- `mwan/go/cmd/mwan/mwan-ifmgr@.service`: ordered ahead of udev's coldplug trigger and of systemd-networkd, with a write path for the unit directory.

What stops carrying the files:

- `mwan/networkd/`: every per-provider template goes, except `20-att.link` and `20-att.network`.
- `ansible/playbooks/deploy-mwan.yml` and both gateway groups' `mwan_networkd_files`: the per-provider loop keeps only the management link, the internal bridge, and the AT&T physical pair.

### What the source says that the tasks follow

Each item was read out of the tree at the commit this plan was written against.

**The loader is the only reader of the file.** `networkjson.Load(path, schemaDir)`
reads `/etc/mwan/network.json`, validates it against the schema directory with
libyang, unmarshals into an unexported document, and builds a `*networkjson.Config`.
`ApplyDefault` calls it and copies the result onto the daemon's configuration
structure. Every failure is fatal; no value is defaulted. The daemon calls this
from `loadNetworkConfig` in `mwan/go/cmd/mwan/ifmgr_linux.go`, which first asks
`roleSteersProviders(role)` whether the role runs `wan.routes` and returns
untouched when it does not.

**The built configuration drops what the daemon does not use.** `Config.WAN` is a
map of `config.IfMgrWANEntry`, which carries the routing numbers, the translation
prefix, the IPv4 source pin, the forced marking, the tier, the weight, and the
static mappings. It carries no link identity, because nothing needed it. The link
identity therefore stays on the loaded document rather than passing through that
structure, and the renderer reads it there.

**The schema is one file with a revision stack.** `mwan/yang/` holds exactly one
module file, named for its newest revision, with every earlier revision statement
below it. Each revision's description states whether it is additive. The gate
globs `mwan/yang/*.yang`, so a second file would be loaded as a second module: a
revision renames the file rather than adding one.

**The template already loops the provider list.** `mwan/config/network.json.j2`
iterates `mwan_providers` once and emits the steering and WAN containers per
provider. Link identity joins that loop; no second loop and no second list.

**The deploy wipes the unit directory on every run.** It deletes
`/etc/systemd/network`, recreates it, templates the management pair, then
templates each name in `mwan_networkd_files` from `mwan/networkd/` and notifies a
networkd reload. Because the directory is emptied first, a file the daemon wrote
on a previous boot does not survive a deploy: the daemon writes its files again at
the restart the deploy triggers, and the ordering that matters is the reboot,
where the daemon runs before udev.

**The daemon constructs modules, then starts the kernel monitor, then initializes
them.** `NewDaemon` resolves the role to a module list and constructs each one.
`Run` starts the netlink monitor before any module initializes, so a module's own
kernel change is observed, then initializes modules in role order, then reconciles
once and enters the event loop. The render belongs before all of it, beside the
configuration load, because the files must exist before udev names a device and
the daemon must not wait on a link it has not yet caused.

**The AT&T chain is driven by files this plan does not touch.** The supplicant unit
waits for the physical interface, authenticates, and writes a file a path unit
watches; the path unit starts a service that triggers DHCP on the VLAN. The
physical link's two unit files stay hand-authored because that chain names the
interface they create.

### Open items for the operator

None. The two rulings this plan implements, that the daemon renders and that the
daemon's own unit moves early rather than a separate early verb, are recorded in
the spec.

---

### Task 1: the daemon's unit starts before udev and networkd

The unit file is the whole change. No Go code moves, no renderer exists yet, and
the hand-authored unit files still supply every link, so this task is observable
on its own: the daemon starts earlier and nothing else differs.

**Files:**
- Modify: `mwan/go/cmd/mwan/mwan-ifmgr@.service`

**Interfaces:**
- Consumes: nothing from an earlier task.
- Produces: a daemon process running before `systemd-udev-trigger.service` and `systemd-networkd.service`, which every later task's render depends on.

- [ ] **Step 1: replace the unit's ordering block**

Replace the `[Unit]` section:

```ini
[Unit]
Description=MWAN ifmgr (interface manager) daemon for role %i
Documentation=https://github.com/agoodkind/configs
# The daemon writes each provider link's systemd-networkd unit files from
# /etc/mwan/network.json. udev applies a .link file when the device appears and
# the kernel refuses to rename a link that is already up, so a file written
# after udev's coldplug trigger has no effect until the next boot. The daemon
# therefore runs ahead of that trigger and of networkd, writes the files, and
# then waits on netlink for the links as it always has.
DefaultDependencies=no
After=local-fs.target systemd-journald.socket
Before=systemd-udev-trigger.service systemd-networkd.service shutdown.target
# DefaultDependencies=no drops the implicit shutdown ordering along with the
# rest, so the unit states it. Without this the daemon is killed at shutdown
# rather than stopped.
Conflicts=shutdown.target
```

`After=local-fs.target` is what makes the binary, `/etc/mwan/network.json`, and
the schema directory readable. `After=systemd-journald.socket` keeps the render's
own log lines in the journal, which is what the next task reads to prove the
ordering. The replaced `After=networking.service` referred to ifupdown, which
this gateway does not run.

- [ ] **Step 2: give the sandbox a write path for the unit directory**

`ProtectSystem=strict` makes `/etc` read-only. Extend the existing
`ReadWritePaths` line so the daemon can write what it renders:

```ini
ReadWritePaths=/var/log /var/run /run /var/lib/mwan /etc/systemd/network -/etc/sysrepo
```

The path is added here rather than with the renderer, so the unit file changes
once and the renderer task changes no unit.

- [ ] **Step 3: build and run the gates**

Run from `mwan/go`:

```bash
make check
make build
```

Expected: both pass. Neither reads the unit file; this step proves the task
broke nothing else.

- [ ] **Step 4: commit**

```bash
git add mwan/go/cmd/mwan/mwan-ifmgr@.service
git commit -S -m "Order the mwan ifmgr daemon before udev and systemd-networkd"
```

---

### Task 2: the testbed proves the boot order and survives the failure cases

Task 1 is only real once a gateway has booted with it. This task runs the
cutover and the exercise the ticket requires, and records each result with the
command, the host, and the observed output.

**Files:**
- Create: `docs/superpowers/runbooks/2026-09-16-daemon-boot-order.md`

**Interfaces:**
- Consumes: Task 1's unit file, merged and released.
- Produces: the recorded proof that gates the production run in Task 3.

Each of the two deploys below installs the release its environment pins, so
each is preceded by its own reviewed pin commit naming the build that carries
this task's unit file.

- [ ] **Step 1: capture the testbed before the cutover**

```bash
ssh mwan-testbed 'ip rule show; ip -6 rule show' > before-rules.txt
ssh mwan-testbed 'nft list ruleset' > before-nft.txt
ssh mwan-testbed 'ip -br link; ip -br addr' > before-links.txt
ssh mwan-testbed 'ls -l /etc/systemd/network' > before-units.txt
ssh mwan-testbed 'systemd-analyze critical-chain mwan-ifmgr@wan.service' > before-chain.txt
```

Take the served tree as well, from the testbed's management address.

- [ ] **Step 2: deploy, verify the unit, reboot**

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
ssh mwan-testbed 'systemd-analyze verify /etc/systemd/system/mwan-ifmgr@.service'
ssh mwan-testbed 'systemctl reboot'
```

The play installs the release the testbed group pins, so moving to the build
that carries this task's unit file is a pin commit of its own, merged before
this deploy runs.

Expected: `systemd-analyze verify` prints nothing, which is how it reports a unit
with no ordering cycle and no unknown directive. A cycle prints the loop it broke
and which unit it dropped to break it. That output fails this task; it is not a
warning.

- [ ] **Step 3: prove the order from the journal**

```bash
ssh mwan-testbed 'journalctl -b -o short-monotonic -u mwan-ifmgr@wan -u systemd-udev-trigger -u systemd-networkd | head -40'
ssh mwan-testbed 'systemd-analyze critical-chain mwan-ifmgr@wan.service'
```

Expected: the daemon's first line carries a smaller monotonic timestamp than
`systemd-udev-trigger.service` starting and than `systemd-networkd.service`
starting. Record all three.

- [ ] **Step 4: prove the links and the state are unchanged**

Repeat every capture from Step 1 and compare. Expected: policy rules, links,
addresses, unit directory listing, firewall, and served tree identical to the
before capture. The critical chain differs, which is the point.

- [ ] **Step 5: run the failure cases**

Each is a separate recorded result. Run them in this order, because the last two
leave the gateway in a state the earlier ones do not.

1. **Fallback in both families.** Fail the top tier at the simulators, watch the lower tier take over in the served tree and the policy rules, restore, watch it return. Record both directions, IPv4 and IPv6.
2. **A link absent at boot.** Detach one provider's interface at the hypervisor, reboot, and confirm the daemon starts, logs the missing link, and steers over the rest. Reattach and confirm it joins with no reboot.
3. **The 802.1X path.** Reboot and confirm the supplicant authenticates, the path unit fires, and the VLAN takes its lease only after `AUTHENTICATED` appears. The daemon starting earlier must not change that sequence.
4. **A daemon restart with links up.** Restart the daemon while every link is up. Expected: no rename attempted, no link flap.

- [ ] **Step 6: write the results down and commit**

Record each step's command, host, and output. A step with no recorded output did
not happen.

```bash
git add docs/superpowers/runbooks/2026-09-16-daemon-boot-order.md
git commit -S -m "Record the testbed boot-order cutover and failure cases"
```

---

### Task 3: production runs in check mode, then cuts over

**Files:**
- Modify: `docs/superpowers/runbooks/2026-09-16-daemon-boot-order.md`

**Interfaces:**
- Consumes: Task 2's recorded results, all passing.
- Produces: the production gateway running the new ordering.

- [ ] **Step 1: ask the operator for the check-mode run**

A check-mode run against production is a production command. Ask, wait, and run
only what was approved.

- [ ] **Step 2: run the deploy in check mode**

```bash
./configsctl deploy deploy-mwan --limit mwan_servers --check --diff
```

Expected: the only reported change is the ifmgr unit file, whose diff shows the
new ordering block and the extended write path. Any other changed file is a
finding. Stop and report it rather than proceeding.

The production pin moves in its own reviewed commit before this runs, and the
check-mode run is what shows that moving it changes the unit file and nothing
else.

- [ ] **Step 3: capture production, announce, ask, then deploy**

Take the Step 1 captures against production. Announce the window to the peer
sessions. Ask the operator for the deploy. The deploy reboots the gateway.

- [ ] **Step 4: prove and report**

Repeat Task 2 Steps 3 and 4 against production, report the window closed, and add
the results to the runbook.

---

### Task 4: the model carries link identity and free-form sections

One revision, additive. No node an earlier revision defined changes name, type,
or shape.

**Files:**
- Rename: `mwan/yang/goodkind-mwan-steering@2026-09-14.yang` to `mwan/yang/goodkind-mwan-steering@2026-09-17.yang`, or to the day the revision lands
- Modify: the renamed file
- Modify: `mwan/yang/instances/network-min.json`
- Create: `mwan/yang/instances/network-freeform.json`

**Interfaces:**
- Consumes: nothing from an earlier task.
- Produces: the node names every later task uses. The loader decodes exactly these paths; the template emits exactly these paths.

- [ ] **Step 1: rename the file and prepend the revision**

The gate globs `mwan/yang/*.yang`, so a second file loads as a second module. The
revision statement goes above the existing newest one:

```yang
  revision 2026-09-17 {
    description
      "Carry each interface's link identity and any unit-file content the
       typed leaves do not name, so the daemon renders the network
       manager's unit files from this model rather than from a per-provider
       template. Every addition is a new node: no node defined by an
       earlier revision changes name, type, or shape.";
  }
```

- [ ] **Step 2: add the link container**

A new augment beside the existing interface augments:

```yang
  augment "/if:interfaces/if:interface" {
    description
      "How the network manager brings this interface up. The published
       models name an interface and its addresses. They do not name the
       device the interface is bound to, the tag a VLAN carries, or the
       identity a delegation is requested with.";

    leaf link-files {
      type enumeration {
        enum rendered;
        enum hand-authored;
      }
      description
        "Who writes this interface's unit files. An interface carrying a
         wan container states one or the other: rendered means it carries a
         link container the daemon writes files from, and hand-authored
         means its files are in the repository and this model says nothing
         about how the link comes up. An interface that states neither
         fails the load, so an unwritten link container is caught rather
         than read as an exemption.";
    }

    container link {
      description
        "What the device is, what it is named, and how it is created.";

      container match {
        description
          "How the network manager recognizes the device. An interface
           that carries a wan container sets exactly one of these leaves
           or a vlan container; the daemon fails the load otherwise.";
        leaf driver {
          type string;
          description
            "The kernel driver the device presents, for a device whose
             address is not stable across a rebuild.";
        }
        leaf hardware-address {
          type yang:mac-address;
          description
            "The device's address, for a device whose driver is shared
             with another interface.";
        }
      }

      leaf hardware-address {
        type yang:mac-address;
        description
          "The address to set on the device, which is not always the
           address it is matched by.";
      }

      container vlan {
        presence "The interface is a VLAN on another interface.";
        description
          "The interface is created as a VLAN rather than bound to a
           device. The parent interface carries its own unit files.";
        leaf parent {
          mandatory true;
          type if:interface-ref;
          description "The interface the VLAN is created on.";
        }
        leaf id {
          mandatory true;
          type uint16 {
            range "1..4094";
          }
          description "The VLAN tag.";
        }
      }
    }

    container networkd {
      description
        "Unit-file content the typed leaves do not name, written as the
         network manager reads it. The schema validates the structure and
         the network manager validates the keys, because it is the thing
         that knows them. A key named here and by a typed leaf is a
         load-time failure.";
      list file {
        key "kind";
        description "One entry per unit file kind.";
        leaf kind {
          type enumeration {
            enum link;
            enum network;
            enum netdev;
          }
          description "Which unit file these sections are written to.";
        }
        list section {
          key "index";
          ordered-by user;
          description
            "A heading and its lines, in file order. The key is positional
             because the network manager reads a repeated heading as one
             section and a repeated key within a section as a list, so
             neither the heading nor the key is unique.";
          leaf index {
            type uint16;
            description "Position in the file.";
          }
          leaf name {
            mandatory true;
            type string;
            description "The heading, without its brackets.";
          }
          list entry {
            key "index";
            ordered-by user;
            description "One line, in section order.";
            leaf index {
              type uint16;
              description "Position in the section.";
            }
            leaf key {
              mandatory true;
              type string;
              description "The name left of the equals sign.";
            }
            leaf value {
              mandatory true;
              type string;
              description "The text right of the equals sign.";
            }
          }
        }
      }
    }
  }
```

- [ ] **Step 3: add the per-family leaves**

IPv4 gains a new augment. IPv6 gains leaves inside the augment that already
carries `delegated-prefix`, rather than a second augment on the same target.

```yang
  augment "/if:interfaces/if:interface/ip:ipv4" {
    description "How this family is addressed on this interface.";
    leaf dhcp {
      type boolean;
      description
        "Whether the network manager runs a DHCPv4 client here. An
         interface carrying a wan container states it either way; the
         daemon fails the load when it is absent.";
    }
    leaf gateway {
      type inet:ipv4-address;
      description
        "The next hop for a statically addressed interface. Absent when
         the address is leased, because the lease carries it.";
    }
    leaf route-metric {
      type uint32;
      description
        "The metric on the default route this interface contributes to
         the main table. Lower is preferred.";
    }
  }
```

The same three leaves are added to the IPv6 augment, plus:

```yang
    leaf accept-ra {
      type boolean;
      description
        "Whether the network manager accepts router advertisements here.";
    }
    container delegation {
      presence "The interface requests a delegated prefix.";
      description
        "The delegation the interface asks for, and the identity it asks
         with. The identity is a public identifier, not a secret.";
      leaf hint {
        type inet:ipv6-prefix;
        description "The prefix length the client asks for.";
      }
      leaf duid-type {
        type enumeration {
          enum link-layer-time;
          enum link-layer;
          enum vendor;
          enum uuid;
        }
        description "How the client identity is formed.";
      }
      leaf duid {
        type string {
          pattern '([0-9a-fA-F]{2}:)*[0-9a-fA-F]{2}';
        }
        description "The client identity, as colon-separated octets.";
      }
      leaf without-ra {
        type enumeration {
          enum no;
          enum solicit;
          enum information-request;
        }
        description
          "Whether the client starts without waiting for a router
           advertisement, and which exchange it starts with.";
      }
      leaf use-delegated-prefix {
        type boolean;
        description
          "Whether the network manager applies the delegated prefix to
           this link.";
      }
      leaf router-lifetime-seconds {
        type uint32;
        description
          "The lifetime the link advertises to downstream routers, in
           seconds.";
      }
    }
```

The IPv4 augment also gains the source pin's extension:

```yang
    leaf-list source-addresses {
      type inet:ipv4-address;
      description
        "Addresses beyond the link's own that traffic sourced from this
         interface's provider may carry. The routing module pins the
         link's static address on its own; this list adds to it and
         never replaces it, because a source the provider does not route
         back is dropped at its edge.";
    }
```

Every line the current providers' unit files carry now has a typed leaf. The
free-form layer starts empty for every current provider and is exercised by
the instance document in Step 4 and the tests in Task 7.

The `v4-source` leaf the earlier revision defined keeps its name and type,
because no node an earlier revision defined changes shape. Its description
changes to say the daemon fills it from the static address, so the served
tree reports the same value it always has while inventory stops typing it.

- [ ] **Step 4: add an instance that exercises the free-form path**

`mwan/yang/instances/network-freeform.json` carries one interface with a link
container, a static IPv4 address, a delegation, and a free-form `[DHCPv6]`
section carrying `UseDNS=no`, a key no typed leaf names. `network-min.json`
gains a link container so the minimum document still validates against the
new nodes.

- [ ] **Step 5: run the schema gates**

```bash
make yang-validate
make yang-validate-instances
```

Expected: both pass, and the instance run prints the new instance file's name.

- [ ] **Step 6: update the spec's illustrative examples**

The spec's examples name leaves illustratively and say the model revision fixes
them. Bring the example JSON in `docs/superpowers/wanconfig/providers.md` to the
node names this task defines, including the positional index on sections and
entries. A reader must be able to copy the example into a document that
validates.

- [ ] **Step 7: commit**

```bash
git add mwan/yang docs/superpowers/wanconfig/providers.md
git commit -S -m "Add link identity and free-form unit sections to the steering model"
```

---

### Task 5: the loader decodes link identity and rejects a doubled key

The link specification and the leaf-to-key table land here rather than with the
renderer, because the doubled-key check needs the table and the loader is what
must fail. Task 7 adds serialization to the same package.

**Files:**
- Create: `mwan/go/internal/networkd/spec.go`
- Create: `mwan/go/internal/networkd/spec_test.go`
- Modify: `mwan/go/internal/networkjson/networkjson.go`
- Modify: `mwan/go/internal/networkjson/networkjson_test.go`

**Interfaces:**
- Consumes: Task 4's node names.
- Produces: `networkd.Spec`, the leaf-to-key table, `networkd.Validate`, and `Config.Links []networkd.Spec`. Task 7 renders from the first, Task 8 writes what it renders.

- [ ] **Step 1: write the failing test**

```go
func TestLoadRejectsAKeySetByBothLayers(t *testing.T) {
	doc := strings.Replace(validDocument,
		`"goodkind-mwan-steering:wan": {`,
		`"goodkind-mwan-steering:networkd": {"file": [{"kind": "network", "section": [`+
			`{"index": 0, "name": "DHCPv6", "entry": [`+
			`{"index": 0, "key": "PrefixDelegationHint", "value": "::/60"}]}]}]},`+"\n"+
			`      "goodkind-mwan-steering:wan": {`, 1)

	path := writeTemp(t, doc)
	_, err := networkjson.Load(path, schemaDir(t))
	if err == nil {
		t.Fatal("Load accepted a key set by both layers")
	}
	const want = "is set by the delegation hint leaf"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Load error = %q, want it to contain %q", err, want)
	}
}
```

A second test loads the same document with the free-form key changed to
`UseDNS` and asserts the load succeeds and the section survives into
`Config.Links`. A third loads a document whose fixed-address provider carries
no `v4-source` and asserts the built WAN entry's `V4Source` equals the
address leaf, and that a leased-link provider's stays empty.

- [ ] **Step 2: run it and watch it fail**

```bash
make test ARGS='-run TestLoadRejectsAKeySetByBothLayers ./internal/networkjson/...'
```

Expected: FAIL, because `Load` accepts the document.

- [ ] **Step 3: add the wire types**

```go
type linkMatch struct {
	Driver          string `json:"driver"`
	HardwareAddress string `json:"hardware-address"`
}

type linkVLAN struct {
	Parent string `json:"parent"`
	ID     *int   `json:"id"`
}

type linkIdentity struct {
	Match           *linkMatch `json:"match"`
	HardwareAddress string     `json:"hardware-address"`
	VLAN           *linkVLAN   `json:"vlan"`
}

type networkdEntry struct {
	Index int    `json:"index"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type networkdSection struct {
	Index   int             `json:"index"`
	Name    string          `json:"name"`
	Entries []networkdEntry `json:"entry"`
}

type networkdFile struct {
	Kind     string            `json:"kind"`
	Sections []networkdSection `json:"section"`
}

type networkdContainer struct {
	Files []networkdFile `json:"file"`
}

type familyV4 struct {
	Forwarding  *bool      `json:"forwarding"`
	Address     []ipAddress `json:"address"`
	DHCP        *bool      `json:"goodkind-mwan-steering:dhcp"`
	Gateway     string     `json:"goodkind-mwan-steering:gateway"`
	RouteMetric *int       `json:"goodkind-mwan-steering:route-metric"`
}
```

`familyV6` adds `AcceptRA *bool` and `Delegation *delegation`. `ipAddress` is
`{IP string; PrefixLength *int}`. `ifaceEntry` gains `Link`, `Networkd`, `IPv4`,
and `IPv6`, each with the namespaced JSON name Task 4 defined.

- [ ] **Step 4: write the specification and the leaf-to-key table**

`mwan/go/internal/networkd/spec.go` holds what a link is and where each typed
leaf lands in a unit file:

```go
// FileKind names one of the three unit files an interface can produce.
type FileKind string

const (
	FileLink    FileKind = "link"
	FileNetwork FileKind = "network"
	FileNetdev  FileKind = "netdev"
)

// placement names the file, section, and key one typed leaf renders to.
// This table is the only place in the daemon where a networkd key name
// appears, so a new option is a row here and a leaf in the model, never a
// change to the renderer.
type placement struct {
	File    FileKind
	Section string
	Key     string
}

var placements = map[string]placement{
	"match.driver":           {FileLink, "Match", "Driver"},
	"match.hardware-address": {FileLink, "Match", "MACAddress"},
	"name":                   {FileLink, "Link", "Name"},
	"hardware-address":       {FileLink, "Link", "MACAddress"},
	"vlan.id":                {FileNetdev, "VLAN", "Id"},
	"ipv4.address":           {FileNetwork, "Network", "Address"},
	"ipv4.dhcp":              {FileNetwork, "Network", "DHCP"},
	"ipv6.dhcp":              {FileNetwork, "Network", "DHCP"},
	"ipv6.accept-ra":         {FileNetwork, "Network", "IPv6AcceptRA"},
	"ipv4.forwarding":        {FileNetwork, "Network", "IPv4Forwarding"},
	"ipv6.forwarding":        {FileNetwork, "Network", "IPv6Forwarding"},
	"delegation.duid-type":   {FileNetwork, "DHCPv6", "DUIDType"},
	"delegation.duid":        {FileNetwork, "DHCPv6", "DUIDRawData"},
	"delegation.hint":        {FileNetwork, "DHCPv6", "PrefixDelegationHint"},
	"delegation.without-ra":  {FileNetwork, "DHCPv6", "WithoutRA"},
	"delegation.use-delegated-prefix": {FileNetwork, "DHCPv6", "UseDelegatedPrefix"},
	"delegation.router-lifetime-seconds": {FileNetwork, "IPv6PrefixDelegation", "RouterLifetimeSec"},
}
```

`source-addresses` has no row. It never reaches a unit file; the routing
module reads it and adds one source rule per address at the provider's source
priority, beside the rule the link address already gets.

The two `DHCP` rows fold into one emitted key, because the network manager takes
one value naming the families: `yes`, `ipv4`, `ipv6`, or `no`. The routes a
static address contributes are their own repeated `[Route]` sections and are
built rather than looked up, because a route carries three keys that travel
together.

One line has no row, because it lands on another interface's file. A VLAN
exists only when its parent's `.network` carries `VLAN=<child>`, so that line
belongs to the parent and is built from the child's `vlan.parent` leaf rather
than looked up from the child's own placements. `Render` emits it into the
parent's file, and the loader has already failed any entry whose parent no
interface in the document describes.

`Validate(spec Spec) error` walks the free-form sections, resolves each one
against the placements the spec's own typed leaves occupy, and returns an error
naming the section, the key, and the leaf that already sets it. Its own test
covers both directions with no file writing involved.

- [ ] **Step 5: build the renderer input and validate it**

`build` gains a pass that turns each interface carrying a `wan` container into a
`networkd.Spec` and calls `networkd.Validate`. An interface with a `wan`
container and neither a device match nor a VLAN fails the load, as does one whose
family container omits `dhcp`. A fixed-address provider's `V4Source` is set
from its address leaf, never read from the document; a leased-link provider's
stays empty, which is the routing module's existing contract. A document that
still carries `v4-source` fails the load naming the leaf, so inventory cannot
keep typing a value the daemon now derives. A provider naming a VLAN parent that no interface
in the same document describes also fails the load, because a VLAN whose parent
is absent is created by nothing and the provider would be silently missing with
no error from any layer.

An entry carrying neither link identity nor `link_files: hand-authored` fails
the load too. The exemption is a stated leaf, never an inferred absence, so a
provider whose link block someone forgot to write is caught rather than
quietly left unrendered.

```go
if err := networkd.Validate(spec); err != nil {
    return nil, fmt.Errorf("network.json: %s: %w", entry.Name, err)
}
```

- [ ] **Step 6: run the tests**

```bash
make test ARGS='./internal/networkjson/... ./internal/networkd/...'
```

Expected: PASS, including the existing tests, which prove the added fields
changed no decoded value.

- [ ] **Step 7: commit**

```bash
git add mwan/go/internal/networkjson mwan/go/internal/networkd
git commit -S -m "Decode link identity and free-form unit sections in the network configuration loader"
```

---

### Task 6: inventory and the template carry link identity

**Files:**
- Modify: `ansible/inventory/group_vars/mwan_servers.yml`
- Modify: `ansible/inventory/group_vars/mwan_suburban_servers.yml`
- Modify: `mwan/config/network.json.j2`

**Interfaces:**
- Consumes: Task 4's node names.
- Produces: a rendered `network.json` that carries every value the current unit files carry, which Task 9's gate compares against those files.

- [ ] **Step 1: add a link block to each provider entry**

Each rendered entry in `mwan_providers` gains a `link` mapping, a per-family
`dhcp` and `forwarding`, and the delegation its current unit file carries.
Where a hardware value is also read by the environment file, the entry
references that variable rather than repeating the value. Monkeybrains, whose
file carries every delegation key the model names:

```yaml
  - name: monkeybrains
    link_files: rendered
    link:
      iface: "{{ mwan_monkeybrains_iface }}"
      match: { mac: "{{ mwan_mbrains_mac | lower }}" }
    ipv4:
      forwarding: true
      dhcp: true
      route_metric: 5000
    ipv6:
      forwarding: true
      dhcp: true
      accept_ra: true
      route_metric: 5000
      delegation:
        hint: "::/56"
        duid_type: link-layer-time
        duid: "{{ mwan_monkeybrains_duid_raw_data }}"
        use_delegated_prefix: true
        without_ra: solicit
        router_lifetime_seconds: 1800
```

Webpass drops its `v4_source` line: the daemon now takes the pin from the
address leaf, and a document that still carries the value fails the load.

Read each current template under `mwan/networkd/` and carry every key it emits
into a typed leaf. Every key the current providers carry has one, so no entry
carries a free-form block, and a key left behind is a key the gate in Task 9
will report.

AT&T is the exception and gets the opposite treatment. Its entry gains
`link_files: hand-authored`, no link block, and no per-family addressing, and
its four templates stay exactly as they are. Nothing about that link is
written into inventory twice.

- [ ] **Step 2: emit it from the existing loop**

`mwan/config/network.json.j2` keeps its single `{% for provider in mwan_providers %}`
loop and adds the link container, the two family containers, and the free-form
container, each conditional on the entry carrying it. The positional index on
sections and entries comes from `loop.index0`.

- [ ] **Step 3: render and validate both groups**

Render `network.json` for each gateway group and validate it with the same
`yanglint` invocation the daemon uses.

Expected: both documents validate, and a value-by-value read shows every key the
current unit files carry is present.

- [ ] **Step 4: commit**

```bash
git add ansible/inventory/group_vars mwan/config/network.json.j2
git commit -S -m "Carry link identity in the provider entries and the network configuration template"
```

---

### Task 7: the renderer turns a spec into unit files

**Files:**
- Create: `mwan/go/internal/networkd/networkd.go`
- Create: `mwan/go/internal/networkd/networkd_test.go`
- Create: `mwan/go/internal/networkd/testdata/`
- Modify: `mwan/go/go.mod`, `mwan/go/go.sum`

**Interfaces:**
- Consumes: Task 5's `networkd.Spec` and its leaf-to-key table.
- Produces: `Render`, which Task 8 calls and Task 9's gate runs.

- [ ] **Step 1: add the serializer dependency**

```bash
go get github.com/coreos/go-systemd/v22/unit
```

The package writes a section, a name, and a value; it knows nothing about
networking, so nothing about the unit-file syntax is ours.

- [ ] **Step 2: write the failing test**

The test renders the webpass spec and compares against a checked-in file:

```go
func TestRenderWritesAStaticLinkWithADelegation(t *testing.T) {
	files, err := networkd.Render(webpassSpec())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want, err := os.ReadFile("testdata/20-webpass.network")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := files["20-webpass.network"]; got != string(want) {
		t.Fatalf("20-webpass.network mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
```

A second test renders a spec whose free-form sections name a heading the typed
layer also emits and asserts the two merge under one heading, with the typed
lines first. A third renders a VLAN spec and asserts both the `.netdev` and the
`.network` appear with the parent's own files absent.

- [ ] **Step 3: run them and watch them fail**

```bash
make test ARGS='./internal/networkd/...'
```

Expected: FAIL to compile, because `Render` does not exist. Task 5 created the
package; this task gives it serialization.

- [ ] **Step 4: write Render and the file names**

`Render(spec Spec) (map[string]string, error)` walks the leaf-to-key table Task 5
wrote, appends the free-form sections, serializes each file with the systemd unit
package, and returns the file name mapped to its content. Names follow what the
hand-authored files use, so the gate in Task 9 compares like with like: the
numeric prefix comes from the spec, which carries it from inventory. A heading
named by both layers is emitted once, with the typed lines first, because the
network manager reads a repeated heading as one section. Every file opens with
one marker line, which is what makes pruning in Task 8 safe. It is exported
because the pruning test asserts against it:

```go
// Marker opens every file this package writes. Pruning removes a file only
// when it carries this line, so a file the deploy owns is never touched.
const Marker = "# Generated by mwan from /etc/mwan/network.json. Do not edit."
```

- [ ] **Step 5: run the tests and the gates**

```bash
make test ARGS='./internal/networkd/...'
make check
```

Expected: PASS.

- [ ] **Step 6: commit**

```bash
git add mwan/go/internal/networkd mwan/go/go.mod mwan/go/go.sum
git commit -S -m "Render systemd-networkd unit files from a link specification"
```

---

### Task 8: the daemon writes the files before it starts

**Files:**
- Modify: `mwan/go/internal/networkd/networkd.go`
- Modify: `mwan/go/internal/networkd/networkd_test.go`
- Modify: `mwan/go/cmd/mwan/ifmgr_linux.go`

**Interfaces:**
- Consumes: Task 5's `Config.Links` and Task 7's `Render`.
- Produces: unit files on disk before udev runs, which Task 11 proves on a gateway.

- [ ] **Step 1: write the failing test for writing and pruning**

```go
func TestWriteDirPrunesOnlyItsOwnStaleFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "20-webpass.network", networkd.Marker+"\n[Match]\nName=old\n")
	mustWrite(t, dir, "10-mgmt.network", "[Match]\nName=enmgmt0\n")

	changed, err := networkd.WriteDir(dir, []networkd.Spec{monkeybrainsSpec()})
	if err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20-webpass.network")); !os.IsNotExist(err) {
		t.Fatal("a stale file this package wrote was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "10-mgmt.network")); err != nil {
		t.Fatalf("a file this package did not write was removed: %v", err)
	}
	if len(changed) == 0 {
		t.Fatal("WriteDir reported no change after writing a new file")
	}

	again, err := networkd.WriteDir(dir, []networkd.Spec{monkeybrainsSpec()})
	if err != nil {
		t.Fatalf("WriteDir second pass: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("WriteDir rewrote unchanged files: %v", again)
	}
}
```

The second pass is the whole point of the test: a rewrite with identical content
must not touch the file, because a changed modification time on a `.link` file
is a reason for udev to act.

- [ ] **Step 2: run it and watch it fail**

```bash
make test ARGS='-run TestWriteDirPrunes ./internal/networkd/...'
```

Expected: FAIL to compile, because `WriteDir` does not exist.

- [ ] **Step 3: write WriteDir**

It renders every spec, compares each file against what is on disk, writes only
the ones that differ, and removes any file in the directory whose first line is
the marker and whose name no rendered spec produced. It never touches a file
without the marker, which is what keeps the management link, the internal
bridge, and the AT&T physical pair safe.

- [ ] **Step 4: call it from the startup path**

`loadNetworkConfig` keeps the load and the apply and gains the render between
them:

```go
loaded, err := networkjson.Load(networkjson.DefaultPath, networkjson.DefaultSchemaDir)
if err != nil {
	log.ErrorContext(ctx, "network configuration load failed", "err", err)
	return fmt.Errorf("load network configuration: %w", err)
}
loaded.Apply(cfg)

changed, err := networkd.WriteDir(networkd.DefaultUnitDir, loaded.Links)
if err != nil {
	log.ErrorContext(ctx, "writing networkd unit files failed", "err", err)
	return fmt.Errorf("write networkd unit files: %w", err)
}
log.InfoContext(ctx, "networkd unit files written", "changed", changed)
if len(changed) > 0 {
	if err := networkd.ReloadIfRunning(ctx); err != nil {
		log.ErrorContext(ctx, "reloading systemd-networkd failed", "err", err)
		return fmt.Errorf("reload systemd-networkd: %w", err)
	}
}
```

At boot the daemon runs first, so `ReloadIfRunning` finds the network manager
inactive and does nothing: the manager reads the fresh files when it starts.
After a deploy the daemon restarts while the manager is already running, and the
reload is what makes a changed `.network` or `.netdev` take effect without a
reboot.

A changed `.link` is not carried by that reload, because udev reads a `.link`
when the device appears and the reload does not revisit it. `WriteDir`
therefore returns which kinds changed, and the caller compares the name each
rendered `.link` asks for against the name the link currently carries. A
difference is logged at warning level naming both names and saying the new one
takes effect at the next reboot. The daemon never attempts the rename, which
the kernel refuses on a link that is up.
`networkjson.ApplyDefault` stays as it is for the debug command, which reads the
configuration and renders nothing.

- [ ] **Step 5: run the tests and the gates**

```bash
make test
make check
make build
```

Expected: PASS. The existing startup tests prove the load path still fails the
same way on a bad document.

- [ ] **Step 6: commit**

```bash
git add mwan/go
git commit -S -m "Write and prune provider networkd unit files from the loaded network configuration"
```

---

### Task 9: a gate proves the rendered files match the hand-authored ones

This gate is what makes Task 10 safe. It renders both sides from the checked-in
inventory and compares them, so a value carried wrongly into the provider entry
in Task 6 is caught before the templates are deleted.

**Files:**
- Create: `mwan/scripts/networkd-fidelity.py`
- Create: `mwan/go/internal/networkd/cmd/render/main.go`
- Modify: `mwan/go/Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: Task 6's inventory and template, Task 7's renderer.
- Produces: a red gate for any drift, which Task 10 requires green.

- [ ] **Step 1: write the render helper**

A small program in the package's own `cmd` directory that reads a `network.json`
path and a target directory and writes the rendered files there. It exists for
the gate, so the gate runs the same code the daemon runs. It is not installed on
a gateway and is not a verb of the `mwan` binary.

- [ ] **Step 2: write the comparison script**

For one gateway group the script loads `ansible/inventory/group_vars/all/vars.yml`
and that group's variables with PyYAML, renders `mwan/config/network.json.j2` and
every remaining per-provider template under `mwan/networkd/` with Jinja2, runs
the helper over the rendered JSON, and compares each pair. Comment lines and
blank lines are stripped from both sides before the comparison, and keys within
one section are compared as a set so a reordering inside a section is not a
failure. A missing file, an extra file, a missing key, or a differing value is a
failure that prints the file, the section, and the key.

AT&T's four files are excluded outright, because its entry names them
hand-authored and describes nothing about that link. The script reads the
`link_files` leaf to decide, rather than carrying a provider name.

For every provider whose entry does name a VLAN parent, the script reads the
parent's rendered file and fails when it carries no line naming the child, and
fails in the other direction when a parent names a VLAN no entry declares.

- [ ] **Step 3: run it against both gateway groups and fix what it finds**

```bash
python3 mwan/scripts/networkd-fidelity.py mwan_servers
python3 mwan/scripts/networkd-fidelity.py mwan_suburban_servers
```

Expected: both report every provider matching. Anything it reports is a value
Task 6 carried wrongly; fix the inventory entry, not the script.

- [ ] **Step 4: wire it into the make target and CI**

A `networkd-fidelity` target in `mwan/go/Makefile` runs both groups, and `check`
depends on it. The CI workflow runs the same target.

- [ ] **Step 5: commit**

```bash
git add mwan/scripts mwan/go .github/workflows/ci.yml
git commit -S -m "Compare rendered and hand-authored networkd unit files in CI"
```

---

### Task 10: the per-provider templates and the deploy loop go

**Files:**
- Delete: every file under `mwan/networkd/` except AT&T's four: `20-att.link.j2`, `20-att.network.j2`, `21-att-vlan.netdev.j2`, and `21-att-vlan.network.j2`
- Modify: `ansible/inventory/group_vars/mwan_servers.yml`, `mwan_suburban_servers.yml`
- Modify: `ansible/playbooks/deploy-mwan.yml`

**Interfaces:**
- Consumes: Task 9's gate, green.
- Produces: a repository with no per-provider unit file, which is the epic's acceptance sentence.

- [ ] **Step 1: shorten `mwan_networkd_files` in both groups**

Each group keeps the management pair, the internal bridge pair, and, for
production, AT&T's four files. Every other name goes.

- [ ] **Step 2: delete the templates**

Delete every per-provider template the list no longer names, including the
testbed forks and the astound pair.

- [ ] **Step 3: point the gate at what remains**

The gate now has only AT&T's four files to exclude and no per-provider
templates to render. It keeps running as a guard that nothing reintroduces one:
a template under `mwan/networkd/` for a provider whose entry does not name its
files hand-authored is a failure.

- [ ] **Step 4: run the gates**

```bash
make check
```

Expected: PASS, and the deploy's own lint passes with the shortened list.

- [ ] **Step 5: commit**

```bash
git add -A mwan/networkd ansible
git commit -S -m "Delete the per-provider networkd templates and their deploy list"
```

---

### Task 11: the testbed renders, then production does

**Files:**
- Modify: `docs/superpowers/runbooks/2026-09-16-daemon-boot-order.md`

**Interfaces:**
- Consumes: Tasks 4 through 10, merged and released.
- Produces: the proof the epic's acceptance sentence needs.

- [ ] **Step 1: capture the testbed, including the files themselves**

Take the Task 2 captures, and add the unit files as they stand before the
cutover:

```bash
ssh mwan-testbed 'for f in /etc/systemd/network/*; do echo "== $f"; cat "$f"; done' > before-unit-contents.txt
```

- [ ] **Step 2: deploy and reboot**

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
ssh mwan-testbed 'systemctl reboot'
```

- [ ] **Step 3: compare the files the daemon wrote against the files it replaced**

```bash
ssh mwan-testbed 'for f in /etc/systemd/network/*; do echo "== $f"; cat "$f"; done' > after-unit-contents.txt
```

Expected: every provider file differs only in its comment lines, and the AT&T
physical pair is byte identical because nothing rendered it. This is the same
comparison the gate runs, made on the gateway against files a previous release
wrote, which is the one place the inventory, the template, the loader, and the
renderer are all real at once.

- [ ] **Step 4: prove behavior is unchanged**

Repeat Task 2 Steps 3, 4, and 5 in full. The failure cases run again because the
files now come from a different writer.

- [ ] **Step 5: production**

Ask the operator. Run the check-mode run, capture, announce the window, ask
again, deploy, prove, and report the window closed. Record every command, host,
and output.

---

## Failure modes

**The daemon starts before the rest of the system is up.** `DefaultDependencies=no`
removes the implicit ordering after `sysinit.target` and `basic.target` along
with everything else, so the daemon can reach code that expects a running
journal, a mounted state directory, or a reachable datastore before any of them
exist. The explicit `After=local-fs.target systemd-journald.socket` covers the
two the render itself needs. Everything past the render is covered by Task 2's
failure cases rather than by ordering: the exercise is what shows whether a
module's initialization tolerates an early start. A module that does not is a
finding against this plan, not a reason to reorder.

**A unit file written after udev has already named a device does nothing.** The
kernel refuses to rename a link that is up, so a late `.link` file is silently
ineffective and the interface keeps the name it was given. The symptom is an
interface named by its driver rather than by the provider, and every layer above
keys on the provider name. Task 2 Step 3 reads the ordering out of the journal
rather than assuming it. The same limit applies at runtime: a `.link` changed
by a reload waits for the next reboot, which the daemon logs rather than
hiding.

**A VLAN whose parent does not name it is created by nothing.** The provider is
then absent, with no error from the network manager, the daemon, or the deploy,
because nothing asked for a device that does not exist. Two checks cover it:
the loader fails when a VLAN parent matches no interface in the document, and
the gate compares the naming line in both directions against the parent's file.

**An exemption inferred from an absence hides a mistake.** An entry with no
link block looks the same whether its files are deliberately hand-authored or
its link block was never written. The `link_files` leaf makes the first case
explicit and the second a load failure, which is why the exemption is a
statement rather than a silence.

**A rewrite with identical content changes a modification time.** That is enough
for udev to reconsider a device. `WriteDir` compares before it writes, and the
test's second pass is what keeps that true.

**Pruning by name would delete a file the deploy owns.** The management link, the
internal bridge, and the AT&T physical pair live in the same directory. Pruning
is by marker line, never by name or by pattern.

**A free-form key is not checked by the deploy's schema validation.** A typo
there reaches the gateway and surfaces as a network manager warning, not as a
deploy failure. The typed leaves exist so the common shapes never take that
path, and the fidelity gate reports a free-form key that no longer matches the
file it replaced.

**Deleting the templates before the gate is green leaves a gateway whose links
come up differently after a reboot.** Task 10 depends on Task 9 for exactly this
reason, and Task 11's file comparison is the second check on the same risk.

**The IPv4 source pin is the static address, so it is never typed twice.** The
routing module's own contract defines the pin as the link's static address.
The loader fills `V4Source` from the address leaf and refuses a document that
still carries `v4-source`, so the two cannot drift. A provider that must source
traffic from further addresses lists them in `source-addresses`; a listed
address the provider does not route back is dropped at the provider's edge
with no error on the gateway, which the daemon cannot detect and the operator
can.
