> Read the [repository context](../../README.md) before using copied commands or historical plans.

# The model

The gateway describes itself with published IETF models. Only the steering
concepts have no published model, so they live in one local module that
extends the others. This page is the single home for the model's shape; the
five specifications express their behavior in its terms.

## What the published models give

**Interfaces.** RFC 8343 defines an `interfaces` container holding an
`interface` list keyed by name, with `type`, `enabled`, and operational
status. Every provider link, the internal link, and the management link are
entries in that one list.

**A container per address family.** RFC 8344 augments each interface with an
`ipv4` container and an `ipv6` container. Each carries `enabled`,
`forwarding`, `mtu`, an `address` list, and a `neighbor` list; the IPv6
container adds an `autoconf` container. This is the single most valuable
thing the model brings. Every asymmetry in the current system exists because
there is nowhere per-family to put behavior, so behavior landed wherever it
was first needed. With these containers, treating the two families alike is
structural rather than a thing to remember.

**Translation as a typed instance.** RFC 8512 defines a `nat` container
holding an `instances/instance` list, each with a `type` drawn from
identities including `napt44`, `basic-nat44`, `dst-nat`, and `nptv6`. An
instance carries a `policy`, which for prefix translation holds
`nptv6-prefixes` as an explicit internal and external pair, and a
`mapping-table` whose entries may be `static`. The local interface model
declares native, NPTv6, or NAPT44 as each family's base mode and references
the applicable RFC 8512 data. IPv4 static mappings are entries within NAPT44,
not an exclusive mode. NPTv6 itself maps the configured IPv6 prefixes one to
one. Native mode is explicit because RFC 8512 models NAT instances and has no
instance type for unchanged forwarding.

**Routing.** RFC 8349 defines control-plane protocols and routing tables,
which covers the per-provider tables and the learned routes.

## What the local module adds

One module extends the interface list with the steering properties no
published model covers:

- `tier`, a small integer. Lower is preferred.
- `weight`, a positive integer, for unequal balancing among members of one
  tier. Validation rejects zero and negative values, so the slot sum the
  balancer divides by is never zero.
- `probe-policy`, naming the health probes that decide the member's state.
  The completed migration uses a combined verdict. The planned extension
  evaluates IPv4 and IPv6 separately before selecting a forwarding path.

Alongside the interface list it adds the group's `hash-mode`, selecting how a
new connection is assigned to a member: at random, by source address, or by
source and destination.

A member is an interface with steering properties, not a parallel object.
That keeps one identity per link.

The module also gives each interface what the network manager needs to
bring it up and the published models do not name. Typed leaves carry how
the device is matched (by driver or by hardware address), the hardware
address, whether each family runs a DHCP client, the delegation client's
identity and hint, whether the delegation is solicited without a router
advertisement, whether the delegated prefix is applied to the link, the
downstream router lifetime, whether router advertisements are accepted, the
route metric, an optional VLAN parent with its tag, and any extra addresses
the provider's source pin covers beyond the link's own. One leaf states
whether the interface's unit files are rendered from these leaves or
hand-authored, and an interface carrying a provider states one or the other.
A `networkd` container carries free-form unit-file sections, one ordered
list of sections for each of `link`, `network`, and `netdev`, each section
an ordered list of key and value pairs keyed by position, so a link shape
with no typed leaf is still expressible. The schema validates the free-form
structure and networkd validates its keys.

The module also carries the daemon settings that no published model covers
and the surface serves: the rollback watchdog's thresholds and probe
targets, the out-of-band access policy, and the tunnel tap. They are plain
configuration nodes with no steering semantics.

The module defines two notifications, a member health transition and an
active-tier change, each carrying the value before and after. How and when
the daemon sends them is the surface specification's streaming section.

## Interface ownership and acquisition

MWAN-305 extends this model with explicit interface ownership, acquisition
lifetimes, applied state, and failure history. The
[interface ownership specification](../../interfaces.md) defines those
requirements and the migration from networkd. The unit-rendering fields
above support the current networkd stage. They do not define the final
interface lifecycle. MWAN-507 uses the shared ownership and dependency
contracts when adding tunnels and external BGP.

