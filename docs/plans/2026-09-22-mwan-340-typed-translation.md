> Read the [repository context](../README.md) before using copied commands or historical plans.

# MWAN-340 Typed Translation Implementation Plan

**Goal:** Make translation an explicit per-member, per-family policy. Preserve
the current providers' packet behavior, support native routed IPv6 without
DHCP prefix delegation, and stop steering one family through a member when
that family's required translation is absent.

**Architecture:** Each interface family declares one base mode: native,
NPTv6, or NAPT44 masquerade. Optional IPv4 static mappings take precedence
over masquerade for matching addresses. NPTv6 maps its configured IPv6
prefixes one to one. The configuration loader converts this policy into typed
Go values. MWAN realizes IPv6 translation, publishes configured and realized
state, and supplies per-family translation readiness to steering. Configs
continues to render IPv4 NAT until MWAN-341 transfers all firewall ownership
to the daemon.

**Public proof:** Validate rendered configuration through the released `mwan`
command, publish it through real sysrepo, and verify packets with Linux network
namespaces and the testbed ISP simulators. Add no helper-only unit-test suite.
Update existing low-level tests only when compilation or an existing contract
requires it.

**Specifications:** [Translation](../superpowers/wanconfig/translation.md),
[model](../superpowers/wanconfig/model.md), [configuration](../superpowers/wanconfig/config.md), and
[firewall ownership](../firewall.md).

## Decisions

- Each family has one base mode. IPv4 supports native and NAPT44 masquerade.
  IPv6 supports native and NPTv6.
- IPv4 static one-to-one mappings are optional entries within NAPT44. They are
  not an exclusive base mode. A matching mapping takes precedence over
  masquerade. NPTv6 provides one-to-one translation across its IPv6 prefixes.
- Native mode is explicit. It does not require a DHCP delegation or create a
  translation rule.
- NPTv6 selects either a configured external prefix or a delegated external
  prefix. Delegated mode may include an expected prefix for operator alerts.
  Only delegated mode depends on DHCP prefix delegation.
- Every policy references an interface. Provider company, ASN, and tunnel
  type do not select translation behavior.
- Direct and tunnel interfaces use the same policy. External BGP adds routing
  readiness later and does not add a translation type.
- Translation readiness is separate for IPv4 and IPv6. A missing required
  translation excludes only that family from steering.
- MWAN-340 introduces the shared per-family eligibility input that steering
  needs. MWAN-507 extends the same input with physical link, tunnel, route,
  probe, and BGP dependencies.
- Configs remains the IPv4 NAT writer during MWAN-340. The daemon remains the
  IPv6 NPT writer. MWAN-341 later transfers the remaining firewall writes
  without changing this configuration model.
- Configs updates the inventory, JSON renderer, and IPv4 firewall renderer
  before deployment. Separate release pin changes install the released loader
  in the testbed first and production after testbed acceptance. The deploy
  validates the new document before restarting the daemon.

## Scope limits

- Sonic and Astound can operate as ordinary MWAN providers before any tunnel,
  external ASN, or BGP configuration exists.
- The first external BGP deployment is IPv6 only. Ordinary IPv4 keeps its
  independent addressing, translation, routes, and steering.
- MWAN-340 does not choose GRE, IP-in-IP, 6in4, or another tunnel protocol.
- MWAN-340 does not transfer full firewall ownership, rebuild tables after a
  complete ruleset deletion, or add BGP policy.
- MWAN-340 publishes the resolved delegated prefix that MWAN-333 needs. It
  does not change the separate IPv6 source-pin policy rule. MWAN-333 consumes
  that value after typed translation is available.
- Correct RFC 6296 translation can change the non-prefix portion of current
  external IPv6 addresses. Production activation must inventory and update
  any externally published address that depends on the current NETMAP result.
- Production exercises for the new circuits wait until after October 2026.
  Testbed proof and existing-provider compatibility can finish before then.

## Task 1: Add typed translation to the MWAN model and loader

**Repository:** `agoodkind/mwan`

**Files:**

- Rename and modify the current schema revision under
  `gateway/internal/yangpub/schema/goodkind-mwan-steering@2026-09-21.yang`.
- Modify `gateway/internal/yangpub/schema.go` and
  `gateway/internal/yangpub/schema_cgo.go` for the new revision and features.
- Modify `gateway/internal/networkjson/networkjson.go`.
- Modify `gateway/internal/config/ifmgr_modules.go`.
- Modify the valid documents under `gateway/yang/instances/`.
- Preserve `gateway/cmd/mwan/testdata/goodkind-mwan-steering@2026-09-13.yang` as the
  install-upgrade fixture.

