# MWAN packet example

This example illustrates provider selection and address translation. The
addresses and names are not production configuration. IPv4 ISP blocks use
TEST-NET space. Public IPv6 blocks use `2001:db8::/32`. The internal
`fd06::/56` block is an example unique local address prefix.

| Provider | IPv4 block | IPv6 prefix | Role |
| --- | --- | --- | --- |
| ISP-1 | `192.0.2.0/29` | `2001:db8:1::/56` | First preference tier |
| ISP-2 | `198.51.100.0/29` | `2001:db8:2::/56` | First preference tier |
| ISP-3 | `203.0.113.0/29` | `2001:db8:3::/56` | Later tier and backup uplink |

A client with source `fd06::10` sends an IPv6 packet through OPNsense. MWAN
selects ISP-1 and translates the source from `fd06::/56` into
`2001:db8:1::/56`. Network Prefix Translation (NPTv6) adjusts an address
word to preserve transport checksums, so replacing the prefix text alone
does not calculate the exact external host address. MWAN reverses the
translation for replies.

For IPv4, OPNsense can translate client `10.0.0.10` to its transit address
`192.0.2.9`. A static mapping on ISP-2 can then translate that source to
`198.51.100.1`. Without that mapping, ISP-2 masquerades the transit source.

A LAN normalization rule can make OPNsense stamp DSCP CS1 before it forwards
a packet. If ISP-1 is configured to match CS1, MWAN selects ISP-1 before
balancing unmarked connections. The DSCP setting survives the router's IPv4
source translation.