## Direct connections and tunnels

The provider-set migration is complete. MWAN-507 requires direct and tunneled
IPv6 routing. Those external routing capabilities are not implemented yet.

A tunnel encapsulates packets for transport between routers. The Provider terms
table defines BGP, VPS, ASN, and prefix.

All three arrangements use the same interface and routing model. Selecting a
remote provider changes configuration and acceptance tests, not the MWAN
architecture.

| Arrangement | MWAN behavior | Remote behavior |
|---|---|---|
| A tunnel uses configured routes. | MWAN forwards IPv6 through the tunnel without a BGP session on that tunnel. | The remote router forwards return traffic through a configured route and exchanges BGP routes with its upstream provider. |
| A tunnel connects MWAN to an upstream BGP peer. | MWAN exchanges routes with that upstream router through the tunnel. | The provider terminates the tunnel, or an intermediate VPS forwards packets to the agreed peer. The provider must support that session arrangement. |
| A tunnel connects MWAN to a VPS BGP peer. | MWAN exchanges routes with the VPS through the tunnel. | The VPS runs a separate BGP session with its upstream provider and applies policy between the sessions. |

Every arrangement transports ordinary IPv6 packets through the tunnel.
Arrangements with a local BGP session also transport BGP protocol packets.
A default route handles destinations without a more specific route and can
be configured or learned through BGP. A full table supplies the provider's
available internet destination routes. Receiving one is a separate policy
and capacity decision.

Etheric uses the same BGP implementation directly on its physical connection.
Sonic and Astound first operate as ordinary MWAN providers, like the current
Webpass and AT&T connections. Their onboarding, IPv4 service, and any native
IPv6 service do not depend on a tunnel, ASN, portable prefix, or external BGP.
A later tunnel depends on one of those working provider connections and
provides an additional IPv6 forwarding path without removing the ordinary
provider path. Failure of that tunnel or its BGP session removes only the
dependent IPv6 path. A second Sonic circuit has its own connection identity.
Configuration permits repeated ISP names and local or remote ASNs across
connections. Interface identities and allocated routing identifiers must be
unique.

The first deployment exchanges only IPv6 routes through external BGP. IPv4
keeps ordinary ISP addressing, routing, translation, and steering. The
address family of the tunnel's outer packets does not determine the address
families supported inside it. The BGP session's transport addresses likewise
do not select the route families it exchanges.

The design does not depend on a known production IPv6 prefix. Configuration
supplies one or more prefixes when they become available and states which
sessions may advertise each prefix. The model must not embed a specific
prefix, prefix length, provider, or ASN. Tests use documentation prefixes and
prove the same behavior with different values. A production deploy requires
the selected upstreams to authorize the configured prefix, but obtaining that
authorization is not an implementation prerequisite.

WireGuard is excluded from MWAN-507. Additional encryption is not required.
The selected tunnel protocol must demonstrate the required throughput;
unencrypted encapsulation alone does not prove line-rate performance. The
production protocol, provider endpoints, packet-size limit, and throughput
target remain open until the service requirements are known. The first
testbed tunnel uses 6in4 without selecting the production protocol.

## Persistent tunnel and BGP simulators

MWAN-507 requires two simulator prerequisites: IPv6 over the IPv4-only Astound
connection without BGP, followed by real BGP peers for Etheric and the three
tunnel arrangements. These are planned capabilities, not deployed acceptance.
Use thin Linux containers (LXCs) with real kernel networking and routing
software. Declare the guests and networks in Configs OpenTofu and configure
them through Ansible. Preserve existing provider and downstream-client roles.

Keep every scenario provisioned at the same time. Give each tunnel variant
its own endpoint LXC, stable endpoint address, and scenario identity. Select
a variant through MWAN configuration rather than rewriting a shared remote
router or destroying the previous scenario. Reuse templates and packages
across the independent instances.

