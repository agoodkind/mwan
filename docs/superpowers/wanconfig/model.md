# WANCONFIG model

The gateway uses one interface list for provider, tunnel, internal, and
management links. The [MWAN steering module](../../../gateway/internal/yangpub/schema/goodkind-mwan-steering.yang)
extends the IETF models with provider selection, interface ownership, link
configuration, and operational state.

## IETF models

- RFC 8343 defines the interface list. Each entry has an interface name,
  type, enabled setting, and operational status.
- RFC 8344 adds IPv4 and IPv6 settings to each interface. Each family has
  its own addresses, neighbors, and forwarding setting. IPv6 also has
  router discovery and address autoconfiguration settings.
- RFC 8349 defines control plane protocols and routing tables.
- RFC 8512 defines typed NAT instances, including NAT44, NAPT44, and NPTv6.
  NAPT44 translates IPv4 addresses and ports, and NPTv6 translates IPv6
  prefixes. The NAT model provides a mapping table for static entries and
  prefix pairs for NPTv6.

## Provider selection

The `steering` container marks an interface as a member. The `tier` leaf sets
its preference. The `weight` leaf assigns its relative share within a tier
and accepts only positive values. The group's `hash-mode` selects random
assignment, source address hashing, or source and destination address hashing.

The `enabled` setting controls whether a member participates in selection.
Disabling selection does not stop address acquisition, translation, or
interface ownership. The `probe-policy` leaf identifies the member's health
policy in the published model. The daemon combines probe results into a
member health verdict and retains evidence for each address family.

Steering checks route and translation readiness separately for IPv4 and
IPv6. For each family, it selects the lowest numbered tier with an eligible
member. It assigns new connections among that tier's eligible members using
their weights. Connections that already have a mark can keep it while the
path remains eligible.

The gateway requires unique provider routing tables, firewall marks, and
policy rule priorities. It rejects provider routing tables that Linux reserves
and tables that the network document lists in `reserved-tables`.

## Interface ownership and acquisition

The `connection-id` leaf identifies a connection independently of its
provider name or BGP autonomous system number. The `owner` leaf identifies
the component that configures the interface. Its values are `mwan`,
`networkd`, and `external`.

The link model includes device matching, VLAN parent and tag, and tunnel
endpoints. The `link-files` leaf distinguishes files that MWAN renders from
files an operator supplies. The `networkd` container supports ordered
sections and entries for link, network, and netdev files. The YANG model
validates their structure. Networkd interprets their settings.

The address family model defines DHCP settings, resolver settings, and
routes. The IPv6 model also defines router discovery and delegated prefix
settings. Operational ownership state reports the observed device, address
and route assignments, lease storage results, the last apply result, and
readiness. A stored lease does not establish forwarding readiness.

The `route` list accepts one entry per destination for each connection and
address family. Omitting a next hop creates an on-link route. Configured
routes use the Linux main table. The address family's gateway and metric
settings also provide default route shorthand. A connection cannot define
a default in both forms. The loader checks IPv6 metric collisions using
Linux's effective metric of 1024 when the configured metric is zero.

## Direct connections and tunnels

A WAN can use configured routes or BGP sessions. A session can use a
physical connection or a tunnel. The peer address family and the route
family are separate settings. WANCONFIG provider BGP sessions exchange
IPv6 unicast routes. An independent IPv4 path can remain eligible when
an IPv6 session loses its routes.

The tunnel model supports 6in4, which transports IPv6 packets within IPv4,
and VXLAN, which transports Ethernet frames over UDP and IPv4. The
`underlay` leaf identifies the connection that forwards the outer packets.
When the underlay has an IPv4 gateway, MWAN installs the tunnel endpoint
route in the underlay's provider table. A BGP peer across the tunnel can
be the tunnel endpoint or a router farther along the path.

Each provider BGP session has local and remote autonomous system numbers.
Sessions can share a number. The import policy matches IPv6 prefixes and
length ranges. The export policy sets the prefixes and attributes for a
session, including communities and path preference values. An export in
`backup` mode lists the connections it backs up. MWAN advertises that
prefix when the local IPv6 path is ready and none of those connections has
a ready IPv6 path.

The operational model reports the session state and any kernel route
prefixes that MWAN retains during a negotiated graceful restart. The
restarted session refreshes those routes as it receives new announcements
and removes routes the peer does not announce again. Without negotiated
graceful restart, MWAN removes the session's routes during shutdown.

## Translation

Each address family can select native routing, which preserves packet
addresses. IPv4 supports NAPT44 masquerade. IPv4 static mappings take
precedence over NAPT44 masquerade and specify whether the external
address is local to the gateway or routed to it. IPv6 supports masquerade
and NPTv6.

NPTv6 uses an internal prefix and a configured or delegated external
prefix. The BPF translator replaces the address prefix and adjusts an
address word to preserve the transport checksum. When prefix lengths
differ, the translator uses the longer length and only translates the
corresponding portion of the shorter prefix. An external delegation must
have enough space for the required prefix. A path that requires NPTv6
needs usable translation state before steering can select it for IPv6.

## Operational notifications

The `health-transition` notification identifies the interface and gives
the old and new combined health verdicts after the configured thresholds
confirm a change. The `tier-change` notification gives the old and new
published active tier.

Libyang validates the models and network documents. The daemon publishes
configuration and operational values through sysrepo. Rousette serves
those values through the read-only RESTCONF interface.