### Model contract

Add a translation policy beneath each configured IPv4 and IPv6 family. The
policy contains:

- an explicit base mode;
- the outgoing interface association supplied by the containing interface;
- NPTv6 internal-prefix and external-prefix source data when the mode is
  NPTv6;
- optional IPv4 static mappings with internal and external IPv4 addresses.

Use RFC 8512 NAT identities and data where they express the policy. Keep the
local policy as the source of native mode, interface association, and a
delegated NPTv6 prefix source, which RFC 8512 does not express. Publish the
resolved NPTv6 prefix pair and static mappings through the RFC 8512 tree.

Define concrete Go types for mode, prefix source, NPTv6 data, and static
mapping. Replace `NptPrefix string` and the unscoped static mapping slice in
`config.IfMgrWANEntry`. Do not represent a mode with an empty string or infer
it from prefix presence.

Extend `networkjson.document` and `buildProvider` to decode and validate the
new policy. Reject these document errors with the member and family in the
message:

- a mode that does not apply to the family;
- NPTv6 without an internal prefix or usable external-prefix source;
- a configured NPTv6 source without an external prefix;
- a delegated NPTv6 source on a family with no delegation configuration;
- a static mapping with a non-IPv4 address;
- duplicate external mapping addresses;
- a translated family without a usable outgoing interface.

Keep the existing loader boundary. A schema-invalid document fails entirely.
A provider-local semantic error remains a provider rejection at runtime, and
`mwan deploy-gate check-network` fails the deployment when any provider would
be rejected.

### Public verification

Extend `gateway/cmd/mwan/deploygate_checknetwork_test.go` with one accepted typed
document and one rejected family or mode combination. Run the real child
process command. Extend `gateway/cmd/mwan/wanconfig_roundtrip_test.go` so the normal
loader, `Apply`, module projection, and publisher chain compares translation
input instead of excluding every `/ietf-nat:nat/` leaf.

Run:

```bash
make -C gateway wanconfig-builder-image
docker run --rm --platform linux/amd64 \
    -v "$PWD:/src" -w /src/gateway \
    -v mwan-wanconfig-gomod:/go/pkg/mod \
    -e GOWORK=off \
    mwan-wanconfig-builder \
    go test -count=1 ./internal/networkjson ./cmd/mwan
```

Expected: the command accepts the valid policy, rejects the invalid policy
with the member and family, and the round trip preserves every configured
translation value.

## Task 2: Project typed policy and publish configured translation

**Repository:** `agoodkind/mwan`

**Files:**

- Modify `gateway/cmd/mwan/ifmgr_module_configs.go`.
- Modify `gateway/cmd/mwan/wanconfig_publish.go`.
- Modify `gateway/cmd/mwan/ifmgr.go` where the shared live-state store is created.
- Modify `gateway/internal/wanconfig/tree.go`.
- Modify `gateway/cmd/mwan/wanconfig_roundtrip_test.go`.
- Modify `gateway/cmd/mwan/wanconfig_selftest.go` and
  `gateway/cmd/mwan/wanconfig_selftest_test.go`.

Replace `sharedWAN.NptPrefix` with the typed per-family policy. Project the
same value into the translation module, routing address ownership, steering,
and the published tree. Assign one stable translation identity during this
projection and reuse it for configuration and operational state. Do not
number instances independently in the publisher and live-state code.

Publish configured intent and resolved state separately:

- native mode publishes the explicit local family mode and no NAT rule;
- delegated NPTv6 publishes the configured source choice immediately and the
  RFC 8512 internal and external prefix pair after delegation resolves;
- configured NPTv6 publishes its complete prefix pair immediately;
- NAPT44 publishes one instance with its static mapping table;
- static mappings remain associated with their IPv4 interface.

Create `wanstate.Store` before opening sysrepo and pass it to every runtime
module even when the management surface is disabled or unavailable. Runtime
readiness must not depend on sysrepo. Add the resolved NAT items to the live
operational provider so delegation arrival, renumbering, and loss change the
served RFC 8512 tree on the next read. Do not freeze a delegated prefix in the
one-time configuration publication.

Extend the private sysrepo selftest to export and inspect both
`/ietf-interfaces:*` and `/ietf-nat:*`. Use the real embedded schema, private
sysrepo datastore, production publisher, and production reader. Do not add a
separate test of path-building helpers.

Run the focused Linux command from Task 1. Expected: the private sysrepo test
reads the base mode, interface association, NPTv6 prefix pair, and static
mappings that the input document configured.

## Task 3: Realize IPv6 policy and expose per-family readiness

**Repository:** `agoodkind/mwan`