Selecting a scenario applies its endpoint, tunnel settings, configured routes,
peer settings, and routing policy as one compatible configuration. Changing
only an endpoint address is insufficient when the peer arrangement changes.
Use the supported deployment mechanism; this requirement does not depend on
management writes or hot reload under MWAN-440. Keep unselected scenarios
available without making their routes eligible on MWAN.

### Astound tunnel without BGP

Restore the ordinary managed Astound connection under MWAN-524 first. Keep
that simulator IPv4-only. Configure a separate remote LXC as a 6in4 endpoint,
which receives IPv6 packets encapsulated inside IPv4 packets. Configure IPv6
routes in both directions without BGP on either endpoint or upstream.

Verify traffic between a downstream client and a remote test client. Prove
that outer packets use Astound, ordinary IPv4 still works, packet-size errors
are handled, and forwarding recovers after tunnel restart. Preserve this
no-BGP scenario when adding the BGP variants. The testbed must forward the
actual encapsulated packets across the simulated ISP; a direct bridge between
tunnel endpoints does not prove the ISP path.

### Etheric and tunnel BGP scenarios

Run real BGP implementations on the simulated routers. Give each BGP scenario
its own upstream-router LXC. Keep each VPS separate from its upstream and
isolate each scenario's test networks. A failed upstream in one scenario must
not disable another scenario. Provide a remote test client beyond the upstream router and
use the existing downstream clients without an out-of-band forwarding bypass.

| Scenario | Required behavior |
| --- | --- |
| Etheric provides native BGP. | MWAN peers directly with a dedicated Etheric router LXC without a tunnel. |
| Tunnel scenario 1 uses configured home routes. | MWAN uses configured IPv6 routes without a local BGP session. The VPS has a configured return route home and peers with its upstream. |
| Tunnel scenario 2 uses an upstream peer. | MWAN peers with the upstream router through the tunnel and forwarding VPS. The VPS does not terminate that BGP session. |
| Tunnel scenario 3 uses a VPS peer. | MWAN peers with the VPS through the tunnel. The VPS runs a separate upstream BGP session. |

Configure ordinary Sonic ISP transport separately from the remote tunnel and
BGP roles. Permit compatible tunnel scenarios over Astound as well. Configure
prefixes, local and remote ASNs, and peer addresses independently of scenario
identity. Support repeated ASNs and a second Sonic connection without merging
their identities. External BGP exchanges IPv6 routes; ordinary IPv4 remains
independent. Keep test announcements inside the testbed.

### Prerequisites and acceptance

Prepare the Astound endpoint before MWAN-510 tunnel and MWAN-511 configured
route acceptance. Prepare the Etheric and remote BGP routers before MWAN-509
and MWAN-512 acceptance. Simulator provisioning must not depend on those
feature tickets being complete. Infrastructure checks verify the remote
routers independently; feature acceptance then uses the production MWAN path.

Verify each scenario's permitted announcements, installed routes, downstream
traffic, and return traffic. Changing the selected variant must leave the
other simulator configurations intact. Concurrent availability of simulator
guests is distinct from selecting multiple MWAN paths. MWAN-513 verifies
combined path selection, withdrawal, balancing, and failure recovery after
the individual implementations pass. Reuse MWAN-522's applicable client and
packet-test infrastructure without making BGP a prerequisite for MWAN-305.

Record exact releases, configurations, selected scenario, observed packets,
and interruption and recovery results. A passing 6in4 test proves that
protocol only. Another tunnel protocol requires its own implementation and
acceptance; simulator success does not establish production throughput.

## Shared routing requirements

Represent physical and tunnel interfaces in the existing interface list.
Reference the underlying connection from each tunnel. Configure tunnel
creation separately from configured routes and BGP sessions. Reuse the
existing routing, steering, translation, and operational models.

