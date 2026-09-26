> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Four: translation becomes typed instances

Each address family of each provider declares how its traffic is translated,
or that it is not translated at all. IPv4 static mappings compose with the
family's default masquerade. IPv6 NPTv6 maps one prefix to another one to one.
Prefixes of differing length follow the standard's rule instead of a
hardcoded length.

Depends on the provider set being data, because an instance attaches to a
member.

## The problem this solves

IPv6 has exactly one translation behavior and no way to express another.
Every provider gets the same prefix-translation rule set, built
unconditionally, with no type field anywhere. A provider with no readable
delegation is dropped from the translation table entirely, so a statically
routed prefix, a plain address masquerade, and native routing without
translation are all inexpressible.

The exclusivity is already load-bearing and recorded only as a comment in the
firewall template, warning that a static masquerade on one provider would
override prefix translation through rule ordering. Two behaviors that cannot
coexist, with nothing in configuration saying which applies, is the shape of
a bug waiting for a fourth provider.

IPv4 has the mirror gap. Static one-to-one mapping exists as a separate
provider field, outside any typed per-family translation policy. The firewall
template combines it with masquerade through rule order.

## What each family declares

Each family on each member declares one base mode: native, NPTv6, or NAPT44
masquerade. Native mode forwards addresses unchanged. NPTv6 translates one
IPv6 prefix into another. NAPT44 masquerade translates IPv4 sources to the
address of the outgoing interface.

Static one-to-one mappings are entries within the IPv4 NAPT44 configuration.
They are not a fourth exclusive mode. A matching static mapping takes
precedence over masquerade. This composition preserves the current IPv4
behavior and follows RFC 8512's mapping-table structure. NPTv6 already
provides stateless one-to-one translation for every address in its configured
prefixes, so it needs no separate static mapping table.

A member whose mode is native routes that family without translation and
raises no missing-delegation alert. Today the absence of a delegation is
always an error; under the model it is only an error for a member that
declared delegated NPTv6.

IPv4 static mappings become entries in the member's typed NAPT44 policy. The
implementation selects them through the containing interface and family.

## Direct and tunneled IPv6 routing

Apply the same translation choices to physical and tunnel interfaces.
An IPv6 prefix routed to the gateway can use no translation whether routes
are configured or learned through BGP. BGP exchanges routes between routers;
it does not allocate a prefix through DHCP. Require a DHCP delegation only
when the selected translation configuration depends on one.

Test untranslated IPv6 forwarding and return traffic through direct and
tunnel interfaces while ordinary IPv4 translation continues independently.
Remove a required IPv6 route and verify that steering excludes the affected
IPv6 path without disabling working IPv4. Reuse the shared path eligibility
work instead of adding a separate BGP translation model.

## Prefixes of differing length

Apply the complete RFC 6296 algorithm. First extend the shorter prefix with
zeroes until both prefixes have the same length. Then extend both to `/64`,
calculate the one's-complement checksum adjustment, replace the prefix, and
apply the adjustment to the address word the RFC selects. Do not force either
prefix to a fixed length.

The translation is stateless. An inbound packet reverses the address mapping
without a prior outbound packet or a connection-tracking entry. The translated
address keeps the same transport pseudo-header checksum as the internal
address.

Internal clients can use each other's external addresses. For that case, the
translator maps the packet's internal source to its external address and maps
its external destination to the peer's internal address before forwarding the
packet internally. Both directions use the translator for the entire session.

For prefixes of `/48` or shorter, apply the adjustment to bits 48 through 63.
For longer prefixes, inspect the four 16-bit interface-identifier words in
order and use the first word that is not `0xffff`. Drop addresses and subnets
that RFC 6296 excludes instead of creating another mapping.

The current implementation forces the delegation to a fixed length regardless
of what was delegated, with no upper bound check, so a provider delegating a
shorter prefix produces translation onto address space the gateway does not
hold. Only one of the four delegation probes filters by length, and it is not
the one that runs first.

Reject an unsupported prefix pair before programming rules, and report the
member and both prefixes. Do not call a stateful NETMAP rule NPTv6. A stateful
rule depends on connection tracking and does not implement RFC 6296's checksum
adjustment.

## Translation never matches a firewall mark

Source translation keys on the outgoing interface and the internal source
address, in both families. The outgoing interface already determines the exit
path, so a mark adds nothing to the match and removes the rule's ability to
handle traffic whose mark is stale.

This is not a change for IPv6, whose rules already key on the interface
alone, and that property must survive the move to typed instances. It is a
change for IPv4, where two of three rules require a matching mark today.
Internal IPv4 traffic carries only the marks the balancer assigns, which are
the top tier's, and steering sends that traffic out an activated fallback
member without rewriting the mark. A mark-scoped rule would not match, the
traffic would leave with a private source address, and IPv4 would fail at the
moment fallback exists to prevent it. Neither the deploy gate nor a normal
testbed run exercises that path, because fallback is not active during a
deploy.

## Realization is coupled to steering

A member whose family cannot translate stops receiving that family's traffic.

Today there is no such coupling. A provider with no delegation is dropped
from the translation table while steering keeps its rules and health still
passes it on the other family's probes, so half the internal IPv6 flows leave
with an untranslated source and are discarded upstream. The only signal is a
warning alert.

The per-family operational status the read-only surface already exposes is
where this coupling becomes visible: a family whose instance is not realized
reports down, and steering reads that.

## Inbound survives every mode that can support it

A member that declares inbound reachability keeps it: the reverse translation
for prefix translation, the mapping entries for one-to-one, and the edge
address rewrite. Address masquerade is outbound only by nature, and a member
declaring it declares no inbound reachability, which the model states rather
than leaving to be discovered.

## Acceptance

Each base mode is expressible and produces the rules it should. IPv4 static
mappings compose with NAPT44 and take precedence for their configured
addresses. NPTv6 translates every address in its configured prefixes one to
one.

For the current provider set, IPv4 packet behavior is unchanged. IPv6 keeps
the same connectivity and inbound reachability policy. RFC 6296 checksum
adjustment can change the non-prefix portion of an external IPv6 address
compared with the current stateful NETMAP result. Record every externally
published IPv6 address that needs a configuration or DNS update before a
production cutover.

A member with a delegation shorter than the internal prefix translates
correctly under the standard's rule rather than onto space the gateway does
not hold.

An inbound packet reverses NPTv6 after connection-tracking state is cleared.
TCP and UDP packets retain valid transport checksums. An address that RFC 6296
excludes is dropped with the required ICMPv6 error where the kernel supports
it.

Two internal clients can complete TCP, UDP, and ICMPv6 exchanges through their
external addresses without forwarding a packet to an ISP interface.

A member whose IPv6 cannot be realized receives no IPv6 traffic, and its
per-family status reports down.

Outbound traffic is translated while a fallback tier is active, in both
families. This is the case the mark rule change exists for, and it must be
exercised deliberately.

## Failure modes

The testbed provider simulators masquerade what they receive on the way out,
so nothing observed beyond a simulator proves the gateway produced the right
source address. Observe at the simulator's ingress.

Loosening the missing-delegation alert to apply only to members that need one
must not silence it for members that do. The alert is the only signal today
that a provider's IPv6 has quietly stopped working.
