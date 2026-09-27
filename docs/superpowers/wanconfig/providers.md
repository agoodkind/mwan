> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Three: the provider set becomes data

A provider is one entry in inventory. Adding, removing, re-tiering, or
re-weighting a provider is an edit to that entry and a configuration deploy.
The entry holds the provider's routing numbers, tier, weight, translation
prefix, probe policy, and link identity. The daemon validates the entry at
load and renders the systemd-networkd unit files for the provider's link
from it. No provider name appears in Go outside tests, and no file is
written by hand for a provider.

One provider is exempt: AT&T, whose link runs 802.1X authentication before the
provider answers a lease at all. Its entry says so in one leaf and carries no
link identity, so the daemon renders nothing for it and its four unit files
stay in the repository: the physical `.link` and `.network`, the VLAN
`.netdev`, and the VLAN `.network`. Those files are the description of that
link. Nothing in the configuration file describes it a second time, and
nothing checks them against anything, because a second description of a link
this delicate is the thing worth avoiding. The authentication chain that
drives them stays beside them.

This piece depends on the configuration format, so the inventory is written
once in its final shape.

## Why a fourth provider is impossible today

Five things block it.

The daemon carries att, webpass, and monkeybrains as constants and decides
the fallback by comparing against one of them.

Two validators accept only the rule priorities 100, 200, 300 and 55, 56, 57.
A failed validation stops the daemon, so a provider with any other numbers
cannot start.

The load balancer is three fixed lines in the firewall ruleset file, one for
IPv4 and two for IPv6. Each line chooses between marks 1 and 2, so a third
member cannot be selected.

The network configuration renders from a template that names the three
providers, so a fourth inventory entry is not rendered.

The systemd-networkd unit files are written by hand, one pair per provider,
each named in a deploy list. A fourth provider needs two to four new files,
and nothing checks them against the provider's other values.

## Inventory takes the model's shape

Each gateway group carries one list with one entry per provider. Every value
the gateway reads about a provider is in that entry, including the hardware
values. No per-provider variable exists outside the list.

The production group after this piece:

```yaml
mwan_providers:
  - name: att
    table: 100
    mark: 1
    mark_prio: 100
    from_prio: 55
    tier: 0
    weight: 1
    # This link's four unit files are hand-authored and describe it on their
    # own, so the entry carries no link block and no per-family addressing.
    # It names the interface the daemon steers and says nothing about how
    # that interface comes up.
    link_files: hand-authored
    iface: enatt0.3242
    npt_prefix: "2600:1700:2f71:c80::/60"
    forced_dscp: cs1
    static_mappings:
      - { internal: "10.250.250.2", external: "104.57.226.193" }
      # ... four more
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
    table: 200
    mark: 2
    mark_prio: 200
    from_prio: 56
    tier: 0
    weight: 1
    link:
      iface: enwebpass0
      match: { driver: igc }
      mac: "..."
    ipv4:
      forwarding: true
      address: "136.25.91.242/29"
      gateway: "136.25.91.241"
      route_metric: 10
    ipv6:
      forwarding: true
      dhcp: true
      accept_ra: true
      delegation:
        hint: "::/56"
        duid_type: link-layer-time
        duid: "..."
    npt_prefix: "2604:5500:c271:be00::/60"
    static_mappings: [ ... ]
    health: { ... }

  - name: monkeybrains
    table: 300
    mark: 3
    mark_prio: 300
    from_prio: 57
    tier: 1
    weight: 1
    link:
      iface: enmbrains0
      match: { mac: "..." }
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
        duid: "..."
        use_delegated_prefix: true
        without_ra: solicit
        router_lifetime_seconds: 1800
    npt_prefix: "2607:f598:d3e8:4500::/60"
    health: { ... }

mwan_hash_mode: random
mwan_reserved_tables:
  cloudflared: 400
  oob: 500
mwan_pin_provider: att
```

A fourth provider is one more entry with `table: 600`, `mark: 4`,
`mark_prio: 600`, `from_prio: 58`, its own tier, and its own link block.
Tables 400 and 500 are reserved, so 600 is the first free hundred. An
IPv4-only provider has no `npt_prefix` and no IPv6 probe targets. It gets no
IPv6 lease, no translation, and no IPv6 source rule. Its `ipv6` block states
`dhcp: false` and `accept_ra: false`, which renders `IPv6AcceptRA=no` into its
unit file. The loader rejects any entry with an `ipv4` or `ipv6` block that
omits `dhcp`.