Configure each BGP session's peer address, local ASN, remote ASN, supported
route families (IPv4 or IPv6), accepted routes, and advertised routes. Support
both same-ASN sessions (iBGP) and different-ASN sessions (eBGP) through that
shared configuration. Permit several connections to use the
same ASN. Preserve existing internal router sessions and their separate role.

Configure advertisement policy per prefix and session. The policy can
advertise a prefix through every eligible session, prefer some advertised
paths over others, or advertise a backup path only when its configured
condition becomes true. Provider-specific communities and other supported
BGP attributes are policy values. No provider, connection, or policy is the
hardcoded primary. Changing the production policy requires configuration and
deployment, not a binary change.

Install the route to the tunnel endpoint's remote address in the underlying
ISP provider table. The tunnel's routing table must not route its own endpoint.
Specify how route selection and connection weights interact before enabling
multiple paths. Only select paths that support the packet's source address
and destination. Use the translation specification for address changes.

Determine whether a path can forward traffic separately for IPv4 and IPv6.
Check its required physical link, tunnel, routes, probes, and translation.
Loss of an IPv6 BGP session must leave working ordinary IPv4 usable. Loss of
a physical circuit disables all paths that depend on that circuit. Expose
the failed dependency and recovery state through the existing served model.

The first release validates path readiness. A new external IPv6 path starts as
unavailable. It becomes eligible after its configured physical link and
optional tunnel are ready, its required routes exist, its BGP session is
established when configured, and one IPv6 forwarding probe succeeds through
that path. MWAN may then select the path and apply its advertisement policy.
Report `starting`, `ready`, or `unavailable` with one plain reason. Detailed
history records every readiness transition, its failed dependency, its
reason, and its time. Reuse the existing configurable failure and recovery
thresholds. Additional failure classification and recovery-time optimization
can follow without changing the configuration model.

A configured route alone does not prove that a remote router can still
deliver traffic. Test failure detection for configured routes as well as BGP
withdrawals. Define when a remote VPS stops advertising the home prefix if
all usable home paths fail. Operating that VPS is separate from MWAN support
and depends on the selected service.

A direct upstream peer receives MWAN withdrawals on its own session. When
MWAN peers with a VPS, the VPS must condition its upstream announcements on
a usable home route. A permanent announcement must not attract traffic after
all home paths fail. If another home tunnel remains usable, the VPS may
select it and keep its upstream announcement. The VPS must also stop offering
a usable internet route to MWAN when its required upstream service fails.

Test forwarding loss while a BGP session remains established, session loss,
and ordinary process restart separately. Configure route retention during a
restart deliberately; a live session or retained route alone does not prove
that packets can be delivered. Measure outbound recovery and inbound recovery
separately. Do not assume identical timing for the two BGP arrangements.

## Provider terms

The following terms have these meanings in service descriptions. Sharing an
ASN does not by itself require every connection to advertise the same prefix.

| Term | Plain meaning |
|---|---|
| ISP circuit | An ISP circuit is a physical internet connection, such as one Sonic service. |
| Interface | An interface is a named physical or logical network connection on MWAN. |
| Tunnel | A tunnel encapsulates packets for transport between two endpoints. |
| Underlay | The underlay consists of the ISP connection and routes that forward the tunnel's outer packets to the remote endpoint. |
| Overlay | The overlay is the logical network provided by the tunnel. |
| Encapsulation | One packet is placed inside another packet for transport. |
| 6in4 | IPv6 packets are encapsulated inside IPv4 packets. |
| GRE | Generic Routing Encapsulation supports several inner protocol types. GRE does not provide encryption by itself. |
| IP-in-IP | An IP packet is encapsulated inside another IP packet. Confirm the provider's exact inner and outer address families; IPIP often specifically means IPv4 inside IPv4. |
| VPS | A virtual private server can run routing software when the hosting provider permits it. |
| Upstream or transit provider | The provider supplies routes and forwards traffic to other networks. |
| Prefix | A prefix is an address block written in slash notation, such as an IPv6 /48. |
| ASN | An autonomous system number identifies a routing network under one administration. |
| BGP session | Two configured routers exchange route announcements using Border Gateway Protocol. |
| BGP peer | A peer is the other router participating in a BGP session. |
| iBGP | The two BGP peers use the same ASN. |
| eBGP | The two BGP peers use different ASNs. |
| Native BGP | The session uses the ISP connection directly without the additional remote tunnel. |
| Import policy | The policy selects which received routes MWAN accepts. |
| Export policy | The policy selects which routes MWAN advertises to a peer. |
| Route withdrawal | A router stops advertising a previously announced route. |
| Default route | The router uses this route when no more specific destination route matches. The IPv6 default is ::/0. |
| Full table | The provider supplies its available internet destination routes instead of only a default. |
| Next hop | This router receives the packet for the next forwarding step. |
| Multihop BGP | The peer is beyond a directly connected link, and session configuration permits the intervening router hops. |
| MTU | The maximum transmission unit limits packet size on an interface. Tunnel headers consume part of the underlying packet-size allowance. |
| Translation | The gateway changes packet addresses, such as replacing a private IPv4 source with an ISP address. |