**Files:**

- Modify `gateway/internal/ifmgr/modules/npt/npt.go`.
- Modify `gateway/internal/ifmgr/modules/npt/rules.go`.
- Modify `gateway/internal/ifmgr/modules/npt/applier.go`.
- Add `gateway/internal/ifmgr/modules/npt/algorithm.go` for RFC 6296 prefix validation,
  checksum adjustment, and address calculation.
- Add `gateway/internal/ifmgr/modules/npt/bpf/npt.c` and generated Go bindings for the
  traffic control translator.
- Add `gateway/internal/ifmgr/modules/npt/npt_namespace_test.go`.
- Modify `gateway/go.mod`, `gateway/go.sum`, and the build configuration for the eBPF compiler
  and loader dependencies.
- Modify `gateway/cmd/mwan/mwan-ifmgr@.service` to grant `CAP_BPF` with the existing
  network capabilities.
- Modify `gateway/internal/wanstate/wanstate.go`.
- Modify `gateway/cmd/mwan/wanconfig_livestate.go`.
- Modify `gateway/internal/ifmgr/modules/steering/` and the shared module environment
  that supplies family eligibility.
- Modify `gateway/internal/ifmgr/modules/wanroutes/` where it selects family routes and
  consumes the same family eligibility result.
- Modify `gateway/Makefile` to add the NPT namespace test to `make -C gateway test-netns`.

Replace the current fixed `/60` computation with the typed NPTv6 policy.
Delegated mode reads the live delegated prefix. Configured mode uses the
configured external prefix. Native mode creates no translation rule and
reports ready when no translation is required.

Replace the current stateful prefix rule with a traffic control eBPF program.
Attach destination translation to each WAN interface ingress point. The
kernel executes that program before connection tracking. Attach source
translation to each WAN interface egress point. The kernel executes that
program after routing, steering, and connection tracking. Both attachments
use the same resolved prefix pair and checksum adjustment.

Attach the destination program to each internal interface ingress point and
the source program to its egress point for RFC 6296 hairpin traffic. Ingress
translates the external destination to its internal address and records the
selected prefix pair in reserved packet-priority metadata. It also clears any
existing packet mark. Connection tracking therefore sees the internal source
and destination pair. Both prerouting rule owners must skip a packet carrying
this selector before DSCP pins, destination pins, connection-mark restore, or
random steering can assign another provider mark. Policy routing then uses the
verified internal-prefix route. Internal egress uses the selector to translate
the internal source to its external address, then clears the metadata. Both
directions use the translator, and each host sees the other host's external
address.

Do not use the Linux SNPT and DNPT targets. Those targets require `NOTRACK`.
MWAN requires connection tracking for connection marks, established-flow
affinity, and inbound reply symmetry. Do not disable connection tracking for
translated traffic.

The released binary embeds the compiled eBPF object, loads it, updates maps
for interface, direction, and prefix-pair selection, and reconciles the
traffic control links. An update must install the new program and map state
before removing the old attachment. Read back each attachment and map entry
before reporting the family ready. The installed systemd service grants
`CAP_BPF` for program and map loading and retains `CAP_NET_ADMIN` for traffic
control attachment.

Implement the complete RFC 6296 calculation. Equalize the prefix lengths,
zero-extend both prefixes to `/64`, calculate the one's-complement adjustment,
and select the correction word according to the configured prefix length.
Reject unsupported pairs before applying rules. Drop RFC-excluded addresses
and subnets. Include the member and both prefixes in every configuration
error.

Preserve outbound connectivity, inbound reachability, and the edge rewrite
policy for existing providers. The corrected checksum-neutral mapping may
produce a different external address than the current stateful NETMAP rule.
Source translation matches the outgoing interface and internal source. It
does not depend on a firewall mark.

Keep the existing stateful edge exceptions in the IPv6 NAT chains during this
epic. Exclude the internal edge addresses from egress NPTv6, then map them to
the external prefix's `::1` address in the existing source NAT chain. Exclude
that `::1` address and each configured external `/128` from ingress NPTv6,
then apply the existing destination NAT rules. Preserve the destination NAT
return guard so an edge exception is not translated twice.

Replace `wanstate.MemberTranslation` with per-family configured mode,
realization status, and one plain reason. Read back the kernel state before
reporting a translated family ready. Publish native mode as ready without a
kernel rule. Keep missing delegated-prefix alerts for delegated NPTv6 and
suppress that alert for native or configured-prefix modes.

Read back the route for the internal IPv6 prefix before enabling the internal
hairpin attachments. Report the hairpin path unavailable when that route does
not select the internal interface. Do not let a provider-table default route
satisfy this check.