Each value is typed once. Where a gateway entry and a simulator definition
describe the same wire, both read the service map.

The IPv4 source pin is not typed at all. The routing module pins traffic the
gateway sources from a provider's own address to that provider's table, and
that address is the link's static address, so the daemon takes the pin from
the address leaf. A leased link has no static address and gets no pin, as
today. A provider that must also source traffic from other addresses, such as
the rest of a delegated block held on the link or a routed prefix on a
loopback, lists them under `source_addresses`, and the pin covers the link
address plus that list. The list adds to the link address; it never replaces
it, because a source the provider does not route back is dropped at its edge.

Inventory writes an address the way an operator writes one, as a prefix in
slash notation. The published model splits it into an address and a prefix
length, so the template that renders the configuration file splits it too. That
is the only shape difference between an entry and the document it produces, and
it exists because both sides are written for their own reader.

The pinned-destination lists carry no provider name. `mwan_pin_provider`
names the provider the pins target. The seed and name lists are named for
what they pin. The kernel set names and the refresher timer keep their
current names until the refresher moves into the daemon under its own ticket.
The two WireGuard control-plane pins in the firewall ruleset file use the pin
provider's mark.

The network configuration renders by looping over the list. No template
names a provider.

## Routing numbers are typed, and only checked

Each provider carries its routing table, firewall mark, and two policy rule
priorities as typed values. Nothing derives them. The current numbering (100,
200, 300 for tables and mark-rule priorities; 1, 2, 3 for marks; 55, 56, 57
for source-rule priorities) does not change.

Three checks run at load. Every provider's table, mark, mark-rule priority,
and source-rule priority is unique across providers. No provider's table is
in the reserved set. Every weight is at least one. Two checks in the routing
module stay: a mark is never zero, because zero is the unmarked state the
balancing rule's guard tests, and neither rule priority equals the catch-all
priority the routing module owns. A failed check stops the daemon before it
touches the kernel.

The reserved set is typed once in inventory, in `mwan_reserved_tables`,
rendered into the network configuration under the steering group, and read
from there by the daemon. The tunnel table, 400, and the out-of-band table,
500, are in the inventory registry that names the routing tables, and every
template that needs one reads the registry. The kernel's own tables (253,
254, 255, and 0) are always reserved. No reader carries a copy of the set.

The fixed priority checks are deleted and the new checks land in one change.
No state exists where one layer accepts a fourth provider and another rejects
it.

## Steering becomes tier and weight

Every provider carries a tier and a weight. The active tier is the
lowest-numbered tier with at least one healthy provider. A new connection
from an internal source is assigned a mark computed over that tier's healthy
providers: a generated number modulo the sum of their weights, mapped onto
their marks with one slot per weight unit. A weight is a positive integer,
so the sum is never zero. `mwan_hash_mode` selects whether the number is
random per connection, derived from the source address, or derived from
source and destination.

The tiers in inventory decide fallback. A provider alone in its tier is the
sole carrier when that tier is active. Providers that share a tier share it
by weight. The daemon carries no tie-break rule.

The daemon owns the balancing rule. A steering module computes the rule from
the active tier and programs it into a kernel table and chain the module
creates, with the same apply discipline the translation module uses: create
the table, create the chain, clear the chain, add the rules, commit once, and
repair a flushed table through the watcher. The three fixed lines leave the
firewall ruleset file in the same change. The firewall piece later gives the
daemon the whole ruleset, and this rule stays where it is.

An unhealthy provider leaves the split on the next reconcile pass, and its
policy rules are pruned. It does not fall through to the main table.

An unknown health state reads as healthy. Before the health module writes its
first state, every provider reads healthy and the first tier activates.

## Link bring-up renders from the provider entry

systemd-networkd brings links up: it matches the device, names it, sets its
address, and runs both DHCP clients. The daemon writes its unit files. From
the loaded network configuration it renders one `.link` and one `.network`
per provider, plus a `.netdev` and a second `.network` for a provider on a
VLAN. No per-provider template exists in the repository. The deploy copies no
unit file for a provider link.