## What today's behavior becomes

Each row is a current special case and the model element that replaces it.

| Today | In the model |
|---|---|
| IPv4 and IPv6 translation are different code paths in different components | two instances on one interface, types `napt44` and `nptv6` |
| IPv4 static mapping is separate from the family's translation type | `mapping-table` entries of type `static` on the interface's NAPT44 instance |
| No way to express address masquerade, a statically routed prefix, or no translation | the family's explicit base mode: NAPT44, NPTv6, or native |
| A delegation is forced to a fixed length, and a shorter one is widened onto space the gateway does not hold | both prefixes are explicit leaves, and RFC 6296 defines the rest |
| Nothing checks the two prefix lengths against each other | both are modeled, so the check is schema-level |
| The load balancer is hardcoded in three expressions and cannot select the fallback provider | derived from the member list of the active tier |
| A provider with no delegation keeps receiving IPv6 and discarding it | the IPv6 container's operational status is down when its instance cannot be realized |
| The health verdict merges both families | MWAN-507 evaluates each family and its required link, routing, and translation state separately. |
| Forwarding is enabled globally before any firewall exists | the `forwarding` leaf, per interface per family |
| The rollback watchdog's probe list is hand-maintained and omits one provider | the daemon pushes its verdict to the watchdog, which holds no list |
| Four routing identifiers are hand-assigned per provider | still typed per provider, and checked for uniqueness and collision at load |

## Prefixes of differing length

RFC 6296 does not require the internal and external prefixes to match. The
translation function first extends the shorter prefix with zeroes until both
prefixes have the same length. It then computes a one's-complement adjustment
from the two prefixes and applies that adjustment to a selected 16-bit address
word. This produces a stateless, checksum-neutral mapping. A stateful NETMAP
rule that only replaces prefix bits does not satisfy this contract.

The current implementation forces the delegation to a fixed length and uses a
stateful NAT prefix rule. A shorter delegation is widened onto address space
the gateway does not have, and the rule does not apply RFC 6296's checksum
adjustment. The model includes both prefixes explicitly, so the implementation
can calculate the complete mapping.

## Toolchain

libyang reads and validates the modules, and its validator, `yanglint`, is
the build gate. sysrepo holds the datastore the daemon publishes into.
rousette serves RESTCONF over that datastore, and netopeer2 serves NETCONF
from the same stack when wanted. The stack ships as Debian packages that
every mwan release builds and proves in a container, so a gateway installs
them with apt and never compiles.

The one piece that is ours is the publishing binding: a small Go-to-C layer
through which the daemon sets values by path and applies them. It carries no
write acceptance, no validation, and no transaction machinery, because the
surface accepts no edits.

The Go-only alternatives were probed and set aside. The single-dependency
library fails to parse the translation module at its only tagged release,
and the mature Go parser has no server, so pairing it with a served surface
means writing the protocol layer ourselves, which this work does not do.