Add one shared per-family eligibility result. The balancer, DSCP pins,
destination pins, provider-table default routes, and catch-all routes must all
read it. Remove the affected family route from an ineligible provider table so
a stale connection mark cannot continue selecting that path. If no member is
eligible, install no family default route and report the family unavailable.
Permit different active tiers for IPv4 and IPv6. MWAN-507 will add its routing
dependencies to this same decision.

Publish the resolved delegated prefix in `wanstate.Store` for MWAN-333. Keep
the current source-pin rule behavior in this epic. Do not claim that MWAN-333
is complete.

### Packet verification

Add `TestNPTNamespacePackets` under the existing `make -C gateway test-netns` entry point.
Use real namespaces, veth interfaces, routes, nftables, connection tracking,
TCP, UDP, and ICMPv6. Do not use mocked kernel operations. Cover these
outcomes:

1. IPv6 NPTv6 preserves outbound and return traffic with valid TCP and UDP
   checksums.
2. Native routed IPv6 preserves the source address and return traffic while
   ordinary IPv4 remains translated.
3. Unequal NPTv6 prefixes translate only within the configured external
   prefix.
4. A missing delegated prefix removes only that member's IPv6 eligibility and
   reports the reason.
5. Inbound NPTv6 succeeds after connection-tracking state is cleared and an
   RFC-excluded address is dropped.
6. DSCP-pinned, destination-pinned, stale-marked, and unmarked traffic all
   obey the same family eligibility result. IPv4 and IPv6 may select different
   active tiers.
7. Connection tracking remains enabled. A new flow keeps one provider, an
   inbound flow uses the matching return provider, and established packets
   preserve their connection mark.
8. The external `::1` edge address, configured external `/128` addresses, and
   the destination NAT return guard retain their existing behavior.
9. Two internal hosts complete TCP, UDP, and ICMPv6 exchanges through their
   external NPTv6 addresses without forwarding a packet to an ISP interface.
   Repeat the proof with each provider mark and pin input already present.
   Load the real steering rules, policy-routing rules, routes, and rendered
   production filter rules for this proof.

Run:

```bash
make -C gateway test-netns
```

Expected: all packet, checksum, stateless return, connection affinity, edge
exception, and eligibility outcomes pass through the real kernel boundaries.

## Task 4: Render typed policy in Configs

**Repository:** `agoodkind/configs`

**Files:**

- Modify `ansible/inventory/group_vars/mwan_servers.yml`.
- Modify `ansible/inventory/group_vars/mwan_suburban_servers.yml`.
- Modify `mwan/config/network.json.j2`.
- Modify `mwan/config/nftables.conf.j2`.
- Modify `spec/ansible/mwan_install_spec.rb` only for the public render and
  released-loader contract.

Convert every current provider explicitly:

- IPv4 uses NAPT44 masquerade with its current static mappings.
- IPv6 uses delegated NPTv6 with the current internal prefix and expected
  external prefix.
- Astound IPv6 uses native mode only when the family is enabled. Its initial
  IPv4-only deployment must not invent an IPv6 route or delegation.

Render the typed family policy into `network.json`. Stop emitting `npt-prefix`
and the provider-level `static-mapping` list.

Keep IPv4 realization in `nftables.conf.j2` until MWAN-341. Generate each
NAPT44 provider's static DNAT and SNAT rules from the typed mapping entries,
followed by the provider's masquerade rule. Generate no IPv4 translation rule
for native mode. Remove firewall-mark matches from source translation. Every
source rule must match the outgoing interface and internal source, so fallback
traffic receives the same translation.

Keep the IPv6 NAT chains that the daemon uses for the edge `/128` exceptions.
The eBPF translator owns prefix translation and does not require another
nftables chain or a kernel module declaration. MWAN-341 later transfers the
remaining IPv4 firewall writes and IPv6 chain creation. Do not transfer filter
rules, marking rules, or full ruleset repair into this change.

Add one forward-chain rule for translated NPTv6 hairpin packets. Match the
internal input and output interface plus the reserved packet-priority selector,
then accept the packet. Do not permit untagged internal-to-internal forwarding.
The internal egress program clears the selector before transmission. Leave
marks on non-hairpin traffic unchanged.

Add the matching selector guard at the start of the Configs prerouting mangle
chain. Clear the packet mark and stop before DSCP pins, destination pins, and
connection-mark restore. The daemon steering chain uses the same guard. This
ensures a stale mark or pin cannot select a provider table for hairpin traffic.

Add one Configs contract example that renders the testbed document and runs
the released `mwan deploy-gate check-network` command against it. Do not add
static template-content assertions or duplicate the schema rules in Ruby.