The daemon starts before udev names the devices and before systemd-networkd
starts: its unit carries `DefaultDependencies=no` and orders itself before
`systemd-udev-trigger.service` and `systemd-networkd.service`. udev applies a
`.link` file when a device appears, and the kernel refuses to rename a link
that is already up, so the files must be on disk before either happens. The
daemon writes the unit files first, then waits on netlink for the links as it
does today. It renders again whenever it reloads the configuration, tracks
which unit files it wrote for the current provider list, deletes any it wrote
for a provider that list no longer names, writes a file only when the content
differs from what is on disk, and asks systemd-networkd to reload after a
write or a deletion. Its sandbox gains a write path for
the network manager's unit directory. The firewall keeps loading before
`network-pre.target`, and the daemon takes no ordering after
`network-online.target`, which would close a cycle.

A reload does not carry a changed `.link` file. udev reads that file when the
device appears, and the network manager's reload does not revisit it, so a
device already present keeps the name and the address it was given at boot.
The daemon writes the new file, compares the name it asks for against the name
the link currently has, and logs the difference rather than renaming a live
link, which the kernel refuses anyway. Changing an existing provider's
interface name or device match therefore takes effect at the next reboot. A
provider whose device is attached after the deploy is not affected, because
udev reads the new file when that device appears. A changed `.network` or
`.netdev` does take effect on reload, so addressing, routing, and the
delegation change without a reboot.

The entry describes a link in two layers.

**Typed leaves.** The standard per-family containers, plus the leaves this
piece adds for what the published models do not name: how the device is
matched (by driver or by hardware address), the interface name, the hardware
address, whether each family runs a DHCP client, the delegation client's
identity and hint, whether the delegation is solicited without a router
advertisement, whether the delegated prefix is applied to the link, the
downstream router lifetime, whether router advertisements are accepted, the
route metric, and an optional VLAN parent with its tag. The schema validates
each one. The daemon reads the ones it needs for its own behavior and maps
each leaf to a unit-file section and key through one table. That table is the
only place a networkd key name appears in Go. Every line the current
providers' files carry has a typed leaf, so their free-form sections are
empty.

**Free-form sections.** An augment on the interface that mirrors the unit
file format: for each of `link`, `network`, and `netdev`, an ordered list of
sections, each an ordered list of key and value pairs. The schema validates
the structure. networkd validates the keys. Any shape networkd can read can be
written here: a bond, a bridge, a tunnel, a VLAN stack, or an option added to
networkd after the model revision. A shape can start as free-form sections
and gain typed leaves later without a renderer change.

The renderer maps the typed leaves through its table, then appends the
free-form sections. A section named by both layers is merged under one
heading, because networkd treats repeated headings as one. A key named by
both layers is a load error. The unit files are written through a maintained
serializer for the systemd unit format, so the daemon owns the mapping and
not the syntax.

The current three providers render from typed leaves except for the keys
named in the examples below. The fidelity gate lists every free-form key the
current providers use.

An entry the renderer rejects stops the daemon at load, before it writes any
file or touches the kernel.

### Example: a static link with a delegation client

Webpass in `network.json`, link identity only. Node names are the ones the
model defines.

```json
{
  "name": "enwebpass0",
  "type": "iana-if-type:other",
  "goodkind-mwan-steering:link": {
    "match": { "driver": "igc" },
    "hardware-address": "00:00:00:00:00:00"
  },
  "ietf-ip:ipv4": {
    "forwarding": true,
    "address": [ { "ip": "136.25.91.242", "prefix-length": 29 } ],
    "goodkind-mwan-steering:dhcp": false,
    "goodkind-mwan-steering:gateway": "136.25.91.241",
    "goodkind-mwan-steering:route-metric": 10
  },
  "ietf-ip:ipv6": {
    "forwarding": true,
    "goodkind-mwan-steering:dhcp": true,
    "goodkind-mwan-steering:accept-ra": true,
    "goodkind-mwan-steering:delegation": {
      "hint": "::/56",
      "duid-type": "link-layer-time",
      "duid": "00:00:00:00:00:00:00:00:00:00:00:00"
    }
  },
  "goodkind-mwan-steering:wan": { "name": "webpass", "table-id": 200 }
}
```

The route metric and the delegation client sit on the family they address,
`ietf-ip:ipv4` and `ietf-ip:ipv6`, not on the `link` container. The `link`
container carries only what identifies and creates the device.

The daemon writes `20-webpass.link`:

```ini
[Match]
Driver=igc

[Link]
Name=enwebpass0
MACAddress=00:00:00:00:00:00
```

and `20-webpass.network`:

```ini
[Match]
Name=enwebpass0

[Network]
Address=136.25.91.242/29
DHCP=ipv6
IPv6AcceptRA=yes
IPv4Forwarding=yes
IPv6Forwarding=yes

[DHCPv6]
DUIDType=link-layer-time
DUIDRawData=00:00:00:00:00:00:00:00:00:00:00:00
PrefixDelegationHint=::/56

[Route]
Gateway=136.25.91.241
Metric=10

[Route]
Gateway=136.25.91.241
Table=200
```

Every line comes from a typed leaf. The second route's table is the
provider's routing table, taken from `table-id`.

### Example: a key with no typed leaf

A provider whose DHCPv6 lease must not supply resolvers needs `UseDNS=no`
under `[DHCPv6]`. No typed leaf names it. The entry carries it as a
free-form section, keyed by position:

```json
"goodkind-mwan-steering:networkd": {
  "file": [
    { "kind": "network", "section": [
      { "index": 0, "name": "DHCPv6", "entry": [
        { "index": 0, "key": "UseDNS", "value": "no" }
      ] }
    ] }
  ]
}
```

The rendered `.network` file carries it after the typed keys under the same
heading:

```ini
[DHCPv6]
DUIDType=link-layer-time
DUIDRawData=00:00:00:00:00:00:00:00:00:00:00:00
PrefixDelegationHint=::/56
UseDNS=no
```

If the free-form sections also set `PrefixDelegationHint`, the daemon stops
at load with:

```
network.json: enmbrains0: networkd section DHCPv6 key PrefixDelegationHint is set by the delegation hint leaf; remove one
```

### Example: a provider on a VLAN

A provider that hands off on a tagged VLAN names its parent and its tag. The
parent is an interface in the same document, with a link block of its own:

```json
"goodkind-mwan-steering:link": {
  "vlan": { "parent": "ensonic0", "id": 101 }
}
```

The daemon writes the VLAN's `.netdev`:

```ini
[NetDev]
Name=ensonic0.101
Kind=vlan

[VLAN]
Id=101
```

and its `.network`, from the same typed leaves every other provider uses.

A VLAN exists only because its parent's `.network` names it, with a line
carrying the child's name:

```ini
[Network]
VLAN=ensonic0.101
```

The renderer emits that line into the parent's file from the child's entry.
No entry declares its own children; the child names its parent, and the
renderer reads the list the other way.

AT&T is not this case. Its link comes up through 802.1X authentication and its
four unit files are hand-authored, so its entry carries no link block and the
daemon writes nothing for it.

### Example: a shape with no typed leaf at all

A bonded uplink has no typed leaves in this piece. It is one entry with
free-form sections:

```json
"goodkind-mwan-steering:networkd": {
  "file": [
    { "kind": "netdev", "section": [
      { "index": 0, "name": "NetDev", "entry": [
        { "index": 0, "key": "Name", "value": "bond0" },
        { "index": 1, "key": "Kind", "value": "bond" }
      ] },
      { "index": 1, "name": "Bond", "entry": [
        { "index": 0, "key": "Mode", "value": "active-backup" }
      ] }
    ] },
    { "kind": "network", "section": [
      { "index": 0, "name": "Network", "entry": [
        { "index": 0, "key": "DHCP", "value": "yes" }
      ] }
    ] }
  ]
}
```

The daemon writes a `.netdev` and a `.network` with those sections.

## The watchdog holds no provider list

The rollback watchdog on the hypervisor pings the internet through each
provider interface during a diagnosis. After this piece the gateway daemon
pushes its per-provider health verdict to the watchdog. The watchdog keeps its
basic egress pings and smoke checks, drops its per-interface pings, and holds
no interface names.

The push is advisory and stateless. Every message carries the whole verdict,
one entry per provider plus the active tier. The watchdog keeps the latest
message and the time it arrived, and logs both during a diagnosis. A restart
on either side, or a lost message, is repaired by the next probe cycle, which
sends the whole state again. A watchdog that has received nothing reports
that it holds no verdict. No rollback decision reads the verdict in this
piece. Whether it blocks a rollback is separate work (MWAN-442, MWAN-332,
MWAN-336).

## Carried through unchanged

The IPv6 source-pin prefix stays a configured value. Steering builds a policy
rule from it, and the cleanup pass claims that rule's priority
unconditionally, so rendering the value empty deletes the live rule rather
than skipping it. Moving the pin onto the live delegation is separate work,
because at daemon start the delegation may not be readable yet.