Run:

```bash
./configsctl lint
bundle exec rspec spec/ansible/mwan_install_spec.rb
```

Expected: Configs passes its lint gate, the rendered testbed document passes
the released loader, and the deploy still validates the document before the
management stack and daemon restart.

## Task 5: Release MWAN and pin the compatible testbed build

Finish the MWAN repository gates before creating the release:

```bash
make -C gateway check
make -C gateway test-netns
```

Expected: schema validation, Go checks, private sysrepo publication, and real
packet tests pass.

Merge the reviewed MWAN change, wait for its signed release, and verify the
downloaded archives and attestations. Pin the testbed release in
`ansible/inventory/group_vars/mwan_testbed_all.yml`. Keep the production pin
unchanged until testbed acceptance. Before the production deploy, pin
production to the accepted release in a separate change.

Do not deploy a new document with an older loader or a new loader with the old
document shape. `deploy-mwan.yml` already copies the rendered document to the
delegate, validates it with the pinned released loader, installs the document,
installs the binary, and restarts the daemon in that order.

## Task 6: Prove behavior on the testbed

**Repository:** `agoodkind/configs`

**Files:**

- Add a separate routed IPv6 simulator in the service mapping and suburban
  OpenTofu resources. Give it its own bridge, LXC, and MWAN WAN interface.
- Add the simulator and a native IPv6 provider to the testbed inventory.
  Preserve the Webpass, AT&T, Monkeybrains, and Astound simulator policies.
- The public Configs render test checks the routed simulator's return route
  and firewall rules.

Merge the simulator and testbed release pin changes before deployment. The new
bridge, LXC, and VM interface must exist before the simulator and gateway
playbooks run:

```bash
./configsctl tofu plan
./configsctl tofu apply
./configsctl deploy deploy-testbed --limit suburban
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
```

Capture packets at the ISP simulator ingress. Traffic beyond the simulator
does not prove which source address MWAN produced because the simulator also
masquerades traffic.

Run each load-balancing request as a separate `qm guest exec` call to the
testbed router. Record packets on both simulator ingress links for the entire
sample. Require one completed command and one distinct captured flow per
request. Rerun that family's sample when any command result or capture is
missing.

Record these acceptance results:

1. Current providers retain IPv6 NPTv6, IPv4 masquerade, and static mapping
   behavior.
2. A native routed IPv6 prefix works without DHCP prefix delegation.
3. Native IPv6 and ordinary translated IPv4 work at the same time.
4. Fallback IPv4 uses the selected provider's source translation even when a
   stale or absent mark exists.
5. Removing a required delegated prefix excludes only that provider's IPv6
   path, records the reason, and restores eligibility after the prefix returns.
6. Inbound return traffic works for NPTv6 and static mappings.
7. Native IPv4 installs no translation rule and preserves the source address.
8. NPTv6 return traffic succeeds without connection-tracking state, and TCP
   and UDP checksums remain valid.
9. The installed `mwan-ifmgr@wan` service loads and attaches the embedded eBPF
   translator with its configured service capability set.
10. Two internal testbed clients use each other's external NPTv6 addresses
    without sending packets to an ISP simulator.
11. With AT&T and Webpass eligible in the same tier, send 100 fresh unmarked
    downstream flows per family. Capture each selected provider at the gateway
    before simulator masquerade and confirm every reply. Each provider must
    receive 35 to 65 of 100 flows when weights are equal. Verify the
    distribution by comparing captured counts against the configured weights.

The testbed result can complete implementation acceptance. Record production
new-circuit exercises as deferred until after October 2026. Do not mark an
unperformed production exercise as passed.

## Completion evidence

MWAN-340 is ready to close after all of these facts are recorded:

- the released loader accepts the typed production and testbed documents;
- the private sysrepo selftest publishes the configured policy and realized
  per-family state;
- `make -C gateway check` and `make -C gateway test-netns` pass on the release commit;
- Configs lint and the focused public render contract pass with the release
  pins;
- testbed ingress captures prove translated and native source addresses,
  stateless return traffic, fallback translation, checksum validity, and
  per-family exclusion;
- internal captures prove RFC 6296 hairpin translation in both directions;
- testbed captures prove that the gateway assigns fresh IPv4 and IPv6 flows
  to both eligible providers at the configured weights and each flow receives
  a reply;
- the installed service proves the deployed capability set can load and attach
  the embedded translator;
- current providers retain connectivity and inbound policy, with any changed
  RFC 6296 external IPv6 address recorded before production activation;
- production new-circuit validation remains explicitly scheduled after
  October 2026.