The IPv4 source pin is different: its value is the link's static address,
which the entry already carries, so it is derived rather than typed. The
served tree keeps reporting it under the same leaf, now filled by the daemon
from the address, so a reader of the served tree sees no change.

Networkd manages links and delegation in this intermediate rendering stage.
MWAN-305 replaces that management under the
[interface ownership specification](../../interfaces.md). Its migration
assigns each object one writer and completes the required acquisition support
before transferring a connection.

## Acceptance

No provider name remains in Go outside tests or in an inventory variable that
the daemon or a rendered template reads by provider name.

For the current provider set, the routes, policy rules, and the served tree
are unchanged. The firewall rules are unchanged except that the three
balancing lines move from the ruleset file into the daemon's chain, where they
express the same half-and-half split.

The daemon's move before udev and the network manager is proven on the
testbed before it reaches production: a cutover with a reboot that records
the daemon starting before both, every provider link up with its name and
lease, and rules, routes, firewall and served tree unchanged; a failover
exercise (the fallback drill in both families, a reboot with one link
absent, the AT&T 802.1X path, and a daemon restart while links are up); and
a check-mode run against the production gateway before the production
cutover.

The rendered systemd-networkd units for the current provider set are
identical to the hand-authored files they replace, outside comment lines.
The comparison ran once, by operator ruling, against the checked-in files
before those files were deleted (agoodkind/mwan Actions run 35522419921), and
again on each gateway after its cutover by comparing every file in
`/etc/systemd/network` with the capture taken before it. No permanent CI gate
exists. AT&T's four files are excluded, because they stay and nothing in
the configuration file describes that link.

A fourth provider can be added, re-tiered, and removed by one inventory entry
and a configuration deploy, with the binary unchanged and no file written by
hand. Traffic is observed leaving it at the simulator's ingress in every
address family the provider has. The testbed's fourth simulated provider,
astound, is IPv4-only, so its proof is IPv4 only.

## Failure modes

Deleting the priority checks and introducing the new checks in separate
changes leaves a state where one layer accepts a fourth provider and another
rejects it. Do both in one change.

Deleting the hand-authored unit files before the rendered ones are proven
identical leaves a gateway whose links come up differently after a reboot.
Every layer above keys on interface names. The fidelity comparison gates the
deletion.

A free-form key is not checked by the deploy's schema validation. A typo
there surfaces as a networkd warning on the gateway, not as a deploy failure.
Every line the current providers need has a typed leaf, so no current
provider takes that path, and the free-form layer is exercised only by its
tests until a provider needs it.

A source address the provider does not route back is dropped at the
provider's edge as spoofed, with no error on the gateway. `source_addresses`
therefore only adds to the link address and never replaces it, and a listed
address must sit on the link or in a prefix the provider routes to the
gateway. The daemon cannot check the second condition; the operator can.

A VLAN whose parent's file does not name it is created by nothing, and the
provider is simply absent with no error from any layer. The daemon fails the
load when a provider declares a VLAN parent that no interface in the same
document describes, which is the case this catches. AT&T is outside it: its
entry declares no parent, and its hand-authored files carry the naming line
themselves.

A provider with no link identity is a provider the daemon does not bring up.
That is the point for AT&T, whose files are hand-authored, and it is a mistake
for anyone else. The loader rejects any entry that has no link block and no
`link_files: hand-authored` leaf. The exemption is written down rather than
inferred from an absence.

The loader rejects any provider entry with a defect inside it, logs the
interface name, the provider name and the reason at error level, and runs on
the remaining entries. The served steering state reports each rejected entry
with the same identity and reason. The deploy runs the production loader and
rejects the entire intended change when the runtime would omit any provider.

Schema validation runs before these provider-local checks. A schema-invalid
document remains fatal because it is not an accepted configuration tree. The
daemon also does not start on a missing group-wide value, a routing number two
providers share, a reserved table, or a document with no loadable provider.
systemd-networkd keeps every other link configured when one `.network` file is
bad (MWAN-506).

The ordering within the firewall's translation chain decides behavior,
because a translation statement stops rule evaluation. Grouping outbound
rules by provider is equivalent to the current grouping only because every
outbound translation rule carries an outgoing-interface match, and the
inbound one-to-one rules match on the incoming interface. Assert both
invariants.

The steering module's chain must run after the ruleset file's mangle chain,
which restores the connection mark and sets the ingress marks. Its balancing
rules must keep the `meta mark 0` guard. Otherwise the control-plane pins set
earlier in the pass are overwritten.
